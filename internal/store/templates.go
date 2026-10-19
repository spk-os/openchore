package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// TemplateTx is the store's transaction for importing one chore template
// pack (admin templates page): every chore and schedule of the import lands
// together, or nothing does.
type TemplateTx struct {
	tx *sql.Tx
}

// BeginTemplateTx opens the import transaction.
func (s *Store) BeginTemplateTx(ctx context.Context) (*TemplateTx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &TemplateTx{tx: tx}, nil
}

// Commit lands the import; Rollback undoes everything.
func (t *TemplateTx) Commit() error   { return t.tx.Commit() }
func (t *TemplateTx) Rollback() error { return t.tx.Rollback() }

// CreateChore inserts one imported chore (source="template",
// external_id=<pack id> so the UI can badge template chores).
func (t *TemplateTx) CreateChore(ctx context.Context, c *model.Chore) error {
	requiresApproval := boolToInt(c.RequiresApproval)
	requiresPhoto := boolToInt(c.RequiresPhoto)
	photoSource := c.PhotoSource
	if photoSource == "" {
		photoSource = model.PhotoSourceChild
	}
	res, err := t.tx.ExecContext(ctx,
		`INSERT INTO chores (title, description, category, subject, icon, points_value, missed_penalty_value, estimated_minutes, requires_approval, requires_photo, photo_source, reference_photo_url, source, external_id, tts_audio_url, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Title, c.Description, c.Category, c.Subject, c.Icon, c.PointsValue, c.MissedPenaltyValue, c.EstimatedMinutes, requiresApproval, requiresPhoto, photoSource, c.ReferencePhotoURL, c.Source, c.ExternalID, c.TTSAudioURL, c.CreatedBy)
	if err != nil {
		return err
	}
	c.ID, _ = res.LastInsertId()
	return nil
}

// CreateSchedule inserts one schedule of the import.
func (t *TemplateTx) CreateSchedule(ctx context.Context, cs *model.ChoreSchedule) error {
	res, err := t.tx.ExecContext(ctx,
		`INSERT INTO chore_schedules (chore_id, assigned_to, assignment_type, fcfs_group_id, day_of_week, day_of_month, specific_date, available_at, points_multiplier, start_date, end_date, recurrence_interval, recurrence_start, due_by, expiry_penalty, expiry_penalty_value)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cs.ChoreID, cs.AssignedTo, cs.AssignmentType, cs.FcfsGroupID, cs.DayOfWeek, cs.DayOfMonth, cs.SpecificDate, cs.AvailableAt, cs.PointsMultiplier, cs.StartDate, cs.EndDate, cs.RecurrenceInterval, cs.RecurrenceStart, cs.DueBy, cs.ExpiryPenalty, cs.ExpiryPenaltyValue)
	if err != nil {
		return err
	}
	cs.ID, _ = res.LastInsertId()
	return nil
}

// StopDailySchedules ends the kid's other open-ended day-of-week schedules
// (never a delete): the daily/weekly routine pauses while a different
// template pack — e.g. the exam-week sprint — runs. today is "now" for the
// still-running filter; stopDate is the end_date written, which callers set
// to yesterday because schedule queries treat end_date inclusively
// (end_date >= day), so a schedule ending "today" would still run today.
// Today's completions and points are untouched. Template chores imported
// from keepPackID keep running, so re-importing the routine pack while its
// schedules are still live never stops them.
func (t *TemplateTx) StopDailySchedules(ctx context.Context, userID int64, today, stopDate, keepPackID string) (int, error) {
	res, err := t.tx.ExecContext(ctx,
		`UPDATE chore_schedules
		    SET end_date = ?
		  WHERE assigned_to = ?
		    AND day_of_week IS NOT NULL
		    AND (end_date IS NULL OR end_date >= ?)
		    AND chore_id IN (
		        SELECT id FROM chores
		         WHERE NOT (source = 'template' AND external_id = ?)
		    )`,
		stopDate, userID, today, keepPackID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// DailySchedulesRunning counts the kid's day-of-week schedules that are
// still open-ended (no end date, or one in the future), so the templates
// page can warn "switching will stop N daily tasks" before the parent
// opts in. Chores from keepPackID don't count: switching to a pack you
// are already running is a no-op, not a conflict.
func (s *Store) DailySchedulesRunning(ctx context.Context, userID int64, today, keepPackID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*)
		   FROM chore_schedules cs
		   JOIN chores c ON c.id = cs.chore_id
		  WHERE cs.assigned_to = ?
		    AND cs.day_of_week IS NOT NULL
		    AND (cs.end_date IS NULL OR cs.end_date >= ?)
		    AND NOT (c.source = 'template' AND c.external_id = ?)`,
		userID, today, keepPackID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting running daily schedules: %w", err)
	}
	return n, nil
}

// todayInZone formats the current time in the given location using the
// standard date layout, so import code and tests agree on "today" even
// when the server's clock sits near midnight. Exported callers (handlers)
// format their own "today"; this stays package-private for store tests.
func todayInZone(loc *time.Location) string {
	return time.Now().In(loc).Format(model.DateFormat)
}
