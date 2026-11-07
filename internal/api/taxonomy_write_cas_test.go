package api_test

// DEF-DIAG-WRITE-CAS-API-TESTER: Answer / Undo / Finish 三条写路径的 CAS
// 与副作用时序。真实 httptest + 真实 SQLite（临时文件 DB，两个连接），复用
// taxonomy_test.go / api_test.go 的既有 helper，不改公共 fixture。
//
// 并发构造：casConnector 包装 driver.Connector，在每个 Conn.BeginTx 上做一次
// rendezvous。两个请求各自在事务外读完 session state（loadOwnedSession），
// 先到达 BeginTx 的请求等第二个到齐后才真正开事务；测试收到第一个响应后
// 放行第二个，于是两个请求持有同一个 expected state 进入 CAS —— 一个 200、
// 一个 409，不是 mock 出来的冲突。
//
// 确定性冲突构造：SQLite BEFORE trigger + RAISE(IGNORE) 让 session 的 CAS
// UPDATE 返回 0 行（真实 RowsAffected=0 → ErrDiagnosticStateConflict），
// RAISE(ABORT) 让 attempt INSERT 失败，用于验证 DAO 失败不产生任何副作用。

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	msqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/liftedkilt/openchore/internal/api"
	"github.com/liftedkilt/openchore/internal/store"
	"github.com/liftedkilt/openchore/internal/taxonomy"
	"github.com/liftedkilt/openchore/internal/webhook"
	"github.com/liftedkilt/openchore/migrations"
	"modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// 并发屏障 / 双连接 env
// ---------------------------------------------------------------------------

// casBarrier 让前两个 BeginTx 会合：n=0 等 n=1 到齐后先走完写事务，
// n=1 等 releaseLoser() 再进入 CAS。n>=2 或 armed=false 直接放行。
type casBarrier struct {
	armed   atomic.Bool
	mu      sync.Mutex
	arrived int
	first   chan struct{}
	ready   chan struct{}
	release chan struct{}
	once    sync.Once
	firstOn sync.Once
	readyOn sync.Once
}

func newCASBarrier() *casBarrier {
	return &casBarrier{
		first:   make(chan struct{}),
		ready:   make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *casBarrier) arm() { b.armed.Store(true) }

func (b *casBarrier) releaseLoser() {
	b.once.Do(func() { close(b.release) })
}

func (b *casBarrier) disarm() {
	b.armed.Store(false)
	b.releaseLoser()
}

func (b *casBarrier) atBeginTx() {
	if !b.armed.Load() {
		return
	}
	b.mu.Lock()
	n := b.arrived
	b.arrived++
	b.mu.Unlock()
	switch n {
	case 0:
		b.firstOn.Do(func() { close(b.first) })
		<-b.ready
	case 1:
		b.readyOn.Do(func() { close(b.ready) })
		<-b.release
	}
}

// casConn 只拦 BeginTx；其余接口原样委托给真实 modernc sqlite 连接。
type casConn struct {
	base driver.Conn
	gate *casBarrier
}

func (c *casConn) Prepare(q string) (driver.Stmt, error) { return c.base.Prepare(q) }
func (c *casConn) Close() error                          { return c.base.Close() }
func (c *casConn) Begin() (driver.Tx, error)             { return c.base.Begin() }

func (c *casConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.gate.atBeginTx()
	if b, ok := c.base.(driver.ConnBeginTx); ok {
		return b.BeginTx(ctx, opts)
	}
	return c.base.Begin()
}

func (c *casConn) CheckNamedValue(nv *driver.NamedValue) error {
	if ck, ok := c.base.(driver.NamedValueChecker); ok {
		return ck.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

type casConnector struct {
	base driver.Connector
	gate *casBarrier
}

func (c *casConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &casConn{base: conn, gate: c.gate}, nil
}

func (c *casConnector) Driver() driver.Driver { return c.base.Driver() }

type casTestEnv struct {
	*testEnv
	gate *casBarrier
}

// setupCASTest 复制 setupTest 的 wiring，但用文件 DB + 2 个连接，使两个
// httptest 请求能真实并发读到同一份 state_json。
func setupCASTest(t *testing.T) *casTestEnv {
	t.Helper()

	dsn := "file:" + filepath.Join(t.TempDir(), "cas.db") + "?_foreign_keys=on&_busy_timeout=5000"
	base, err := sqlite.NewConnector(dsn)
	if err != nil {
		t.Fatalf("cas connector: %v", err)
	}
	gate := newCASBarrier()
	db := sql.OpenDB(&casConnector{base: base, gate: gate})
	db.SetMaxOpenConns(2)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}

	driverInst, err := msqlite.WithInstance(db, &msqlite.Config{})
	if err != nil {
		t.Fatalf("migration driver: %v", err)
	}
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("migration source: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "sqlite", driverInst)
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrations: %v", err)
	}

	s := store.New(db)
	d := webhook.NewDispatcher(s)
	sessions := api.NewSessionManager(testSessionSecret)
	oidcSvc := api.NewOIDCService(s, sessions, d, nil)
	router, chores, reports, coach := api.NewRouter(s, d, api.Auth{Sessions: sessions, OIDC: oidcSvc})
	server := httptest.NewServer(router)
	t.Cleanup(func() {
		gate.releaseLoser()
		server.Close()
		db.Close()
	})

	return &casTestEnv{
		testEnv: &testEnv{server: server, db: db, store: s, chores: chores, reports: reports, coach: coach},
		gate:    gate,
	}
}

// ---------------------------------------------------------------------------
// 小 helper（本文件本地，不改公共 fixture）
// ---------------------------------------------------------------------------

type casResult struct {
	status int
	body   []byte
	err    error
}

// casRequest 不调用 t.Fatalf，可从 goroutine 直接使用。
func casRequest(e *testEnv, method, path string, body any, headers map[string]string) casResult {
	var reqBody io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.server.URL+path, reqBody)
	if err != nil {
		return casResult{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return casResult{err: err}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return casResult{status: resp.StatusCode, body: b}
}

func casWant(t *testing.T, label string, r casResult, want int) {
	t.Helper()
	if r.err != nil {
		t.Fatalf("%s: request failed: %v", label, r.err)
	}
	if r.status != want {
		t.Fatalf("%s: status = %d, want %d (body %s)", label, r.status, want, r.body)
	}
}

func casJSON(t *testing.T, label string, r casResult) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("%s: bad JSON %q: %v", label, r.body, err)
	}
	return out
}

func casCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

type casRow struct {
	state, status, summary, completedAt string
}

func casSessionRow(t *testing.T, db *sql.DB, id int64) casRow {
	t.Helper()
	var r casRow
	if err := db.QueryRow(`SELECT state_json, status, COALESCE(summary_json,''), COALESCE(completed_at,'')
		FROM diagnostic_sessions WHERE id = ?`, id).Scan(&r.state, &r.status, &r.summary, &r.completedAt); err != nil {
		t.Fatalf("session row %d: %v", id, err)
	}
	return r
}

func casFinishedOf(t *testing.T, raw string) bool {
	t.Helper()
	if raw == "" {
		return false
	}
	var s struct {
		Finished bool `json:"finished"`
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("summary %q is not JSON: %v", raw, err)
	}
	return s.Finished
}

func casAnswerCount(t *testing.T, stateJSON string) int {
	t.Helper()
	var s struct {
		Answers []any `json:"answers"`
	}
	if err := json.Unmarshal([]byte(stateJSON), &s); err != nil {
		t.Fatalf("state %q is not JSON: %v", stateJSON, err)
	}
	return len(s.Answers)
}

func casSessionID(t *testing.T, detail map[string]any) int64 {
	t.Helper()
	s, _ := detail["session"].(map[string]any)
	id, _ := s["id"].(float64)
	if id == 0 {
		t.Fatalf("no session id in detail: %v", detail)
	}
	return int64(id)
}

// casTwoPhase 让 winner 先到达 BeginTx、loser 随后到达；返回 winner/loser
// 的真实 HTTP 结果。winner 的请求完成后才放行 loser。
func casTwoPhase(t *testing.T, gate *casBarrier, e *testEnv, winner, loser func() casResult) (casResult, casResult) {
	t.Helper()
	gate.arm()
	defer gate.disarm()
	chW := make(chan casResult, 1)
	go func() { chW <- winner() }()
	select {
	case <-gate.first:
	case <-timeAfterCAS():
		gate.disarm()
		t.Fatal("winner never reached BeginTx (barrier timeout)")
	}
	chL := make(chan casResult, 1)
	go func() { chL <- loser() }()
	w := <-chW
	gate.releaseLoser()
	l := <-chL
	return w, l
}

// timeAfterCAS 防止 barrier 意外挂死（长任务不贴全量日志，只报超时）。
func timeAfterCAS() <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		// 15s 足够 13MB pack 加载 + 两次 httptest 往返
		time.Sleep(15 * time.Second)
		close(ch)
	}()
	return ch
}

// casNewSession 是三个并发用例的公共 setup：admin + kid + pack + 一个
// 未完成的 screen 会话。setupAnswers 为进入并发前已落库的作答次数。
func casNewSession(t *testing.T, e *testEnv, limit int, setupAnswers int) (int64, int, casRow, string) {
	t.Helper()
	e.createAdmin(t)
	kid := e.createChild(t, "小CAS")
	importPack(t, e)
	detail := createDiagnosticSession(t, e, childHeaders(kid), map[string]any{
		"mode":    "screen",
		"subject": "math",
		"targets": []string{"cnm_p001", "cnm_p002"},
		"limit":   limit,
		"confirm": 2,
		"seed":    "cas-seed",
	})
	id := casSessionID(t, detail)
	for i := 0; i < setupAnswers; i++ {
		answerNext(t, e, childHeaders(kid), id, casCurrentWrongAnswer(t, e, id))
	}
	return id, kid, casSessionRow(t, e.db, id), casCurrentJudgedAnswer(t, e, id)
}

var (
	casPackOnce    sync.Once
	casPackCatalog *taxonomy.Catalog
	casPackErr     error
)

// casCatalog 只加载一次内嵌题库（12MB pack），避免并发用例重复解析。
func casCatalog(t *testing.T) *taxonomy.Catalog {
	t.Helper()
	casPackOnce.Do(func() { casPackCatalog, casPackErr = taxonomy.Load() })
	if casPackErr != nil {
		t.Fatalf("load embedded taxonomy pack: %v", casPackErr)
	}
	return casPackCatalog
}

// casCurrentQuestion 用生产引擎重放库里的 state_json，再调用同一条
// NextItem 出题路径拿到当前待答题（含 answer/accept）。生产 GET detail
// 对所有角色都脱敏，因此不能从 HTTP 视图取答案。
func casCurrentQuestion(t *testing.T, e *testEnv, id int64) *taxonomy.Session {
	t.Helper()
	var stateJSON string
	if err := e.db.QueryRow(`SELECT state_json FROM diagnostic_sessions WHERE id = ?`, id).Scan(&stateJSON); err != nil {
		t.Fatalf("state of session %d: %v", id, err)
	}
	var st taxonomy.Session
	if err := json.Unmarshal([]byte(stateJSON), &st); err != nil {
		t.Fatalf("decode state of session %d: %v (raw %s)", id, err, stateJSON)
	}
	return &st
}

// casAdminQuestion 名字保留（其余用例仍按此调用）：重放 state 得到当前
// 题目，答案来自内嵌题库（生产判题用的同一份数据）。
func casAdminQuestion(t *testing.T, e *testEnv, id int64) *taxonomy.Question {
	t.Helper()
	st := casCurrentQuestion(t, e, id)
	item := casCatalog(t).NextItem(st)
	if item == nil || item.Question == nil {
		t.Fatalf("session %d has no next question (asked=%d limit=%d stack=%v status=%v)",
			id, len(st.Answers), st.Limit, st.Stack, st.Status)
	}
	return item.Question
}

// casCurrentJudgedAnswer 返回当前题目确定能触发 learner 副作用的答案：
// 优先复用题库的正确答案（judged correct），choice 再试有效错误选项
// （judged wrong）。"999" 对 choice 是 manual，不能当通用错答。
func casCurrentJudgedAnswer(t *testing.T, e *testEnv, id int64) string {
	t.Helper()
	q := casAdminQuestion(t, e, id)
	candidates := []string{q.Answer}
	for _, c := range q.Choices {
		if c.Key != "" {
			candidates = append(candidates, c.Key)
		}
	}
	candidates = append(candidates, q.Accept...)
	for _, ans := range candidates {
		if ans == "" {
			continue
		}
		if v := taxonomy.JudgeQuestion(q, ans).Verdict; v == taxonomy.VerdictCorrect || v == taxonomy.VerdictWrong {
			return ans
		}
	}
	// numeric 题的参考答案可能依赖 verify oracle；用一个与答案不同的
	// 数字让 JudgeAnswer/verifyOracle 给出确定 wrong。
	if n, err := strconv.Atoi(q.Answer); err == nil {
		ans := strconv.Itoa(n + 1000)
		if v := taxonomy.JudgeQuestion(q, ans).Verdict; v == taxonomy.VerdictCorrect || v == taxonomy.VerdictWrong {
			return ans
		}
	}
	t.Fatalf("no deterministic judged answer for %q type=%q answer=%q", q.ID, q.Type, q.Answer)
	return ""
}

// casCurrentWrongAnswer 返回当前题目的确定性 judged wrong 答案。
// self/short 无法保证判 wrong 时立即失败，避免夹具把 manual/skip
// 误当成已产生 learner 副作用。
func casCurrentWrongAnswer(t *testing.T, e *testEnv, id int64) string {
	t.Helper()
	q := casAdminQuestion(t, e, id)
	for _, c := range q.Choices {
		if c.Key != "" && taxonomy.JudgeQuestion(q, c.Key).Verdict == taxonomy.VerdictWrong {
			return c.Key
		}
	}
	for _, ans := range []string{"998", "999", "1000"} {
		if taxonomy.JudgeQuestion(q, ans).Verdict == taxonomy.VerdictWrong {
			return ans
		}
	}
	t.Fatalf("no deterministic wrong answer for %q type=%q answer=%q", q.ID, q.Type, q.Answer)
	return ""
}

// casExpectedMastery 从已落库的 answered events 反推应有多少个 KC
// rollup：每个事件 context.kc_ids 里的不同 tag 各一行。这样并发用例
// 不必猜第二题是同知识点还是先修知识点。
func casExpectedMastery(t *testing.T, e *testEnv, kid int) int {
	t.Helper()
	return casCount(t, e.db,
		`SELECT COUNT(DISTINCT je.value) FROM learner_events le, json_each(le.context_json, '$.kc_ids') je
		 WHERE le.user_id = ? AND le.verb = 'answered'`, kid)
}

func casAskedOf(t *testing.T, raw string) int {
	t.Helper()
	if raw == "" {
		return 0
	}
	var s struct {
		Asked int `json:"asked"`
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("summary %q is not JSON: %v", raw, err)
	}
	return s.Asked
}

// casAssertSideEffects 检查成功 answer 应产生的 learner_events /
// kc_mastery 行数，以及 review_items（finished hook）。mastery 期望
// 由 events 的 kc_ids 推导，而不是写死题序。
func casAssertSideEffects(t *testing.T, e *testEnv, kid, events, reviews int) {
	t.Helper()
	if got := casCount(t, e.db, `SELECT COUNT(*) FROM learner_events WHERE user_id = ? AND verb = 'answered'`, kid); got != events {
		t.Errorf("learner_events = %d, want %d", got, events)
	}
	if got := casCount(t, e.db, `SELECT COUNT(*) FROM kc_mastery WHERE user_id = ?`, kid); got != casExpectedMastery(t, e, kid) {
		t.Errorf("kc_mastery = %d, want %d (distinct kc_ids from events)", got, casExpectedMastery(t, e, kid))
	}
	if got := casCount(t, e.db, `SELECT COUNT(*) FROM review_items WHERE user_id = ? AND source_type = 'kc'`, kid); got != reviews {
		t.Errorf("review_items(kc) = %d, want %d", got, reviews)
	}
}

// ---------------------------------------------------------------------------
// 1) 并发同 expected state：只有一个成功，另一个 409，attempts/state 不半写
// ---------------------------------------------------------------------------

func TestTaxonomyAnswerCASConcurrentSameExpectedOneWins(t *testing.T) {
	ce := setupCASTest(t)
	e := ce.testEnv
	id, kid, before, answer := casNewSession(t, e, 4, 0)
	path := fmt.Sprintf("/api/taxonomy/diagnostics/%d/answers", id)

	// 两个请求都拿同一道题的 judged answer：赢家应记录一次 learner
	// event，输家 409 后不得记录；"999" 对 choice 题是 manual，不能
	// 用来验证冲突方的副作用隔离。
	w, l := casTwoPhase(t, ce.gate, e,
		func() casResult {
			return casRequest(e, "POST", path, map[string]any{"answer": answer}, childHeaders(kid))
		},
		func() casResult {
			return casRequest(e, "POST", path, map[string]any{"answer": answer}, childHeaders(kid))
		},
	)

	// 顺序确定：先到 BeginTx 的是赢家；无论哪个 goroutine 先发起，
	// 只有一个 200、一个 409。
	casWant(t, "winner answer", w, http.StatusOK)
	casWant(t, "loser answer", l, http.StatusConflict)
	if errMsg, _ := casJSON(t, "loser", l)["error"].(string); errMsg == "" {
		t.Errorf("409 body has no error message: %s", l.body)
	}
	if casAnswerCount(t, before.state) != 0 {
		t.Fatalf("setup: state already has answers: %s", before.state)
	}

	// DB 只有赢家的一次写入：一行 attempt、一份新 state，无半写。
	if got := casCount(t, e.db, `SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ?`, id); got != 1 {
		t.Errorf("attempts = %d, want 1 (loser must insert nothing)", got)
	}
	if got := casCount(t, e.db, `SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ? AND status = 'answered'`, id); got != 1 {
		t.Errorf("answered attempts = %d, want 1", got)
	}
	after := casSessionRow(t, e.db, id)
	if after.state == before.state {
		t.Errorf("winner state swap did not land")
	}
	if got := casAnswerCount(t, after.state); got != 1 {
		t.Errorf("state answers = %d, want 1 (no half-write / double apply)", got)
	}
	if after.status != "active" || after.completedAt != "" {
		t.Errorf("status/completed_at = %q/%q, want active/empty", after.status, after.completedAt)
	}
	if casFinishedOf(t, after.summary) {
		t.Errorf("unfinished session summary.finished = true")
	}
	// 只有 commit 成功的那次产生副作用。
	casAssertSideEffects(t, e, kid, 1, 0)

	// 赢家响应与 DB 同源。
	wd := casJSON(t, "winner", w)
	ws, _ := wd["session"].(map[string]any)
	if ws["status"] != after.status {
		t.Errorf("winner session.status = %v, DB = %q", ws["status"], after.status)
	}
}

// ---------------------------------------------------------------------------
// 2) Undo 与 Answer 冲突：loser 的 void overlay / state replay 全部回滚
// ---------------------------------------------------------------------------

func TestTaxonomyAnswerCASUndoConflictRollsBackVoidAndState(t *testing.T) {
	ce := setupCASTest(t)
	e := ce.testEnv
	id, kid, before, answer := casNewSession(t, e, 4, 1) // 先有一行可 void 的 answered
	if got := casCount(t, e.db, `SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ? AND status = 'answered'`, id); got != 1 {
		t.Fatalf("setup: answered attempts = %d, want 1", got)
	}

	answerPath := fmt.Sprintf("/api/taxonomy/diagnostics/%d/answers", id)
	undoPath := fmt.Sprintf("/api/taxonomy/diagnostics/%d/undo", id)
	w, l := casTwoPhase(t, ce.gate, e,
		func() casResult {
			return casRequest(e, "POST", answerPath, map[string]any{"answer": answer}, childHeaders(kid))
		},
		func() casResult {
			return casRequest(e, "POST", undoPath, map[string]any{"count": 1}, childHeaders(kid))
		},
	)
	casWant(t, "answer (winner)", w, http.StatusOK)
	casWant(t, "undo (loser)", l, http.StatusConflict)

	// Undo 的 void overlay 未落：原本那行仍是 answered，没有任何 voided。
	if got := casCount(t, e.db, `SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ? AND status = 'voided'`, id); got != 0 {
		t.Errorf("voided attempts after losing undo = %d, want 0", got)
	}
	if got := casCount(t, e.db, `SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ?`, id); got != 2 {
		t.Errorf("attempts = %d, want 2 (setup + winner only)", got)
	}
	// Undo 的 state replay 未落：state 是赢家答完后的 2 次作答，不是回退到 1 次。
	after := casSessionRow(t, e.db, id)
	if got := casAnswerCount(t, after.state); got != 2 {
		t.Errorf("state answers = %d, want 2 (undo replay rolled back)", got)
	}
	if after.state == before.state {
		t.Errorf("state did not advance with the winning answer")
	}
	if after.status != "active" || after.completedAt != "" {
		t.Errorf("status/completed_at = %q/%q, want active/empty", after.status, after.completedAt)
	}
	// 冲突请求不产生 learner 副作用；成功 answer 只加一次。
	casAssertSideEffects(t, e, kid, 2, 0)
}

// ---------------------------------------------------------------------------
// 3) Finish 冲突：不改 completed_at/status，不触发 onDiagnosticFinished
// ---------------------------------------------------------------------------

func TestTaxonomyAnswerCASFinishConflictNoSideEffects(t *testing.T) {
	ce := setupCASTest(t)
	e := ce.testEnv
	id, kid, before, answer := casNewSession(t, e, 4, 1) // 一次 judged wrong，summary 带 weak/未确认项
	if before.completedAt != "" || before.status == "completed" {
		t.Fatalf("setup: session already completed")
	}

	answerPath := fmt.Sprintf("/api/taxonomy/diagnostics/%d/answers", id)
	finishPath := fmt.Sprintf("/api/taxonomy/diagnostics/%d/finish", id)
	w, l := casTwoPhase(t, ce.gate, e,
		func() casResult {
			return casRequest(e, "POST", answerPath, map[string]any{"answer": answer}, childHeaders(kid))
		},
		func() casResult { return casRequest(e, "POST", finishPath, nil, childHeaders(kid)) },
	)
	casWant(t, "answer (winner)", w, http.StatusOK)
	casWant(t, "finish (loser)", l, http.StatusConflict)

	after := casSessionRow(t, e.db, id)
	if after.status != "active" {
		t.Errorf("status = %q after losing finish, want active", after.status)
	}
	if after.completedAt != "" {
		t.Errorf("completed_at = %q after losing finish, want empty", after.completedAt)
	}
	// 冻结契约：赢家 answer 同事务推进 state/summary；输家 finish
	// 不得覆盖赢家，也不能留下自己算出的 finished=true summary。
	if after.state == before.state || casAnswerCount(t, after.state) != 2 {
		t.Errorf("winner answer did not advance state: before %q after %q", before.state, after.state)
	}
	if casAskedOf(t, after.summary) != casAnswerCount(t, after.state) {
		t.Errorf("summary asked=%d, want %d (same transaction/state source)", casAskedOf(t, after.summary), casAnswerCount(t, after.state))
	}
	if casFinishedOf(t, after.summary) {
		t.Errorf("losing finish left summary.finished = true")
	}
	if casFinishedOf(t, before.summary) {
		t.Errorf("setup summary unexpectedly finished")
	}
	// finish 的 onDiagnosticFinished 没跑：没有任何 KC review 卡；
	// 只保留 setup + winner 两次成功 judged answer 的 learner 副作用。
	casAssertSideEffects(t, e, kid, 2, 0)

	// 400ms 后 fresh GET 也不该变成 completed（无延迟副作用）。
	resp := e.expectStatus(t, "GET", fmt.Sprintf("/api/taxonomy/diagnostics/%d", id), nil, childHeaders(kid), http.StatusOK)
	var again map[string]any
	decodeBody(t, resp, &again)
	s, _ := again["session"].(map[string]any)
	if s["status"] != "active" {
		t.Errorf("re-read status = %v, want active", s["status"])
	}
}

// ---------------------------------------------------------------------------
// 4) CAS 409 / DAO 失败：commit 之前不得有 learner_events/mastery/review
// ---------------------------------------------------------------------------

func TestTaxonomyAnswerCASConflictNoLearnerSideEffects(t *testing.T) {
	e := setupTest(t)
	id, kid, before, answer := casNewSession(t, e, 4, 0)
	_ = before

	// 真实 SQLite：BEFORE UPDATE + RAISE(IGNORE) 让 CAS UPDATE 影响 0 行，
	// 触发 ErrDiagnosticStateConflict → 409，事务整体回滚。
	trigger := fmt.Sprintf(`CREATE TRIGGER oc_cas_ignore_%d BEFORE UPDATE ON diagnostic_sessions
		WHEN NEW.id = %d BEGIN SELECT RAISE(IGNORE); END`, id, id)
	if _, err := e.db.Exec(trigger); err != nil {
		t.Fatalf("install CAS-ignore trigger: %v", err)
	}
	t.Cleanup(func() { e.db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS oc_cas_ignore_%d`, id)) })

	path := fmt.Sprintf("/api/taxonomy/diagnostics/%d/answers", id)
	answer = casCurrentJudgedAnswer(t, e, id)
	resp := e.expectStatus(t, "POST", path, map[string]any{"answer": answer}, childHeaders(kid), http.StatusConflict)
	var out map[string]any
	decodeBody(t, resp, &out)
	if msg, _ := out["error"].(string); msg == "" {
		t.Errorf("409 body has no error message: %v", out)
	}

	after := casSessionRow(t, e.db, id)
	if after != before {
		t.Errorf("conflict changed the session row:\n before %+v\n after  %+v", before, after)
	}
	if got := casCount(t, e.db, `SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ?`, id); got != 0 {
		t.Errorf("attempts = %d after conflict, want 0", got)
	}
	casAssertSideEffects(t, e, kid, 0, 0)
}

func TestTaxonomyAnswerCASDAOFailureRollsBack(t *testing.T) {
	e := setupTest(t)
	id, kid, before, answer := casNewSession(t, e, 4, 0)
	_ = before
	_ = answer

	// 真实 SQLite：CAS UPDATE 先成功，attempt INSERT 被 ABORT 打断 →
	// DAO 返回普通错误（500），整个事务必须回滚（state 也不能半写）。
	trigger := fmt.Sprintf(`CREATE TRIGGER oc_cas_abort_%d BEFORE INSERT ON diagnostic_attempts
		WHEN NEW.session_id = %d BEGIN SELECT RAISE(ABORT, 'injected DAO failure'); END`, id, id)
	if _, err := e.db.Exec(trigger); err != nil {
		t.Fatalf("install insert-abort trigger: %v", err)
	}
	t.Cleanup(func() { e.db.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS oc_cas_abort_%d`, id)) })

	path := fmt.Sprintf("/api/taxonomy/diagnostics/%d/answers", id)
	e.expectStatus(t, "POST", path, map[string]any{"answer": "999"}, childHeaders(kid), http.StatusInternalServerError)

	after := casSessionRow(t, e.db, id)
	if after.state != before.state {
		t.Errorf("state swap survived the failed INSERT:\n before %s\n after  %s", before.state, after.state)
	}
	if after != before {
		t.Errorf("DAO failure changed the session row:\n before %+v\n after  %+v", before, after)
	}
	if got := casCount(t, e.db, `SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ?`, id); got != 0 {
		t.Errorf("attempts = %d after DAO failure, want 0", got)
	}
	casAssertSideEffects(t, e, kid, 0, 0)
}

// ---------------------------------------------------------------------------
// 5) completed / undo / finish：summary.finished 与 completed_at 同源
// ---------------------------------------------------------------------------

// casAssertFinishedInvariant 同时检查写响应/fresh GET 的 payload 与 DB 行：
// finished 位、status、completed_at 三者必须一致（都来自同一 session row），
// 不依赖 store 内部 COALESCE / summary 替换的具体写法。
func casAssertFinishedInvariant(t *testing.T, db *sql.DB, label string, payload map[string]any, want bool) {
	t.Helper()
	sess, _ := payload["session"].(map[string]any)
	if sess == nil {
		t.Fatalf("%s: no session block: %v", label, payload)
	}
	status, _ := sess["status"].(string)
	ca, _ := sess["completed_at"].(string)
	raw, _ := json.Marshal(payload["summary"])
	var sum struct {
		Finished bool `json:"finished"`
	}
	if err := json.Unmarshal(raw, &sum); err != nil {
		t.Fatalf("%s: summary is not an object: %v (%v)", label, err, payload["summary"])
	}
	id, _ := sess["id"].(float64)
	row := casSessionRow(t, db, int64(id))

	if (status == "completed") != want {
		t.Errorf("%s: session.status = %q, want completed=%v", label, status, want)
	}
	if (ca != "") != want {
		t.Errorf("%s: payload completed_at = %q, want nonempty=%v", label, ca, want)
	}
	if (row.completedAt != "") != want {
		t.Errorf("%s: DB completed_at = %q, want nonempty=%v", label, row.completedAt, want)
	}
	if (row.status == "completed") != want {
		t.Errorf("%s: DB status = %q, want completed=%v", label, row.status, want)
	}
	if sum.Finished != want {
		t.Errorf("%s: payload summary.finished = %v, want %v", label, sum.Finished, want)
	}
	if got := casFinishedOf(t, row.summary); got != want {
		t.Errorf("%s: DB summary.finished = %v, want %v (summary %q)", label, got, want, row.summary)
	}
}

func casGETPayload(t *testing.T, e *testEnv, id int64, kid int) map[string]any {
	t.Helper()
	resp := e.expectStatus(t, "GET", fmt.Sprintf("/api/taxonomy/diagnostics/%d", id), nil, childHeaders(kid), http.StatusOK)
	var out map[string]any
	decodeBody(t, resp, &out)
	return out
}

// casDriveToWeak 反复提交确定性 judged wrong，直到 fresh GET 的
// summary.weak 非空；返回 weak KC 列表。
func casDriveToWeak(t *testing.T, e *testEnv, kid int, id int64, maxAnswers int) []string {
	t.Helper()
	for i := 0; i < maxAnswers; i++ {
		if weak := casWeakOf(casGETPayload(t, e, id, kid)); len(weak) > 0 {
			return weak
		}
		out := answerNext(t, e, childHeaders(kid), id, casCurrentWrongAnswer(t, e, id))
		if q, _ := out["question"].(map[string]any); q == nil {
			t.Fatalf("answer %d did not yield a next question but summary.weak is still empty: %v", i+1, out)
		}
	}
	return casWeakOf(casGETPayload(t, e, id, kid))
}

func casWeakOf(payload map[string]any) []string {
	sum, _ := payload["summary"].(map[string]any)
	raw, _ := sum["weak"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if sv, ok := v.(string); ok && sv != "" {
			out = append(out, sv)
		}
	}
	return out
}

func TestTaxonomyCASFinishedSummaryAndCompletedAtSameSource(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小同源")
	importPack(t, e)

	newSession := func(limit int) (int64, map[string]any) {
		t.Helper()
		d := createDiagnosticSession(t, e, childHeaders(kid), map[string]any{
			"mode": "screen", "subject": "math",
			"targets": []string{"cnm_p001", "cnm_p002"},
			"limit":   limit, "confirm": 2, "seed": "same-source-seed",
		})
		return casSessionID(t, d), d
	}

	t.Run("active-unfinished", func(t *testing.T) {
		id, _ := newSession(4)
		detail := answerNext(t, e, childHeaders(kid), id, "999")
		casAssertFinishedInvariant(t, e.db, "answer response", detail, false)
		casAssertFinishedInvariant(t, e.db, "fresh GET", casGETPayload(t, e, id, kid), false)
	})

	t.Run("answer-completes", func(t *testing.T) {
		id, _ := newSession(1) // 一次作答即达到 limit → finished
		detail := answerNext(t, e, childHeaders(kid), id, "4")
		casAssertFinishedInvariant(t, e.db, "finish-by-answer response", detail, true)
		casAssertFinishedInvariant(t, e.db, "fresh GET", casGETPayload(t, e, id, kid), true)

		stamp := casSessionRow(t, e.db, id).completedAt
		if stamp == "" {
			t.Fatalf("completed_at empty after answer-completed")
		}
		// completed 会话上的 undo 保持终态：completed_at 不迁移。
		resp := e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/undo", id),
			map[string]any{"count": 1}, childHeaders(kid), http.StatusOK)
		var undone map[string]any
		decodeBody(t, resp, &undone)
		casAssertFinishedInvariant(t, e.db, "undo response", undone, true)
		casAssertFinishedInvariant(t, e.db, "fresh GET after undo", casGETPayload(t, e, id, kid), true)
		if got := casSessionRow(t, e.db, id).completedAt; got != stamp {
			t.Errorf("undo moved completed_at: %q → %q", stamp, got)
		}
	})

	t.Run("finish-forces", func(t *testing.T) {
		id, _ := newSession(4)
		// 冻结语义：manual/skip 不判错也不产生 weak；engine 首次 wrong 还会
		// 等确认题再下结论。因此循环提交真实判题器确认的 judged wrong，
		// 直到 fresh GET 的 summary.weak 非空（题量有限，最多 12 次）。
		weak := casDriveToWeak(t, e, kid, id, 12)
		if len(weak) == 0 {
			t.Fatalf("could not reach a weak KC before finish (summary.weak empty after 12 judged-wrong answers)")
		}
		resp := e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/finish", id),
			nil, childHeaders(kid), http.StatusOK)
		var finished map[string]any
		decodeBody(t, resp, &finished)
		casAssertFinishedInvariant(t, e.db, "finish response", finished, true)
		casAssertFinishedInvariant(t, e.db, "fresh GET", casGETPayload(t, e, id, kid), true)
		// onDiagnosticFinished 已跑：弱项 KC 有 review 卡。
		if got := casCount(t, e.db, `SELECT COUNT(*) FROM review_items WHERE user_id = ? AND source_type = 'kc'`, kid); got == 0 {
			t.Errorf("finish created no weak-KC review card")
		}

		stamp := casSessionRow(t, e.db, id).completedAt
		// 重复 finish：completed_at 保持第一次的戳，summary/status 不变。
		resp = e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/finish", id),
			nil, childHeaders(kid), http.StatusOK)
		var again map[string]any
		decodeBody(t, resp, &again)
		casAssertFinishedInvariant(t, e.db, "repeat finish response", again, true)
		if got := casSessionRow(t, e.db, id).completedAt; got != stamp {
			t.Errorf("repeat finish moved completed_at: %q → %q", stamp, got)
		}
	})
}
