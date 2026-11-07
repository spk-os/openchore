package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// Diagnostic sessions & share links (042). Sessions persist the
// engine's serializable state (state_json) so answering can resume;
// attempts are append-only with void/correct overlay flags (never an
// UPDATE of the payload); shares store only the token hash with a
// frozen snapshot.

var (
	ErrDiagnosticSessionNotFound = errors.New("diagnostic session not found")
	ErrDiagnosticShareNotFound   = errors.New("diagnostic share not found")
	// ErrDiagnosticStateConflict: the session's persisted state_json no
	// longer matches the caller's expected snapshot — another writer
	// (e.g. a concurrent answer) got there first.
	ErrDiagnosticStateConflict = errors.New("diagnostic session state changed concurrently")
)

// DB session modes (042 CHECK): diagnose|retest|confirm — the API/
// engine modes (target|screen) live inside state_json.
const (
	DiagnosticModeDiagnose = "diagnose"
	DiagnosticModeRetest   = "retest"
	DiagnosticModeConfirm  = "confirm"
)

// Session statuses.
const (
	DiagnosticStatusActive    = "active"
	DiagnosticStatusCompleted = "completed"
	DiagnosticStatusVoided    = "voided"
)

// CreateDiagnosticSession inserts one session shell.
func (s *Store) CreateDiagnosticSession(ctx context.Context, d *model.DiagnosticSession) error {
	if d.Mode == "" {
		d.Mode = DiagnosticModeDiagnose
	}
	if d.Status == "" {
		d.Status = DiagnosticStatusActive
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO diagnostic_sessions (user_id, subject, mode, status, scope_json, state_json, summary_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.UserID, d.Subject, d.Mode, d.Status, d.ScopeJSON, d.StateJSON, nullString(d.SummaryJSON))
	if err != nil {
		return err
	}
	d.ID, _ = res.LastInsertId()
	now := time.Now()
	d.CreatedAt = now
	d.UpdatedAt = now
	return nil
}

const diagnosticSessionColumns = `id, user_id, subject, mode, status, scope_json, state_json,
	summary_json, voided_at, voided_by, restored_at, created_at, updated_at, completed_at`

func scanDiagnosticSession(row interface{ Scan(...any) error }) (*model.DiagnosticSession, error) {
	var d model.DiagnosticSession
	var summary sql.NullString
	var voidedAt, restoredAt, completedAt sql.NullTime
	var voidedBy sql.NullInt64
	err := row.Scan(&d.ID, &d.UserID, &d.Subject, &d.Mode, &d.Status, &d.ScopeJSON, &d.StateJSON,
		&summary, &voidedAt, &voidedBy, &restoredAt, &d.CreatedAt, &d.UpdatedAt, &completedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if summary.Valid {
		d.SummaryJSON = summary.String
	}
	if voidedAt.Valid {
		t := voidedAt.Time
		d.VoidedAt = &t
	}
	if voidedBy.Valid {
		v := voidedBy.Int64
		d.VoidedBy = &v
	}
	if restoredAt.Valid {
		t := restoredAt.Time
		d.RestoredAt = &t
	}
	if completedAt.Valid {
		t := completedAt.Time
		d.CompletedAt = &t
	}
	return &d, nil
}

// GetDiagnosticSession loads one session by id.
func (s *Store) GetDiagnosticSession(ctx context.Context, id int64) (*model.DiagnosticSession, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+diagnosticSessionColumns+` FROM diagnostic_sessions WHERE id = ?`, id)
	return scanDiagnosticSession(row)
}

// ListDiagnosticSessions returns a kid's sessions, newest first.
// Always non-nil.
func (s *Store) ListDiagnosticSessions(ctx context.Context, userID int64) ([]model.DiagnosticSession, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+diagnosticSessionColumns+` FROM diagnostic_sessions
		 WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.DiagnosticSession, 0, 8)
	for rows.Next() {
		d, err := scanDiagnosticSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// UpdateDiagnosticSessionState persists the engine state (and, when
// the session finished, the summary + completed timestamp).
func (s *Store) UpdateDiagnosticSessionState(ctx context.Context, id int64, stateJSON, summaryJSON string, completed bool) error {
	var summary any
	if summaryJSON != "" {
		summary = summaryJSON
	}
	var completedAt any
	if completed {
		// Keep the first completion time (re-answering a finished
		// session must not move the stamp).
		completedAt = dbTime(time.Now())
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE diagnostic_sessions SET
		   state_json = ?,
		   summary_json = COALESCE(?, summary_json),
		   completed_at = COALESCE(completed_at, ?),
		   status = CASE WHEN ? = 1 THEN 'completed' ELSE status END,
		   updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		stateJSON, summary, completedAt, boolToInt(completed), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrDiagnosticSessionNotFound
	}
	return nil
}

// VoidDiagnosticSession marks a session voided (data stays — the
// audit trail and restore path depend on it).
func (s *Store) VoidDiagnosticSession(ctx context.Context, id, voidedBy int64) error {
	now := dbTime(time.Now())
	res, err := s.db.ExecContext(ctx,
		`UPDATE diagnostic_sessions SET status = 'voided', voided_at = ?, voided_by = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, now, voidedBy, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrDiagnosticSessionNotFound
	}
	return nil
}

// RestoreDiagnosticSession un-voids a session.
func (s *Store) RestoreDiagnosticSession(ctx context.Context, id int64) error {
	now := dbTime(time.Now())
	res, err := s.db.ExecContext(ctx,
		`UPDATE diagnostic_sessions SET status = 'active', restored_at = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrDiagnosticSessionNotFound
	}
	return nil
}

// --- attempts (append-only) ---

// CreateDiagnosticAttempt appends one answer row. QuestionID/TopicID
// carry the engine's string ids; the caller resolves the integer FK
// columns (questionRowID/topicRowID, 0 = NULL — legacy synthesized
// questions have no DB row).
func (s *Store) CreateDiagnosticAttempt(ctx context.Context, a *model.DiagnosticAttempt, questionRowID, topicRowID int64) error {
	var correct any
	if a.Correct != nil {
		correct = boolToInt(*a.Correct)
	}
	var qRow, tRow any
	if questionRowID > 0 {
		qRow = questionRowID
	}
	if topicRowID > 0 {
		tRow = topicRowID
	}
	status := a.Status
	if status == "" {
		status = "answered"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO diagnostic_attempts (session_id, question_id, topic_id, seq, role, answer_json, correct, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.SessionID, qRow, tRow, a.Seq, a.Role, a.AnswerJSON, correct, status)
	if err != nil {
		return err
	}
	a.ID, _ = res.LastInsertId()
	return nil
}

// ListDiagnosticAttempts returns a session's answer rows in seq order.
// Always non-nil.
func (s *Store) ListDiagnosticAttempts(ctx context.Context, sessionID int64) ([]model.DiagnosticAttempt, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT da.id, da.session_id,
		        COALESCE(kq.external_ref, ''), COALESCE(kt.external_ref, ''),
		        da.seq, da.role, da.answer_json, da.correct, da.status, da.voided_at, da.corrected_by_at
		 FROM diagnostic_attempts da
		 LEFT JOIN knowledge_questions kq ON kq.id = da.question_id
		 LEFT JOIN knowledge_tags kt ON kt.id = da.topic_id
		 WHERE da.session_id = ? ORDER BY da.seq, da.id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.DiagnosticAttempt, 0, 16)
	for rows.Next() {
		var a model.DiagnosticAttempt
		var answerJSON sql.NullString
		var correct sql.NullInt64
		var voidedAt, correctedAt sql.NullTime
		if err := rows.Scan(&a.ID, &a.SessionID, &a.QuestionID, &a.TopicID,
			&a.Seq, &a.Role, &answerJSON, &correct, &a.Status, &voidedAt, &correctedAt); err != nil {
			return nil, err
		}
		if answerJSON.Valid {
			a.AnswerJSON = answerJSON.String
		}
		if correct.Valid {
			v := correct.Int64 == 1
			a.Correct = &v
		}
		if voidedAt.Valid {
			t := voidedAt.Time
			a.VoidedAt = &t
		}
		if correctedAt.Valid {
			t := correctedAt.Time
			a.CorrectedByAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkAttemptVoided overlays the void flag on one attempt (the row's
// payload stays — append-only with status overlay, the 042 contract).
func (s *Store) MarkAttemptVoided(ctx context.Context, attemptID int64) error {
	now := dbTime(time.Now())
	res, err := s.db.ExecContext(ctx,
		`UPDATE diagnostic_attempts SET status = 'voided', voided_at = ? WHERE id = ?`, now, attemptID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// MarkAttemptCorrected overlays the corrected flag on one attempt.
func (s *Store) MarkAttemptCorrected(ctx context.Context, attemptID int64) error {
	now := dbTime(time.Now())
	res, err := s.db.ExecContext(ctx,
		`UPDATE diagnostic_attempts SET status = 'corrected', corrected_by_at = ? WHERE id = ?`, now, attemptID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CorrectDiagnosticReplay applies a parent's correction atomically:
// the attempt's status flip (answered → corrected) and the session's
// state swap happen in one transaction, so a concurrent answer can't
// wedge the pair half-applied.
//
// The session update is a compare-and-swap on state_json: it only
// lands when the persisted state still equals expectedStateJSON (the
// snapshot the caller loaded before recomputing). A mismatch rolls
// everything back and returns ErrDiagnosticStateConflict; a missing or
// already-corrected attempt returns sql.ErrNoRows. The state swap
// follows the reactivate contract (see casSessionCompletion): a
// completed session whose replayed engine state is unfinished
// (completed=false) reactivates — status back to 'active',
// completed_at cleared, the caller's new summary stored.
func (s *Store) CorrectDiagnosticReplay(ctx context.Context, sessionID, attemptID int64, expectedStateJSON, newStateJSON, summaryJSON string, completed bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1) Flip the attempt (status='answered' guard: correcting an
	// already-corrected or voided attempt is a no-op conflict).
	now := dbTime(time.Now())
	res, err := tx.ExecContext(ctx,
		`UPDATE diagnostic_attempts SET status = 'corrected', corrected_by_at = ? WHERE id = ? AND session_id = ? AND status = 'answered'`,
		now, attemptID, sessionID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}

	// 2) CAS the session state (reactivate contract, see
	// casSessionCompletion).
	if err := casSessionCompletion(ctx, tx, sessionID, expectedStateJSON, newStateJSON, summaryJSON, completed); err != nil {
		return err
	}
	return tx.Commit()
}

// --- share links ---

// shareTokenBytes is the raw-token entropy (32 bytes = 256 bits ≥ the
// 128-bit floor in the spec).
const shareTokenBytes = 32

// NewShareToken mints a raw token (returned once) and its hash (the
// only thing stored).
func NewShareToken() (raw string, hash string, err error) {
	buf := make([]byte, shareTokenBytes)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(sum[:]), nil
}

// HashShareToken is the storage-side hash of a raw token.
func HashShareToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CreateDiagnosticShare stores one share link (token hash + snapshot).
func (s *Store) CreateDiagnosticShare(ctx context.Context, d *model.DiagnosticShare) error {
	if d.ReportKind == "" {
		d.ReportKind = "diagnosis"
	}
	var sessionID any
	if d.SessionID != nil {
		sessionID = *d.SessionID
	}
	var expiresAt any
	if d.ExpiresAt != nil {
		expiresAt = dbTime(*d.ExpiresAt)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO diagnostic_shares (token_hash, owner_id, session_id, report_kind, snapshot_json, expires_at, created_by)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.TokenHash, d.OwnerID, sessionID, d.ReportKind, d.SnapshotJSON, expiresAt, d.CreatedBy)
	if err != nil {
		return err
	}
	d.ID, _ = res.LastInsertId()
	d.CreatedAt = time.Now()
	return nil
}

// GetDiagnosticShareByToken loads one share by raw token (hashed
// first). Expired and revoked links are still returned — the caller
// maps them to 410 so the UI can say why, instead of a bare 404.
func (s *Store) GetDiagnosticShareByToken(ctx context.Context, rawToken string) (*model.DiagnosticShare, error) {
	hash := HashShareToken(rawToken)
	row := s.db.QueryRowContext(ctx,
		`SELECT id, token_hash, owner_id, session_id, report_kind, snapshot_json, expires_at, revoked_at, created_by, created_at
		 FROM diagnostic_shares WHERE token_hash = ?`, hash)
	var d model.DiagnosticShare
	var sessionID sql.NullInt64
	var snapshot sql.NullString
	var expiresAt, revokedAt sql.NullTime
	err := row.Scan(&d.ID, &d.TokenHash, &d.OwnerID, &sessionID, &d.ReportKind, &snapshot,
		&expiresAt, &revokedAt, &d.CreatedBy, &d.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sessionID.Valid {
		v := sessionID.Int64
		d.SessionID = &v
	}
	if snapshot.Valid {
		d.SnapshotJSON = snapshot.String
	}
	if expiresAt.Valid {
		t := expiresAt.Time
		d.ExpiresAt = &t
	}
	if revokedAt.Valid {
		t := revokedAt.Time
		d.RevokedAt = &t
	}
	return &d, nil
}

// GetDiagnosticShare loads one share by row id (the revoke path).
func (s *Store) GetDiagnosticShare(ctx context.Context, id int64) (*model.DiagnosticShare, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, token_hash, owner_id, session_id, report_kind, snapshot_json, expires_at, revoked_at, created_by, created_at
		 FROM diagnostic_shares WHERE id = ?`, id)
	var d model.DiagnosticShare
	var sessionID sql.NullInt64
	var snapshot sql.NullString
	var expiresAt, revokedAt sql.NullTime
	err := row.Scan(&d.ID, &d.TokenHash, &d.OwnerID, &sessionID, &d.ReportKind, &snapshot,
		&expiresAt, &revokedAt, &d.CreatedBy, &d.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sessionID.Valid {
		v := sessionID.Int64
		d.SessionID = &v
	}
	if snapshot.Valid {
		d.SnapshotJSON = snapshot.String
	}
	if expiresAt.Valid {
		t := expiresAt.Time
		d.ExpiresAt = &t
	}
	if revokedAt.Valid {
		t := revokedAt.Time
		d.RevokedAt = &t
	}
	return &d, nil
}

// ListDiagnosticShares lists a kid's share links (admin view).
// Always non-nil.
func (s *Store) ListDiagnosticShares(ctx context.Context, ownerID int64) ([]model.DiagnosticShare, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, token_hash, owner_id, session_id, report_kind, snapshot_json, expires_at, revoked_at, created_by, created_at
		 FROM diagnostic_shares WHERE owner_id = ? ORDER BY id DESC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.DiagnosticShare, 0, 4)
	for rows.Next() {
		var d model.DiagnosticShare
		var sessionID sql.NullInt64
		var snapshot sql.NullString
		var expiresAt, revokedAt sql.NullTime
		if err := rows.Scan(&d.ID, &d.TokenHash, &d.OwnerID, &sessionID, &d.ReportKind, &snapshot,
			&expiresAt, &revokedAt, &d.CreatedBy, &d.CreatedAt); err != nil {
			return nil, err
		}
		if sessionID.Valid {
			v := sessionID.Int64
			d.SessionID = &v
		}
		if expiresAt.Valid {
			t := expiresAt.Time
			d.ExpiresAt = &t
		}
		if revokedAt.Valid {
			t := revokedAt.Time
			d.RevokedAt = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokeDiagnosticShare soft-revokes one share (the row stays for
// audit; the token stops resolving).
func (s *Store) RevokeDiagnosticShare(ctx context.Context, id int64) error {
	now := dbTime(time.Now())
	res, err := s.db.ExecContext(ctx,
		`UPDATE diagnostic_shares SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrDiagnosticShareNotFound
	}
	return nil
}

// ShareShareIsValid reports whether a loaded share is still open (not
// revoked, not past expiry).
func ShareIsValid(d *model.DiagnosticShare, now time.Time) bool {
	if d == nil {
		return false
	}
	if d.RevokedAt != nil {
		return false
	}
	if d.ExpiresAt != nil && now.After(*d.ExpiresAt) {
		return false
	}
	return true
}

// shareRawTokenPrefix keeps the raw token out of any log line: the
// only place it appears is the create response.
func shareRawTokenPrefix(raw string) string {
	if len(raw) > 8 {
		return raw[:8] + "…"
	}
	return strings.Repeat("*", len(raw))
}

var _ = shareRawTokenPrefix
