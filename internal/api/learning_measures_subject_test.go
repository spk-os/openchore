package api_test

// DEF-AI-MEASURE-SUBJECT-R1 专项：learning_measures.go 的学科守卫。
//
// 全部走真实 SQLite（setupTest 跑完整迁移，setupPaperTest 用真 ImportPack
// 写入 subjects 词表）+ httptest，AI 复用包内既有 fakeAI / fakePaperAI。
// 不直接调用 store 或 handler 内部函数（那只是对 production 的复述）：
// 每一个断言都从 HTTP 或 DB 观测可达路径：
//   - 手动路径：POST/PUT /api/admin/measures
//   - 自动路径：ai_auto_paper_analysis=on 时孩子拍照上交 → 后台分析 →
//     distillMeasuresForAnalysis（无母会话点击，无人造 analysis 行）
//   - 手动 distill：POST /api/admin/analyses/{id}/measures

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// ─────────────────────────── 夹具 ───────────────────────────

// measureDraftJSON is one element of the fake model's {"measures":[...]}.
type measureDraftJSON struct {
	Subject    string   `json:"subject"`
	KC         string   `json:"kc"`
	Title      string   `json:"title"`
	Detail     string   `json:"detail"`
	ErrorTypes []string `json:"error_types"`
	Priority   int      `json:"priority"`
}

func measureDraft(subject, kc, title string) measureDraftJSON {
	return measureDraftJSON{
		Subject: subject, KC: kc, Title: title,
		Detail:     title + "：怎么做、做多少、坚持多久。",
		ErrorTypes: []string{"calculation"},
		Priority:   1,
	}
}

// setMeasures swaps the JSON the fake model answers for the 034 distill
// prompt. Only the distill prompt changes answer — segmentation/judgement
// still get their own fixtures.
func (f *fakePaperAI) setMeasures(drafts ...measureDraftJSON) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := json.Marshal(map[string]any{"measures": drafts})
	if err != nil {
		panic(err)
	}
	f.measures = string(b)
}

// insertAnalysis writes a finished analysis row directly (the kid paper
// half is irrelevant to the distill pass; this only skips the photo parse).
func (e *testEnv) insertAnalysis(t *testing.T, kidID int, subject string) int64 {
	t.Helper()
	// 借既有的一个日程满足外键；distill 不读它，测试仍走真路由。
	res, err := e.db.Exec(`INSERT INTO chore_completions (chore_schedule_id, completed_by, status, photo_url, completion_date)
		SELECT id, ?, 'pending', '/uploads/x.jpg', '2026-03-11' FROM chore_schedules LIMIT 1`, kidID)
	if err != nil {
		t.Fatalf("seeding completion: %v", err)
	}
	completionID, _ := res.LastInsertId()
	res, err = e.db.Exec(`INSERT INTO paper_analyses (completion_id, user_id, created_by, subject, status, stage)
		VALUES (?, ?, 1, ?, 'done', 'done')`, completionID, kidID, subject)
	if err != nil {
		t.Fatalf("seeding analysis: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// measureTitles is the kid's stored measure titles (analysis origin only).
func (e *testEnv) measureTitles(t *testing.T, kidID int) []string {
	t.Helper()
	rows, err := e.db.Query(`SELECT title FROM learning_measures WHERE user_id = ? AND origin = 'analysis' ORDER BY id`, kidID)
	if err != nil {
		t.Fatalf("listing measures: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// kcSubjects lists (subject, name) for every knowledge tag on the DB.
func (e *testEnv) knowledgeTags(t *testing.T) [][2]string {
	t.Helper()
	rows, err := e.db.Query(`SELECT subject, name FROM knowledge_tags ORDER BY id`)
	if err != nil {
		t.Fatalf("listing knowledge tags: %v", err)
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var s, n string
		if err := rows.Scan(&s, &n); err != nil {
			t.Fatal(err)
		}
		out = append(out, [2]string{s, n})
	}
	return out
}

// measureCount 统计目标用户的全部 learning_measures（不限 origin/status），
// 用来观测手动路径被拒请求是否真的零写入（measureTitles 只查 analysis）。
func (e *testEnv) measureCount(t *testing.T, kidID int) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM learning_measures WHERE user_id = ?`, kidID).Scan(&n); err != nil {
		t.Fatalf("counting measures: %v", err)
	}
	return n
}

func (e *testEnv) distilHTTP(t *testing.T, analysisID int64) (int, string) {
	t.Helper()
	resp := e.request(t, "POST", fmt.Sprintf("/api/admin/analyses/%d/measures", analysisID), nil, adminHeaders())
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *testEnv) measureByTitle(t *testing.T, kidID int, title string) map[string]any {
	t.Helper()
	resp := e.expectStatus(t, "GET", fmt.Sprintf("/api/admin/measures?user_id=%d", kidID), nil, adminHeaders(), http.StatusOK)
	var list []map[string]any
	decodeBody(t, resp, &list)
	for _, m := range list {
		if m["title"] == title {
			return m
		}
	}
	return nil
}

func nameSet(pairs [][2]string) map[string]bool {
	out := make(map[string]bool, len(pairs))
	for _, p := range pairs {
		out[p[0]+"\x00"+p[1]] = true
	}
	return out
}

// tagSnapshot 记录用例开始时的知识点集合。seedSubjects 会导入整套课标
// 知识点，所以断言"新增了哪些 KC"必须用前后差集，不能假定表是空的。
func (e *testEnv) tagSnapshot(t *testing.T) map[string]bool {
	t.Helper()
	return nameSet(e.knowledgeTags(t))
}

// newTagsSince 返回快照之后新增的 subject\x00name 集合。
func (e *testEnv) newTagsSince(t *testing.T, before map[string]bool) map[string]bool {
	t.Helper()
	added := make(map[string]bool)
	for k := range nameSet(e.knowledgeTags(t)) {
		if !before[k] {
			added[k] = true
		}
	}
	return added
}

// newKCWithEmptySubject 报告新增里是否有 subject=” 的知识点。
func (e *testEnv) newKCWithEmptySubject(t *testing.T, before map[string]bool) []string {
	t.Helper()
	var hits []string
	for k := range e.newTagsSince(t, before) {
		if strings.HasPrefix(k, "\x00") {
			hits = append(hits, k)
		}
	}
	return hits
}

// captureLogs collects log output (process-global default logger) for the
// span of the test.
func captureLogs(t *testing.T) *strings.Builder {
	t.Helper()
	var mu sync.Mutex
	var buf strings.Builder
	prev := log.Writer()
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// ───────────── 1) 合法 subject draft 完整保留策略字段 ─────────────

func TestDistillMeasuresSubjectValidDraftKeepsFields(t *testing.T) {
	env, f := setupPaperTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	env.createScheduledChore(t, "Make bed", kidID, nil)
	analysisID := env.insertAnalysis(t, kidID, "math")

	f.setMeasures(measureDraftJSON{
		Subject: "math", KC: "合法加法KC", Title: "合法措施：每天 5 题",
		Detail:     "每天饭后 5 题，连做 5 天。",
		ErrorTypes: []string{"calculation", "method"}, Priority: 3,
	})

	code, body := env.distilHTTP(t, analysisID)
	if code != http.StatusOK {
		t.Fatalf("distill = %d %s", code, body)
	}
	var out struct {
		Count   int `json:"count"`
		Created []struct {
			ID         int64    `json:"id"`
			Subject    string   `json:"subject"`
			KCTagID    *int64   `json:"kc_tag_id"`
			KCName     string   `json:"kc_name"`
			Origin     string   `json:"origin"`
			OriginID   *int64   `json:"origin_id"`
			Priority   int      `json:"priority"`
			ErrorTypes []string `json:"error_types"`
			Status     string   `json:"status"`
		} `json:"created"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if out.Count != 1 || len(out.Created) != 1 {
		t.Fatalf("want 1 created measure, got %d (%s)", out.Count, body)
	}
	m := out.Created[0]
	if m.Subject != "math" {
		t.Errorf("subject = %q, want math", m.Subject)
	}
	if m.Origin != "analysis" || m.OriginID == nil || *m.OriginID != analysisID {
		t.Errorf("origin = %q/%v, want analysis/%d", m.Origin, m.OriginID, analysisID)
	}
	if m.Priority != 3 {
		t.Errorf("priority = %d, want 3", m.Priority)
	}
	if strings.Join(m.ErrorTypes, ",") != "calculation,method" {
		t.Errorf("error_types = %v", m.ErrorTypes)
	}
	if m.KCTagID == nil || m.KCName != "合法加法KC" {
		t.Fatalf("kc binding lost: KCTagID=%v KCName=%q", m.KCTagID, m.KCName)
	}
	var tagSubject, tagName string
	if err := env.db.QueryRow(`SELECT subject, name FROM knowledge_tags WHERE id = ?`, *m.KCTagID).Scan(&tagSubject, &tagName); err != nil {
		t.Fatalf("reading KC: %v", err)
	}
	if tagSubject != "math" || tagName != "合法加法KC" {
		t.Errorf("KC row = (%q,%q), want (math,合法加法KC)", tagSubject, tagName)
	}
	// And the stored row (not just the response echo).
	var dbSubject, dbOrigin, dbTypes string
	var dbPriority int
	if err := env.db.QueryRow(`SELECT subject, origin, error_types, priority FROM learning_measures WHERE id = ?`, m.ID).
		Scan(&dbSubject, &dbOrigin, &dbTypes, &dbPriority); err != nil {
		t.Fatalf("reading measure: %v", err)
	}
	if dbSubject != "math" || dbOrigin != "analysis" || dbPriority != 3 || !strings.Contains(dbTypes, "method") {
		t.Errorf("db row = %q/%q/%q/%d", dbSubject, dbOrigin, dbTypes, dbPriority)
	}
}

// ─── 2) 未知/disabled/大小写不匹配：整条跳过，零措施零KC，合法同批继续 ───

func TestDistillMeasuresSubjectUnknownDisabledCaseSkipped(t *testing.T) {
	env, f := setupPaperTest(t) // 真 ImportPack 的十学科词表
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	env.createScheduledChore(t, "Make bed", kidID, nil)
	analysisID := env.insertAnalysis(t, kidID, "math")
	before := env.tagSnapshot(t)
	// 真停用一个真学科：distill 每次现读 enabled 词表，立刻生效。
	env.mustExec(t, `UPDATE subjects SET enabled = 0 WHERE key = 'physics'`)

	f.setMeasures(
		measureDraft("gym", "体育KC", "非法-未知学科"),
		measureDraft("physics", "停用KC", "非法-已停用学科"),
		measureDraft("Math", "大写KC", "非法-大小写不匹配"),
		// 空 subject 的 draft 即使带 KC 也合法落库（不建/不绑 KC）。
		measureDraft("", "空学科KC", "合法-空学科"),
		measureDraft("math", "合法数学KC", "合法-数学"),
		measureDraft("english", "合法英语KC", "合法-英语"),
	)

	code, body := env.distilHTTP(t, analysisID)
	if code != http.StatusOK {
		t.Fatalf("distill = %d %s", code, body)
	}
	var out struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	if out.Count != 3 {
		t.Fatalf("count = %d, want 3 (%s)", out.Count, body)
	}

	want := []string{"合法-空学科", "合法-数学", "合法-英语"}
	got := env.measureTitles(t, kidID)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("measures = %v, want %v", got, want)
	}

	// 零 FindOrCreateKnowledgeTag 副作用：新增 KC 只有合法学科的那两个；
	// 非法条目（含空 subject 带 KC 的 draft）一律没有建 KC。
	wantTags := map[string]bool{
		"math\x00合法数学KC":    true,
		"english\x00合法英语KC": true,
	}
	if have := env.newTagsSince(t, before); fmt.Sprint(have) != fmt.Sprint(wantTags) {
		t.Fatalf("new knowledge_tags = %v, want exactly %v", have, wantTags)
	}
	if hits := env.newKCWithEmptySubject(t, before); len(hits) != 0 {
		t.Fatalf("subject='' KC leaked: %v", hits)
	}
	// 非法条目没有在 measures 里留下任何行（标题维度已在上面覆盖）。
	// 空 subject 措施不带 KC 绑定（KC 非空但 subject 空 → 不建不绑）。
	if m := env.measureByTitle(t, kidID, "合法-空学科"); m == nil || m["kc_tag_id"] != nil {
		t.Fatalf("empty-subject measure must exist without KC: %v", m)
	}
}

// ─── 3) 空 subject 措施合法落库、不得建/绑 subject='' KC（手动路径） ───

func TestCreateMeasureSubjectEmptyStaysEmptyNoDefaultMath(t *testing.T) {
	env, _ := setupPaperTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	before := env.tagSnapshot(t)

	resp := env.expectStatus(t, "POST", "/api/admin/measures", map[string]any{
		"user_id": kidID, "title": "全科习惯：睡前复盘", "detail": "每晚 5 分钟说今天学了什么。",
		"priority": 2, "kc_tag_id": nil,
	}, adminHeaders(), http.StatusCreated)
	var m struct {
		ID      int64  `json:"id"`
		Subject string `json:"subject"`
		Origin  string `json:"origin"`
	}
	decodeBody(t, resp, &m)
	if m.Subject != "" {
		t.Fatalf("omitted subject stored as %q, want \"\" (never default math)", m.Subject)
	}
	if m.Origin != "manual" {
		t.Errorf("origin = %q, want manual", m.Origin)
	}
	var dbSubject string
	if err := env.db.QueryRow(`SELECT subject FROM learning_measures WHERE id = ?`, m.ID).Scan(&dbSubject); err != nil {
		t.Fatal(err)
	}
	if dbSubject != "" {
		t.Fatalf("db subject = %q, want empty", dbSubject)
	}
	if added := env.newTagsSince(t, before); len(added) != 0 {
		t.Fatalf("empty-subject measure created knowledge tags: %v", added)
	}

	// 手动路径的非法 subject：未知 / disabled / 大小写不匹配 → 400，零写入。
	for _, subj := range []string{"gym", "Math", "math "} {
		env.expectStatus(t, "POST", "/api/admin/measures", map[string]any{
			"user_id": kidID, "subject": subj, "title": "x", "detail": "y",
		}, adminHeaders(), http.StatusBadRequest)
	}
	// 停用学科按项目既有语义要换一个 handler（见 TestChoreSubjectDisabled）：
	// 本 handler 的 validSubject 缓存是启动快照，直接改表它仍会放行。
	fresh := setupTest(t)
	fresh.createAdmin(t)
	seedSubjects(t, fresh)
	freshKid := fresh.createChild(t, "Kid2")
	fresh.mustExec(t, `UPDATE subjects SET enabled = 0 WHERE key = 'physics'`)
	fresh.expectStatus(t, "POST", "/api/admin/measures", map[string]any{
		"user_id": freshKid, "subject": "physics", "title": "x", "detail": "y",
	}, adminHeaders(), http.StatusBadRequest)
	fresh.expectStatus(t, "POST", "/api/admin/measures", map[string]any{
		"user_id": freshKid, "subject": "math", "title": "fresh-math", "detail": "y",
	}, adminHeaders(), http.StatusCreated)
	if m := fresh.measureByTitle(t, freshKid, "fresh-math"); m == nil || m["subject"] != "math" {
		t.Fatalf("fresh handler did not store the one valid math row: %v", m)
	}
	// 被拒的手动请求零写入：全量计数仍只有上面那条空 subject 习惯措施，
	// 且所有被拒请求用的标题 "x" 一条都不存在（含 origin=manual）。
	if n := env.measureCount(t, kidID); n != 1 {
		t.Fatalf("rejected manual creates left rows: total = %d, want 1", n)
	}
	if m := env.measureByTitle(t, kidID, "x"); m != nil {
		t.Fatalf("rejected manual create persisted: %v", m)
	}

	// 合法手动 subject 正常落库。
	resp = env.expectStatus(t, "POST", "/api/admin/measures", map[string]any{
		"user_id": kidID, "subject": "english", "title": "英语晨读", "detail": "每天 10 分钟。",
	}, adminHeaders(), http.StatusCreated)
	var ok struct {
		ID      int64  `json:"id"`
		Subject string `json:"subject"`
	}
	decodeBody(t, resp, &ok)
	if ok.Subject != "english" {
		t.Fatalf("valid subject = %q", ok.Subject)
	}

	// PUT 同样守门：非法 400 且原值不变；清空为空合法。
	env.expectStatus(t, "PUT", fmt.Sprintf("/api/admin/measures/%d", ok.ID), map[string]any{
		"subject": "gym", "title": "英语晨读", "detail": "每天 10 分钟。",
	}, adminHeaders(), http.StatusBadRequest)
	var afterBad string
	_ = env.db.QueryRow(`SELECT subject FROM learning_measures WHERE id = ?`, ok.ID).Scan(&afterBad)
	if afterBad != "english" {
		t.Fatalf("rejected PUT changed subject to %q", afterBad)
	}
	env.expectStatus(t, "PUT", fmt.Sprintf("/api/admin/measures/%d", ok.ID), map[string]any{
		"subject": "", "title": "英语晨读", "detail": "每天 10 分钟。",
	}, adminHeaders(), http.StatusOK)
	var afterClear string
	_ = env.db.QueryRow(`SELECT subject FROM learning_measures WHERE id = ?`, ok.ID).Scan(&afterClear)
	if afterClear != "" {
		t.Fatalf("cleared subject = %q, want empty", afterClear)
	}
}

// ─── 4) subjects 词表加载失败：零写入 + 稳定通用 500，不回显 SQL ───

// breakSubjectsVocabulary makes ListSubjects (and only it) fail on the real
// DB: the subjects table is renamed, so the store's SELECT errors with an
// SQLite "no such table" message while every other table keeps working.
func breakSubjectsVocabulary(t *testing.T, env *testEnv) {
	t.Helper()
	env.mustExec(t, `ALTER TABLE subjects RENAME TO subjects_def_subject_r1`)
}

func TestDistillMeasuresSubjectVocabularyLoadFailure(t *testing.T) {
	t.Run("manual_distill", func(t *testing.T) {
		env, f := setupPaperTest(t)
		env.createAdmin(t)
		kidID := env.createChild(t, "Kid")
		env.createScheduledChore(t, "Make bed", kidID, nil)
		analysisID := env.insertAnalysis(t, kidID, "math")
		f.setMeasures(measureDraft("math", "不该落库的KC", "不该落库的措施"))
		before := env.tagSnapshot(t)

		logs := captureLogs(t)
		breakSubjectsVocabulary(t, env)
		code, body := env.distilHTTP(t, analysisID)

		if code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 (%s)", code, body)
		}
		var errBody struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(body), &errBody); err != nil || errBody.Error == "" {
			t.Fatalf("body is not the JSON error shape: %q", body)
		}
		// 稳定通用消息：不泄露 SQL/表名/驱动细节，也不冒充上游 AI 的 502。
		lower := strings.ToLower(body)
		for _, bad := range []string{"select", "subjects_def", "no such table", "sql", "sqlite", "ai distill"} {
			if strings.Contains(lower, bad) {
				t.Errorf("500 body leaks %q: %s", bad, body)
			}
		}
		if !strings.Contains(errBody.Error, "failed to load subjects") {
			t.Errorf("error = %q, want the stable generic message", errBody.Error)
		}

		if got := env.measureTitles(t, kidID); len(got) != 0 {
			t.Fatalf("measures written despite vocabulary failure: %v", got)
		}
		if added := env.newTagsSince(t, before); len(added) != 0 {
			t.Fatalf("KCs written despite vocabulary failure: %v", added)
		}
		// 内部细节只进服务端日志。
		if !strings.Contains(logs.String(), "measures: loading subjects vocabulary") {
			t.Errorf("expected the raw cause in the server log, got: %s", logs.String())
		}
	})

	t.Run("auto_path", func(t *testing.T) {
		env, f := setupPaperTest(t)
		env.createAdmin(t)
		env.setSetting(t, "ai_auto_paper_analysis", "true")
		before := env.tagSnapshot(t)

		// 包一层真 fake 的 HTTP handler（不改 error_items_test.go）：第一轮
		// 判读请求到达时就地在真 DB 上把 subjects 表改名。判读发生在分析
		// 中途、自动 distill 之前，所以之后的 ListSubjects 必失败，而分析
		// 的其他步骤（分段/判读/记错题）不查词表。
		var renameErr error
		var renameMu sync.Mutex
		hideOnce := sync.OnceFunc(func() {
			_, err := env.db.Exec(`ALTER TABLE subjects RENAME TO subjects_def_subject_r1`)
			renameMu.Lock()
			renameErr = err
			renameMu.Unlock()
		})
		orig := f.server.Config.Handler
		f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/chat/completions" {
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), "diagnosing ONE question") {
					hideOnce()
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			orig.ServeHTTP(w, r)
		})

		kidID, _ := completePhotoChore(t, env)
		env.chores.WaitForAnalyses()

		renameMu.Lock()
		err := renameErr
		renameMu.Unlock()
		if err != nil {
			t.Fatalf("hiding the subjects table: %v", err)
		}
		var analyses []analysisResp
		resp := env.expectStatus(t, "GET", fmt.Sprintf("/api/admin/users/%d/analyses", kidID), nil, adminHeaders(), http.StatusOK)
		decodeBody(t, resp, &analyses)
		if len(analyses) != 1 || analyses[0].Status != "done" {
			t.Fatalf("auto analysis did not finish: %+v", analyses)
		}
		// 自动路径 distill 失败是 best-effort：分析保留，措施/KC 零写入。
		if got := env.measureTitles(t, kidID); len(got) != 0 {
			t.Fatalf("auto distill wrote measures on vocabulary failure: %v", got)
		}
		if added := env.newTagsSince(t, before); len(added) != 0 {
			t.Fatalf("auto distill wrote KCs on vocabulary failure: %v", added)
		}
	})
}

// ─── 5a) 重复非法 subject：同一批只加载一次词表，结果不随重复漂移 ───

func TestDistillMeasuresSubjectRepeatedInvalidNoSnapshotDrift(t *testing.T) {
	env, f := setupPaperTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	env.createScheduledChore(t, "Make bed", kidID, nil)
	analysisID := env.insertAnalysis(t, kidID, "math")
	before := env.tagSnapshot(t)
	env.mustExec(t, `UPDATE subjects SET enabled = 0 WHERE key = 'physics'`)

	f.setMeasures(
		measureDraft("gym", "KC-a", "非法-rep-1"),
		measureDraft("gym", "KC-b", "非法-rep-2"),
		measureDraft("math", "KC-m", "合法-mid"),
		measureDraft("gym", "KC-c", "非法-rep-3"),
		measureDraft("physics", "KC-d", "非法-physics"),
	)

	code, body := env.distilHTTP(t, analysisID)
	if code != http.StatusOK {
		t.Fatalf("distill = %d %s", code, body)
	}
	if got := env.measureTitles(t, kidID); strings.Join(got, "|") != "合法-mid" {
		t.Fatalf("measures = %v, want only 合法-mid", got)
	}
	// 同批快照：重复非法项各日志一次、且都是"从同一次读取拒绝"。
	// 可测断言：重复非法 subject 没有因为第一批被拒而在第二批被接受
	// （同快照下结果逐条一致，不随批次位置漂移）。
	want := map[string]bool{"math\x00KC-m": true}
	if have := env.newTagsSince(t, before); fmt.Sprint(have) != fmt.Sprint(want) {
		t.Fatalf("new knowledge_tags = %v, want %v", have, want)
	}

	// 再跑一次（幂等）结果相同：非法项仍然全部被拒。
	if code, body := env.distilHTTP(t, analysisID); code != http.StatusOK {
		t.Fatalf("second distill = %d %s", code, body)
	}
	if got := env.measureTitles(t, kidID); len(got) != 2 {
		t.Fatalf("second distill = %v, want exactly 2 legal rows", got)
	}
}

// ─── 5b) 不可信长 subject：日志被截断，不夹带 draft 隐私文本 ───

func TestDistillMeasuresSubjectUntrustedLogTruncated(t *testing.T) {
	env, f := setupPaperTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	env.createScheduledChore(t, "Make bed", kidID, nil)
	analysisID := env.insertAnalysis(t, kidID, "math")

	const secret = "PRIVATE-KID-NOTE-9b1d"
	longSubject := "🏫" + strings.Repeat("S", 300) // 301 个 rune
	f.setMeasures(
		measureDraftJSON{
			Subject: longSubject, KC: "私密KC-" + secret, Title: "私密标题-" + secret,
			Detail: "私密细节-" + secret, Priority: 1,
		},
		measureDraft("math", "", "合法尾项"),
	)

	logs := captureLogs(t)
	code, body := env.distilHTTP(t, analysisID)
	if code != http.StatusOK {
		t.Fatalf("distill = %d %s", code, body)
	}
	if got := env.measureTitles(t, kidID); strings.Join(got, "|") != "合法尾项" {
		t.Fatalf("measures = %v", got)
	}
	out := logs.String()
	if !strings.Contains(out, "skipping distilled draft with unknown or disabled subject") {
		t.Fatalf("missing skip log: %s", out)
	}
	if !strings.Contains(out, "…") {
		t.Errorf("long subject not truncated (no ellipsis): %s", out)
	}
	if strings.Contains(out, strings.Repeat("S", 65)) {
		t.Errorf("log carries more than 64 runes of the subject")
	}
	if strings.Contains(out, secret) {
		t.Errorf("log leaked draft privacy text: %s", out)
	}
	if !json.Valid([]byte(body)) {
		t.Errorf("response body is not JSON: %q", body)
	}
}

// ─── 6) 手动 distill 走 analysis.Subject 之外的 draft subject 守卫 ───

// analysis.Subject 不再决定 draft 学科：draft 自带 subject，必须逐条过关。
func TestDistillMeasuresSubjectUsesDraftNotAnalysisSubject(t *testing.T) {
	env, f := setupPaperTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	env.createScheduledChore(t, "Make bed", kidID, nil)
	analysisID := env.insertAnalysis(t, kidID, "") // 分析本身无学科

	f.setMeasures(
		measureDraft("gym", "", "draft-未知"),
		measureDraft("math", "", "draft-math"),
	)
	code, body := env.distilHTTP(t, analysisID)
	if code != http.StatusOK {
		t.Fatalf("distill = %d %s", code, body)
	}
	if got := env.measureTitles(t, kidID); strings.Join(got, "|") != "draft-math" {
		t.Fatalf("measures = %v, want only draft-math", got)
	}

	// 反向：analysis.Subject=math 也不能给空 subject draft 或非法 draft 背书。
	env2, f2 := setupPaperTest(t)
	env2.createAdmin(t)
	kid2 := env2.createChild(t, "Kid2")
	env2.createScheduledChore(t, "Make bed", kid2, nil)
	a2 := env2.insertAnalysis(t, kid2, "math")
	f2.setMeasures(
		measureDraft("gym", "", "draft-未知-2"),
		measureDraft("", "", "draft-空"),
	)
	if code, body := env2.distilHTTP(t, a2); code != http.StatusOK {
		t.Fatalf("distill 2 = %d %s", code, body)
	}
	if got := env2.measureTitles(t, kid2); strings.Join(got, "|") != "draft-空" {
		t.Fatalf("measures = %v, want only the empty-subject draft", got)
	}
}
