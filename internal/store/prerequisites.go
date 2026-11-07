package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/liftedkilt/openchore/internal/taxonomy"
)

// Prerequisite-edge admin DAO (TX-PREREQ-STORE-V3-CODER). This file
// owns the graph-editor surface of knowledge_tag_prerequisites: the
// external_ref-projected edge list the admin graph renders from, the
// manual upsert (parent-added edges land as review_status='reviewed'
// immediately — a human adding an edge is the review), and the
// review/reject toggle that keeps rejected rows for audit.
//
// The 042 CHECK constrains review_status to machine/reviewed/rejected
// (there is no 'manual'/'source' column); strength is hard/soft.
// Refs resolve through TagIDByExternalRef outside any transaction so
// the write path stays a single short statement (single-writer pool).

// validatePrerequisiteStrength guards the 042 CHECK up front so the
// caller gets a readable error instead of a constraint failure.
func validatePrerequisiteStrength(strength string) error {
	if strength != "hard" && strength != "soft" {
		return errors.New(`prerequisite strength must be "hard" or "soft"`)
	}
	return nil
}

// resolvePrereqTagID resolves one external_ref to its row id with the
// store's edge-dao conventions: the ref is trimmed first, and an empty
// ref never reaches the query — a blank id cannot match anything and
// must fail as sql.ErrNoRows rather than silently succeed (DEF-PREREQ-
// READ-FILTER: whitespace-only refs used to pass through as a match
// against a blank-ref row). Returns 0 for refs that resolve to nothing.
func resolvePrereqTagID(ctx context.Context, s *Store, externalRef string) (int64, error) {
	externalRef = strings.TrimSpace(externalRef)
	if externalRef == "" {
		return 0, nil
	}
	return s.TagIDByExternalRef(ctx, externalRef)
}

// ListEffectivePrerequisiteEdges returns every non-rejected edge with
// both endpoints projected back to their external_ref ids (taxonomy.Edge
// talks in upstream string ids, the 042 table in integer FKs). Rows
// whose endpoints carry no external_ref (parent/AI free-text tags)
// cannot be represented in the engine's id space and are skipped.
// The result is always non-nil so callers can range it unconditionally.
func (s *Store) ListEffectivePrerequisiteEdges(ctx context.Context) ([]*taxonomy.Edge, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tt.external_ref, pt.external_ref, e.strength, e.reason, e.review_status
		 FROM knowledge_tag_prerequisites e
		 JOIN knowledge_tags tt ON tt.id = e.topic_id
		 JOIN knowledge_tags pt ON pt.id = e.prerequisite_id
		 WHERE e.review_status <> 'rejected'
		   AND COALESCE(tt.external_ref, '') <> ''
		   AND COALESCE(pt.external_ref, '') <> ''
		 ORDER BY e.topic_id, e.prerequisite_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*taxonomy.Edge, 0, 64)
	for rows.Next() {
		e := &taxonomy.Edge{}
		var reason sql.NullString
		if err := rows.Scan(&e.TopicID, &e.PrerequisiteID, &e.Strength, &reason, &e.ReviewStatus); err != nil {
			return nil, err
		}
		if reason.Valid {
			e.Reason = reason.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpsertManualPrerequisite adds or rewrites one prerequisite edge on
// admin authority: review_status is set to 'reviewed' on both the
// insert and the update (a human-entered edge is already reviewed —
// 042 has no 'manual' status). Both refs are resolved to row ids
// outside the write; an unknown ref surfaces as sql.ErrNoRows, the
// API layer maps it to a 404. Self-loops are rejected here as well
// (the API validates first, this is the belt to its braces).
//
// Re-upserting over a rejected edge is deliberate: the admin re-adding
// a previously rejected edge is a human re-judgement, so the row flips
// back to 'reviewed' with the new payload (the rejected row is never
// resurrected as-is — excluded.strength/reason always win).
func (s *Store) UpsertManualPrerequisite(ctx context.Context, topicRef, prerequisiteRef, strength, reason string) error {
	if err := validatePrerequisiteStrength(strength); err != nil {
		return err
	}
	topicID, err := resolvePrereqTagID(ctx, s, topicRef)
	if err != nil {
		return err
	}
	if topicID == 0 {
		return sql.ErrNoRows
	}
	prereqID, err := resolvePrereqTagID(ctx, s, prerequisiteRef)
	if err != nil {
		return err
	}
	if prereqID == 0 {
		return sql.ErrNoRows
	}
	if topicID == prereqID {
		return errors.New("a knowledge tag cannot be its own prerequisite")
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO knowledge_tag_prerequisites (topic_id, prerequisite_id, strength, reason, review_status)
		 VALUES (?, ?, ?, ?, 'reviewed')
		 ON CONFLICT(topic_id, prerequisite_id) DO UPDATE SET
		   strength = excluded.strength,
		   reason = excluded.reason,
		   review_status = 'reviewed'`,
		topicID, prereqID, strength, reason)
	return err
}

// SetPrerequisiteReviewStatus flips an existing edge's review_status
// between 'reviewed' and 'rejected' (rejected rows stay in place for
// audit — ListEffectivePrerequisiteEdges filters them out). An unknown
// ref returns sql.ErrNoRows; a resolved pair with no stored edge
// reports (false, nil) so the API can answer 404 vs 409 distinctly.
func (s *Store) SetPrerequisiteReviewStatus(ctx context.Context, topicRef, prerequisiteRef, status string) (bool, error) {
	if status != "reviewed" && status != "rejected" {
		return false, errors.New(`prerequisite review status must be "reviewed" or "rejected"`)
	}
	topicID, err := resolvePrereqTagID(ctx, s, topicRef)
	if err != nil {
		return false, err
	}
	if topicID == 0 {
		return false, sql.ErrNoRows
	}
	prereqID, err := resolvePrereqTagID(ctx, s, prerequisiteRef)
	if err != nil {
		return false, err
	}
	if prereqID == 0 {
		return false, sql.ErrNoRows
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE knowledge_tag_prerequisites SET review_status = ?
		 WHERE topic_id = ? AND prerequisite_id = ?`,
		status, topicID, prereqID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
