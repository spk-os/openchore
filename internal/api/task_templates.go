package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// L0 task templates: a parent saves a tuned chore as a template, lists
// them, instantiates one as a real chore (+schedule from the snapshot),
// and pushes edits to instances that haven't started. Routes live under
// /admin/task-templates — the /admin/templates namespace is taken by the
// built-in P2-9 packs.

// createTaskTemplateRequest creates a template, either from scratch or by
// saving an existing chore ("from_chore_id"). Saving from a chore is the
// primary flow: the parent tunes a real task (points, AI prompt, reference
// photo, TTS) and taps "save as template"; the schedule snapshot travels
// along so instantiation can recreate it.
type createTaskTemplateRequest struct {
	Name              string  `json:"name"`
	FromChoreID       *int64  `json:"from_chore_id"`
	Category          string  `json:"category"`
	Points            *int    `json:"points"`
	Description       string  `json:"description"`
	RequiresPhoto     *bool   `json:"requires_photo"`
	PhotoSource       string  `json:"photo_source"`
	ReferencePhotoURL *string `json:"reference_photo_url"`
	TTSAudioURL       string  `json:"tts_audio_url"`
	AIPrompt          string  `json:"ai_prompt"`
	ScheduleJSON      string  `json:"schedule_json"`
}

// CreateTaskTemplate saves a chore shape as a reusable template.
func (h *ChoreHandler) CreateTaskTemplate(w http.ResponseWriter, r *http.Request) {
	var req createTaskTemplateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	user := UserFromContext(r.Context())

	t := &model.TaskTemplate{
		Name:         req.Name,
		Category:     req.Category,
		Description:  req.Description,
		PhotoSource:  req.PhotoSource,
		TTSAudioURL:  req.TTSAudioURL,
		AIPrompt:     req.AIPrompt,
		ScheduleJSON: req.ScheduleJSON,
	}

	// Saving an existing chore: carry its tuned fields over. Explicit
	// request fields still win, so the parent can rename while saving.
	if req.FromChoreID != nil {
		chore, err := h.store.GetChore(r.Context(), *req.FromChoreID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to get chore")
			return
		}
		if chore == nil {
			writeError(w, http.StatusNotFound, "chore not found")
			return
		}
		if req.Name == "" {
			t.Name = chore.Title
		}
		if req.Category == "" {
			t.Category = chore.Category
		}
		if req.Points == nil {
			t.Points = chore.PointsValue
		}
		if req.Description == "" {
			t.Description = chore.Description
		}
		if req.RequiresPhoto == nil {
			t.RequiresPhoto = chore.RequiresPhoto
		}
		if req.PhotoSource == "" {
			t.PhotoSource = chore.PhotoSource
		}
		if req.ReferencePhotoURL == nil {
			t.ReferencePhotoURL = chore.ReferencePhotoURL
		}
		t.TTSAudioURL = firstNonEmpty(t.TTSAudioURL, chore.TTSAudioURL)
	}

	if t.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if t.Category == "" {
		t.Category = model.CategoryRequired
	}
	if !validCategory(t.Category) {
		writeError(w, http.StatusBadRequest, "category must be required, core, or bonus")
		return
	}
	if req.Points != nil {
		t.Points = *req.Points
	}
	if req.RequiresPhoto != nil {
		t.RequiresPhoto = *req.RequiresPhoto
	}
	if req.ReferencePhotoURL != nil {
		t.ReferencePhotoURL = *req.ReferencePhotoURL
	}
	if t.PhotoSource == "" {
		t.PhotoSource = model.PhotoSourceBoth
	}
	if !validPhotoSource(t.PhotoSource) {
		writeError(w, http.StatusBadRequest, "photo_source must be child, external, or both")
		return
	}

	// The schedule snapshot: taken from the source chore's schedule when
	// saving from a chore, so instantiation reproduces the cadence.
	if t.ScheduleJSON == "" && req.FromChoreID != nil {
		schedules, _ := h.store.ListSchedulesForChore(r.Context(), *req.FromChoreID)
		if len(schedules) > 0 {
			snap, err := scheduleSnapshot(schedules[0])
			if err == nil {
				t.ScheduleJSON = snap
			}
		}
	}

	t.CreatedBy = user.ID
	if err := h.store.CreateTaskTemplate(r.Context(), t); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create template")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// ListTaskTemplates lists templates; ?archived=1 includes archived ones.
func (h *ChoreHandler) ListTaskTemplates(w http.ResponseWriter, r *http.Request) {
	includeArchived := r.URL.Query().Get("archived") == "1"
	templates, err := h.store.ListTaskTemplates(r.Context(), includeArchived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list templates")
		return
	}
	if templates == nil {
		templates = []model.TaskTemplate{}
	}
	// Attach instance counts so the list can say "5 uses".
	type templateWithUsage struct {
		model.TaskTemplate
		InstanceCount int `json:"instance_count"`
		OpenInstances int `json:"open_instances"`
	}
	out := make([]templateWithUsage, 0, len(templates))
	for _, t := range templates {
		instances, _ := h.store.ListChoresByTemplate(r.Context(), t.ID)
		open, _ := h.store.CountOpenTemplateInstances(r.Context(), t.ID)
		out = append(out, templateWithUsage{TaskTemplate: t, InstanceCount: len(instances), OpenInstances: open})
	}
	writeJSON(w, http.StatusOK, out)
}

// UpdateTaskTemplate edits a template's editable fields.
func (h *ChoreHandler) UpdateTaskTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid template id")
		return
	}
	t, err := h.store.GetTaskTemplate(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get template")
		return
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	var req createTaskTemplateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != "" {
		t.Name = req.Name
	}
	if req.Category != "" {
		if !validCategory(req.Category) {
			writeError(w, http.StatusBadRequest, "category must be required, core, or bonus")
			return
		}
		t.Category = req.Category
	}
	if req.Points != nil {
		t.Points = *req.Points
	}
	if req.Description != "" {
		t.Description = req.Description
	}
	if req.RequiresPhoto != nil {
		t.RequiresPhoto = *req.RequiresPhoto
	}
	if req.PhotoSource != "" {
		if !validPhotoSource(req.PhotoSource) {
			writeError(w, http.StatusBadRequest, "photo_source must be child, external, or both")
			return
		}
		t.PhotoSource = req.PhotoSource
	}
	if req.ReferencePhotoURL != nil {
		t.ReferencePhotoURL = *req.ReferencePhotoURL
	}
	if req.TTSAudioURL != "" {
		t.TTSAudioURL = req.TTSAudioURL
	}
	if req.AIPrompt != "" {
		t.AIPrompt = req.AIPrompt
	}
	if req.ScheduleJSON != "" {
		t.ScheduleJSON = req.ScheduleJSON
	}
	if err := h.store.UpdateTaskTemplate(r.Context(), t); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update template")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ArchiveTaskTemplate archives (soft-deletes) a template.
func (h *ChoreHandler) ArchiveTaskTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid template id")
		return
	}
	if err := h.store.ArchiveTaskTemplate(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive template")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// instantiateTaskTemplateRequest turns a template into a live chore: the
// assignee, optional date overrides, and the schedule snapshot's missing
// pieces. The chore links back via template_id.
type instantiateTaskTemplateRequest struct {
	AssignedTo   int64   `json:"assigned_to"`
	SpecificDate *string `json:"specific_date"`
	AvailableAt  *string `json:"available_at"`
	DueBy        *string `json:"due_by"`
}

// InstantiateTaskTemplate creates a chore (and its schedule) from a
// template, in one step. schedule_json decides the cadence: day_of_week,
// specific_date or recurrence_interval; the request can override the date
// for one-off assignments.
func (h *ChoreHandler) InstantiateTaskTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid template id")
		return
	}
	t, err := h.store.GetTaskTemplate(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get template")
		return
	}
	if t == nil || t.IsArchived {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	var req instantiateTaskTemplateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AssignedTo == 0 {
		writeError(w, http.StatusBadRequest, "assigned_to is required")
		return
	}
	if _, err := h.store.GetUser(r.Context(), req.AssignedTo); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get user")
		return
	}

	chore := &model.Chore{
		Title:             t.Name,
		Description:       t.Description,
		Category:          t.Category,
		PointsValue:       t.Points,
		RequiresApproval:  true, // template chores start parent-gated
		RequiresPhoto:     t.RequiresPhoto,
		PhotoSource:       t.PhotoSource,
		ReferencePhotoURL: t.ReferencePhotoURL,
		TTSAudioURL:       t.TTSAudioURL,
		Source:            model.SourceManual,
		TemplateID:        &t.ID,
		CreatedBy:         UserFromContext(r.Context()).ID,
	}
	if err := h.store.CreateChore(r.Context(), chore); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create chore")
		return
	}

	schedule, err := h.scheduleFromSnapshot(r.Context(), t, chore.ID, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"chore":    chore,
		"schedule": schedule,
	})
}

// SyncTaskTemplate pushes template edits to instances the parent picked
// (or all open instances when the request lists none). Instances with any
// completion are never touched — the kid's history is not the template's.
func (h *ChoreHandler) SyncTaskTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid template id")
		return
	}
	t, err := h.store.GetTaskTemplate(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get template")
		return
	}
	if t == nil {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	var req struct {
		InstanceIDs []int64 `json:"instance_ids"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	targets := req.InstanceIDs
	if len(targets) == 0 {
		instances, err := h.store.ListChoresByTemplate(r.Context(), t.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list instances")
			return
		}
		for _, c := range instances {
			targets = append(targets, c.ID)
		}
	}

	updated, err := h.store.SyncTemplateToInstances(r.Context(), t.ID, targets, *t)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to sync template")
		return
	}
	if updated == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"updated": 0, "message": "no open instances to sync"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
}

// scheduleSnapshot serializes a schedule's cadence fields for a template.
func scheduleSnapshot(s model.ChoreSchedule) (string, error) {
	snap := map[string]any{}
	if s.DayOfWeek != nil {
		snap["day_of_week"] = *s.DayOfWeek
	}
	if s.SpecificDate != nil {
		snap["specific_date"] = *s.SpecificDate
	}
	if s.RecurrenceInterval != nil {
		snap["recurrence_interval"] = *s.RecurrenceInterval
	}
	if s.RecurrenceStart != nil {
		snap["recurrence_start"] = *s.RecurrenceStart
	}
	if s.DueBy != nil {
		snap["due_by"] = *s.DueBy
	}
	if s.AvailableAt != nil {
		snap["available_at"] = *s.AvailableAt
	}
	if s.StartDate != nil {
		snap["start_date"] = *s.StartDate
	}
	if s.EndDate != nil {
		snap["end_date"] = *s.EndDate
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// scheduleFromSnapshot rebuilds a schedule from the template's snapshot,
// letting the instantiation request override the date fields.
func (h *ChoreHandler) scheduleFromSnapshot(ctx context.Context, t *model.TaskTemplate, choreID int64, req instantiateTaskTemplateRequest) (*model.ChoreSchedule, error) {
	var snap map[string]any
	if t.ScheduleJSON != "" && t.ScheduleJSON != "{}" {
		if err := json.Unmarshal([]byte(t.ScheduleJSON), &snap); err != nil {
			return nil, errors.New("invalid schedule_json on template")
		}
	}
	schedule := &model.ChoreSchedule{
		ChoreID:          choreID,
		AssignedTo:       req.AssignedTo,
		AssignmentType:   model.AssignmentIndividual,
		PointsMultiplier: 1.0,
		ExpiryPenalty:    model.ExpiryBlock,
	}
	if v, ok := snap["day_of_week"].(float64); ok {
		dow := int(v)
		schedule.DayOfWeek = &dow
	}
	if v, ok := snap["recurrence_interval"].(float64); ok {
		ri := int(v)
		if ri < 1 {
			return nil, errors.New("recurrence_interval must be >= 1")
		}
		schedule.RecurrenceInterval = &ri
		rs, _ := snap["recurrence_start"].(string)
		if rs == "" {
			rs = time.Now().Format(model.DateFormat)
		}
		schedule.RecurrenceStart = &rs
	}
	if v, ok := snap["available_at"].(string); ok && v != "" {
		schedule.AvailableAt = &v
	}
	if v, ok := snap["due_by"].(string); ok && v != "" {
		schedule.DueBy = &v
	}
	if v, ok := snap["start_date"].(string); ok && v != "" {
		schedule.StartDate = &v
	}
	if v, ok := snap["end_date"].(string); ok && v != "" {
		schedule.EndDate = &v
	}
	// Request overrides win over the snapshot.
	if req.SpecificDate != nil && *req.SpecificDate != "" {
		schedule.SpecificDate = req.SpecificDate
	}
	if req.AvailableAt != nil && *req.AvailableAt != "" {
		schedule.AvailableAt = req.AvailableAt
	}
	if req.DueBy != nil && *req.DueBy != "" {
		schedule.DueBy = req.DueBy
	}
	// A template with no cadence and no override lands on today: the
	// parent instantiated it for a reason.
	if schedule.DayOfWeek == nil && schedule.SpecificDate == nil && schedule.RecurrenceInterval == nil {
		today := time.Now().Format(model.DateFormat)
		schedule.SpecificDate = &today
	}
	if err := h.store.CreateSchedule(ctx, schedule); err != nil {
		return nil, errors.New("failed to create schedule")
	}
	return schedule, nil
}

func validCategory(c string) bool {
	return c == model.CategoryRequired || c == model.CategoryCore || c == model.CategoryBonus
}

func validPhotoSource(p string) bool {
	return p == model.PhotoSourceChild || p == model.PhotoSourceExternal || p == model.PhotoSourceBoth
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var _ = store.ErrTemplateNotFound // keep the store error import honest
