package store

import (
	"context"
	"fmt"
)

// Taxonomy status observability (card FR2-B). Read-only aggregates over
// error_item_tags for GET /api/admin/taxonomy/status: how many
// error-item↔knowledge-tag bindings point at an in-system pack KC
// (is_system=1 with a non-empty external_ref) versus the fallback
// free-text tags (FR-11). Nothing here writes, rewrites or deletes a
// binding — the fallback path is a feature that must survive
// zero-regression, this only measures it.

// bindingNumeratorCase is the "in-system KC" test. Both queries below
// embed the exact same expression so the per-subject slices always
// reconcile with the global numerator/denominator.
const bindingNumeratorCase = `CASE WHEN kt.is_system=1 AND kt.external_ref IS NOT NULL AND kt.external_ref<>'' THEN 1 ELSE 0 END`

// Scope / criterion constants of the frozen response contract: a global
// (admin, not per-user) data-health read, counted over error_item_tags.
const (
	TaxonomyBindingScope     = "global"
	TaxonomyBindingCriterion = "error_item_tags"
)

// SubjectBinding is one subject's slice of the FR-11 binding ratio,
// grouped by error_items.subject (the item's subject, not the tag's).
type SubjectBinding struct {
	Numerator   int64    `json:"numerator"`
	Denominator int64    `json:"denominator"`
	Ratio       *float64 `json:"ratio"`
}

// TaxonomyBindingStats is the FR-11 aggregate: in-system KC bindings
// (numerator) over all error-item tag bindings (denominator). Ratio is
// nil (JSON null) when the denominator is zero — an empty table has no
// ratio, never a fake 0.
type TaxonomyBindingStats struct {
	Numerator   int64                     `json:"numerator"`
	Denominator int64                     `json:"denominator"`
	Ratio       *float64                  `json:"ratio"`
	Scope       string                    `json:"scope"`
	Criterion   string                    `json:"criterion"`
	BySubject   map[string]SubjectBinding `json:"by_subject"`
}

// TaxonomyBindingStats aggregates the FR-11 binding ratio. Two short
// queries, no explicit transaction (the pool is single-writer,
// MaxOpenConns=1 — short reads are what keep it unblocked). The global
// pass LEFT JOINs knowledge_tags so a dangling tag_id still counts as a
// binding (denominator) while never qualifying as an in-system KC.
func (s *Store) TaxonomyBindingStats(ctx context.Context) (*TaxonomyBindingStats, error) {
	stats := &TaxonomyBindingStats{
		Scope:     TaxonomyBindingScope,
		Criterion: TaxonomyBindingCriterion,
		BySubject: make(map[string]SubjectBinding),
	}
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*), COALESCE(SUM(%s),0)
		   FROM error_item_tags eit
		   LEFT JOIN knowledge_tags kt ON kt.id = eit.tag_id`, bindingNumeratorCase),
	).Scan(&stats.Denominator, &stats.Numerator); err != nil {
		return nil, err
	}
	stats.Ratio = bindingRatio(stats.Numerator, stats.Denominator)

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT ei.subject, COUNT(*), COALESCE(SUM(%s),0)
		   FROM error_item_tags eit
		   JOIN error_items ei ON ei.id = eit.error_item_id
		   LEFT JOIN knowledge_tags kt ON kt.id = eit.tag_id
		  GROUP BY ei.subject`, bindingNumeratorCase))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var subject string
		var sb SubjectBinding
		if err := rows.Scan(&subject, &sb.Denominator, &sb.Numerator); err != nil {
			return nil, err
		}
		sb.Ratio = bindingRatio(sb.Numerator, sb.Denominator)
		stats.BySubject[subject] = sb
	}
	return stats, rows.Err()
}

// bindingRatio returns numerator/denominator, or nil when the
// denominator is zero (the JSON null contract).
func bindingRatio(numerator, denominator int64) *float64 {
	if denominator == 0 {
		return nil
	}
	r := float64(numerator) / float64(denominator)
	return &r
}
