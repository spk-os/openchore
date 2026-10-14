package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// Spaced repetition (L4). review_items stores the go-fsrs Card fields
// verbatim (the scheduler columns — due/stability/difficulty/… — are
// written only by the grade path through the library; this file never
// invents values for them), review_logs mirrors go-fsrs ReviewLog, and
// fsrs_params is the per-kid scheduler config. The daily queue is
// computed live: due <= now minus cards already rated today, under a
// new-cards-per-day cap and a total cap (Anki's queue math, minus the
// 00:05 snapshot — an evening correction must surface the same night).

var ErrReviewItemNotFound = errors.New("review item not found")

const reviewItemColumns = `id, user_id, source_type, source_id, front_text, back_text, subject,
	due, stability, difficulty, scheduled_days, reps, lapses, state, last_review, remaining_steps,
	suspended, created_at`

func scanReviewItem(row interface{ Scan(...any) error }) (*model.ReviewItem, error) {
	i := &model.ReviewItem{}
	var lastReview sql.NullTime
	var suspended int
	if err := row.Scan(&i.ID, &i.UserID, &i.SourceType, &i.SourceID, &i.FrontText, &i.BackText, &i.Subject,
		&i.Due, &i.Stability, &i.Difficulty, &i.ScheduledDays, &i.Reps, &i.Lapses, &i.State,
		&lastReview, &i.RemainingSteps, &suspended, &i.CreatedAt); err != nil {
		return nil, err
	}
	if lastReview.Valid {
		i.LastReview = &lastReview.Time
	}
	i.Suspended = suspended != 0
	return i, nil
}

// toCard maps a ReviewItem onto the go-fsrs Card. The library's State is
// an int8; review_items stores the readable TEXT form, so parse it back
// (parseState is in the fsrs service file to keep the import there).
type cardFields = model.ReviewItem

// CreateReviewItem inserts a fresh card (state New, due now). The
// UNIQUE(source_type, source_id) index makes re-creating a card for the
// same source a no-op: pass allowExists=true when the caller wants
// "ensure a card exists" semantics (the T+0 creation hook).
func (s *Store) CreateReviewItem(ctx context.Context, i *model.ReviewItem, allowExists bool) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO review_items (user_id, source_type, source_id, front_text, back_text, subject, due)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		i.UserID, i.SourceType, i.SourceID, i.FrontText, i.BackText, i.Subject, dbTime(i.Due))
	if err != nil {
		if allowExists && isUniqueConstraintErr(err) {
			return nil
		}
		return err
	}
	i.ID, _ = res.LastInsertId()
	return nil
}

// GetReviewItem fetches one card by id.
func (s *Store) GetReviewItem(ctx context.Context, id int64) (*model.ReviewItem, error) {
	i, err := scanReviewItem(s.db.QueryRowContext(ctx,
		`SELECT `+reviewItemColumns+` FROM review_items WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return i, nil
}

// GetReviewItemBySource fetches one card by its source object.
func (s *Store) GetReviewItemBySource(ctx context.Context, sourceType string, sourceID int64) (*model.ReviewItem, error) {
	i, err := scanReviewItem(s.db.QueryRowContext(ctx,
		`SELECT `+reviewItemColumns+` FROM review_items WHERE source_type = ? AND source_id = ?`,
		sourceType, sourceID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return i, nil
}

// UpdateReviewItemCard persists the go-fsrs Card fields after a rating
// (the grade path's only writer of the scheduler columns). lastReview
// NULL means the card was never rated.
func (s *Store) UpdateReviewItemCard(ctx context.Context, i *model.ReviewItem) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE review_items SET due = ?, stability = ?, difficulty = ?, scheduled_days = ?,
		 reps = ?, lapses = ?, state = ?, last_review = ?, remaining_steps = ? WHERE id = ?`,
		dbTime(i.Due), i.Stability, i.Difficulty, i.ScheduledDays, i.Reps, i.Lapses, i.State,
		nullTime(i.LastReview), i.RemainingSteps, i.ID)
	return err
}

// SetReviewItemSuspended flips the parent's pause switch on one card.
func (s *Store) SetReviewItemSuspended(ctx context.Context, id int64, suspended bool) error {
	v := 0
	if suspended {
		v = 1
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE review_items SET suspended = ? WHERE id = ?`, v, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrReviewItemNotFound
	}
	return nil
}

// ListDueReviewItems computes the kid's current queue, live (Anki's
// day-cutoff + due + new-card-limit math, without the 00:05 snapshot):
//   - due: Learning/Relearning cards (their dues are minute-scale, same
//     session) and Review/New cards whose due has passed;
//   - minus cards the kid already rated today (a card rated Good at 4pm
//     is done for the day even when the library schedules it 10 minutes
//     out — the next session picks it up);
//   - newCardsToday caps how many state='New' cards may surface today
//     (Anki's "New cards/day": new cards amplify ~10× into future
//     reviews, so the parent dials this down);
//   - totalCap caps the whole queue (storm guard).
func (s *Store) ListDueReviewItems(ctx context.Context, userID int64, dayStart time.Time, newCardsToday, totalCap int) ([]model.ReviewItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+reviewItemColumns+` FROM review_items
		 WHERE user_id = ? AND suspended = 0 AND id NOT IN (
		     SELECT review_item_id FROM review_logs
		     WHERE user_id = ? AND reviewed_at >= ?
		 )
		 AND (
		     state IN ('Learning','Relearning')
		     OR (state IN ('New','Review') AND due <= ?)
		 )
		 ORDER BY CASE state WHEN 'Learning' THEN 0 WHEN 'Relearning' THEN 1 WHEN 'Review' THEN 2 ELSE 3 END,
		          due
		 LIMIT ?`,
		userID, userID, dbTime(dayStart), dbTime(time.Now()), totalCap)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ReviewItem
	newCount := 0
	for rows.Next() {
		i, err := scanReviewItem(rows)
		if err != nil {
			return nil, err
		}
		if i.State == model.ReviewStateNew {
			// New cards fill the tail of the queue, after everything
			// already in flight — up to today's remaining allowance.
			if newCount >= newCardsToday {
				continue
			}
			newCount++
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// CountNewReviewCardsRatedToday returns how many state='New'-at-rating
// cards the kid already saw today (the new-card allowance's debit side).
// review_logs.state is the card's state BEFORE the rating (Anki's revlog
// "type" semantics, set by GradeReviewCard), so 'New' here identifies a
// card on its first sight — the count the daily cap debits against.
func (s *Store) CountNewReviewCardsRatedToday(ctx context.Context, userID int64, dayStart time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM review_logs l WHERE l.user_id = ? AND l.reviewed_at >= ? AND l.state = 'New'`,
		userID, dbTime(dayStart)).Scan(&n)
	return n, err
}

// ListUpcomingReviewItems previews the queue's future (the "接下来还会
// 遇到" list and the parent's 30-day load chart): every non-suspended
// card ordered by due, capped.
func (s *Store) ListUpcomingReviewItems(ctx context.Context, userID int64, limit int) ([]model.ReviewItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+reviewItemColumns+` FROM review_items
		 WHERE user_id = ? AND suspended = 0 AND state != 'New'
		 ORDER BY due LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ReviewItem
	for rows.Next() {
		i, err := scanReviewItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// InsertReviewLog records one rating event (go-fsrs ReviewLog).
func (s *Store) InsertReviewLog(ctx context.Context, l *model.ReviewLog) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO review_logs (review_item_id, user_id, rating, state, elapsed_days, scheduled_days, reviewed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		l.ReviewItemID, l.UserID, l.Rating, l.State, nullInt64(l.ElapsedDays), l.ScheduledDays, dbTime(l.ReviewedAt))
	if err != nil {
		return err
	}
	l.ID, _ = res.LastInsertId()
	return nil
}

// GetFSRSParams reads the kid's scheduler config. No row means the
// defaults — same values the migration seeds, restated here so a
// missing row is indistinguishable from an untouched one.
func (s *Store) GetFSRSParams(ctx context.Context, userID int64) (*model.FSRSParams, error) {
	p := &model.FSRSParams{
		UserID:              userID,
		RequestRetention:    0.90,
		MaximumInterval:     36500,
		LearningStepsJSON:   "[1,10]",
		RelearningStepsJSON: "[10]",
		EnableFuzz:          true,
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT user_id, request_retention, maximum_interval, learning_steps_json,
		        relearning_steps_json, enable_fuzz, weights_json, updated_at
		 FROM fsrs_params WHERE user_id = ?`, userID)
	var weights sql.NullString
	err := row.Scan(&p.UserID, &p.RequestRetention, &p.MaximumInterval, &p.LearningStepsJSON,
		&p.RelearningStepsJSON, &p.EnableFuzz, &weights, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return p, nil // defaults
	}
	if err != nil {
		return nil, err
	}
	if weights.Valid {
		p.WeightsJSON = weights.String
	}
	return p, nil
}

// SaveFSRSParams upserts the scheduler config.
func (s *Store) SaveFSRSParams(ctx context.Context, p *model.FSRSParams) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO fsrs_params (user_id, request_retention, maximum_interval, learning_steps_json,
		     relearning_steps_json, enable_fuzz, weights_json, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		     request_retention = excluded.request_retention,
		     maximum_interval = excluded.maximum_interval,
		     learning_steps_json = excluded.learning_steps_json,
		     relearning_steps_json = excluded.relearning_steps_json,
		     enable_fuzz = excluded.enable_fuzz,
		     weights_json = excluded.weights_json,
		     updated_at = excluded.updated_at`,
		p.UserID, p.RequestRetention, p.MaximumInterval, p.LearningStepsJSON,
		p.RelearningStepsJSON, p.EnableFuzz, nullString(p.WeightsJSON), dbTime(time.Now()))
	return err
}

// ReviewStatsDay is one day of the review heatmap (review_logs by day).
type ReviewStatsDay struct {
	Date     string `json:"date"`
	Count    int    `json:"count"`
	GoodCount int   `json:"good_count"`
}

// ReviewStatsBucket is one bar of the future-load chart (review_items
// grouped by due date; a day over the warn threshold renders red).
type ReviewStatsBucket struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// ReviewLoadForecast groups upcoming dues by day for the next N days.
func (s *Store) ReviewLoadForecast(ctx context.Context, userID int64, from time.Time, days int) ([]ReviewStatsBucket, error) {
	to := from.AddDate(0, 0, days)
	rows, err := s.db.QueryContext(ctx,
		`SELECT date(due) AS d, COUNT(*) FROM review_items
		 WHERE user_id = ? AND suspended = 0 AND state != 'New' AND due >= ? AND due < ?
		 GROUP BY d ORDER BY d`,
		userID, dbTime(from), dbTime(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewStatsBucket
	for rows.Next() {
		var b ReviewStatsBucket
		if err := rows.Scan(&b.Date, &b.Count); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ReviewHeatmap aggregates review_logs by day (the GitHub-style
// contribution chart; good_count feeds the retention curve).
func (s *Store) ReviewHeatmap(ctx context.Context, userID int64, from time.Time) ([]ReviewStatsDay, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT date(reviewed_at) AS d, COUNT(*),
		        SUM(CASE WHEN rating IN ('Good','Easy') THEN 1 ELSE 0 END)
		 FROM review_logs
		 WHERE user_id = ? AND reviewed_at >= ?
		 GROUP BY d ORDER BY d`,
		userID, dbTime(from))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewStatsDay
	for rows.Next() {
		var d ReviewStatsDay
		if err := rows.Scan(&d.Date, &d.Count, &d.GoodCount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ReviewStreakDays counts consecutive days (ending today or yesterday)
// with at least one rating — the 🔥 on the review screens.
func (s *Store) ReviewStreakDays(ctx context.Context, userID int64, today time.Time) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT date(reviewed_at) FROM review_logs
		 WHERE user_id = ? ORDER BY date(reviewed_at) DESC LIMIT 400`,
		userID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	streak := 0
	expect := today.Format(model.DateFormat)
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return 0, err
		}
		if d == expect {
			streak++
			expect = minusOneDay(expect)
		} else if streak == 0 && d == minusOneDay(expect) {
			// Today has no reviews yet: the streak counts from yesterday.
			expect = d
			streak++
			expect = minusOneDay(expect)
		} else {
			break
		}
	}
	return streak, rows.Err()
}

// CountReviewItems counts one kid's cards (suspended included — the
// parent's "总共 N 张卡" line).
func (s *Store) CountReviewItems(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM review_items WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// GetErrorItemLite returns the provenance line for an error-item card:
// "来源 · 题干前 30 字" (the review screen's "来自：…" note). Kept light
// on purpose — the queue endpoint resolves one line per card, not a
// full ErrorItem.
func (s *Store) GetErrorItemLite(ctx context.Context, id int64) (string, error) {
	var source, question sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT source, question_text FROM error_items WHERE id = ?`, id).Scan(&source, &question)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	line := ""
	if source.Valid && source.String != "" {
		line = source.String
	}
	if question.Valid && question.String != "" {
		q := question.String
		if len([]rune(q)) > 30 {
			q = string([]rune(q)[:30]) + "…"
		}
		if line != "" {
			line += " · "
		}
		line += q
	}
	return line, nil
}

// minusOneDay steps a YYYY-MM-DD string back one day without parsing
// (the dates come straight out of SQLite's date()).
func minusOneDay(date string) string {
	t, err := time.Parse(model.DateFormat, date)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, -1).Format(model.DateFormat)
}

// CreditReviewPoints anchors the day's queue-completion credit: one row
// per kid per date, keyed by the caller's idempotency key. A duplicate
// insert (reopened app, retry) is a no-op — the partial UNIQUE index on
// idempotency_key enforces it (same pattern as DebitMissedChore).
func (s *Store) CreditReviewPoints(ctx context.Context, userID int64, date, key string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO point_transactions (user_id, amount, reason, reference_id, note, idempotency_key)
		 VALUES (?, ?, ?, NULL, ?, ?)`,
		userID, ReviewCreditAmount, model.ReasonReviewComplete,
		"Daily review completed on "+date, key)
	if isUniqueConstraintErr(err) {
		return nil
	}
	return err
}

// ReviewCreditAmount is the flat credit for finishing the day's queue
// (the review screens' "+6 分已入账").
const ReviewCreditAmount = 6
