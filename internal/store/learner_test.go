package store_test

// L3 learner profile store tests: the append-only event stream, the KC
// rollup's Laplace-smoothed recomputation (json_each membership — a LIKE
// would match tag 11 for tag 1), and the Ranedeer-enum student_config
// fallbacks. The API layer is tested separately; these pin the SQL.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// recordEvent is the test shorthand for one append + rollup refresh.
func recordEvent(t *testing.T, s *store.Store, userID int64, verb string, kcIDs []int64, success bool) *model.LearnerEvent {
	t.Helper()
	ctxJSON, _ := json.Marshal(map[string]any{"kc_ids": kcIDs})
	resultJSON, _ := json.Marshal(map[string]any{"success": success})
	e, err := s.RecordLearnerEvent(context.Background(), store.LearnerEventInput{
		UserID:      userID,
		Verb:        verb,
		ObjectType:  model.EventObjectErrorItem,
		ObjectID:    1,
		ResultJSON:  string(resultJSON),
		ContextJSON: string(ctxJSON),
	})
	if err != nil {
		t.Fatalf("RecordLearnerEvent: %v", err)
	}
	return e
}

// recordSkipEvent appends the manual/self round's frozen result shape —
// {"success":false,"skipped":true} — which the rollup must treat as "no
// verdict" (not a wrong answer).
func recordSkipEvent(t *testing.T, s *store.Store, userID int64, verb string, kcIDs []int64) *model.LearnerEvent {
	t.Helper()
	ctxJSON, _ := json.Marshal(map[string]any{"kc_ids": kcIDs})
	resultJSON, _ := json.Marshal(map[string]any{"success": false, "skipped": true})
	e, err := s.RecordLearnerEvent(context.Background(), store.LearnerEventInput{
		UserID:      userID,
		Verb:        verb,
		ObjectType:  model.EventObjectErrorItem,
		ObjectID:    1,
		ResultJSON:  string(resultJSON),
		ContextJSON: string(ctxJSON),
	})
	if err != nil {
		t.Fatalf("RecordLearnerEvent: %v", err)
	}
	return e
}

func TestRecordLearnerEventAppendsAndRollsUp(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()
	kid := createTestUser(t, s, "小明", "child")
	tagA, err := s.FindOrCreateKnowledgeTag(ctx, "一元二次方程", "math", nil)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	// tagB proves json_each membership: id 1 vs 11-style mismatches.
	tagB, err := s.FindOrCreateKnowledgeTag(ctx, "因式分解", "math", nil)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}

	// Two wrong, one right on tagA; two right on tagB.
	recordEvent(t, s, kid.ID, model.EventVerbAnswered, []int64{tagA}, false)
	recordEvent(t, s, kid.ID, model.EventVerbAnswered, []int64{tagA}, false)
	recordEvent(t, s, kid.ID, model.EventVerbAnswered, []int64{tagA}, true)
	recordEvent(t, s, kid.ID, model.EventVerbReviewed, []int64{tagB}, true)
	recordEvent(t, s, kid.ID, model.EventVerbReviewed, []int64{tagB}, true)

	// Events append-only: all five rows exist, newest-first listing.
	events, err := s.ListLearnerEvents(ctx, kid.ID, 0)
	if err != nil {
		t.Fatalf("ListLearnerEvents: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(events))
	}

	// tagA: (1+1)/(3+2) = 0.4 Laplace-smoothed, low confidence (n=3<5).
	stats, err := s.ListKCMastery(ctx, kid.ID)
	if err != nil {
		t.Fatalf("ListKCMastery: %v", err)
	}
	byTag := map[int64]store.KCMasteryStat{}
	for _, st := range stats {
		byTag[st.TagID] = st
	}
	a, ok := byTag[tagA]
	if !ok {
		t.Fatalf("tag %d missing from rollup", tagA)
	}
	if a.CorrectCount != 1 || a.TotalCount != 3 {
		t.Fatalf("tagA counts = %d/%d, want 1/3", a.CorrectCount, a.TotalCount)
	}
	if a.Mastery < 0.39 || a.Mastery > 0.41 {
		t.Fatalf("tagA mastery = %.3f, want 0.4", a.Mastery)
	}
	if !a.LowConfidence {
		t.Fatal("tagA with 3 samples must be low confidence")
	}
	// tagB: (2+1)/(2+2) = 0.75 — and the json_each filter must not have
	// leaked tagA's rows into tagB (a LIKE '%1%' would).
	b, ok := byTag[tagB]
	if !ok {
		t.Fatalf("tag %d missing from rollup", tagB)
	}
	if b.TotalCount != 2 || b.CorrectCount != 2 {
		t.Fatalf("tagB counts = %d/%d, want 2/2 (json_each membership)", b.CorrectCount, b.TotalCount)
	}
	if b.Mastery < 0.74 || b.Mastery > 0.76 {
		t.Fatalf("tagB mastery = %.3f, want 0.75", b.Mastery)
	}

	// The weakest-first ordering puts tagA (0.4) before tagB (0.75).
	if stats[0].TagID != tagA {
		t.Fatalf("weakest-first ordering broken: first = tag %d", stats[0].TagID)
	}
}

// TestBumpKCMasterySkipsSkippedEvents pins the skip semantics: a
// manual/self round's event ({"success":false,"skipped":true}) leaves
// the rollup untouched — same mastery/correct/total before and after —
// while a real wrong answer (success=false, no skipped flag) still
// counts against the KC.
func TestBumpKCMasterySkipsSkippedEvents(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()
	kid := createTestUser(t, s, "小跳", "child")
	tag, err := s.FindOrCreateKnowledgeTag(ctx, "有理数的分类", "math", nil)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}

	// Baseline: one right, one wrong → (1+1)/(2+2) = 0.5.
	recordEvent(t, s, kid.ID, model.EventVerbAnswered, []int64{tag}, true)
	recordEvent(t, s, kid.ID, model.EventVerbAnswered, []int64{tag}, false)
	masteryOf := func() store.KCMasteryStat {
		stats, err := s.ListKCMastery(ctx, kid.ID)
		if err != nil {
			t.Fatalf("ListKCMastery: %v", err)
		}
		for _, st := range stats {
			if st.TagID == tag {
				return st
			}
		}
		t.Fatalf("tag %d missing from rollup", tag)
		return store.KCMasteryStat{}
	}
	before := masteryOf()
	if before.CorrectCount != 1 || before.TotalCount != 2 {
		t.Fatalf("baseline counts = %d/%d, want 1/2", before.CorrectCount, before.TotalCount)
	}
	if before.Mastery < 0.49 || before.Mastery > 0.51 {
		t.Fatalf("baseline mastery = %.3f, want 0.5", before.Mastery)
	}

	// A manual/self round (skipped=true): the rollup must not move.
	recordSkipEvent(t, s, kid.ID, model.EventVerbAnswered, []int64{tag})
	afterSkip := masteryOf()
	if afterSkip.CorrectCount != before.CorrectCount || afterSkip.TotalCount != before.TotalCount {
		t.Fatalf("skip changed counts: %d/%d, want unchanged %d/%d",
			afterSkip.CorrectCount, afterSkip.TotalCount, before.CorrectCount, before.TotalCount)
	}
	if afterSkip.Mastery != before.Mastery {
		t.Fatalf("skip changed mastery: %.6f, want unchanged %.6f", afterSkip.Mastery, before.Mastery)
	}

	// A reviewed-skip round (same frozen shape, reviewed verb) is also
	// not a wrong answer.
	recordSkipEvent(t, s, kid.ID, model.EventVerbReviewed, []int64{tag})
	if st := masteryOf(); st.TotalCount != 2 || st.Mastery != before.Mastery {
		t.Fatalf("reviewed skip changed the rollup: %d/%d %.6f, want 2 total %.6f",
			st.CorrectCount, st.TotalCount, st.Mastery, before.Mastery)
	}

	// A real wrong answer still counts: (1+1)/(3+2) ≈ 0.4.
	recordEvent(t, s, kid.ID, model.EventVerbAnswered, []int64{tag}, false)
	afterWrong := masteryOf()
	if afterWrong.CorrectCount != 1 || afterWrong.TotalCount != 3 {
		t.Fatalf("counts after a real wrong = %d/%d, want 1/3", afterWrong.CorrectCount, afterWrong.TotalCount)
	}
	if afterWrong.Mastery < 0.39 || afterWrong.Mastery > 0.41 {
		t.Fatalf("mastery after a real wrong = %.3f, want 0.4", afterWrong.Mastery)
	}

	// The skipped event itself is still in the append-only stream.
	events, err := s.ListLearnerEvents(ctx, kid.ID, 0)
	if err != nil {
		t.Fatalf("ListLearnerEvents: %v", err)
	}
	skips := 0
	for _, e := range events {
		if strings.Contains(e.ResultJSON, `"skipped":true`) {
			skips++
		}
	}
	if skips != 2 {
		t.Fatalf("skipped events in the stream = %d, want 2 (append-only, never dropped)", skips)
	}
}

func TestRecordLearnerEventNoTagNoRollup(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()
	kid := createTestUser(t, s, "小红", "child")

	// An event without kc_ids (a plain chore completion) must not crash
	// and must not write any rollup row.
	e, err := s.RecordLearnerEvent(ctx, store.LearnerEventInput{
		UserID:     kid.ID,
		Verb:       model.EventVerbCompleted,
		ObjectType: model.EventObjectChore,
		ObjectID:   7,
		ResultJSON: `{"success":true}`,
	})
	if err != nil {
		t.Fatalf("RecordLearnerEvent: %v", err)
	}
	if e.ID == 0 {
		t.Fatal("expected non-zero event id")
	}
	if stats, _ := s.ListKCMastery(ctx, kid.ID); len(stats) != 0 {
		t.Fatalf("expected no rollup rows, got %d", len(stats))
	}
	n, err := s.CountLearnerEventsSince(ctx, kid.ID, time.Now().Add(-time.Hour), model.EventVerbCompleted)
	if err != nil || n != 1 {
		t.Fatalf("CountLearnerEventsSince = %d, %v; want 1, nil", n, err)
	}
}

func TestStudentConfigDefaultsAndUpsert(t *testing.T) {
	s := setupStore(t)
	ctx := context.Background()
	kid := createTestUser(t, s, "小刚", "child")

	// First read creates the default row (Ranedeer schema defaults).
	c, err := s.GetStudentConfig(ctx, kid.ID)
	if err != nil {
		t.Fatalf("GetStudentConfig: %v", err)
	}
	if c.Depth != "小学" || c.LearningStyle != "Visual" || c.Communication != "Textbook" ||
		c.Tone != "Friendly" || c.Reasoning != "Deductive" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.Interests != "[]" || c.Strengths != "[]" || c.Weaknesses != "[]" {
		t.Fatalf("list fields must default to [], got %q %q %q", c.Interests, c.Strengths, c.Weaknesses)
	}

	// A second read returns the same row (no duplicate create).
	c2, _ := s.GetStudentConfig(ctx, kid.ID)
	if c2.UserID != kid.ID || c2.Depth != "小学" {
		t.Fatalf("second read diverged: %+v", c2)
	}

	// Update through the normalizer: one valid change, one invalid enum
	// (normalized back to a valid value, not rejected by the CHECK), one
	// junk list (must become []).
	c.Depth = "初中"
	c.Tone = "Sarcastic" // not a Ranedeer tone
	c.Interests = "not json"
	store.ValidateStudentConfig(c)
	if err := s.PutStudentConfig(ctx, c); err != nil {
		t.Fatalf("PutStudentConfig: %v", err)
	}
	got, _ := s.GetStudentConfig(ctx, kid.ID)
	if got.Depth != "初中" {
		t.Fatalf("depth update lost: %+v", got)
	}
	if got.Tone != "Friendly" {
		t.Fatalf("invalid tone must normalize to the default before the write: %+v", got)
	}
	if got.Interests != "[]" {
		t.Fatalf("junk interests must normalize to [], got %q", got.Interests)
	}
}

func TestValidateStudentConfigNormalizes(t *testing.T) {
	c := model.StudentConfig{
		Depth:         "高中",
		LearningStyle: "Reflective",
		Communication: "Socratic",
		Tone:          "Encouraging",
		Reasoning:     "Analogical",
		Interests:     `["足球","游戏"]`,
		Strengths:     "",
		Weaknesses:    "[1,2]",
	}
	store.ValidateStudentConfig(&c)
	if c.Depth != "高中" || c.LearningStyle != "Reflective" || c.Communication != "Socratic" ||
		c.Tone != "Encouraging" || c.Reasoning != "Analogical" {
		t.Fatalf("valid enums must survive: %+v", c)
	}
	if c.Interests != `["足球","游戏"]` {
		t.Fatalf("valid interests must survive, got %q", c.Interests)
	}
	if c.Strengths != "[]" || c.Weaknesses != "[]" {
		t.Fatalf("empty/non-string-array must become [], got %q %q", c.Strengths, c.Weaknesses)
	}
}
