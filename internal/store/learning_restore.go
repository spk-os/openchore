package store

// L3 learning-data restore (V5A + V5B diagnostics): rebuild one kid's
// mastery, event stream, diagnostic sessions (with their attempts) and
// FSRS review cards from a model.LearningExport document.
//
// Scope rules:
//   - the export's join keys are pack-level refs (knowledge_tags.
//     external_ref for mastery/event/review-card/attempt-topic refs,
//     knowledge_questions.external_ref for attempt question refs),
//     never machine-local row ids, so a restore onto another machine
//     remaps everything through MapTagIDsByExternalRef and the
//     question-ref map before the write transaction opens;
//   - one transaction writes all five sections — the pool is a single
//     writer (MaxOpenConns=1), so nothing inside the tx may touch s.db
//     (nested connection use would self-deadlock); every statement
//     below is inlined against the tx, not routed through the
//     s.db-based DAO methods, and any error rolls the whole document
//     back — no section ever lands half-applied;
//   - mastery rows are upserted with the exported values verbatim —
//     bumpKCMastery is NOT called: the export already carries the
//     rollup, and recomputing it from a half-restored event stream
//     would produce numbers the old machine never saw;
//   - a stale export never clobbers newer local data: a row is only
//     overwritten when the exported timestamp (last_event_at for
//     mastery, last_review for cards) is strictly newer than the local
//     one, which also makes a repeated restore of the same document a
//     no-op (identical timestamps skip);
//   - learner_events has no unique key (append-only xAPI semantics), so
//     repeat-restore idempotency is enforced by a content-level dedupe
//     key (verb/object/result/context/occurred_at) built from the rows
//     the target user already has;
//   - review cards restore their full go-fsrs Card fields (the
//     scheduler columns), so the restored deck schedules exactly as it
//     did on the old machine;
//   - a ref that cannot be mapped (tag or question missing on this
//     machine, or an empty source ref) skips that row — skipped rows are
//     counted, never silently dropped;
//   - diagnostic_sessions/diagnostic_attempts (V5B): the exported
//     session.ID is the source machine's audit id — it is never reused
//     as this machine's row id; the restore inserts fresh rows and
//     links attempts to the new session id through a session-id map.
//     The schema has no cross-machine UUID, so repeat-restore
//     idempotency rides on a deterministic content key (see
//     restoreDiagnosticsTx). voided_by is a source-machine user id
//     with no mapping here — it lands NULL while every timestamp
//     stays. Attempt refs (QuestionID/TopicID) are external refs
//     remapped through the question/tag maps; an unmappable or empty
//     ref becomes NULL (legacy synthesized questions have no DB row),
//     never a guessed integer.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// learningExportSchemaVersion is the export schema_version this restore
// path understands. A document from any other schema (newer, older, 0,
// negative) is refused whole rather than half-imported.
const learningExportSchemaVersion = 1

// ErrLearningRestoreUnsupportedSchema wraps every schema_version
// refusal (newer, older, 0 or negative — the export writer's contract
// is exactly 1): callers can errors.Is it instead of matching message
// text.
var ErrLearningRestoreUnsupportedSchema = errors.New("learning restore: unsupported schema_version")

// ErrLearningRestoreTargetNotFound wraps the mandatory-target refusal
// (targetUserID names no user on this machine). DB-level errors from
// the user lookup are returned unwrapped, as-is.
var ErrLearningRestoreTargetNotFound = errors.New("learning restore: target user not found on this server")

// RestoreLearning restores one kid's learning data (mastery, learner
// events, FSRS review cards, diagnostic sessions and their attempts)
// from an export document onto targetUserID. targetUserID is the
// frozen-signature mandatory restore target: it must be > 0 and name a
// user that already exists on this machine — the document's own
// TargetUserID/User fields are never consulted, so an admin cannot
// accidentally restore one kid's data onto another profile. The
// restore never creates profiles (creating a kid is an explicit
// parent action, and an implicit remap onto the wrong household is the
// classic data-corruption footgun).
func (s *Store) RestoreLearning(ctx context.Context, targetUserID int64, payload *model.LearningExport) (*model.LearningRestoreResult, error) {
	if payload == nil {
		return nil, errors.New("learning restore: payload is required")
	}
	// Strict equality, not a ceiling: schema_version 0/negative is just
	// as uninterpretable as 2+ — the export writer's contract is "1".
	if payload.SchemaVersion != learningExportSchemaVersion {
		return nil, fmt.Errorf("%w %d (this build understands %d)",
			ErrLearningRestoreUnsupportedSchema, payload.SchemaVersion, learningExportSchemaVersion)
	}
	if targetUserID <= 0 {
		return nil, errors.New("learning restore: target user id is required")
	}
	user, err := s.GetUser(ctx, targetUserID)
	if err != nil {
		return nil, err // DB failure stays the raw store error
	}
	if user == nil {
		return nil, fmt.Errorf("%w: %d on this server", ErrLearningRestoreTargetNotFound, targetUserID)
	}

	// --- ref → local id mapping, OUTSIDE the transaction ---
	// One batched lookup for every pack-level tag ref the document
	// carries (mastery keys, event KC refs, attempt topic refs). The
	// events' ObjectRef is NOT in this set: it is taxonomy topic
	// metadata, never mapped onto a target object id (frozen event
	// contract, see restoreEventsTx). Question refs resolve through a
	// separate batched query on knowledge_questions — the two ref
	// spaces are disjoint and must not be cross-resolved. Review cards
	// map separately: their SourceRef is resolved per source_type (a KC
	// ref here, an error-item ref there), not as a blanket tag ref.
	// Empty refs are not queried (they cannot map). Unknown refs are
	// simply absent from the map and the owning row counts as skipped
	// (for attempt refs the row survives with a NULL FK instead —
	// legacy synthesized questions carry no DB row).
	refSet := make(map[string]bool)
	for _, m := range payload.Mastery {
		if m.ExternalRef != "" {
			refSet[m.ExternalRef] = true
		}
	}
	for _, e := range payload.Events {
		for _, r := range e.KCRefs {
			if r != "" {
				refSet[r] = true
			}
		}
	}
	for _, a := range payload.Attempts {
		if a.TopicID != "" {
			refSet[a.TopicID] = true
		}
	}
	for _, ri := range payload.ReviewItems {
		// Only KC cards resolve through the tag map; error-item cards go
		// through error_items.source, so their ref must not pollute the
		// tag lookup (and vice versa).
		if ri.SourceType == model.ReviewSourceErrorItem {
			continue
		}
		if ri.SourceRef != "" {
			refSet[ri.SourceRef] = true
		}
	}
	refs := make([]string, 0, len(refSet))
	for r := range refSet {
		refs = append(refs, r)
	}
	// Batched tag mapping: SQLite's default host-parameter ceiling is 999,
	// so a document carrying a whole curriculum's worth of distinct refs
	// would overflow one IN (…) query. Map in chunks of 400 distinct refs
	// and merge per batch (refs are already deduplicated, so a ref never
	// spans two batches and the merged map is deterministic). The chunking
	// lives here, not in MapTagIDsByExternalRef, which other callers share.
	tagIDs := make(map[string]int64, len(refs))
	const tagRefBatch = 400
	for start := 0; start < len(refs); start += tagRefBatch {
		end := start + tagRefBatch
		if end > len(refs) {
			end = len(refs)
		}
		part, err := s.MapTagIDsByExternalRef(ctx, refs[start:end])
		if err != nil {
			return nil, fmt.Errorf("learning restore: ref mapping failed: %w", err)
		}
		for k, v := range part {
			tagIDs[k] = v
		}
	}
	// Question refs (attempt QuestionID) resolve in one batched query on
	// knowledge_questions, outside the tx. Blank refs stay out of the
	// query; an unmappable ref is simply absent from the map and the
	// attempt's question FK lands NULL (legacy synthesized questions).
	questionRefs := make([]string, 0, len(payload.Attempts))
	for _, a := range payload.Attempts {
		if a.QuestionID != "" {
			questionRefs = append(questionRefs, a.QuestionID)
		}
	}
	questionIDs, err := s.mapQuestionIDsByExternalRef(ctx, questionRefs)
	if err != nil {
		return nil, fmt.Errorf("learning restore: question ref mapping failed: %w", err)
	}

	// --- single write transaction ---
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res := &model.LearningRestoreResult{}
	if err := restoreMasteryTx(ctx, tx, targetUserID, payload.ExportedAt, payload.Mastery, tagIDs, res); err != nil {
		return nil, err
	}
	if err := restoreEventsTx(ctx, tx, targetUserID, payload.Events, tagIDs, res); err != nil {
		return nil, err
	}
	if err := restoreReviewItemsTx(ctx, tx, targetUserID, payload.ExportedAt, payload.ReviewItems, tagIDs, res); err != nil {
		return nil, err
	}
	if err := restoreDiagnosticsTx(ctx, tx, targetUserID, payload.Diagnostics, payload.Attempts, tagIDs, questionIDs, res); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// Totals: the five restored sections.
	res.Imported = res.Mastery.Imported + res.Events.Imported + res.ReviewItems.Imported +
		res.Diagnostics.Imported + res.Attempts.Imported
	res.Skipped = res.Mastery.Skipped + res.Events.Skipped + res.ReviewItems.Skipped +
		res.Diagnostics.Skipped + res.Attempts.Skipped
	return res, nil
}

// strictlyNewerTime is the stale-export guard's comparator: an exported
// timestamp only wins when it is strictly newer than the stored one.
// A nil/zero exported timestamp never overwrites; a valid exported
// timestamp against a NULL column does (the local row was never
// touched). Equal timestamps skip, which is what makes a repeated
// restore of the same document a no-op.
func strictlyNewerTime(cand *time.Time, cur sql.NullTime) bool {
	if cand == nil || cand.IsZero() {
		return false
	}
	if !cur.Valid {
		return true
	}
	return cand.After(cur.Time)
}

// utcNanoKey renders one timestamp for a content-level dedupe key: UTC
// RFC3339Nano, so keys survive timezone-loaded scans and still
// distinguish sub-second stamps. Zero times map to "".
func utcNanoKey(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// nanoKeyOrEmpty renders a nullable timestamp for a content key: nil or
// zero becomes "" (the two are indistinguishable across a JSON
// round-trip anyway, so keying them the same is the honest choice).
func nanoKeyOrEmpty(t *time.Time) string {
	if t == nil {
		return ""
	}
	return utcNanoKey(*t)
}

// mapQuestionIDsByExternalRef resolves a whole batch of
// knowledge_questions external_refs to their local row ids in one
// query (the attempt restore path batches its FK lookups like
// MapTagIDsByExternalRef does for tags). Unknown refs are absent from
// the map; the caller decides whether that skips or NULLs the FK.
func (s *Store) mapQuestionIDsByExternalRef(ctx context.Context, refs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	const questionRefBatch = 400 // SQLite's default host-parameter ceiling is 999
	for start := 0; start < len(refs); start += questionRefBatch {
		end := start + questionRefBatch
		if end > len(refs) {
			end = len(refs)
		}
		batch := refs[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, r := range batch {
			placeholders[i] = "?"
			args[i] = r
		}
		rows, err := s.db.QueryContext(ctx,
			`SELECT external_ref, id FROM knowledge_questions WHERE external_ref IN (`+
				strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ref string
			var id int64
			if err := rows.Scan(&ref, &id); err != nil {
				rows.Close()
				return nil, err
			}
			out[ref] = id
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// restoreMasteryTx upserts the exported kc_mastery rows verbatim. No
// rollup recomputation (no bumpKCMastery): the export already carries
// the numbers the old machine computed, and recomputing from a possibly
// partial event restore would invent values. A local row whose
// last_event_at is at or after the exported one is left untouched.
//
// The exported row's effective time is its LastEventAt, falling back to
// the document's ExportedAt when the row carries none: an export cannot
// be older than the moment it was taken, and "no timestamp at all"
// used to mean the row could never overwrite a NULL local column —
// now it compares against ExportedAt instead, which is strictly better
// than refusing the write and strictly safer than always writing (a
// local row updated after the export still wins).
func restoreMasteryTx(ctx context.Context, tx *sql.Tx, userID int64, exportedAt time.Time, rows []model.LearningMastery, tagIDs map[string]int64, res *model.LearningRestoreResult) error {
	for _, m := range rows {
		tagID, ok := tagIDs[m.ExternalRef]
		if !ok || tagID <= 0 {
			res.Mastery.Skipped++ // unknown knowledge point on this machine
			continue
		}
		// Effective exported time: the row's own stamp, else ExportedAt.
		mAt := exportedAt
		if m.LastEventAt != nil && !m.LastEventAt.IsZero() {
			mAt = *m.LastEventAt
		}
		var curLastEvent sql.NullTime
		err := tx.QueryRowContext(ctx,
			`SELECT last_event_at FROM kc_mastery WHERE user_id = ? AND tag_id = ?`,
			userID, tagID).Scan(&curLastEvent)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO kc_mastery (user_id, tag_id, correct_count, total_count, mastery, low_confidence, last_event_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				userID, tagID, m.CorrectCount, m.TotalCount, m.Mastery, boolToInt(m.LowConfidence), nullTime(m.LastEventAt)); err != nil {
				return err
			}
			res.Mastery.Imported++
			continue
		}
		if err != nil {
			return err
		}
		// Stale guard against the effective exported time: a local NULL
		// last_event_at loses to any effective time (the local row was
		// never touched), a local stamp at or after it keeps the row.
		effective := mAt
		if !strictlyNewerTime(&effective, curLastEvent) {
			res.Mastery.Skipped++ // local rollup is newer (or same restore again)
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE kc_mastery SET correct_count = ?, total_count = ?, mastery = ?, low_confidence = ?, last_event_at = ?
			 WHERE user_id = ? AND tag_id = ?`,
			m.CorrectCount, m.TotalCount, m.Mastery, boolToInt(m.LowConfidence), nullTime(m.LastEventAt),
			userID, tagID); err != nil {
			return err
		}
		res.Mastery.Imported++
	}
	return nil
}

// eventDedupeKey identifies one restored event by content. learner_events
// is append-only with no unique key, so repeat-restore idempotency has to
// be content-based: an event identical to one the target already has
// (same verb, object, payloads and timestamp) is treated as already
// restored.
type eventDedupeKey struct {
	Verb        string
	ObjectType  string
	ObjectID    int64
	ResultJSON  string
	ContextJSON string
	OccurredAt  string // UTC, time.DateTime — matches how the column stores
}

// restoreEventsTx re-inserts the exported learner events under the
// target user, under the frozen event contract (see learning_export.go):
//
//   - ObjectRef is taxonomy TOPIC metadata only — the knowledge point a
//     diagnostic/practice round was about, never a reference to the
//     object_type's object. It is NOT mapped onto a target object_id
//     (an earlier draft mapped it onto the local tag; that mis-pointed
//     chore/review_item/error_item events at a taxonomy tag).
//   - only events whose source ObjectID == 0 restore, and they land with
//     object_id 0: the source machine's local id has no counterpart
//     here, and guessing one (a chore here that happens to share the
//     id) would credit the wrong object twice. An event with a
//     non-zero source ObjectID skips — counted, never silently
//     dropped, and never a duplicate or mis-point of a local object.
//   - KCRefs (and only they) remap to local tag ids and rewrite
//     context_json's kc_ids, so the mastery rollup joins on valid
//     local ids. An unmappable KCRef is dropped from kc_ids while the
//     event itself — a durable xAPI fact — is still restored.
func restoreEventsTx(ctx context.Context, tx *sql.Tx, userID int64, rows []model.LearningEvent, tagIDs map[string]int64, res *model.LearningRestoreResult) error {
	// Load the target's existing events (dedupe set) inside the tx so a
	// concurrent writer cannot slip a duplicate in between the read and
	// the inserts. Only tx queries here — the pool has one connection.
	existing, err := tx.QueryContext(ctx,
		`SELECT verb, object_type, object_id, COALESCE(result_json, ''), COALESCE(context_json, ''), occurred_at
		 FROM learner_events WHERE user_id = ?`, userID)
	if err != nil {
		return err
	}
	seen := make(map[eventDedupeKey]bool, 64)
	for existing.Next() {
		var k eventDedupeKey
		var occurredAt time.Time
		if err := existing.Scan(&k.Verb, &k.ObjectType, &k.ObjectID, &k.ResultJSON, &k.ContextJSON, &occurredAt); err != nil {
			existing.Close()
			return err
		}
		k.OccurredAt = occurredAt.UTC().Format(time.DateTime)
		seen[k] = true
	}
	if err := existing.Err(); err != nil {
		existing.Close()
		return err
	}
	existing.Close()

	for _, e := range rows {
		if e.Verb == "" || e.ObjectType == "" {
			res.Events.Skipped++ // an xAPI statement needs both to mean anything
			continue
		}
		if e.ObjectID != 0 {
			res.Events.Skipped++ // source object id is machine-local: no safe target counterpart
			continue
		}
		kcIDs := make([]int64, 0, len(e.KCRefs))
		for _, r := range e.KCRefs {
			if id, ok := tagIDs[r]; ok && id > 0 {
				kcIDs = append(kcIDs, id)
			}
		}
		contextJSON, err := rewriteEventKCIDs(e.ContextJSON, kcIDs)
		if err != nil {
			res.Events.Skipped++ // malformed context_json cannot be remapped
			continue
		}
		occurredAt := e.OccurredAt
		if occurredAt.IsZero() {
			occurredAt = time.Now()
		}
		createdAt := e.CreatedAt
		if createdAt.IsZero() {
			createdAt = occurredAt
		}
		// occurred_at/created_at are written in the same UTC time.DateTime
		// form the column's CURRENT_TIMESTAMP default produces, not dbTime's
		// RFC3339: CountLearnerEventsSince compares the column against
		// time.DateTime strings, and the two formats do not sort together.
		key := eventDedupeKey{
			Verb:        e.Verb,
			ObjectType:  e.ObjectType,
			ObjectID:    0, // safe-restore contract: source 0 → target 0
			ResultJSON:  e.ResultJSON,
			ContextJSON: contextJSON,
			OccurredAt:  occurredAt.UTC().Format(time.DateTime),
		}
		if seen[key] {
			res.Events.Skipped++ // already restored (idempotent re-restore)
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO learner_events (user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, e.Verb, e.ObjectType, 0, e.ResultJSON, contextJSON,
			key.OccurredAt, createdAt.UTC().Format(time.DateTime)); err != nil {
			return err
		}
		seen[key] = true
		res.Events.Imported++
	}
	return nil
}

// rewriteEventKCIDs swaps context_json's kc_ids array for the remapped
// local tag ids. The exported array's local ids are meaningless here, so
// with no mappable refs the key is removed entirely (a stale id pointing
// at an unrelated local tag would corrupt the rollup). An empty context
// with refs becomes a fresh {"kc_ids": [...]} object.
func rewriteEventKCIDs(contextJSON string, kcIDs []int64) (string, error) {
	if contextJSON == "" {
		if len(kcIDs) == 0 {
			return "", nil
		}
		b, err := json.Marshal(map[string]any{"kc_ids": kcIDs})
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(contextJSON), &m); err != nil {
		return "", err
	}
	if len(kcIDs) == 0 {
		delete(m, "kc_ids")
	} else {
		m["kc_ids"] = kcIDs
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// restoreReviewItemsTx restores the exported FSRS cards with their full
// scheduler state (due/stability/difficulty/scheduled_days/reps/lapses/
// state/last_review/remaining_steps — the go-fsrs Card fields), so the
// deck schedules on this machine exactly as it did on the old one. The
// card's SourceRef resolves per source_type — the two ref spaces are
// disjoint and must not be cross-resolved:
//   - 'kc': the pack-level knowledge-tag external_ref, resolved through
//     the caller's tag map (mapped OUTSIDE the tx; a tx-side s.db call
//     would nest on the single-connection pool and self-deadlock);
//   - 'error_item': the error item's stable source string
//     ("taxonomy:<questionID>" and kin), resolved against the TARGET
//     user's error_items rows by Source == SourceRef. Zero matches
//     (unknown here) and duplicate matches (error_items has no
//     UNIQUE(user_id, source)) both skip — this restore never guesses a
//     binding, never creates a wrong question, and the ref is never
//     treated as a KC external_ref;
//   - anything else (empty, 'course_outcome', …): skipped — an unknown
//     source_type's ref space cannot be verified on this machine.
//
// review_items is UNIQUE(source_type, source_id) table-wide, so a source
// object owned by another local profile skips — the schema allows one
// card per source, not one per kid.
//
// The stale guard compares the card's effective exported time (its
// LastReview, else the document's ExportedAt) against the local
// last_review: a NULL local stamp loses to any effective time, a local
// stamp at or after it keeps the row, and both sides nil no longer
// means an eternal skip.
func restoreReviewItemsTx(ctx context.Context, tx *sql.Tx, userID int64, exportedAt time.Time, rows []model.LearningReviewItem, tagIDs map[string]int64, res *model.LearningRestoreResult) error {
	for _, ri := range rows {
		if ri.SourceRef == "" {
			res.ReviewItems.Skipped++ // no source ref: nothing to attach the card to
			continue
		}
		var sourceType string
		var sourceID int64
		switch ri.SourceType {
		case model.ReviewSourceKC:
			// KC cards resolve through the caller's pre-mapped tag map
			// (no s.db inside the tx — single-writer pool).
			id, ok := tagIDs[ri.SourceRef]
			if !ok || id <= 0 {
				res.ReviewItems.Skipped++ // knowledge point unknown on this machine
				continue
			}
			sourceType = model.ReviewSourceKC
			sourceID = id
		case model.ReviewSourceErrorItem:
			// Error-item cards resolve against the target's own error
			// book by the stable source string. error_items has no
			// UNIQUE(user_id, source), so duplicate sources are
			// schema-possible: bind the card only when EXACTLY one row
			// matches. Zero rows (unknown here) and duplicates both
			// skip — this restore never guesses a binding, never
			// creates the row, never reinterprets the ref as a KC ref.
			var matches int
			err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*), COALESCE(MIN(id), 0) FROM error_items WHERE user_id = ? AND source = ?`,
				userID, ri.SourceRef).Scan(&matches, &sourceID)
			if err != nil {
				return err
			}
			if matches != 1 {
				res.ReviewItems.Skipped++ // unknown here (0) or duplicate source (>1): refuse to guess
				continue
			}
			sourceType = model.ReviewSourceErrorItem
		default:
			res.ReviewItems.Skipped++ // unknown source type: unverifiable ref space
			continue
		}
		state := ri.State
		if state == "" {
			state = model.ReviewStateNew // state has a CHECK constraint; "" would violate it
		}
		due := ri.Due
		if due.IsZero() {
			due = time.Now()
		}
		createdAt := ri.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now()
		}

		var existingID int64
		var ownerID int64
		var curLastReview, curCreatedAt sql.NullTime
		err := tx.QueryRowContext(ctx,
			`SELECT id, user_id, last_review, created_at FROM review_items WHERE source_type = ? AND source_id = ?`,
			sourceType, sourceID).Scan(&existingID, &ownerID, &curLastReview, &curCreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO review_items (user_id, source_type, source_id, front_text, back_text, subject,
				     due, stability, difficulty, scheduled_days, reps, lapses, state, last_review,
				     remaining_steps, suspended, created_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				userID, sourceType, sourceID, ri.FrontText, ri.BackText, ri.Subject,
				dbTime(due), ri.Stability, ri.Difficulty, ri.ScheduledDays, ri.Reps, ri.Lapses, state,
				nullTime(ri.LastReview), ri.RemainingSteps, boolToInt(ri.Suspended), dbTime(createdAt)); err != nil {
				return err
			}
			res.ReviewItems.Imported++
			continue
		}
		if err != nil {
			return err
		}
		if ownerID != userID {
			res.ReviewItems.Skipped++ // source object's card belongs to another profile here
			continue
		}
		// Effective exported review time: the card's LastReview, else
		// the document's ExportedAt (the card cannot be older than the
		// export that carried it). A local NULL last_review compares
		// against the local created_at as its placeholder stamp — the
		// card's best "when was this row last touched" — so a card
		// created locally after the export is never clobbered, and
		// both-sides-nil no longer means an eternal skip.
		riAt := exportedAt
		if ri.LastReview != nil && !ri.LastReview.IsZero() {
			riAt = *ri.LastReview
		}
		curRef := curLastReview
		if !curRef.Valid {
			curRef = curCreatedAt
		}
		if !strictlyNewerTime(&riAt, curRef) {
			res.ReviewItems.Skipped++ // local card is at least as reviewed (or same restore again)
			continue
		}
		// Local card is older: overwrite the scheduler columns with the
		// exported FSRS state. Front/back/subject/created_at stay local —
		// they are this machine's content, not scheduler state.
		if _, err := tx.ExecContext(ctx,
			`UPDATE review_items SET due = ?, stability = ?, difficulty = ?, scheduled_days = ?,
			     reps = ?, lapses = ?, state = ?, last_review = ?, remaining_steps = ?, suspended = ?
			 WHERE id = ?`,
			dbTime(due), ri.Stability, ri.Difficulty, ri.ScheduledDays, ri.Reps, ri.Lapses, state,
			nullTime(ri.LastReview), ri.RemainingSteps, boolToInt(ri.Suspended), existingID); err != nil {
			return err
		}
		res.ReviewItems.Imported++
	}
	return nil
}

// diagnosticSessionKey is the deterministic content key standing in for
// the cross-machine UUID the schema does not have: one kid's session is
// identified by when it was created plus everything its shell carries.
// Two exports of the same source session produce the same key, so a
// repeated restore finds the first restore's row instead of appending a
// duplicate. CreatedAt participates at RFC3339Nano precision: SQLite
// stores it at second granularity via CURRENT_TIMESTAMP, but two
// source sessions with the same second and identical
// subject/mode/scope/state/summary are the colliding-tail edge a
// no-UUID schema cannot split — the key keeps the nano digits the
// document carries, so a genuine source pair a nanosecond apart stays
// distinguishable.
type diagnosticSessionKey struct {
	UserID      int64
	CreatedAt   string // UTC RFC3339Nano
	Subject     string
	Mode        string
	Status      string
	ScopeJSON   string
	StateJSON   string
	SummaryJSON string
	CompletedAt string // UTC RFC3339Nano, "" when nil
}

// diagnosticAttemptKey is one attempt's content key: the session it
// belongs to (by the NEW local session id), its position in the run and
// everything the append-only row carries. Attempts are append-only
// with no unique key, so repeat restores dedupe against the rows the
// new session already has. VoidedAt/CorrectedByAt are part of the key:
// a void/correct overlay changes the row's content, and a document
// re-exported after the overlay legitimately differs from one exported
// before it.
type diagnosticAttemptKey struct {
	SessionID     int64
	Seq           int
	Role          string
	QuestionID    int64 // local row id, 0 = NULL (legacy synthesized question)
	TopicID       int64 // local row id, 0 = NULL
	AnswerJSON    string
	Correct       sql.NullBool
	Status        string
	VoidedAt      string
	CorrectedByAt string
}

// diagnosticSessionPlan is the restore's per-session decision: which
// local row the exported session maps onto (existing or to-insert) and
// which attempts go with it.
type diagnosticSessionPlan struct {
	key     diagnosticSessionKey
	session model.DiagnosticSession // the exported session (ID is the source machine's audit id)
	// existingID > 0: the exported session already has a local row
	// under this content key (created by an earlier restore); 0: insert.
	existingID    int64
	localCurAt    *time.Time // existing row's updated_at (nil when NULL)
	allowedUpdate bool       // exported UpdatedAt is strictly newer than localCurAt
	// attempts are the exported attempts whose exported SessionID names
	// this session, in document order.
	attempts []model.DiagnosticAttempt
}

// restoreDiagnosticsTx restores the exported diagnostic sessions and
// their attempts, in the caller's single write transaction (any error
// rolls the whole document — sessions and attempts included — back
// together with mastery/events/cards).
//
// Identity, without a cross-machine UUID:
//   - the exported session.ID is the source machine's audit id. It is
//     never used as this machine's row id and never queried by it;
//     attempts reference their session through the source ID, which the
//     first pass translates into a source-ID → plan map;
//   - idempotency rides on the deterministic content key (user, UTC
//     created_at at RFC3339Nano, subject, mode, status, scope/state/
//     summary JSON, completed_at). A repeat restore of the same
//     document finds the earlier restore's row and skips it whole —
//     its attempts are then all duplicates and skip too;
//   - same created_at but different content is the no-UUID collision
//     edge: the key includes every shell column, so a different
//     subject/mode/summary simply does not collide. When it does
//     collide (byte-identical key), the stale rule applies — a local
//     updated_at at or after the exported one skips the session, an
//     exported one strictly newer may update the allowed fields
//     (status/scope/state/summary/voided_at/voided_by→NULL/
//     restored_at/completed_at/updated_at) and then backfills only the
//     attempts the local session is still missing. Without a UUID
//     there is no way to prove two byte-identical sessions on two
//     machines are "the same one" rather than an honest same-second
//     collision; treating them as the same (instead of appending a
//     near-duplicate row that no list view can tell apart) is the
//     least-surprise reading, and it can never clobber a local row the
//     document is not strictly newer than.
//
// The session's own fields:
//   - user_id is forced to the restore target; the exported UserID is
//     the source machine's audit value and is never consulted;
//   - voided_by is a source-machine user id with no mapping here — it
//     is written NULL on every path, while voided_at/restored_at/
//     completed_at/created_at/updated_at keep their exported values
//     (created_at rides the content key; the rest are plain columns);
//   - subject/mode/status/scope_json/state_json/summary_json travel
//     verbatim. mode/status have CHECK constraints: a value the schema
//     refuses is a per-session skip (the session AND its attempts —
//     never half a session), reported through Diagnostics.Skipped and
//     Attempts.Skipped, not a whole-document error;
//   - attempts: QuestionID/TopicID are external refs remapped through
//     the question/tag maps; missing/empty refs become NULL FKs (the
//     legacy question bank has synthesized questions with no DB row),
//     never a guessed integer. Seq/Role/AnswerJSON/Correct/Status/
//     VoidedAt/CorrectedByAt are restored verbatim; created_at has no
//     exported counterpart and is left to the column's DEFAULT; the
//     source attempt ID is never reused.
//
// This restore does NOT touch diagnostic_shares (token/owner audit is
// out of scope here) and triggers no learner events or review-card
// side effects: the values land exactly as exported.
func restoreDiagnosticsTx(ctx context.Context, tx *sql.Tx, userID int64,
	sessions []model.DiagnosticSession, attempts []model.DiagnosticAttempt,
	tagIDs, questionIDs map[string]int64, res *model.LearningRestoreResult) error {

	// --- pass 1: build the content key per exported session and pair
	// every exported attempt with its session (by the source audit id).
	plans := make([]diagnosticSessionPlan, 0, len(sessions))
	bySrcID := make(map[int64]*diagnosticSessionPlan, len(sessions))
	for i := range sessions {
		s := sessions[i]
		// CHECK-constraint guards: refuse the values the schema would
		// reject anyway, as a per-session skip rather than a failed
		// INSERT that would roll the whole document back.
		if !validDiagnosticMode(s.Mode) || !validDiagnosticStatus(s.Status) {
			res.Diagnostics.Skipped++ // illegal DB CHECK value: this session (and, below, its attempts) skips
			bySrcID[s.ID] = nil       // marker: attempts of an invalid session must skip too
			continue
		}
		plans = append(plans, diagnosticSessionPlan{
			key: diagnosticSessionKey{
				UserID:      userID,
				CreatedAt:   utcNanoKey(s.CreatedAt),
				Subject:     s.Subject,
				Mode:        s.Mode,
				Status:      s.Status,
				ScopeJSON:   s.ScopeJSON,
				StateJSON:   s.StateJSON,
				SummaryJSON: s.SummaryJSON,
				CompletedAt: nanoKeyOrEmpty(s.CompletedAt),
			},
			session:       s,
			existingID:    0,
			allowedUpdate: false,
		})
	}
	// Sessions with identical content keys collapse onto one plan; the
	// duplicate source rows are counted as skipped sessions (their
	// attempts re-attach to the surviving plan, whose own attempt
	// dedupe then skips the repeated rows — a document that carries the
	// same session twice restores it once, like a repeat restore).
	byKey := make(map[diagnosticSessionKey]*diagnosticSessionPlan, len(plans))
	duplicateSessions := 0
	for i := range plans {
		p := &plans[i]
		if first, ok := byKey[p.key]; ok {
			p.existingID = -1 // marker: duplicate of an earlier plan in this same document
			duplicateSessions++
			_ = first
			continue
		}
		byKey[p.key] = p
	}
	for i := range plans {
		if plans[i].existingID == -1 {
			continue
		}
		bySrcID[plans[i].session.ID] = &plans[i]
	}
	// Pair attempts with their session. An attempt whose exported
	// SessionID names no exported session (or one skipped as invalid or
	// duplicate) cannot be restored — there is no local row to attach
	// it to — so it skips.
	for _, a := range attempts {
		p, known := bySrcID[a.SessionID]
		if !known || p == nil {
			res.Attempts.Skipped++ // orphaned attempt: its session is not restorable
			continue
		}
		p.attempts = append(p.attempts, a)
	}

	// --- pass 2: resolve each plan against the target's existing rows
	// inside the tx, then write: insert fresh sessions, update allowed
	// fields of strictly-newer sessions, insert only missing attempts.
	for i := range plans {
		p := &plans[i]
		if p.existingID == -1 {
			// Duplicate content within one document: restore the session
			// once; the copy itself counts as a skipped session, and its
			// attempts were already attached to the surviving plan.
			res.Diagnostics.Skipped++
			continue
		}

		// Resolve the content key against the target's existing rows:
		// one query per plan, in the tx (single-writer pool — never
		// s.db here). created_at compares at the column's text
		// granularity, UTC-normalized, so a re-export of the same
		// document matches the row an earlier restore wrote.
		var existingID int64
		var curUpdated sql.NullTime
		err := tx.QueryRowContext(ctx,
			`SELECT id, updated_at FROM diagnostic_sessions
			 WHERE user_id = ? AND subject = ? AND mode = ?
			   AND created_at = ?`,
			userID, p.session.Subject, p.session.Mode,
			dbTime(p.session.CreatedAt.UTC().Truncate(time.Second))).Scan(&existingID, &curUpdated)

		switch {
		case errors.Is(err, sql.ErrNoRows):
			// Fresh session: insert under a new local id. The exported ID
			// is the source machine's audit id and is never reused as
			// this machine's row id; voided_by is a source-machine user
			// reference with no mapping here, so it lands NULL while
			// every exported timestamp keeps its value.
			insert, err := tx.ExecContext(ctx,
				`INSERT INTO diagnostic_sessions (user_id, subject, mode, status, scope_json, state_json, summary_json,
				     voided_at, voided_by, restored_at, created_at, updated_at, completed_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?)`,
				userID, p.session.Subject, p.session.Mode, p.session.Status,
				p.session.ScopeJSON, p.session.StateJSON, p.session.SummaryJSON,
				nullTime(p.session.VoidedAt), nullTime(p.session.RestoredAt),
				dbTime(p.session.CreatedAt), dbTime(p.session.UpdatedAt), nullTime(p.session.CompletedAt))
			if err != nil {
				return err
			}
			newID, err := insert.LastInsertId()
			if err != nil {
				return err
			}
			p.existingID = newID
			p.allowedUpdate = true // brand-new row: the plan's attempts all insert
			res.Diagnostics.Imported++
		case err != nil:
			return err
		default:
			// The content key matched an existing local row — either an
			// earlier restore of the same document or (per the collision
			// note above) a byte-identical same-second session. The
			// stale rule decides: a local updated_at at or after the
			// exported one skips the session whole (a repeat restore is
			// a no-op); a strictly newer exported updated_at updates the
			// allowed fields and then backfills only missing attempts.
			p.existingID = existingID
			if curUpdated.Valid {
				p.localCurAt = &curUpdated.Time
				p.allowedUpdate = p.session.UpdatedAt.After(curUpdated.Time)
			} else {
				p.allowedUpdate = true // NULL local updated_at: the row was never touched
			}
			if !p.allowedUpdate {
				res.Diagnostics.Skipped++
			} else {
				// Exported shell is strictly newer: update the allowed
				// fields — subject/mode/status/scope_json/state_json/
				// summary_json verbatim, the exported overlay timestamps
				// (voided_at/restored_at/completed_at/updated_at), and
				// voided_by forced NULL (a source-machine user id has no
				// mapping here). created_at is the content key's spine
				// and stays; the source session ID never lands anywhere.
				if _, err := tx.ExecContext(ctx,
					`UPDATE diagnostic_sessions SET subject = ?, mode = ?, status = ?,
					     scope_json = ?, state_json = ?, summary_json = ?,
					     voided_at = ?, voided_by = NULL, restored_at = ?, completed_at = ?, updated_at = ?
					 WHERE id = ?`,
					p.session.Subject, p.session.Mode, p.session.Status,
					p.session.ScopeJSON, p.session.StateJSON, p.session.SummaryJSON,
					nullTime(p.session.VoidedAt), nullTime(p.session.RestoredAt),
					nullTime(p.session.CompletedAt), dbTime(p.session.UpdatedAt), p.existingID); err != nil {
					return err
				}
				res.Diagnostics.Imported++
			}
		}
		if err := restoreDiagnosticAttemptsTx(ctx, tx, userID, p, tagIDs, questionIDs, res); err != nil {
			return err
		}
	}
	return nil
}

// restoreDiagnosticAttemptsTx restores one plan's attempts against its
// resolved local session row (existing or freshly inserted). On the
// update path the target's existing attempts are loaded by content key
// so only genuinely missing rows backfill; on a skipped session every
// attempt skips too — a session is never half-restored. Attempt refs
// (QuestionID/TopicID) remap through the pre-built maps; a missing or
// empty ref becomes a NULL FK (the legacy question bank synthesized
// questions with no DB row) — never a guessed integer. created_at has
// no exported counterpart and is left to the column's DEFAULT. All
// queries run on the tx.
func restoreDiagnosticAttemptsTx(ctx context.Context, tx *sql.Tx, userID int64, p *diagnosticSessionPlan,
	tagIDs, questionIDs map[string]int64, res *model.LearningRestoreResult) error {

	// Dedupe set for an existing local row: the session's attempts by
	// content key (append-only rows, no unique constraint). A freshly
	// inserted session cannot have any, so the query is skipped for it.
	existing := make(map[diagnosticAttemptKey]bool, len(p.attempts))
	if !p.allowedUpdate || p.session.ID == 0 {
		// The `p.session.ID == 0` case is unreachable in practice
		// (plans always carry the source audit id); the guard keeps the
		// load on the safe side for hand-built documents.
		rows, err := tx.QueryContext(ctx,
			`SELECT session_id, COALESCE(question_id, 0), COALESCE(topic_id, 0), seq, role,
			        COALESCE(answer_json, ''), correct, status, voided_at, corrected_by_at
			 FROM diagnostic_attempts WHERE session_id = ?`, p.existingID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k diagnosticAttemptKey
			var correct sql.NullInt64
			var voidedAt, correctedAt sql.NullTime
			if err := rows.Scan(&k.SessionID, &k.QuestionID, &k.TopicID, &k.Seq, &k.Role,
				&k.AnswerJSON, &correct, &k.Status, &voidedAt, &correctedAt); err != nil {
				rows.Close()
				return err
			}
			if correct.Valid {
				k.Correct.Bool = correct.Int64 != 0
				k.Correct.Valid = true
			}
			if voidedAt.Valid {
				k.VoidedAt = utcNanoKey(voidedAt.Time)
			}
			if correctedAt.Valid {
				k.CorrectedByAt = utcNanoKey(correctedAt.Time)
			}
			existing[k] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}

	for _, a := range p.attempts {
		// Refs remap through the pre-built maps; missing/empty → NULL
		// FK (the row still restores: the attempt itself is the fact).
		var qRow, tRow any
		if id, ok := questionIDs[a.QuestionID]; ok && id > 0 {
			qRow = id
		}
		if id, ok := tagIDs[a.TopicID]; ok && id > 0 {
			tRow = id
		}
		var correct any
		if a.Correct != nil {
			correct = boolToInt(*a.Correct)
		}
		status := a.Status
		if status == "" {
			status = "answered" // the live writer's default (042 CHECK: answered|voided|corrected)
		}
		k := diagnosticAttemptKey{
			SessionID:     p.existingID,
			Seq:           a.Seq,
			Role:          a.Role,
			QuestionID:    int64FromAny(qRow),
			TopicID:       int64FromAny(tRow),
			AnswerJSON:    a.AnswerJSON,
			Status:        status,
			VoidedAt:      nanoKeyOrEmpty(a.VoidedAt),
			CorrectedByAt: nanoKeyOrEmpty(a.CorrectedByAt),
		}
		if a.Correct != nil {
			k.Correct.Bool = *a.Correct
			k.Correct.Valid = true
		}
		if !p.allowedUpdate || existing[k] {
			res.Attempts.Skipped++ // skipped session or already-restored attempt (idempotent re-restore)
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO diagnostic_attempts (session_id, question_id, topic_id, seq, role, answer_json, correct, status, voided_at, corrected_by_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.existingID, qRow, tRow, a.Seq, a.Role, a.AnswerJSON, correct, status,
			nullTime(a.VoidedAt), nullTime(a.CorrectedByAt)); err != nil {
			return err
		}
		existing[k] = true
		res.Attempts.Imported++
	}
	return nil
}

// int64FromAny unwraps a nil-able FK bind back to its id (0 = NULL).
func int64FromAny(v any) int64 {
	if id, ok := v.(int64); ok {
		return id
	}
	return 0
}

// validDiagnosticMode reports whether the exported mode satisfies the
// 042 CHECK (diagnose|retest|confirm). An empty string is NOT valid
// here: the live write path defaults it, but a restore must not invent
// a mode the source machine never wrote.
func validDiagnosticMode(m string) bool {
	switch m {
	case DiagnosticModeDiagnose, DiagnosticModeRetest, DiagnosticModeConfirm:
		return true
	}
	return false
}

// validDiagnosticStatus reports whether the exported status satisfies
// the 042 CHECK (active|completed|voided). Same rule as the mode guard:
// empty is the export writer's bug, not a value to default.
func validDiagnosticStatus(s string) bool {
	switch s {
	case DiagnosticStatusActive, DiagnosticStatusCompleted, DiagnosticStatusVoided:
		return true
	}
	return false
}
