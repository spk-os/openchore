package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/liftedkilt/openchore/internal/model"
)

// Course system (L2) — Canvas Modules mapped onto SQLite. This file owns
// the CRUD plus the read side of the unlock engine; the engine's rule
// (a unit unlocks when unlock_at has passed AND every prerequisite unit
// is completed AND the unit is published) is evaluated per request in
// the API layer from the trees this store returns. Attempts are the
// facts: UNIQUE(resource, user), overwritten on retry.

var (
	ErrCourseNotFound   = errors.New("course not found")
	ErrUnitNotFound     = errors.New("course unit not found")
	ErrResourceNotFound = errors.New("unit resource not found")
	ErrOutcomeNotFound  = errors.New("course outcome not found")
)

const courseColumns = `id, user_id, title, description, syllabus_md, status, is_template, created_at, updated_at`

func scanCourse(row interface{ Scan(...any) error }) (*model.Course, error) {
	c := &model.Course{}
	var isTemplate int
	if err := row.Scan(&c.ID, &c.UserID, &c.Title, &c.Description, &c.SyllabusMD, &c.Status,
		&isTemplate, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.IsTemplate = isTemplate != 0
	return c, nil
}

// CreateCourse inserts a new course (status draft unless the caller says
// otherwise).
func (s *Store) CreateCourse(ctx context.Context, c *model.Course) error {
	isTemplate := boolToInt(c.IsTemplate)
	status := c.Status
	if status == "" {
		status = model.CourseStatusDraft
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO courses (user_id, title, description, syllabus_md, status, is_template)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		c.UserID, c.Title, c.Description, c.SyllabusMD, status, isTemplate)
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	c.Status = status
	return nil
}

// GetCourse fetches one course by id.
func (s *Store) GetCourse(ctx context.Context, id int64) (*model.Course, error) {
	c, err := scanCourse(s.db.QueryRowContext(ctx,
		`SELECT `+courseColumns+` FROM courses WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// ListCourses returns courses. userID > 0 filters per kid (the admin
// list passes the household view by leaving it 0 — every course in the
// family, template or not); includeArchived toggles the archived cut.
func (s *Store) ListCourses(ctx context.Context, userID int64, includeArchived bool) ([]model.Course, error) {
	q := `SELECT ` + courseColumns + ` FROM courses`
	conds := []string{}
	args := []any{}
	if userID > 0 {
		conds = append(conds, "user_id = ?")
		args = append(args, userID)
	}
	if !includeArchived {
		conds = append(conds, "status != 'archived'")
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Course
	for rows.Next() {
		c, err := scanCourse(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// UpdateCourse patches a course's editable fields.
func (s *Store) UpdateCourse(ctx context.Context, id int64, c *model.Course) error {
	status := c.Status
	if status == "" {
		status = model.CourseStatusDraft
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE courses SET title = ?, description = ?, syllabus_md = ?, status = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		c.Title, c.Description, c.SyllabusMD, status, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrCourseNotFound
	}
	return nil
}

// SetCourseStatus moves a course between draft/published/archived. The
// kid side only ever sees published courses (edX semantics).
func (s *Store) SetCourseStatus(ctx context.Context, id int64, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE courses SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrCourseNotFound
	}
	return nil
}

// DeleteCourse removes a course (units/resources/attempts cascade).
func (s *Store) DeleteCourse(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM courses WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrCourseNotFound
	}
	return nil
}

// --- Units ---

const unitColumns = `id, course_id, title, position, prerequisite_unit_ids, requirement_type,
	require_sequential_progress, unlock_at, published`

func scanUnit(row interface{ Scan(...any) error }) (*model.CourseUnit, error) {
	u := &model.CourseUnit{}
	var unlockAt sql.NullTime
	var seq, published int
	if err := row.Scan(&u.ID, &u.CourseID, &u.Title, &u.Position, &u.PrerequisiteUnitIDsJSON,
		&u.RequirementType, &seq, &unlockAt, &published); err != nil {
		return nil, err
	}
	u.RequireSequentialProgress = seq != 0
	if unlockAt.Valid {
		t := unlockAt.Time
		u.UnlockAt = &t
	}
	u.Published = published != 0
	return u, nil
}

// CreateUnit appends a unit to a course at the given (or next) position.
func (s *Store) CreateUnit(ctx context.Context, u *model.CourseUnit) error {
	reqType := u.RequirementType
	if reqType == "" {
		reqType = model.UnitRequirementAll
	}
	prereq := u.PrerequisiteUnitIDsJSON
	if prereq == "" {
		prereq = "[]"
	}
	position := u.Position
	if position <= 0 {
		var maxPos int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(position), 0) FROM course_units WHERE course_id = ?`, u.CourseID).Scan(&maxPos); err != nil {
			return err
		}
		position = maxPos + 1
	}
	var unlock any
	if u.UnlockAt != nil {
		unlock = dbTime(*u.UnlockAt)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO course_units (course_id, title, position, prerequisite_unit_ids, requirement_type,
		 require_sequential_progress, unlock_at, published)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.CourseID, u.Title, position, prereq, reqType, boolToInt(u.RequireSequentialProgress),
		unlock, boolToInt(u.Published))
	if err != nil {
		return err
	}
	u.ID, _ = res.LastInsertId()
	u.Position = position
	u.RequirementType = reqType
	u.PrerequisiteUnitIDsJSON = prereq
	return nil
}

// GetUnit fetches one unit by id.
func (s *Store) GetUnit(ctx context.Context, id int64) (*model.CourseUnit, error) {
	u, err := scanUnit(s.db.QueryRowContext(ctx,
		`SELECT `+unitColumns+` FROM course_units WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// ListUnits returns a course's units in position order.
func (s *Store) ListUnits(ctx context.Context, courseID int64) ([]model.CourseUnit, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+unitColumns+` FROM course_units WHERE course_id = ? ORDER BY position`, courseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.CourseUnit
	for rows.Next() {
		u, err := scanUnit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// UpdateUnit patches a unit's editable fields (title, prerequisites,
// requirement type, unlock time, published flag).
func (s *Store) UpdateUnit(ctx context.Context, id int64, u *model.CourseUnit) error {
	reqType := u.RequirementType
	if reqType == "" {
		reqType = model.UnitRequirementAll
	}
	prereq := u.PrerequisiteUnitIDsJSON
	if prereq == "" {
		prereq = "[]"
	}
	var unlock any
	if u.UnlockAt != nil {
		unlock = dbTime(*u.UnlockAt)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE course_units SET title = ?, prerequisite_unit_ids = ?, requirement_type = ?,
		 require_sequential_progress = ?, unlock_at = ?, published = ? WHERE id = ?`,
		u.Title, prereq, reqType, boolToInt(u.RequireSequentialProgress), unlock,
		boolToInt(u.Published), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUnitNotFound
	}
	return nil
}

// DeleteUnit removes one unit (resources and attempts cascade).
func (s *Store) DeleteUnit(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM course_units WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUnitNotFound
	}
	return nil
}

// --- Resources ---

const resourceColumns = `id, unit_id, title, position, resource_type, content_ref,
	completion_type, min_score, min_minutes`

func scanResource(row interface{ Scan(...any) error }) (*model.UnitResource, error) {
	r := &model.UnitResource{}
	var completionType sql.NullString
	var minScore, minMinutes sql.NullInt64
	if err := row.Scan(&r.ID, &r.UnitID, &r.Title, &r.Position, &r.ResourceType, &r.ContentRef,
		&completionType, &minScore, &minMinutes); err != nil {
		return nil, err
	}
	if completionType.Valid {
		r.CompletionType = completionType.String
	}
	if minScore.Valid {
		v := int(minScore.Int64)
		r.MinScore = &v
	}
	if minMinutes.Valid {
		v := int(minMinutes.Int64)
		r.MinMinutes = &v
	}
	return r, nil
}

// CreateResource appends a resource to a unit.
func (s *Store) CreateResource(ctx context.Context, r *model.UnitResource) error {
	position := r.Position
	if position <= 0 {
		var maxPos int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(position), 0) FROM unit_resources WHERE unit_id = ?`, r.UnitID).Scan(&maxPos); err != nil {
			return err
		}
		position = maxPos + 1
	}
	var completionType, minScore, minMinutes any
	if r.CompletionType != "" {
		completionType = r.CompletionType
	}
	if r.MinScore != nil {
		minScore = *r.MinScore
	}
	if r.MinMinutes != nil {
		minMinutes = *r.MinMinutes
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO unit_resources (unit_id, title, position, resource_type, content_ref,
		 completion_type, min_score, min_minutes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.UnitID, r.Title, position, r.ResourceType, r.ContentRef, completionType, minScore, minMinutes)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	r.Position = position
	return nil
}

// GetResource fetches one resource by id.
func (s *Store) GetResource(ctx context.Context, id int64) (*model.UnitResource, error) {
	r, err := scanResource(s.db.QueryRowContext(ctx,
		`SELECT `+resourceColumns+` FROM unit_resources WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// ListResources returns a unit's resources in position order.
func (s *Store) ListResources(ctx context.Context, unitID int64) ([]model.UnitResource, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+resourceColumns+` FROM unit_resources WHERE unit_id = ? ORDER BY position`, unitID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.UnitResource
	for rows.Next() {
		r, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// UpdateResource patches a resource's editable fields.
func (s *Store) UpdateResource(ctx context.Context, id int64, r *model.UnitResource) error {
	var completionType, minScore, minMinutes any
	if r.CompletionType != "" {
		completionType = r.CompletionType
	}
	if r.MinScore != nil {
		minScore = *r.MinScore
	}
	if r.MinMinutes != nil {
		minMinutes = *r.MinMinutes
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE unit_resources SET title = ?, resource_type = ?, content_ref = ?,
		 completion_type = ?, min_score = ?, min_minutes = ? WHERE id = ?`,
		r.Title, r.ResourceType, r.ContentRef, completionType, minScore, minMinutes, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrResourceNotFound
	}
	return nil
}

// DeleteResource removes one resource (attempts cascade).
func (s *Store) DeleteResource(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM unit_resources WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrResourceNotFound
	}
	return nil
}

// --- Attempts (the engine's fact table) ---

// UpsertResourceAttempt merges a progress report into the kid's attempt
// row: viewed_seconds/score/submitted_ref overwrite, completed_at flips
// when the predicate passes (the API layer judges; this just persists).
// UNIQUE(resource_id, user_id) makes a retry an update, not a new row.
func (s *Store) UpsertResourceAttempt(ctx context.Context, a *model.ResourceAttempt) error {
	var score, submitted, completed any
	if a.Score != nil {
		score = *a.Score
	}
	if a.SubmittedRef != "" {
		submitted = a.SubmittedRef
	}
	if a.CompletedAt != nil {
		completed = dbTime(*a.CompletedAt)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO resource_attempts (resource_id, user_id, viewed_seconds, submitted_ref, score, completed_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(resource_id, user_id) DO UPDATE SET
		   viewed_seconds = MAX(viewed_seconds, excluded.viewed_seconds),
		   submitted_ref = COALESCE(NULLIF(excluded.submitted_ref, ''), submitted_ref),
		   score = COALESCE(excluded.score, score),
		   completed_at = COALESCE(excluded.completed_at, completed_at)`,
		a.ResourceID, a.UserID, a.ViewedSeconds, submitted, score, completed)
	return err
}

// GetResourceAttempt fetches one kid's attempt row for a resource.
func (s *Store) GetResourceAttempt(ctx context.Context, resourceID, userID int64) (*model.ResourceAttempt, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, resource_id, user_id, viewed_seconds, submitted_ref, score, completed_at, created_at
		 FROM resource_attempts WHERE resource_id = ? AND user_id = ?`, resourceID, userID)
	a := &model.ResourceAttempt{}
	var submitted sql.NullString
	var score sql.NullInt64
	var completed sql.NullTime
	if err := row.Scan(&a.ID, &a.ResourceID, &a.UserID, &a.ViewedSeconds, &submitted, &score, &completed, &a.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if submitted.Valid {
		a.SubmittedRef = submitted.String
	}
	if score.Valid {
		v := int(score.Int64)
		a.Score = &v
	}
	if completed.Valid {
		t := completed.Time
		a.CompletedAt = &t
	}
	return a, nil
}

// ListUserAttempts returns every attempt row a kid has in one course's
// resource set — the one query the unlock engine's per-request state
// build starts from.
func (s *Store) ListUserAttempts(ctx context.Context, courseID, userID int64) ([]model.ResourceAttempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.id, a.resource_id, a.user_id, a.viewed_seconds, a.submitted_ref, a.score, a.completed_at, a.created_at
		 FROM resource_attempts a
		 JOIN unit_resources r ON r.id = a.resource_id
		 JOIN course_units u ON u.id = r.unit_id
		 WHERE u.course_id = ? AND a.user_id = ?`, courseID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ResourceAttempt
	for rows.Next() {
		a := &model.ResourceAttempt{}
		var submitted sql.NullString
		var score sql.NullInt64
		var completed sql.NullTime
		if err := rows.Scan(&a.ID, &a.ResourceID, &a.UserID, &a.ViewedSeconds, &submitted, &score, &completed, &a.CreatedAt); err != nil {
			return nil, err
		}
		if submitted.Valid {
			a.SubmittedRef = submitted.String
		}
		if score.Valid {
			v := int(score.Int64)
			a.Score = &v
		}
		if completed.Valid {
			t := completed.Time
			a.CompletedAt = &t
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// --- Outcomes ---

// CreateOutcome adds a mastery line to a course.
func (s *Store) CreateOutcome(ctx context.Context, o *model.CourseOutcome) error {
	method := o.CalculationMethod
	if method == "" {
		method = model.OutcomeCalcLatest
	}
	mastery := o.MasteryPoints
	if mastery <= 0 {
		mastery = 80
	}
	ratings := o.RatingsJSON
	if ratings == "" {
		ratings = "[]"
	}
	var calcInt any
	if o.CalcInt != nil {
		calcInt = *o.CalcInt
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO course_outcomes (course_id, title, mastery_points, calculation_method, calc_int, ratings_json)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		o.CourseID, o.Title, mastery, method, calcInt, ratings)
	if err != nil {
		return err
	}
	o.ID, _ = res.LastInsertId()
	o.MasteryPoints = mastery
	o.CalculationMethod = method
	o.RatingsJSON = ratings
	return nil
}

// ListOutcomes returns a course's mastery lines.
func (s *Store) ListOutcomes(ctx context.Context, courseID int64) ([]model.CourseOutcome, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, course_id, title, mastery_points, calculation_method, calc_int, ratings_json
		 FROM course_outcomes WHERE course_id = ? ORDER BY id`, courseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.CourseOutcome
	for rows.Next() {
		o := &model.CourseOutcome{}
		var calcInt sql.NullInt64
		if err := rows.Scan(&o.ID, &o.CourseID, &o.Title, &o.MasteryPoints, &o.CalculationMethod, &calcInt, &o.RatingsJSON); err != nil {
			return nil, err
		}
		if calcInt.Valid {
			v := int(calcInt.Int64)
			o.CalcInt = &v
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// DeleteOutcome removes one mastery line (assessments cascade).
func (s *Store) DeleteOutcome(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM course_outcomes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrOutcomeNotFound
	}
	return nil
}

// CreateAssessment appends one score event to an outcome.
func (s *Store) CreateAssessment(ctx context.Context, a *model.OutcomeAssessment) error {
	var artifactID any
	if a.ArtifactID != nil {
		artifactID = *a.ArtifactID
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO outcome_assessments (outcome_id, user_id, score, assessment_type, artifact_type, artifact_id, comments)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.OutcomeID, a.UserID, a.Score, a.AssessmentType, a.ArtifactType, artifactID, a.Comments)
	if err != nil {
		return err
	}
	a.ID, _ = res.LastInsertId()
	return nil
}

// ListAssessments returns one kid's score events for an outcome, oldest
// first (the rollup methods need the order; Canvas sorts the same way).
func (s *Store) ListAssessments(ctx context.Context, outcomeID, userID int64) ([]model.OutcomeAssessment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, outcome_id, user_id, score, assessment_type, artifact_type, artifact_id, comments, created_at
		 FROM outcome_assessments WHERE outcome_id = ? AND user_id = ? ORDER BY created_at, id`,
		outcomeID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.OutcomeAssessment
	for rows.Next() {
		a := &model.OutcomeAssessment{}
		var artifactType sql.NullString
		var artifactID sql.NullInt64
		if err := rows.Scan(&a.ID, &a.OutcomeID, &a.UserID, &a.Score, &a.AssessmentType, &artifactType, &artifactID, &a.Comments, &a.CreatedAt); err != nil {
			return nil, err
		}
		if artifactType.Valid {
			a.ArtifactType = artifactType.String
		}
		if artifactID.Valid {
			v := artifactID.Int64
			a.ArtifactID = &v
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// --- Clone (§5.5: the reuse path is a whole-tree deep copy) ---

// CloneCourse deep-copies a course (units, resources, outcomes) to a new
// draft owned by targetUserID. Template masters clone to per-kid
// instances; per-kid courses clone as "run it again next term". The
// attempt rows never copy — progress belongs to the original kid.
func (s *Store) CloneCourse(ctx context.Context, courseID, targetUserID int64, newTitle string) (*model.Course, error) {
	src, err := s.GetCourse(ctx, courseID)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, ErrCourseNotFound
	}
	title := newTitle
	if title == "" {
		title = src.Title
	}
	clone := &model.Course{
		UserID:      targetUserID,
		Title:       title,
		Description: src.Description,
		SyllabusMD:  src.SyllabusMD,
		Status:      model.CourseStatusDraft,
	}
	if err := s.CreateCourse(ctx, clone); err != nil {
		return nil, err
	}

	units, err := s.ListUnits(ctx, courseID)
	if err != nil {
		return nil, err
	}
	// Map old unit ids to new unit ids so prerequisite_unit_ids survive
	// the copy (Canvas blueprint sync does the same id rewrite).
	unitIDMap := map[int64]int64{}
	for _, u := range units {
		nc := model.CourseUnit{
			CourseID:                 clone.ID,
			Title:                    u.Title,
			Position:                 u.Position,
			PrerequisiteUnitIDsJSON:  "[]",
			RequirementType:          u.RequirementType,
			RequireSequentialProgress: u.RequireSequentialProgress,
			UnlockAt:                 u.UnlockAt,
			Published:                u.Published,
		}
		if err := s.CreateUnit(ctx, &nc); err != nil {
			return nil, err
		}
		unitIDMap[u.ID] = nc.ID
	}
	// Second pass: rewrite prerequisites now that every id exists.
	for _, u := range units {
		prereq, err := decodePrereqIDs(u.PrerequisiteUnitIDsJSON)
		if err != nil {
			return nil, err
		}
		rewritten := make([]int64, 0, len(prereq))
		for _, pid := range prereq {
			if nid, ok := unitIDMap[pid]; ok {
				rewritten = append(rewritten, nid)
			}
		}
		enc, err := json.Marshal(rewritten)
		if err != nil {
			return nil, err
		}
		nu := u
		nu.PrerequisiteUnitIDsJSON = string(enc)
		if err := s.UpdateUnit(ctx, unitIDMap[u.ID], &nu); err != nil {
			return nil, err
		}
	}
	for _, u := range units {
		resources, err := s.ListResources(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range resources {
			nr := r
			nr.UnitID = unitIDMap[u.ID]
			nr.ID = 0
			if err := s.CreateResource(ctx, &nr); err != nil {
				return nil, err
			}
		}
	}
	outcomes, err := s.ListOutcomes(ctx, courseID)
	if err != nil {
		return nil, err
	}
	for _, o := range outcomes {
		no := o
		no.CourseID = clone.ID
		no.ID = 0
		if err := s.CreateOutcome(ctx, &no); err != nil {
			return nil, err
		}
	}
	return clone, nil
}

// decodePrereqIDs parses the prerequisite JSON int array, tolerating
// empty/dirty values with an empty slice.
func decodePrereqIDs(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("prerequisite_unit_ids: %w", err)
	}
	return ids, nil
}
