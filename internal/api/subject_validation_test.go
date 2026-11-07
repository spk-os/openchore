package api_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/liftedkilt/openchore/internal/api"
)

// 036/042: chores.subject — the vocabulary is table-driven (subjects 表的
// enabled 行), the same word list the media library and error book share.
// Create must reject junk on the same terms as Update (a POST with
// subject="history" used to slip straight into the DB), the round-trip must
// preserve the subject through read-back, and seeding the word list after
// the handler was built must be picked up (the cache loads on first use).

// seedSubjects writes the ten-subject word list the way the production
// startup (taxonomy pack import) does. sb_code stays empty except where
// verified; only key/sort_order/enabled matter to the vocabulary check.
func seedSubjects(t *testing.T, e *testEnv) {
	t.Helper()
	h := api.NewTaxonomyHandler(e.store, nil)
	if _, err := h.ImportPack(t.Context()); err != nil {
		t.Fatalf("seeding subjects failed: %v", err)
	}
}

func TestChoreSubjectValidation(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	seedSubjects(t, e)

	// Create with a valid subject stores it.
	resp := e.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Math homework", "category": "core", "subject": "math",
	}, adminHeaders(), http.StatusCreated)
	var chore map[string]any
	decodeBody(t, resp, &chore)
	if chore["subject"] != "math" {
		t.Fatalf("created subject = %v, want math", chore["subject"])
	}
	id := int(chore["id"].(float64))

	// Every one of the ten seeded subjects passes (042 widened the five).
	for _, subject := range []string{"chinese", "english", "physics", "chemistry",
		"science", "morality", "history", "geography", "biology"} {
		resp := e.expectStatus(t, "POST", "/api/chores", map[string]any{
			"title": fmt.Sprintf("%s homework", subject), "category": "core", "subject": subject,
		}, adminHeaders(), http.StatusCreated)
		var c map[string]any
		decodeBody(t, resp, &c)
		if c["subject"] != subject {
			t.Fatalf("created subject = %v, want %s", c["subject"], subject)
		}
	}

	// Create without a subject stays "".
	resp = e.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Dishes", "category": "core",
	}, adminHeaders(), http.StatusCreated)
	var plain map[string]any
	decodeBody(t, resp, &plain)
	if plain["subject"] != "" {
		t.Fatalf("omitted subject = %v, want empty", plain["subject"])
	}

	// Update to another valid value works.
	resp = e.expectStatus(t, "PUT", fmt.Sprintf("/api/chores/%d", id), map[string]any{
		"subject": "chinese",
	}, adminHeaders(), http.StatusOK)
	decodeBody(t, resp, &chore)
	if chore["subject"] != "chinese" {
		t.Fatalf("updated subject = %v, want chinese", chore["subject"])
	}

	// Update to junk is still rejected (outside the seeded vocabulary).
	e.expectStatus(t, "PUT", fmt.Sprintf("/api/chores/%d", id), map[string]any{
		"subject": "gym",
	}, adminHeaders(), http.StatusBadRequest)

	// Update with an explicit empty string clears it.
	resp = e.expectStatus(t, "PUT", fmt.Sprintf("/api/chores/%d", id), map[string]any{
		"subject": "",
	}, adminHeaders(), http.StatusOK)
	decodeBody(t, resp, &chore)
	if chore["subject"] != "" {
		t.Fatalf("cleared subject = %v, want empty", chore["subject"])
	}

	// Omitted subject on update keeps the current value.
	resp = e.expectStatus(t, "PUT", fmt.Sprintf("/api/chores/%d", id), map[string]any{
		"description": "just a note",
	}, adminHeaders(), http.StatusOK)
	decodeBody(t, resp, &chore)
	if chore["subject"] != "" {
		t.Fatalf("subject after omitted update = %v, want empty (kept)", chore["subject"])
	}

	// The DB row agrees with the API (the media library reads this column).
	var dbSubject string
	e.db.QueryRow(`SELECT subject FROM chores WHERE id = ?`, id).Scan(&dbSubject)
	if dbSubject != "" {
		t.Fatalf("db subject = %q, want empty", dbSubject)
	}
}

// TestChoreSubjectDisabled pins the enabled flag: a subject switched off
// in the table is no longer a valid value for new/updated chores, while
// a pre-existing chore keeps its stored subject (the vocabulary gates
// writes, it doesn't rewrite history).
func TestChoreSubjectDisabled(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	seedSubjects(t, e)

	// A chore lands with a subject that is about to be switched off.
	resp := e.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Math homework", "category": "core", "subject": "math",
	}, adminHeaders(), http.StatusCreated)
	var chore map[string]any
	decodeBody(t, resp, &chore)
	id := int(chore["id"].(float64))

	e.mustExec(t, `UPDATE subjects SET enabled = 0 WHERE key = 'math'`)

	// The rest of the ten stay valid on this handler too (its cache holds
	// the pre-update snapshot; english was never flipped).
	e.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "English homework", "category": "core", "subject": "english",
	}, adminHeaders(), http.StatusCreated)
	// Empty is always allowed (household chores).
	e.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Dishes", "category": "core",
	}, adminHeaders(), http.StatusCreated)

	// A fresh handler on the same DB (restart semantics) rejects the
	// disabled value: SeedSubjects upserts enabled=1 back on conflict, so
	// only flip the one row again after re-seeding the fresh handler's
	// table state.
	e2 := setupTest(t)
	e2.createAdmin(t)
	seedSubjects(t, e2)
	e2.mustExec(t, `UPDATE subjects SET enabled = 0 WHERE key = 'math'`)
	e2.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Math homework", "category": "core", "subject": "math",
	}, adminHeaders(), http.StatusBadRequest)
	// The rest of the ten stay valid.
	e2.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "English homework", "category": "core", "subject": "english",
	}, adminHeaders(), http.StatusCreated)
	// Empty is always allowed (household chores).
	e2.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Dishes", "category": "core",
	}, adminHeaders(), http.StatusCreated)
	// Update to a disabled subject is rejected the same way.
	e2.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Seed", "category": "core",
	}, adminHeaders(), http.StatusCreated)
	e2.expectStatus(t, "PUT", "/api/chores/1", map[string]any{
		"subject": "math",
	}, adminHeaders(), http.StatusBadRequest)

	// An all-disabled table: nothing in the enabled vocabulary, so every
	// non-empty subject is rejected and the empty set is never cached
	// (the next enabled write must still pass through on the same
	// handler).
	e3 := setupTest(t)
	e3.createAdmin(t)
	seedSubjects(t, e3)
	e3.mustExec(t, `UPDATE subjects SET enabled = 0`)
	e3.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Math homework", "category": "core", "subject": "math",
	}, adminHeaders(), http.StatusBadRequest)
	e3.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "English homework", "category": "core", "subject": "english",
	}, adminHeaders(), http.StatusBadRequest)
	// Empty stays allowed even with the whole vocabulary disabled.
	e3.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Dishes", "category": "core",
	}, adminHeaders(), http.StatusCreated)

	// Re-enable one subject only: the previously rejected value passes on
	// the same handler (an all-disabled table was never cached).
	e3.mustExec(t, `UPDATE subjects SET enabled = 1 WHERE key = 'english'`)
	e3.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "English homework", "category": "core", "subject": "english",
	}, adminHeaders(), http.StatusCreated)
	e3.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Math homework", "category": "core", "subject": "math",
	}, adminHeaders(), http.StatusBadRequest)

	// The pre-existing chore is untouched by the flip: its row still reads
	// math and an empty-subject update still goes through.
	var dbSubject string
	e.db.QueryRow(`SELECT subject FROM chores WHERE id = ?`, id).Scan(&dbSubject)
	if dbSubject != "math" {
		t.Fatalf("db subject = %q, want math (stored rows keep their subject)", dbSubject)
	}
	e.expectStatus(t, "PUT", fmt.Sprintf("/api/chores/%d", id), map[string]any{
		"description": "note",
	}, adminHeaders(), http.StatusOK)
}

// TestChoreSubjectSeededAfterStart: the vocabulary cache is lazy — a
// handler built before the word list exists picks the table up on its
// first write, so the startup order (router first, pack import after)
// never rejects valid subjects with 500s.
func TestChoreSubjectSeededAfterStart(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)

	// A junk subject against the still-empty table is a clean 400 (the
	// seeded word list is the vocabulary, not the hard-coded ten).
	e.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "x", "category": "core", "subject": "gym",
	}, adminHeaders(), http.StatusBadRequest)

	seedSubjects(t, e)

	// After seeding, math validates — same handler, no rebuild.
	e.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Math homework", "category": "core", "subject": "math",
	}, adminHeaders(), http.StatusCreated)
}
