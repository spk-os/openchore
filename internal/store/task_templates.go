package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// Task templates (L0). A template is a saved chore: the parent tunes it
// once and instantiates it repeatedly. Deleting is archiving (Habitica's
// "broken" mark) — instances keep working. Sync pushes template edits into
// instances that haven't started (no completion yet), using
// template_synced_at as the cursor; completed instances stay untouched
// because their history is the kid's record, not the template's.

var (
	ErrTemplateNotFound = errors.New("task template not found")
)

const templateColumns = `id, name, category, points, description, requires_photo, photo_source, reference_photo_url, tts_audio_url, ai_prompt, schedule_json, created_by, is_archived, created_at, updated_at`

func scanTemplate(row interface{ Scan(...any) error }) (*model.TaskTemplate, error) {
	t := &model.TaskTemplate{}
	var reqPhoto int
	var archived int
	var refURL, ttsURL, aiPrompt sql.NullString
	if err := row.Scan(&t.ID, &t.Name, &t.Category, &t.Points, &t.Description,
		&reqPhoto, &t.PhotoSource, &refURL, &ttsURL, &aiPrompt, &t.ScheduleJSON,
		&t.CreatedBy, &archived, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.RequiresPhoto = reqPhoto == 1
	t.IsArchived = archived == 1
	if refURL.Valid {
		t.ReferencePhotoURL = refURL.String
	}
	if ttsURL.Valid {
		t.TTSAudioURL = ttsURL.String
	}
	if aiPrompt.Valid {
		t.AIPrompt = aiPrompt.String
	}
	return t, nil
}

// CreateTaskTemplate saves a chore shape as a reusable template.
func (s *Store) CreateTaskTemplate(ctx context.Context, t *model.TaskTemplate) error {
	reqPhoto := boolToInt(t.RequiresPhoto)
	photoSource := t.PhotoSource
	if photoSource == "" {
		photoSource = model.PhotoSourceBoth
	}
	if t.ScheduleJSON == "" {
		t.ScheduleJSON = "{}"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO task_templates (name, category, points, description, requires_photo, photo_source, reference_photo_url, tts_audio_url, ai_prompt, schedule_json, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.Category, t.Points, t.Description, reqPhoto, photoSource,
		nullString(t.ReferencePhotoURL), nullString(t.TTSAudioURL), t.AIPrompt, t.ScheduleJSON, t.CreatedBy)
	if err != nil {
		return err
	}
	t.ID, _ = res.LastInsertId()
	return nil
}

// GetTaskTemplate fetches one template, archived or not.
func (s *Store) GetTaskTemplate(ctx context.Context, id int64) (*model.TaskTemplate, error) {
	t, err := scanTemplate(s.db.QueryRowContext(ctx,
		`SELECT `+templateColumns+` FROM task_templates WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

// ListTaskTemplates lists templates. includeArchived flips the default
// live-only view so the admin page can show archived ones greyed out.
func (s *Store) ListTaskTemplates(ctx context.Context, includeArchived bool) ([]model.TaskTemplate, error) {
	q := `SELECT ` + templateColumns + ` FROM task_templates`
	if !includeArchived {
		q += ` WHERE is_archived = 0`
	}
	q += ` ORDER BY is_archived, name`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TaskTemplate
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// UpdateTaskTemplate edits a template's fields. Points/description/prompt
// travel to instances only through an explicit sync call.
func (s *Store) UpdateTaskTemplate(ctx context.Context, t *model.TaskTemplate) error {
	reqPhoto := boolToInt(t.RequiresPhoto)
	photoSource := t.PhotoSource
	if photoSource == "" {
		photoSource = model.PhotoSourceBoth
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE task_templates SET name=?, category=?, points=?, description=?, requires_photo=?, photo_source=?, reference_photo_url=?, tts_audio_url=?, ai_prompt=?, schedule_json=?, updated_at=CURRENT_TIMESTAMP
		 WHERE id=?`,
		t.Name, t.Category, t.Points, t.Description, reqPhoto, photoSource,
		nullString(t.ReferencePhotoURL), nullString(t.TTSAudioURL), t.AIPrompt, t.ScheduleJSON, t.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTemplateNotFound
	}
	return nil
}

// ArchiveTaskTemplate soft-deletes: instances live on untouched.
func (s *Store) ArchiveTaskTemplate(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE task_templates SET is_archived = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

// ListChoresByTemplate lists a template's instances (newest first), so the
// sync flow can show "sync to N instances" before doing it.
func (s *Store) ListChoresByTemplate(ctx context.Context, templateID int64) ([]model.Chore, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+choreColumns+` FROM chores WHERE template_id = ? ORDER BY id DESC`, templateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Chore
	for rows.Next() {
		c, err := scanChore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// CountOpenTemplateInstances counts instances that haven't been completed
// today — the sync preview's headline number.
func (s *Store) CountOpenTemplateInstances(ctx context.Context, templateID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT c.id)
		 FROM chores c
		 JOIN chore_schedules cs ON cs.chore_id = c.id
		 WHERE c.template_id = ?
		   AND NOT EXISTS (
		     SELECT 1 FROM chore_completions cc
		     WHERE cc.chore_schedule_id = cs.id AND cc.uncompleted_at IS NULL
		   )`, templateID).Scan(&n)
	return n, err
}

// coalesceStr maps "" to "" — used where a NOT NULL column (like
// chores.tts_audio_url) must never receive SQL NULL. Template fields that
// were never filled in stay empty strings on the instance too.
func coalesceStr(s string) string {
	return s
}

// SyncTemplateToInstances pushes template edits into the given instance
// chores. Only instances without any completion are eligible — a completed
// chore is the kid's history. Returns how many instances were updated.
func (s *Store) SyncTemplateToInstances(ctx context.Context, templateID int64, instanceIDs []int64, fields model.TaskTemplate) (int, error) {
	if len(instanceIDs) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	reqPhoto := boolToInt(fields.RequiresPhoto)
	photoSource := fields.PhotoSource
	if photoSource == "" {
		photoSource = model.PhotoSourceBoth
	}
	now := time.Now().UTC()

	updated := 0
	for _, id := range instanceIDs {
		res, err := tx.ExecContext(ctx,
			`UPDATE chores SET
				title = ?, description = ?, category = ?, points_value = ?,
				requires_photo = ?, photo_source = ?,
				reference_photo_url = ?, tts_audio_url = ?,
				template_synced_at = ?
			 WHERE id = ? AND template_id = ?
			   AND NOT EXISTS (
			     SELECT 1 FROM chore_completions cc
			     JOIN chore_schedules cs ON cs.id = cc.chore_schedule_id
			     WHERE cs.chore_id = chores.id AND cc.uncompleted_at IS NULL
			   )`,
			fields.Name, fields.Description, fields.Category, fields.Points,
			reqPhoto, photoSource,
			coalesceStr(fields.ReferencePhotoURL), coalesceStr(fields.TTSAudioURL),
			now, id, templateID)
		if err != nil {
			return updated, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			updated++
		}
	}
	if err := tx.Commit(); err != nil {
		return updated, err
	}
	return updated, nil
}
