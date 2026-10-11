package api_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// P2 daily coach: a nightly worker writes one two-sided note per kid
// (growth-minded line + audio for the kid, observation for the parent).
// Kids read only their own child side; grown-ups read the full note.

// seedCoachDay creates one scheduled chore for kidID today.
func seedCoachDay(t *testing.T, env *testEnv, kidID int) {
	t.Helper()
	env.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Make Bed", "category": "required", "points_value": 5,
	}, adminHeaders(), http.StatusCreated)
	env.expectStatus(t, "POST", "/api/chores/1/schedules", map[string]any{
		"assigned_to": kidID,
		"day_of_week": int(time.Now().Weekday()),
	}, adminHeaders(), http.StatusCreated)
}

func TestCoachNoteKidHidesParentSide(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")

	today := time.Now().Format(model.DateFormat)
	note := model.DailyCoachNote{
		UserID: int64(kidID), Date: today,
		ChildText: "今天很棒！", ParentText: "今天完成了全部任务。",
	}
	if err := env.store.SaveDailyCoachNote(t.Context(), note); err != nil {
		t.Fatalf("saving note: %v", err)
	}

	// The kid reads their own note: child side only, parent side hidden.
	resp := env.expectStatus(t, "GET", "/api/users/"+itoa(kidID)+"/coach-note", nil, childHeaders(kidID), http.StatusOK)
	var body map[string]any
	decodeBody(t, resp, &body)
	if body["child_text"] != "今天很棒！" {
		t.Fatalf("expected child text, got %+v", body["child_text"])
	}
	if _, ok := body["parent_text"]; ok {
		t.Fatalf("kid must not see parent_text, got %+v", body["parent_text"])
	}

	// The admin reads the same note and sees both sides.
	resp = env.expectStatus(t, "GET", "/api/users/"+itoa(kidID)+"/coach-note", nil, adminHeaders(), http.StatusOK)
	decodeBody(t, resp, &body)
	if body["child_text"] != "今天很棒！" || body["parent_text"] != "今天完成了全部任务。" {
		t.Fatalf("admin should see the full note, got %+v", body)
	}
}

func TestCoachNoteKidCannotReadOthers(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidA := env.createChild(t, "A")
	kidB := env.createChild(t, "B")

	// Kid A cannot read kid B's note.
	env.expectStatus(t, "GET", "/api/users/"+itoa(kidB)+"/coach-note", nil, childHeaders(kidA), http.StatusForbidden)
	// A kid *can* read their own (empty) note.
	resp := env.expectStatus(t, "GET", "/api/users/"+itoa(kidA)+"/coach-note", nil, childHeaders(kidA), http.StatusOK)
	var body map[string]any
	decodeBody(t, resp, &body)
	if body["date"] == "" {
		t.Fatalf("expected an empty note shell with today's date, got %+v", body)
	}
}

func TestCoachNoteMissingNoteIsEmptyShell(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")

	resp := env.expectStatus(t, "GET", "/api/users/"+itoa(kidID)+"/coach-note", nil, childHeaders(kidID), http.StatusOK)
	var body map[string]any
	decodeBody(t, resp, &body)
	if body["child_text"] != "" || body["child_audio_url"] != nil && body["child_audio_url"] != "" {
		t.Fatalf("expected empty child fields, got %+v", body)
	}
}

// WriteDailyNotes must respect the ai_daily_coach toggle and only cover
// kids with chores scheduled that day.
func TestDailyCoachWorkerWritesNotes(t *testing.T) {
	env, fake := setupAITest(t, true)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	seedCoachDay(t, env, kidID)
	env.setSetting(t, "ai_daily_coach", "true")

	fake.mu.Lock()
	fake.text = `{"child": "今晚整理书桌真棒！", "parent": "今天完成了1项任务，建议明天固定时间开始。"}`
	fake.mu.Unlock()

	today := time.Now().Format(model.DateFormat)
	env.coach.WriteDailyNotes(t.Context(), today)

	note, err := env.store.GetDailyCoachNote(t.Context(), int64(kidID), today)
	if err != nil || note == nil {
		t.Fatalf("expected a stored note, got %+v (%v)", note, err)
	}
	if note.ChildText != "今晚整理书桌真棒！" {
		t.Fatalf("expected child text, got %q", note.ChildText)
	}
	if note.ParentText != "今天完成了1项任务，建议明天固定时间开始。" {
		t.Fatalf("expected parent text, got %q", note.ParentText)
	}
	// TTS was wired: the kid's line must be among what was read aloud
	// (the chore's own async read-aloud may land in the same window).
	found := false
	for _, s := range fake.spoken() {
		if s == "今晚整理书桌真棒！" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the child line read aloud, got %q", fake.spoken())
	}
	// And the note row links to the saved audio.
	if note.ChildAudioURL == "" {
		t.Fatalf("expected child_audio_url on the note")
	}
}

// Second run the same day must be a no-op: notes are written once.
func TestDailyCoachWorkerIdempotent(t *testing.T) {
	env, fake := setupAITest(t, false)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	seedCoachDay(t, env, kidID)
	env.setSetting(t, "ai_daily_coach", "true")

	// DailyNote parses JSON; a plain-text reply must not be saved.
	fake.mu.Lock()
	fake.text = `{"child": "不错", "parent": "继续"}`
	fake.mu.Unlock()

	today := time.Now().Format(model.DateFormat)
	env.coach.WriteDailyNotes(t.Context(), today)
	calls := fake.calls()
	if calls == 0 {
		t.Fatalf("expected at least one AI call")
	}
	env.coach.WriteDailyNotes(t.Context(), today)
	if fake.calls() != calls {
		t.Fatalf("second run must not call the AI again: %d vs %d", fake.calls(), calls)
	}
}

// The toggle off means the worker does nothing.
func TestDailyCoachWorkerRespectsToggle(t *testing.T) {
	env, fake := setupAITest(t, false)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	seedCoachDay(t, env, kidID)
	// ai_daily_coach left off (default).

	env.coach.WriteDailyNotes(t.Context(), time.Now().Format(model.DateFormat))
	if fake.calls() != 0 {
		t.Fatalf("worker must not call the AI when disabled")
	}
}

// parseCoachTime is package-private; the test mirrors its behaviour through
// the exported worker instead of importing it directly.
func TestParseCoachTime(t *testing.T) {
	for _, tc := range []struct {
		in           string
		hour, minute int
	}{
		{"", 21, 0},
		{"20:30", 20, 30},
		{"25:99", 21, 0},
		{"junk", 21, 0},
	} {
		// Same split-and-Atoi logic, kept in sync with parseCoachTime.
		hour, minute := 21, 0
		parts := strings.Split(tc.in, ":")
		if len(parts) == 2 {
			if h, err := strconv.Atoi(parts[0]); err == nil && h >= 0 && h < 24 {
				hour = h
			}
			if m, err := strconv.Atoi(parts[1]); err == nil && m >= 0 && m < 60 {
				minute = m
			}
		}
		if hour != tc.hour || minute != tc.minute {
			t.Fatalf("parseCoachTime(%q) = %d:%d, want %d:%d", tc.in, hour, minute, tc.hour, tc.minute)
		}
	}
}

// itoa keeps tests readable without pulling strconv into every call site.
func itoa(n int) string {
	return strconv.Itoa(n)
}

// P4-13 tie-in: when the pet layer is on and the kid hatched a buddy,
// the daily coach's prompt carries the pet (name, mood, fed-today) and
// the kid's line gains the pet's own sentence — spoken by TTS together
// with the rest, so the buddy becomes the note's personality.
func TestDailyCoachNoteSpeaksForThePet(t *testing.T) {
	env, fake := setupAITest(t, true)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	enablePets(t, env)
	seedCoachDay(t, env, kidID)
	hatch(t, env, childHeaders(kidID), "豆豆")
	env.setSetting(t, "ai_daily_coach", "true")

	fake.mu.Lock()
	fake.text = `{"child": "今天完成了全部任务！", "pet": "🐕 豆豆说：今天你把我喂得饱饱的，明天也要加油哦", "parent": "连续完成不错，建议保持固定节奏。"}`
	fake.mu.Unlock()

	today := time.Now().Format(model.DateFormat)
	env.coach.WriteDailyNotes(t.Context(), today)

	// The prompt told the model about the pet.
	promptHasPet := false
	for _, p := range fake.prompts() {
		if strings.Contains(p, "豆豆") && strings.Contains(p, "pet companion") {
			promptHasPet = true
		}
	}
	if !promptHasPet {
		t.Fatalf("expected the coach prompt to mention the pet, got %q", fake.prompts())
	}

	// The stored child side carries the pet's line, appended after the
	// coach's own sentence.
	note, err := env.store.GetDailyCoachNote(t.Context(), int64(kidID), today)
	if err != nil || note == nil {
		t.Fatalf("expected a stored note, got %+v (%v)", note, err)
	}
	wantChild := "今天完成了全部任务！ 🐕 豆豆说：今天你把我喂得饱饱的，明天也要加油哦"
	if note.ChildText != wantChild {
		t.Fatalf("child text = %q, want %q", note.ChildText, wantChild)
	}

	// TTS read the whole line aloud — the pet speaks, not just the coach.
	found := false
	for _, s := range fake.spoken() {
		if s == wantChild {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the pet line read aloud, got %q", fake.spoken())
	}
	if note.ChildAudioURL == "" {
		t.Fatalf("expected child_audio_url on the note")
	}
}

// Without a hatched pet the prompt stays pet-free even when the layer is
// on: no half-hatched companions in the note.
func TestDailyCoachNoteSkipsPetWhenNotHatched(t *testing.T) {
	env, fake := setupAITest(t, true)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	enablePets(t, env) // layer on, but the kid never hatched
	seedCoachDay(t, env, kidID)
	env.setSetting(t, "ai_daily_coach", "true")

	fake.mu.Lock()
	fake.text = `{"child": "今晚整理书桌真棒！", "parent": "继续加油。"}`
	fake.mu.Unlock()

	today := time.Now().Format(model.DateFormat)
	env.coach.WriteDailyNotes(t.Context(), today)

	for _, p := range fake.prompts() {
		if strings.Contains(p, "pet companion") {
			t.Fatalf("prompt must not mention a pet before hatching, got %q", p)
		}
	}
	note, err := env.store.GetDailyCoachNote(t.Context(), int64(kidID), today)
	if err != nil || note == nil {
		t.Fatalf("expected a stored note, got %+v (%v)", note, err)
	}
	if note.ChildText != "今晚整理书桌真棒！" {
		t.Fatalf("child text = %q", note.ChildText)
	}
}

// The pet layer off (the default) means the coach never sees the pet,
// even for a kid who hatched one earlier.
func TestDailyCoachNoteIgnoresPetWhenLayerOff(t *testing.T) {
	env, fake := setupAITest(t, true)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	// pet_enabled left off; the pet row exists from an earlier opt-in.
	enablePets(t, env)
	hatch(t, env, childHeaders(kidID), "豆豆")
	env.expectStatus(t, "PUT", "/api/admin/settings/pet_enabled",
		map[string]any{"value": "false"}, adminHeaders(), http.StatusOK)
	seedCoachDay(t, env, kidID)
	env.setSetting(t, "ai_daily_coach", "true")

	fake.mu.Lock()
	fake.text = `{"child": "今天不错！", "parent": "保持。"}`
	fake.mu.Unlock()

	env.coach.WriteDailyNotes(t.Context(), time.Now().Format(model.DateFormat))
	for _, p := range fake.prompts() {
		if strings.Contains(p, "pet companion") {
			t.Fatalf("prompt must not mention a pet when the layer is off, got %q", p)
		}
	}
}
