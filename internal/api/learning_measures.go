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

// validate normalizes one measure request in place.
func (req *measureRequest) validate() error {
	req.Title = strings.TrimSpace(req.Title)
	req.Detail = strings.TrimSpace(req.Detail)
	if req.Title == "" || req.Detail == "" {
		return errors.New("title and detail are required")
	}
	if req.Subject == "" {
		req.Subject = "math"
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
		KidName:  kid.Name,
		Subject:  analysis.Subject,
		WeakTags: weakInputs,
		ErrorMix: distInputs,
		ParentNotes: parentNotes,
	}, fmt.Sprintf("measures:%d", analysis.ID))
	if err != nil {
		return nil, err
	}
	created := make([]model.LearningMeasure, 0, len(drafts))
	for _, d := range drafts {
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
		if d.KC != "" {
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
