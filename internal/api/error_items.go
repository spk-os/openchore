package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/liftedkilt/openchore/internal/llm"
	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// L1 error items / wrong-question loop. The parent posts a completion's
// photographed paper to /admin/analyses; the two-stage parse runs in the
// background (segment the page, judge each question) with progress
// persisted on the paper_analyses row the parent polls. Extracted wrong
// questions land in error_items with their knowledge tags; the kid's
// review flow moves the three-state mastery machine along. Routes:
//   POST /api/admin/analyses                     start the async parse
//   GET  /api/admin/analyses/{id}                poll progress/report
//   GET  /api/error-items?user_id=&mastery=&subject=   the wrong-question book
//   GET  /api/error-items/{id}                   one item with tags
//   POST /api/error-items/{id}/review-result     kid/parent reports the retry outcome
//   POST /api/admin/error-items/{id}/correction-task  write a correction chore

// maxPaperPages caps how many proof photos one analysis parses — a 9-photo
// submission is at most a few exam pages plus answer sheets.
const maxPaperPages = model.MaxImageAttachments

// getQuestionText and getConfidence give the item-building code uniform
// accessors on a judgement that may be nil (the degrade path).
func judgementQuestionText(j *llm.QuestionJudgement) string {
	if j == nil {
		return ""
	}
	return j.QuestionText
}

func judgementConfidence(j *llm.QuestionJudgement) float64 {
	if j == nil {
		return 0
	}
	return j.Confidence
}

// startAnalysisRequest kicks off the whole-paper parse.
type startAnalysisRequest struct {
	CompletionID int64  `json:"completion_id"`
	Subject      string `json:"subject"`
}

// StartPaperAnalysis validates the completion, creates the async job row,
// and launches the two-stage parse in the background. The response is the
// job the parent polls — never the analysis itself (multimodal parses
// take minutes, not request lifetimes).
func (h *ChoreHandler) StartPaperAnalysis(w http.ResponseWriter, r *http.Request) {
	var req startAnalysisRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CompletionID == 0 {
		writeError(w, http.StatusBadRequest, "completion_id is required")
		return
	}
	completion, err := h.store.GetCompletion(r.Context(), req.CompletionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get completion")
		return
	}
	if completion == nil {
		writeError(w, http.StatusNotFound, "completion not found")
		return
	}
	if req.Subject == "" {
		req.Subject = "math"
	}
	ai := h.aiSvc.AI()
	if ai == nil {
		writeError(w, http.StatusServiceUnavailable, "AI is not configured")
		return
	}
	// The parent may have parked the whole L1 loop (flag-errorbook off):
	// the manual start button obeys the same switch as the auto path.
	if !h.errorbookEnabled(r.Context()) {
		writeError(w, http.StatusForbidden, "error analysis is disabled")
		return
	}

	// The paper belongs to whoever completed the chore — that's whose
	// error book the extracted items land in.
	parent := UserFromContext(r.Context())
	analysis := &model.PaperAnalysis{
		CompletionID: req.CompletionID,
		UserID:       completion.CompletedBy,
		CreatedBy:    parent.ID,
		Subject:      req.Subject,
		Status:       model.AnalysisPending,
		Stage:        model.StageQueued,
	}
	if err := h.store.CreatePaperAnalysis(r.Context(), analysis); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create analysis")
		return
	}

	h.analyses.Add(1)
	go func() {
		defer h.analyses.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		h.runPaperAnalysis(ctx, analysis.ID)
	}()

	writeJSON(w, http.StatusCreated, analysis)
}

// WaitForAnalyses blocks until background paper analyses finish (tests
// and shutdown).
func (h *ChoreHandler) WaitForAnalyses() {
	h.analyses.Wait()
}

// autoPaperAnalysisSetting gates the student-submission auto-parse: with
// it on (and AI configured), every photo completion the kid hands in is
// analyzed without the parent clicking anything — the user-facing
// contract 「学生拍照上交，自动分析出结果并学习与加强规则措施」.
const autoPaperAnalysisSetting = "ai_auto_paper_analysis"

// featureErrorbookSetting is the settings-l0l4 master switch for the L1
// error loop (flag-errorbook). Absent = on; only the literal "false"
// stops new parses — the popover's promise: existing errors and review
// cards stay browsable, nothing is deleted.
const featureErrorbookSetting = "feature_errorbook"

// errorbookEnabled reports whether new paper analyses may start.
func (h *ChoreHandler) errorbookEnabled(ctx context.Context) bool {
	on, err := h.store.GetSetting(ctx, featureErrorbookSetting)
	if err != nil {
		return true // unreadable setting must not brick the feature
	}
	return on != "false"
}

// queueAutoPaperAnalysis starts the async parse for a completion when the
// household opted in. Mirrors queueReview's never-block-the-kid shape:
// the check + create run in a goroutine so a slow settings read can't
// hold the complete response. Skips silently (no job row) when the
// feature is off, AI is absent or this completion already has an
// analysis — the parent's manual button stays the only path then.
func (h *ChoreHandler) queueAutoPaperAnalysis(completion *model.ChoreCompletion) {
	if h.aiSvc.AI() == nil || completion == nil || completion.CompletedBy == 0 {
		return
	}
	h.analyses.Add(1)
	go func() {
		defer h.analyses.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		on, _ := h.store.GetSetting(ctx, autoPaperAnalysisSetting)
		if on != "true" {
			return
		}
		// The L0-L4 master switch sits above the auto-parse opt-in: both
		// must be on for the zero-touch path.
		if !h.errorbookEnabled(ctx) {
			return
		}
		// One analysis per completion: a re-check after an uncheck must not
		// re-run the (expensive) parse on the same photos.
		prior, err := h.store.ListPaperAnalysesForCompletion(ctx, completion.ID)
		if err != nil || len(prior) > 0 {
			return
		}
		// created_by: the system user of the auto path is the kid who
		// handed the paper in (the manual path records the parent).
		analysis := &model.PaperAnalysis{
			CompletionID: completion.ID,
			UserID:       completion.CompletedBy,
			CreatedBy:    completion.CompletedBy,
			Subject:      "math",
			Status:       model.AnalysisPending,
			Stage:        model.StageQueued,
		}
		if err := h.store.CreatePaperAnalysis(ctx, analysis); err != nil {
			log.Printf("auto-analysis: creating job for completion %d: %v", completion.ID, err)
			return
		}
		log.Printf("auto-analysis: queued completion %d as analysis %d (ai_auto_paper_analysis=on)", completion.ID, analysis.ID)
		h.runPaperAnalysis(ctx, analysis.ID)
		// Learn from what was parsed: distill the fresh report into active
		// measures right away (the 「学习与加强规则措施」 half of the loop).
		// Best-effort — a distill failure never fails the analysis.
		done, err := h.store.GetPaperAnalysis(ctx, analysis.ID)
		if err != nil || done == nil || done.Status != model.AnalysisDone {
			return
		}
		ai := h.aiSvc.AI()
		if ai == nil {
			return
		}
		if _, err := h.distillMeasuresForAnalysis(ctx, ai, done, completion.CompletedBy); err != nil {
			log.Printf("auto-analysis: distilling measures for analysis %d: %v", analysis.ID, err)
		}
	}()
}

// analysisQuestion is the per-question summary the diagnosis report
// renders: one row per cut question with the AI's verdict.
type analysisQuestion struct {
	Number          string   `json:"number"`
	QuestionText    string   `json:"question_text"`
	Attempted       bool     `json:"attempted"`
	IsWrong         bool     `json:"is_wrong"`
	WrongAnswerText string   `json:"wrong_answer_text,omitempty"`
	AnswerText      string   `json:"answer_text,omitempty"`
	MistakeAnalysis string   `json:"mistake_analysis,omitempty"`
	ErrorType       string   `json:"error_type,omitempty"`
	KnowledgePoints []string `json:"knowledge_points,omitempty"`
	Confidence      float64  `json:"confidence"`
	ErrorItemID     *int64   `json:"error_item_id,omitempty"` // set when a wrong item was recorded
}

// analysisSummary is the diagnosis report JSON stored on the analysis row
// once the parse finishes: the per-question verdicts plus the aggregates
// the report page renders (error-type mix, weak knowledge points).
type analysisSummary struct {
	Questions       []analysisQuestion  `json:"questions"`
	ErrorTypeCounts map[string]int      `json:"error_type_counts"`
	WeakTags        []store.WeakTagStat `json:"weak_tags"`
	Notes           []string            `json:"notes,omitempty"`
}

// runPaperAnalysis is the background two-stage parse. Progress is
// persisted at every stage boundary so the parent's progress bar moves
// even on a slow model; any failure marks the job failed with the error —
// never a half-recorded success.
func (h *ChoreHandler) runPaperAnalysis(ctx context.Context, analysisID int64) {
	analysis, err := h.store.GetPaperAnalysis(ctx, analysisID)
	if err != nil || analysis == nil {
		log.Printf("analysis: loading job %d: %v", analysisID, err)
		return
	}
	completion, err := h.store.GetCompletion(ctx, analysis.CompletionID)
	if err != nil || completion == nil {
		_ = h.store.FinishPaperAnalysis(ctx, analysisID, model.AnalysisFailed, "", "completion not found", 0, 0)
		return
	}

	// The proof set to parse: image attachments, falling back to the
	// legacy single photo_url when the completion predates L0.
	pages, err := h.analysisPagePaths(ctx, analysis.CompletionID, completion.PhotoURL)
	if err != nil {
		_ = h.store.FinishPaperAnalysis(ctx, analysisID, model.AnalysisFailed, "", err.Error(), 0, 0)
		return
	}
	if len(pages) == 0 {
		_ = h.store.FinishPaperAnalysis(ctx, analysisID, model.AnalysisFailed, "", "no image proof on this completion", 0, 0)
		return
	}

	ai := h.aiSvc.AI()
	if ai == nil {
		_ = h.store.FinishPaperAnalysis(ctx, analysisID, model.AnalysisFailed, "", "AI is not configured", 0, 0)
		return
	}

	start := time.Now()
	totalPages := len(pages)
	var questions []analysisQuestion
	var notes []string
	seen := make(map[string]bool)

	// Stage one + two per page: cut the page, then judge each cut
	// question. Progress blends pages done and questions judged so the
	// bar keeps moving on a dense page.
	for pageIdx, pagePath := range pages {
		_ = h.store.UpdatePaperAnalysisProgress(ctx, analysisID, model.StageSegmenting,
			progressFor(pageIdx, 0, totalPages, 0, 1), 0, 0)

		cut, err := ai.SegmentPaper(ctx, analysis.Subject, pagePath)
		if err != nil {
			// A page that won't segment is noted, not fatal: the rest
			// of the paper still parses.
			log.Printf("analysis %d: segmenting page %d: %v", analysisID, pageIdx+1, err)
			notes = append(notes, fmt.Sprintf("第%d页切题失败，已跳过", pageIdx+1))
			continue
		}
		if len(cut) == 0 {
			notes = append(notes, fmt.Sprintf("第%d页未识别到题目", pageIdx+1))
			continue
		}

		for qi, q := range cut {
			_ = h.store.UpdatePaperAnalysisProgress(ctx, analysisID, model.StageJudging,
				progressFor(pageIdx, qi, totalPages, len(cut), 1), len(questions), countWrong(questions))

			j, err := ai.JudgeQuestion(ctx, analysis.Subject, pagePath, q.Number, q.Text)
			if err != nil {
				// The spec's degrade path: a failed judgement is not a
				// lost question. Record it into the manual "unknown"
				// queue so the parent can fill it in by hand.
				log.Printf("analysis %d: judging %s: %v", analysisID, q.Number, err)
				j = &llm.QuestionJudgement{
					QuestionText: q.Text,
					Attempted:    false,
					IsWrong:      true,
					Confidence:   0,
				}
				notes = append(notes, fmt.Sprintf("第%s题判读失败，已加入人工核对", q.Number))
			}
			item := analysisQuestion{
				Number:          q.Number,
				QuestionText:    firstNonEmpty(judgementQuestionText(j), q.Text),
				Attempted:       j.Attempted,
				IsWrong:         j.IsWrong,
				WrongAnswerText: j.WrongAnswerText,
				AnswerText:      j.AnswerText,
				MistakeAnalysis: j.MistakeAnalysis,
				ErrorType:       j.ErrorType,
				KnowledgePoints: j.KnowledgePoints,
				Confidence:      j.Confidence,
			}

			// Only wrong questions enter the error book — the whole
			// point is what to fix, not what already works.
			if item.IsWrong {
				key := q.Number + "|" + item.QuestionText
				if seen[key] {
					continue
				}
				seen[key] = true
				itemID, err := h.recordErrorItem(ctx, analysis, completion, item, j)
				if err != nil {
					log.Printf("analysis %d: recording error item %s: %v", analysisID, q.Number, err)
					notes = append(notes, fmt.Sprintf("第%s题入库失败", q.Number))
				} else {
					item.ErrorItemID = &itemID
				}
			}
			questions = append(questions, item)
		}
	}

	// The diagnosis aggregates. Weak tags come straight from the store —
	// they include the kid's earlier mistakes too, which is exactly what
	// the "薄弱知识点" ranking should show.
	weakTags, _ := h.store.WeakKnowledgeTags(ctx, analysis.UserID, 10)
	if weakTags == nil {
		weakTags = []store.WeakTagStat{}
	}
	errorTypeCounts := map[string]int{}
	for _, q := range questions {
		if q.IsWrong && q.ErrorType != "" {
			errorTypeCounts[q.ErrorType]++
		}
	}
	summary := analysisSummary{
		Questions:       questions,
		ErrorTypeCounts: errorTypeCounts,
		WeakTags:        weakTags,
		Notes:           notes,
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		_ = h.store.FinishPaperAnalysis(ctx, analysisID, model.AnalysisFailed, "", "failed to build summary", 0, 0)
		return
	}
	if err := h.store.FinishPaperAnalysis(ctx, analysisID, model.AnalysisDone, string(summaryJSON), "",
		len(questions), countWrong(questions)); err != nil {
		log.Printf("analysis %d: finishing: %v", analysisID, err)
		return
	}
	log.Printf("analysis %d: parsed %d page(s), %d question(s), %d wrong in %s",
		analysisID, totalPages, len(questions), countWrong(questions), time.Since(start).Round(time.Second))
}

// analysisPagePaths resolves the proof images for one analysis: image
// attachments of the completion, else the legacy photo_url.
func (h *ChoreHandler) analysisPagePaths(ctx context.Context, completionID int64, legacyPhotoURL string) ([]string, error) {
	attachments, err := h.store.ListCompletionAttachments(ctx, completionID)
	if err != nil {
		return nil, fmt.Errorf("listing attachments: %w", err)
	}
	var pages []string
	for _, a := range attachments {
		if a.MimeClass != model.MimeClassImage {
			continue
		}
		p, err := resolveUploadPath(a.FilePath)
		if err != nil {
			continue
		}
		pages = append(pages, p)
	}
	if len(pages) == 0 && legacyPhotoURL != "" && isImageProof(legacyPhotoURL) {
		p, err := resolveUploadPath(legacyPhotoURL)
		if err != nil {
			return nil, fmt.Errorf("resolving legacy photo: %w", err)
		}
		pages = append(pages, p)
	}
	if len(pages) > maxPaperPages {
		pages = pages[:maxPaperPages]
	}
	return pages, nil
}

// progressFor blends page progress and in-page question progress into the
// 0-100 progress bar the parent watches.
func progressFor(pageIdx, questionIdx, totalPages, questionsInPage, questionWeight int) int {
	if totalPages <= 0 {
		return 0
	}
	pageSpan := 100 / totalPages
	base := pageIdx * pageSpan
	if questionsInPage <= 0 {
		return min(base+pageSpan, 99)
	}
	inPage := pageSpan * questionIdx / questionsInPage
	return min(base+inPage, 99)
}

// countWrong counts the wrong questions in one cut page.
func countWrong(questions []analysisQuestion) int {
	n := 0
	for _, q := range questions {
		if q.IsWrong {
			n++
		}
	}
	return n
}

// recordErrorItem writes one wrong question into the error book with its
// knowledge tags (find-or-create per tag).
func (h *ChoreHandler) recordErrorItem(ctx context.Context, analysis *model.PaperAnalysis, completion *model.ChoreCompletion, item analysisQuestion, j *llm.QuestionJudgement) (int64, error) {
	mistakeStatus := model.MistakeWrongAttempt
	if j == nil || j.Confidence == 0 && !j.Attempted {
		// Failed judge (or a zero-confidence blank): the manual queue.
		mistakeStatus = model.MistakeUnknown
	}
	originalURL := ""
	if completion.PhotoURL != "" {
		originalURL = completion.PhotoURL
	}
	if originalURL == "" {
		originalURL = fmt.Sprintf("completion:%d", completion.ID)
	}
	// The 来源 line (error_items.source): the chore the photographed
	// paper was submitted against — 「来自：数学错题订正」 in the book.
	source := "试卷分析"
	if completion != nil && completion.ChoreScheduleID != 0 {
		if sched, err := h.store.GetSchedule(ctx, completion.ChoreScheduleID); err == nil && sched != nil {
			if chore, err := h.store.GetChore(ctx, sched.ChoreID); err == nil && chore != nil {
				source = chore.Title
			}
		}
	}
	ei := &model.ErrorItem{
		UserID:           analysis.UserID,
		CompletionID:     &analysis.CompletionID,
		AnalysisID:       &analysis.ID,
		Subject:          analysis.Subject,
		OriginalImageURL: originalURL,
		QuestionText:     item.QuestionText,
		AnswerText:       item.AnswerText,
		WrongAnswerText:  item.WrongAnswerText,
		MistakeAnalysis:  item.MistakeAnalysis,
		MistakeStatus:    mistakeStatus,
		ErrorType:        item.ErrorType,
		AIConfidence:     item.Confidence,
		Source:           source,
		MasteryLevel:     model.MasteryNew,
	}
	if j != nil {
		ei.Analysis = j.Analysis
		ei.Difficulty = j.Difficulty
	}
	if err := h.store.CreateErrorItem(ctx, ei); err != nil {
		return 0, err
	}
	for _, name := range item.KnowledgePoints {
		tagID, err := h.store.FindOrCreateKnowledgeTag(ctx, name, analysis.Subject, nil)
		if err != nil {
			continue
		}
		if err := h.store.AttachErrorItemTag(ctx, ei.ID, tagID); err != nil {
			return 0, err
		}
	}
	// T+0 (spec §7.2): the wrong question is reviewable the same night.
	// Front is the question, back is the answer plus the mistake
	// analysis — what the flip side of the review card shows.
	if h.spaced != nil {
		back := strings.TrimSpace(ei.AnswerText)
		if ei.MistakeAnalysis != "" {
			if back != "" {
				back += "\n\n"
			}
			back += ei.MistakeAnalysis
		}
		h.spaced.EnsureReviewCard(ctx, ei.UserID, model.ReviewSourceErrorItem, ei.ID,
			ei.QuestionText, back, ei.Subject)
	}
	return ei.ID, nil
}

// GetPaperAnalysis is the parent's poll: the job row plus, when done,
// the parsed diagnosis report.
func (h *ChoreHandler) GetPaperAnalysis(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid analysis id")
		return
	}
	analysis, err := h.store.GetPaperAnalysis(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get analysis")
		return
	}
	if analysis == nil {
		writeError(w, http.StatusNotFound, "analysis not found")
		return
	}
	writeJSON(w, http.StatusOK, paperAnalysisJSON(analysis))
}

// ListKidAnalyses lists the kid's finished paper analyses (newest first),
// for the parent's dashboard quick-report. Limit defaults to 5.
func (h *ChoreHandler) ListKidAnalyses(w http.ResponseWriter, r *http.Request) {
	userID, err := urlParamInt64(r, "userID")
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	limit := 5
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 50 {
			writeError(w, http.StatusBadRequest, "limit must be 1-50")
			return
		}
		limit = n
	}
	list, err := h.store.LatestDonePaperAnalysesForUser(r.Context(), userID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list analyses")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for i := range list {
		out = append(out, paperAnalysisJSON(&list[i]))
	}
	writeJSON(w, http.StatusOK, out)
}

// paperAnalysisJSON is the wire shape of one analysis row: job fields
// always, the parsed diagnosis report when it finished.
func paperAnalysisJSON(analysis *model.PaperAnalysis) map[string]any {
	out := map[string]any{
		"id":              analysis.ID,
		"completion_id":   analysis.CompletionID,
		"user_id":         analysis.UserID,
		"created_by":      analysis.CreatedBy,
		"subject":         analysis.Subject,
		"status":          analysis.Status,
		"stage":           analysis.Stage,
		"progress":        analysis.Progress,
		"total_questions": analysis.TotalQuestions,
		"wrong_count":     analysis.WrongCount,
		"error":           analysis.Error,
		"created_at":      analysis.CreatedAt,
		"updated_at":      analysis.UpdatedAt,
	}
	if analysis.Status == model.AnalysisDone && analysis.Summary != "" {
		var summary analysisSummary
		if err := json.Unmarshal([]byte(analysis.Summary), &summary); err == nil {
			out["summary"] = summary
		} else {
			out["summary"] = json.RawMessage(analysis.Summary)
		}
	}
	return out
}

// ListErrorItems is the wrong-question book: filterable by kid (parents)
// or mastery state and subject. Kids see their own book; parents may pass
// any user_id (defaulting to the completion's kid is the caller's job).
func (h *ChoreHandler) ListErrorItems(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())

	userID := user.ID
	if user.Role == model.RoleAdmin {
		if v := r.URL.Query().Get("user_id"); v != "" {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid user_id")
				return
			}
			userID = id
		}
	}
	masteryLevel := -1
	if v := r.URL.Query().Get("mastery"); v != "" {
		m, err := strconv.Atoi(v)
		if err != nil || m < 0 || m > model.MasteryMastered {
			writeError(w, http.StatusBadRequest, "mastery must be 0, 1 or 2")
			return
		}
		masteryLevel = m
	}
	subject := r.URL.Query().Get("subject")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if l, err := strconv.Atoi(v); err == nil && l > 0 && l <= 500 {
			limit = l
		}
	}

	items, err := h.store.ListErrorItems(r.Context(), userID, masteryLevel, subject, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list error items")
		return
	}
	if items == nil {
		items = []model.ErrorItem{}
	}
	writeJSON(w, http.StatusOK, items)
}

// GetErrorItem returns one wrong question with its tags.
func (h *ChoreHandler) GetErrorItem(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid error item id")
		return
	}
	item, err := h.store.GetErrorItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get error item")
		return
	}
	if item == nil {
		writeError(w, http.StatusNotFound, "error item not found")
		return
	}
	user := UserFromContext(r.Context())
	if user.Role != model.RoleAdmin && item.UserID != user.ID {
		writeError(w, http.StatusForbidden, "this error item belongs to someone else")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// reviewResultRequest is the retry outcome: the kid redid the question
// (from the error book's "我订正完了" button), and someone reports whether
// the retry was right.
type reviewResultRequest struct {
	IsCorrect *bool `json:"is_correct"`
}

// SubmitReviewResult moves the mastery machine: a correct retry advances
// New → Reviewing → Mastered; a wrong retry keeps the current state (the
// item stays in the book — that's the loop working). L4's FSRS takes
// over scheduling later; for now a correct retry schedules the next
// review a day out (Reviewing) or clears the schedule (Mastered).
func (h *ChoreHandler) SubmitReviewResult(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid error item id")
		return
	}
	item, err := h.store.GetErrorItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get error item")
		return
	}
	if item == nil {
		writeError(w, http.StatusNotFound, "error item not found")
		return
	}
	user := UserFromContext(r.Context())
	if user.Role != model.RoleAdmin && item.UserID != user.ID {
		writeError(w, http.StatusForbidden, "this error item belongs to someone else")
		return
	}
	var req reviewResultRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.IsCorrect == nil {
		writeError(w, http.StatusBadRequest, "is_correct is required")
		return
	}

	var nextReview *time.Time
	var newMastery int
	switch {
	case *req.IsCorrect && item.MasteryLevel == model.MasteryNew:
		newMastery = model.MasteryReviewing
		t := time.Now().AddDate(0, 0, 1)
		nextReview = &t
	case *req.IsCorrect:
		newMastery = model.MasteryMastered
	default:
		newMastery = item.MasteryLevel
	}
	if err := h.store.UpdateErrorItemMastery(r.Context(), item.ID, newMastery, nextReview); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update error item")
		return
	}
	item.MasteryLevel = newMastery
	item.NextReviewAt = nextReview

	// L3 事件流：订正尝试即一次「作答」（xAPI answered）。context 带上
	// 知识点标签 id 列表，供 KC 掌握度 rollup（Laplace 平滑）消费。
	kcIDs := []int64{}
	if tags, err := h.store.TagsForItem(r.Context(), item.ID); err == nil {
		for _, t := range tags {
			kcIDs = append(kcIDs, t.ID)
		}
	}
	h.LearnerHook.recordLearnerEventSafe(r.Context(), item.UserID, model.EventVerbAnswered,
		model.EventObjectErrorItem, item.ID,
		map[string]any{"success": *req.IsCorrect, "completion": *req.IsCorrect},
		map[string]any{"subject": item.Subject, "kc_ids": kcIDs})

	writeJSON(w, http.StatusOK, item)
}

// correctionTaskRequest writes a chore for the kid to redo this question.
type correctionTaskRequest struct {
	Points       *int    `json:"points"`
	SpecificDate *string `json:"specific_date"`
	DueBy        *string `json:"due_by"`
}

// CreateCorrectionTask turns one wrong question into a correction chore
// (the diagnosis report's "一键订正" button): a one-day, parent-approved
// task pointing back at the error item. Completing it goes through the
// normal chore flow — points, approval and all.
func (h *ChoreHandler) CreateCorrectionTask(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid error item id")
		return
	}
	item, err := h.store.GetErrorItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get error item")
		return
	}
	if item == nil {
		writeError(w, http.StatusNotFound, "error item not found")
		return
	}

	var req correctionTaskRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	points := 3
	if req.Points != nil {
		if *req.Points < 1 || *req.Points > 20 {
			writeError(w, http.StatusBadRequest, "points must be between 1 and 20")
			return
		}
		points = *req.Points
	}

	// The task title names the question so the kid sees what to redo.
	title := fmt.Sprintf("订正错题 %s", item.QuestionText)
	if len(item.QuestionText) > 40 {
		title = fmt.Sprintf("订正错题（%s）", truncateFor(item.QuestionText, 40))
	}
	description := "把这道错题重新做一遍，对照解析订正。"
	if item.MistakeAnalysis != "" {
		description = "把这道错题重新做一遍。错因：" + item.MistakeAnalysis
	}

	// Traceability (OD chore-source-analysis contract): ExternalID points
	// back at the error item so the kid's task row can render「来自：卷面
	// 分析」. The scheduled-chore query joins chores.external_id, and the
	// kid side renders the label when it parses as error_item:<id>.
	externalID := fmt.Sprintf("error_item:%d", item.ID)
	if item.Source != "" {
		externalID = "error_item:" + strconv.FormatInt(item.ID, 10) + "|" + item.Source
	}

	chore := &model.Chore{
		Title:            title,
		Description:      description,
		Category:         model.CategoryRequired,
		Icon:             "📝",
		PointsValue:      points,
		RequiresApproval: true,
		Source:           model.SourceManual,
		ExternalID:       externalID,
		CreatedBy:        UserFromContext(r.Context()).ID,
	}
	if err := h.store.CreateChore(r.Context(), chore); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create chore")
		return
	}

	date := time.Now().Format(model.DateFormat)
	if req.SpecificDate != nil && *req.SpecificDate != "" {
		date = *req.SpecificDate
	}
	schedule := &model.ChoreSchedule{
		ChoreID:          chore.ID,
		AssignedTo:       item.UserID,
		AssignmentType:   model.AssignmentIndividual,
		SpecificDate:     &date,
		PointsMultiplier: 1.0,
		ExpiryPenalty:    model.ExpiryBlock,
	}
	if req.DueBy != nil && *req.DueBy != "" {
		schedule.DueBy = req.DueBy
	}
	if err := h.store.CreateSchedule(r.Context(), schedule); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create schedule")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"chore":    chore,
		"schedule": schedule,
	})
}

// ErrorBookReport is the aggregate view the parent's diagnosis page needs
// beyond one analysis: error-type distribution and weak knowledge tags
// across the kid's whole book.
type ErrorBookReport struct {
	ErrorTypes []store.ErrorTypeStat `json:"error_types"`
	WeakTags   []store.WeakTagStat   `json:"weak_tags"`
}

// GetErrorBookReport aggregates one kid's whole error book.
func (h *ChoreHandler) GetErrorBookReport(w http.ResponseWriter, r *http.Request) {
	userID, err := urlParamInt64(r, "userID")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if _, err := h.store.GetUser(r.Context(), userID); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	errorTypes, err := h.store.ErrorTypeDistribution(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to aggregate error types")
		return
	}
	weakTags, err := h.store.WeakKnowledgeTags(r.Context(), userID, 10)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to aggregate weak tags")
		return
	}
	writeJSON(w, http.StatusOK, ErrorBookReport{ErrorTypes: errorTypes, WeakTags: weakTags})
}

// UpdateErrorItem is the parent's manual fix for the "unknown" queue: the
// AI could not read the question, so the parent types what it should say.
func (h *ChoreHandler) UpdateErrorItem(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid error item id")
		return
	}
	item, err := h.store.GetErrorItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get error item")
		return
	}
	if item == nil {
		writeError(w, http.StatusNotFound, "error item not found")
		return
	}
	var req struct {
		Subject         string `json:"subject"`
		QuestionText    string `json:"question_text"`
		AnswerText      string `json:"answer_text"`
		WrongAnswerText string `json:"wrong_answer_text"`
		Analysis        string `json:"analysis"`
		MistakeAnalysis string `json:"mistake_analysis"`
		ErrorType       string `json:"error_type"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	validErrorTypes := map[string]bool{
		model.ErrorTypeCalculation: true, model.ErrorTypeConcept: true,
		model.ErrorTypeReading: true, model.ErrorTypeMethod: true,
	}
	if req.ErrorType != "" && !validErrorTypes[req.ErrorType] {
		writeError(w, http.StatusBadRequest, "error_type must be calculation, concept, reading or method")
		return
	}
	if req.Subject != "" {
		item.Subject = req.Subject
	}
	if req.QuestionText != "" {
		item.QuestionText = req.QuestionText
	}
	if req.AnswerText != "" {
		item.AnswerText = req.AnswerText
	}
	if req.WrongAnswerText != "" {
		item.WrongAnswerText = req.WrongAnswerText
	}
	if req.Analysis != "" {
		item.Analysis = req.Analysis
	}
	if req.MistakeAnalysis != "" {
		item.MistakeAnalysis = req.MistakeAnalysis
	}
	if req.ErrorType != "" {
		item.ErrorType = req.ErrorType
	}
	// A parent correction resolves the "unknown" degrade into a real
	// wrong_attempt entry.
	if item.MistakeStatus == model.MistakeUnknown && req.QuestionText != "" {
		item.MistakeStatus = model.MistakeWrongAttempt
	}
	if err := h.store.UpdateErrorItemFields(r.Context(), item.ID, item); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update error item")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// truncateFor cuts a string on a rune boundary for display titles.
func truncateFor(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

var _ = store.ErrErrorItemNotFound // keep the store error import honest
var _ = errors.New                 // placeholder to keep errors imported if unused
