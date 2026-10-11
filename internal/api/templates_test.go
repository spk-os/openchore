package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/llm"
	"github.com/liftedkilt/openchore/internal/model"
)

// The admin templates endpoints (P2-9) serve the embedded packs and proxy
// the AI tuner. These tests cover the contract the templates page uses:
// what ships, who may read it, and what happens without AI configured.

func TestListTemplatesServesEmbeddedPacks(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)

	resp := env.expectStatus(t, "GET", "/api/admin/templates", nil, adminHeaders(), http.StatusOK)
	var packs []map[string]any
	decodeBody(t, resp, &packs)
	if len(packs) < 4 {
		t.Fatalf("expected at least 4 template packs, got %d", len(packs))
	}
	seen := map[string]bool{}
	for _, p := range packs {
		id, _ := p["id"].(string)
		seen[id] = true
		if _, ok := p["name"]; !ok {
			t.Errorf("pack %s: missing name", id)
		}
		tasks, ok := p["tasks"].([]any)
		if !ok || len(tasks) == 0 {
			t.Errorf("pack %s: expected non-empty tasks", id)
			continue
		}
		for _, raw := range tasks {
			task, _ := raw.(map[string]any)
			if _, ok := task["title"]; !ok {
				t.Errorf("pack %s: task without title", id)
			}
		}
	}
	for _, want := range []string{"grade9-schoolday", "weekend-enrichment", "exam-week", "holiday-balance"} {
		if !seen[want] {
			t.Errorf("expected pack %s in the response", want)
		}
	}
}

func TestListTemplatesRequiresAdmin(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	env.createChild(t, "Kid")

	env.expectStatus(t, "GET", "/api/admin/templates", nil, childHeaders(2), http.StatusForbidden)
	env.expectStatus(t, "GET", "/api/admin/templates", nil, nil, http.StatusUnauthorized)
}

func TestTuneTemplateWithoutAI(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)

	body := map[string]any{
		"tasks": []map[string]any{
			{"title": "完成学校作业", "category": "required", "points": 10, "estimated_minutes": 90},
			{"title": "错题订正", "category": "required", "points": 6, "estimated_minutes": 25},
		},
		"tweak": "他英语弱，英语加量",
	}
	env.expectStatus(t, "POST", "/api/admin/templates/grade9-schoolday/tune", body, adminHeaders(), http.StatusServiceUnavailable)
}

func TestTuneTemplateValidates(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)

	// Missing tweak
	env.expectStatus(t, "POST", "/api/admin/templates/grade9-schoolday/tune",
		map[string]any{"tasks": []map[string]any{{"title": "x"}}}, adminHeaders(), http.StatusBadRequest)
	// Missing tasks
	env.expectStatus(t, "POST", "/api/admin/templates/grade9-schoolday/tune",
		map[string]any{"tweak": "加量"}, adminHeaders(), http.StatusBadRequest)
	// Unknown template id: 404 before the AI-not-configured 503
	env.expectStatus(t, "POST", "/api/admin/templates/no-such-pack/tune",
		map[string]any{"tweak": "x", "tasks": []map[string]any{{"title": "x"}}}, adminHeaders(), http.StatusNotFound)
}

// The exam-week pack must announce bonus_paused so the UI can explain why
// every bonus chore is stopped during exam week.
func TestExamWeekPackFlagsBonusPaused(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)

	resp := env.expectStatus(t, "GET", "/api/admin/templates", nil, adminHeaders(), http.StatusOK)
	var packs []map[string]any
	decodeBody(t, resp, &packs)
	for _, p := range packs {
		if p["id"] != "exam-week" {
			continue
		}
		if paused, _ := p["bonus_paused"].(bool); !paused {
			t.Error("exam-week pack should carry bonus_paused: true")
		}
		return
	}
	t.Fatal("exam-week pack not found")
}

// tuneAI is an OpenAI-compatible stub that captures the last chat request
// and replays one canned reply, so we can assert what the tune handler
// sends and that the fenced answer still parses.
type tuneAI struct {
	server   *httptest.Server
	mu       sync.Mutex
	lastBody string
}

func newTuneAI(t *testing.T, reply string) *tuneAI {
	t.Helper()
	f := &tuneAI{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.lastBody = string(body)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": reply}}},
		})
	}))
	t.Cleanup(f.server.Close)
	return f
}

// TestTuneTemplateWithAI exercises the full tune flow against a stub LLM
// server: the handler must forward the client's task list positionally and
// surface the parsed changes.
func TestTuneTemplateWithAI(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)

	// Answer in a code fence on purpose: parseTemplateTune must cope.
	stub := newTuneAI(t, "```json\n{\"summary\":\"已把英语加量\",\"changes\":[{\"index\":0,\"points\":12}]}\n```")
	env.chores.SetAI(llm.New(stub.server.URL+"/v1", "", "test-model"), nil)

	body := map[string]any{
		"tasks": []map[string]any{
			{"title": "完成学校作业", "category": "required", "points": 10, "estimated_minutes": 90},
			{"title": "错题订正", "category": "required", "points": 6, "estimated_minutes": 25},
		},
		"tweak": "他英语弱，英语加量",
	}
	resp := env.expectStatus(t, "POST", "/api/admin/templates/exam-week/tune", body, adminHeaders(), http.StatusOK)
	var tune struct {
		Summary string `json:"summary"`
		Changes []struct {
			Index  int `json:"index"`
			Points int `json:"points"`
		} `json:"changes"`
	}
	decodeBody(t, resp, &tune)
	if tune.Summary == "" {
		t.Error("expected a summary in the tune response")
	}
	if len(tune.Changes) != 1 || tune.Changes[0].Index != 0 || tune.Changes[0].Points != 12 {
		t.Fatalf("expected the fenced change parsed, got %+v", tune.Changes)
	}

	// The prompt must carry the tweak, the pack name and the task list.
	stub.mu.Lock()
	sent := stub.lastBody
	stub.mu.Unlock()
	for _, want := range []string{"他英语弱，英语加量", "完成学校作业", "错题订正"} {
		if !strings.Contains(sent, want) {
			t.Errorf("AI prompt missing %q", want)
		}
	}
}

// A failing or unreachable AI must surface as a 502, not hang the page.
func TestTuneTemplateAIError(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)

	stub := newTuneAI(t, "not json at all")
	env.chores.SetAI(llm.New(stub.server.URL+"/v1", "", "test-model"), nil)

	body := map[string]any{
		"tasks": []map[string]any{{"title": "错题订正", "category": "required", "points": 6}},
		"tweak": "他英语弱，英语加量",
	}
	env.expectStatus(t, "POST", "/api/admin/templates/exam-week/tune", body, adminHeaders(), http.StatusBadGateway)
}

// --- Import (the schedule-swap half of the templates page) ---

// importBody builds a valid import payload for one kid from a fixed task
// list, so each test below reads like the JSON the templates page sends.
func importBody(templateID string, assignedTo int, stopDaily bool, tasks ...map[string]any) map[string]any {
	if tasks == nil {
		tasks = []map[string]any{
			{
				"title": "完成学校作业", "description": "当天作业当天清",
				"icon": "book", "category": "required", "points": 10,
				"missed_penalty": 5, "estimated_minutes": 90,
				"days": []string{"weekdays"}, "due_by": "21:00", "expiry": "block",
			},
			{
				"title": "阅读 30 分钟", "icon": "book", "category": "bonus",
				"points": 3, "days": []string{"daily"}, "expiry": "no_points",
			},
		}
	}
	return map[string]any{
		"template_id": templateID,
		"assigned_to": assignedTo,
		"tasks":       tasks,
		"stop_daily":  stopDaily,
	}
}

// importResponse mirrors the 201 the import endpoint returns.
type importResponse struct {
	Imported int `json:"imported"`
}

// choresOf lists every chore row so tests can assert source/external_id.
func choresOf(t *testing.T, env *testEnv) []map[string]any {
	t.Helper()
	rows, err := env.db.Query(`SELECT title, source, external_id FROM chores ORDER BY id`)
	if err != nil {
		t.Fatalf("query chores: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var title, source string
		var external any
		if err := rows.Scan(&title, &source, &external); err != nil {
			t.Fatalf("scan chore: %v", err)
		}
		out = append(out, map[string]any{"title": title, "source": source, "external_id": external})
	}
	return out
}

// openDailySchedules counts the kid's still-open day-of-week schedules that
// don't belong to packID — the number the swap warning shows.
func openDailySchedules(t *testing.T, env *testEnv, userID int, today, packID string) int {
	t.Helper()
	var n int
	err := env.db.QueryRow(
		`SELECT COUNT(*) FROM chore_schedules cs JOIN chores c ON c.id = cs.chore_id
		  WHERE cs.assigned_to = ? AND cs.day_of_week IS NOT NULL
		    AND (cs.end_date IS NULL OR cs.end_date >= ?)
		    AND NOT (c.source = 'template' AND c.external_id = ?)`,
		userID, time.Now().Format(model.DateFormat), packID).Scan(&n)
	if err != nil {
		t.Fatalf("count open dailies: %v", err)
	}
	return n
}

// A plain import (no stop_daily) creates source="template" chores and one
// open-ended schedule per expanded day, and the kid sees them land.
func TestImportTemplateCreatesChoresAndSchedules(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")

	resp := env.expectStatus(t, "POST", "/api/admin/templates/import",
		importBody("grade9-schoolday", kid, false), adminHeaders(), http.StatusCreated)
	var out importResponse
	decodeBody(t, resp, &out)
	if out.Imported != 2 {
		t.Fatalf("expected 2 imported chores, got %d", out.Imported)
	}

	for _, c := range choresOf(t, env) {
		if c["source"] != "template" {
			t.Errorf("chore %v: expected source=template, got %v", c["title"], c["source"])
		}
		if c["external_id"] != "grade9-schoolday" {
			t.Errorf("chore %v: expected external_id=grade9-schoolday, got %v", c["title"], c["external_id"])
		}
	}

	// 5 weekday schedules for the homework task + 7 daily for reading.
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM chore_schedules WHERE assigned_to = ?`, kid).Scan(&n); err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	if n != 12 {
		t.Fatalf("expected 12 schedules (5 weekday + 7 daily), got %d", n)
	}

	// Every imported schedule must carry multiplier 1.0 — the insert is
	// explicit, so a forgotten zero would silently zero the kid's points.
	var zeros int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM chore_schedules WHERE points_multiplier = 0`).Scan(&zeros); err != nil {
		t.Fatalf("count zero multipliers: %v", err)
	}
	if zeros != 0 {
		t.Errorf("expected no zero points_multiplier rows, got %d", zeros)
	}

	// The kid's day view must include the imported chores.
	dayResp := env.request(t, "GET", fmt.Sprintf("/api/users/%d/chores?view=daily&date=%s", kid, time.Now().Format(model.DateFormat)), nil, childHeaders(kid))
	if dayResp.StatusCode != http.StatusOK {
		t.Fatalf("day view: %d", dayResp.StatusCode)
	}
	var scheduled []map[string]any
	decodeBody(t, dayResp, &scheduled)
	if len(scheduled) == 0 {
		t.Error("expected the kid's day view to show imported chores")
	}
}

// The exam-week swap: stop_daily ends the kid's other open-ended day
// schedules today (end_date = today, never a delete) while template
// schedules from the imported pack keep running.
func TestImportTemplateStopDailyPausesOtherRoutines(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")

	// A manual daily chore the kid already runs.
	manual := map[string]any{
		"title": "洗晚饭的碗", "category": "core", "points_value": 2,
		"icon": "dish", "description": "",
	}
	resp := env.request(t, "POST", "/api/chores", manual, adminHeaders())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create manual chore: %d", resp.StatusCode)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	decodeBody(t, resp, &created)
	sched := map[string]any{
		"assigned_to": kid, "assignment_type": "individual",
		"day_of_week": 1, "expiry_penalty": "block",
	}
	sresp := env.request(t, "POST", fmt.Sprintf("/api/chores/%d/schedules", created.ID), sched, adminHeaders())
	if sresp.StatusCode != http.StatusCreated {
		t.Fatalf("create manual schedule: %d", sresp.StatusCode)
	}

	// The conflict preview counts it.
	var running struct {
		Running int `json:"running"`
	}
	decodeBody(t, env.expectStatus(t, "GET",
		fmt.Sprintf("/api/users/%d/running-daily?template_id=exam-week", kid), nil, adminHeaders(), http.StatusOK), &running)
	if running.Running != 1 {
		t.Fatalf("expected 1 running daily before import, got %d", running.Running)
	}

	// Import the sprint pack with stop_daily.
	env.expectStatus(t, "POST", "/api/admin/templates/import",
		importBody("exam-week", kid, true), adminHeaders(), http.StatusCreated)

	// The manual schedule is closed today but still exists.
	var endDate any
	var alive int
	if err := env.db.QueryRow(`SELECT end_date FROM chore_schedules WHERE chore_id = ?`, created.ID).Scan(&endDate); err != nil {
		t.Fatalf("load manual schedule: %v", err)
	}
	if endDate == nil {
		t.Error("expected the manual schedule to get an end_date")
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM chore_schedules WHERE chore_id = ?`, created.ID).Scan(&alive); err != nil {
		t.Fatalf("count manual schedules: %v", err)
	}
	if alive != 1 {
		t.Errorf("expected the manual schedule preserved (not deleted), rows=%d", alive)
	}

	// The preview now reports zero other dailies (exam-week ones excluded).
	decodeBody(t, env.expectStatus(t, "GET",
		fmt.Sprintf("/api/users/%d/running-daily?template_id=exam-week", kid), nil, adminHeaders(), http.StatusOK), &running)
	if running.Running != 0 {
		t.Fatalf("expected 0 other running dailies after swap, got %d", running.Running)
	}

	// Re-importing the same pack must not stop its own schedules.
	env.expectStatus(t, "POST", "/api/admin/templates/import",
		importBody("exam-week", kid, true), adminHeaders(), http.StatusCreated)
	var open int
	if err := env.db.QueryRow(
		`SELECT COUNT(*) FROM chore_schedules cs JOIN chores c ON c.id = cs.chore_id
		  WHERE c.external_id = 'exam-week' AND cs.end_date IS NOT NULL`).Scan(&open); err != nil {
		t.Fatalf("count closed exam-week schedules: %v", err)
	}
	if open != 0 {
		t.Errorf("re-import stopped %d of the pack's own schedules", open)
	}
}

// Import is admin-only and validates its payload up front.
func TestImportTemplateAuthAndValidation(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")

	env.expectStatus(t, "POST", "/api/admin/templates/import",
		importBody("grade9-schoolday", kid, false), childHeaders(kid), http.StatusForbidden)
	env.expectStatus(t, "POST", "/api/admin/templates/import",
		importBody("grade9-schoolday", kid, false), nil, http.StatusUnauthorized)

	// No kid, no tasks, bad category, and days that expand to nothing.
	env.expectStatus(t, "POST", "/api/admin/templates/import",
		map[string]any{"template_id": "grade9-schoolday", "tasks": []any{}, "stop_daily": false}, adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/templates/import",
		importBody("grade9-schoolday", 0, false), adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/templates/import",
		importBody("grade9-schoolday", kid, false, map[string]any{
			"title": "神秘任务", "category": "weird", "points": 1, "days": []string{"daily"},
		}), adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/templates/import",
		importBody("grade9-schoolday", kid, false, map[string]any{
			"title": "没排期的任务", "category": "core", "points": 1, "days": []string{},
		}), adminHeaders(), http.StatusBadRequest)

	// A task with no title must abort the whole import (atomicity): nothing
	// may land when the second task fails validation.
	env.expectStatus(t, "POST", "/api/admin/templates/import",
		importBody("grade9-schoolday", kid, false,
			map[string]any{"title": "会回滚的任务", "category": "core", "points": 1, "days": []string{"daily"}},
			map[string]any{"title": "", "category": "core", "points": 1, "days": []string{"daily"}},
		), adminHeaders(), http.StatusBadRequest)
	if n := len(choresOf(t, env)); n != 0 {
		t.Fatalf("expected atomic rollback (0 chores), found %d", n)
	}
}
