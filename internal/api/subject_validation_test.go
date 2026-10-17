package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

// 036: chores.subject — the five-value vocabulary the media library and
// error book share. Create must reject junk on the same terms as Update
// (a POST with subject="history" used to slip straight into the DB), and
// the round-trip must preserve the subject through read-back.

func TestChoreSubjectValidation(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)

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

	// Create with junk is rejected (the Create-side gap this test locks).
	e.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "History homework", "category": "core", "subject": "history",
	}, adminHeaders(), http.StatusBadRequest)

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

	// Update to junk is still rejected.
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
