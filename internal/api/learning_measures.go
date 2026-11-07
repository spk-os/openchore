package api

// Learning-measure endpoints (迁移 034): the analysis→measures engine.
// The paper-analysis pipeline distills its report into concrete measures
// the parent confirms; the parent can pause/finish/delete, and ask the
// AI to strengthen one measure against the kid's latest error data.
// Routes:
//   GET    /api/admin/measures?user_id=&status=&subject=
//   POST   /api/admin/measures                     (parent creates by hand)
//   PUT    /api/admin/measures/{id}                (parent edits)
//   DELETE /api/admin/measures/{id}
//   POST   /api/admin/measures/{id}/status         (active/paused/done)
//   POST   /api/admin/measures/{id}/strengthen     (AI rewrite with evidence)
//   POST   /api/admin/analyses/{id}/measures       (distill from a report)

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/liftedkilt/openchore/internal/llm"
	"github.com/liftedkilt/openchore/internal/model"
)

// measureRequest is the create/edit body.
type measureRequest struct {
	UserID     int64    `json:"user_id"`
	Subject    string   `json:"subject"`
	KCTagID    *int64   `json:"kc_tag_id"`
	Title      string   `json:"title"`
	Detail     string   `json:"detail"`
	ErrorTypes []string `json:"error_types"`
	Priority   int      `json:"priority"`
}

// validate normalizes one measure request in place. The subject is kept
// exactly as sent: "" is a legitimate no-subject (habit-level) measure and
// is never silently defaulted to "math"; a non-empty value is checked
// against the subjects vocabulary by the handler (checkMeasureSubject).
func (req *measureRequest) validate() error {
	req.Title = strings.TrimSpace(req.Title)
	req.Detail = strings.TrimSpace(req.Detail)
	if req.Title == "" || req.Detail == "" {
		return errors.New("title and detail are required")
	}
	if req.Priority < 1 || req.Priority > 3 {
		req.Priority = 2
	}
	types := make([]string, 0, len(req.ErrorTypes))
	for _, t := range req.ErrorTypes {
		switch t {
		case "calculation", "concept", "reading", "method":
			types = append(types, t)
		}
	}
	req.ErrorTypes = types
	return nil
}

// ListLearningMeasures serves GET /api/admin/measures.
func (h *ChoreHandler) ListLearningMeasures(w http.ResponseWriter, r *http.Request) {
	// ?user_id= is a query param here (not a route param — the route is
	// /admin/measures), so it must come off r.URL.Query(), not chi.URLParam.
	userID, err := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	if err != nil || userID == 0 {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", model.MeasureStatusActive, model.MeasureStatusPaused, model.MeasureStatusDone:
	default:
		writeError(w, http.StatusBadRequest, "status must be active, paused or done")
		return
	}
	subject := r.URL.Query().Get("subject")
	measures, err := h.store.ListLearningMeasures(r.Context(), userID, status, subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list measures")
		return
	}
	writeJSON(w, http.StatusOK, measures)
}

// checkMeasureSubject validates a non-empty request subject against the
// same table-driven vocabulary the chores use: unknown or disabled keys
// are rejected with 400, a vocabulary-load failure is a 500. It must run
// BEFORE any store write (never inside a transaction): the vocabulary
// load takes the DB's single connection, so wrapping it in a write would
// deadlock the pool. Empty is always allowed (no-subject measures).
func (h *ChoreHandler) checkMeasureSubject(w http.ResponseWriter, ctx context.Context, subject string) bool {
	if subject == "" {
		return true
	}
	ok, err := h.validSubject(ctx, subject)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load subjects")
		return false
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "subject must be empty or a subject from the subjects list")
		return false
	}
	return true
}

// CreateLearningMeasure serves POST /api/admin/measures — a parent
// writing a measure by hand (origin=manual).
func (h *ChoreHandler) CreateLearningMeasure(w http.ResponseWriter, r *http.Request) {
	var req measureRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.UserID == 0 {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.checkMeasureSubject(w, r.Context(), req.Subject) {
		return
	}
	parent := UserFromContext(r.Context())
	m := &model.LearningMeasure{
		UserID:     req.UserID,
		Subject:    req.Subject,
		KCTagID:    req.KCTagID,
		Title:      req.Title,
		Detail:     req.Detail,
		ErrorTypes: req.ErrorTypes,
		Origin:     model.MeasureOriginManual,
		Status:     model.MeasureStatusActive,
		Priority:   req.Priority,
		CreatedBy:  parent.ID,
	}
	if err := h.store.CreateLearningMeasure(r.Context(), m); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create measure")
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// loadMeasure resolves one measure id.
func (h *ChoreHandler) loadMeasure(w http.ResponseWriter, r *http.Request) *model.LearningMeasure {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid measure id")
		return nil
	}
	m, err := h.store.GetLearningMeasure(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get measure")
		return nil
	}
	if m == nil {
		writeError(w, http.StatusNotFound, "measure not found")
		return nil
	}
	return m
}

// UpdateLearningMeasure serves PUT /api/admin/measures/{id} — the
// parent's hand edit of what the analysis (or an earlier strengthen)
// produced.
func (h *ChoreHandler) UpdateLearningMeasure(w http.ResponseWriter, r *http.Request) {
	m := h.loadMeasure(w, r)
	if m == nil {
		return
	}
	var req measureRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.checkMeasureSubject(w, r.Context(), req.Subject) {
		return
	}
	m.Subject = req.Subject
	m.KCTagID = req.KCTagID
	m.Title = req.Title
	m.Detail = req.Detail
	m.ErrorTypes = req.ErrorTypes
	m.Priority = req.Priority
	if err := h.store.UpdateLearningMeasure(r.Context(), m.ID, m); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update measure")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// DeleteLearningMeasure serves DELETE /api/admin/measures/{id}.
func (h *ChoreHandler) DeleteLearningMeasure(w http.ResponseWriter, r *http.Request) {
	m := h.loadMeasure(w, r)
	if m == nil {
		return
	}
	if err := h.store.DeleteLearningMeasure(r.Context(), m.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete measure")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// measureStatusRequest is the status flip body.
type measureStatusRequest struct {
	Status string `json:"status"`
}

// SetLearningMeasureStatus serves POST /api/admin/measures/{id}/status.
func (h *ChoreHandler) SetLearningMeasureStatus(w http.ResponseWriter, r *http.Request) {
	m := h.loadMeasure(w, r)
	if m == nil {
		return
	}
	var req measureStatusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch req.Status {
	case model.MeasureStatusActive, model.MeasureStatusPaused, model.MeasureStatusDone:
	default:
		writeError(w, http.StatusBadRequest, "status must be active, paused or done")
		return
	}
	if err := h.store.SetLearningMeasureStatus(r.Context(), m.ID, req.Status); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update status")
		return
	}
	m.Status = req.Status
	writeJSON(w, http.StatusOK, m)
}

// StrengthenMeasure serves POST /api/admin/measures/{id}/strengthen: the
// AI rewrites one measure against the kid's latest error data. The fresh
// facts are gathered here (weak tags + error mix + the week's wrong
// count) so the prompt carries evidence, not vibes.
func (h *ChoreHandler) StrengthenMeasure(w http.ResponseWriter, r *http.Request) {
	if h.aiSvc.AI() == nil && h.aiSvc.Agent() == nil {
		writeError(w, http.StatusServiceUnavailable, "AI is not configured")
		return
	}
	m := h.loadMeasure(w, r)
	if m == nil {
		return
	}
	kid, err := h.store.GetUser(r.Context(), m.UserID)
	if err != nil || kid == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	facts, err := h.measureFacts(r.Context(), m)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to gather measure facts")
		return
	}
	// 任务③：spk-agent 优先（真 agent 重写措施），直连 LLM 回退。
	title, detail, err := h.strengthenMeasure(r.Context(), kid.Name, m.Title, m.Detail, facts, fmt.Sprintf("strengthen:%d", m.ID))
	if err != nil {
		writeError(w, http.StatusBadGateway, "AI strengthen failed: "+err.Error())
		return
	}
	m.Title = title
	m.Detail = detail
	m.Origin = model.MeasureOriginStrengthen
	if err := h.store.UpdateLearningMeasure(r.Context(), m.ID, m); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save strengthened measure")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// measureFacts gathers the fresh evidence the strengthen prompt carries:
// the kid's weak knowledge points, the error-type mix and this week's
// new wrong questions for the measure's subject.
func (h *ChoreHandler) measureFacts(ctx context.Context, m *model.LearningMeasure) (string, error) {
	weak, err := h.store.WeakKnowledgeTags(ctx, m.UserID, 5)
	if err != nil {
		return "", err
	}
	dist, err := h.store.ErrorTypeDistribution(ctx, m.UserID)
	if err != nil {
		return "", err
	}
	items, err := h.store.ListErrorItems(ctx, m.UserID, -1, m.Subject, 50)
	if err != nil {
		return "", err
	}
	out := ""
	if len(weak) > 0 {
		out += "当前薄弱知识点（错题数降序）：\n"
		for _, t := range weak {
			out += "- " + t.Name + "（" + t.Subject + "）：错 " + strconv.Itoa(t.WrongCount) + " 题\n"
		}
	}
	if len(dist) > 0 {
		out += "错因分布：\n"
		for _, d := range dist {
			out += "- " + d.ErrorType + "：" + strconv.Itoa(d.Count) + " 题\n"
		}
	}
	if len(items) > 0 {
		open := 0
		for _, it := range items {
			if it.MasteryLevel < model.MasteryMastered {
				open++
			}
		}
		out += "该科目错题总数 " + strconv.Itoa(len(items)) + "，其中未掌握 " + strconv.Itoa(open) + " 题。\n"
	}
	if out == "" {
		out = "暂无新错题数据；请基于措施本身的可执行性加强。"
	}
	return out, nil
}

// DistillMeasures serves POST /api/admin/analyses/{id}/measures: run the
// distill pass on a finished report and create the measures (origin=
// analysis). Idempotent-ish: existing active measures for the same
// subject are listed first and the AI is asked not to duplicate, but the
// parent can delete any duplicates.
func (h *ChoreHandler) DistillMeasures(w http.ResponseWriter, r *http.Request) {
	if h.aiSvc.AI() == nil && h.aiSvc.Agent() == nil {
		writeError(w, http.StatusServiceUnavailable, "AI is not configured")
		return
	}
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
	if analysis.Status != model.AnalysisDone {
		writeError(w, http.StatusBadRequest, "analysis has not finished")
		return
	}
	parent := UserFromContext(r.Context())
	created, err := h.distillMeasuresForAnalysis(r.Context(), analysis, parent.ID)
	if err != nil {
		// subjects 词表加载失败是服务端问题且不会写任何东西：不能沿用
		// 502 "AI distill failed: "+err（会把原生 DB/SQL 细节回给客户端，
		// 也会把内部错误误标成上游 AI 的锅）。内部细节只进日志。
		if errors.Is(err, errMeasureSubjectsUnavailable) {
			log.Printf("measures: distilling analysis %d: %v", analysis.ID, err)
			writeError(w, http.StatusInternalServerError, "failed to load subjects")
			return
		}
		writeError(w, http.StatusBadGateway, "AI distill failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "count": len(created)})
}

// distillMeasuresForAnalysis is the shared distill body (also called
// from the auto-analysis path so a student-submitted paper lands its
// measures without the parent clicking anything).
func (h *ChoreHandler) distillMeasuresForAnalysis(ctx context.Context, analysis *model.PaperAnalysis, parentID int64) ([]model.LearningMeasure, error) {
	kid, err := h.store.GetUser(ctx, analysis.UserID)
	if err != nil || kid == nil {
		return nil, errors.New("analysis kid not found")
	}
	// The report's weak tags + error mix are the prompt's raw material.
	weak, err := h.store.WeakKnowledgeTags(ctx, analysis.UserID, 6)
	if err != nil {
		return nil, err
	}
	dist, err := h.store.ErrorTypeDistribution(ctx, analysis.UserID)
	if err != nil {
		return nil, err
	}
	existing, err := h.store.ListLearningMeasures(ctx, analysis.UserID, model.MeasureStatusActive, "")
	if err != nil {
		return nil, err
	}
	existingTitles := make([]string, 0, len(existing))
	for _, m := range existing {
		existingTitles = append(existingTitles, m.Title)
	}
	weakInputs := make([]llm.MeasureInput, 0, len(weak))
	for _, t := range weak {
		weakInputs = append(weakInputs, llm.MeasureInput{Subject: t.Subject, Tag: t.Name, WrongCount: t.WrongCount})
	}
	distInputs := make([]llm.MeasureInput, 0, len(dist))
	for _, d := range dist {
		distInputs = append(distInputs, llm.MeasureInput{ErrorType: d.ErrorType, WrongCount: d.Count})
	}
	parentNotes := ""
	if len(existingTitles) > 0 {
		// Rendered into the prompt as "do not duplicate" context.
		parentNotes = "已有的进行中措施（不要重复，可加强其中一条但标题必须不同）：" + joinChinese(existingTitles)
	}
	sort.Slice(weakInputs, func(i, j int) bool { return weakInputs[i].WrongCount > weakInputs[j].WrongCount })

	// 任务③：spk-agent 优先（真 agent 蒸馏措施），直连 LLM 回退。
	drafts, err := h.draftMeasures(ctx, llm.MeasureContext{
		KidName:     kid.Name,
		Subject:     analysis.Subject,
		WeakTags:    weakInputs,
		ErrorMix:    distInputs,
		ParentNotes: parentNotes,
	}, fmt.Sprintf("measures:%d", analysis.ID))
	if err != nil {
		return nil, err
	}
	// AI 输出不可信（卡 TX-AI-MEASURE-SUBJECT-CODER）：任何写库之前先把
	// 每条 draft 的 subject 过一遍 enabled subjects 词表（与 chores 同一
	// 白名单）。未知/disabled/大小写不匹配的非空 subject 整条跳过——既不
	// 写 learning_measures，也不借 FindOrCreateKnowledgeTag 落异学科/垃圾
	// 知识点；空 subject 保持合法（习惯级措施）。词表加载失败直接报错，
	// 保证此时尚无任何 KC/措施写入。校验不在事务里跑：SQLite 单写者，
	// 词表加载会占用唯一连接，包进写事务会死锁。
	validSubjects, err := h.validDraftSubjects(ctx, drafts)
	if err != nil {
		return nil, err
	}
	created := make([]model.LearningMeasure, 0, len(drafts))
	for _, d := range drafts {
		if d.Subject != "" {
			if _, ok := validSubjects[d.Subject]; !ok {
				// Only the subject key is logged — draft titles/details can
				// carry the kid's private text and must not reach the logs.
				// The key itself is untrusted AI output, so it is truncated
				// (rune-safe): a runaway model reply must not be able to
				// spray unbounded text into the log either.
				log.Printf("measures: skipping distilled draft with unknown or disabled subject %q for user %d (analysis %d)",
					truncateFor(d.Subject, 64), analysis.UserID, analysis.ID)
				continue
			}
		}
		m := &model.LearningMeasure{
			UserID:     analysis.UserID,
			Subject:    d.Subject,
			Title:      d.Title,
			Detail:     d.Detail,
			ErrorTypes: d.ErrorTypes,
			Origin:     model.MeasureOriginAnalysis,
			OriginID:   &analysis.ID,
			Status:     model.MeasureStatusActive,
			Priority:   d.Priority,
			CreatedBy:  parentID,
		}
		if d.KC != "" && d.Subject != "" {
			// KC 自动建/绑只发生在 subject 已通过校验的 draft 上：空 subject
			// 的 KC 不得进 FindOrCreateKnowledgeTag（否则会落一条 subject=''
			// 的知识点）；空 subject 措施本身照常创建（习惯级）。
			if tagID, terr := h.store.FindOrCreateKnowledgeTag(ctx, d.KC, d.Subject, nil); terr == nil {
				m.KCTagID = &tagID
				m.KCName = d.KC
			}
		}
		if err := h.store.CreateLearningMeasure(ctx, m); err != nil {
			log.Printf("measures: creating distilled measure for user %d: %v", analysis.UserID, err)
			continue
		}
		created = append(created, *m)
	}
	return created, nil
}

// errMeasureSubjectsUnavailable is the fixed sentinel for "the enabled-
// subject vocabulary could not be loaded" on the distill path: the raw
// store error stays in the server log (it can carry SQL/driver detail),
// the client only ever sees the handler's stable generic message.
var errMeasureSubjectsUnavailable = errors.New("subjects vocabulary unavailable")

// validDraftSubjects builds the per-distill enabled-subject snapshot: one
// ListSubjects read, one exact-case set, shared by every non-empty draft
// subject in this validation pass — a repeated unknown subject is refused
// from the same snapshot instead of re-loading the vocabulary, and the
// whole batch sees one consistent word list (no mid-batch drift from a
// concurrent seed/disable). Nothing is written before this returns nil.
// The snapshot deliberately does NOT reuse the handler's subjectAllow
// cache: a single fresh read keeps the whole batch on one vocabulary and
// its error path is decisive (below). An empty subject is the caller's
// business (it stays legal) — this set only answers "is this non-empty
// subject enabled right now?". A load failure returns the sentinel
// errMeasureSubjectsUnavailable so the caller can abort before any write
// and map the failure to a fixed client message (never the raw DB error).
// Never call this inside a transaction: the vocabulary load takes the
// DB's single connection and would deadlock a wrapped write.
func (h *ChoreHandler) validDraftSubjects(ctx context.Context, drafts []llm.MeasureDraft) (map[string]struct{}, error) {
	subs, err := h.store.ListSubjects(ctx)
	if err != nil {
		// The raw store error (SQL/driver detail) stays server-side; the
		// caller only ever sees the fixed sentinel. Logged here because
		// the sentinel deliberately drops the cause.
		log.Printf("measures: loading subjects vocabulary for draft validation: %v", err)
		return nil, errMeasureSubjectsUnavailable
	}
	valid := make(map[string]struct{}, len(subs))
	for _, sub := range subs {
		if !sub.Enabled {
			continue
		}
		valid[sub.Key] = struct{}{}
	}
	return valid, nil
}

// joinChinese joins with 、 without importing strings here (the api
// package's trimSpace already avoided it; keep the pattern).
func joinChinese(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += "、"
		}
		out += s
	}
	return out
}

// decodeMeasures is a tiny helper the tests use to parse a measures list.
func decodeMeasures(body []byte) ([]model.LearningMeasure, error) {
	var out []model.LearningMeasure
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}
