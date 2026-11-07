package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
	"github.com/liftedkilt/openchore/internal/taxonomy"
)

// Taxonomy HTTP handlers (TX-BE-CODER). The embedded pack
// (taxonomy.Load()) is the engine/judging/browsing source of truth;
// the DB (042 tables) is its queryable mirror plus the learner-owned
// state (sessions, attempts, mastery, review cards). This file wires
// the frozen REST contract the frontend (TX-FE-CODER) builds against:
//
//	GET  /api/taxonomy/subjects
//	GET  /api/taxonomy/topics?subject&grade&q&domain&cursor&limit
//	GET  /api/taxonomy/topics/{externalRef}
//	POST /api/taxonomy/practice
//	POST /api/taxonomy/diagnostics
//	GET  /api/taxonomy/diagnostics?user_id=
//	GET  /api/taxonomy/diagnostics/{id}
//	POST /api/taxonomy/diagnostics/{id}/answers
//	POST /api/taxonomy/diagnostics/{id}/undo | /correct | /finish | /retest | /share
//	GET  /api/taxonomy/blindspots?user_id=
//	GET  /api/taxonomy/export?user_id=
//	POST /api/admin/taxonomy/import
//	POST /api/admin/taxonomy/courses/generate
//	GET  /api/taxonomy/shared/{token}   (public)
//
// Kids act on their own data (user_id is only honored for admins);
// X-User-ID is never trusted.

// TaxonomyHandler serves the taxonomy endpoints.
type TaxonomyHandler struct {
	store   *store.Store
	catalog *taxonomy.Catalog
	reviews *ReviewHandler
	LearnerHook

	// importOnce guards the pack's lazy load (Load parses 13MB of
	// JSON; a failed load is retried on the next import call).
	loadMu   sync.Mutex
	loadErr  error
	subjects []model.Subject
}

// NewTaxonomyHandler wires the store and review handler (the practice
// and diagnostic paths create review cards through it). The catalog
// is loaded lazily so a corrupted pack never blocks server start.
func NewTaxonomyHandler(s *store.Store, reviews *ReviewHandler) *TaxonomyHandler {
	return &TaxonomyHandler{store: s, reviews: reviews, LearnerHook: newLearnerHook(s)}
}

// HasCatalog reports whether the embedded pack has been loaded into
// memory. Test-only observability for the fingerprint skip: the skip
// path must never load the 13MB catalog.
func (h *TaxonomyHandler) HasCatalog() bool {
	h.loadMu.Lock()
	defer h.loadMu.Unlock()
	return h.catalog != nil
}

// DefaultSubjects is the ten-subject word list the seeder writes
// (sb_code only where JY/T 0644-2022 was verified — the rest stay
// empty, never invented).
func DefaultSubjects() []model.Subject {
	return []model.Subject{
		{Key: taxonomy.SubjectMath, LabelZh: "数学", SBCode: "330400", SortOrder: 1, Enabled: true},
		{Key: taxonomy.SubjectChinese, LabelZh: "语文", SBCode: "330101", SortOrder: 2, Enabled: true},
		{Key: taxonomy.SubjectEnglish, LabelZh: "英语", SBCode: "330302", SortOrder: 3, Enabled: true},
		{Key: taxonomy.SubjectPhysics, LabelZh: "物理", SBCode: "330440", SortOrder: 4, Enabled: true},
		{Key: taxonomy.SubjectChemistry, LabelZh: "化学", SBCode: "330450", SortOrder: 5, Enabled: true},
		{Key: taxonomy.SubjectBiology, LabelZh: "生物", SBCode: "330460", SortOrder: 6, Enabled: true},
		{Key: taxonomy.SubjectScience, LabelZh: "科学", SortOrder: 7, Enabled: true},
		{Key: taxonomy.SubjectMorality, LabelZh: "道德与法治", SortOrder: 8, Enabled: true},
		{Key: taxonomy.SubjectHistory, LabelZh: "历史", SBCode: "330501", SortOrder: 9, Enabled: true},
		{Key: taxonomy.SubjectGeography, LabelZh: "地理", SBCode: "330502", SortOrder: 10, Enabled: true},
	}
}

// catalogOnce returns the loaded pack, loading it on first use. A
// load failure is sticky until the next call retries it.
//
// The first load (and every ImportPack/sync) rebuilds the effective
// graph from the DB: the admin's reviewed/rejected prerequisite edges
// (042 tables) are the durable truth — a restart must not silently
// resurrect a rejected pack edge by loading only the embedded pack.
// A DB read or rebuild failure is logged and propagated to the caller
// (never swallowed into a pack-only catalog): the admin's verdicts
// staying visible beats availability of a wrong graph.
func (h *TaxonomyHandler) catalogOnce(ctx context.Context) (*taxonomy.Catalog, error) {
	h.loadMu.Lock()
	defer h.loadMu.Unlock()
	if h.catalog != nil {
		return h.catalog, nil
	}
	if h.loadErr != nil {
		return nil, h.loadErr
	}
	cat, err := taxonomy.Load()
	if err != nil {
		h.loadErr = err
		return nil, err
	}
	h.subjects = DefaultSubjects()
	// Rebuild the effective edges from the DB (admin verdicts applied)
	// BEFORE publishing the catalog. Errors propagate: a pack-only
	// fallback would silently undo every admin reviewed/rejected edge.
	edges, err := h.store.ListEffectivePrerequisiteEdges(ctx)
	if err != nil {
		err = fmt.Errorf("load effective prerequisite edges: %w", err)
		h.loadErr = err
		return nil, err
	}
	if len(edges) > 0 {
		next, err := cat.WithEdges(edges)
		if err != nil {
			err = fmt.Errorf("rebuild catalog from stored edges: %w", err)
			h.loadErr = err
			return nil, err
		}
		cat = next
	}
	h.catalog = cat
	return cat, nil
}

// ImportPack seeds the subjects and runs the idempotent pack import
// (the main.go startup hook and the admin re-import endpoint).
func (h *TaxonomyHandler) ImportPack(ctx context.Context) (*store.TaxonomyCounts, error) {
	cat, err := h.catalogOnce(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.store.ImportTaxonomy(ctx, cat, h.subjects); err != nil {
		return nil, err
	}
	return h.store.CountTaxonomy(ctx)
}

// The app_settings keys SyncTaxonomyPack maintains. The pack ships in
// the binary, so the fingerprint only changes on a rebuild with new
// data — these keys are server bookkeeping, deliberately not in the
// admin writable-settings whitelist.
const (
	packFingerprintKey = "taxonomy.pack_fingerprint"
	packSyncedAtKey    = "taxonomy.pack_synced_at"
)

// SyncTaxonomyPack is the fingerprint-gated import: when the stored
// fingerprint matches the embedded pack it skips the 13MB load+parse
// and the full upsert entirely (skipped=true, counts straight from the
// DB); otherwise it runs the full ImportPack and records the new
// fingerprint and timestamp. force=true ignores a matching fingerprint
// (the manual re-import escape hatch). A GetSetting failure degrades
// to a full import — never silently skip on an unreadable setting.
func (h *TaxonomyHandler) SyncTaxonomyPack(ctx context.Context, force bool) (*store.TaxonomyCounts, bool, error) {
	fp, err := taxonomy.PackFingerprint()
	if err != nil {
		return nil, false, err
	}
	if !force {
		stored, err := h.store.GetSetting(ctx, packFingerprintKey)
		if err != nil {
			log.Printf("taxonomy: read stored pack fingerprint, falling back to full import: %v", err)
		} else if stored != "" && stored == fp {
			// Fingerprint match: the DB already mirrors this exact pack.
			// Count straight from the DB — no catalogOnce, no Load, no
			// ImportTaxonomy on this path.
			counts, err := h.store.CountTaxonomy(ctx)
			if err != nil {
				return nil, false, err
			}
			return counts, true, nil
		}
	}
	counts, err := h.ImportPack(ctx)
	if err != nil {
		return nil, false, err
	}
	if err := h.store.SetSetting(ctx, packFingerprintKey, fp); err != nil {
		log.Printf("taxonomy: record pack fingerprint: %v", err)
	}
	if err := h.store.SetSetting(ctx, packSyncedAtKey, time.Now().UTC().Format(time.RFC3339)); err != nil {
		log.Printf("taxonomy: record pack sync time: %v", err)
	}
	return counts, false, nil
}

// --- word list & browse ---

// GetSubjects serves GET /api/taxonomy/subjects — the ten-subject word
// list with imported-topic counts.
func (h *TaxonomyHandler) GetSubjects(w http.ResponseWriter, r *http.Request) {
	stats, err := h.store.SubjectStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load subjects")
		return
	}
	if stats == nil {
		stats = []store.SubjectStat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"subjects": stats})
}

// GetTopics serves GET /api/taxonomy/topics — the browse/search list
// with cursor pagination.
func (h *TaxonomyHandler) GetTopics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var grade *int
	if v := q.Get("grade"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 16 {
			writeError(w, http.StatusBadRequest, "invalid grade")
			return
		}
		grade = &n
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	topics, next, err := h.store.BrowseTopics(r.Context(), q.Get("subject"), grade,
		q.Get("q"), q.Get("domain"), q.Get("cursor"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list topics")
		return
	}
	// The response is a SW-cacheable public document (blank-q list):
	// every row is projected onto a dedicated public DTO that carries
	// neither the diagnostic question (its embedded answer/explanation
	// would ride the shared cache) nor any user-differentiated field —
	// the raw store row (model.KnowledgeTag + diagnostic_question_json)
	// must never leave the server as-is.
	rows := make([]topicBrowseView, 0, len(topics))
	for _, t := range topics {
		rows = append(rows, topicBrowseView{
			ID:            t.ID,
			Name:          t.Name,
			Subject:       t.Subject,
			Grade:         t.Grade,
			Semester:      t.Semester,
			Domain:        t.Domain,
			Chapter:       t.Chapter,
			Description:   t.Description,
			ExternalRef:   t.ExternalRef,
			QuestionCount: t.QuestionCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"topics": rows, "next_cursor": next})
}

// topicBrowseView is the public topics-list row: the browse fields the
// SubjectBrowser renders (name/grade/domain/chapter/description, the
// external_ref key and the question count) and nothing else. The
// store's TopicBrowseRow embeds model.KnowledgeTag, whose
// mastery_criteria_json / diagnostic_question_json (the latter carries
// the diagnostic question's answer and explanation) are server-side
// engine inputs, not cacheable public data.
type topicBrowseView struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Subject       string `json:"subject"`
	Grade         *int   `json:"grade"`
	Semester      *int   `json:"semester"`
	Domain        string `json:"domain"`
	Chapter       string `json:"chapter"`
	Description   string `json:"description,omitempty"`
	ExternalRef   string `json:"external_ref,omitempty"`
	QuestionCount int    `json:"question_count"`
}

// topicDetail is one knowledge point with its graph neighborhood and
// bank (answers stripped for kids).
type topicDetail struct {
	Topic         *model.KnowledgeTag        `json:"topic"`
	Prerequisites []model.KnowledgeTag       `json:"prerequisites"`
	Dependents    []model.KnowledgeTag       `json:"dependents"`
	Questions     []taxonomyQuestionView     `json:"questions"`
	Standards     []model.CurriculumStandard `json:"standards"`
}

// taxonomyQuestionView is one bank question as served over the API.
// Answer is omitted for kids (leak guard: they practice, they don't
// peek).
type taxonomyQuestionView struct {
	ID             string           `json:"id"`
	TopicID        string           `json:"topic_id"`
	Difficulty     string           `json:"difficulty"`
	Type           string           `json:"type"`
	Prompt         string           `json:"prompt"`
	Choices        []taxonomyChoice `json:"choices,omitempty"`
	Answer         string           `json:"answer,omitempty"`
	Accept         []string         `json:"accept,omitempty"`
	Explanation    string           `json:"explanation,omitempty"`
	Figure         string           `json:"figure,omitempty"`
	Unordered      bool             `json:"unordered,omitempty"`
	HideChoiceText bool             `json:"hide_choice_text,omitempty"`
	Percent        bool             `json:"percent,omitempty"`
}

type taxonomyChoice struct {
	Key    string `json:"key"`
	Text   string `json:"text"`
	Figure string `json:"figure,omitempty"`
}

// questionToView converts an engine question to the API view,
// stripping the answer unless includeAnswers.
func questionToView(q *taxonomy.Question, includeAnswers bool) taxonomyQuestionView {
	v := taxonomyQuestionView{
		ID:             q.ID,
		TopicID:        q.TopicID,
		Difficulty:     q.Difficulty,
		Type:           q.Type,
		Prompt:         q.Prompt,
		Figure:         q.Figure,
		Unordered:      q.Unordered,
		HideChoiceText: q.HideChoiceText,
		Percent:        q.Percent,
	}
	for _, c := range q.Choices {
		v.Choices = append(v.Choices, taxonomyChoice{Key: c.Key, Text: c.Text, Figure: c.Figure})
	}
	if includeAnswers {
		v.Answer = q.Answer
		v.Accept = q.Accept
		v.Explanation = q.Explanation
	}
	return v
}

// GetTopic serves GET /api/taxonomy/topics/{externalRef} — the
// knowledge-point detail with prerequisites, dependents, bank and
// standards. Kids get questions without answers.
func (h *TaxonomyHandler) GetTopic(w http.ResponseWriter, r *http.Request) {
	ref := urlParam(r, "externalRef")
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	tp := cat.Topic(ref)
	if tp == nil {
		// alias resolution (cnm_ext_* → targets) is a list, not one
		// topic: 404 keeps the contract single-resource.
		writeError(w, http.StatusNotFound, "topic not found")
		return
	}
	dbTag, err := h.store.GetTopicByExternalRef(r.Context(), ref)
	if err != nil || dbTag == nil {
		writeError(w, http.StatusNotFound, "topic not found")
		return
	}
	// The topic body (and each neighbor row) is served through the
	// public projection: the raw model.KnowledgeTag carries
	// diagnostic_question_json (the diagnostic question with its answer
	// and explanation) — engine input, never API output.
	dbTag.MasteryJSON = ""
	dbTag.DiagnosticJSON = ""
	detail := topicDetail{
		Topic:         dbTag,
		Prerequisites: h.tagNeighbors(r, cat, cat.Prereqs[ref], sidePrerequisite),
		Dependents:    h.tagNeighbors(r, cat, cat.DepsOf[ref], sideDependent),
		Questions:     []taxonomyQuestionView{},
		Standards:     []model.CurriculumStandard{},
	}
	// Bank questions: answerless for everyone — the admin's bank view
	// carried answer/accept/explanation pre-submit, a second answer
	// face the kid-side leak guard never covered. e2e helpers that need
	// an answer fetch it through the practice/diagnostic submit
	// feedback or the admin-only flows, not this endpoint.
	for _, q := range cat.QuestionsOf(ref) {
		detail.Questions = append(detail.Questions, questionToView(q, false))
	}
	if dbTag.ID > 0 {
		if stds, err := h.store.ListStandardsForTopic(r.Context(), dbTag.ID); err == nil && stds != nil {
			detail.Standards = stds
		}
	}
	writeJSON(w, http.StatusOK, detail)
}

// neighborSide says which end of an edge the reference topic occupies —
// the ref end picks the other topic per edge list direction:
//
//   - cat.Prereqs[ref] edges have ref as TopicID, the neighbor is
//     PrerequisiteID (sidePrerequisite);
//   - cat.DepsOf[ref] edges have ref as PrerequisiteID, the neighbor
//     is TopicID (sideDependent).
type neighborSide int

const (
	sidePrerequisite neighborSide = iota
	sideDependent
)

// tagNeighbors loads the KnowledgeTag rows for one edge list (nil
// edges → empty slice, never null). The neighbor ref comes from the
// edge side: prereq lists read PrerequisiteID, dependent lists read
// TopicID.
func (h *TaxonomyHandler) tagNeighbors(r *http.Request, cat *taxonomy.Catalog, edges []*taxonomy.Edge, side neighborSide) []model.KnowledgeTag {
	out := make([]model.KnowledgeTag, 0, len(edges))
	for _, e := range edges {
		ref := e.PrerequisiteID
		if side == sideDependent {
			ref = e.TopicID
		}
		tp := cat.Topic(ref)
		if tp == nil {
			continue
		}
		t, err := h.store.GetTopicByExternalRef(r.Context(), ref)
		if err != nil || t == nil {
			continue
		}
		// Same public projection as the topic body: the neighbor row's
		// diagnostic/mastery JSON blobs never ride the detail response.
		t.MasteryJSON = ""
		t.DiagnosticJSON = ""
		out = append(out, *t)
	}
	return out
}

// --- practice ---

// practiceRequest is POST /api/taxonomy/practice.
type practiceRequest struct {
	UserID     *int64 `json:"user_id"`
	TopicID    string `json:"topic_id"`
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
}

// practiceResponse is the frozen contract's answer.
type practiceResponse struct {
	Correct      bool                 `json:"correct"`
	Explanation  string               `json:"explanation"`
	Mastery      float64              `json:"mastery"`
	ReviewItemID *int64               `json:"review_item_id,omitempty"`
	Verdict      string               `json:"verdict"`
	Question     taxonomyQuestionView `json:"question"`
	// ReferenceAnswer ships the bank answer once the verdict is out —
	// or the manual round actually attempted something (self/skip
	// self-checks against it). A blank submission is a non-attempt: no
	// answer ships. The embedded question view never carries it (same
	// leak guard as the topic detail).
	ReferenceAnswer string `json:"reference_answer,omitempty"`
}

// Practice serves POST /api/taxonomy/practice — one bank question
// answered, judged by the engine (JudgeQuestion: choice/numeric/short
// deterministic, self manual), rolled into the learner profile
// (learner_events + kc_mastery) and, when the KC came out weak, a
// T+0 review card. A wrong verdict additionally lands the question
// in the error book (error_items, once per question) with its own
// T+0 card.
func (h *TaxonomyHandler) Practice(w http.ResponseWriter, r *http.Request) {
	var req practiceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.TopicID == "" || req.QuestionID == "" {
		writeError(w, http.StatusBadRequest, "topic_id and question_id are required")
		return
	}
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	tp := cat.Topic(req.TopicID)
	if tp == nil {
		writeError(w, http.StatusBadRequest, "unknown topic_id")
		return
	}
	q := cat.QuestionByID(req.QuestionID)
	if q == nil || q.TopicID != req.TopicID {
		writeError(w, http.StatusBadRequest, "unknown question_id")
		return
	}
	target, ok := h.resolveTarget(w, r, req.UserID)
	if !ok {
		return
	}
	subject := cat.SubjectOf(tp)

	// Judge: choice/numeric/short are deterministic; self (and an
	// unrecognizable answer) come back manual — recorded as an
	// attempt without a verdict, never a rejection.
	judged := taxonomy.JudgeQuestion(q, req.Answer)
	resp := practiceResponse{
		Verdict:  judged.Verdict,
		Question: questionToView(q, false),
	}
	// The verdict is out, or the kid actually answered something: the
	// reference answer and explanation are safe to ship (a blank
	// manual submission is a non-attempt — nothing ships).
	attempted := strings.TrimSpace(req.Answer) != ""
	if judged.Verdict != taxonomy.VerdictManual || attempted {
		resp.ReferenceAnswer = q.Answer
		resp.Explanation = q.Explanation
	}
	switch judged.Verdict {
	case taxonomy.VerdictCorrect:
		resp.Correct = true
	case taxonomy.VerdictWrong:
		resp.Correct = false
	default: // manual: self-assessed or unsure
		// A manual round is still a recorded attempt: skipped=true
		// marks the no-verdict semantics while success=false keeps
		// the event stream consistent.
		dbTagID, _ := h.store.TagIDByExternalRef(r.Context(), req.TopicID)
		h.recordPracticeSkip(r.Context(), target, subject, req.TopicID, req.QuestionID, dbTagID)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Profile: one answered event on this KC refreshes kc_mastery.
	dbTagID, _ := h.store.TagIDByExternalRef(r.Context(), req.TopicID)
	h.recordPracticeEvent(r.Context(), target, subject, req.TopicID, req.QuestionID, resp.Correct, dbTagID)

	// Wrong → error book: the question becomes an error_items row (once
	// per user+question — EnsureTaxonomyErrorItem dedups on (user_id,
	// source); a repeat wrong never overwrites the first answer, the
	// mastery level or the FSRS schedule) and gets its own T+0 card.
	// Correct/manual/skip rounds never touch the error book.
	if judged.Verdict == taxonomy.VerdictWrong && dbTagID > 0 {
		item := &model.ErrorItem{
			UserID:          target,
			Subject:         subject,
			QuestionText:    q.Prompt,
			AnswerText:      q.Answer,
			WrongAnswerText: req.Answer,
			Analysis:        q.Explanation,
			MistakeAnalysis: q.Explanation,
			MistakeStatus:   model.MistakeWrongAttempt,
			Source:          "taxonomy:" + req.QuestionID,
			MasteryLevel:    model.MasteryNew,
		}
		itemID, _, err := h.store.EnsureTaxonomyErrorItem(r.Context(), item, dbTagID)
		if err != nil {
			// A lost mistake must never answer 200: the error book is a
			// durable outcome of a wrong verdict, not a best-effort
			// side effect (learner_events above stays — it is its own
			// boundary, deliberately not one big transaction).
			log.Printf("taxonomy: ensure error item for practice %s: %v", req.QuestionID, err)
			writeError(w, http.StatusInternalServerError, "failed to record the mistake in the error book")
			return
		}
		if h.reviews != nil {
			h.reviews.EnsureReviewCard(r.Context(), target, model.ReviewSourceErrorItem, itemID,
				q.Prompt, q.Answer, subject)
			if card, err := h.store.GetReviewItemBySource(r.Context(), model.ReviewSourceErrorItem, itemID); err == nil && card != nil {
				resp.ReviewItemID = &card.ID
			}
		}
	}

	if dbTagID > 0 {
		if mastery, err := h.kcMasteryOf(r.Context(), target, dbTagID); err == nil {
			resp.Mastery = mastery
		}
		// Weak KC → T+0 review card (UNIQUE(source kc, tag) makes a
		// repeat practice a no-op create).
		if resp.Mastery < 0.6 && h.reviews != nil {
			h.reviews.EnsureReviewCard(r.Context(), target, model.ReviewSourceKC, dbTagID,
				tp.Name, tp.Description, subject)
			if item, err := h.store.GetReviewItemBySource(r.Context(), model.ReviewSourceKC, dbTagID); err == nil && item != nil {
				resp.ReviewItemID = &item.ID
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// recordPracticeEvent appends the answered event (the durable fact
// behind every later profile number).
func (h *TaxonomyHandler) recordPracticeEvent(ctx context.Context, userID int64, subject, topicRef, questionID string, correct bool, dbTagID int64) {
	kcIDs := []int64{}
	if dbTagID > 0 {
		kcIDs = append(kcIDs, dbTagID)
	}
	h.recordLearnerEventSafe(ctx, userID, model.EventVerbAnswered, model.EventObjectChore, 0,
		map[string]any{"success": correct},
		map[string]any{"kc_ids": kcIDs, "topic_id": topicRef, "question_id": questionID, "subject": subject})
}

// recordPracticeSkip appends the manual-round attempt as an answered
// event with no verdict: skipped=true marks the round as un-judged (the
// mastery rollup treats it as "no signal") while success=false keeps
// the stored shape uniform.
func (h *TaxonomyHandler) recordPracticeSkip(ctx context.Context, userID int64, subject, topicRef, questionID string, dbTagID int64) {
	kcIDs := []int64{}
	if dbTagID > 0 {
		kcIDs = append(kcIDs, dbTagID)
	}
	h.recordLearnerEventSafe(ctx, userID, model.EventVerbAnswered, model.EventObjectChore, 0,
		map[string]any{"success": false, "skipped": true},
		map[string]any{"kc_ids": kcIDs, "topic_id": topicRef, "question_id": questionID, "subject": subject})
}

// kcMasteryOf reads one KC's current mastery rollup.
func (h *TaxonomyHandler) kcMasteryOf(ctx context.Context, userID, tagID int64) (float64, error) {
	rows, err := h.store.ListKCMastery(ctx, userID)
	if err != nil {
		return 0, err
	}
	for _, m := range rows {
		if m.TagID == tagID {
			return m.Mastery, nil
		}
	}
	return 0.5, nil // no rollup yet: neutral prior
}

// resolveTarget applies the access rule shared by practice and
// diagnostics: admins may name any user (user_id, required > 0); kids
// always act as themselves (user_id ignored — never trusted).
func (h *TaxonomyHandler) resolveTarget(w http.ResponseWriter, r *http.Request, requested *int64) (int64, bool) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return 0, false
	}
	if user.Role == model.RoleAdmin {
		if requested == nil || *requested <= 0 {
			writeError(w, http.StatusBadRequest, "user_id is required")
			return 0, false
		}
		// GetUser answers (nil, nil) for an unknown id (sql.ErrNoRows is
		// swallowed into a nil row): both nil and an error must reject —
		// a bare error check would wave user_id=999999 through as the
		// target and silently render an admin-named ghost user.
		target, err := h.store.GetUser(r.Context(), *requested)
		if err != nil || target == nil {
			writeError(w, http.StatusBadRequest, "unknown user_id")
			return 0, false
		}
		return *requested, true
	}
	if requested != nil && *requested != user.ID {
		// A kid naming someone else is an access probe, not a typo:
		// reject without confirming the id exists.
		writeError(w, http.StatusForbidden, "cannot act as another user")
		return 0, false
	}
	return user.ID, true
}

// --- diagnostics ---

// diagnosticsScope is the frozen contract's scope block.
type diagnosticsScope struct {
	Grade    *int   `json:"grade"`
	Semester string `json:"semester"`
	Domain   string `json:"domain"`
	Chapter  string `json:"chapter"`
}

// diagnosticsRequest is POST /api/taxonomy/diagnostics.
type diagnosticsRequest struct {
	UserID  *int64            `json:"user_id"`
	Mode    string            `json:"mode"` // target|screen
	Subject string            `json:"subject"`
	Targets []string          `json:"targets"`
	Scope   *diagnosticsScope `json:"scope"`
	Limit   int               `json:"limit"`
	Confirm int               `json:"confirm"`
	Seed    string            `json:"seed"`
	Avoid   []string          `json:"avoid"`
}

// CreateDiagnostic serves POST /api/taxonomy/diagnostics — start a
// session (target mode: goals + ancestors; screen mode: the scope's
// whole topic set) and persist the engine state for resumable
// answering.
func (h *TaxonomyHandler) CreateDiagnostic(w http.ResponseWriter, r *http.Request) {
	var req diagnosticsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	target, ok := h.resolveTarget(w, r, req.UserID)
	if !ok {
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = taxonomy.ModeTarget
	}
	if mode != taxonomy.ModeTarget && mode != taxonomy.ModeScreen {
		writeError(w, http.StatusBadRequest, "mode must be target or screen")
		return
	}
	targets := taxonomy.ResolveTargets(req.Targets, cat.Aliases)
	if len(targets) == 0 && req.Scope == nil {
		writeError(w, http.StatusBadRequest, "targets or scope is required")
		return
	}
	// A grade/chapter scope expands to its topic set (screen mode's
	// default entry).
	if len(targets) == 0 {
		targets, err = h.scopeTargets(cat, req.Subject, req.Scope)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(targets) == 0 {
			writeError(w, http.StatusBadRequest, "no topics matched the scope")
			return
		}
	}
	opts := &taxonomy.CreateSessionOptions{
		Mode:    mode,
		Limit:   req.Limit,
		Confirm: taxonomy.ClampConfirm(req.Confirm),
		Seed:    req.Seed,
		Avoid:   req.Avoid,
	}
	sess, err := cat.CreateSession(targets, opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Subject == "" && len(sess.Targets) > 0 {
		if tp := cat.Topic(sess.Targets[0]); tp != nil {
			req.Subject = cat.SubjectOf(tp)
		}
	}
	stateJSON, err := json.Marshal(sess)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to serialize session")
		return
	}
	scopeJSON, _ := json.Marshal(req.Scope)
	d := &model.DiagnosticSession{
		UserID:    target,
		Subject:   req.Subject,
		Mode:      store.DiagnosticModeDiagnose,
		ScopeJSON: string(scopeJSON),
		StateJSON: string(stateJSON),
	}
	if err := h.store.CreateDiagnosticSession(r.Context(), d); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	writeJSON(w, http.StatusCreated, h.sessionDetail(r, cat, d))
}

// scopeTargets expands a grade/domain/chapter scope into topic ids.
func (h *TaxonomyHandler) scopeTargets(cat *taxonomy.Catalog, subject string, sc *diagnosticsScope) ([]string, error) {
	if sc == nil {
		return nil, nil
	}
	if sc.Grade == nil {
		return nil, nil
	}
	var volumes []string
	switch sc.Semester {
	case "上", "1":
		volumes = []string{"上"}
	case "下", "2":
		volumes = []string{"下"}
	case "全":
		volumes = []string{"全"}
	default: // whole grade: both halves
		volumes = []string{"上", "下", "全"}
	}
	sub := subject
	if sub == "" {
		sub = taxonomy.SubjectMath
	}
	var out []string
	for _, t := range cat.Topics {
		if t.External {
			continue
		}
		if sub != taxonomy.SubjectAll && cat.SubjectOf(t) != sub {
			continue
		}
		if t.Grade != *sc.Grade {
			continue
		}
		if sc.Domain != "" && t.Domain != sc.Domain {
			continue
		}
		if !containsAnyVolume(t.Semester, volumes) {
			continue
		}
		out = append(out, t.ID)
	}
	return out, nil
}

func containsAnyVolume(semester string, volumes []string) bool {
	for _, v := range volumes {
		if semester == v {
			return true
		}
	}
	return false
}

// ListDiagnostics serves GET /api/taxonomy/diagnostics?user_id=.
func (h *TaxonomyHandler) ListDiagnostics(w http.ResponseWriter, r *http.Request) {
	target, ok := h.listTarget(w, r)
	if !ok {
		return
	}
	sessions, err := h.store.ListDiagnosticSessions(r.Context(), target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}
	if sessions == nil {
		sessions = []model.DiagnosticSession{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// listTarget is resolveTarget for the read-only list view (user_id
// optional for kids — defaults to self).
func (h *TaxonomyHandler) listTarget(w http.ResponseWriter, r *http.Request) (int64, bool) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return 0, false
	}
	if user.Role == model.RoleAdmin {
		v := r.URL.Query().Get("user_id")
		if v == "" {
			writeError(w, http.StatusBadRequest, "user_id is required")
			return 0, false
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid user_id")
			return 0, false
		}
		// GetUser answers (nil, nil) for an unknown id — the bare error
		// check would serve a ghost user's (empty) data with 200.
		target, err := h.store.GetUser(r.Context(), id)
		if err != nil || target == nil {
			writeError(w, http.StatusBadRequest, "unknown user_id")
			return 0, false
		}
		return id, true
	}
	if v := r.URL.Query().Get("user_id"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err != nil || id != user.ID {
			writeError(w, http.StatusForbidden, "cannot read another user's sessions")
			return 0, false
		}
	}
	return user.ID, true
}

// loadOwnedSession loads one session and enforces ownership (kids see
// only their own; admins see any).
func (h *TaxonomyHandler) loadOwnedSession(w http.ResponseWriter, r *http.Request) (*model.DiagnosticSession, bool) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return nil, false
	}
	d, err := h.store.GetDiagnosticSession(r.Context(), id)
	if err != nil || d == nil {
		writeError(w, http.StatusNotFound, "session not found")
		return nil, false
	}
	user := UserFromContext(r.Context())
	if user.Role != model.RoleAdmin && d.UserID != user.ID {
		writeError(w, http.StatusNotFound, "session not found")
		return nil, false
	}
	return d, true
}

// GetDiagnostic serves GET /api/taxonomy/diagnostics/{id}.
func (h *TaxonomyHandler) GetDiagnostic(w http.ResponseWriter, r *http.Request) {
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	d, ok := h.loadOwnedSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.sessionDetail(r, cat, d))
}

// sessionDetail is the frozen contract's session view: the session
// row, the next question (answers NEVER travel here — even an admin
// sees the same answerless question view the kid gets; the only
// answer face is the submit feedback), the summary when finished,
// and the append-only attempts.
func (h *TaxonomyHandler) sessionDetail(r *http.Request, cat *taxonomy.Catalog, d *model.DiagnosticSession) map[string]any {
	attempts, _ := h.store.ListDiagnosticAttempts(r.Context(), d.ID)
	if attempts == nil {
		attempts = []model.DiagnosticAttempt{}
	}
	out := map[string]any{
		"session":  d,
		"attempts": attempts,
	}
	// A completed session never serves another question: the engine
	// state may still carry a pending stack (a forced finish keeps
	// it), but the contract says done is done.
	completed := d.Status == store.DiagnosticStatusCompleted || d.CompletedAt != nil
	if completed {
		out["question"] = nil
	} else {
		var sess taxonomy.Session
		if err := json.Unmarshal([]byte(d.StateJSON), &sess); err == nil {
			if next := cat.NextItem(&sess); next != nil {
				out["question"] = map[string]any{
					"topic_id": next.TopicID,
					"attempt":  next.Attempt,
					// includeAnswers=false for everyone: the admin's
					// pre-submit view carrying answer/accept/explanation
					// was a second, unguarded answer face — the submit
					// feedback is the only place answers ship.
					"question": questionToView(next.Question, false),
				}
			}
		} else {
			out["summary"] = nil
			out["question"] = nil
		}
	}
	// The stored summary (SummaryJSON) is the finished session's frozen
	// answer; only an unfinished session still gets the live engine view.
	if !completed && d.SummaryJSON == "" {
		var sess taxonomy.Session
		if err := json.Unmarshal([]byte(d.StateJSON), &sess); err == nil {
			if summary := cat.Summarize(&sess); summary != nil {
				out["summary"] = summary
			}
		}
	}
	if d.SummaryJSON != "" {
		var raw json.RawMessage = json.RawMessage(d.SummaryJSON)
		out["summary"] = raw
	}
	return out
}

// answerRequest is POST /api/taxonomy/diagnostics/{id}/answers.
type answerRequest struct {
	QuestionID string `json:"question_id"`
	Answer     string `json:"answer"`
}

// AnswerDiagnostic serves POST /api/taxonomy/diagnostics/{id}/answers
// — one answer applied to the engine state, persisted (state +
// append-only attempt), profiled (learner_events/kc_mastery) and, on
// finish, turned into summary + review cards for the weak spots.
func (h *TaxonomyHandler) AnswerDiagnostic(w http.ResponseWriter, r *http.Request) {
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	d, ok := h.loadOwnedSession(w, r)
	if !ok {
		return
	}
	var req answerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var sess taxonomy.Session
	if err := json.Unmarshal([]byte(d.StateJSON), &sess); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session state")
		return
	}
	if d.Status == store.DiagnosticStatusVoided {
		writeError(w, http.StatusBadRequest, "session is voided")
		return
	}
	// A completed session is terminal — no more answers land on it. The
	// engine state of a forced finish may still hold a pending question
	// (NextItem != nil), so the row's status is the authority here, not
	// the engine view; same completed test as sessionDetail.
	if d.Status == store.DiagnosticStatusCompleted || d.CompletedAt != nil {
		writeError(w, http.StatusConflict, "session already completed")
		return
	}
	next := cat.NextItem(&sess)
	if next == nil {
		writeError(w, http.StatusBadRequest, "session has no more questions")
		return
	}
	if req.QuestionID != "" && req.QuestionID != next.Question.ID {
		writeError(w, http.StatusBadRequest, "question_id does not match the next question")
		return
	}
	// Judge against the bank question (deterministic where possible;
	// manual = recorded without a verdict).
	judged := taxonomy.JudgeQuestion(next.Question, req.Answer)
	result := ""
	switch judged.Verdict {
	case taxonomy.VerdictCorrect:
		result = taxonomy.ResultCorrect
	case taxonomy.VerdictWrong:
		result = taxonomy.ResultWrong
	default:
		result = taxonomy.ResultSkip // manual/self: no verdict this round
	}
	newSess, err := cat.ApplyAnswerText(&sess, next.TopicID, result, req.Answer)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// 042 contract: answer_json stores a JSON string (the kid's "999"
	// lands as "\"999\"") — the shape decodeAttemptText decodes first.
	answerJSON, err := json.Marshal(req.Answer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to serialize answer")
		return
	}
	stateJSON, err := json.Marshal(newSess)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to serialize session")
		return
	}
	// Build the summary first: the atomic write carries it with the
	// state swap, so a failed write leaves no stale summary behind.
	summary := cat.Summarize(newSess)
	summaryJSON := ""
	finished := summary != nil && summary.Finished
	if summary != nil {
		if b, err := json.Marshal(summary); err == nil {
			summaryJSON = string(b)
		}
	}
	// Append-only attempt row. Integer FKs resolve through the
	// mirror; legacy synthesized questions have no DB row (NULL).
	qRowID, _ := h.store.QuestionIDByExternalRef(r.Context(), next.Question.ID)
	tRowID, _ := h.store.TagIDByExternalRef(r.Context(), next.TopicID)
	correct := judged.Verdict == taxonomy.VerdictCorrect
	attempt := &model.DiagnosticAttempt{
		SessionID:  d.ID,
		QuestionID: next.Question.ID,
		TopicID:    next.TopicID,
		Seq:        len(newSess.Answers),
		Role:       "probe",
		AnswerJSON: string(answerJSON),
	}
	if judged.Verdict != taxonomy.VerdictManual {
		attempt.Correct = &correct
	}
	// One transaction: the attempt insert and the state swap land
	// together — the state update is a compare-and-swap on the
	// snapshot this handler loaded (d.StateJSON), so a concurrent
	// answer/undo that moved state_json first rolls the insert back
	// with a conflict instead of wedging the pair half-applied.
	if err := h.store.AppendDiagnosticAttempt(r.Context(), d.ID, d.StateJSON, attempt, qRowID, tRowID, string(stateJSON), summaryJSON, finished); err != nil {
		switch {
		case errors.Is(err, store.ErrDiagnosticStateConflict):
			writeError(w, http.StatusConflict, "session changed concurrently, retry")
		default:
			writeError(w, http.StatusInternalServerError, "failed to record attempt")
		}
		return
	}
	// Profile only the judged answers (manual carries no signal) — and
	// only after the write committed.
	if judged.Verdict != taxonomy.VerdictManual && tRowID > 0 {
		h.recordPracticeEvent(r.Context(), d.UserID, d.Subject, next.TopicID, next.Question.ID, correct, tRowID)
	}
	if finished {
		h.onDiagnosticFinished(r.Context(), cat, newSess, summary, d)
	}
	updated, _ := h.store.GetDiagnosticSession(r.Context(), d.ID)
	if updated == nil {
		updated = d
	}
	writeJSON(w, http.StatusOK, h.sessionDetail(r, cat, updated))
}

// onDiagnosticFinished is the terminal hook: weak/shaky KCs get a T+0
// review card each, and mastered ones get their active card suspended
// through the KC sync (reviewing a mastered point is busywork — the
// sync never un-suspends and never creates). Untested KCs stay
// untouched: unknown is a no-op on the card side.
func (h *TaxonomyHandler) onDiagnosticFinished(ctx context.Context, cat *taxonomy.Catalog, sess *taxonomy.Session, summary *taxonomy.Summary, d *model.DiagnosticSession) {
	if summary == nil {
		return
	}
	// Mastered → suspend the kid's active card for the KC, best-effort.
	// This runs before the reviews gate: the sync needs only the store
	// (the card text, if a card has to be read, comes from
	// knowledge_tags), so a bare handler without a ReviewHandler still
	// suspends. A KC without a DB mirror row (tagID 0) is skipped.
	for _, ref := range summary.Mastered {
		tagID, err := h.store.TagIDByExternalRef(ctx, ref)
		if err != nil || tagID == 0 {
			continue
		}
		h.syncKCReviewCard(ctx, d.UserID, tagID, taxonomy.StatusMastered)
	}
	if h.reviews == nil {
		return
	}
	weak := append(append([]string(nil), summary.Weak...), summary.Shaky...)
	for _, ref := range weak {
		tp := cat.Topic(ref)
		if tp == nil {
			continue
		}
		tagID, err := h.store.TagIDByExternalRef(ctx, ref)
		if err != nil || tagID == 0 {
			continue
		}
		h.reviews.EnsureReviewCard(ctx, d.UserID, model.ReviewSourceKC, tagID, tp.Name, tp.Description, cat.SubjectOf(tp))
	}
}

// undoRequest is POST /api/taxonomy/diagnostics/{id}/undo.
type undoRequest struct {
	Count int `json:"count"`
}

// UndoDiagnostic serves POST /api/taxonomy/diagnostics/{id}/undo —
// replay the session minus the last N answers; the affected attempt
// rows get the void overlay (never deleted). A row a parent corrected
// is undoable too: it is still the live row for its answer slot in the
// engine state, so cutting the slot voids the row whatever its
// answered/corrected status.
func (h *TaxonomyHandler) UndoDiagnostic(w http.ResponseWriter, r *http.Request) {
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	d, ok := h.loadOwnedSession(w, r)
	if !ok {
		return
	}
	var req undoRequest
	_ = decodeJSON(r, &req)
	if req.Count <= 0 {
		req.Count = 1
	}
	var sess taxonomy.Session
	if err := json.Unmarshal([]byte(d.StateJSON), &sess); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session state")
		return
	}
	if req.Count > len(sess.Answers) {
		req.Count = len(sess.Answers)
	}
	if req.Count == 0 {
		writeJSON(w, http.StatusOK, h.sessionDetail(r, cat, d))
		return
	}
	newSess, err := cat.Undo(&sess, req.Count, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.persistReplay(w, r, cat, d, newSess, req.Count)
}

// correctRequest is POST /api/taxonomy/diagnostics/{id}/correct.
type correctRequest struct {
	AttemptID int64  `json:"attempt_id"`
	Verdict   string `json:"verdict"` // correct | wrong | skip
}

// CorrectDiagnostic serves POST /api/taxonomy/diagnostics/{id}/correct
// — the parent overrides one historical answer's verdict (the kid was
// right after all, or the wrong/skip was too generous) and the session
// replays. The kid's original answer text is kept verbatim. Admin only.
func (h *TaxonomyHandler) CorrectDiagnostic(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil || user.Role != model.RoleAdmin {
		writeError(w, http.StatusForbidden, "admin only")
		return
	}
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	d, ok := h.loadOwnedSession(w, r)
	if !ok {
		return
	}
	var req correctRequest
	if err := decodeJSON(r, &req); err != nil || req.AttemptID == 0 {
		writeError(w, http.StatusBadRequest, "attempt_id is required")
		return
	}
	// The verdict maps 1:1 onto engine results; anything else is a client error.
	var newResult string
	switch req.Verdict {
	case taxonomy.ResultCorrect:
		newResult = taxonomy.ResultCorrect
	case taxonomy.ResultWrong:
		newResult = taxonomy.ResultWrong
	case taxonomy.ResultSkip:
		newResult = taxonomy.ResultSkip
	default:
		writeError(w, http.StatusBadRequest, "verdict must be one of correct, wrong, skip")
		return
	}
	var sess taxonomy.Session
	if err := json.Unmarshal([]byte(d.StateJSON), &sess); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session state")
		return
	}
	attempts, err := h.store.ListDiagnosticAttempts(r.Context(), d.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load attempts")
		return
	}
	// Locate the attempt row. Only an 'answered' row can be corrected —
	// voided/corrected rows are history, not live answers.
	var attempt *model.DiagnosticAttempt
	for i := range attempts {
		if attempts[i].ID == req.AttemptID {
			attempt = &attempts[i]
			break
		}
	}
	if attempt == nil {
		writeError(w, http.StatusNotFound, "attempt not found")
		return
	}
	if attempt.Status != "answered" {
		writeError(w, http.StatusNotFound, "attempt is no longer correctable")
		return
	}
	// The engine's answers[] position = seq-1 (one row per applied
	// answer). Bounds + question + text must all line up, else the
	// client is holding a stale id (e.g. from before an undo replayed
	// the session onto different questions).
	idx := attempt.Seq - 1
	if idx < 0 || idx >= len(sess.Answers) {
		writeError(w, http.StatusNotFound, "attempt not found")
		return
	}
	rec := sess.Answers[idx]
	if rec.QID != attempt.QuestionID {
		writeError(w, http.StatusNotFound, "attempt does not match the session state")
		return
	}
	if text, ok := decodeAttemptText(attempt.AnswerJSON); !ok || text != rec.Text {
		writeError(w, http.StatusNotFound, "attempt does not match the session state")
		return
	}
	// Apply the parent's verdict on the original answer text — the
	// kid's own text is kept, never re-judged against a parent edit.
	newSess, err := cat.CorrectAnswer(&sess, idx, newResult, rec.Text)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	newState, err := json.Marshal(newSess)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to serialize session")
		return
	}
	summary := cat.Summarize(newSess)
	// A completed session stays completed: force the finished bit back
	// on so the summary the client (and SummaryJSON) sees keeps saying
	// so, whatever the replayed engine state left pending.
	completed := d.Status == store.DiagnosticStatusCompleted
	if completed && summary != nil {
		summary.Finished = true
	} else {
		completed = summary != nil && summary.Finished
	}
	summaryJSON := ""
	if summary != nil {
		b, err := json.Marshal(summary)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to serialize summary")
			return
		}
		summaryJSON = string(b)
	}
	// One transaction: flip the attempt to corrected + CAS the session
	// state (a concurrent answer/undo that moved state_json rolls us
	// back with a conflict instead of wedging the pair half-applied).
	if err := h.store.CorrectDiagnosticReplay(r.Context(), d.ID, req.AttemptID, d.StateJSON, string(newState), summaryJSON, completed); err != nil {
		switch {
		case errors.Is(err, store.ErrDiagnosticStateConflict):
			writeError(w, http.StatusConflict, "session changed concurrently, retry")
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "attempt not found")
		default:
			log.Printf("taxonomy: correct diagnostic %d attempt %d: %v", d.ID, req.AttemptID, err)
			writeError(w, http.StatusInternalServerError, "failed to correct attempt")
		}
		return
	}
	updated, err := h.store.GetDiagnosticSession(r.Context(), d.ID)
	if err != nil || updated == nil {
		log.Printf("taxonomy: reload diagnostic %d after correct: %v", d.ID, err)
		writeError(w, http.StatusInternalServerError, "failed to reload session")
		return
	}
	writeJSON(w, http.StatusOK, h.sessionDetail(r, cat, updated))
}

// decodeAttemptText pulls the kid's original answer text out of an
// attempt row's answer_json. The 042 contract stores a plain JSON
// string ("\"42\"") and the answer path always writes it that way;
// this tree's earlier builds, though, wrote the bare text (999, A),
// so a legacy row is accepted as-is when it isn't valid JSON of any
// other kind — never rewriting the kid's answer, and never treating
// a JSON number/object as the string format (those aren't shapes the
// answer path produces; a row holding one must fail the match like
// any other tampered payload, keeping the QID/seq/status guards on
// top of it intact).
func decodeAttemptText(answerJSON string) (string, bool) {
	var text string
	if err := json.Unmarshal([]byte(answerJSON), &text); err == nil {
		return text, true
	}
	if json.Valid([]byte(answerJSON)) {
		// Valid non-string JSON (number, object, …): not a format the
		// answer path writes; reject instead of guessing a coercion.
		return "", false
	}
	// Legacy bare-text row (pre-contract build on this tree): the
	// whole column value is the kid's answer verbatim.
	return answerJSON, true
}

// persistReplay saves a replayed engine state (undo uses it): the
// superseded attempt rows get the void overlay and the state + summary
// swap lands in one transaction — a concurrent writer that moved
// state_json first rolls the whole replay back with a conflict instead
// of leaving it half-applied. A session that already reached completed
// stays completed — the replay can't un-finish it.
func (h *TaxonomyHandler) persistReplay(w http.ResponseWriter, r *http.Request, cat *taxonomy.Catalog, d *model.DiagnosticSession, newSess *taxonomy.Session, voidCount int) {
	// The engine state was cut by voidCount answer records, so exactly
	// the last voidCount live attempt rows must be voided. Locate them
	// by position against the pre-replay state's Answers tail: seq +
	// questionID + answer text must all line up per slot, and the rows
	// must be the actual tail (answered or corrected — a corrected row
	// is still the live row of its slot). Positioning by status alone
	// ("the newest N 'answered' rows") skips a corrected tail row and
	// voids an earlier substitute instead — the state/attempt
	// misalignment this guard exists to prevent.
	var sess taxonomy.Session
	if err := json.Unmarshal([]byte(d.StateJSON), &sess); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session state")
		return
	}
	if voidCount < 0 || voidCount > len(sess.Answers) {
		log.Printf("taxonomy: replay diagnostic %d: void count %d out of range (answers %d)", d.ID, voidCount, len(sess.Answers))
		writeError(w, http.StatusInternalServerError, "failed to void attempts")
		return
	}
	attempts, err := h.store.ListDiagnosticAttempts(r.Context(), d.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load attempts")
		return
	}
	cut := sess.Answers[len(sess.Answers)-voidCount:]
	voidIDs := make([]int64, 0, voidCount)
	// attempts are seq-ordered; walk the cut tail (oldest first) and
	// match each slot against the rows from the end backwards. The
	// pre-replay live tail rows are the last voidCount non-voided rows
	// (a voided row is history that was never part of this state's
	// answer tail).
	tail := make([]model.DiagnosticAttempt, 0, voidCount)
	for i := len(attempts) - 1; i >= 0 && len(tail) < voidCount; i-- {
		if attempts[i].Status != "voided" {
			tail = append(tail, attempts[i])
		}
	}
	if len(tail) != voidCount {
		log.Printf("taxonomy: replay diagnostic %d: %d live attempts, want %d", d.ID, len(tail), voidCount)
		writeError(w, http.StatusInternalServerError, "failed to void attempts")
		return
	}
	// tail was collected newest-first; the cut is oldest-first — walk
	// them pairwise.
	for i := range cut {
		row := tail[len(tail)-1-i]
		rec := cut[i]
		if row.Status != "answered" && row.Status != "corrected" {
			log.Printf("taxonomy: replay diagnostic %d: attempt %d status %q not voidable", d.ID, row.ID, row.Status)
			writeError(w, http.StatusInternalServerError, "failed to void attempts")
			return
		}
		// seq is 1-based over the state's applied answers; the slot must
		// carry the same question and (when both recorded one) the same
		// answer text. A legacy synthesized row has no question id —
		// match it on seq/text only.
		if row.Seq != len(sess.Answers)-voidCount+i+1 || row.QuestionID != rec.QID {
			log.Printf("taxonomy: replay diagnostic %d: attempt %d (seq %d, q %q) does not match state slot %d (q %q)", d.ID, row.ID, row.Seq, row.QuestionID, len(sess.Answers)-voidCount+i, rec.QID)
			writeError(w, http.StatusInternalServerError, "failed to void attempts")
			return
		}
		if text, ok := decodeAttemptText(row.AnswerJSON); ok && rec.Text != "" && text != rec.Text {
			log.Printf("taxonomy: replay diagnostic %d: attempt %d answer %q does not match state text", d.ID, row.ID, text)
			writeError(w, http.StatusInternalServerError, "failed to void attempts")
			return
		}
		voidIDs = append(voidIDs, row.ID)
	}
	if len(voidIDs) != voidCount {
		log.Printf("taxonomy: replay diagnostic %d: %d voidable attempts, want %d", d.ID, len(voidIDs), voidCount)
		writeError(w, http.StatusInternalServerError, "failed to void attempts")
		return
	}
	stateJSON, err := json.Marshal(newSess)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to serialize session")
		return
	}
	summary := cat.Summarize(newSess)
	// A completed session stays completed: force the finished bit into
	// the summary too, so the stored SummaryJSON (and the client) keep
	// seeing finished=true, whatever the replayed engine state says.
	if d.Status == store.DiagnosticStatusCompleted && summary != nil {
		summary.Finished = true
	}
	summaryJSON := ""
	if summary != nil {
		if b, err := json.Marshal(summary); err == nil {
			summaryJSON = string(b)
		}
	}
	completed := d.Status == store.DiagnosticStatusCompleted || (summary != nil && summary.Finished)
	// One transaction: the void overlay and the state CAS land together
	// (the CAS expects the snapshot this handler loaded, d.StateJSON).
	if err := h.store.ReplayDiagnosticState(r.Context(), d.ID, d.StateJSON, string(stateJSON), summaryJSON, completed, voidIDs); err != nil {
		switch {
		case errors.Is(err, store.ErrDiagnosticStateConflict):
			writeError(w, http.StatusConflict, "session changed concurrently, retry")
		default:
			log.Printf("taxonomy: replay diagnostic %d: %v", d.ID, err)
			writeError(w, http.StatusInternalServerError, "failed to save session")
		}
		return
	}
	updated, err := h.store.GetDiagnosticSession(r.Context(), d.ID)
	if err != nil || updated == nil {
		writeError(w, http.StatusInternalServerError, "failed to reload session")
		return
	}
	writeJSON(w, http.StatusOK, h.sessionDetail(r, cat, updated))
}

// FinishDiagnostic serves POST /api/taxonomy/diagnostics/{id}/finish —
// force the terminal state (summary + review cards) on an unfinished
// session the kid walked away from.
func (h *TaxonomyHandler) FinishDiagnostic(w http.ResponseWriter, r *http.Request) {
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	d, ok := h.loadOwnedSession(w, r)
	if !ok {
		return
	}
	var sess taxonomy.Session
	if err := json.Unmarshal([]byte(d.StateJSON), &sess); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session state")
		return
	}
	summary := cat.Summarize(&sess)
	// A forced finish is a finish: the summary the client (and the
	// stored SummaryJSON) sees must say so, whatever the engine state
	// left pending.
	if summary != nil {
		summary.Finished = true
	}
	summaryJSON := ""
	if summary != nil {
		if b, err := json.Marshal(summary); err == nil {
			summaryJSON = string(b)
		}
	}
	// One transaction: force the terminal state with a compare-and-swap
	// on the snapshot this handler loaded — a concurrent answer/undo
	// that moved state_json first rolls the finish back with a
	// conflict instead of finishing over a stale state.
	if err := h.store.ReplayDiagnosticState(r.Context(), d.ID, d.StateJSON, d.StateJSON, summaryJSON, true, nil); err != nil {
		switch {
		case errors.Is(err, store.ErrDiagnosticStateConflict):
			writeError(w, http.StatusConflict, "session changed concurrently, retry")
		default:
			log.Printf("taxonomy: finish diagnostic %d: %v", d.ID, err)
			writeError(w, http.StatusInternalServerError, "failed to finish session")
		}
		return
	}
	h.onDiagnosticFinished(r.Context(), cat, &sess, summary, d)
	updated, _ := h.store.GetDiagnosticSession(r.Context(), d.ID)
	if updated == nil {
		updated = d
	}
	writeJSON(w, http.StatusOK, h.sessionDetail(r, cat, updated))
}

// RetestDiagnostic serves POST /api/taxonomy/diagnostics/{id}/retest —
// a fresh session over the weak/untested set of a finished one (a new
// question seed, so the retest isn't a memory game).
func (h *TaxonomyHandler) RetestDiagnostic(w http.ResponseWriter, r *http.Request) {
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	d, ok := h.loadOwnedSession(w, r)
	if !ok {
		return
	}
	var sess taxonomy.Session
	if err := json.Unmarshal([]byte(d.StateJSON), &sess); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load session state")
		return
	}
	summary := cat.Summarize(&sess)
	if summary == nil {
		writeError(w, http.StatusBadRequest, "session state unavailable")
		return
	}
	retestTargets := append(append([]string(nil), summary.Weak...), summary.Shaky...)
	if len(retestTargets) == 0 {
		writeError(w, http.StatusBadRequest, "nothing to retest")
		return
	}
	seed := strconv.FormatInt(time.Now().UnixNano(), 36)
	newSess, err := cat.CreateSession(retestTargets, &taxonomy.CreateSessionOptions{
		Mode:  taxonomy.ModeTarget,
		Seed:  seed,
		Avoid: sessionQuestionIDs(&sess),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	stateJSON, err := json.Marshal(newSess)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to serialize session")
		return
	}
	nd := &model.DiagnosticSession{
		UserID:    d.UserID,
		Subject:   d.Subject,
		Mode:      store.DiagnosticModeRetest,
		ScopeJSON: d.ScopeJSON,
		StateJSON: string(stateJSON),
	}
	if err := h.store.CreateDiagnosticSession(r.Context(), nd); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create retest")
		return
	}
	writeJSON(w, http.StatusCreated, h.sessionDetail(r, cat, nd))
}

// sessionQuestionIDs collects a session's asked question ids (the
// retest's avoid list).
func sessionQuestionIDs(s *taxonomy.Session) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range s.Answers {
		if a.QID != "" && !seen[a.QID] {
			seen[a.QID] = true
			out = append(out, a.QID)
		}
	}
	return out
}

// --- blind spots ---

// blindspotRow is one KC in the blind-spot view.
type blindspotRow struct {
	TagID         int64   `json:"tag_id"`
	Ref           string  `json:"ref"`
	Name          string  `json:"name"`
	Subject       string  `json:"subject"`
	Mastery       float64 `json:"mastery"`
	Correct       int     `json:"correct_count"`
	Total         int     `json:"total_count"`
	LowConfidence bool    `json:"low_confidence"`
}

// blindspotResponse is GET /api/taxonomy/blindspots.
type blindspotResponse struct {
	Weak      []blindspotRow `json:"weak"`
	Shaky     []blindspotRow `json:"shaky"`
	Mastered  []blindspotRow `json:"mastered"`
	ByChapter []chapterRow   `json:"by_chapter"`
}

type chapterRow struct {
	Subject string `json:"subject"`
	Chapter string `json:"chapter"`
	Weak    int    `json:"weak"`
	Total   int    `json:"total"`
}

// GetBlindspots serves GET /api/taxonomy/blindspots?user_id= — the
// learner's per-KC states bucketed weak (<0.6) / shaky (<0.8) /
// mastered, plus a per-chapter weak summary.
func (h *TaxonomyHandler) GetBlindspots(w http.ResponseWriter, r *http.Request) {
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	target, ok := h.listTarget(w, r)
	if !ok {
		return
	}
	mastery, err := h.store.ListKCMastery(r.Context(), target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load mastery")
		return
	}
	resp := blindspotResponse{
		Weak: []blindspotRow{}, Shaky: []blindspotRow{}, Mastered: []blindspotRow{}, ByChapter: []chapterRow{},
	}
	chapters := map[string]*chapterRow{}
	for _, m := range mastery {
		tag, err := h.store.GetKnowledgeTag(r.Context(), m.TagID)
		if err != nil || tag == nil {
			continue
		}
		row := blindspotRow{
			TagID: m.TagID, Ref: tag.ExternalRef, Name: m.Name, Subject: m.Subject,
			Mastery: m.Mastery, Correct: m.CorrectCount, Total: m.TotalCount, LowConfidence: m.LowConfidence,
		}
		switch {
		case m.Mastery < 0.6:
			resp.Weak = append(resp.Weak, row)
		case m.Mastery < 0.8:
			resp.Shaky = append(resp.Shaky, row)
		default:
			resp.Mastered = append(resp.Mastered, row)
		}
		if tag.Chapter != "" {
			key := tag.Subject + "|" + tag.Chapter
			cr := chapters[key]
			if cr == nil {
				cr = &chapterRow{Subject: tag.Subject, Chapter: tag.Chapter}
				chapters[key] = cr
			}
			cr.Total++
			if m.Mastery < 0.6 {
				cr.Weak++
			}
		}
		_ = cat
	}
	for _, cr := range chapters {
		if cr.Weak > 0 {
			resp.ByChapter = append(resp.ByChapter, *cr)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- share links ---
// learning restore answers with model.LearningRestoreResult).
type ImportResult struct {
	Imported int                   `json:"imported"`
	Skipped  int                   `json:"skipped"`
	Counts   *store.TaxonomyCounts `json:"counts"`
}

// AdminImportPack serves POST /api/admin/taxonomy/sync — re-run the
// idempotent pack import (the manual retry path when startup logged a
// warning). The pack path lives under /sync: /import is the learning
// document restore, the two must never alias. ?force=1 bypasses the
// fingerprint skip and re-runs the full import.
func (h *TaxonomyHandler) AdminImportPack(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "1"
	counts, skipped, err := h.SyncTaxonomyPack(r.Context(), force)
	if err != nil {
		// Log the real cause; the admin gets a stable generic message —
		// a store/SQLite detail never echoes back to the client.
		log.Printf("taxonomy: manual import: %v", err)
		writeError(w, http.StatusInternalServerError, "import failed")
		return
	}
	if skipped {
		writeJSON(w, http.StatusOK, ImportResult{Imported: 0, Skipped: 1, Counts: counts})
		return
	}
	writeJSON(w, http.StatusOK, ImportResult{Imported: int(counts.Topics), Skipped: 0, Counts: counts})
}

// --- share links ---

// shareRequest is POST /api/taxonomy/diagnostics/{id}/share.
type shareRequest struct {
	ExpiresInHours int    `json:"expires_in_hours"`
	Kind           string `json:"kind"`
}

// shareResponse is the share-create answer. Token appears here only
// once — never stored, never logged (only its hash is). URL is the
// public frontend route (/share/taxonomy/{token}), not the API
// endpoint; the raw token in the path is the same credential the
// public GET /api/taxonomy/shared/{token} accepts.
type shareResponse struct {
	ShareID   int64      `json:"share_id"`
	URL       string     `json:"url"`
	Token     string     `json:"token"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// ShareDiagnostic serves POST /api/taxonomy/diagnostics/{id}/share —
// mint a read-only share of the session's summary snapshot. Kids can
// share their own; admins can share any.
func (h *TaxonomyHandler) ShareDiagnostic(w http.ResponseWriter, r *http.Request) {
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	d, ok := h.loadOwnedSession(w, r)
	if !ok {
		return
	}
	var req shareRequest
	_ = decodeJSON(r, &req)
	if req.ExpiresInHours <= 0 || req.ExpiresInHours > 24*30 {
		req.ExpiresInHours = 24 * 7
	}
	// The snapshot: the diagnosis summary only — no attempts' raw
	// answers, no other kid's data, no secrets.
	snapshot := map[string]any{
		"session_id": d.ID,
		"subject":    d.Subject,
		"mode":       d.Mode,
		"created_at": d.CreatedAt,
		"summary":    json.RawMessage("null"),
	}
	var sess taxonomy.Session
	if err := json.Unmarshal([]byte(d.StateJSON), &sess); err == nil {
		if summary := cat.Summarize(&sess); summary != nil {
			snapshot["summary"] = summary
		}
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build snapshot")
		return
	}
	raw, hash, err := store.NewShareToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mint token")
		return
	}
	kind := req.Kind
	if kind == "" {
		kind = "diagnosis"
	}
	user := UserFromContext(r.Context())
	expires := time.Now().Add(time.Duration(req.ExpiresInHours) * time.Hour)
	share := &model.DiagnosticShare{
		TokenHash:    hash,
		OwnerID:      d.UserID,
		SessionID:    &d.ID,
		ReportKind:   kind,
		SnapshotJSON: string(snapshotJSON),
		ExpiresAt:    &expires,
	}
	if user != nil {
		share.CreatedBy = user.ID
	}
	if err := h.store.CreateDiagnosticShare(r.Context(), share); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create share")
		return
	}
	writeJSON(w, http.StatusCreated, shareResponse{
		ShareID:   share.ID,
		URL:       "/share/taxonomy/" + raw,
		Token:     raw,
		ExpiresAt: &expires,
	})
}

// GetSharedReport serves GET /api/taxonomy/shared/{token} — public,
// read-only, snapshot-only. Expired → 410, revoked → 410, unknown →
// 404 (never a reason string — the token is the only credential).
func (h *TaxonomyHandler) GetSharedReport(w http.ResponseWriter, r *http.Request) {
	token := urlParam(r, "token")
	if token == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	share, err := h.store.GetDiagnosticShareByToken(r.Context(), token)
	if err != nil || share == nil || !store.ShareIsValid(share, time.Now()) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var snapshot json.RawMessage = json.RawMessage(share.SnapshotJSON)
	writeJSON(w, http.StatusOK, map[string]any{
		"report_kind": share.ReportKind,
		"expires_at":  share.ExpiresAt,
		"snapshot":    snapshot,
	})
}

// RevokeShare serves DELETE /api/admin/taxonomy/shares/{id}.
func (h *TaxonomyHandler) RevokeShare(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid share id")
		return
	}
	if err := h.store.RevokeDiagnosticShare(r.Context(), id); err != nil {
		if err == store.ErrDiagnosticShareNotFound {
			writeError(w, http.StatusNotFound, "share not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to revoke share")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- course generation ---

// courseGenerateRequest is POST /api/admin/taxonomy/courses/generate.
type courseGenerateRequest struct {
	UserID              int64    `json:"user_id"`
	Title               string   `json:"title"`
	Subject             string   `json:"subject"`
	DiagnosticSessionID *int64   `json:"diagnostic_session_id"`
	TopicIDs            []string `json:"topic_ids"`
}

// GenerateCourse serves POST /api/admin/taxonomy/courses/generate —
// build a draft course from a diagnostic's stable learning path
// (Summarize().Path: grade-ascending topological order) or from an
// explicit topic list. Units carry the path's KCs (unit_knowledge_tags),
// practice resources and knowledge-linked outcomes; prerequisite_unit_ids
// chain the previous unit so the unlock engine runs the path order.
func (h *TaxonomyHandler) GenerateCourse(w http.ResponseWriter, r *http.Request) {
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	var req courseGenerateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if _, err := h.store.GetUser(r.Context(), req.UserID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown user_id")
		return
	}
	// Source of the path: the diagnostic session or the explicit list.
	var pathIDs []string
	if req.DiagnosticSessionID != nil {
		d, err := h.store.GetDiagnosticSession(r.Context(), *req.DiagnosticSessionID)
		if err != nil || d == nil {
			writeError(w, http.StatusBadRequest, "unknown diagnostic_session_id")
			return
		}
		if d.UserID != req.UserID {
			writeError(w, http.StatusBadRequest, "diagnostic session belongs to another user")
			return
		}
		var sess taxonomy.Session
		if err := json.Unmarshal([]byte(d.StateJSON), &sess); err != nil {
			writeError(w, http.StatusBadRequest, "session state unavailable")
			return
		}
		summary := cat.Summarize(&sess)
		if summary == nil || len(summary.Path) == 0 {
			writeError(w, http.StatusBadRequest, "session has no learning path")
			return
		}
		for _, p := range summary.Path {
			pathIDs = append(pathIDs, p.ID)
		}
	} else {
		pathIDs = taxonomy.ResolveTargets(req.TopicIDs, cat.Aliases)
	}
	if len(pathIDs) == 0 {
		writeError(w, http.StatusBadRequest, "no topics to build from")
		return
	}
	// Group the path into units by chapter (stable order: the path is
	// already grade-ascending topological).
	type unitPlan struct {
		chapter string
		topics  []*taxonomy.Topic
	}
	var plan []unitPlan
	byChapter := map[string]int{}
	for _, id := range pathIDs {
		tp := cat.Topic(id)
		if tp == nil {
			continue
		}
		ch := tp.Chapter
		if ch == "" {
			ch = tp.Domain
		}
		idx, ok := byChapter[ch]
		if !ok {
			plan = append(plan, unitPlan{chapter: ch})
			idx = len(plan) - 1
			byChapter[ch] = idx
		}
		plan[idx].topics = append(plan[idx].topics, tp)
	}
	if len(plan) == 0 {
		writeError(w, http.StatusBadRequest, "no known topics to build from")
		return
	}
	course := &model.Course{
		UserID:      req.UserID,
		Title:       strings.TrimSpace(req.Title),
		Description: "由知识图谱学习路径生成的课程草稿",
		Status:      model.CourseStatusDraft,
	}
	if err := h.store.CreateCourse(r.Context(), course); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create course")
		return
	}
	var prevUnitID int64
	for i, up := range plan {
		unit := &model.CourseUnit{
			CourseID:        course.ID,
			Title:           up.chapter,
			Position:        i + 1,
			RequirementType: model.UnitRequirementAll,
			Published:       false,
		}
		if prevUnitID > 0 {
			unit.PrerequisiteUnitIDsJSON = encodeIDs([]int64{prevUnitID})
		} else {
			unit.PrerequisiteUnitIDsJSON = "[]"
		}
		if err := h.store.CreateUnit(r.Context(), unit); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create unit")
			return
		}
		prevUnitID = unit.ID
		for _, tp := range up.topics {
			// unit_knowledge_tags: the unit covers this KC.
			if tagID, err := h.store.TagIDByExternalRef(r.Context(), tp.ID); err == nil && tagID > 0 {
				if err := h.store.AttachUnitKnowledgeTag(r.Context(), unit.ID, tagID); err != nil {
					writeError(w, http.StatusInternalServerError, "failed to link unit tag")
					return
				}
			}
			// One practice resource per KC (the practice entry point).
			res := &model.UnitResource{
				UnitID:         unit.ID,
				Title:          "练习：" + tp.Name,
				ResourceType:   model.ResourceTypePractice,
				ContentRef:     tp.ID,
				CompletionType: model.CompletionMustSubmit,
				Position:       0,
			}
			if err := h.store.CreateResource(r.Context(), res); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to create resource")
				return
			}
			// One outcome per KC, linked through knowledge_tag_id so
			// practice and assessments roll into the profile.
			outcome := &model.CourseOutcome{
				CourseID:          course.ID,
				Title:             tp.Name,
				MasteryPoints:     80,
				CalculationMethod: model.OutcomeCalcLatest,
				RatingsJSON:       "[]",
			}
			if tagID, err := h.store.TagIDByExternalRef(r.Context(), tp.ID); err == nil && tagID > 0 {
				outcome.KnowledgeTagID = &tagID
			}
			if err := h.store.CreateOutcome(r.Context(), outcome); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to create outcome")
				return
			}
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"course_id": course.ID})
}
