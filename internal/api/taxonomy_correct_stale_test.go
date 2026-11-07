package api_test

// DEF-DIAG-CORRECT-STALE：reviewer 回归——admin Correct B（尾行 corrected）后
// Undo count=1 会把更早的 A 当作"最新 answered"作废，state 只回退到 A，
// 而 A 的 attempt 已被 voided（再 Correct/Undo 直接 404/矛盾）。冻结契约：
// undo 必须按 engine 实际回退的 answers 尾段（seq = 旧 len-1…）对应到
// attempt 行，corrected 行可以转 voided（answered 行的既有语义不变），
// 更早的 answered 行一律不动；数量对不上时整笔回滚（不猜替代行、不 500）。
//
// 全部走真实 httptest + 真实 SQLite；断言基于实际 attempt id/seq 与
// state_json，不只看 HTTP 200。

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/liftedkilt/openchore/internal/taxonomy"
)

type staleAttempt struct {
	id     int64
	seq    int
	status string
	qid    string
	voided sql.NullString
}

func staleAttemptRows(t *testing.T, e *testEnv, sessionID int64) []staleAttempt {
	t.Helper()
	rows, err := e.db.Query(`SELECT a.id, a.seq, a.status, COALESCE(q.external_ref, ''),
		COALESCE(a.voided_at, '') FROM diagnostic_attempts a
		LEFT JOIN knowledge_questions q ON q.id = a.question_id
		WHERE a.session_id = ? ORDER BY a.seq, a.id`, sessionID)
	if err != nil {
		t.Fatalf("list attempts of %d: %v", sessionID, err)
	}
	defer rows.Close()
	var out []staleAttempt
	for rows.Next() {
		var a staleAttempt
		if err := rows.Scan(&a.id, &a.seq, &a.status, &a.qid, &a.voided); err != nil {
			t.Fatalf("scan attempt: %v", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate attempts: %v", err)
	}
	return out
}

// staleSessionAnswers 读库里的 state_json（权威状态），不是 HTTP 视图。
func staleSessionAnswers(t *testing.T, e *testEnv, sessionID int64) []taxonomy.AnswerRecord {
	t.Helper()
	var raw string
	if err := e.db.QueryRow(`SELECT state_json FROM diagnostic_sessions WHERE id = ?`, sessionID).Scan(&raw); err != nil {
		t.Fatalf("state of %d: %v", sessionID, err)
	}
	var sess taxonomy.Session
	if err := json.Unmarshal([]byte(raw), &sess); err != nil {
		t.Fatalf("decode state of %d: %v (raw %s)", sessionID, err, raw)
	}
	return sess.Answers
}

func staleFindAttempt(t *testing.T, attempts []staleAttempt, seq int) staleAttempt {
	t.Helper()
	for _, a := range attempts {
		if a.seq == seq {
			return a
		}
	}
	t.Fatalf("no attempt with seq %d in %+v", seq, attempts)
	return staleAttempt{}
}

type staleEnv struct {
	e         *testEnv
	kid       int
	sessionID int64
}

// staleNewSession 建一个真实的 screen 会话并作答 n 次（"999" 对 choice 是
// manual、对 numeric 是 wrong —— 两种情况都只保证 attempt 行是 answered，
// 与本卡要测的回退配对无关）。
func staleNewSession(t *testing.T, answers int) *staleEnv {
	t.Helper()
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小回退")
	importPack(t, e)
	detail := createDiagnosticSession(t, e, childHeaders(kid), map[string]any{
		"mode":    "screen",
		"subject": "math",
		"targets": []string{"cnm_p001", "cnm_p002"},
		"limit":   4,
		"confirm": 2,
		"seed":    "correct-stale-seed",
	})
	sessionID := casSessionID(t, detail)
	for i := 0; i < answers; i++ {
		answerNext(t, e, childHeaders(kid), sessionID, "999")
	}
	return &staleEnv{e: e, kid: kid, sessionID: sessionID}
}

func (s *staleEnv) correct(t *testing.T, attemptID int64, verdict string, want int) {
	t.Helper()
	s.e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/correct", s.sessionID),
		map[string]any{"attempt_id": attemptID, "verdict": verdict}, adminHeaders(), want)
}

func (s *staleEnv) undo(t *testing.T, count int, want int) {
	t.Helper()
	s.e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/undo", s.sessionID),
		map[string]any{"count": count}, childHeaders(s.kid), want)
}

// TestTaxonomyCorrectTailThenUndoVoidsCorrectedNotEarlierAnswer：
// A、B 作答 → Correct B → Undo 1。engine 回退的是 B 那一条答案，所以 B 的
// corrected 行必须转 voided；A 必须仍是 answered 且可再 Correct、再 Undo。
// 绝不允许"作废 A、留 B corrected"或"找不到足够 answered 行 → 500"。
func TestTaxonomyCorrectTailThenUndoVoidsCorrectedNotEarlierAnswer(t *testing.T) {
	s := staleNewSession(t, 2)
	before := staleAttemptRows(t, s.e, s.sessionID)
	if len(before) != 2 || len(staleSessionAnswers(t, s.e, s.sessionID)) != 2 {
		t.Fatalf("setup: attempts=%+v answers=%d, want 2/2", before, len(staleSessionAnswers(t, s.e, s.sessionID)))
	}
	a, b := staleFindAttempt(t, before, 1), staleFindAttempt(t, before, 2)
	if a.status != "answered" || b.status != "answered" {
		t.Fatalf("setup statuses = %q/%q, want answered/answered", a.status, b.status)
	}

	// Admin 推翻 B：answered → corrected，state 里 B 的 verdict 被覆盖。
	s.correct(t, b.id, taxonomy.ResultCorrect, http.StatusOK)
	afterCorrect := staleAttemptRows(t, s.e, s.sessionID)
	if got := staleFindAttempt(t, afterCorrect, 2); got.status != "corrected" {
		t.Fatalf("attempt B status = %q, want corrected", got.status)
	}
	if got := staleFindAttempt(t, afterCorrect, 1); got.status != "answered" {
		t.Fatalf("attempt A status = %q, want answered after correcting B only", got.status)
	}

	// Undo 1：engine 丢掉最后一条答案（B 的），attempt 侧只有 B 该动。
	s.undo(t, 1, http.StatusOK)
	afterUndo := staleAttemptRows(t, s.e, s.sessionID)
	if got := staleFindAttempt(t, afterUndo, 2); got.status != "voided" || got.voided.String == "" {
		t.Errorf("undone corrected attempt B = %q voided_at=%q, want voided with stamp", got.status, got.voided.String)
	}
	if got := staleFindAttempt(t, afterUndo, 1); got.status != "answered" || got.voided.String != "" {
		t.Errorf("attempt A = %q voided_at=%q, want untouched answered (undo must not substitute an earlier row)", got.status, got.voided.String)
	}
	if n := len(afterUndo); n != 2 {
		t.Errorf("attempt rows = %d, want 2 (undo is a void overlay, never delete)", n)
	}
	if answers := staleSessionAnswers(t, s.e, s.sessionID); len(answers) != 1 {
		t.Fatalf("state answers = %d, want 1 (only A survives the undo)", len(answers))
	} else if answers[0].QID != a.qid {
		t.Errorf("state answer qid = %q, want A's question %q", answers[0].QID, a.qid)
	}

	// A 仍可 Correct：陈旧/替代作废会让这里 404 "no longer correctable"。
	s.correct(t, a.id, taxonomy.ResultWrong, http.StatusOK)
	if got := staleFindAttempt(t, staleAttemptRows(t, s.e, s.sessionID), 1); got.status != "corrected" {
		t.Fatalf("attempt A status = %q, want corrected", got.status)
	}
	// 已 voided 的 B 不可再 Correct（status 守卫不因 corrected→voided 放宽）。
	s.correct(t, b.id, taxonomy.ResultWrong, http.StatusNotFound)

	// 再次 Undo：回退 A，状态机归零；B 不会复活。
	s.undo(t, 1, http.StatusOK)
	final := staleAttemptRows(t, s.e, s.sessionID)
	if got := staleFindAttempt(t, final, 1); got.status != "voided" {
		t.Errorf("attempt A status = %q, want voided after second undo", got.status)
	}
	if got := staleFindAttempt(t, final, 2); got.status != "voided" {
		t.Errorf("attempt B was resurrected: status %q", got.status)
	}
	if answers := staleSessionAnswers(t, s.e, s.sessionID); len(answers) != 0 {
		t.Errorf("state answers = %d, want 0", len(answers))
	}
}

// TestTaxonomyConsecutiveCorrectedTailUndoUsesActualSeq：连续两行 corrected
// （B、C）后 Undo 2，必须按实际 seq 回退 B、C，A 仍 answered；undo 映射
// 不能"数 answered 行"或替换成别的行。
func TestTaxonomyConsecutiveCorrectedTailUndoUsesActualSeq(t *testing.T) {
	s := staleNewSession(t, 3)
	before := staleAttemptRows(t, s.e, s.sessionID)
	if len(before) != 3 || len(staleSessionAnswers(t, s.e, s.sessionID)) != 3 {
		t.Fatalf("setup: attempts=%+v answers=%d, want 3/3", before, len(staleSessionAnswers(t, s.e, s.sessionID)))
	}
	a, b, c := staleFindAttempt(t, before, 1), staleFindAttempt(t, before, 2), staleFindAttempt(t, before, 3)

	// 连续 correct 两条尾行。
	s.correct(t, b.id, taxonomy.ResultCorrect, http.StatusOK)
	s.correct(t, c.id, taxonomy.ResultSkip, http.StatusOK)
	afterCorrect := staleAttemptRows(t, s.e, s.sessionID)
	if got := staleFindAttempt(t, afterCorrect, 2); got.status != "corrected" {
		t.Fatalf("attempt B status = %q, want corrected", got.status)
	}
	if got := staleFindAttempt(t, afterCorrect, 3); got.status != "corrected" {
		t.Fatalf("attempt C status = %q, want corrected", got.status)
	}

	// Undo 2 → engine 丢掉 C、B 两条答案；attempt 侧必须按 seq 3、2 作废。
	s.undo(t, 2, http.StatusOK)
	afterUndo := staleAttemptRows(t, s.e, s.sessionID)
	if got := staleFindAttempt(t, afterUndo, 3); got.status != "voided" {
		t.Errorf("attempt C status = %q, want voided (seq 3)", got.status)
	}
	if got := staleFindAttempt(t, afterUndo, 2); got.status != "voided" {
		t.Errorf("attempt B status = %q, want voided (seq 2)", got.status)
	}
	if got := staleFindAttempt(t, afterUndo, 1); got.status != "answered" || got.voided.String != "" {
		t.Errorf("attempt A = %q voided_at=%q, want untouched answered", got.status, got.voided.String)
	}
	if n := len(afterUndo); n != 3 {
		t.Errorf("attempt rows = %d, want 3 (void overlay only)", n)
	}
	answers := staleSessionAnswers(t, s.e, s.sessionID)
	if len(answers) != 1 || answers[0].QID != a.qid {
		t.Fatalf("state answers = %+v, want only A's %q", answers, a.qid)
	}

	// 尾行已回退后 A 仍可 Correct/Undo；作废的 B、C 不可再 Correct。
	s.correct(t, a.id, taxonomy.ResultCorrect, http.StatusOK)
	s.correct(t, b.id, taxonomy.ResultCorrect, http.StatusNotFound)
	s.correct(t, c.id, taxonomy.ResultSkip, http.StatusNotFound)
	s.undo(t, 1, http.StatusOK)
	final := staleAttemptRows(t, s.e, s.sessionID)
	if got := staleFindAttempt(t, final, 1); got.status != "voided" {
		t.Errorf("attempt A status = %q, want voided after final undo", got.status)
	}
	if answers := staleSessionAnswers(t, s.e, s.sessionID); len(answers) != 0 {
		t.Errorf("state answers = %d, want 0", len(answers))
	}
}

// TestTaxonomyUndoCountBeyondAnswersNeverSubstitutes：计数超过 state 的作答数
// 时 handler 夹到实际条数，按真实 seq 逐条作废，不出现 500 "failed to void
// attempts"，也不拿 corrected/更早行当替代。
func TestTaxonomyUndoCountBeyondAnswersNeverSubstitutes(t *testing.T) {
	s := staleNewSession(t, 2)
	b := staleFindAttempt(t, staleAttemptRows(t, s.e, s.sessionID), 2)
	s.correct(t, b.id, taxonomy.ResultCorrect, http.StatusOK)

	s.undo(t, 5, http.StatusOK)
	rows := staleAttemptRows(t, s.e, s.sessionID)
	if got := staleFindAttempt(t, rows, 1); got.status != "voided" {
		t.Errorf("attempt A status = %q, want voided (count clamped to 2 answers)", got.status)
	}
	if got := staleFindAttempt(t, rows, 2); got.status != "voided" {
		t.Errorf("attempt B status = %q, want voided (count clamped to 2 answers)", got.status)
	}
	if answers := staleSessionAnswers(t, s.e, s.sessionID); len(answers) != 0 {
		t.Errorf("state answers = %d, want 0", len(answers))
	}
}
