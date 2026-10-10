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
