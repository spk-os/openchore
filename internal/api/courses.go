package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// Course HTTP handlers (L2). Parent side: CRUD over the course tree
// (courses → units → resources), publish, clone, preview. Kid side: the
// published course list with progress, the course tree with each unit's
// state, progress reporting (the unlock engine's write path), and the
// mastery (outcomes) view. The unlock rules themselves are Canvas
// semantics: unlock_at passed AND every prerequisite unit completed AND
// published — evaluated per request from the attempts, never cached.

// CourseHandler serves the course endpoints.
type CourseHandler struct {
	store *store.Store
}

func NewCourseHandler(s *store.Store) *CourseHandler {
	return &CourseHandler{store: s}
}

// unitView is one unit of the kid's course tree: the unit, its
// resources (with the kid's attempt merged in), and the computed state.
type unitView struct {
	model.CourseUnit
	PrerequisiteUnitIDs []int64            `json:"prerequisite_unit_ids"`
	State               string             `json:"state"` // locked/unlocked/started/completed
	LockExplanation     string             `json:"lock_explanation,omitempty"`
	Resources           []resourceView     `json:"resources"`
	CompletedResources  int                `json:"completed_resources"`
	TotalResources      int                `json:"total_resources"`
}

// resourceView is one resource as the kid sees it: the resource plus
// its attempt-derived done flag.
type resourceView struct {
	model.UnitResource
	Done bool `json:"done"`
}

// courseTreeResponse is the kid-side course tree (and the admin
// preview: identical shape, same engine).
type courseTreeResponse struct {
	model.Course
	Units    []unitView  `json:"units"`
	Outcomes []outcomeView `json:"outcomes,omitempty"`
}

// outcomeView is one mastery line with the kid's rollup applied.
type outcomeView struct {
	model.CourseOutcome
	Score        *int   `json:"score,omitempty"`        // the rolled-up score, nil when no assessments yet
	Mastered     bool   `json:"mastered"`               // score >= mastery_points
	Assessments  int    `json:"assessments"`            // how many score events fed the rollup
}

// --- Kid side ---

// ListCoursesForKid serves GET /api/courses — the kid's published
// courses with progress percent (draft courses are invisible to the
// kid, edX semantics; archived even more so).
func (h *CourseHandler) ListCoursesForKid(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	courses, err := h.store.ListCourses(r.Context(), user.ID, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list courses")
		return
	}
	out := make([]courseTreeResponse, 0, len(courses))
	for _, c := range courses {
		if c.Status != model.CourseStatusPublished {
			continue
		}
		v, err := h.buildTree(r, &c, user.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to build course tree")
			return
		}
		out = append(out, *v)
	}
	writeJSON(w, http.StatusOK, out)
}

// GetCourseForKid serves GET /api/courses/{id} — the course tree with
// per-unit state and lock explanations.
func (h *CourseHandler) GetCourseForKid(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid course id")
		return
	}
	c, err := h.store.GetCourse(r.Context(), id)
	if err != nil || c == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	// Admins may preview any course (View As Learner); kids only see
	// their own published ones.
	if user.Role != model.RoleAdmin {
		if c.UserID != user.ID || c.Status != model.CourseStatusPublished {
			writeError(w, http.StatusNotFound, "course not found")
			return
		}
	}
	v, err := h.buildTree(r, c, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build course tree")
		return
	}
	if v.Status == model.CourseStatusPublished && user.Role != model.RoleAdmin {
		v.Status = model.CourseStatusPublished
	}
	writeJSON(w, http.StatusOK, v)
}

// progressRequest is the kid's report: any subset of the fact fields.
type progressRequest struct {
	ViewedSeconds *int64 `json:"viewed_seconds"`
	Score         *int   `json:"score"`
	SubmittedRef  string `json:"submitted_ref"`
}

// ReportProgress serves POST /api/resources/{id}/progress — the unlock
// engine's write path. The predicate is judged here (Canvas
// completion_requirement semantics), the attempt row is upserted, and
// the response carries the fresh unit state so the UI can flip the lock
// without a refetch.
func (h *CourseHandler) ReportProgress(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource id")
		return
	}
	var req progressRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := h.store.GetResource(r.Context(), id)
	if err != nil || res == nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	unit, err := h.store.GetUnit(r.Context(), res.UnitID)
	if err != nil || unit == nil {
		writeError(w, http.StatusNotFound, "unit not found")
		return
	}
	course, err := h.store.GetCourse(r.Context(), unit.CourseID)
	if err != nil || course == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	// Kids report progress on their own published courses; admins may
	// report on any (preview support).
	if user.Role != model.RoleAdmin && (course.UserID != user.ID || course.Status != model.CourseStatusPublished) {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	// A locked unit rejects progress: the kid must clear the
	// prerequisites first (Canvas gates ModuleItem submissions the same
	// way — the API 4xx's rather than silently banking the work).
	courseUnits, err := h.store.ListUnits(r.Context(), unit.CourseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load units")
		return
	}
	attempts, err := h.store.ListUserAttempts(r.Context(), unit.CourseID, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load attempts")
		return
	}
	attemptByID := map[int64]*model.ResourceAttempt{}
	for i := range attempts {
		attemptByID[attempts[i].ResourceID] = &attempts[i]
	}
	resourcesByUnit, err := h.unitResourceStates(r.Context(), courseUnits)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load resources")
		return
	}
	if states := h.computeUnitStates(courseUnits, resourcesByUnit, attemptByID, time.Now()); states[unit.ID] == model.UnitStateLocked {
		writeError(w, http.StatusBadRequest, "unit is locked")
		return
	}

	attempt, err := h.store.GetResourceAttempt(r.Context(), res.ID, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load attempt")
		return
	}
	if attempt == nil {
		attempt = &model.ResourceAttempt{ResourceID: res.ID, UserID: user.ID}
	}
	if req.ViewedSeconds != nil && *req.ViewedSeconds > attempt.ViewedSeconds {
		attempt.ViewedSeconds = *req.ViewedSeconds
	}
	if req.Score != nil {
		s := *req.Score
		attempt.Score = &s
	}
	if req.SubmittedRef != "" {
		attempt.SubmittedRef = req.SubmittedRef
	}
	if h.predicatePasses(res, attempt) {
		now := time.Now()
		attempt.CompletedAt = &now
	}
	if err := h.store.UpsertResourceAttempt(r.Context(), attempt); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save progress")
		return
	}

	// Recompute the owning unit's state for the response (Moodle-style
	// incremental recompute: only this unit, not the whole course).
	// The fresh attempt must feed the engine too — refetch the kid's
	// attempts so the response state reflects the write that just
	// landed, not the pre-write snapshot the lock check used.
	attempts, err = h.store.ListUserAttempts(r.Context(), unit.CourseID, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load attempts")
		return
	}
	attemptByID = map[int64]*model.ResourceAttempt{}
	for i := range attempts {
		attemptByID[attempts[i].ResourceID] = &attempts[i]
	}
	units := courseUnits
	resources := resourcesByUnit[unit.ID]
	unitStates := h.computeUnitStates(units, resourcesByUnit, attemptByID, time.Now())

	done := 0
	var resView []resourceView
	for _, rr := range resources {
		rv := resourceView{UnitResource: rr, Done: h.predicatePasses(&rr, attemptByID[rr.ID])}
		if rv.Done {
			done++
		}
		resView = append(resView, rv)
	}
	sv := unitView{
		CourseUnit:          *unit,
		PrerequisiteUnitIDs: decodeIDs(unit.PrerequisiteUnitIDsJSON),
		State:               unitStates[unit.ID],
		Resources:           resView,
		CompletedResources:  done,
		TotalResources:      len(resources),
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"attempt": attempt,
		"unit":    sv,
	})
}

// ListCourseOutcomes serves GET /api/courses/{id}/outcomes — the mastery
// view: each outcome with the kid's rolled-up score against its mastery
// line.
func (h *CourseHandler) ListCourseOutcomes(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid course id")
		return
	}
	c, err := h.store.GetCourse(r.Context(), id)
	if err != nil || c == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	if user.Role != model.RoleAdmin && (c.UserID != user.ID || c.Status != model.CourseStatusPublished) {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	outcomes, err := h.store.ListOutcomes(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load outcomes")
		return
	}
	out := make([]outcomeView, 0, len(outcomes))
	for _, o := range outcomes {
		v := outcomeView{CourseOutcome: o}
		assessments, err := h.store.ListAssessments(r.Context(), o.ID, user.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load assessments")
			return
		}
		if score, ok := h.rollupScore(&o, assessments); ok {
			v.Score = &score
			v.Mastered = score >= o.MasteryPoints
		}
		v.Assessments = len(assessments)
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// --- The unlock engine ---

// buildTree assembles the kid-side (or preview) view of one course:
// units with computed states, resources with done flags, outcomes with
// rollups, and the course-level progress percent.
func (h *CourseHandler) buildTree(r *http.Request, c *model.Course, userID int64) (*courseTreeResponse, error) {
	units, err := h.store.ListUnits(r.Context(), c.ID)
	if err != nil {
		return nil, err
	}
	attempts, err := h.store.ListUserAttempts(r.Context(), c.ID, userID)
	if err != nil {
		return nil, err
	}
	attemptByID := map[int64]*model.ResourceAttempt{}
	for i := range attempts {
		attemptByID[attempts[i].ResourceID] = &attempts[i]
	}
	resourcesByUnit, err := h.unitResourceStates(r.Context(), units)
	if err != nil {
		return nil, err
	}
	unitStates := h.computeUnitStates(units, resourcesByUnit, attemptByID, time.Now())

	v := &courseTreeResponse{Course: *c}
	completedUnits := 0
	for _, u := range units {
		resources := resourcesByUnit[u.ID]
		done := 0
		var resViews []resourceView
		for _, rr := range resources {
			rv := resourceView{UnitResource: rr, Done: h.predicatePasses(&rr, attemptByID[rr.ID])}
			if rv.Done {
				done++
			}
			resViews = append(resViews, rv)
		}
		// Resources with a predicate are the ones that gate; display-only
		// ones (completion_type NULL) never count toward the total.
		var gated int
		for _, rr := range resources {
			if rr.CompletionType != "" {
				gated++
			}
		}
		uv := unitView{
			CourseUnit:          u,
			PrerequisiteUnitIDs: decodeIDs(u.PrerequisiteUnitIDsJSON),
			State:               unitStates[u.ID],
			Resources:           resViews,
			CompletedResources:  done,
			TotalResources:      gated,
		}
		uv.LockExplanation = h.lockExplanation(&u, units, unitStates, attemptByID)
		if uv.State == model.UnitStateCompleted {
			completedUnits++
		}
		v.Units = append(v.Units, uv)
	}
	v.UnitsTotal = len(units)
	v.UnitsCompleted = completedUnits
	if len(units) > 0 {
		v.ProgressPercent = completedUnits * 100 / len(units)
	}
	outcomes, err := h.store.ListOutcomes(r.Context(), c.ID)
	if err != nil {
		return nil, err
	}
	for _, o := range outcomes {
		ov := outcomeView{CourseOutcome: o}
		assessments, err := h.store.ListAssessments(r.Context(), o.ID, userID)
		if err != nil {
			return nil, err
		}
		if score, ok := h.rollupScore(&o, assessments); ok {
			ov.Score = &score
			ov.Mastered = score >= o.MasteryPoints
		}
		ov.Assessments = len(assessments)
		v.Outcomes = append(v.Outcomes, ov)
	}
	return v, nil
}

// computeUnitStates runs the unlock rules over every unit at once. A
// unit is completed when its gated resources satisfy the requirement
// (all/one); unlocked when unlock_at has passed AND every prerequisite
// is completed AND it is published; started when unlocked and some
// (but not all) progress exists. Prerequisite cycles can't deadlock
// the loop: a unit whose prerequisites aren't all resolvable just
// stays locked.
func (h *CourseHandler) computeUnitStates(units []model.CourseUnit, resourcesByUnit map[int64][]model.UnitResource, attemptByID map[int64]*model.ResourceAttempt, now time.Time) map[int64]string {
	states := map[int64]string{}
	byID := map[int64]*model.CourseUnit{}
	for i := range units {
		byID[units[i].ID] = &units[i]
	}
	var resolve func(u *model.CourseUnit, seen map[int64]bool) string
	resolve = func(u *model.CourseUnit, seen map[int64]bool) string {
		if s, ok := states[u.ID]; ok {
			return s
		}
		if seen[u.ID] {
			return model.UnitStateLocked // cycle: never completes
		}
		seen[u.ID] = true
		defer delete(seen, u.ID)

		prereqIDs := decodeIDs(u.PrerequisiteUnitIDsJSON)
		prereqDone := true
		for _, pid := range prereqIDs {
			p, ok := byID[pid]
			if !ok {
				prereqDone = false
				continue
			}
			if resolve(p, seen) != model.UnitStateCompleted {
				prereqDone = false
			}
		}
		unlockable := prereqDone && u.Published
		if u.UnlockAt != nil && u.UnlockAt.After(now) {
			unlockable = false
		}

		completed := h.requirementMet(u, resourcesByUnit[u.ID], attemptByID)
		if completed && unlockable {
			states[u.ID] = model.UnitStateCompleted
			return states[u.ID]
		}
		if completed {
			// Done but gated (e.g. unlock_at in the future): it counts
			// as started — the work exists, the unit just isn't open.
			states[u.ID] = model.UnitStateStarted
			return states[u.ID]
		}
		if unlockable {
			if h.unitStarted(u, resourcesByUnit[u.ID], attemptByID) {
				states[u.ID] = model.UnitStateStarted
			} else {
				states[u.ID] = model.UnitStateUnlocked
			}
			return states[u.ID]
		}
		states[u.ID] = model.UnitStateLocked
		return states[u.ID]
	}
	for i := range units {
		resolve(&units[i], map[int64]bool{})
	}
	return states
}

// predicatePasses judges one resource against the kid's attempt using
// the Canvas completion_requirement cut down to four predicates:
// must_view / must_submit / min_score / min_minutes. NULL completion
// type means display-only — never done, never gating.
func (h *CourseHandler) predicatePasses(res *model.UnitResource, a *model.ResourceAttempt) bool {
	if res.CompletionType == "" {
		return false
	}
	if a == nil {
		return false
	}
	switch res.CompletionType {
	case model.CompletionMustView:
		return a.ViewedSeconds > 0
	case model.CompletionMustSubmit:
		return a.SubmittedRef != ""
	case model.CompletionMinScore:
		return a.Score != nil && res.MinScore != nil && *a.Score >= *res.MinScore
	case model.CompletionMinMinutes:
		return res.MinMinutes != nil && a.ViewedSeconds >= int64(*res.MinMinutes)*60
	}
	return false
}

// unitResourceStates is the shared loader the handlers call before
// computeUnitStates: the engine needs the resources to judge completion.
func (h *CourseHandler) unitResourceStates(ctx context.Context, units []model.CourseUnit) (map[int64][]model.UnitResource, error) {
	out := map[int64][]model.UnitResource{}
	for _, u := range units {
		resources, err := h.store.ListResources(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		out[u.ID] = resources
	}
	return out, nil
}

// unitStarted reports whether any gated resource has an attempt row
// (the "2/4" progress marker).
func (h *CourseHandler) unitStarted(u *model.CourseUnit, resources []model.UnitResource, attemptByID map[int64]*model.ResourceAttempt) bool {
	for _, rr := range resources {
		if rr.CompletionType == "" {
			continue
		}
		if a := attemptByID[rr.ID]; a != nil {
			return true
		}
	}
	return false
}

// requirementMet applies the all/one rule over the gated resources.
func (h *CourseHandler) requirementMet(u *model.CourseUnit, resources []model.UnitResource, attemptByID map[int64]*model.ResourceAttempt) bool {
	gated := 0
	done := 0
	for _, rr := range resources {
		if rr.CompletionType == "" {
			continue
		}
		gated++
		if h.predicatePasses(&rr, attemptByID[rr.ID]) {
			done++
		}
	}
	if gated == 0 {
		return false // nothing to gate on: completion never auto-fires
	}
	if u.RequirementType == model.UnitRequirementOne {
		return done >= 1
	}
	return done == gated
}

// lockExplanation renders the locked bubble's text (Canvas
// content_details.lock_explanation): name the first blocking reason —
// an unmet prerequisite by title, else the timed unlock, else the
// unpublished flag.
func (h *CourseHandler) lockExplanation(u *model.CourseUnit, units []model.CourseUnit, states map[int64]string, attemptByID map[int64]*model.ResourceAttempt) string {
	if states[u.ID] != model.UnitStateLocked {
		return ""
	}
	byID := map[int64]*model.CourseUnit{}
	for i := range units {
		byID[units[i].ID] = &units[i]
	}
	var pending []string
	for _, pid := range decodeIDs(u.PrerequisiteUnitIDsJSON) {
		if p, ok := byID[pid]; ok && states[pid] != model.UnitStateCompleted {
			pending = append(pending, p.Title)
		}
	}
	sort.Strings(pending)
	if len(pending) > 0 {
		return "先完成『" + strings.Join(pending, "』『") + "』单元后解锁"
	}
	if u.UnlockAt != nil && u.UnlockAt.After(time.Now()) {
		return "定时解锁：" + u.UnlockAt.Local().Format("2006-01-02 15:04")
	}
	if !u.Published {
		return "该单元未发布"
	}
	return "尚未解锁"
}

// rollupScore applies the outcome's calculation method to the score
// events (Canvas Outcomes): latest / highest / average / decaying
// average (Canvas's 65/35 two-most-recent split, calc_int overriding).
func (h *CourseHandler) rollupScore(o *model.CourseOutcome, assessments []model.OutcomeAssessment) (int, bool) {
	if len(assessments) == 0 {
		return 0, false
	}
	switch o.CalculationMethod {
	case model.OutcomeCalcHighest:
		max := assessments[0].Score
		for _, a := range assessments[1:] {
			if a.Score > max {
				max = a.Score
			}
		}
		return max, true
	case model.OutcomeCalcAverage:
		sum := 0
		for _, a := range assessments {
			sum += a.Score
		}
		return sum / len(assessments), true
	case model.OutcomeCalcDecayingAverage:
		if len(assessments) == 1 {
			return assessments[0].Score, true
		}
		weight := 65
		if o.CalcInt != nil && *o.CalcInt > 0 && *o.CalcInt < 100 {
			weight = *o.CalcInt
		}
		last := assessments[len(assessments)-1].Score
		var prevSum int
		for _, a := range assessments[:len(assessments)-1] {
			prevSum += a.Score
		}
		prev := prevSum / (len(assessments) - 1)
		return (last*weight + prev*(100-weight)) / 100, true
	default: // latest
		return assessments[len(assessments)-1].Score, true
	}
}

// decodeIDs parses the prerequisite JSON array, tolerating dirt.
func decodeIDs(raw string) []int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}

// --- Parent side (admin) ---

// ListCoursesAdmin serves GET /api/admin/courses — every course in the
// household (drafts included), optionally filtered by ?user_id=. Progress
// is joined in through the same unlock engine the kid side uses, so the
// parent's list shows the same numbers the kid sees (no drift).
func (h *CourseHandler) ListCoursesAdmin(w http.ResponseWriter, r *http.Request) {
	var userID int64
	if v := r.URL.Query().Get("user_id"); v != "" {
		id, err := urlParamInt64(r, "user_id")
		if err == nil {
			userID = id
		}
	}
	courses, err := h.store.ListCourses(r.Context(), userID, true)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list courses")
		return
	}
	out := make([]model.Course, 0, len(courses))
	for _, c := range courses {
		// Progress is per-owning-kid: attempts are keyed (resource, user),
		// so the engine must run against the course's user, not the admin.
		if c.UserID > 0 {
			units, err := h.store.ListUnits(r.Context(), c.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to list units")
				return
			}
			attempts, err := h.store.ListUserAttempts(r.Context(), c.ID, c.UserID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to load attempts")
				return
			}
			attemptByID := map[int64]*model.ResourceAttempt{}
			for i := range attempts {
				attemptByID[attempts[i].ResourceID] = &attempts[i]
			}
			resourcesByUnit, err := h.unitResourceStates(r.Context(), units)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to load resources")
				return
			}
			unitStates := h.computeUnitStates(units, resourcesByUnit, attemptByID, time.Now())
			c.UnitsTotal = len(units)
			for _, u := range units {
				if unitStates[u.ID] == model.UnitStateCompleted {
					c.UnitsCompleted++
				}
			}
			if len(units) > 0 {
				c.ProgressPercent = c.UnitsCompleted * 100 / len(units)
			}
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}

// courseRequest is the admin's create/update payload.
type courseRequest struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	SyllabusMD  string `json:"syllabus_md"`
	UserID      int64  `json:"user_id"`
	Status      string `json:"status"`
	IsTemplate  bool   `json:"is_template"`
}

// CreateCourseAdmin serves POST /api/admin/courses.
func (h *CourseHandler) CreateCourseAdmin(w http.ResponseWriter, r *http.Request) {
	var req courseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if req.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if req.Status != "" && req.Status != model.CourseStatusDraft &&
		req.Status != model.CourseStatusPublished && req.Status != model.CourseStatusArchived {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	c := &model.Course{
		UserID:      req.UserID,
		Title:       strings.TrimSpace(req.Title),
		Description: req.Description,
		SyllabusMD:  req.SyllabusMD,
		Status:      req.Status,
		IsTemplate:  req.IsTemplate,
	}
	if err := h.store.CreateCourse(r.Context(), c); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create course")
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// GetCourseAdmin serves GET /api/admin/courses/{id} — the full tree for
// the editor.
func (h *CourseHandler) GetCourseAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid course id")
		return
	}
	c, err := h.store.GetCourse(r.Context(), id)
	if err != nil || c == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	units, err := h.store.ListUnits(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load units")
		return
	}
	type adminUnit struct {
		model.CourseUnit
		PrerequisiteUnitIDs []int64             `json:"prerequisite_unit_ids"`
		Resources           []model.UnitResource `json:"resources"`
	}
	unitViews := make([]adminUnit, 0, len(units))
	for _, u := range units {
		resources, err := h.store.ListResources(r.Context(), u.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load resources")
			return
		}
		if resources == nil {
			resources = []model.UnitResource{}
		}
		unitViews = append(unitViews, adminUnit{
			CourseUnit:          u,
			PrerequisiteUnitIDs: decodeIDs(u.PrerequisiteUnitIDsJSON),
			Resources:           resources,
		})
	}
	outcomes, err := h.store.ListOutcomes(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load outcomes")
		return
	}
	if outcomes == nil {
		outcomes = []model.CourseOutcome{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"course":   c,
		"units":    unitViews,
		"outcomes": outcomes,
	})
}

// UpdateCourseAdmin serves PUT /api/admin/courses/{id}.
func (h *CourseHandler) UpdateCourseAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid course id")
		return
	}
	var req courseRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	c := &model.Course{
		Title:       strings.TrimSpace(req.Title),
		Description: req.Description,
		SyllabusMD:  req.SyllabusMD,
		Status:      req.Status,
	}
	if err := h.store.UpdateCourse(r.Context(), id, c); err != nil {
		if err == store.ErrCourseNotFound {
			writeError(w, http.StatusNotFound, "course not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update course")
		return
	}
	updated, _ := h.store.GetCourse(r.Context(), id)
	writeJSON(w, http.StatusOK, updated)
}

// DeleteCourseAdmin serves DELETE /api/admin/courses/{id}.
func (h *CourseHandler) DeleteCourseAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid course id")
		return
	}
	if err := h.store.DeleteCourse(r.Context(), id); err != nil {
		if err == store.ErrCourseNotFound {
			writeError(w, http.StatusNotFound, "course not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete course")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// unitRequest is the admin's create/update unit payload.
type unitRequest struct {
	Title                    string     `json:"title"`
	Position                 int        `json:"position"`
	PrerequisiteUnitIDs      []int64    `json:"prerequisite_unit_ids"`
	RequirementType          string     `json:"requirement_type"`
	RequireSequentialProgress bool      `json:"require_sequential_progress"`
	UnlockAt                 *time.Time `json:"unlock_at"`
	Published                bool       `json:"published"`
}

// encodeIDs is decodeIDs's inverse.
func encodeIDs(ids []int64) string {
	if len(ids) == 0 {
		return "[]"
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// CreateUnitAdmin serves POST /api/admin/courses/{id}/units.
func (h *CourseHandler) CreateUnitAdmin(w http.ResponseWriter, r *http.Request) {
	courseID, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid course id")
		return
	}
	c, err := h.store.GetCourse(r.Context(), courseID)
	if err != nil || c == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	var req unitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if req.RequirementType != "" && req.RequirementType != model.UnitRequirementAll && req.RequirementType != model.UnitRequirementOne {
		writeError(w, http.StatusBadRequest, "invalid requirement_type")
		return
	}
	// Prerequisites must reference units of the same course.
	existing, err := h.store.ListUnits(r.Context(), courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load units")
		return
	}
	own := map[int64]bool{}
	for _, u := range existing {
		own[u.ID] = true
	}
	for _, pid := range req.PrerequisiteUnitIDs {
		if !own[pid] {
			writeError(w, http.StatusBadRequest, "prerequisite unit does not belong to this course")
			return
		}
	}
	u := &model.CourseUnit{
		CourseID:                  courseID,
		Title:                     strings.TrimSpace(req.Title),
		Position:                  req.Position,
		PrerequisiteUnitIDsJSON:   encodeIDs(req.PrerequisiteUnitIDs),
		RequirementType:           req.RequirementType,
		RequireSequentialProgress: req.RequireSequentialProgress,
		UnlockAt:                  req.UnlockAt,
		Published:                 req.Published,
	}
	if err := h.store.CreateUnit(r.Context(), u); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create unit")
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

// UpdateUnitAdmin serves PUT /api/admin/units/{id}.
func (h *CourseHandler) UpdateUnitAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid unit id")
		return
	}
	existing, err := h.store.GetUnit(r.Context(), id)
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "unit not found")
		return
	}
	var req unitRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if req.RequirementType != "" && req.RequirementType != model.UnitRequirementAll && req.RequirementType != model.UnitRequirementOne {
		writeError(w, http.StatusBadRequest, "invalid requirement_type")
		return
	}
	siblings, err := h.store.ListUnits(r.Context(), existing.CourseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load units")
		return
	}
	own := map[int64]bool{}
	for _, u := range siblings {
		if u.ID != id {
			own[u.ID] = true
		}
	}
	for _, pid := range req.PrerequisiteUnitIDs {
		if !own[pid] {
			writeError(w, http.StatusBadRequest, "prerequisite unit does not belong to this course")
			return
		}
		// Self-prerequisite is a deadlock by construction.
		if pid == id {
			writeError(w, http.StatusBadRequest, "a unit cannot require itself")
			return
		}
	}
	u := *existing
	u.Title = strings.TrimSpace(req.Title)
	u.PrerequisiteUnitIDsJSON = encodeIDs(req.PrerequisiteUnitIDs)
	if req.RequirementType != "" {
		u.RequirementType = req.RequirementType
	}
	u.RequireSequentialProgress = req.RequireSequentialProgress
	u.UnlockAt = req.UnlockAt
	u.Published = req.Published
	if err := h.store.UpdateUnit(r.Context(), id, &u); err != nil {
		if err == store.ErrUnitNotFound {
			writeError(w, http.StatusNotFound, "unit not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update unit")
		return
	}
	writeJSON(w, http.StatusOK, &u)
}

// DeleteUnitAdmin serves DELETE /api/admin/units/{id}.
func (h *CourseHandler) DeleteUnitAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid unit id")
		return
	}
	if err := h.store.DeleteUnit(r.Context(), id); err != nil {
		if err == store.ErrUnitNotFound {
			writeError(w, http.StatusNotFound, "unit not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete unit")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// resourceRequest is the admin's create/update resource payload.
type resourceRequest struct {
	Title          string `json:"title"`
	Position       int    `json:"position"`
	ResourceType   string `json:"resource_type"`
	ContentRef     string `json:"content_ref"`
	CompletionType string `json:"completion_type"`
	MinScore       *int   `json:"min_score"`
	MinMinutes     *int   `json:"min_minutes"`
}

var validResourceTypes = map[string]bool{
	model.ResourceTypePage: true, model.ResourceTypeFile: true,
	model.ResourceTypeAudio: true, model.ResourceTypeVideo: true,
	model.ResourceTypePractice: true, model.ResourceTypeLink: true,
	model.ResourceTypeTask: true,
}

var validCompletionTypes = map[string]bool{
	model.CompletionMustView: true, model.CompletionMustSubmit: true,
	model.CompletionMinScore: true, model.CompletionMinMinutes: true,
	"": true, // display-only is a valid choice
}

func (h *CourseHandler) validateResource(req *resourceRequest) string {
	if strings.TrimSpace(req.Title) == "" {
		return "title is required"
	}
	if !validResourceTypes[req.ResourceType] {
		return "invalid resource_type"
	}
	if !validCompletionTypes[req.CompletionType] {
		return "invalid completion_type"
	}
	if req.CompletionType == model.CompletionMinScore && req.MinScore == nil {
		return "min_score requires a threshold"
	}
	if req.CompletionType == model.CompletionMinMinutes && req.MinMinutes == nil {
		return "min_minutes requires a threshold"
	}
	return ""
}

// CreateResourceAdmin serves POST /api/admin/units/{id}/resources.
func (h *CourseHandler) CreateResourceAdmin(w http.ResponseWriter, r *http.Request) {
	unitID, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid unit id")
		return
	}
	unit, err := h.store.GetUnit(r.Context(), unitID)
	if err != nil || unit == nil {
		writeError(w, http.StatusNotFound, "unit not found")
		return
	}
	var req resourceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := h.validateResource(&req); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	res := &model.UnitResource{
		UnitID:         unitID,
		Title:          strings.TrimSpace(req.Title),
		Position:       req.Position,
		ResourceType:   req.ResourceType,
		ContentRef:     req.ContentRef,
		CompletionType: req.CompletionType,
		MinScore:       req.MinScore,
		MinMinutes:     req.MinMinutes,
	}
	if err := h.store.CreateResource(r.Context(), res); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create resource")
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// UpdateResourceAdmin serves PUT /api/admin/resources/{id}.
func (h *CourseHandler) UpdateResourceAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource id")
		return
	}
	existing, err := h.store.GetResource(r.Context(), id)
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	var req resourceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := h.validateResource(&req); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	res := *existing
	res.Title = strings.TrimSpace(req.Title)
	res.ResourceType = req.ResourceType
	res.ContentRef = req.ContentRef
	res.CompletionType = req.CompletionType
	res.MinScore = req.MinScore
	res.MinMinutes = req.MinMinutes
	if err := h.store.UpdateResource(r.Context(), id, &res); err != nil {
		if err == store.ErrResourceNotFound {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update resource")
		return
	}
	writeJSON(w, http.StatusOK, &res)
}

// DeleteResourceAdmin serves DELETE /api/admin/resources/{id}.
func (h *CourseHandler) DeleteResourceAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid resource id")
		return
	}
	if err := h.store.DeleteResource(r.Context(), id); err != nil {
		if err == store.ErrResourceNotFound {
			writeError(w, http.StatusNotFound, "resource not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete resource")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// PublishCourseAdmin serves POST /api/admin/courses/{id}/publish —
// draft→published. The kid side starts seeing the course the moment
// this lands (edX semantics: the published tree is what the kid gets;
// further edits stay invisible until re-publish is a no-op here —
// edits write straight into the live tree but drafts hide the course
// as a whole).
func (h *CourseHandler) PublishCourseAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid course id")
		return
	}
	if err := h.store.SetCourseStatus(r.Context(), id, model.CourseStatusPublished); err != nil {
		if err == store.ErrCourseNotFound {
			writeError(w, http.StatusNotFound, "course not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to publish course")
		return
	}
	c, _ := h.store.GetCourse(r.Context(), id)
	writeJSON(w, http.StatusOK, c)
}

// ArchiveCourseAdmin serves POST /api/admin/courses/{id}/archive — the
// soft-finish path (course done, keep the record, hide everywhere).
func (h *CourseHandler) ArchiveCourseAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid course id")
		return
	}
	if err := h.store.SetCourseStatus(r.Context(), id, model.CourseStatusArchived); err != nil {
		if err == store.ErrCourseNotFound {
			writeError(w, http.StatusNotFound, "course not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to archive course")
		return
	}
	c, _ := h.store.GetCourse(r.Context(), id)
	writeJSON(w, http.StatusOK, c)
}

// CloneCourseAdmin serves POST /api/admin/courses/{id}/clone — the
// §5.5 reuse path: a whole-tree deep copy (units, resources,
// outcomes; attempts stay with the original kid).
func (h *CourseHandler) CloneCourseAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid course id")
		return
	}
	var req struct {
		UserID int64  `json:"user_id"`
		Title  string `json:"title"`
	}
	_ = decodeJSON(r, &req)
	if req.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	clone, err := h.store.CloneCourse(r.Context(), id, req.UserID, req.Title)
	if err != nil {
		if err == store.ErrCourseNotFound {
			writeError(w, http.StatusNotFound, "course not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to clone course")
		return
	}
	writeJSON(w, http.StatusCreated, clone)
}

// PreviewCourseAdmin serves GET/POST /api/admin/courses/preview — View As
// Learner (edX Studio's self-check): the admin names a course id and
// gets the exact tree the kid would see, states and all.
func (h *CourseHandler) PreviewCourseAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CourseID int64 `json:"course_id"`
		UserID   int64 `json:"user_id"`
	}
	// POST reads the JSON body; GET falls back to the ?course_id= query.
	if r.Method == http.MethodPost {
		if err := decodeJSON(r, &req); err != nil || req.CourseID <= 0 {
			writeError(w, http.StatusBadRequest, "course_id is required")
			return
		}
	} else if id, err := strconv.ParseInt(r.URL.Query().Get("course_id"), 10, 64); err == nil && id > 0 {
		req.CourseID = id
	} else {
		writeError(w, http.StatusBadRequest, "course_id is required")
		return
	}
	c, err := h.store.GetCourse(r.Context(), req.CourseID)
	if err != nil || c == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	// Preview the course as its owning kid (the tree's attempt rows are
	// per-user; admins have none, which would render everything locked
	// — the kid's view is the honest one).
	v, err := h.buildTree(r, c, c.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build course tree")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// --- Outcome admin ---

// CreateOutcomeAdmin serves POST /api/admin/courses/{id}/outcomes.
func (h *CourseHandler) CreateOutcomeAdmin(w http.ResponseWriter, r *http.Request) {
	courseID, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid course id")
		return
	}
	c, err := h.store.GetCourse(r.Context(), courseID)
	if err != nil || c == nil {
		writeError(w, http.StatusNotFound, "course not found")
		return
	}
	var req model.CourseOutcome
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	switch req.CalculationMethod {
	case "", model.OutcomeCalcLatest, model.OutcomeCalcHighest,
		model.OutcomeCalcAverage, model.OutcomeCalcDecayingAverage:
	default:
		writeError(w, http.StatusBadRequest, "invalid calculation_method")
		return
	}
	req.CourseID = courseID
	if err := h.store.CreateOutcome(r.Context(), &req); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create outcome")
		return
	}
	writeJSON(w, http.StatusCreated, req)
}

// ListOutcomesAdmin serves GET /api/admin/courses/{id}/outcomes.
func (h *CourseHandler) ListOutcomesAdmin(w http.ResponseWriter, r *http.Request) {
	courseID, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid course id")
		return
	}
	outcomes, err := h.store.ListOutcomes(r.Context(), courseID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load outcomes")
		return
	}
	if outcomes == nil {
		outcomes = []model.CourseOutcome{}
	}
	writeJSON(w, http.StatusOK, outcomes)
}

// DeleteOutcomeAdmin serves DELETE /api/admin/outcomes/{id}.
func (h *CourseHandler) DeleteOutcomeAdmin(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid outcome id")
		return
	}
	if err := h.store.DeleteOutcome(r.Context(), id); err != nil {
		if err == store.ErrOutcomeNotFound {
			writeError(w, http.StatusNotFound, "outcome not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete outcome")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// CreateAssessmentAdmin serves POST /api/admin/outcomes/{id}/assessments
// — the parent's manual score entry (assessment_type 'parent').
func (h *CourseHandler) CreateAssessmentAdmin(w http.ResponseWriter, r *http.Request) {
	outcomeID, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid outcome id")
		return
	}
	var req struct {
		UserID  int64  `json:"user_id"`
		Score   int    `json:"score"`
		Comment string `json:"comments"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.UserID <= 0 {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	if req.Score < 0 || req.Score > 100 {
		writeError(w, http.StatusBadRequest, "score must be 0-100")
		return
	}
	a := model.OutcomeAssessment{
		OutcomeID:       outcomeID,
		UserID:          req.UserID,
		Score:           req.Score,
		AssessmentType:  model.AssessmentTypeParent,
		Comments:        req.Comment,
	}
	if err := h.store.CreateAssessment(r.Context(), &a); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record assessment")
		return
	}
	writeJSON(w, http.StatusCreated, a)
}
