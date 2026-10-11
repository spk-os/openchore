package api

import (
	"net/http"
	"time"

	"github.com/liftedkilt/openchore/internal/config"
	"github.com/liftedkilt/openchore/internal/llm"
	"github.com/liftedkilt/openchore/internal/model"
)

// runningDailyResponse is the conflict warning on the templates page: how
// many of the kid's open-ended daily/weekday schedules a stop_daily import
// would end today, so the parent sees "this will stop N daily tasks" — and
// a zero means a plain import with no schedule swap.
type runningDailyResponse struct {
	Running int `json:"running"`
}

// RunningDailySchedules reports how many of the kid's day-of-week schedules
// are still open-ended, excluding schedules of chores imported from the pack
// being previewed (switching to a pack you already run is a no-op).
func (h *ChoreHandler) RunningDailySchedules(w http.ResponseWriter, r *http.Request) {
	userID, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	today := time.Now().Format(model.DateFormat)
	n, err := h.store.DailySchedulesRunning(r.Context(), userID, today, r.URL.Query().Get("template_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count running schedules")
		return
	}
	writeJSON(w, http.StatusOK, runningDailyResponse{Running: n})
}

// ListTemplates serves the embedded chore template packs (P2-9) for the
// admin templates page. The packs ship with the binary, so this is a pure
// read of the embedded JSON — nothing touches the database.
func (h *ChoreHandler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	packs, err := config.Templates()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load templates")
		return
	}
	writeJSON(w, http.StatusOK, packs)
}

// tuneTemplateRequest is what the templates page sends: the task list it is
// showing (same order, so indexes match) and the parent's one-line request.
type tuneTemplateRequest struct {
	Tasks []llm.TemplateTuneInput `json:"tasks"`
	Tweak string                  `json:"tweak"`
}

// TuneTemplate applies a parent's one-line request ("他英语弱，英语加量")
// to one template pack with the configured AI, returning per-task overrides
// the parent confirms before importing. Nothing is saved here.
func (h *ChoreHandler) TuneTemplate(w http.ResponseWriter, r *http.Request) {
	var req tuneTemplateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Tweak == "" {
		writeError(w, http.StatusBadRequest, "tweak is required")
		return
	}
	if len(req.Tasks) == 0 {
		writeError(w, http.StatusBadRequest, "tasks are required")
		return
	}

	// The pack must exist, so a stale UI can't tune a pack that no longer
	// ships; its name grounds the model's answer.
	pack, err := findTemplate(urlParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load templates")
		return
	}
	if pack == nil {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}

	ai := h.aiSvc.AI()
	if ai == nil {
		writeError(w, http.StatusServiceUnavailable, "AI is not configured")
		return
	}
	// Indexes are positional: the client sends the same task list it is
	// showing, in order, and the model references tasks by that index.
	for i := range req.Tasks {
		req.Tasks[i].Index = i
	}
	tune, err := ai.TuneTemplate(r.Context(), pack.Name, req.Tasks, req.Tweak)
	if err != nil {
		writeError(w, http.StatusBadGateway, "AI tuning failed: "+err.Error())
		return
	}
	if tune.Changes == nil {
		tune.Changes = []llm.TemplateTuneTask{}
	}
	writeJSON(w, http.StatusOK, tune)
}

// importTemplateTask is one selected task of a pack, as the templates page
// sends it for import: fields the AI tuner or the parent may have adjusted.
type importTemplateTask struct {
	Title            string `json:"title"`
	Description      string `json:"description"`
	Icon             string `json:"icon"`
	Category         string `json:"category"`
	Points           int    `json:"points"`
	MissedPenalty    int    `json:"missed_penalty"`
	EstimatedMinutes int    `json:"estimated_minutes"`
	Days             []string `json:"days"`
	AvailableAt      string `json:"available_at"`
	DueBy            string `json:"due_by"`
	Expiry           string `json:"expiry"`
}

// importTemplateRequest imports a template pack into one kid's week: create
// the selected chores (or reuse them), then give each a schedule per day.
// Existing daily chores from a *different* pack are stopped by setting
// end_date = today (never deleted — switching back just adds a new schedule
// with start_date = today), which is the exam-week orchestration: importing
// the sprint pack pauses the school-day routine, importing the school-day
// pack brings it back.
type importTemplateRequest struct {
	TemplateID string                `json:"template_id"`
	AssignedTo int64                 `json:"assigned_to"`
	Tasks      []importTemplateTask  `json:"tasks"`
	// StopDaily stops the kid's other open-ended daily/weekday schedules
	// first (schedule swap). The UI shows the conflict warning before
	// the parent opts in.
	StopDaily bool `json:"stop_daily"`
}

// ImportTemplate imports the selected tasks of one embedded pack for one
// kid: each task becomes a chore (source="template") plus one schedule per
// expanded day.
func (h *ChoreHandler) ImportTemplate(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	var req importTemplateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AssignedTo == 0 {
		writeError(w, http.StatusBadRequest, "assigned_to is required")
		return
	}
	if len(req.Tasks) == 0 {
		writeError(w, http.StatusBadRequest, "tasks are required")
		return
	}
	if _, err := findTemplate(req.TemplateID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load templates")
		return
	}

	// Everything below mutates, and the whole import should land or not
	// land together (a half-imported pack is confusing to clean up).
	tx, err := h.store.BeginTemplateTx(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start import")
		return
	}
	defer tx.Rollback()

	today := time.Now().Format(model.DateFormat)
	if req.StopDaily {
		// The schedule queries treat end_date inclusively (end_date >= day),
		// so writing today would leave the old routines running today. Stop
		// them yesterday: the sprint pack takes over from today on.
		yesterday := time.Now().AddDate(0, 0, -1).Format(model.DateFormat)
		if _, err := tx.StopDailySchedules(r.Context(), req.AssignedTo, today, yesterday, req.TemplateID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to stop daily schedules")
			return
		}
	}

	imported := make([]model.Chore, 0, len(req.Tasks))
	for _, t := range req.Tasks {
		if t.Title == "" {
			writeError(w, http.StatusBadRequest, "task title is required")
			return
		}
		category := t.Category
		if category == "" {
			category = model.CategoryCore
		}
		if category != model.CategoryRequired && category != model.CategoryCore && category != model.CategoryBonus {
			writeError(w, http.StatusBadRequest, "category must be required, core, or bonus")
			return
		}
		days := config.ExpandDays(t.Days)
		if len(days) == 0 {
			writeError(w, http.StatusBadRequest, "task has no expandable days")
			return
		}

		chore := &model.Chore{
			Title:              t.Title,
			Description:        t.Description,
			Category:           category,
			Icon:               t.Icon,
			PointsValue:        t.Points,
			MissedPenaltyValue: t.MissedPenalty,
			EstimatedMinutes:   &t.EstimatedMinutes,
			Source:             model.SourceTemplate,
			ExternalID:         req.TemplateID,
			CreatedBy:          user.ID,
		}
		if err := tx.CreateChore(r.Context(), chore); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create chore")
			return
		}
		expiry := t.Expiry
		if expiry == "" {
			expiry = model.ExpiryBlock
		}
		var availableAt, dueBy *string
		if t.AvailableAt != "" {
			availableAt = &t.AvailableAt
		}
		if t.DueBy != "" {
			dueBy = &t.DueBy
		}
		for _, day := range days {
			schedule := &model.ChoreSchedule{
				ChoreID:          chore.ID,
				AssignedTo:       req.AssignedTo,
				AssignmentType:   model.AssignmentIndividual,
				DayOfWeek:        &day,
				AvailableAt:      availableAt,
				DueBy:            dueBy,
				ExpiryPenalty:    expiry,
				PointsMultiplier: 1.0,
				StartDate:        &today,
			}
			if err := tx.CreateSchedule(r.Context(), schedule); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to create schedule")
				return
			}
		}
		imported = append(imported, *chore)
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit import")
		return
	}

	// Read-aloud audio is generated outside the transaction (network I/O).
	if audio := h.aiSvc.Audio(); audio != nil {
		for i := range imported {
			audio.GenerateAsync(imported[i])
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"imported": len(imported),
		"chores":   imported,
	})
}

// findTemplate loads the embedded packs and returns the one with this id,
// or nil when the id is unknown.
func findTemplate(id string) (*config.Template, error) {
	packs, err := config.Templates()
	if err != nil {
		return nil, err
	}
	for i := range packs {
		if packs[i].ID == id {
			return &packs[i], nil
		}
	}
	return nil, nil
}
