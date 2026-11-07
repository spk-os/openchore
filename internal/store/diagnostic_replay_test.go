package store_test

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
	replayState0        = `{"answers":[{"seq":1}],"phase":"asking"}`
	replayState1        = `{"answers":[{"seq":1,"verdict":"correct"}],"phase":"asking"}`
	replayState2        = `{"answers":[{"seq":2,"verdict":"wrong"}],"phase":"asking"}`
	replayStateRaceA    = `{"answers":[{"seq":1}],"phase":"race-a"}`
	replayStateRaceB    = `{"answers":[{"seq":2}],"phase":"race-b"}`
	replayOtherState    = `{"answers":[],"phase":"other-session"}`
	replaySummary0      = `{"mastered":[],"weak":[]}`
	replaySummary1      = `{"mastered":["kc-replay-1"],"weak":[]}`
	replaySummary2      = `{"mastered":["kc-replay-2"],"weak":["kc-replay-1"]}`
	replaySummaryRaceA  = `{"mastered":["race-a"],"weak":[]}`
	replaySummaryRaceB  = `{"mastered":["race-b"],"weak":[]}`
	replayStaleState    = `{"answers":[],"phase":"stale"}`
	replayOtherSummary0 = `{"mastered":[],"weak":[]}`
)

type diagnosticReplayFixture struct {
	s           *store.Store
	db          *sql.DB
	userID      int64
	sessionID   int64
	attemptIDs  [2]int64
	tagIDs      [2]int64
	questionIDs [2]int64
}

type replaySessionSnapshot struct {
	status      string
	state       string
	summary     string
	completedAt string
}

// newDiagnosticReplayFixture builds a real migrated SQLite DB with one user,
// two knowledge points/questions, one session and two answered attempts.
func newDiagnosticReplayFixture(t *testing.T) *diagnosticReplayFixture {
	t.Helper()
	ctx := context.Background()
	s, db := setupStoreWithDB(t)
	u := createTestUser(t, s, "ReplayKid", "child")
	f := &diagnosticReplayFixture{s: s, db: db, userID: u.ID}

	names := [2]string{"加法", "减法"}
	refs := [2]string{"kc-replay-1", "kc-replay-2"}
	prompts := [2]string{"1+1=?", "2-1=?"}
	for i := 0; i < 2; i++ {
		res, err := db.ExecContext(ctx,
			`INSERT INTO knowledge_tags (name, subject, external_ref, grade, source)
			 VALUES (?, 'math', ?, 1, 'test')`, names[i], refs[i])
		if err != nil {
			t.Fatalf("insert knowledge tag %d: %v", i, err)
		}
		f.tagIDs[i], err = res.LastInsertId()
		if err != nil {
			t.Fatalf("knowledge tag %d id: %v", i, err)
		}
		res, err = db.ExecContext(ctx,
			`INSERT INTO knowledge_questions (external_ref, topic_id, difficulty, type, prompt, answer_json, source)
			 VALUES (?, ?, 'basic', 'numeric', ?, '"1"', 'test')`, refs[i]+"-q1", f.tagIDs[i], prompts[i])
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
		ScopeJSON:   `{"grade":1}`,
		StateJSON:   replayState0,
		SummaryJSON: replaySummary0,
	}
	if err := s.CreateDiagnosticSession(ctx, d); err != nil {
		t.Fatalf("CreateDiagnosticSession: %v", err)
	}
	f.sessionID = d.ID

	answers := [2]string{`"4"`, `"1"`}
	correct := [2]bool{true, false}
	for i := 0; i < 2; i++ {
		c := correct[i]
		a := &model.DiagnosticAttempt{
			SessionID:  f.sessionID,
			Seq:        i + 1,
			Role:       "probe",
			AnswerJSON: answers[i],
			Correct:    &c,
			Status:     "answered",
		}
		if err := s.CreateDiagnosticAttempt(ctx, a, f.questionIDs[i], f.tagIDs[i]); err != nil {
			t.Fatalf("CreateDiagnosticAttempt %d: %v", i, err)
		}
		f.attemptIDs[i] = a.ID
	}
	return f
}

func (f *diagnosticReplayFixture) newSessionWithAttempt(t *testing.T, state string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	d := &model.DiagnosticSession{
		UserID:      f.userID,
		Subject:     "math",
		Mode:        store.DiagnosticModeDiagnose,
		Status:      store.DiagnosticStatusActive,
		ScopeJSON:   `{"grade":1}`,
		StateJSON:   state,
		SummaryJSON: replayOtherSummary0,
	}
	if err := f.s.CreateDiagnosticSession(ctx, d); err != nil {
		t.Fatalf("CreateDiagnosticSession (other): %v", err)
	}
	a := &model.DiagnosticAttempt{
		SessionID:  d.ID,
		Seq:        1,
		Role:       "probe",
		AnswerJSON: `"other"`,
		Status:     "answered",
	}
	if err := f.s.CreateDiagnosticAttempt(ctx, a, f.questionIDs[0], f.tagIDs[0]); err != nil {
		t.Fatalf("CreateDiagnosticAttempt (other): %v", err)
	}
	return d.ID, a.ID
}

func (f *diagnosticReplayFixture) loadSession(t *testing.T, id int64) *model.DiagnosticSession {
	t.Helper()
	d, err := f.s.GetDiagnosticSession(context.Background(), id)
	if err != nil {
		t.Fatalf("GetDiagnosticSession(%d): %v", id, err)
	}
	if d == nil {
		t.Fatalf("GetDiagnosticSession(%d): nil session", id)
	}
	return d
}

func (f *diagnosticReplayFixture) snapshotSession(t *testing.T, id int64) replaySessionSnapshot {
	t.Helper()
	d := f.loadSession(t, id)
	completedAt := ""
	if d.CompletedAt != nil {
		completedAt = d.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	return replaySessionSnapshot{
		status:      d.Status,
		state:       d.StateJSON,
		summary:     d.SummaryJSON,
		completedAt: completedAt,
	}
}

func (f *diagnosticReplayFixture) requireSession(t *testing.T, state, summary, status string) *model.DiagnosticSession {
	t.Helper()
	d := f.loadSession(t, f.sessionID)
	if d.StateJSON != state || d.SummaryJSON != summary || d.Status != status {
		t.Fatalf("session state=%q summary=%q status=%q, want state=%q summary=%q status=%q",
			d.StateJSON, d.SummaryJSON, d.Status, state, summary, status)
	}
	return d
}

func (f *diagnosticReplayFixture) setCompletedAt(t *testing.T, stamp time.Time) {
	t.Helper()
	if _, err := f.db.ExecContext(context.Background(),
		`UPDATE diagnostic_sessions SET completed_at = ? WHERE id = ?`,
		stamp.UTC().Format(time.RFC3339), f.sessionID); err != nil {
		t.Fatalf("backdate completed_at: %v", err)
	}
}

func (f *diagnosticReplayFixture) attempt(t *testing.T, sessionID, attemptID int64) model.DiagnosticAttempt {
	t.Helper()
	attempts, err := f.s.ListDiagnosticAttempts(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ListDiagnosticAttempts(%d): %v", sessionID, err)
	}
	for _, a := range attempts {
		if a.ID == attemptID {
			return a
		}
	}
	t.Fatalf("attempt %d not found in session %d", attemptID, sessionID)
	return model.DiagnosticAttempt{}
}

func (f *diagnosticReplayFixture) requireAttempt(t *testing.T, sessionID, attemptID int64, status string, wantCorrectedStamp bool) model.DiagnosticAttempt {
	t.Helper()
	a := f.attempt(t, sessionID, attemptID)
	if a.Status != status {
		t.Fatalf("attempt %d status=%q, want %q", attemptID, a.Status, status)
	}
	if wantCorrectedStamp && a.CorrectedByAt == nil {
		t.Fatalf("attempt %d corrected_by_at is nil", attemptID)
	}
	if !wantCorrectedStamp && a.CorrectedByAt != nil {
		t.Fatalf("attempt %d corrected_by_at=%v, want nil", attemptID, a.CorrectedByAt)
	}
	return a
}

func TestCorrectDiagnosticReplay_SuccessCompletedAndRepeatKeepsFirstStamp(t *testing.T) {
	f := newDiagnosticReplayFixture(t)
	ctx := context.Background()

	if err := f.s.CorrectDiagnosticReplay(ctx, f.sessionID, f.attemptIDs[0],
		replayState0, replayState1, replaySummary1, true); err != nil {
		t.Fatalf("first correction: %v", err)
	}
	d := f.requireSession(t, replayState1, replaySummary1, store.DiagnosticStatusCompleted)
	if d.CompletedAt == nil {
		t.Fatal("first completed=true correction did not set completed_at")
	}
	f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "corrected", true)
	f.requireAttempt(t, f.sessionID, f.attemptIDs[1], "answered", false)

	// Simulate a first completion stamp from an earlier run. A second
	// completed=true replay must preserve it (COALESCE semantics).
	firstStamp := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	f.setCompletedAt(t, firstStamp)
	if got := f.loadSession(t, f.sessionID); got.CompletedAt == nil || !got.CompletedAt.Equal(firstStamp) {
		t.Fatalf("backdated completed_at=%v, want %v", got.CompletedAt, firstStamp)
	}

	// 空 summary 必须清空旧 summary（冻结契约：不用 COALESCE 保留）；
	// 第二次 completed=true 仍只保留第一次的 completed_at。
	if err := f.s.CorrectDiagnosticReplay(ctx, f.sessionID, f.attemptIDs[1],
		replayState1, replayState2, "", true); err != nil {
		t.Fatalf("second correction: %v", err)
	}
	d = f.requireSession(t, replayState2, "", store.DiagnosticStatusCompleted)
	if d.CompletedAt == nil || !d.CompletedAt.Equal(firstStamp) {
		t.Fatalf("completed_at moved to %v, want first stamp %v", d.CompletedAt, firstStamp)
	}
	f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "corrected", true)
	f.requireAttempt(t, f.sessionID, f.attemptIDs[1], "corrected", true)
}

func TestCorrectDiagnosticReplay_CompletedFalseLeavesSessionActive(t *testing.T) {
	f := newDiagnosticReplayFixture(t)
	ctx := context.Background()

	if err := f.s.CorrectDiagnosticReplay(ctx, f.sessionID, f.attemptIDs[0],
		replayState0, replayState1, replaySummary1, false); err != nil {
		t.Fatalf("correction: %v", err)
	}
	d := f.requireSession(t, replayState1, replaySummary1, store.DiagnosticStatusActive)
	if d.CompletedAt != nil {
		t.Fatalf("completed=false set completed_at=%v", d.CompletedAt)
	}
	f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "corrected", true)
	f.requireAttempt(t, f.sessionID, f.attemptIDs[1], "answered", false)
}

func TestCorrectDiagnosticReplay_StateConflictRollsBackAttempt(t *testing.T) {
	f := newDiagnosticReplayFixture(t)
	ctx := context.Background()
	before := f.snapshotSession(t, f.sessionID)

	err := f.s.CorrectDiagnosticReplay(ctx, f.sessionID, f.attemptIDs[0],
		replayStaleState, replayState2, replaySummary2, false)
	if !errors.Is(err, store.ErrDiagnosticStateConflict) {
		t.Fatalf("expected ErrDiagnosticStateConflict, got %v", err)
	}
	after := f.snapshotSession(t, f.sessionID)
	if before != after {
		t.Fatalf("session changed on CAS conflict: before=%+v after=%+v", before, after)
	}
	f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "answered", false)
	f.requireAttempt(t, f.sessionID, f.attemptIDs[1], "answered", false)
}

func TestCorrectDiagnosticReplay_NonAnsweredOrForeignAttempts(t *testing.T) {
	t.Run("voided_attempt", func(t *testing.T) {
		f := newDiagnosticReplayFixture(t)
		ctx := context.Background()
		if err := f.s.MarkAttemptVoided(ctx, f.attemptIDs[0]); err != nil {
			t.Fatalf("MarkAttemptVoided: %v", err)
		}
		before := f.snapshotSession(t, f.sessionID)

		err := f.s.CorrectDiagnosticReplay(ctx, f.sessionID, f.attemptIDs[0],
			replayState0, replayState1, replaySummary1, false)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expected sql.ErrNoRows, got %v", err)
		}
		if after := f.snapshotSession(t, f.sessionID); before != after {
			t.Fatalf("session changed: before=%+v after=%+v", before, after)
		}
		a := f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "voided", false)
		if a.VoidedAt == nil {
			t.Fatal("voided_at is nil after MarkAttemptVoided")
		}
	})

	t.Run("already_corrected_attempt", func(t *testing.T) {
		f := newDiagnosticReplayFixture(t)
		ctx := context.Background()
		if err := f.s.MarkAttemptCorrected(ctx, f.attemptIDs[1]); err != nil {
			t.Fatalf("MarkAttemptCorrected: %v", err)
		}
		before := f.snapshotSession(t, f.sessionID)

		err := f.s.CorrectDiagnosticReplay(ctx, f.sessionID, f.attemptIDs[1],
			replayState0, replayState1, replaySummary1, false)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expected sql.ErrNoRows, got %v", err)
		}
		if after := f.snapshotSession(t, f.sessionID); before != after {
			t.Fatalf("session changed: before=%+v after=%+v", before, after)
		}
		f.requireAttempt(t, f.sessionID, f.attemptIDs[1], "corrected", true)
	})

	t.Run("attempt_from_other_session", func(t *testing.T) {
		f := newDiagnosticReplayFixture(t)
		ctx := context.Background()
		otherSessionID, otherAttemptID := f.newSessionWithAttempt(t, replayOtherState)
		before := f.snapshotSession(t, f.sessionID)
		otherBefore := f.snapshotSession(t, otherSessionID)

		err := f.s.CorrectDiagnosticReplay(ctx, f.sessionID, otherAttemptID,
			replayState0, replayState1, replaySummary1, false)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expected sql.ErrNoRows, got %v", err)
		}
		if after := f.snapshotSession(t, f.sessionID); before != after {
			t.Fatalf("target session changed: before=%+v after=%+v", before, after)
		}
		if otherAfter := f.snapshotSession(t, otherSessionID); otherBefore != otherAfter {
			t.Fatalf("other session changed: before=%+v after=%+v", otherBefore, otherAfter)
		}
		f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "answered", false)
		f.requireAttempt(t, f.sessionID, f.attemptIDs[1], "answered", false)
		f.requireAttempt(t, otherSessionID, otherAttemptID, "answered", false)
	})
}

type replayRaceResult struct {
	attemptID int64
	err       error
	state     string
	summary   string
}

func TestCorrectDiagnosticReplay_ConcurrentCAS(t *testing.T) {
	t.Run("sequential", func(t *testing.T) {
		f := newDiagnosticReplayFixture(t)
		ctx := context.Background()

		if err := f.s.CorrectDiagnosticReplay(ctx, f.sessionID, f.attemptIDs[0],
			replayState0, replayState1, replaySummary1, false); err != nil {
			t.Fatalf("first correction: %v", err)
		}
		// Same stale expected state, different attempt: the CAS must reject
		// the second writer and roll its attempt flip back.
		err := f.s.CorrectDiagnosticReplay(ctx, f.sessionID, f.attemptIDs[1],
			replayState0, replayState2, replaySummary2, false)
		if !errors.Is(err, store.ErrDiagnosticStateConflict) {
			t.Fatalf("second correction: expected ErrDiagnosticStateConflict, got %v", err)
		}
		d := f.requireSession(t, replayState1, replaySummary1, store.DiagnosticStatusActive)
		if d.CompletedAt != nil {
			t.Fatalf("unexpected completed_at=%v", d.CompletedAt)
		}
		f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "corrected", true)
		f.requireAttempt(t, f.sessionID, f.attemptIDs[1], "answered", false)
	})

	t.Run("goroutines", func(t *testing.T) {
		f := newDiagnosticReplayFixture(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		calls := []struct {
			attemptID int64
			state     string
			summary   string
		}{
			{f.attemptIDs[0], replayStateRaceA, replaySummaryRaceA},
			{f.attemptIDs[1], replayStateRaceB, replaySummaryRaceB},
		}
		start := make(chan struct{})
		results := make(chan replayRaceResult, len(calls))
		var wg sync.WaitGroup
		for _, c := range calls {
			c := c
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				err := f.s.CorrectDiagnosticReplay(ctx, f.sessionID, c.attemptID,
					replayState0, c.state, c.summary, false)
				results <- replayRaceResult{attemptID: c.attemptID, err: err, state: c.state, summary: c.summary}
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		if err := ctx.Err(); err != nil {
			t.Fatalf("single-connection race context: %v", err)
		}

		byAttempt := make(map[int64]replayRaceResult, len(calls))
		var winner, loser replayRaceResult
		successes, conflicts := 0, 0
		for r := range results {
			byAttempt[r.attemptID] = r
			switch {
			case r.err == nil:
				winner = r
				successes++
			case errors.Is(r.err, store.ErrDiagnosticStateConflict):
				loser = r
				conflicts++
			default:
				t.Fatalf("attempt %d: unexpected error %v", r.attemptID, r.err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("expected exactly one success and one conflict, got %d/%d", successes, conflicts)
		}
		if _, ok := byAttempt[winner.attemptID]; !ok {
			t.Fatalf("winner attempt %d missing from results", winner.attemptID)
		}
		if _, ok := byAttempt[loser.attemptID]; !ok {
			t.Fatalf("loser attempt %d missing from results", loser.attemptID)
		}
		d := f.requireSession(t, winner.state, winner.summary, store.DiagnosticStatusActive)
		if d.CompletedAt != nil {
			t.Fatalf("unexpected completed_at=%v", d.CompletedAt)
		}
		f.requireAttempt(t, f.sessionID, winner.attemptID, "corrected", true)
		f.requireAttempt(t, f.sessionID, loser.attemptID, "answered", false)
	})
}

func TestCorrectDiagnosticReplay_EmptySummarySingleConnection(t *testing.T) {
	f := newDiagnosticReplayFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := f.s.CorrectDiagnosticReplay(ctx, f.sessionID, f.attemptIDs[0],
		replayState0, replayState1, "", false); err != nil {
		t.Fatalf("correction with empty summary: %v", err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("single-connection context: %v", err)
	}
	// 冻结契约：空 summary 清空旧值、completed=false 保持 active 且不设戳。
	d := f.requireSession(t, replayState1, "", store.DiagnosticStatusActive)
	if d.CompletedAt != nil {
		t.Fatalf("unexpected completed_at=%v", d.CompletedAt)
	}
	f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "corrected", true)
	f.requireAttempt(t, f.sessionID, f.attemptIDs[1], "answered", false)
}

// ---- ReplayDiagnosticState：corrected 尾行回退（DEF-DIAG-CORRECT-STALE） ----

// TestReplayDiagnosticState_VoidsCorrectedTailNotEarlierAnswered 冻结契约：
// undo 回退的答案可能已经被 admin 标成 corrected，因此 void overlay 必须
// 允许 corrected→voided；更早的 answered 行一行都不能动，行数只增不删。
func TestReplayDiagnosticState_VoidsCorrectedTailNotEarlierAnswered(t *testing.T) {
	f := newDiagnosticReplayFixture(t)
	ctx := context.Background()
	if err := f.s.MarkAttemptCorrected(ctx, f.attemptIDs[1]); err != nil {
		t.Fatalf("MarkAttemptCorrected: %v", err)
	}
	f.requireAttempt(t, f.sessionID, f.attemptIDs[1], "corrected", true)

	if err := f.s.ReplayDiagnosticState(ctx, f.sessionID, replayState0, replayState1,
		replaySummary1, false, []int64{f.attemptIDs[1]}); err != nil {
		t.Fatalf("ReplayDiagnosticState(corrected tail): %v", err)
	}
	d := f.requireSession(t, replayState1, replaySummary1, store.DiagnosticStatusActive)
	if d.CompletedAt != nil {
		t.Fatalf("completed=false replay set completed_at=%v", d.CompletedAt)
	}
	v := f.attempt(t, f.sessionID, f.attemptIDs[1])
	if v.Status != "voided" {
		t.Fatalf("attempt %d status=%q, want voided", f.attemptIDs[1], v.Status)
	}
	if v.VoidedAt == nil {
		t.Fatal("corrected→voided row has no voided_at stamp")
	}
	f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "answered", false)
	if n := f.countAttempts(t); n != 2 {
		t.Fatalf("attempt rows = %d, want 2 (void overlay never deletes)", n)
	}
}

// TestReplayDiagnosticState_CorrectedIdIsNotAConflict：旧冲突表把 corrected
// 当非法目标；冻结契约下 corrected 是 undo 的合法目标（answer → parent
// Correct → undo）。同一列表混入 corrected 行仍应成功落地，而不是像
// invalid-void-ids 用例那样整笔 Conflict。
func TestReplayDiagnosticState_CorrectedIdIsNotAConflict(t *testing.T) {
	f := newDiagnosticReplayFixture(t)
	ctx := context.Background()
	if err := f.s.MarkAttemptCorrected(ctx, f.attemptIDs[1]); err != nil {
		t.Fatalf("MarkAttemptCorrected: %v", err)
	}
	if err := f.s.ReplayDiagnosticState(ctx, f.sessionID, replayState0, replayState1,
		replaySummary1, false, []int64{f.attemptIDs[0], f.attemptIDs[1]}); err != nil {
		t.Fatalf("ReplayDiagnosticState(with corrected id) = %v, want nil", err)
	}
	f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "voided", false)
	v := f.attempt(t, f.sessionID, f.attemptIDs[1])
	if v.Status != "voided" || v.VoidedAt == nil {
		t.Fatalf("corrected attempt %d = %q voided_at=%v, want voided with stamp", f.attemptIDs[1], v.Status, v.VoidedAt)
	}
	f.requireSession(t, replayState1, replaySummary1, store.DiagnosticStatusActive)
	if n := f.countAttempts(t); n != 2 {
		t.Fatalf("attempt rows = %d, want 2", n)
	}
}

// TestReplayDiagnosticState_MixedTailAtomic：一条 corrected + 一条 answered
// 在同一笔回退里都必须落成 voided；任何一条不满足 status 守卫就整笔回滚。
func TestReplayDiagnosticState_MixedTailAtomic(t *testing.T) {
	f := newDiagnosticReplayFixture(t)
	ctx := context.Background()
	if err := f.s.MarkAttemptCorrected(ctx, f.attemptIDs[1]); err != nil {
		t.Fatalf("MarkAttemptCorrected: %v", err)
	}

	if err := f.s.ReplayDiagnosticState(ctx, f.sessionID, replayState0, replayState1,
		replaySummary1, false, []int64{f.attemptIDs[0], f.attemptIDs[1]}); err != nil {
		t.Fatalf("ReplayDiagnosticState(mixed): %v", err)
	}
	f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "voided", false)
	v := f.attempt(t, f.sessionID, f.attemptIDs[1])
	if v.Status != "voided" || v.VoidedAt == nil {
		t.Fatalf("attempt %d = %q voided_at=%v, want voided with stamp", f.attemptIDs[1], v.Status, v.VoidedAt)
	}
	f.requireSession(t, replayState1, replaySummary1, store.DiagnosticStatusActive)
}

// TestReplayDiagnosticState_StatusGuardNotRelaxed：只对 answered/corrected
// 放宽到 voided；已 voided（重复/陈旧 id）、其他 session 的行仍必须冲突，
// 且 corrected 行不得被半写（回滚后保持 corrected）。
func TestReplayDiagnosticState_StatusGuardNotRelaxed(t *testing.T) {
	t.Run("voided_attempt_in_list", func(t *testing.T) {
		f := newDiagnosticReplayFixture(t)
		ctx := context.Background()
		if err := f.s.MarkAttemptVoided(ctx, f.attemptIDs[0]); err != nil {
			t.Fatalf("MarkAttemptVoided: %v", err)
		}
		if err := f.s.MarkAttemptCorrected(ctx, f.attemptIDs[1]); err != nil {
			t.Fatalf("MarkAttemptCorrected: %v", err)
		}
		before := f.snapshotSession(t, f.sessionID)

		err := f.s.ReplayDiagnosticState(ctx, f.sessionID, replayState0, replayState1,
			replaySummary1, false, []int64{f.attemptIDs[1], f.attemptIDs[0]})
		if !errors.Is(err, store.ErrDiagnosticStateConflict) {
			t.Fatalf("expected ErrDiagnosticStateConflict, got %v", err)
		}
		if after := f.snapshotSession(t, f.sessionID); before != after {
			t.Fatalf("session changed on status-guard conflict: before=%+v after=%+v", before, after)
		}
		// 先写的 corrected 行也必须回滚：不允许半笔 overlay。
		f.requireAttempt(t, f.sessionID, f.attemptIDs[1], "corrected", true)
		f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "voided", false)
	})

	t.Run("foreign_attempt_in_list", func(t *testing.T) {
		f := newDiagnosticReplayFixture(t)
		ctx := context.Background()
		otherSessionID, otherAttemptID := f.newSessionWithAttempt(t, replayOtherState)
		before := f.snapshotSession(t, f.sessionID)
		otherBefore := f.snapshotSession(t, otherSessionID)

		err := f.s.ReplayDiagnosticState(ctx, f.sessionID, replayState0, replayState1,
			replaySummary1, false, []int64{otherAttemptID})
		if !errors.Is(err, store.ErrDiagnosticStateConflict) {
			t.Fatalf("expected ErrDiagnosticStateConflict, got %v", err)
		}
		if after := f.snapshotSession(t, f.sessionID); before != after {
			t.Fatalf("target session changed: before=%+v after=%+v", before, after)
		}
		if otherAfter := f.snapshotSession(t, otherSessionID); otherBefore != otherAfter {
			t.Fatalf("other session changed: before=%+v after=%+v", otherBefore, otherAfter)
		}
		f.requireAttempt(t, otherSessionID, otherAttemptID, "answered", false)
	})

	t.Run("state_cas_conflict_rolls_back_corrected_flip", func(t *testing.T) {
		f := newDiagnosticReplayFixture(t)
		ctx := context.Background()
		if err := f.s.MarkAttemptCorrected(ctx, f.attemptIDs[1]); err != nil {
			t.Fatalf("MarkAttemptCorrected: %v", err)
		}
		before := f.snapshotSession(t, f.sessionID)

		err := f.s.ReplayDiagnosticState(ctx, f.sessionID, replayStaleState, replayState1,
			replaySummary1, false, []int64{f.attemptIDs[1]})
		if !errors.Is(err, store.ErrDiagnosticStateConflict) {
			t.Fatalf("expected ErrDiagnosticStateConflict, got %v", err)
		}
		if after := f.snapshotSession(t, f.sessionID); before != after {
			t.Fatalf("session changed on CAS conflict: before=%+v after=%+v", before, after)
		}
		// correction 是前人留下的合法历史，不属于本次 overlay，不能被动。
		f.requireAttempt(t, f.sessionID, f.attemptIDs[1], "corrected", true)
		f.requireAttempt(t, f.sessionID, f.attemptIDs[0], "answered", false)
	})
}

func (f *diagnosticReplayFixture) countAttempts(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ?`, f.sessionID).Scan(&n); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	return n
}
