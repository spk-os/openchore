package store_test

// Atomic diagnostic DAO tests (042): AppendDiagnosticAttempt and
// ReplayDiagnosticState must move the attempt row and the session state
// in one transaction guarded by the state_json compare-and-swap. Every
// case runs against the real migrated SQLite DB (single connection,
// MaxOpenConns=1 via setupStoreWithDB) so a deadlock would surface as a
// context timeout rather than a hang.

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

const (
	atomicState0     = `{"answers":[],"phase":"atomic-0"}`
	atomicState1     = `{"answers":[{"seq":1}],"phase":"atomic-1"}`
	atomicState2     = `{"answers":[{"seq":2}],"phase":"atomic-2"}`
	atomicStateRaceA = `{"answers":[{"seq":1}],"phase":"atomic-race-a"}`
	atomicStateRaceB = `{"answers":[{"seq":2}],"phase":"atomic-race-b"}`
	atomicStaleState = `{"answers":[],"phase":"atomic-stale"}`
	atomicSummary0   = `{"mastered":[],"weak":[]}`
	atomicSummary1   = `{"mastered":["kc-atomic-1"],"weak":[]}`
	atomicSummary2   = `{"mastered":["kc-atomic-2"],"weak":["kc-atomic-1"]}`
)

type atomicFixture struct {
	s           *store.Store
	db          *sql.DB
	userID      int64
	sessionID   int64
	questionIDs [2]int64
	tagIDs      [2]int64
	nextSeq     int
}

// newAtomicFixture builds one user, two knowledge questions and a single
// active diagnostic session in atomicState0 with no attempts yet.
func newAtomicFixture(t *testing.T) *atomicFixture {
	t.Helper()
	ctx := context.Background()
	s, db := setupStoreWithDB(t)
	u := createTestUser(t, s, "AtomicKid", "child")
	f := &atomicFixture{s: s, db: db, userID: u.ID, nextSeq: 1}

	names := [2]string{"乘法", "除法"}
	refs := [2]string{"kc-atomic-1", "kc-atomic-2"}
	for i := 0; i < 2; i++ {
		res, err := db.ExecContext(ctx,
			`INSERT INTO knowledge_tags (name, subject, external_ref, grade, source)
			 VALUES (?, 'math', ?, 2, 'test')`, names[i], refs[i])
		if err != nil {
			t.Fatalf("insert knowledge tag %d: %v", i, err)
		}
		f.tagIDs[i], err = res.LastInsertId()
		if err != nil {
			t.Fatalf("knowledge tag %d id: %v", i, err)
		}
		res, err = db.ExecContext(ctx,
			`INSERT INTO knowledge_questions (external_ref, topic_id, difficulty, type, prompt, answer_json, source)
			 VALUES (?, ?, 'basic', 'numeric', '?', '"1"', 'test')`, refs[i]+"-q", f.tagIDs[i])
		if err != nil {
			t.Fatalf("insert knowledge question %d: %v", i, err)
		}
		f.questionIDs[i], err = res.LastInsertId()
		if err != nil {
			t.Fatalf("knowledge question %d id: %v", i, err)
		}
	}

	d := &model.DiagnosticSession{
		UserID:      f.userID,
		Subject:     "math",
		Mode:        store.DiagnosticModeDiagnose,
		Status:      store.DiagnosticStatusActive,
		ScopeJSON:   `{"grade":2}`,
		StateJSON:   atomicState0,
		SummaryJSON: atomicSummary0,
	}
	if err := s.CreateDiagnosticSession(ctx, d); err != nil {
		t.Fatalf("CreateDiagnosticSession: %v", err)
	}
	f.sessionID = d.ID
	return f
}

func (f *atomicFixture) newSession(t *testing.T, state string) int64 {
	t.Helper()
	d := &model.DiagnosticSession{
		UserID:      f.userID,
		Subject:     "math",
		Mode:        store.DiagnosticModeConfirm,
		Status:      store.DiagnosticStatusActive,
		ScopeJSON:   `{"grade":2}`,
		StateJSON:   state,
		SummaryJSON: atomicSummary0,
	}
	if err := f.s.CreateDiagnosticSession(context.Background(), d); err != nil {
		t.Fatalf("CreateDiagnosticSession (extra): %v", err)
	}
	return d.ID
}

func (f *atomicFixture) answer(t *testing.T, sessionID int64, correct *bool, answer string) *model.DiagnosticAttempt {
	t.Helper()
	a := &model.DiagnosticAttempt{
		SessionID:  sessionID,
		Seq:        f.nextSeq,
		Role:       "probe",
		AnswerJSON: answer,
		Correct:    correct,
	}
	return a
}

func (f *atomicFixture) appendOK(t *testing.T, expected, newState, summary string, completed bool, correct *bool) *model.DiagnosticAttempt {
	t.Helper()
	a := f.answer(t, f.sessionID, correct, `"42"`)
	if err := f.s.AppendDiagnosticAttempt(context.Background(), f.sessionID, expected, a,
		f.questionIDs[0], f.tagIDs[0], newState, summary, completed); err != nil {
		t.Fatalf("AppendDiagnosticAttempt: %v", err)
	}
	f.nextSeq++
	return a
}

func (f *atomicFixture) loadSession(t *testing.T, id int64) *model.DiagnosticSession {
	t.Helper()
	d, err := f.s.GetDiagnosticSession(context.Background(), id)
	if err != nil || d == nil {
		t.Fatalf("GetDiagnosticSession(%d): %v %v", id, d, err)
	}
	return d
}

func (f *atomicFixture) requireSession(t *testing.T, id int64, state, summary, status string) *model.DiagnosticSession {
	t.Helper()
	d := f.loadSession(t, id)
	if d.StateJSON != state || d.SummaryJSON != summary || d.Status != status {
		t.Fatalf("session state=%q summary=%q status=%q, want %q/%q/%q",
			d.StateJSON, d.SummaryJSON, d.Status, state, summary, status)
	}
	return d
}

func (f *atomicFixture) attempts(t *testing.T, sessionID int64) []model.DiagnosticAttempt {
	t.Helper()
	rows, err := f.s.ListDiagnosticAttempts(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListDiagnosticAttempts(%d): %v", sessionID, err)
	}
	return rows
}

func (f *atomicFixture) countAttempts(t *testing.T, sessionID int64) int {
	t.Helper()
	return len(f.attempts(t, sessionID))
}

func (f *atomicFixture) attemptByID(t *testing.T, sessionID, id int64) model.DiagnosticAttempt {
	t.Helper()
	for _, a := range f.attempts(t, sessionID) {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("attempt %d not found in session %d", id, sessionID)
	return model.DiagnosticAttempt{}
}

func (f *atomicFixture) backdateCompleted(t *testing.T, id int64, stamp time.Time) {
	t.Helper()
	if _, err := f.db.ExecContext(context.Background(),
		`UPDATE diagnostic_sessions SET completed_at = ? WHERE id = ?`,
		stamp.UTC().Format(time.RFC3339), id); err != nil {
		t.Fatalf("backdate completed_at: %v", err)
	}
}

// ---- AppendDiagnosticAttempt ----

func TestDiagnosticAtomic_Append_SuccessInsertsAndSwapsSingleTransaction(t *testing.T) {
	f := newAtomicFixture(t)
	d := f.requireSession(t, f.sessionID, atomicState0, atomicSummary0, store.DiagnosticStatusActive)
	if d.CompletedAt != nil {
		t.Fatalf("fresh session has completed_at=%v", d.CompletedAt)
	}

	yes := true
	a := f.appendOK(t, atomicState0, atomicState1, atomicSummary1, true, &yes)
	if a.ID <= 0 {
		t.Fatalf("attempt.ID not backfilled: %d", a.ID)
	}
	d = f.requireSession(t, f.sessionID, atomicState1, atomicSummary1, store.DiagnosticStatusCompleted)
	if d.CompletedAt == nil {
		t.Fatal("completed=true append did not set completed_at")
	}

	got := f.attemptByID(t, f.sessionID, a.ID)
	if got.SessionID != f.sessionID || got.Seq != a.Seq || got.Role != "probe" ||
		got.AnswerJSON != `"42"` || got.Status != "answered" || got.Correct == nil || !*got.Correct {
		t.Fatalf("inserted attempt mismatch: %+v", got)
	}

	// A nil correct must persist as NULL (undecided short/self answer).
	a = f.appendOK(t, atomicState1, atomicState2, "", false, nil)
	if n := f.countAttempts(t, f.sessionID); n != 2 {
		t.Fatalf("attempt count = %d, want 2", n)
	}
	got = f.attemptByID(t, f.sessionID, a.ID)
	if got.Correct != nil || got.Status != "answered" {
		t.Fatalf("nil-correct attempt persisted as correct=%v status=%q", got.Correct, got.Status)
	}
	// 冻结契约：summary_json 严格取调用者新值（空串清空旧 summary），
	// completed=false 重新激活会话并清掉 completed_at，三者与 state 同事务。
	d = f.requireSession(t, f.sessionID, atomicState2, "", store.DiagnosticStatusActive)
	if d.CompletedAt != nil {
		t.Fatalf("completed=false append kept completed_at=%v, want NULL", d.CompletedAt)
	}
}

func TestDiagnosticAtomic_Append_StaleExpectedConflictsWithoutSideEffects(t *testing.T) {
	f := newAtomicFixture(t)
	yes := true
	_ = f.appendOK(t, atomicState0, atomicState1, atomicSummary1, false, &yes)
	before := f.loadSession(t, f.sessionID)

	a := f.answer(t, f.sessionID, &yes, `"wrong"`)
	err := f.s.AppendDiagnosticAttempt(context.Background(), f.sessionID, atomicState0, a,
		f.questionIDs[1], f.tagIDs[1], atomicState2, atomicSummary2, true)
	if !errors.Is(err, store.ErrDiagnosticStateConflict) {
		t.Fatalf("expected ErrDiagnosticStateConflict, got %v", err)
	}
	if a.ID != 0 {
		t.Fatalf("conflicted attempt got ID backfilled: %d", a.ID)
	}
	if n := f.countAttempts(t, f.sessionID); n != 1 {
		t.Fatalf("conflicted append inserted a row: count=%d", n)
	}
	after := f.loadSession(t, f.sessionID)
	if after.StateJSON != before.StateJSON || after.SummaryJSON != before.SummaryJSON ||
		after.Status != before.Status ||
		(after.CompletedAt == nil) != (before.CompletedAt == nil) ||
		(after.CompletedAt != nil && !after.CompletedAt.Equal(*before.CompletedAt)) {
		t.Fatalf("session changed on conflict: before=%+v after=%+v", before, after)
	}

	// The engine can retry from the fresh state and succeed.
	if err := f.s.AppendDiagnosticAttempt(context.Background(), f.sessionID, atomicState1, a,
		f.questionIDs[1], f.tagIDs[1], atomicState2, atomicSummary2, false); err != nil {
		t.Fatalf("retry after reload: %v", err)
	}
	f.requireSession(t, f.sessionID, atomicState2, atomicSummary2, store.DiagnosticStatusActive)
}

func TestDiagnosticAtomic_Append_InsertFailureRollsBackSession(t *testing.T) {
	cases := []struct {
		name       string
		role       string
		topicRowID int64
		stage      string
	}{
		{name: "bad_role_check", role: "bogus", topicRowID: 1, stage: "insert CHECK"},
		// topic_id is a FK; a dangling id fails the INSERT after the state
		// swap already ran in the same tx.
		{name: "fk_violation_topic", role: "probe", topicRowID: 999999, stage: "insert FK"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newAtomicFixture(t)
			a := &model.DiagnosticAttempt{
				SessionID:  f.sessionID,
				Seq:        1,
				Role:       c.role,
				AnswerJSON: `"x"`,
			}
			err := f.s.AppendDiagnosticAttempt(context.Background(), f.sessionID, atomicState0, a,
				f.questionIDs[0], c.topicRowID, atomicState1, atomicSummary1, true)
			if err == nil {
				t.Fatalf("%s: expected insert failure", c.stage)
			}
			if errors.Is(err, store.ErrDiagnosticStateConflict) {
				t.Fatalf("%s: insert failure surfaced as conflict: %v", c.stage, err)
			}
			if a.ID != 0 {
				t.Fatalf("%s: ID backfilled on failed insert: %d", c.stage, a.ID)
			}
			// The state swap from step 1 must not survive the failed insert.
			f.requireSession(t, f.sessionID, atomicState0, atomicSummary0, store.DiagnosticStatusActive)
			if n := f.countAttempts(t, f.sessionID); n != 0 {
				t.Fatalf("%s: attempt rows after rollback = %d", c.stage, n)
			}
		})
	}
}

func TestDiagnosticAtomic_Append_CompletedKeepsFirstStamp(t *testing.T) {
	f := newAtomicFixture(t)
	yes := true
	a := f.appendOK(t, atomicState0, atomicState1, atomicSummary1, true, &yes)
	if d := f.loadSession(t, f.sessionID); d.CompletedAt == nil {
		t.Fatal("first completion stamp missing")
	}

	stamp := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Second)
	f.backdateCompleted(t, f.sessionID, stamp)

	// Second completed append on the already-completed session.
	b := f.appendOK(t, atomicState1, atomicState2, atomicSummary2, true, &yes)
	d := f.requireSession(t, f.sessionID, atomicState2, atomicSummary2, store.DiagnosticStatusCompleted)
	if d.CompletedAt == nil || !d.CompletedAt.Equal(stamp) {
		t.Fatalf("completed_at moved to %v, want first stamp %v", d.CompletedAt, stamp)
	}
	if f.attemptByID(t, f.sessionID, a.ID).Status != "answered" || f.attemptByID(t, f.sessionID, b.ID).Status != "answered" {
		t.Fatal("append must not touch earlier attempts")
	}
}

func TestDiagnosticAtomic_Append_CompletedFalseKeepsSessionActive(t *testing.T) {
	f := newAtomicFixture(t)
	no := false
	f.appendOK(t, atomicState0, atomicState1, atomicSummary1, false, &no)
	d := f.requireSession(t, f.sessionID, atomicState1, atomicSummary1, store.DiagnosticStatusActive)
	if d.CompletedAt != nil {
		t.Fatalf("completed=false set completed_at=%v", d.CompletedAt)
	}
}

func TestDiagnosticAtomic_Append_NullRowIDsForSynthesizedQuestions(t *testing.T) {
	f := newAtomicFixture(t)
	a := f.answer(t, f.sessionID, nil, `"free"`)
	if err := f.s.AppendDiagnosticAttempt(context.Background(), f.sessionID, atomicState0, a,
		0, 0, atomicState1, "", false); err != nil {
		t.Fatalf("append with zero row ids: %v", err)
	}
	got := f.attemptByID(t, f.sessionID, a.ID)
	if got.QuestionID != "" || got.TopicID != "" {
		t.Fatalf("zero row ids should persist NULL FKs, got question=%q topic=%q", got.QuestionID, got.TopicID)
	}
}

// ---- ReplayDiagnosticState ----

func (f *atomicFixture) answeredIDs(t *testing.T, n int) []int64 {
	t.Helper()
	ids := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		a := f.answer(t, f.sessionID, nil, `"a"`)
		if err := f.s.CreateDiagnosticAttempt(context.Background(), a, 0, 0); err != nil {
			t.Fatalf("CreateDiagnosticAttempt %d: %v", i, err)
		}
		f.nextSeq++
		ids = append(ids, a.ID)
	}
	return ids
}

func TestDiagnosticAtomic_Replay_SuccessVoidsAndSwapsState(t *testing.T) {
	f := newAtomicFixture(t)
	ids := f.answeredIDs(t, 2)

	if err := f.s.ReplayDiagnosticState(context.Background(), f.sessionID,
		atomicState0, atomicState2, atomicSummary2, true, ids); err != nil {
		t.Fatalf("ReplayDiagnosticState: %v", err)
	}
	d := f.requireSession(t, f.sessionID, atomicState2, atomicSummary2, store.DiagnosticStatusCompleted)
	if d.CompletedAt == nil {
		t.Fatal("completed=true replay did not set completed_at")
	}
	for _, id := range ids {
		a := f.attemptByID(t, f.sessionID, id)
		if a.Status != "voided" || a.VoidedAt == nil {
			t.Fatalf("attempt %d status=%q voided_at=%v, want voided", id, a.Status, a.VoidedAt)
		}
		if a.AnswerJSON != `"a"` {
			t.Fatalf("void must keep payload, got answer=%q", a.AnswerJSON)
		}
	}
}

func TestDiagnosticAtomic_Replay_EmptyIDsStillCasState(t *testing.T) {
	f := newAtomicFixture(t)
	ids := f.answeredIDs(t, 1)

	// finish CAS: no ids to void, session state still swaps in one tx.
	if err := f.s.ReplayDiagnosticState(context.Background(), f.sessionID,
		atomicState0, atomicState1, atomicSummary1, false, nil); err != nil {
		t.Fatalf("replay with nil ids: %v", err)
	}
	f.requireSession(t, f.sessionID, atomicState1, atomicSummary1, store.DiagnosticStatusActive)
	if a := f.attemptByID(t, f.sessionID, ids[0]); a.Status != "answered" {
		t.Fatalf("empty-ids replay touched attempt: status=%q", a.Status)
	}

	// Empty non-nil slice behaves the same on the new state.
	if err := f.s.ReplayDiagnosticState(context.Background(), f.sessionID,
		atomicState1, atomicState2, "", false, []int64{}); err != nil {
		t.Fatalf("replay with empty ids: %v", err)
	}
	// 第二次 replay 传空 summary：冻结契约要求清空旧 summary，而不是
	// COALESCE 保留；冲突路径也不得把旧 summary 写回来。
	f.requireSession(t, f.sessionID, atomicState2, "", store.DiagnosticStatusActive)

	// Stale expected state with empty ids still conflicts and leaves state.
	err := f.s.ReplayDiagnosticState(context.Background(), f.sessionID,
		atomicState0, atomicState1, "", false, nil)
	if !errors.Is(err, store.ErrDiagnosticStateConflict) {
		t.Fatalf("expected ErrDiagnosticStateConflict, got %v", err)
	}
	f.requireSession(t, f.sessionID, atomicState2, "", store.DiagnosticStatusActive)
}

func TestDiagnosticAtomic_Replay_InvalidVoidIDsRollBackEverything(t *testing.T) {
	cases := []struct {
		name string
		// setup returns extra attempt ids to void beyond the valid ones.
		setup func(t *testing.T, f *atomicFixture, valid []int64, foreignSession int64) []int64
	}{
		{
			name: "foreign_session_id",
			setup: func(t *testing.T, f *atomicFixture, valid []int64, foreignSession int64) []int64 {
				other := &model.DiagnosticAttempt{SessionID: foreignSession, Seq: 1, Role: "probe", AnswerJSON: `"o"`}
				if err := f.s.CreateDiagnosticAttempt(context.Background(), other, 0, 0); err != nil {
					t.Fatalf("create foreign attempt: %v", err)
				}
				return []int64{valid[0], other.ID}
			},
		},
		{
			name: "missing_id",
			setup: func(t *testing.T, f *atomicFixture, valid []int64, _ int64) []int64 {
				return []int64{valid[0], 999999}
			},
		},
		{
			name: "already_voided_id",
			setup: func(t *testing.T, f *atomicFixture, valid []int64, _ int64) []int64 {
				if err := f.s.MarkAttemptVoided(context.Background(), valid[1]); err != nil {
					t.Fatalf("MarkAttemptVoided: %v", err)
				}
				return []int64{valid[0], valid[1]}
			},
		},
		// 注意：corrected 行现在属于合法回退目标（undo 一条被 parent Correct
		// 推翻的作答），不再列入本冲突表；对应正向覆盖见
		// TestDiagnosticAtomic_Replay_VoidsCorrectedTailNotEarlierAnswered。
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newAtomicFixture(t)
			valid := f.answeredIDs(t, 2)
			foreignSession := f.newSession(t, atomicStaleState)
			ids := c.setup(t, f, valid, foreignSession)
			before := f.loadSession(t, f.sessionID)
			foreignBefore := f.loadSession(t, foreignSession)
			voidedBefore := f.attemptByID(t, f.sessionID, valid[1])

			err := f.s.ReplayDiagnosticState(context.Background(), f.sessionID,
				atomicState0, atomicState2, atomicSummary2, true, ids)
			if !errors.Is(err, store.ErrDiagnosticStateConflict) {
				t.Fatalf("expected ErrDiagnosticStateConflict, got %v", err)
			}
			after := f.loadSession(t, f.sessionID)
			if after.StateJSON != before.StateJSON || after.SummaryJSON != before.SummaryJSON ||
				after.Status != before.Status || after.CompletedAt != nil {
				t.Fatalf("session changed on rolled-back replay: before=%+v after=%+v", before, after)
			}
			if fb := f.loadSession(t, foreignSession); fb.StateJSON != foreignBefore.StateJSON ||
				fb.SummaryJSON != foreignBefore.SummaryJSON || fb.Status != foreignBefore.Status {
				t.Fatalf("foreign session changed: before=%+v after=%+v", foreignBefore, fb)
			}
			// The earlier valid id in the list was voided inside the tx; the
			// rollback must restore it.
			if a := f.attemptByID(t, f.sessionID, valid[0]); a.Status != "answered" || a.VoidedAt != nil {
				t.Fatalf("valid attempt %d left voided after rollback: %+v", valid[0], a)
			}
			if a := f.attemptByID(t, f.sessionID, valid[1]); a.Status != voidedBefore.Status {
				t.Fatalf("attempt %d status=%q, want pre-replay %q", valid[1], a.Status, voidedBefore.Status)
			}
			if n := f.countAttempts(t, f.sessionID); n != 2 {
				t.Fatalf("attempt count = %d, want 2", n)
			}
		})
	}
}

func TestDiagnosticAtomic_Replay_DuplicateIDsConflict(t *testing.T) {
	f := newAtomicFixture(t)
	ids := f.answeredIDs(t, 2)

	// Same id twice: the first flip succeeds, the second finds nothing
	// 'answered' and must roll the whole transaction (including the first
	// flip and the state swap) back.
	err := f.s.ReplayDiagnosticState(context.Background(), f.sessionID,
		atomicState0, atomicState1, atomicSummary1, false, []int64{ids[0], ids[1], ids[0]})
	if !errors.Is(err, store.ErrDiagnosticStateConflict) {
		t.Fatalf("expected ErrDiagnosticStateConflict on duplicate id, got %v", err)
	}
	f.requireSession(t, f.sessionID, atomicState0, atomicSummary0, store.DiagnosticStatusActive)
	for _, id := range ids {
		if a := f.attemptByID(t, f.sessionID, id); a.Status != "answered" || a.VoidedAt != nil {
			t.Fatalf("attempt %d left voided after duplicate-id rollback: %+v", id, a)
		}
	}

	// The same replay with unique ids then succeeds.
	if err := f.s.ReplayDiagnosticState(context.Background(), f.sessionID,
		atomicState0, atomicState1, atomicSummary1, false, ids); err != nil {
		t.Fatalf("replay with unique ids: %v", err)
	}
	for _, id := range ids {
		if a := f.attemptByID(t, f.sessionID, id); a.Status != "voided" {
			t.Fatalf("attempt %d status=%q, want voided", id, a.Status)
		}
	}
}

func TestDiagnosticAtomic_Replay_CompletedKeepsFirstStamp(t *testing.T) {
	f := newAtomicFixture(t)
	stamp := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	f.backdateCompleted(t, f.sessionID, stamp)
	ids := f.answeredIDs(t, 1)

	if err := f.s.ReplayDiagnosticState(context.Background(), f.sessionID,
		atomicState0, atomicState1, atomicSummary1, true, ids); err != nil {
		t.Fatalf("replay: %v", err)
	}
	d := f.requireSession(t, f.sessionID, atomicState1, atomicSummary1, store.DiagnosticStatusCompleted)
	if d.CompletedAt == nil || !d.CompletedAt.Equal(stamp) {
		t.Fatalf("completed_at moved to %v, want first stamp %v", d.CompletedAt, stamp)
	}
}

// ---- Concurrency on the single-writer pool ----

type atomicRaceResult struct {
	attemptID int64
	err       error
	state     string
	summary   string
}

func TestDiagnosticAtomic_Append_ConcurrentSameExpectedOneWins(t *testing.T) {
	f := newAtomicFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const writers = 2
	calls := []struct {
		correct *bool
		state   string
		summary string
	}{
		{new(bool), atomicStateRaceA, atomicSummary1},
		{new(bool), atomicStateRaceB, atomicSummary2},
	}
	*calls[1].correct = true

	attempts := make([]*model.DiagnosticAttempt, writers)
	for i := range attempts {
		attempts[i] = &model.DiagnosticAttempt{
			SessionID:  f.sessionID,
			Seq:        i + 1,
			Role:       "probe",
			AnswerJSON: `"race"`,
			Correct:    calls[i].correct,
		}
	}

	start := make(chan struct{})
	results := make(chan atomicRaceResult, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := f.s.AppendDiagnosticAttempt(ctx, f.sessionID, atomicState0, attempts[i],
				0, 0, calls[i].state, calls[i].summary, false)
			id := int64(0)
			if err == nil {
				id = attempts[i].ID
			}
			results <- atomicRaceResult{attemptID: id, err: err, state: calls[i].state, summary: calls[i].summary}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	if err := ctx.Err(); err != nil {
		t.Fatalf("single-connection race context (deadlock?): %v", err)
	}

	var winner atomicRaceResult
	successes, conflicts := 0, 0
	for r := range results {
		switch {
		case r.err == nil:
			winner = r
			successes++
			if r.attemptID <= 0 {
				t.Fatalf("winner attempt ID not backfilled: %d", r.attemptID)
			}
		case errors.Is(r.err, store.ErrDiagnosticStateConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", r.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("expected 1 success + 1 conflict, got %d/%d", successes, conflicts)
	}

	rows := f.attempts(t, f.sessionID)
	if len(rows) != 1 {
		t.Fatalf("final attempt count = %d, want exactly 1", len(rows))
	}
	if rows[0].ID != winner.attemptID {
		t.Fatalf("persisted attempt id=%d, winner=%d", rows[0].ID, winner.attemptID)
	}
	f.requireSession(t, f.sessionID, winner.state, winner.summary, store.DiagnosticStatusActive)

	// Losers never leak IDs or partial rows.
	for i := range attempts {
		if attempts[i].ID != 0 && attempts[i].ID != winner.attemptID {
			t.Fatalf("loser attempt %d got ID %d", i, attempts[i].ID)
		}
	}

	// The race winner's state is now the expected one for a follow-up write.
	a := f.answer(t, f.sessionID, nil, `"after"`)
	if err := f.s.AppendDiagnosticAttempt(ctx, f.sessionID, winner.state, a, 0, 0, atomicStaleState, "", false); err != nil {
		t.Fatalf("follow-up append after race: %v", err)
	}
	if n := f.countAttempts(t, f.sessionID); n != 2 {
		t.Fatalf("follow-up attempt count = %d, want 2", n)
	}
}

func TestDiagnosticAtomic_ReplayAndAppend_ConcurrentSameExpectedOneWins(t *testing.T) {
	f := newAtomicFixture(t)
	ids := f.answeredIDs(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	appendAttempt := &model.DiagnosticAttempt{
		SessionID:  f.sessionID,
		Seq:        9,
		Role:       "confirm",
		AnswerJSON: `"confirm"`,
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	var replayErr, appendErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		replayErr = f.s.ReplayDiagnosticState(ctx, f.sessionID, atomicState0, atomicStateRaceA, atomicSummary1, false, ids)
	}()
	go func() {
		defer wg.Done()
		<-start
		appendErr = f.s.AppendDiagnosticAttempt(ctx, f.sessionID, atomicState0, appendAttempt,
			0, 0, atomicStateRaceB, atomicSummary2, false)
	}()
	close(start)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		t.Fatalf("single-connection race context (deadlock?): %v", err)
	}

	okReplay := replayErr == nil
	okAppend := appendErr == nil
	if okReplay == okAppend {
		t.Fatalf("expected exactly one winner, replay=%v append=%v", replayErr, appendErr)
	}
	if !okReplay && !errors.Is(replayErr, store.ErrDiagnosticStateConflict) {
		t.Fatalf("replay error = %v", replayErr)
	}
	if !okAppend && !errors.Is(appendErr, store.ErrDiagnosticStateConflict) {
		t.Fatalf("append error = %v", appendErr)
	}

	switch {
	case okReplay:
		d := f.requireSession(t, f.sessionID, atomicStateRaceA, atomicSummary1, store.DiagnosticStatusActive)
		_ = d
		if n := f.countAttempts(t, f.sessionID); n != 1 {
			t.Fatalf("loser append left %d rows, want 1", n)
		}
		if a := f.attemptByID(t, f.sessionID, ids[0]); a.Status != "voided" {
			t.Fatalf("winner replay did not void: status=%q", a.Status)
		}
	default:
		f.requireSession(t, f.sessionID, atomicStateRaceB, atomicSummary2, store.DiagnosticStatusActive)
		if n := f.countAttempts(t, f.sessionID); n != 2 {
			t.Fatalf("attempt count = %d, want 2 (original + appended)", n)
		}
		if a := f.attemptByID(t, f.sessionID, ids[0]); a.Status != "answered" {
			t.Fatalf("loser replay left attempt status=%q", a.Status)
		}
		if f.attemptByID(t, f.sessionID, appendAttempt.ID).Status != "answered" {
			t.Fatal("appended row missing")
		}
	}
}

func TestDiagnosticAtomic_ConcurrentStressNoDeadlock(t *testing.T) {
	if testing.Short() {
		t.Skip("stress")
	}
	f := newAtomicFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			a := &model.DiagnosticAttempt{SessionID: f.sessionID, Seq: 100 + i, Role: "probe", AnswerJSON: `"s"`}
			errs <- f.s.AppendDiagnosticAttempt(ctx, f.sessionID, atomicState0, a, 0, 0,
				`{"answers":[],"phase":"stress"}`, "", false)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	if err := ctx.Err(); err != nil {
		t.Fatalf("stress deadlock/timeout: %v", err)
	}
	successes := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, store.ErrDiagnosticStateConflict):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("stress successes = %d, want 1", successes)
	}
	if got := f.countAttempts(t, f.sessionID); got != 1 {
		t.Fatalf("stress attempt count = %d, want 1", got)
	}
	if _, err := f.db.ExecContext(context.Background(), `SELECT 1`); err != nil {
		t.Fatalf("connection still blocked after stress: %v", err)
	}
}
