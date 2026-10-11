package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// P4-14 family goal tie-in: when the kid joined a shared savings pot,
// the daily coach's prompt carries the family's progress (name, pooled
// points, percent) so the parent side can tie the kid's effort to the
// shared "we" — without ever comparing siblings' contributions.
func TestDailyCoachNoteSpeaksForTheFamilyGoal(t *testing.T) {
	env, fake := setupAITest(t, true)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	seedCoachDay(t, env, kidID)
	env.setSetting(t, "ai_daily_coach", "true")

	// A shareable reward the whole family saves toward; the kid joins
	// and contributes, so the pool carries real progress.
	env.expectStatus(t, "POST", "/api/rewards", map[string]any{
		"name": "Family Trip", "cost": 500, "shareable": true,
	}, adminHeaders(), http.StatusCreated)
	resp := env.expectStatus(t, "POST", "/api/rewards/1/commit", map[string]any{
		"auto_contribute_percent": 0,
	}, childHeaders(kidID), http.StatusCreated)
	var commitment map[string]any
	decodeBody(t, resp, &commitment)

	env.expectStatus(t, "POST", "/api/points/adjust", map[string]any{
		"user_id": kidID, "amount": 50, "note": "seed",
	}, adminHeaders(), http.StatusNoContent)
	env.expectStatus(t, "POST", "/api/commitments/1/contribute",
		map[string]any{"amount": 50}, childHeaders(kidID), http.StatusOK)

	fake.mu.Lock()
	fake.text = `{"child": "今天做得很棒！", "parent": "家庭目标已完成10%，照这个节奏本周能达成。"}`
	fake.mu.Unlock()

	today := time.Now().Format(model.DateFormat)
	env.coach.WriteDailyNotes(t.Context(), today)

	// The prompt told the model about the family's pot.
	found := false
	for _, p := range fake.prompts() {
		if strings.Contains(p, "Family Trip") && strings.Contains(p, "The whole family is saving together") && strings.Contains(p, "50 of 500") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the coach prompt to mention the family goal, got %q", fake.prompts())
	}

	// The note itself is stored unchanged from the model's reply.
	note, err := env.store.GetDailyCoachNote(t.Context(), int64(kidID), today)
	if err != nil || note == nil {
		t.Fatalf("expected a stored note, got %+v (%v)", note, err)
	}
	if !strings.Contains(note.ParentText, "家庭目标") {
		t.Fatalf("parent text = %q", note.ParentText)
	}
}

// A kid with only personal goals (or none) keeps a family-free prompt:
// the pot clause never leaks into other households' notes.
func TestDailyCoachNoteSkipsFamilyGoalWhenNotShared(t *testing.T) {
	env, fake := setupAITest(t, true)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	seedCoachDay(t, env, kidID)
	env.setSetting(t, "ai_daily_coach", "true")

	fake.mu.Lock()
	fake.text = `{"child": "今天不错！", "parent": "保持。"}`
	fake.mu.Unlock()

	env.coach.WriteDailyNotes(t.Context(), time.Now().Format(model.DateFormat))
	for _, p := range fake.prompts() {
		if strings.Contains(p, "The whole family is saving together") {
			t.Fatalf("prompt must not mention a family goal without a shared pool, got %q", p)
		}
	}
}
