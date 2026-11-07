package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// Atomic diagnostic writes (042): the append/replay pair that must land
// an attempt-row change and the session state swap together. Both use
// the same compare-and-swap on state_json as CorrectDiagnosticReplay —
// a concurrent writer that moved the state first rolls the whole
// transaction back with ErrDiagnosticStateConflict.

// casSessionCompletion applies the frozen reactivate contract to one
// diagnostic_sessions row inside the caller's transaction:
//
//   - completed=true  → status='completed', completed_at keeps the
//     first non-NULL stamp (COALESCE: re-finishing must not move it).
//   - completed=false → status='active', completed_at=NULL (a completed
//     session reactivated by an undo/correct replay drops its terminal
//     state; status, stamp, summary and state swap together in this one
//     statement, i.e. same transaction, same source of truth).
//
// summary_json always takes the caller's new summary: when summaryJSON
// is non-empty it replaces the old one (an unfinished replay's
// finished=false summary must not leave the old finished=true stored);
// an empty summaryJSON clears the column — the stale summary never
// survives into the new state. A voided row is never reactivated: its
// status and stamp stay as they are (only the state/summary swap
// applies). The state swap is a compare-and-swap on
// expectedStateJSON — a row that moved (or never matched) is
// ErrDiagnosticStateConflict and the whole transaction rolls back.
func casSessionCompletion(ctx context.Context, tx *sql.Tx, sessionID int64, expectedStateJSON, newStateJSON, summaryJSON string, completed bool) error {
	var completedAt any
	if completed {
		// Keep the first completion time (re-answering a finished
		// session must not move the stamp).
		completedAt = dbTime(time.Now())
	}
	// Both CASE expressions read the pre-update row (SQLite evaluates
	// SET expressions against the old values), so the status guard in
	// the completed_at branch sees the persisted status, not the new
	// one.
	res, err := tx.ExecContext(ctx,
		`UPDATE diagnostic_sessions SET
		   state_json = ?,
		   summary_json = ?,
		   completed_at = CASE
		     WHEN status = 'voided' THEN completed_at
		     WHEN ? = 1 THEN COALESCE(completed_at, ?)
		     ELSE NULL
		   END,
		   status = CASE
		     WHEN status = 'voided' THEN 'voided'
		     WHEN ? = 1 THEN 'completed'
		     ELSE 'active'
		   END,
		   updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND state_json = ?`,
		newStateJSON, summaryJSON, boolToInt(completed), completedAt, boolToInt(completed), sessionID, expectedStateJSON)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDiagnosticStateConflict
	}
	return nil
}

// AppendDiagnosticAttempt appends one answer row and swaps the session
// state in a single transaction. The state update is a compare-and-swap:
// it only lands when the persisted state still equals expectedStateJSON
// (the snapshot the caller loaded before answering). A mismatch rolls
// everything back with ErrDiagnosticStateConflict and nothing is
// inserted. The insert mirrors CreateDiagnosticAttempt (NULL FKs,
// nullable correct, status default) and belongs to the CAS'd session;
// attempt.ID is backfilled from the inserted row.
//
// Only an active session reaches this path (the answer handler rejects
// completed/voided sessions first), so a normal answer append passes
// completed=false and the session stays active; a finishing append
// (completed=true) stamps the first completion time.
func (s *Store) AppendDiagnosticAttempt(ctx context.Context, sessionID int64, expectedStateJSON string, attempt *model.DiagnosticAttempt, questionRowID, topicRowID int64, newStateJSON, summaryJSON string, completed bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1) CAS the session state (reactivate contract, see
	// casSessionCompletion).
	if err := casSessionCompletion(ctx, tx, sessionID, expectedStateJSON, newStateJSON, summaryJSON, completed); err != nil {
		return err
	}

	// 2) Append the attempt row (NULL/correct/status handling as in
	// CreateDiagnosticAttempt; the row belongs to the CAS'd session).
	var correct any
	if attempt.Correct != nil {
		correct = boolToInt(*attempt.Correct)
	}
	var qRow, tRow any
	if questionRowID > 0 {
		qRow = questionRowID
	}
	if topicRowID > 0 {
		tRow = topicRowID
	}
	status := attempt.Status
	if status == "" {
		status = "answered"
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO diagnostic_attempts (session_id, question_id, topic_id, seq, role, answer_json, correct, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, qRow, tRow, attempt.Seq, attempt.Role, attempt.AnswerJSON, correct, status)
	if err != nil {
		return err
	}
	attempt.ID, _ = res.LastInsertId()
	return tx.Commit()
}

// ReplayDiagnosticState re-applies an engine replay atomically: the
// session state CAS and the voiding of the superseded attempts happen
// in one transaction. Every id in voidAttemptIDs must be a live
// 'answered' or 'corrected' attempt of this session; if any flip
// misses (already voided, foreign or missing) the whole transaction
// rolls back with ErrDiagnosticStateConflict. 'corrected' is voidable:
// a row a parent corrected stays in the replayed engine state (it is
// still the live row for its answer slot), so an undo that cuts it
// must void it — anything looser (voided rows re-voided, other
// statuses) stays a conflict.
//
// The state swap follows the reactivate contract (see
// casSessionCompletion): a replay of a finished session whose engine
// state is unfinished (completed=false) reactivates it — status back to
// 'active', completed_at cleared, the caller's new summary stored.
func (s *Store) ReplayDiagnosticState(ctx context.Context, sessionID int64, expectedStateJSON, newStateJSON, summaryJSON string, completed bool, voidAttemptIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1) CAS the session state (reactivate contract, see
	// casSessionCompletion).
	if err := casSessionCompletion(ctx, tx, sessionID, expectedStateJSON, newStateJSON, summaryJSON, completed); err != nil {
		return err
	}

	// 2) Void the superseded attempts ('answered' or 'corrected' guard:
	// voiding an already-voided or foreign attempt, or a status the
	// answer/correct paths never write, is a conflict that rolls the
	// state swap back too).
	now := dbTime(time.Now())
	for _, id := range voidAttemptIDs {
		res, err := tx.ExecContext(ctx,
			`UPDATE diagnostic_attempts SET status = 'voided', voided_at = ? WHERE id = ? AND session_id = ? AND status IN ('answered', 'corrected')`,
			now, id, sessionID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrDiagnosticStateConflict
		}
	}
	return tx.Commit()
}
