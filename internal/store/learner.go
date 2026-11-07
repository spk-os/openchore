package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// Learner profile (L3). The event stream is append-only (xAPI Statement
// semantics: never UPDATE, never DELETE); kc_mastery is the per-tag
// rollup materialized on each answer/review so the profile page and the
// planner read one row instead of replaying events. All writers go
// through RecordLearnerEvent, which also refreshes the rollup.

// ErrStudentConfigNotFound is never returned to the caller: GetStudentConfig
// falls back to defaults for a kid with no row yet.
var ErrStudentConfigNotFound = errors.New("student config not found")

// LearnerEventInput is one append to learner_events. Result and Context
// are marshaled once by the caller-provided strings; this store keeps to
// rows, not semantics.
type LearnerEventInput struct {
	UserID      int64
	Verb        string
	ObjectType  string
	ObjectID    int64
	ResultJSON  string
	ContextJSON string
}

// RecordLearnerEvent appends one event and refreshes the KC rollup for
// the tags named in context_json ({"kc_ids": [...]}). The refresh is
// best-effort per tag: a bad tag id (deleted tag) is skipped, not an
// error — the event row itself is the durable fact.
func (s *Store) RecordLearnerEvent(ctx context.Context, in LearnerEventInput) (*model.LearnerEvent, error) {
	e := &model.LearnerEvent{
		UserID:      in.UserID,
		Verb:        in.Verb,
		ObjectType:  in.ObjectType,
		ObjectID:    in.ObjectID,
		ResultJSON:  in.ResultJSON,
		ContextJSON: in.ContextJSON,
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO learner_events (user_id, verb, object_type, object_id, result_json, context_json)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		in.UserID, in.Verb, in.ObjectType, in.ObjectID, in.ResultJSON, in.ContextJSON)
	if err != nil {
		return nil, err
	}
	e.ID, _ = res.LastInsertId()
	e.OccurredAt = time.Now()
	e.CreatedAt = e.OccurredAt

	// Best-effort rollup refresh for the tags this event touched. The
	// per-tag recount reads the event rows (json_each membership), so a
	// deleted tag's rows just stop matching — the event stands either way.
	var ctxData struct {
		KCIDs []int64 `json:"kc_ids"`
	}
	if in.ContextJSON != "" {
		_ = json.Unmarshal([]byte(in.ContextJSON), &ctxData)
	}
	for _, tagID := range ctxData.KCIDs {
		if err := s.bumpKCMastery(ctx, in.UserID, tagID); err != nil {
			continue // deleted tag or similar: the event stands
		}
	}
	return e, nil
}

// kcWindow is the sliding window for the rollup: the most recent N
// answer/review events per (kid, tag) feed the Laplace-smoothed rate.
// Small on purpose — the family app's tags see tens of events, not
// thousands, and a 30-event window keeps "last week's slump" visible.
const kcWindow = 30

// bumpKCMastery recomputes one (kid, tag) rollup from the recent
// correct/incorrect events and upserts the row. Correct rate uses
// Laplace smoothing ((correct+1)/(total+2)) so one wrong answer on a
// fresh tag doesn't read as 0% mastery; fewer than 5 samples marks the
// row low-confidence so the UI greys it and the planner down-weights it.
func (s *Store) bumpKCMastery(ctx context.Context, userID, tagID int64) error {
	// Recount from the recent answer/review events carrying this tag.
	// Membership is tested with json_each against context_json's kc_ids
	// array — a LIKE '%1%' would also match tag 11, 21, 100…
	rows, err := s.db.QueryContext(ctx,
		`SELECT result_json FROM learner_events e
		 WHERE e.user_id = ? AND (e.verb = ? OR e.verb = ?)
		   AND e.context_json IS NOT NULL
		   AND EXISTS (SELECT 1 FROM json_each(e.context_json, '$.kc_ids') je WHERE je.value = ?)
		 ORDER BY e.occurred_at DESC, e.id DESC LIMIT ?`,
		userID, model.EventVerbAnswered, model.EventVerbReviewed, tagID, kcWindow)
	if err != nil {
		return err
	}
	defer rows.Close()
	correctCount, total := 0, 0
	for rows.Next() {
		var resultJSON string
		if err := rows.Scan(&resultJSON); err != nil {
			return err
		}
		var result struct {
			Success *bool `json:"success"`
			Skipped bool  `json:"skipped"`
		}
		if json.Unmarshal([]byte(resultJSON), &result) != nil || result.Success == nil {
			continue // an event without a verdict carries no signal
		}
		if result.Skipped {
			continue // a skipped round (manual/self) is not a wrong answer
		}
		total++
		if *result.Success {
			correctCount++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	mastery := float64(correctCount+1) / float64(total+2)
	lowConf := total < 5

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO kc_mastery (user_id, tag_id, correct_count, total_count, mastery, low_confidence, last_event_at)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT (user_id, tag_id) DO UPDATE SET
		   correct_count = excluded.correct_count,
		   total_count = excluded.total_count,
		   mastery = excluded.mastery,
		   low_confidence = excluded.low_confidence,
		   last_event_at = excluded.last_event_at`,
		userID, tagID, correctCount, total, mastery, boolToInt(lowConf))
	return err
}

// ListLearnerEvents returns the kid's events, newest first, for the
// profile page's trend sections. limit <= 0 means a sane default.
func (s *Store) ListLearnerEvents(ctx context.Context, userID int64, limit int) ([]model.LearnerEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at
		 FROM learner_events WHERE user_id = ?
		 ORDER BY occurred_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Initialized (not nil) so an empty stream marshals as [] — the
	// profile page maps over these without a null guard.
	out := make([]model.LearnerEvent, 0, 32)
	for rows.Next() {
		var e model.LearnerEvent
		if err := rows.Scan(&e.ID, &e.UserID, &e.Verb, &e.ObjectType, &e.ObjectID,
			&e.ResultJSON, &e.ContextJSON, &e.OccurredAt, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountLearnerEventsSince counts the kid's events of any verb since the
// given time — the profile header's "本周完成 N 项" counter. The bound is
// formatted in UTC because occurred_at defaults to CURRENT_TIMESTAMP,
// which SQLite stores as UTC — a local-time string would sit 8 hours in
// the future and the >= comparison would never match.
func (s *Store) CountLearnerEventsSince(ctx context.Context, userID int64, since time.Time, verb string) (int64, error) {
	q := `SELECT COUNT(*) FROM learner_events WHERE user_id = ? AND occurred_at >= ?`
	args := []any{userID, since.UTC().Format(time.DateTime)}
	if verb != "" {
		q += ` AND verb = ?`
		args = append(args, verb)
	}
	var n int64
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// KCMasteryStat is one row of the profile's KC matrix: the tag joined in
// (name/subject for grouping) plus the rollup.
type KCMasteryStat struct {
	TagID         int64      `json:"tag_id"`
	Name          string     `json:"name"`
	Subject       string     `json:"subject"`
	CorrectCount  int        `json:"correct_count"`
	TotalCount    int        `json:"total_count"`
	Mastery       float64    `json:"mastery"`
	LowConfidence bool       `json:"low_confidence"`
	LastEventAt   *time.Time `json:"last_event_at,omitempty"`
}

// ListKCMastery returns the kid's per-tag rollups joined with tag names,
// weakest first (the profile page orders the matrix by mastery ascending
// so the weak spots are top of the list, mirroring the OD prototype).
func (s *Store) ListKCMastery(ctx context.Context, userID int64) ([]KCMasteryStat, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.tag_id, kt.name, kt.subject, m.correct_count, m.total_count, m.mastery, m.low_confidence, m.last_event_at
		 FROM kc_mastery m
		 JOIN knowledge_tags kt ON kt.id = m.tag_id
		 WHERE m.user_id = ?
		 ORDER BY m.mastery ASC, m.total_count DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Same []-not-null contract as ListLearnerEvents.
	out := make([]KCMasteryStat, 0, 16)
	for rows.Next() {
		var s KCMasteryStat
		if err := rows.Scan(&s.TagID, &s.Name, &s.Subject, &s.CorrectCount, &s.TotalCount,
			&s.Mastery, &s.LowConfidence, &s.LastEventAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// --- student_config ---

// The Ranedeer enum values (validated here so the CHECK constraints stay
// a backstop, not the only guard — SQLite CHECKs give unhelpful errors).
var validDepth = map[string]bool{"小学": true, "初中": true, "高中": true}
var validLearningStyle = map[string]bool{"Visual": true, "Verbal": true, "Active": true, "Intuitive": true, "Reflective": true, "Global": true}
var validCommunication = map[string]bool{"Formal": true, "Textbook": true, "Layman": true, "StoryTelling": true, "Socratic": true}
var validTone = map[string]bool{"Encouraging": true, "Neutral": true, "Informative": true, "Friendly": true, "Humorous": true}
var validReasoning = map[string]bool{"Deductive": true, "Inductive": true, "Abductive": true, "Analogical": true, "Causal": true}

// ValidateStudentConfig normalizes an incoming config: enum fields fall
// back to the row's (or the schema's) defaults when invalid, and the
// three JSON list fields are forced to valid JSON arrays (or "[]").
func ValidateStudentConfig(c *model.StudentConfig) {
	if !validDepth[c.Depth] {
		c.Depth = "小学"
	}
	if !validLearningStyle[c.LearningStyle] {
		c.LearningStyle = "Visual"
	}
	if !validCommunication[c.Communication] {
		c.Communication = "Textbook"
	}
	if !validTone[c.Tone] {
		c.Tone = "Friendly"
	}
	if !validReasoning[c.Reasoning] {
		c.Reasoning = "Deductive"
	}
	c.Interests = ensureJSONArray(c.Interests)
	c.Strengths = ensureJSONArray(c.Strengths)
	c.Weaknesses = ensureJSONArray(c.Weaknesses)
}

// ensureJSONArray parses a string as a JSON array; anything else becomes
// "[]" (the parent's lists are plain string arrays, nothing fancier).
func ensureJSONArray(s string) string {
	s = trimSpace(s)
	if s == "" {
		return "[]"
	}
	var arr []string
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		return "[]"
	}
	return s
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// GetStudentConfig loads the kid's config, creating the default row on
// first read so the profile page always has a config to render and edit.
func (s *Store) GetStudentConfig(ctx context.Context, userID int64) (*model.StudentConfig, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT user_id, depth, learning_style, communication, tone, reasoning, interests, strengths, weaknesses, updated_at
		 FROM student_config WHERE user_id = ?`, userID)
	var c model.StudentConfig
	err := row.Scan(&c.UserID, &c.Depth, &c.LearningStyle, &c.Communication, &c.Tone, &c.Reasoning,
		&c.Interests, &c.Strengths, &c.Weaknesses, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		c = model.StudentConfig{UserID: userID}
		ValidateStudentConfig(&c)
		if err := s.PutStudentConfig(ctx, &c); err != nil {
			return nil, err
		}
		return &c, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// PutStudentConfig upserts the kid's config (validated first by the
// caller via ValidateStudentConfig; this store trusts the CHECKs as the
// final backstop).
func (s *Store) PutStudentConfig(ctx context.Context, c *model.StudentConfig) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO student_config (user_id, depth, learning_style, communication, tone, reasoning, interests, strengths, weaknesses, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT (user_id) DO UPDATE SET
		   depth = excluded.depth,
		   learning_style = excluded.learning_style,
		   communication = excluded.communication,
		   tone = excluded.tone,
		   reasoning = excluded.reasoning,
		   interests = excluded.interests,
		   strengths = excluded.strengths,
		   weaknesses = excluded.weaknesses,
		   updated_at = excluded.updated_at`,
		c.UserID, c.Depth, c.LearningStyle, c.Communication, c.Tone, c.Reasoning,
		c.Interests, c.Strengths, c.Weaknesses)
	return err
}
