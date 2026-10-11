package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/liftedkilt/openchore/internal/config"
	"github.com/liftedkilt/openchore/internal/llm"
	"github.com/liftedkilt/openchore/internal/model"
)

// The parent Copilot (P3-10/11, S'moresUp Frankie & Nori patterns): one
// sentence or one photographed school notice becomes a task-plan draft the
// parent reviews card by card. This endpoint only drafts — applying a card
// goes through the same chores/schedules APIs a parent uses by hand, so the
// AI never writes to the database.

// copilotContextRequest is the input of a draft: the target kid plus
// either a free-form request (mode "text") or an uploaded flyer photo
// (mode "flyer", photo_url pointing at an /uploads/ path).
type copilotContextRequest struct {
	AssignedTo int64  `json:"assigned_to"`
	Mode       string `json:"mode"`
	Request    string `json:"request"`
	PhotoURL   string `json:"photo_url"`
}

// DraftWithCopilot drafts a task plan for one kid with the configured AI.
// The household context (existing chores, reward ladder) is gathered server
// side and injected into the prompt, so the model's point suggestions stay
// anchored to this family's economy no matter what the client sends.
func (h *ChoreHandler) DraftWithCopilot(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	var req copilotContextRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AssignedTo == 0 {
		writeError(w, http.StatusBadRequest, "assigned_to is required")
		return
	}
	mode := req.Mode
	if mode == "" {
		if req.PhotoURL != "" {
			mode = "flyer"
		} else {
			mode = "text"
		}
	}
	if mode != "text" && mode != "flyer" {
		writeError(w, http.StatusBadRequest, "mode must be text or flyer")
		return
	}
	if mode == "text" && strings.TrimSpace(req.Request) == "" {
		writeError(w, http.StatusBadRequest, "request is required")
		return
	}
	if mode == "flyer" && req.PhotoURL == "" {
		writeError(w, http.StatusBadRequest, "photo_url is required")
		return
	}

	kid, err := h.store.GetUser(r.Context(), req.AssignedTo)
	if err != nil || kid == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	ai := h.aiSvc.AI()
	if ai == nil {
		writeError(w, http.StatusServiceUnavailable, "AI is not configured")
		return
	}

	ctxInfo, err := h.copilotContext(r, req.AssignedTo)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to gather household context")
		return
	}

	var draft *llm.CopilotDraft
	if mode == "flyer" {
		photoPath, err := resolveUploadPath(req.PhotoURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid photo_url")
			return
		}
		draft, err = ai.DraftFromFlyer(r.Context(), *ctxInfo, photoPath)
	} else {
		draft, err = ai.DraftFromText(r.Context(), *ctxInfo, strings.TrimSpace(req.Request))
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "AI drafting failed: "+err.Error())
		return
	}

	// Dedup against the household's existing chores before the cards reach
	// the parent (design: "草稿页永不静默创建重复任务"). A near-duplicate is
	// dropped from tasks and named in notes.
	h.dedupDraft(r, draft)

	// Nothing drafted at all: still a 200 with an empty plan so the drawer
	// can explain (the model may have judged the request off-topic).
	writeJSON(w, http.StatusOK, copilotDraftResponse{
		CopilotDraft: draft,
		Mode:         mode,
		PhotoURL:     req.PhotoURL,
		By:           user.ID,
	})
}

// copilotDraftResponse wraps the draft with the drawer's context.
type copilotDraftResponse struct {
	*llm.CopilotDraft
	Mode     string `json:"mode"`
	PhotoURL string `json:"photo_url,omitempty"`
	By       int64  `json:"by"`
}

// copilotContext gathers what the prompt needs: the kid's name, their
// existing chore titles (dedup awareness) and the reward price ladder
// (the pricing anchor). Rewards come back sorted by cost so the ladder
// reads low → high in the prompt.
func (h *ChoreHandler) copilotContext(r *http.Request, userID int64) (*llm.CopilotContext, error) {
	user, err := h.store.GetUser(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	chores, err := h.store.ListChores(r.Context())
	if err != nil {
		return nil, err
	}
	titles := make([]string, 0, len(chores))
	for _, c := range chores {
		titles = append(titles, c.Title)
	}
	rewards, err := h.store.ListRewards(r.Context(), true)
	if err != nil {
		return nil, err
	}
	sort.Slice(rewards, func(i, j int) bool { return rewards[i].Cost < rewards[j].Cost })
	ladder := make([]string, 0, len(rewards))
	for _, rw := range rewards {
		ladder = append(ladder, fmt.Sprintf("%s %d分", rw.Name, rw.Cost))
	}
	return &llm.CopilotContext{
		KidName:         user.Name,
		ExistingChores:  titles,
		RewardLadder:    ladder,
	}, nil
}

// dedupDraft drops drafted tasks whose titles are near-duplicates of an
// existing chore in the household (normalized containment either way, the
// light "标题编辑距离/包含关系" check from the design) and notes each skip.
func (h *ChoreHandler) dedupDraft(r *http.Request, draft *llm.CopilotDraft) {
	chores, err := h.store.ListChores(r.Context())
	if err != nil || len(draft.Tasks) == 0 {
		return
	}
	var existing []string
	for _, c := range chores {
		existing = append(existing, c.Title)
	}
	kept := draft.Tasks[:0]
	for _, task := range draft.Tasks {
		if match := similarExisting(task.Title, existing); match != "" {
			draft.Notes = append(draft.Notes, fmt.Sprintf("与现有任务「%s」重复，已跳过「%s」", match, task.Title))
			continue
		}
		kept = append(kept, task)
	}
	draft.Tasks = kept
}

// similarExisting returns the existing title the candidate duplicates, or
// "". Containment either way after folding case/space/punctuation counts as
// a duplicate — a fuzzy edit distance would over-drop ("读英语" vs "读语文").
func similarExisting(candidate string, existing []string) string {
	norm := copilotNormalize(candidate)
	if norm == "" {
		return ""
	}
	for _, title := range existing {
		n := copilotNormalize(title)
		if n == "" {
			continue
		}
		if n == norm || strings.Contains(n, norm) || strings.Contains(norm, n) {
			return title
		}
	}
	return ""
}

// copilotNormalize folds a title for comparison: lowercase, ASCII spaces,
// no spaces at all, no common punctuation. Chinese titles carry meaning in
// every character, so spaces between CJK runs are dropped too.
func copilotNormalize(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 0x4e00 && r <= 0x9fff:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// --- Applying a draft (P3-10 step 5: same path as a manual create) ---

// copilotApplyTask is one confirmed card of a draft, as the drawer sends
// it: the fields the parent may have edited inline (points, days) plus the
// fields the model drafted.
type copilotApplyTask struct {
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	Category         string   `json:"category"`
	Icon             string   `json:"icon"`
	PointsValue      int      `json:"points_value"`
	MissedPenalty    int      `json:"missed_penalty"`
	EstimatedMinutes int      `json:"estimated_minutes"`
	Days             []string `json:"days"`
	Date             string   `json:"date"`
	DueBy            string   `json:"due_by"`
	AvailableAt      string   `json:"available_at"`
	Expiry           string   `json:"expiry"`
}

type copilotApplyRequest struct {
	AssignedTo int64               `json:"assigned_to"`
	Source     string              `json:"source"`
	Tasks      []copilotApplyTask  `json:"tasks"`
}

// ApplyCopilotDraft lands the parent-confirmed cards: each becomes a chore
// (source="copilot" or "flyer", external_id carrying the draft date) plus
// one schedule per expanded day — or a single specific-date schedule for a
// dated one-off (春游准备). Everything runs in the template-import
// transaction, so a bad card aborts the whole apply, and applying goes
// through the same store paths a manual create does.
func (h *ChoreHandler) ApplyCopilotDraft(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	var req copilotApplyRequest
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
	source := req.Source
	if source == "" {
		source = model.SourceCopilot
	}
	if source != model.SourceCopilot && source != model.SourceFlyer {
		writeError(w, http.StatusBadRequest, "source must be copilot or flyer")
		return
	}
	if _, err := h.store.GetUser(r.Context(), req.AssignedTo); err != nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	tx, err := h.store.BeginTemplateTx(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start apply")
		return
	}
	defer tx.Rollback()

	today := time.Now().Format(model.DateFormat)
	applied := make([]model.Chore, 0, len(req.Tasks))
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
		expiry := t.Expiry
		if expiry == "" {
			expiry = model.ExpiryBlock
		}
		if expiry != model.ExpiryBlock && expiry != model.ExpiryNoPoints && expiry != model.ExpiryPenalty {
			writeError(w, http.StatusBadRequest, "expiry must be block, no_points, or penalty")
			return
		}
		if t.PointsValue < 1 || t.PointsValue > 20 {
			writeError(w, http.StatusBadRequest, "points_value must be between 1 and 20")
			return
		}

		chore := &model.Chore{
			Title:              t.Title,
			Description:        t.Description,
			Category:           category,
			Icon:               t.Icon,
			PointsValue:        t.PointsValue,
			MissedPenaltyValue: t.MissedPenalty,
			EstimatedMinutes:   &t.EstimatedMinutes,
			Source:             source,
			ExternalID:         today,
			CreatedBy:          user.ID,
		}
		if err := tx.CreateChore(r.Context(), chore); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create chore")
			return
		}

		var availableAt, dueBy *string
		if t.AvailableAt != "" {
			availableAt = &t.AvailableAt
		}
		if t.DueBy != "" {
			dueBy = &t.DueBy
		}

		// A dated one-off (flyer deadlines) becomes a single
		// specific-date schedule; a pattern expands to day-of-week rows.
		if t.Date != "" && len(t.Days) == 0 {
			schedule := &model.ChoreSchedule{
				ChoreID:          chore.ID,
				AssignedTo:       req.AssignedTo,
				AssignmentType:   model.AssignmentIndividual,
				SpecificDate:     &t.Date,
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
		} else {
			days := config.ExpandDays(t.Days)
			if len(days) == 0 {
				days = []int{int(time.Now().Weekday())}
			}
			for _, day := range days {
				day := day
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
		}
		applied = append(applied, *chore)
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit apply")
		return
	}

	// Read-aloud audio is generated outside the transaction (network I/O).
	if audio := h.aiSvc.Audio(); audio != nil {
		for i := range applied {
			audio.GenerateAsync(applied[i])
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"applied": len(applied),
		"chores":  applied,
	})
}
