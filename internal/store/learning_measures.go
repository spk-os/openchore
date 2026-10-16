package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// Learning measures (迁移 034): the analysis→measure engine's rows. The
// store keeps error_types as a JSON array in SQL (SQLite has no array
// type) and hydrates it both ways; everything else is a plain column.

const measureColumns = `m.id, m.user_id, m.subject, m.kc_tag_id, kt.name, m.title, m.detail, m.error_types, m.origin, m.origin_id, m.status, m.priority, m.created_by, m.created_at, m.updated_at`

const measureSelect = `SELECT ` + measureColumns + `
	FROM learning_measures m
	LEFT JOIN knowledge_tags kt ON kt.id = m.kc_tag_id`

// scanMeasure reads one measure row; the joined KC name may be NULL.
func scanMeasure(row interface{ Scan(...any) error }) (*model.LearningMeasure, error) {
	m := &model.LearningMeasure{}
	var kcTagID sql.NullInt64
	var kcName sql.NullString
	var originID sql.NullInt64
	var errorTypes sql.NullString
	if err := row.Scan(&m.ID, &m.UserID, &m.Subject, &kcTagID, &kcName, &m.Title, &m.Detail,
		&errorTypes, &m.Origin, &originID, &m.Status, &m.Priority, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	if kcTagID.Valid {
		m.KCTagID = &kcTagID.Int64
	}
	if kcName.Valid {
		m.KCName = kcName.String
	}
	if originID.Valid {
		m.OriginID = &originID.Int64
	}
	m.ErrorTypes = []string{}
	if errorTypes.Valid && errorTypes.String != "" {
		_ = json.Unmarshal([]byte(errorTypes.String), &m.ErrorTypes)
	}
	return m, nil
}

// marshalErrorTypes renders the error_types slice back to its JSON column.
func marshalErrorTypes(types []string) string {
	if len(types) == 0 {
		return "[]"
	}
	b, err := json.Marshal(types)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// CreateLearningMeasure inserts one measure.
func (s *Store) CreateLearningMeasure(ctx context.Context, m *model.LearningMeasure) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO learning_measures (user_id, subject, kc_tag_id, title, detail, error_types, origin, origin_id, status, priority, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.UserID, m.Subject, nullInt64(m.KCTagID), m.Title, m.Detail,
		marshalErrorTypes(m.ErrorTypes), m.Origin, nullInt64(m.OriginID), m.Status, m.Priority, m.CreatedBy)
	if err != nil {
		return err
	}
	m.ID, _ = res.LastInsertId()
	return nil
}

// UpdateLearningMeasure rewrites one measure's editable fields (the AI
// strengthen path and the parent's manual edit share it).
func (s *Store) UpdateLearningMeasure(ctx context.Context, id int64, m *model.LearningMeasure) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE learning_measures SET subject = ?, kc_tag_id = ?, title = ?, detail = ?, error_types = ?, origin = ?, priority = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		m.Subject, nullInt64(m.KCTagID), m.Title, m.Detail,
		marshalErrorTypes(m.ErrorTypes), m.Origin, m.Priority, id)
	return err
}

// SetLearningMeasureStatus flips active/paused/done.
func (s *Store) SetLearningMeasureStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE learning_measures SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, status, id)
	return err
}

// DeleteLearningMeasure removes one measure for good (the parent said so).
func (s *Store) DeleteLearningMeasure(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM learning_measures WHERE id = ?`, id)
	return err
}

// GetLearningMeasure fetches one measure.
func (s *Store) GetLearningMeasure(ctx context.Context, id int64) (*model.LearningMeasure, error) {
	m, err := scanMeasure(s.db.QueryRowContext(ctx, measureSelect+` WHERE m.id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return m, err
}

// ListLearningMeasures lists a kid's measures. status "" means all
// statuses; subject "" means all subjects. Priority order first, newest
// within the same priority.
func (s *Store) ListLearningMeasures(ctx context.Context, userID int64, status, subject string) ([]model.LearningMeasure, error) {
	q := measureSelect + ` WHERE m.user_id = ?`
	args := []any{userID}
	if status != "" {
		q += ` AND m.status = ?`
		args = append(args, status)
	}
	if subject != "" {
		q += ` AND m.subject = ?`
		args = append(args, subject)
	}
	q += ` ORDER BY m.priority, m.id DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.LearningMeasure{}
	for rows.Next() {
		m, err := scanMeasure(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// errMeasureNotFound is the not-found sentinel the handler maps to 404.
var errMeasureNotFound = errors.New("learning measure not found")

// CountLearningMeasures counts one kid's measures by status ("" = all).
func (s *Store) CountLearningMeasures(ctx context.Context, userID int64, status string) (int64, error) {
	q := `SELECT COUNT(*) FROM learning_measures WHERE user_id = ?`
	args := []any{userID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	var n int64
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n)
	if err == nil && n == 0 && status == "" {
		// Zero measures is a normal state, not an error; keep it that way.
		return 0, nil
	}
	return n, err
}

// TouchLearningMeasure stamps updated_at (kept for symmetry with the
// review items' suspend flow; the strengthen path uses Update instead).
func (s *Store) TouchLearningMeasure(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE learning_measures SET updated_at = ? WHERE id = ?`, time.Now(), id)
	return err
}
