package api_test

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/llm"
)

// The parent Copilot endpoints (P3-10/11): draft a task plan from one
// sentence or a photographed school notice, then apply the confirmed cards.
// These tests pin the contract the drawer uses — the injected household
// context, the server-side dedup, and the apply path landing chores plus
// schedules atomically.

// draftReply is the canned JSON the stub LLM answers a draft request with.
// Building it inline keeps each test's intent readable.
func draftReply(tasks string) string {
	return "```json\n{\"tasks\":[" + tasks + "],\"events\":[],\"memos\":[],\"notes\":[\"已按现有定价调整\"]}\n```"
}

// copilotTaskJSON is one task card of a stubbed draft reply.
func copilotTaskJSON(title, category string, points int, days string) string {
	return fmt.Sprintf(`{"title":%q,"description":"当天完成","category":%q,"icon":"📚","points_value":%d,"estimated_minutes":20,"days":[%s],"confidence":0.9,"rationale":"参照现有定价"}`,
		title, category, points, days)
}

// TestCopilotDraftWithoutAI pins the 503 every AI-touching endpoint shares.
func TestCopilotDraftWithoutAI(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")

	env.expectStatus(t, "POST", "/api/admin/copilot/draft",
		map[string]any{"assigned_to": kid, "request": "每天背单词"}, adminHeaders(), http.StatusServiceUnavailable)
}

// TestCopilotDraftValidates pins the up-front checks: the kid must exist,
// the mode must be known, and each mode needs its input.
func TestCopilotDraftValidates(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")

	// Kid zero and unknown kid.
	env.expectStatus(t, "POST", "/api/admin/copilot/draft",
		map[string]any{"assigned_to": 0, "request": "x"}, adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/copilot/draft",
		map[string]any{"assigned_to": 999, "request": "x"}, adminHeaders(), http.StatusNotFound)

	// Text mode without text; flyer mode without photo; bad mode.
	env.expectStatus(t, "POST", "/api/admin/copilot/draft",
		map[string]any{"assigned_to": kid, "request": "  "}, adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/copilot/draft",
		map[string]any{"assigned_to": kid, "mode": "flyer"}, adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/copilot/draft",
		map[string]any{"assigned_to": kid, "mode": "magic", "request": "x"}, adminHeaders(), http.StatusBadRequest)

	// Child may not draft.
	env.expectStatus(t, "POST", "/api/admin/copilot/draft",
		map[string]any{"assigned_to": kid, "request": "x"}, childHeaders(kid), http.StatusForbidden)
	env.expectStatus(t, "POST", "/api/admin/copilot/draft",
		map[string]any{"assigned_to": kid, "request": "x"}, nil, http.StatusUnauthorized)
}

// TestCopilotDraftFromText exercises the whole text-mode flow against a
// stub LLM: the household context (existing chore titles, reward ladder)
// must reach the prompt, and the fenced reply must parse into cards.
func TestCopilotDraftFromText(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")

	// An existing chore and two rewards: both must be injected as anchors.
	env.expectStatus(t, "POST", "/api/chores",
		map[string]any{"title": "数学错题订正", "category": "required", "points_value": 6}, adminHeaders(), http.StatusCreated)
	env.expectStatus(t, "POST", "/api/rewards",
		map[string]any{"name": "看电影", "cost": 30}, adminHeaders(), http.StatusCreated)
	env.expectStatus(t, "POST", "/api/rewards",
		map[string]any{"name": "乐高一套", "cost": 200}, adminHeaders(), http.StatusCreated)

	stub := newTuneAI(t, draftReply(copilotTaskJSON("背单词 20 个", "core", 5, `"daily"`)))
	env.chores.SetAI(llm.New(stub.server.URL+"/v1", "", "test-model"), nil)

	resp := env.expectStatus(t, "POST", "/api/admin/copilot/draft",
		map[string]any{"assigned_to": kid, "request": "下周每天背 20 个英语单词"}, adminHeaders(), http.StatusOK)
	var out struct {
		Tasks []struct {
			Title       string   `json:"title"`
			Category    string   `json:"category"`
			PointsValue int      `json:"points_value"`
			Days        []string `json:"days"`
			Confidence  float64  `json:"confidence"`
		} `json:"tasks"`
		Notes []string `json:"notes"`
		Mode  string   `json:"mode"`
	}
	decodeBody(t, resp, &out)
	if out.Mode != "text" {
		t.Errorf("expected mode=text, got %q", out.Mode)
	}
	if len(out.Tasks) != 1 || out.Tasks[0].Title != "背单词 20 个" || out.Tasks[0].PointsValue != 5 {
		t.Fatalf("expected the drafted card parsed, got %+v", out.Tasks)
	}
	if len(out.Notes) == 0 {
		t.Error("expected the model's notes surfaced")
	}

	// The prompt must carry the kid, the existing chore and both reward
	// rungs, so point suggestions are anchored to this family's economy.
	stub.mu.Lock()
	sent := stub.lastBody
	stub.mu.Unlock()
	for _, want := range []string{"小言", "数学错题订正", "看电影 30分", "乐高一套 200分", "下周每天背 20 个英语单词"} {
		if !strings.Contains(sent, want) {
			t.Errorf("AI prompt missing %q", want)
		}
	}
}

// TestCopilotDraftDedup: a drafted task duplicating an existing chore is
// dropped server side and named in notes — the draft page never silently
// creates a duplicate (design rule 3).
func TestCopilotDraftDedup(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")

	env.expectStatus(t, "POST", "/api/chores",
		map[string]any{"title": "英语朗读 15 分钟", "category": "core", "points_value": 3}, adminHeaders(), http.StatusCreated)

	// The stub drafts one fresh task and one near-duplicate (the existing
	// title with a numeric variant) — the duplicate must be dropped.
	reply := draftReply(
		copilotTaskJSON("英语朗读 15 分钟", "core", 3, `"daily"`) + "," +
			copilotTaskJSON("数学口算 30 题", "core", 4, `"weekdays"`))
	stub := newTuneAI(t, reply)
	env.chores.SetAI(llm.New(stub.server.URL+"/v1", "", "test-model"), nil)

	resp := env.expectStatus(t, "POST", "/api/admin/copilot/draft",
		map[string]any{"assigned_to": kid, "request": "加朗读和口算"}, adminHeaders(), http.StatusOK)
	var out struct {
		Tasks []struct {
			Title string `json:"title"`
		} `json:"tasks"`
		Notes []string `json:"notes"`
	}
	decodeBody(t, resp, &out)
	if len(out.Tasks) != 1 || out.Tasks[0].Title != "数学口算 30 题" {
		t.Fatalf("expected only the fresh task kept, got %+v", out.Tasks)
	}
	found := false
	for _, n := range out.Notes {
		if strings.Contains(n, "英语朗读 15 分钟") && strings.Contains(n, "重复") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a dedup note naming the duplicate, got %v", out.Notes)
	}
}

// writeTestImage drops a tiny valid PNG under the temp dir so the upload
// endpoint can push it into the test server's uploads storage.
func writeTestImage(t *testing.T, name string) string {
	t.Helper()
	// 1x1 transparent PNG.
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d,
		'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15,
		0xc4, 0x89, 0, 0, 0, 0x0a, 'I', 'D', 'A', 'T', 0x78, 0x9c, 0x63, 0, 1,
		0, 0, 5, 0, 1, 0x0d, 0x0a, 0x2d, 0xb4, 0, 0, 0, 0, 'I', 'E', 'N', 'D',
		0xae, 0x42, 0x60, 0x82}
	dir := filepath.Join(os.TempDir(), "openchore-copilot-test")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, png, 0o640); err != nil {
		t.Fatalf("write image: %v", err)
	}
	return path
}

// TestCopilotDraftFromFlyer exercises photo mode: the image bytes must be
// sent to the model as a data URL, and events/memos from the reply must
// survive parsing (the drawer's "事件/家长备忘" sections).
func TestCopilotDraftFromFlyer(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")

	stub := newTuneAI(t, "```json\n{\"tasks\":[{\"title\":\"收拾春游零食\",\"description\":\"自己装零食和水壶\",\"category\":\"core\",\"icon\":\"🎒\",\"points_value\":3,\"estimated_minutes\":15,\"days\":[],\"date\":\"2026-10-27\",\"confidence\":0.8,\"rationale\":\"出发前一天准备\"}],\"events\":[{\"title\":\"春游\",\"date\":\"2026-10-28\",\"template_suggestion\":\"\"}],\"memos\":[{\"title\":\"缴费 200 元\",\"date\":\"2026-10-25\"}],\"notes\":[]}\n```")
	env.chores.SetAI(llm.New(stub.server.URL+"/v1", "", "test-model"), nil)

	// Write a tiny PNG, push it through the real upload endpoint, then
	// draft from the returned /uploads/ path.
	imgPath := writeTestImage(t, "u.png")
	file, err := os.Open(imgPath)
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	defer file.Close()
	body := &bytesReaderCloser{boundary: "testboundary", fieldName: "photo", fileName: "flyer.png", reader: file}
	req, err := http.NewRequest("POST", env.server.URL+"/api/upload", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary=testboundary")
	req.Header.Set("Authorization", "Bearer "+sessionToken(1))
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload failed: %d %s", resp.StatusCode, raw)
	}
	var up struct {
		URL string `json:"url"`
	}
	decodeBody(t, resp, &up)

	dresp := env.expectStatus(t, "POST", "/api/admin/copilot/draft",
		map[string]any{"assigned_to": kid, "mode": "flyer", "photo_url": up.URL}, adminHeaders(), http.StatusOK)
	var out struct {
		Tasks []struct {
			Title string `json:"title"`
			Date  string `json:"date"`
		} `json:"tasks"`
		Events []map[string]any `json:"events"`
		Memos  []map[string]any `json:"memos"`
		Mode   string           `json:"mode"`
	}
	decodeBody(t, dresp, &out)
	if out.Mode != "flyer" {
		t.Errorf("expected mode=flyer, got %q", out.Mode)
	}
	if len(out.Tasks) != 1 || out.Tasks[0].Date != "2026-10-27" {
		t.Fatalf("expected the dated flyer task parsed, got %+v", out.Tasks)
	}
	if len(out.Events) != 1 || out.Events[0]["title"] != "春游" {
		t.Errorf("expected the 春游 event, got %+v", out.Events)
	}
	if len(out.Memos) != 1 || out.Memos[0]["title"] != "缴费 200 元" {
		t.Errorf("expected the parent memo, got %+v", out.Memos)
	}

	// The image must have reached the model as a base64 data URL.
	stub.mu.Lock()
	sent := stub.lastBody
	stub.mu.Unlock()
	if !strings.Contains(sent, "data:image/png;base64,") {
		t.Error("flyer prompt missing the base64 image part")
	}
}

// bytesReaderCloser builds a multipart body around one file part without
// pulling in multipart writer boilerplate per test.
type bytesReaderCloser struct {
	boundary  string
	fieldName string
	fileName  string
	reader    io.Reader
	buf       []byte
	done      bool
}

func (b *bytesReaderCloser) Read(p []byte) (int, error) {
	if b.buf == nil {
		head := "--" + b.boundary + "\r\nContent-Disposition: form-data; name=\"" + b.fieldName + "\"; filename=\"" + b.fileName + "\"\r\nContent-Type: image/png\r\n\r\n"
		data, err := io.ReadAll(b.reader)
		if err != nil {
			return 0, err
		}
		b.buf = append([]byte(head), data...)
		b.buf = append(b.buf, []byte("\r\n--"+b.boundary+"--\r\n")...)
	}
	if len(b.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}
func (b *bytesReaderCloser) Close() error { return nil }

// TestCopilotDraftFlyerRejectsNonUpload: photo_url must be an /uploads/
// path — an arbitrary URL must not be fetched (SSRF guard).
func TestCopilotDraftFlyerRejectsNonUpload(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")

	stub := newTuneAI(t, draftReply("[]"))
	env.chores.SetAI(llm.New(stub.server.URL+"/v1", "", "test-model"), nil)

	env.expectStatus(t, "POST", "/api/admin/copilot/draft",
		map[string]any{"assigned_to": kid, "mode": "flyer", "photo_url": "http://example.com/x.png"},
		adminHeaders(), http.StatusBadRequest)
}

// applyBody builds an apply payload from fixed task cards.
func applyBody(assignedTo int, source string, tasks ...map[string]any) map[string]any {
	if tasks == nil {
		tasks = []map[string]any{{
			"title": "背单词 20 个", "description": "当天完成", "category": "core",
			"subject": "english", "icon": "📚", "points_value": 5, "estimated_minutes": 20,
			"days": []string{"daily"}, "expiry": "block",
		}}
	}
	return map[string]any{"assigned_to": assignedTo, "source": source, "tasks": tasks}
}

// TestApplyCopilotDraft lands confirmed cards as chores plus schedules in
// one transaction, with the draft's source stamped for the UI badge.
func TestApplyCopilotDraft(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")
	// The card carries subject="english": seed the word list the way the
	// production startup (taxonomy pack import) does — the vocabulary is
	// table-driven (042) and an unseeded table rejects every subject.
	seedSubjects(t, env)

	resp := env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "copilot"), adminHeaders(), http.StatusCreated)
	var out struct {
		Applied int `json:"applied"`
	}
	decodeBody(t, resp, &out)
	if out.Applied != 1 {
		t.Fatalf("expected 1 applied chore, got %d", out.Applied)
	}

	// The card's subject lands on the chore: AI-drafted schoolwork must
	// show up under the media library's subject chip.
	rows, err := env.db.Query(`SELECT title, source, external_id, points_value, COALESCE(subject, '') FROM chores`)
	if err != nil {
		t.Fatalf("query chores: %v", err)
	}
	defer rows.Close()
	// Drain the rows fully (MaxOpenConns(1): an open Rows holds the only
	// connection, so leaving one undrained deadlocks the next query).
	var title, source, ext, subject string
	var pts int
	seen := 0
	for rows.Next() {
		seen++
		if err := rows.Scan(&title, &source, &ext, &pts, &subject); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if seen != 1 {
		t.Fatalf("expected exactly 1 chore row, found %d", seen)
	}
	if title != "背单词 20 个" || source != "copilot" || pts != 5 {
		t.Fatalf("chore row wrong: %q %q %d", title, source, pts)
	}
	if subject != "english" {
		t.Fatalf("expected the card's subject=english on the chore, got %q", subject)
	}

	// One schedule per expanded day (daily = 7).
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM chore_schedules WHERE assigned_to = ?`, kid).Scan(&n); err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	if n != 7 {
		t.Fatalf("expected 7 daily schedules, got %d", n)
	}

	// No zero multipliers: the points system would silently zero out.
	var zeros int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM chore_schedules WHERE points_multiplier = 0`).Scan(&zeros); err != nil {
		t.Fatalf("count zeros: %v", err)
	}
	if zeros != 0 {
		t.Errorf("expected no zero points_multiplier rows, got %d", zeros)
	}
}

// TestApplyCopilotDraftDatedOneOff: a flyer card with date and no days
// lands as a single specific-date schedule, not a weekly pattern.
func TestApplyCopilotDraftDatedOneOff(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")

	tasks := []map[string]any{{
		"title": "收拾春游零食", "category": "core", "points_value": 3,
		"days": []string{}, "date": "2026-10-27", "expiry": "no_points",
	}}
	env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "flyer", tasks...), adminHeaders(), http.StatusCreated)

	var dow any
	var specific string
	if err := env.db.QueryRow(`SELECT day_of_week, specific_date FROM chore_schedules`).Scan(&dow, &specific); err != nil {
		t.Fatalf("load schedule: %v", err)
	}
	if dow != nil {
		t.Errorf("expected no day_of_week for a dated one-off, got %v", dow)
	}
	// The column is a timestamp in SQLite; a YYYY-MM-DD write reads back
	// UTC-normalized, so compare on the date prefix like the store tests.
	if !strings.HasPrefix(specific, "2026-10-27") {
		t.Errorf("expected specific_date=2026-10-27, got %q", specific)
	}
}

// TestApplyPlannerDraft pins the L3 apply path: an adopted learner-plan
// card lands with source="planner", so the follow-up query (and the UI
// badge) can tell the AI plan's tasks from the copilot's one-off drafts.
func TestApplyPlannerDraft(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")
	// Same as TestApplyCopilotDraft: the default card carries
	// subject="english", so the word list must be seeded first (042).
	seedSubjects(t, env)

	resp := env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "planner"), adminHeaders(), http.StatusCreated)
	var out struct {
		Applied int `json:"applied"`
	}
	decodeBody(t, resp, &out)
	if out.Applied != 1 {
		t.Fatalf("expected 1 applied chore, got %d", out.Applied)
	}

	var source string
	if err := env.db.QueryRow(`SELECT source FROM chores`).Scan(&source); err != nil {
		t.Fatalf("load chore: %v", err)
	}
	if source != "planner" {
		t.Fatalf("expected source=planner, got %q", source)
	}

	// The follow-up query sees it: one adopted planner chore (daily = 7
	// schedules, but they hang off one chore row), none done yet.
	fu, err := env.store.PlannerChoreFollowup(t.Context(), int64(kid), "2026-01-01", "2099-01-01")
	if err != nil {
		t.Fatalf("followup: %v", err)
	}
	if fu.Adopted != 7 || fu.Completed != 0 {
		t.Fatalf("expected followup 7/0, got %d/%d", fu.Adopted, fu.Completed)
	}
}

// TestApplyCopilotDraftValidates pins the up-front checks and the
// atomicity: a bad card aborts the whole apply, leaving nothing behind.
func TestApplyCopilotDraftValidates(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")

	// Auth, kid, tasks, source.
	env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "copilot"), childHeaders(kid), http.StatusForbidden)
	env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "copilot"), nil, http.StatusUnauthorized)
	env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		map[string]any{"assigned_to": 0, "source": "copilot", "tasks": []any{}}, adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		map[string]any{"assigned_to": 999, "source": "copilot", "tasks": []any{}}, adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "weird"), adminHeaders(), http.StatusBadRequest)

	// Bad category, out-of-band points, and empty title all 400.
	env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "copilot", map[string]any{"title": "x", "category": "weird", "points_value": 5, "days": []string{"daily"}}),
		adminHeaders(), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "copilot", map[string]any{"title": "x", "category": "core", "points_value": 99, "days": []string{"daily"}}),
		adminHeaders(), http.StatusBadRequest)
	// A subject outside the ten-value vocabulary is rejected like a
	// bad category — the media library's chips only know the ten
	// (042 widened the five).
	env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "copilot", map[string]any{"title": "x", "category": "core", "points_value": 5, "subject": "gym", "days": []string{"daily"}}),
		adminHeaders(), http.StatusBadRequest)

	// Atomicity: the second card fails, the first must not land.
	env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "copilot",
			map[string]any{"title": "会生效吗", "category": "core", "points_value": 5, "days": []string{"daily"}},
			map[string]any{"title": "", "category": "core", "points_value": 5, "days": []string{"daily"}},
		), adminHeaders(), http.StatusBadRequest)
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM chores`).Scan(&n); err != nil {
		t.Fatalf("count chores: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected atomic rollback (0 chores), found %d", n)
	}
}

// TestApplyCopilotDraftDedupOnRewrite pins the write-path dedup guard
// (2026-10-22): applying the same card twice — the planner/verify loop used
// to do exactly that — must skip the second copy, not land a duplicate chore.
// The live database had「AI规划验收-每日口算10题」×4 from repeated applies.
func TestApplyCopilotDraftDedupOnRewrite(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "小言")
	// The default card carries subject="english": seed the word list
	// first (042) like the other apply tests.
	seedSubjects(t, env)

	type applyOut struct {
		Applied int      `json:"applied"`
		Skipped []string `json:"skipped"`
	}

	// First apply lands the card.
	resp := env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "planner"), adminHeaders(), http.StatusCreated)
	var first applyOut
	decodeBody(t, resp, &first)
	if first.Applied != 1 || len(first.Skipped) != 0 {
		t.Fatalf("expected 1 applied / 0 skipped on first apply, got %d / %v", first.Applied, first.Skipped)
	}

	// Second apply of the same card is deduped: 201 still, but nothing new.
	resp = env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "planner"), adminHeaders(), http.StatusCreated)
	var second applyOut
	decodeBody(t, resp, &second)
	if second.Applied != 0 || len(second.Skipped) != 1 {
		t.Fatalf("expected 0 applied / 1 skipped on repeat apply, got %d / %v", second.Applied, second.Skipped)
	}

	// Exactly one chore row and one set of schedules on disk.
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM chores`).Scan(&n); err != nil {
		t.Fatalf("count chores: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 chore after two applies, found %d", n)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM chore_schedules`).Scan(&n); err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	if n != 7 {
		t.Fatalf("expected 7 schedules (one daily set), found %d", n)
	}

	// A duplicate card inside one request is caught the same way: the
	// planner drafts repeat themselves, so the guard also dedups within
	// the batch.
	resp = env.expectStatus(t, "POST", "/api/admin/copilot/apply",
		applyBody(kid, "planner",
			map[string]any{"title": "读英语绘本 15 分钟", "category": "core", "points_value": 4, "days": []string{"daily"}},
			map[string]any{"title": "读英语绘本15分钟", "category": "core", "points_value": 4, "days": []string{"daily"}},
		), adminHeaders(), http.StatusCreated)
	var third applyOut
	decodeBody(t, resp, &third)
	if third.Applied != 1 || len(third.Skipped) != 1 {
		t.Fatalf("expected 1 applied / 1 skipped for in-batch duplicate, got %d / %v", third.Applied, third.Skipped)
	}
}
