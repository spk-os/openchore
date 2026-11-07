package store

// L3 learning-data export (TX-LEARNING-HTTP-V2): build one kid's
// portable model.LearningExport document — the counterpart restore
// (learning_restore.go) reads back. Scope of this document: kc_mastery
// rollups, the learner-event stream, diagnostic sessions with their
// attempts, and the FSRS review deck. Review logs, the error-item
// bodies themselves, courses and config are NOT exported here — later
// work items own those, and this file must not claim otherwise.
//
// Consistency contract: BuildLearningExport runs EVERY read through one
// read-only transaction, so all sections observe the same snapshot and
// cannot disagree. The pool is a single writer (MaxOpenConns=1): while
// the tx is open no s.db-based DAO may be called — it would block on
// the very connection the tx holds and self-deadlock — so every query
// below is inlined against the tx, and each result set is fully drained
// and closed before the next query starts.
//
// Event contract (frozen with the parallel V5B restore):
//   - context_json.topic_id is taxonomy TOPIC metadata — the knowledge
//     point a diagnostic/practice round was about — NOT an object
//     reference for whatever object_type the event names. It travels as
//     ObjectRef and nothing else: the restore never maps it onto a
//     chore/review_item/course id.
//   - the source LearnerEvent.ObjectID is KEPT verbatim: the source
//     machine's local id, exported for audit and as the "is this event
//     safely restorable" marker — the restore side owns what it does
//     with it (see learning_restore.go's event contract); it is NEVER
//     a target-machine id, and nothing may map it onto one.
//   - context_json's kc_ids are resolved to tag external_refs (batched
//     queries, learningExportRefBatch ids per round) into KCRefs; a
//     kc_id whose tag has no external_ref contributes nothing — a
//     local id must not masquerade as a cross-machine ref.
//     ResultJSON/ContextJSON travel verbatim (the restore rewrites
//     ContextJSON's kc_ids, this side never does).
//   - ID/UserID are zeroed: a row number and the source user's local id
//     carry no cross-machine meaning — the document's User section is
//     the authoritative source actor.
//
// Review-card contract (one LEFT JOIN, no N+1):
//   - 'kc' cards export the source tag's external_ref (pack-level,
//     stable across machines);
//   - 'error_item' cards export error_items.source ONLY when it is a
//     stable taxonomy ref of the same user: the source starts with
//     "taxonomy:", the suffix is not blank (Go strings.TrimSpace
//     semantics — the Go-side check in the scan loop is the final word,
//     because SQLite's TRIM only strips ASCII spaces and would let
//     "taxonomy:\t" slip through), and that user has exactly one
//     error_items row with that source;
//   - every other card is NOT exported: an empty source, a
//     manual/paper-title source ("期中考试" and kin), a duplicated
//     source, an unknown source_type (course_outcome, …) or a hand-made
//     tag without external_ref. Skipping is the honest option — the
//     restore cannot safely re-attach those refs (a scope limitation,
//     not a random pick: no ORDER BY id guess ever ships a ref, and an
//     integer id string is never emitted as a ref).
//
// Every slice in the document is non-nil so the JSON never renders
// null.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// ErrLearningExportUserNotFound is returned by BuildLearningExport when
// targetUserID names no user row: the HTTP layer maps it onto 404.
var ErrLearningExportUserNotFound = errors.New("learning export: target user not found")

// learningExportPackVersion is the honest provenance stamp written into
// pack_version: this build embeds the cn-math-taxonomy pack without an
// independent upstream version number, so the marker names the pack and
// the embedding scheme instead of inventing a version that cannot be
// verified against anything.
const learningExportPackVersion = "cn-math-taxonomy@embedded-v1"

// BuildLearningExport builds one kid's learning-data document (mastery,
// learner events, diagnostics + attempts, FSRS review cards) against
// the frozen model.LearningExport schema, reading every section through
// one read-only transaction so the sections cannot disagree.
// targetUserID must name an existing user (ErrLearningExportUserNotFound
// otherwise). pack_version carries the embedded-pack provenance marker
// and exported_at is always set.
func (s *Store) BuildLearningExport(ctx context.Context, targetUserID int64) (*model.LearningExport, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	// Read-only: rollback just releases the snapshot. Deferred so every
	// return path below closes the tx and frees the pool's single
	// connection.
	defer tx.Rollback()

	// User lookup inside the tx: while the tx holds the pool's only
	// connection no s.db-based DAO (GetUser included) may run.
	var name, role string
	err = tx.QueryRowContext(ctx, `SELECT name, role FROM users WHERE id = ?`, targetUserID).
		Scan(&name, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLearningExportUserNotFound
	}
	if err != nil {
		return nil, err
	}
	doc := &model.LearningExport{
		SchemaVersion: learningExportSchemaVersion,
		PackVersion:   learningExportPackVersion,
		ExportedAt:    time.Now(),
		User: model.LearningExportUser{
			ID:   targetUserID,
			Name: name,
			Role: role,
		},
		Mastery:     make([]model.LearningMastery, 0, 16),
		Events:      make([]model.LearningEvent, 0, 64),
		Diagnostics: make([]model.DiagnosticSession, 0, 8),
		Attempts:    make([]model.DiagnosticAttempt, 0, 16),
		ReviewItems: make([]model.LearningReviewItem, 0, 16),
	}

	if err := exportLearningMastery(ctx, tx, targetUserID, doc); err != nil {
		return nil, err
	}
	if err := exportLearningEvents(ctx, tx, targetUserID, doc); err != nil {
		return nil, err
	}
	if err := exportLearningDiagnostics(ctx, tx, targetUserID, doc); err != nil {
		return nil, err
	}
	if err := exportLearningReviewItems(ctx, tx, targetUserID, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// exportLearningMastery reads the kc_mastery rollups joined with their
// knowledge_tags in one query, keying every row by the tag's
// external_ref. A mastery row whose tag has no external_ref is skipped:
// it has no stable cross-machine key, and emitting the local tag id (or
// an empty string) as the ref would fake a mapping the restore would
// resolve onto an unrelated knowledge point.
func exportLearningMastery(ctx context.Context, tx *sql.Tx, userID int64, doc *model.LearningExport) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT m.correct_count, m.total_count, m.mastery, m.low_confidence, m.last_event_at,
		        COALESCE(kt.external_ref, '')
		 FROM kc_mastery m
		 JOIN knowledge_tags kt ON kt.id = m.tag_id
		 WHERE m.user_id = ?
		 ORDER BY m.tag_id ASC`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var m model.LearningMastery
		if err := rows.Scan(&m.CorrectCount, &m.TotalCount, &m.Mastery, &m.LowConfidence,
			&m.LastEventAt, &m.ExternalRef); err != nil {
			return err
		}
		if m.ExternalRef == "" {
			continue // hand-made/AI tag: no stable ref, never fake one
		}
		doc.Mastery = append(doc.Mastery, m)
	}
	return rows.Err()
}

// eventExportContext is the slice of context_json the export reads: the
// local kc_ids (resolved to KCRefs) and the topic_id (taxonomy topic
// metadata, kept as ObjectRef).
type eventExportContext struct {
	KCIDs   []int64 `json:"kc_ids"`
	TopicID string  `json:"topic_id"`
}

// exportLearningEvents reads the complete event stream (no cap) through
// the caller's read-only transaction. context_json.topic_id — taxonomy
// topic metadata about the round, never a reference to the event's
// object_type object — becomes ObjectRef, and context_json's kc_ids
// resolve through the batched external-ref queries into KCRefs. The
// source ObjectID is deliberately KEPT: it is the source machine's
// local id, exported for audit and as the safe-restore marker the
// restore side consumes (its event contract lives in
// learning_restore.go); it is never a target-machine id. ID/UserID are
// zeroed (the document's User section is the audit source; a row
// number and the source user's local id carry no cross-machine
// meaning). ResultJSON/ContextJSON travel verbatim: the restore
// rewrites ContextJSON's kc_ids, this side never does. A kc_id whose
// tag carries no external_ref contributes no KCRef — the local id must
// not masquerade as one.
func exportLearningEvents(ctx context.Context, tx *sql.Tx, userID int64, doc *model.LearningExport) error {
	events, err := listAllLearnerEventsTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	contexts := make([]eventExportContext, len(events))
	tagIDSet := make(map[int64]bool)
	for i, e := range events {
		if e.ContextJSON == "" {
			continue
		}
		var c eventExportContext
		if err := json.Unmarshal([]byte(e.ContextJSON), &c); err != nil {
			continue // malformed context_json: event still exports, just refless
		}
		for _, id := range c.KCIDs {
			if id > 0 {
				tagIDSet[id] = true
			}
		}
		contexts[i] = c
	}
	tagIDs := make([]int64, 0, len(tagIDSet))
	for id := range tagIDSet {
		tagIDs = append(tagIDs, id)
	}
	tagRefs, err := externalRefsForTagIDsTx(ctx, tx, tagIDs)
	if err != nil {
		return err
	}
	for i, e := range events {
		c := contexts[i]
		kcRefs := make([]string, 0, len(c.KCIDs))
		for _, id := range c.KCIDs {
			if ref := tagRefs[id]; ref != "" {
				kcRefs = append(kcRefs, ref)
			}
		}
		e.ID = 0     // machine-local row number: no cross-machine meaning
		e.UserID = 0 // source user's local id: doc.User is the audit source
		// e.ObjectID stays: the source machine's local id — audit info
		// and the safe-restore marker the restore side consumes (its
		// event contract lives in learning_restore.go). Never a
		// target-machine id, and nothing maps it onto one.
		doc.Events = append(doc.Events, model.LearningEvent{
			LearnerEvent: e,
			ObjectRef:    c.TopicID, // taxonomy topic metadata only, never an object join key
			KCRefs:       kcRefs,
		})
	}
	return nil
}

// listAllLearnerEventsTx is the tx-scoped twin of ListAllLearnerEvents:
// the complete user's event stream, id ascending (stable insertion
// order), no LIMIT and no time-window filter. COALESCE maps the
// nullable result_json/context_json columns onto "" so the scan cannot
// fail the whole export; the result is fully drained and closed before
// the caller's next query (single-writer pool).
func listAllLearnerEventsTx(ctx context.Context, tx *sql.Tx, userID int64) ([]model.LearnerEvent, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, user_id, verb, object_type, object_id,
		        COALESCE(result_json, ''), COALESCE(context_json, ''), occurred_at, created_at
		 FROM learner_events WHERE user_id = ? ORDER BY id ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.LearnerEvent, 0, 64)
	for rows.Next() {
		var e model.LearnerEvent
		if err := rows.Scan(&e.ID, &e.UserID, &e.Verb, &e.ObjectType, &e.ObjectID,
			&e.ResultJSON, &e.ContextJSON, &e.OccurredAt, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// learningExportRefBatch caps how many tag ids one IN query carries:
// SQLite's default host-parameter ceiling is 999, so one unbounded IN
// over a heavy user's thousands of distinct KCs would fail the whole
// export instead of just being slow. 400 leaves ample headroom (same
// cap as the restore side's question-ref batches).
const learningExportRefBatch = 400

// externalRefsForTagIDsTx is the tx-scoped twin of
// ExternalRefsForTagIDs: batched IN queries resolving DB tag ids back
// to their external_refs (the engine's id space). The id list is
// chunked at learningExportRefBatch per query and the partial maps
// merged; every batch runs against the caller's one read-only
// transaction (same snapshot), and each result set is fully drained
// and closed before the next query starts (the tx holds the pool's
// single connection). An empty input still returns an empty, non-nil
// map; a tag with no external_ref maps to "" exactly as before.
func externalRefsForTagIDsTx(ctx context.Context, tx *sql.Tx, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	for start := 0; start < len(ids); start += learningExportRefBatch {
		end := start + learningExportRefBatch
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args[i] = id
		}
		rows, err := tx.QueryContext(ctx,
			`SELECT id, COALESCE(external_ref, '') FROM knowledge_tags WHERE id IN (`+
				strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var ref string
			if err := rows.Scan(&id, &ref); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = ref
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// exportLearningDiagnostics reads the kid's diagnostic sessions AND
// their attempts with ONE LEFT JOIN (session id + attempt seq in a
// single pass — no per-session DAO round trip, so the two sections can
// never disagree). Every attempt column is nullable through the LEFT
// JOIN and is scanned as such; a session with no answers exports the
// session and contributes NO zero-value attempt row. Session and
// attempt local ids stay for audit only: the restore owns the remap
// semantics (V5B), and no consumer may treat them as a cross-machine
// mapping key.
func exportLearningDiagnostics(ctx context.Context, tx *sql.Tx, userID int64, doc *model.LearningExport) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT ds.id, ds.user_id, ds.subject, ds.mode, ds.status, ds.scope_json, ds.state_json,
		        ds.summary_json, ds.voided_at, ds.voided_by, ds.restored_at, ds.created_at,
		        ds.updated_at, ds.completed_at,
		        da.id, COALESCE(kq.external_ref, ''), COALESCE(kt.external_ref, ''),
		        da.seq, COALESCE(da.role, ''), COALESCE(da.answer_json, ''), da.correct,
		        COALESCE(da.status, ''), da.voided_at, da.corrected_by_at
		 FROM diagnostic_sessions ds
		 LEFT JOIN diagnostic_attempts da ON da.session_id = ds.id
		 LEFT JOIN knowledge_questions kq ON kq.id = da.question_id
		 LEFT JOIN knowledge_tags kt ON kt.id = da.topic_id
		 WHERE ds.user_id = ?
		 ORDER BY ds.id ASC, da.seq ASC, da.id ASC`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	seenSessions := make(map[int64]bool)
	for rows.Next() {
		var d model.DiagnosticSession
		var a model.DiagnosticAttempt
		var summary sql.NullString
		var voidedAt, restoredAt, completedAt sql.NullTime
		var voidedBy sql.NullInt64
		// Attempt columns are NULL whenever the LEFT JOIN matched no
		// attempt row — scan them as nullables, never as zero values.
		var attemptID, attemptSeq, attemptCorrect sql.NullInt64
		var attemptVoided, attemptCorrected sql.NullTime
		if err := rows.Scan(&d.ID, &d.UserID, &d.Subject, &d.Mode, &d.Status, &d.ScopeJSON, &d.StateJSON,
			&summary, &voidedAt, &voidedBy, &restoredAt, &d.CreatedAt, &d.UpdatedAt, &completedAt,
			&attemptID, &a.QuestionID, &a.TopicID, &attemptSeq, &a.Role, &a.AnswerJSON,
			&attemptCorrect, &a.Status, &attemptVoided, &attemptCorrected); err != nil {
			return err
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
		// A session with several attempts appears once per attempt: emit
		// the session on its first row only.
		if !seenSessions[d.ID] {
			seenSessions[d.ID] = true
			doc.Diagnostics = append(doc.Diagnostics, d)
		}
		if !attemptID.Valid {
			continue // no attempt row: never fabricate a zero-value attempt
		}
		a.ID = attemptID.Int64
		a.SessionID = d.ID
		a.Seq = int(attemptSeq.Int64)
		if attemptCorrect.Valid {
			v := attemptCorrect.Int64 == 1
			a.Correct = &v
		}
		if attemptVoided.Valid {
			t := attemptVoided.Time
			a.VoidedAt = &t
		}
		if attemptCorrected.Valid {
			t := attemptCorrected.Time
			a.CorrectedByAt = &t
		}
		doc.Attempts = append(doc.Attempts, a)
	}
	return rows.Err()
}

// exportLearningReviewItems reads the kid's full FSRS deck with one LEFT
// JOIN over review_items/knowledge_tags/error_items (no N+1) and gives
// each card its stable SourceRef per source_type:
//
//   - 'kc': the source tag's external_ref (pack-level, stable across
//     machines);
//   - 'error_item': the same-user error item's source string, exported
//     ONLY when it is a stable taxonomy ref — the source starts with
//     "taxonomy:", the suffix is not blank, and the target user has
//     exactly one error_items row with that source (a duplicated source
//     would make the restore's row ambiguous, so no ORDER BY id guess
//     ever ships). The SQL CASE is the cheap pre-filter; the scan loop's
//     isStableTaxonomySource is the FINAL word on prefix + blankness —
//     SQLite's TRIM strips only ASCII spaces, so a source like
//     "taxonomy:\t" passes the SQL and must still be rejected here;
//   - anything else — empty source, manual/paper-title sources
//     ("期中考试" and kin), unknown source_type (course_outcome, …), a
//     deleted source row, a hand-made tag with no external_ref — is
//     SKIPPED: the restore's scope cannot safely re-attach those cards,
//     and an integer id string is never emitted as a ref.
func exportLearningReviewItems(ctx context.Context, tx *sql.Tx, userID int64, doc *model.LearningExport) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT ri.id, ri.user_id, ri.source_type, ri.source_id, ri.front_text, ri.back_text, ri.subject,
		        ri.due, ri.stability, ri.difficulty, ri.scheduled_days, ri.reps, ri.lapses, ri.state,
		        ri.last_review, ri.remaining_steps, ri.suspended, ri.created_at,
		        COALESCE(kt.external_ref, ''),
		        CASE
		          WHEN ri.source_type = 'error_item'
		               AND (SELECT COUNT(*) FROM error_items e2
		                     WHERE e2.user_id = ri.user_id AND e2.source = ei.source) = 1
		          THEN ei.source
		          ELSE ''
		        END
		 FROM review_items ri
		 LEFT JOIN knowledge_tags kt ON ri.source_type = 'kc' AND kt.id = ri.source_id
		 LEFT JOIN error_items ei ON ri.source_type = 'error_item' AND ei.id = ri.source_id AND ei.user_id = ri.user_id
		 WHERE ri.user_id = ?
		 ORDER BY ri.id ASC`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var i model.ReviewItem
		var lastReview sql.NullTime
		var suspended int
		var kcRef, eiRef string
		if err := rows.Scan(&i.ID, &i.UserID, &i.SourceType, &i.SourceID, &i.FrontText, &i.BackText, &i.Subject,
			&i.Due, &i.Stability, &i.Difficulty, &i.ScheduledDays, &i.Reps, &i.Lapses, &i.State,
			&lastReview, &i.RemainingSteps, &suspended, &i.CreatedAt, &kcRef, &eiRef); err != nil {
			return err
		}
		if lastReview.Valid {
			t := lastReview.Time
			i.LastReview = &t
		}
		i.Suspended = suspended != 0
		var sourceRef string
		switch i.SourceType {
		case model.ReviewSourceKC:
			sourceRef = kcRef
		case model.ReviewSourceErrorItem:
			// The SQL CASE kept only exactly-one-same-user candidates;
			// the prefix + non-blank-suffix check is Go's to make
			// (strings.TrimSpace covers every Unicode whitespace; SQLite
			// TRIM only strips ASCII spaces). A rejected candidate skips
			// the card — no empty ref, no integer id, no guess.
			if !isStableTaxonomySource(eiRef) {
				continue // blank suffix / non-taxonomy source: no stable ref, skip the card
			}
			sourceRef = eiRef
		default:
			sourceRef = "" // unknown source type: no verifiable ref space
		}
		if sourceRef == "" {
			continue // no stable ref: skip the card, never the integer id
		}
		doc.ReviewItems = append(doc.ReviewItems, model.LearningReviewItem{ReviewItem: i, SourceRef: sourceRef})
	}
	return rows.Err()
}

// taxonomySourcePrefix marks error_items.source values that are stable
// taxonomy question refs ("taxonomy:<questionID>" — written by the
// practice handler for a wrong verdict). Manual and paper-title sources
// ("期中考试" and kin) carry no such marker and are not exportable refs.
const taxonomySourcePrefix = "taxonomy:"

// isStableTaxonomySource reports whether source is a stable taxonomy
// ref fit for export: it starts with "taxonomy:" and the suffix after
// the prefix is not blank under Go's strings.TrimSpace semantics (all
// Unicode whitespace). SQLite's TRIM strips only ASCII spaces, so this
// Go check — not the SQL — is the final authority on blankness.
func isStableTaxonomySource(source string) bool {
	suffix, ok := strings.CutPrefix(source, taxonomySourcePrefix)
	return ok && strings.TrimSpace(suffix) != ""
}
