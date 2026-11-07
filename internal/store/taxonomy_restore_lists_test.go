package store_test

import (
	"context"
	"testing"
)

// TX-RESTORE-LISTS-V4 pins the restore/export list semantics of
// ListAllLearnerEvents and ListReviewItemsByUser against a real SQLite
// DB. Rows are inserted with raw SQL on purpose: the restore path must
// survive rows whose nullable columns (result_json / context_json) are
// NULL, and RecordLearnerEvent would rewrite them and bump kc_mastery.

func TestRestoreLists_ListAllLearnerEvents_NullJSONIsolationAndOrder(t *testing.T) {
	s, db := setupStoreWithDB(t)
	ctx := context.Background()

	// Minimal FK rows only: users (learner_events.user_id references it)
	// and one knowledge_tag (the context_json payload points at it, as
	// the real writer does).
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (101, 'Target', 'child'), (102, 'Other', 'child')`)
	mustExec(t, db, `INSERT INTO knowledge_tags (id, name, subject) VALUES (9001, '两位数加法', 'math')`)

	// Target user: NULL result_json on one row, NULL context_json on
	// another, plus a normal row. Inserted out of id order relative to
	// the other user to prove ORDER BY id, not insertion time or user.
	mustExec(t, db, `INSERT INTO learner_events (user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at)
		VALUES (101, 'answered', 'error_item', 7, NULL, '{"subject":"math","kc_ids":[9001]}', '2026-01-02 08:00:00', '2026-01-02 08:00:00')`)
	mustExec(t, db, `INSERT INTO learner_events (user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at)
		VALUES (102, 'completed', 'chore', 3, '{"success":true}', NULL, '2026-01-01 08:00:00', '2026-01-01 08:00:00')`)
	mustExec(t, db, `INSERT INTO learner_events (user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at)
		VALUES (101, 'reviewed', 'review_item', 5, '{"score":0.5}', NULL, '2026-01-03 08:00:00', '2026-01-03 08:00:00')`)
	mustExec(t, db, `INSERT INTO learner_events (user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at)
		VALUES (101, 'attempted', 'course', 11, NULL, NULL, '2026-01-04 08:00:00', '2026-01-04 08:00:00')`)

	events, err := s.ListAllLearnerEvents(ctx, 101)
	if err != nil {
		t.Fatalf("ListAllLearnerEvents: unexpected error with NULL JSON: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("want 3 events for user 101, got %d: %+v", len(events), events)
	}

	// id ASC and user isolation in a single pass.
	for i := 1; i < len(events); i++ {
		if events[i-1].ID >= events[i].ID {
			t.Fatalf("events not id ASC: %d then %d", events[i-1].ID, events[i].ID)
		}
	}
	for _, e := range events {
		if e.UserID != 101 {
			t.Fatalf("leaked event of user %d: %+v", e.UserID, e)
		}
	}

	// NULL columns scan as "" (via COALESCE), never as literal "<NULL>".
	wantVerbs := []string{"answered", "reviewed", "attempted"}
	for i, want := range wantVerbs {
		if events[i].Verb != want {
			t.Fatalf("event %d verb: want %q got %q (orders: %v)", i, want, events[i].Verb, events)
		}
	}
	if events[0].ResultJSON != "" || events[0].ContextJSON == "" {
		t.Fatalf("row 1 NULL result_json must be \"\", context_json kept: %+v", events[0])
	}
	if events[1].ContextJSON != "" || events[1].ResultJSON != `{"score":0.5}` {
		t.Fatalf("row 2 NULL context_json must be \"\", result_json kept: %+v", events[1])
	}
	if events[2].ResultJSON != "" || events[2].ContextJSON != "" {
		t.Fatalf("row 3 both NULL must be \"\": %+v", events[2])
	}
	if events[0].OccurredAt.IsZero() || events[0].CreatedAt.IsZero() {
		t.Fatalf("timestamps not populated: %+v", events[0])
	}

	// Empty user: no error and a non-nil slice (callers range without guard).
	empty, err := s.ListAllLearnerEvents(ctx, 999)
	if err != nil {
		t.Fatalf("ListAllLearnerEvents(empty user): %v", err)
	}
	if empty == nil {
		t.Fatal("empty user must return non-nil slice")
	}
	if len(empty) != 0 {
		t.Fatalf("empty user leaked %d events", len(empty))
	}
}

func TestRestoreLists_ListReviewItemsByUser_FullColumnsOrderAndIsolation(t *testing.T) {
	s, db := setupStoreWithDB(t)
	ctx := context.Background()

	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (201, 'Target', 'child'), (202, 'Other', 'child')`)

	// Two complete FSRS rows for the target user, one for the other. All
	// 18 review_items columns are supplied explicitly so a missing or
	// mis-ordered SELECT/scan column shows up as a wrong value, not a
	// default. source_id differs per row to satisfy UNIQUE(source_type,
	// source_id); the second row is a suspended, future-due, re-learning
	// card that the live queue would filter out but restore must keep.
	mustExec(t, db, `INSERT INTO review_items (id, user_id, source_type, source_id, front_text, back_text, subject,
			due, stability, difficulty, scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended, created_at)
		VALUES (301, 201, 'error_item', 41, '3+4=?', '7', 'math',
			'2026-02-01 09:00:00', 12.5, 6.25, 9, 4, 1, 'Review', '2026-01-20 09:00:00', 0, 0, '2026-01-10 09:00:00')`)
	mustExec(t, db, `INSERT INTO review_items (id, user_id, source_type, source_id, front_text, back_text, subject,
			due, stability, difficulty, scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended, created_at)
		VALUES (302, 201, 'kc', 42, '什么是分数?', '整体的一部分', 'math',
			'2026-12-31 23:00:00', 2.5, 3.75, 3, 2, 2, 'Relearning', NULL, 1, 1, '2026-01-11 09:00:00')`)
	mustExec(t, db, `INSERT INTO review_items (id, user_id, source_type, source_id, front_text, back_text, subject,
			due, stability, difficulty, scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended, created_at)
		VALUES (303, 202, 'error_item', 43, 'other kid', '', 'chinese',
			'2026-02-01 09:00:00', 0, 0, 0, 0, 0, 'New', NULL, 0, 0, '2026-01-12 09:00:00')`)

	items, err := s.ListReviewItemsByUser(ctx, 201)
	if err != nil {
		t.Fatalf("ListReviewItemsByUser: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 cards for user 201, got %d: %+v", len(items), items)
	}
	if items[0].ID != 301 || items[1].ID != 302 {
		t.Fatalf("cards not id ASC: got ids %d, %d", items[0].ID, items[1].ID)
	}
	for _, it := range items {
		if it.UserID != 201 {
			t.Fatalf("leaked card of user %d: %+v", it.UserID, it)
		}
	}

	// Column-by-column: every one of the 18 columns lands in its field.
	a := items[0]
	if a.SourceType != "error_item" || a.SourceID != 41 || a.FrontText != "3+4=?" || a.BackText != "7" || a.Subject != "math" {
		t.Fatalf("text columns mismatched: %+v", a)
	}
	if got := a.Due.UTC().Format("2006-01-02 15:04:05"); got != "2026-02-01 09:00:00" {
		t.Fatalf("due: got %s", got)
	}
	if a.Stability != 12.5 || a.Difficulty != 6.25 {
		t.Fatalf("fsrs floats: %+v", a)
	}
	if a.ScheduledDays != 9 || a.Reps != 4 || a.Lapses != 1 {
		t.Fatalf("fsrs ints: %+v", a)
	}
	if a.State != "Review" || a.RemainingSteps != 0 || a.Suspended {
		t.Fatalf("state/remaining/suspended: %+v", a)
	}
	if a.LastReview == nil || a.LastReview.UTC().Format("2006-01-02 15:04:05") != "2026-01-20 09:00:00" {
		t.Fatalf("last_review: %+v", a.LastReview)
	}
	if got := a.CreatedAt.UTC().Format("2006-01-02 15:04:05"); got != "2026-01-10 09:00:00" {
		t.Fatalf("created_at: got %s", got)
	}
	if a.ErrorItem != nil {
		t.Fatalf("restore list must not join error_item: %+v", a.ErrorItem)
	}

	b := items[1]
	if b.State != "Relearning" || b.RemainingSteps != 1 || !b.Suspended {
		t.Fatalf("suspended/relearning card not round-tripped: %+v", b)
	}
	if b.LastReview != nil {
		t.Fatalf("NULL last_review must stay nil, got %v", *b.LastReview)
	}
	if b.Lapses != 2 || b.Reps != 2 || b.ScheduledDays != 3 {
		t.Fatalf("fsrs ints on card 2: %+v", b)
	}

	empty, err := s.ListReviewItemsByUser(ctx, 999)
	if err != nil {
		t.Fatalf("ListReviewItemsByUser(empty user): %v", err)
	}
	if empty == nil {
		t.Fatal("empty user must return non-nil slice")
	}
	if len(empty) != 0 {
		t.Fatalf("empty user leaked %d cards", len(empty))
	}
}
