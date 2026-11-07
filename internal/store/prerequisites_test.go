package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// TX-PREREQ-STORE-V3-TESTER pins the admin prerequisite-edge DAO
// (ListEffectivePrerequisiteEdges / UpsertManualPrerequisite /
// SetPrerequisiteReviewStatus) against a real 042-migrated SQLite DB.
// Seed rows use fixed ids via raw SQL so the JOIN projection can be
// asserted by external_ref, mirroring what the importer writes.

// seedPrereqTags inserts the tag fixture plus an unrelated tag and a
// real user so the "nothing else touched" assertions have witnesses.
// Tags: 1=cA, 2=cB, 3=cC (with refs), 4=free text (NULL ref), 5=blank
// ref (” — the importer's "no upstream id" shape).
func seedPrereqTags(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (301, 'Kid', 'child')`)
	mustExec(t, db, `INSERT INTO knowledge_tags (id, name, subject, external_ref, source) VALUES
		(1, 'tag A', 'math', 'cnm_a', 'cn-math-taxonomy@v1'),
		(2, 'tag B', 'math', 'cnm_b', 'cn-math-taxonomy@v1'),
		(3, 'tag C', 'math', 'cnm_c', 'cn-math-taxonomy@v1'),
		(4, 'parent free text', 'math', NULL, ''),
		(5, 'blank ref', 'math', '', '')`)
}

func seedPrereqEdge(t *testing.T, db *sql.DB, topicID, prereqID int64, strength, reason, status string) {
	t.Helper()
	mustExec(t, db,
		`INSERT INTO knowledge_tag_prerequisites (topic_id, prerequisite_id, strength, reason, review_status)
		 VALUES (?, ?, ?, ?, ?)`,
		topicID, prereqID, strength, reason, status)
}

// fetchPrereqRow reads one stored edge directly; ok=false when absent.
func fetchPrereqRow(t *testing.T, db *sql.DB, topicID, prereqID int64) (strength, reason, status string, ok bool) {
	t.Helper()
	err := db.QueryRow(
		`SELECT strength, reason, review_status FROM knowledge_tag_prerequisites
		 WHERE topic_id = ? AND prerequisite_id = ?`, topicID, prereqID).
		Scan(&strength, &reason, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", false
	}
	if err != nil {
		t.Fatalf("fetchPrereqRow(%d,%d): %v", topicID, prereqID, err)
	}
	return strength, reason, status, true
}

func countPrereqRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge_tag_prerequisites`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

// TestPrerequisiteListEffectiveEdges covers the read projection: both
// JOINs must resolve external_ref on each endpoint, rejected and
// ref-less rows must drop out, and the empty case must be [] not nil.
func TestPrerequisiteListEffectiveEdges(t *testing.T) {
	s, db := setupStoreWithDB(t)
	ctx := context.Background()
	seedPrereqTags(t, db)

	// Included: machine hard with reason, reviewed soft without reason,
	// id-4 free-text row (NULL ref) and id-5 blank-ref row drop out.
	seedPrereqEdge(t, db, 1, 2, "hard", "A needs B", "machine")        // cnm_a <- cnm_b
	seedPrereqEdge(t, db, 2, 3, "soft", "", "reviewed")                // cnm_b <- cnm_c
	seedPrereqEdge(t, db, 1, 3, "hard", "A needs C", "rejected")       // excluded: rejected
	seedPrereqEdge(t, db, 4, 1, "hard", "free text topic", "machine")  // excluded: NULL ref topic
	seedPrereqEdge(t, db, 1, 4, "hard", "free text prereq", "machine") // excluded: NULL ref prereq
	seedPrereqEdge(t, db, 5, 1, "soft", "blank topic", "machine")      // excluded: '' ref topic
	seedPrereqEdge(t, db, 2, 5, "hard", "blank prereq", "machine")     // excluded: '' ref prereq
	seedPrereqEdge(t, db, 4, 5, "hard", "both ref-less", "rejected")   // excluded twice over

	edges, err := s.ListEffectivePrerequisiteEdges(ctx)
	if err != nil {
		t.Fatalf("ListEffectivePrerequisiteEdges: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2: %+v", len(edges), edges)
	}
	// ORDER BY topic_id, prerequisite_id → (tag A, tag B) then (tag B, tag C).
	first, second := edges[0], edges[1]
	if first.TopicID != "cnm_a" || first.PrerequisiteID != "cnm_b" {
		t.Fatalf("edges[0] = %s<-%s, want cnm_a<-cnm_b", first.TopicID, first.PrerequisiteID)
	}
	if first.Strength != "hard" || first.Reason != "A needs B" || first.ReviewStatus != "machine" {
		t.Fatalf("edges[0] = %+v, want hard/A needs B/machine", first)
	}
	if second.TopicID != "cnm_b" || second.PrerequisiteID != "cnm_c" {
		t.Fatalf("edges[1] = %s<-%s, want cnm_b<-cnm_c", second.TopicID, second.PrerequisiteID)
	}
	if second.Strength != "soft" || second.Reason != "" || second.ReviewStatus != "reviewed" {
		t.Fatalf("edges[1] = %+v, want soft/empty/reviewed (NULL-safe reason)", second)
	}
}

func TestPrerequisiteListEffectiveEdgesEmptyIsNonNil(t *testing.T) {
	s, db := setupStoreWithDB(t)
	seedPrereqTags(t, db)

	edges, err := s.ListEffectivePrerequisiteEdges(context.Background())
	if err != nil {
		t.Fatalf("ListEffectivePrerequisiteEdges: %v", err)
	}
	if edges == nil {
		t.Fatal("edges is nil, want non-nil empty slice")
	}
	if len(edges) != 0 {
		t.Fatalf("got %d edges, want 0", len(edges))
	}
}

// TestPrerequisiteUpsertManual covers the write path: strength/reason
// whitelist and ref resolution errors first, then insert, conflict
// upsert (row count stable, fields rewritten, status forced reviewed),
// and the audit-friendly rejected -> reviewed recovery.
func TestPrerequisiteUpsertManual(t *testing.T) {
	s, db := setupStoreWithDB(t)
	ctx := context.Background()
	seedPrereqTags(t, db)
	// Unrelated rows that must survive every write below.
	seedPrereqEdge(t, db, 2, 1, "soft", "reverse edge", "machine")
	mustExec(t, db, `INSERT INTO learner_events (user_id, verb, object_type, object_id, occurred_at, created_at)
		VALUES (301, 'completed', 'chore', 1, '2026-01-01 08:00:00', '2026-01-01 08:00:00')`)
	unrelatedBefore := countPrereqRows(t, db)

	t.Run("invalid strength", func(t *testing.T) {
		for _, bad := range []string{"", "HARD", "medium", "weak"} {
			if err := s.UpsertManualPrerequisite(ctx, "cnm_a", "cnm_b", bad, "x"); err == nil {
				t.Fatalf("strength %q: want error, got nil", bad)
			}
		}
		if n := countPrereqRows(t, db); n != unrelatedBefore {
			t.Fatalf("invalid strength wrote rows: %d -> %d", unrelatedBefore, n)
		}
	})

	t.Run("unknown refs", func(t *testing.T) {
		cases := []struct{ topic, prereq string }{
			{"cnm_missing", "cnm_b"},
			{"cnm_a", "cnm_missing"},
			{"cnm_missing", "cnm_missing2"},
			{"cnm_a", "cnm_missing"}, // also not the blank-ref tag
		}
		for _, c := range cases {
			err := s.UpsertManualPrerequisite(ctx, c.topic, c.prereq, "hard", "x")
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("Upsert(%q,%q) = %v, want sql.ErrNoRows", c.topic, c.prereq, err)
			}
		}
		if n := countPrereqRows(t, db); n != unrelatedBefore {
			t.Fatalf("unknown refs wrote rows: %d -> %d", unrelatedBefore, n)
		}
	})

	t.Run("self loop", func(t *testing.T) {
		err := s.UpsertManualPrerequisite(ctx, "cnm_a", "cnm_a", "hard", "loop")
		if err == nil {
			t.Fatal("self loop: want error, got nil")
		}
		if n := countPrereqRows(t, db); n != unrelatedBefore {
			t.Fatalf("self loop wrote rows: %d -> %d", unrelatedBefore, n)
		}
	})

	t.Run("insert reviewed", func(t *testing.T) {
		if err := s.UpsertManualPrerequisite(ctx, "cnm_a", "cnm_b", "hard", "parent says so"); err != nil {
			t.Fatalf("Upsert insert: %v", err)
		}
		strength, reason, status, ok := fetchPrereqRow(t, db, 1, 2)
		if !ok {
			t.Fatal("row (1,2) missing after insert")
		}
		if strength != "hard" || reason != "parent says so" || status != "reviewed" {
			t.Fatalf("inserted row = %q/%q/%q, want hard/parent says so/reviewed", strength, reason, status)
		}
		if n := countPrereqRows(t, db); n != unrelatedBefore+1 {
			t.Fatalf("row count = %d, want %d", n, unrelatedBefore+1)
		}
	})

	t.Run("conflict upsert rewrites and forces reviewed", func(t *testing.T) {
		// Pre-existing machine edge with different payload; upsert must
		// replace strength/reason and flip status without adding a row.
		seedPrereqEdge(t, db, 3, 1, "hard", "machine guess", "machine")
		before := countPrereqRows(t, db)
		if err := s.UpsertManualPrerequisite(ctx, "cnm_c", "cnm_a", "soft", "human override"); err != nil {
			t.Fatalf("Upsert conflict: %v", err)
		}
		strength, reason, status, ok := fetchPrereqRow(t, db, 3, 1)
		if !ok {
			t.Fatal("row (3,1) disappeared on upsert")
		}
		if strength != "soft" || reason != "human override" || status != "reviewed" {
			t.Fatalf("upserted row = %q/%q/%q, want soft/human override/reviewed", strength, reason, status)
		}
		if n := countPrereqRows(t, db); n != before {
			t.Fatalf("conflict upsert changed row count: %d -> %d", before, n)
		}
		// Re-upsert back to hard with empty reason.
		if err := s.UpsertManualPrerequisite(ctx, "cnm_c", "cnm_a", "hard", ""); err != nil {
			t.Fatalf("Upsert re-conflict: %v", err)
		}
		strength, reason, status, _ = fetchPrereqRow(t, db, 3, 1)
		if strength != "hard" || reason != "" || status != "reviewed" {
			t.Fatalf("re-upserted row = %q/%q/%q, want hard/empty/reviewed", strength, reason, status)
		}
	})

	t.Run("rejected row can be upserted back to reviewed", func(t *testing.T) {
		seedPrereqEdge(t, db, 2, 3, "hard", "junk edge", "rejected")
		if err := s.UpsertManualPrerequisite(ctx, "cnm_b", "cnm_c", "soft", "actually fine"); err != nil {
			t.Fatalf("Upsert over rejected: %v", err)
		}
		strength, reason, status, _ := fetchPrereqRow(t, db, 2, 3)
		if strength != "soft" || reason != "actually fine" || status != "reviewed" {
			t.Fatalf("rejected->reviewed row = %q/%q/%q, want soft/actually fine/reviewed", strength, reason, status)
		}
	})

	t.Run("unrelated content untouched", func(t *testing.T) {
		strength, reason, status, ok := fetchPrereqRow(t, db, 2, 1)
		if !ok || strength != "soft" || reason != "reverse edge" || status != "machine" {
			t.Fatalf("reverse edge (2,1) = %q/%q/%q ok=%v, want soft/reverse edge/machine", strength, reason, status, ok)
		}
		var users, events int
		if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
			t.Fatalf("count users: %v", err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM learner_events`).Scan(&events); err != nil {
			t.Fatalf("count learner_events: %v", err)
		}
		if users != 1 || events != 1 {
			t.Fatalf("users=%d learner_events=%d, want 1/1", users, events)
		}
		// Tags themselves unchanged (no ALTER side effects via DAO).
		var tags int
		if err := db.QueryRow(`SELECT COUNT(*) FROM knowledge_tags`).Scan(&tags); err != nil {
			t.Fatalf("count tags: %v", err)
		}
		if tags != 5 {
			t.Fatalf("tags=%d, want 5", tags)
		}
	})
}

// TestPrerequisiteSetReviewStatus covers the toggle: only reviewed/
// rejected accepted, rejected rows are kept, missing edge → (false,nil),
// unknown ref → sql.ErrNoRows, and the listing reflects the flip.
func TestPrerequisiteSetReviewStatus(t *testing.T) {
	s, db := setupStoreWithDB(t)
	ctx := context.Background()
	seedPrereqTags(t, db)
	seedPrereqEdge(t, db, 1, 2, "hard", "A needs B", "machine")
	seedPrereqEdge(t, db, 1, 3, "soft", "A needs C", "machine")
	seedPrereqEdge(t, db, 2, 1, "hard", "B needs A", "reviewed")
	mustExec(t, db, `INSERT INTO learner_events (user_id, verb, object_type, object_id, occurred_at, created_at)
		VALUES (301, 'answered', 'error_item', 9, '2026-01-01 08:00:00', '2026-01-01 08:00:00')`)
	total := countPrereqRows(t, db)

	t.Run("invalid status", func(t *testing.T) {
		for _, bad := range []string{"", "machine", "REJECTED", "approved"} {
			ok, err := s.SetPrerequisiteReviewStatus(ctx, "cnm_a", "cnm_b", bad)
			if err == nil || ok {
				t.Fatalf("status %q: got (%v,%v), want (false,error)", bad, ok, err)
			}
		}
		if _, _, status, _ := fetchPrereqRow(t, db, 1, 2); status != "machine" {
			t.Fatalf("invalid status mutated row: %q", status)
		}
	})

	t.Run("unknown refs", func(t *testing.T) {
		for _, c := range [][2]string{{"cnm_missing", "cnm_b"}, {"cnm_a", "cnm_missing"}} {
			ok, err := s.SetPrerequisiteReviewStatus(ctx, c[0], c[1], "rejected")
			if ok || !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("Set(%q,%q) = (%v,%v), want (false,sql.ErrNoRows)", c[0], c[1], ok, err)
			}
		}
	})

	t.Run("missing edge", func(t *testing.T) {
		// Both refs resolve but no stored edge (3 <- 2 absent).
		ok, err := s.SetPrerequisiteReviewStatus(ctx, "cnm_c", "cnm_b", "rejected")
		if ok || err != nil {
			t.Fatalf("missing edge = (%v,%v), want (false,nil)", ok, err)
		}
		if n := countPrereqRows(t, db); n != total {
			t.Fatalf("missing edge wrote rows: %d -> %d", total, n)
		}
	})

	t.Run("reject reviewed edge keeps row", func(t *testing.T) {
		ok, err := s.SetPrerequisiteReviewStatus(ctx, "cnm_b", "cnm_a", "rejected")
		if err != nil || !ok {
			t.Fatalf("reject = (%v,%v), want (true,nil)", ok, err)
		}
		strength, reason, status, present := fetchPrereqRow(t, db, 2, 1)
		if !present || status != "rejected" || strength != "hard" || reason != "B needs A" {
			t.Fatalf("row = %q/%q/%q present=%v, want kept hard/B needs A/rejected", strength, reason, status, present)
		}
		if n := countPrereqRows(t, db); n != total {
			t.Fatalf("row count = %d, want %d (reject must not delete)", n, total)
		}
		edges, err := s.ListEffectivePrerequisiteEdges(ctx)
		if err != nil {
			t.Fatalf("list after reject: %v", err)
		}
		for _, e := range edges {
			if e.TopicID == "cnm_b" && e.PrerequisiteID == "cnm_a" {
				t.Fatalf("rejected edge still listed: %+v", e)
			}
		}
	})

	t.Run("restore to reviewed", func(t *testing.T) {
		ok, err := s.SetPrerequisiteReviewStatus(ctx, "cnm_b", "cnm_a", "reviewed")
		if err != nil || !ok {
			t.Fatalf("restore = (%v,%v), want (true,nil)", ok, err)
		}
		if _, _, status, _ := fetchPrereqRow(t, db, 2, 1); status != "reviewed" {
			t.Fatalf("status = %q, want reviewed", status)
		}
	})

	t.Run("upserted edge is toggleable", func(t *testing.T) {
		if err := s.UpsertManualPrerequisite(ctx, "cnm_a", "cnm_c", "soft", "manual"); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		ok, err := s.SetPrerequisiteReviewStatus(ctx, "cnm_a", "cnm_c", "rejected")
		if err != nil || !ok {
			t.Fatalf("reject upserted = (%v,%v), want (true,nil)", ok, err)
		}
		if _, _, status, _ := fetchPrereqRow(t, db, 1, 3); status != "rejected" {
			t.Fatalf("upserted edge status = %q, want rejected", status)
		}
	})

	t.Run("unrelated content untouched", func(t *testing.T) {
		// (1,2) was never targeted and must stay machine.
		if _, _, status, _ := fetchPrereqRow(t, db, 1, 2); status != "machine" {
			t.Fatalf("unrelated edge status = %q, want machine", status)
		}
		var users, events, tags int
		for q, dst := range map[string]*int{
			`SELECT COUNT(*) FROM users`:          &users,
			`SELECT COUNT(*) FROM learner_events`: &events,
			`SELECT COUNT(*) FROM knowledge_tags`: &tags,
		} {
			if err := db.QueryRow(q).Scan(dst); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if users != 1 || events != 1 || tags != 5 {
			t.Fatalf("users=%d events=%d tags=%d, want 1/1/5", users, events, tags)
		}
	})
}

// TestPrerequisiteListPerTagReadFilter pins DEF-PREREQ-READ-FILTER on the
// per-tag reads (ListPrerequisites / ListDependents): rejected edges and
// edges with a NULL or blank external_ref on either endpoint must not
// leak, reviewed/machine edges survive with integer ids untouched.
func TestPrerequisiteListPerTagReadFilter(t *testing.T) {
	s, db := setupStoreWithDB(t)
	ctx := context.Background()
	seedPrereqTags(t, db)

	// Tag 1 (cnm_a) as topic: one row of every shape.
	seedPrereqEdge(t, db, 1, 2, "hard", "keep machine", "machine")
	seedPrereqEdge(t, db, 1, 3, "soft", "keep reviewed", "reviewed")
	seedPrereqEdge(t, db, 1, 4, "hard", "", "machine")           // prereq NULL ref
	seedPrereqEdge(t, db, 1, 5, "hard", "drop blank", "machine") // prereq '' ref
	seedPrereqEdge(t, db, 4, 2, "hard", "", "machine")           // topic NULL ref
	seedPrereqEdge(t, db, 5, 2, "hard", "", "reviewed")          // topic '' ref
	seedPrereqEdge(t, db, 4, 5, "hard", "", "machine")           // both ref-less
	// Tag 1 as prerequisite (dependents direction).
	seedPrereqEdge(t, db, 2, 1, "soft", "keep dependent", "machine")
	seedPrereqEdge(t, db, 3, 1, "hard", "rejected dependent", "rejected")
	seedPrereqEdge(t, db, 4, 1, "hard", "", "machine") // topic NULL ref
	seedPrereqEdge(t, db, 5, 1, "hard", "", "machine") // topic '' ref
	seedPrereqEdge(t, db, 1, 1, "hard", "", "rejected")

	check := func(name string, got []model.KnowledgeTagPrerequisite, want [][2]int64) {
		t.Helper()
		if got == nil {
			t.Fatalf("%s: nil slice, want non-nil", name)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: got %d edges %+v, want %d", name, len(got), got, len(want))
		}
		for i, w := range want {
			if got[i].TagID != w[0] || got[i].PrerequisiteID != w[1] {
				t.Fatalf("%s[%d] = (%d,%d), want (%d,%d)", name, i, got[i].TagID, got[i].PrerequisiteID, w[0], w[1])
			}
		}
	}

	prereqs, err := s.ListPrerequisites(ctx, 1)
	if err != nil {
		t.Fatalf("ListPrerequisites(1): %v", err)
	}
	check("ListPrerequisites(1)", prereqs, [][2]int64{{1, 2}, {1, 3}})
	if prereqs[0].Strength != "hard" || prereqs[0].Reason != "keep machine" || prereqs[0].ReviewStatus != "machine" {
		t.Fatalf("prereq[0] = %+v, want machine row", prereqs[0])
	}
	if prereqs[1].Strength != "soft" || prereqs[1].Reason != "keep reviewed" || prereqs[1].ReviewStatus != "reviewed" {
		t.Fatalf("prereq[1] = %+v, want reviewed row", prereqs[1])
	}

	// Ref-less topic still has no eligible edges.
	for _, id := range []int64{4, 5} {
		got, err := s.ListPrerequisites(ctx, id)
		if err != nil {
			t.Fatalf("ListPrerequisites(%d): %v", id, err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("ListPrerequisites(%d) = %+v, want empty non-nil", id, got)
		}
	}

	deps, err := s.ListDependents(ctx, 1)
	if err != nil {
		t.Fatalf("ListDependents(1): %v", err)
	}
	check("ListDependents(1)", deps, [][2]int64{{2, 1}})
	if deps[0].TagID != 2 || deps[0].PrerequisiteID != 1 || deps[0].ReviewStatus != "machine" {
		t.Fatalf("dependent = %+v, want (2,1) machine", deps[0])
	}
}

// TestPrerequisiteBlankRefErrors pins the trimming rule seen from the
// write surface: empty/whitespace refs are never resolved (a blank-ref
// row exists in the fixture and must not be matched) and surface as
// sql.ErrNoRows without touching the table.
func TestPrerequisiteBlankRefErrors(t *testing.T) {
	s, db := setupStoreWithDB(t)
	ctx := context.Background()
	seedPrereqTags(t, db)
	before := countPrereqRows(t, db)
	blanks := []string{"", " ", "\t\n  "}

	for _, b := range blanks {
		cases := [][2]string{{b, "cnm_b"}, {"cnm_a", b}, {b, b}, {"  cnm_a\t", b}}
		for _, c := range cases {
			if err := s.UpsertManualPrerequisite(ctx, c[0], c[1], "hard", "x"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("Upsert(%q,%q) = %v, want sql.ErrNoRows", c[0], c[1], err)
			}
			ok, err := s.SetPrerequisiteReviewStatus(ctx, c[0], c[1], "rejected")
			if ok || !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("Set(%q,%q) = (%v,%v), want (false,sql.ErrNoRows)", c[0], c[1], ok, err)
			}
		}
	}
	if n := countPrereqRows(t, db); n != before {
		t.Fatalf("blank-ref writes changed row count: %d -> %d", before, n)
	}

	// A ref that exists only after trimming still resolves (trim, not drop).
	if err := s.UpsertManualPrerequisite(ctx, " cnm_a ", "\tcnm_b\n", "soft", "trimmed"); err != nil {
		t.Fatalf("Upsert trimmed refs: %v", err)
	}
	strength, _, status, ok := fetchPrereqRow(t, db, 1, 2)
	if !ok || strength != "soft" || status != "reviewed" {
		t.Fatalf("trimmed upsert row = %q/%q ok=%v, want soft/reviewed", strength, status, ok)
	}
}

// TestPrerequisiteRejectedReUpsertReplaceReason pins that re-upserting a
// rejected edge is a re-judgement: payload is replaced (not merged) and
// the rejected one never resurfaces in the list.
func TestPrerequisiteRejectedReUpsertReplaceReason(t *testing.T) {
	s, db := setupStoreWithDB(t)
	ctx := context.Background()
	seedPrereqTags(t, db)
	seedPrereqEdge(t, db, 2, 3, "hard", "old rejected reason", "rejected")

	rejected, err := s.ListPrerequisites(ctx, 2)
	if err != nil {
		t.Fatalf("ListPrerequisites pre-upsert: %v", err)
	}
	if len(rejected) != 0 {
		t.Fatalf("rejected edge listed before re-upsert: %+v", rejected)
	}

	if err := s.UpsertManualPrerequisite(ctx, "cnm_b", "cnm_c", "soft", "new reason"); err != nil {
		t.Fatalf("Upsert over rejected: %v", err)
	}
	strength, reason, status, _ := fetchPrereqRow(t, db, 2, 3)
	if strength != "soft" || reason != "new reason" || status != "reviewed" {
		t.Fatalf("row = %q/%q/%q, want soft/new reason/reviewed", strength, reason, status)
	}
	list, err := s.ListPrerequisites(ctx, 2)
	if err != nil {
		t.Fatalf("ListPrerequisites post-upsert: %v", err)
	}
	if len(list) != 1 || list[0].TagID != 2 || list[0].PrerequisiteID != 3 ||
		list[0].Strength != "soft" || list[0].Reason != "new reason" || list[0].ReviewStatus != "reviewed" {
		t.Fatalf("post-upsert list = %+v, want single soft/new reason/reviewed (2,3)", list)
	}
	// Round-trip reject -> list empty -> restore -> list visible.
	if ok, err := s.SetPrerequisiteReviewStatus(ctx, "cnm_b", "cnm_c", "rejected"); err != nil || !ok {
		t.Fatalf("reject = (%v,%v)", ok, err)
	}
	if list, _ := s.ListEffectivePrerequisiteEdges(ctx); len(list) != 0 {
		t.Fatalf("rejected edge leaked into effective list: %+v", list)
	}
	if ok, err := s.SetPrerequisiteReviewStatus(ctx, "cnm_b", "cnm_c", "reviewed"); err != nil || !ok {
		t.Fatalf("restore = (%v,%v)", ok, err)
	}
	if list, _ := s.ListDependents(ctx, 3); len(list) != 1 || list[0].TagID != 2 {
		t.Fatalf("restored dependent list = %+v, want one (2,3)", list)
	}
}

// Compile-time guard that the tests talk to the exported store type.
var _ = (*store.Store)(nil)
