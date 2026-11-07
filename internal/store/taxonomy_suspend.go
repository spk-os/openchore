package store

// KC mastery → FSRS card sync (TX-KC-REVIEW-SYNC-V2-CODER). The
// practice/diagnostic paths update a KC's mastery status; this file
// mirrors that verdict into the kid's spaced-repetition card for the
// same KC — one short transaction, never touching error_item cards:
//
//   - mastered: the KC's active card (source_type='kc', this user) is
//     suspended — reviewing a mastered point is busywork. A card that
//     is already suspended stays suspended (a parent's manual pause is
//     never undone here); a missing card is not created.
//   - weak/shaky: a missing card is created (a minimal review_items
//     row; front/back/subject read from knowledge_tags). An existing
//     card is left completely alone — no suspended flip, no FSRS
//     scheduler reset: the scheduler columns belong to the grade path.
//   - unknown (and any other status): no-op — no signal, no action,
//     and no tag lookup either: nothing was going to be written, so a
//     missing tag under an unrecognized status is not an error.
//
// weak/shaky/mastered all verify the knowledge_tags row first, inside
// the same transaction: a missing tag returns
// ErrKCReviewSyncTagNotFound with changed=false — never a silent
// skip. The caller named a specific KC to sync, and "did nothing,
// reported success" would hide a stale or wrong tag id. In that case
// no card is created, none is suspended, nothing is partially
// written; the transaction rolls back. mastered verifies the tag even
// when a card exists: a write against an unverified KC is not allowed.
//
// review_items is UNIQUE(source_type, source_id) table-wide, so at
// most one card exists per KC: a card owned by another profile is
// never suspended through this user's sync, and a create for it is
// skipped (the constraint would reject a second card anyway).
// Error-item cards are structurally out of reach: every statement here
// is pinned to source_type='kc'.

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/taxonomy"
)

// ErrKCReviewSyncArgs is SyncKCReviewCard's parameter error: a
// non-positive user or tag id can never match a row, which is a
// caller bug, not a data condition.
var ErrKCReviewSyncArgs = errors.New("kc review sync: user_id and tag_id must be positive")

// ErrKCReviewSyncTagNotFound is SyncKCReviewCard's sentinel for a
// weak/shaky/mastered sync whose tag_id matches no knowledge_tags
// row: the caller named a KC that does not exist (stale id, wrong
// subject import, deleted tag). The sync refuses to report success —
// no card is created, nothing is suspended, nothing is written.
var ErrKCReviewSyncTagNotFound = errors.New("kc review sync: knowledge tag not found")

// SyncKCReviewCard mirrors one KC's mastery status into the user's
// review card for that KC (see the file comment for the per-status
// semantics; the status vocabulary is the engine's taxonomy.Status*
// constants). changed reports that a row was actually written (a
// suspend flip or a card insert); every deliberate no-op returns
// (false, nil). The whole read-decide-write runs in one short
// transaction on the single-writer pool — inside it only tx is used.
func (s *Store) SyncKCReviewCard(ctx context.Context, userID, tagID int64, status string) (changed bool, err error) {
	if userID <= 0 || tagID <= 0 {
		return false, ErrKCReviewSyncArgs
	}
	suspend := status == taxonomy.StatusMastered
	create := status == taxonomy.StatusWeak || status == taxonomy.StatusShaky
	if !suspend && !create {
		return false, nil // unknown (or unrecognized): no signal, no action
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	// Every recognized status writes against a KC the caller named, so
	// the knowledge_tags row is verified first, inside the transaction:
	// a missing tag is ErrKCReviewSyncTagNotFound, not a silent skip —
	// and mastered does not get to skip the check just because the card
	// already exists.
	var tagExists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM knowledge_tags WHERE id = ?)`, tagID).
		Scan(&tagExists); err != nil {
		return false, err
	}
	if !tagExists {
		return false, ErrKCReviewSyncTagNotFound
	}

	// The KC's one card, if it exists (UNIQUE(source_type, source_id)
	// is table-wide). ownerID gates every write below: a card owned by
	// another profile is never suspended through this user's sync.
	var cardID, ownerID, suspended int64
	err = tx.QueryRowContext(ctx,
		`SELECT id, user_id, suspended FROM review_items WHERE source_type = ? AND source_id = ?`,
		model.ReviewSourceKC, tagID).Scan(&cardID, &ownerID, &suspended)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	hasCard := err == nil

	switch {
	case suspend && hasCard && ownerID == userID && suspended == 0:
		res, err := tx.ExecContext(ctx,
			`UPDATE review_items SET suspended = 1 WHERE id = ? AND suspended = 0`, cardID)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			changed = true
		}
	case create && !hasCard:
		changed, err = insertKCReviewCardTx(ctx, tx, userID, tagID)
		if err != nil {
			return false, err
		}
	}
	// Every other combination is a deliberate no-op: mastered over a
	// missing / foreign / already-suspended card suspends nothing, and
	// weak/shaky over an existing card flips no flag and resets no
	// scheduler column.
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed, nil
}

// insertKCReviewCardTx creates the minimal KC review card inside an
// open transaction. The caller has already verified that the
// knowledge_tags row exists. It reports inserted=false without an
// error when the UNIQUE(source_type, source_id) constraint rejects a
// lost create race (the card exists, which is the goal). Only tx is
// used: the single-writer pool (MaxOpenConns=1) must never be asked
// for a second connection mid-transaction.
func insertKCReviewCardTx(ctx context.Context, tx *sql.Tx, userID, tagID int64) (inserted bool, err error) {
	var name, subject, description string
	err = tx.QueryRowContext(ctx,
		`SELECT name, subject, description FROM knowledge_tags WHERE id = ?`, tagID).
		Scan(&name, &subject, &description)
	if err != nil {
		return false, err
	}
	// The same minimal shape as CreateReviewItem / EnsureReviewCard:
	// front=name, back=description, subject badge, due now, state New;
	// the FSRS scheduler columns keep their column defaults.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO review_items (user_id, source_type, source_id, front_text, back_text, subject, due, state)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, model.ReviewSourceKC, tagID, name, description, subject,
		dbTime(time.Now()), model.ReviewStateNew); err != nil {
		if isUniqueConstraintErr(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
