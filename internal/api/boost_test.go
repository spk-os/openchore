package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/liftedkilt/openchore/internal/model"
)

// P4-16: grandparent boost links. A grown-up mints a token-addressed
// read-only page per kid; the elder opens it with no account (the token
// is the auth), sees a curated snapshot and taps a capped boost button.
// The boost lands as reason='grandparent_boost' with the link as
// reference_id — every gift auditable, every cap enforced.

// createBoostLink mints a link for a kid and returns its JSON.
func createBoostLink(t *testing.T, env *testEnv, kidID int, label string) map[string]any {
	t.Helper()
	resp := env.expectStatus(t, "POST", fmt.Sprintf("/api/admin/users/%d/boost-links", kidID),
		map[string]any{"label": label}, adminHeaders(), http.StatusCreated)
	var link map[string]any
	decodeBody(t, resp, &link)
	return link
}

// boostToken digs the token out of a link row — CreateLink returns it
// once for the parent to share; ListLinks keeps it private.
func boostToken(t *testing.T, link map[string]any) string {
	t.Helper()
	if tok, ok := link["token"].(string); ok && tok != "" {
		return tok
	}
	t.Fatalf("link has no token: %v", link)
	return ""
}

// TestBoostLinkLifecycle walks the whole path: mint, read the elder's
// page, boost, watch the ledger row appear with the link as its
// reference, then revoke and watch the token die.
func TestBoostLinkLifecycle(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Mia")

	link := createBoostLink(t, env, kidID, "Grandma")
	if link["label"] != "Grandma" {
		t.Fatalf("expected label Grandma, got %v", link["label"])
	}
	token := boostToken(t, link)

	// The elder's page: public, token-addressed, read-only.
	resp := env.expectStatus(t, "GET", "/api/boost/"+token, nil, nil, http.StatusOK)
	var snap map[string]any
	decodeBody(t, resp, &snap)
	if snap["kid_name"] != "Mia" {
		t.Fatalf("expected kid_name Mia, got %v", snap["kid_name"])
	}
	if snap["label"] != "Grandma" {
		t.Fatalf("expected label Grandma, got %v", snap["label"])
	}
	amounts, _ := snap["amounts"].([]any)
	if len(amounts) != 3 {
		t.Fatalf("expected 3 boost tiers, got %v", amounts)
	}

	// Boosting is one POST — the response is the refreshed snapshot.
	resp = env.expectStatus(t, "POST", "/api/boost/"+token,
		map[string]any{"amount": 10}, nil, http.StatusCreated)
	decodeBody(t, resp, &snap)
	if snap["granted_this_month"] != float64(10) {
		t.Fatalf("expected granted 10, got %v", snap["granted_this_month"])
	}

	// The ledger keeps the gift auditable: reason, link reference, label note.
	var amount int
	var reason, note string
	var refID int64
	err := env.db.QueryRow(
		`SELECT amount, reason, reference_id, note FROM point_transactions
		 WHERE user_id = ? AND reason = ?`, kidID, model.ReasonGrandparentBoost,
	).Scan(&amount, &reason, &refID, &note)
	if err != nil {
		t.Fatalf("no boost ledger row: %v", err)
	}
	if amount != 10 || refID != int64(link["id"].(float64)) || note != "Boost: Grandma" {
		t.Fatalf("ledger row mismatch: amount=%d ref=%d note=%q", amount, refID, note)
	}

	// The kid's balance grew by the boost.
	var balance int
	if err := env.db.QueryRow(
		`SELECT COALESCE(SUM(amount), 0) FROM point_transactions WHERE user_id = ?`, kidID,
	).Scan(&balance); err != nil {
		t.Fatalf("balance query: %v", err)
	}
	if balance != 10 {
		t.Fatalf("expected balance 10, got %d", balance)
	}

	// Revoking kills the token — both reads and boosts answer 404.
	env.expectStatus(t, "DELETE",
		fmt.Sprintf("/api/admin/users/%d/boost-links/%v", kidID, link["id"]),
		nil, adminHeaders(), http.StatusOK)
	env.expectStatus(t, "GET", "/api/boost/"+token, nil, nil, http.StatusNotFound)
	env.expectStatus(t, "POST", "/api/boost/"+token,
		map[string]any{"amount": 5}, nil, http.StatusNotFound)
}

// TestBoostCapsHold enforces the two rate limits: at most daily_limit
// boosts a day, and at most monthly_cap points a month. Both answer 422
// with a phrase the elder's page can show.
func TestBoostCapsHold(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Noa")

	link := createBoostLink(t, env, kidID, "Opa")
	token := boostToken(t, link)

	// Daily limit 3: three 5-point boosts pass, the fourth is refused.
	for i := 0; i < 3; i++ {
		env.expectStatus(t, "POST", "/api/boost/"+token,
			map[string]any{"amount": 5}, nil, http.StatusCreated)
	}
	resp := env.expectStatus(t, "POST", "/api/boost/"+token,
		map[string]any{"amount": 5}, nil, http.StatusUnprocessableEntity)
	var body map[string]any
	decodeBody(t, resp, &body)
	if body["error"] != "daily boost limit reached" {
		t.Fatalf("expected daily limit error, got %v", body["error"])
	}

	// A fresh link for the same kid shares nothing with the old one.
	link2 := createBoostLink(t, env, kidID, "Oma")
	token2 := boostToken(t, link2)

	// Monthly cap 60: 5+5+5 (link 1) + 3×20 (link 2) = 70 > 60. The
	// third 20 would tip the kid's total over the cap — but the cap is
	// per link, so all three pass and the monthly cap check needs the
	// cap on a single link. Give link 2 enough to hit its own cap:
	// 3 boosts of 20 = 60 fills it exactly.
	for i := 0; i < 3; i++ {
		env.expectStatus(t, "POST", "/api/boost/"+token2,
			map[string]any{"amount": 20}, nil, http.StatusCreated)
	}
	resp = env.expectStatus(t, "POST", "/api/boost/"+token2,
		map[string]any{"amount": 5}, nil, http.StatusUnprocessableEntity)
	decodeBody(t, resp, &body)
	if body["error"] != "monthly boost cap reached" {
		t.Fatalf("expected monthly cap error, got %v", body["error"])
	}
}

// TestBoostAmountValidation refuses amounts outside the three tiers.
func TestBoostAmountValidation(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Iris")

	link := createBoostLink(t, env, kidID, "")
	token := boostToken(t, link)

	env.expectStatus(t, "POST", "/api/boost/"+token,
		map[string]any{"amount": 7}, nil, http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/boost/"+token,
		map[string]any{"amount": 0}, nil, http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/boost/"+token,
		map[string]any{"amount": -5}, nil, http.StatusBadRequest)
	// No ledger rows were written by the refused boosts.
	var count int
	if err := env.db.QueryRow(
		`SELECT COUNT(*) FROM point_transactions WHERE reason = ?`,
		model.ReasonGrandparentBoost).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no boost rows, got %d", count)
	}
}

// TestBoostLinkListingAndPrivacy checks the management view: the parent
// lists a kid's links; non-admins can't reach it; the created token is
// returned once at creation and never through the listing.
func TestBoostLinkListingAndPrivacy(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Lev")

	createBoostLink(t, env, kidID, "Grandpa")

	resp := env.expectStatus(t, "GET",
		fmt.Sprintf("/api/admin/users/%d/boost-links", kidID), nil, adminHeaders(), http.StatusOK)
	var links []map[string]any
	decodeBody(t, resp, &links)
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
	if links[0]["label"] != "Grandpa" {
		t.Fatalf("expected label Grandpa, got %v", links[0]["label"])
	}
	// The listing must not leak the token — sharing is create-time only.
	if tok, ok := links[0]["token"]; ok && tok != "" {
		t.Fatalf("listing leaked the token: %v", tok)
	}

	// Non-admins see nothing.
	env.expectStatus(t, "GET",
		fmt.Sprintf("/api/admin/users/%d/boost-links", kidID), nil, childHeaders(kidID), http.StatusForbidden)

	// Unknown tokens answer 404 without leaking anything.
	env.expectStatus(t, "GET", "/api/boost/nonexistent-token", nil, nil, http.StatusNotFound)
}

// TestBoostTokenIsLongAndRandom pins the token's shape: 24 random bytes
// hex-encoded, long enough that guessing is hopeless.
func TestBoostTokenIsLongAndRandom(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Ada")

	link := createBoostLink(t, env, kidID, "Nana")
	token := boostToken(t, link)
	if len(token) != 48 {
		t.Fatalf("expected 48-char token (24 bytes hex), got %d chars: %q", len(token), token)
	}

	link2 := createBoostLink(t, env, kidID, "Nana")
	if boostToken(t, link2) == token {
		t.Fatalf("two links share a token")
	}
}

// TestBoostUnknownLinkChecks that a link for a deleted kid is refused
// cleanly (the page can't render a snapshot for nobody).
func TestBoostSnapshotForDeletedKid(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Eli")

	link := createBoostLink(t, env, kidID, "Bubbe")
	token := boostToken(t, link)

	// Delete the kid (cascades to the link row via FK).
	env.expectStatus(t, "DELETE", fmt.Sprintf("/api/users/%d", kidID),
		nil, adminHeaders(), http.StatusNoContent)

	env.expectStatus(t, "GET", "/api/boost/"+token, nil, nil, http.StatusNotFound)
	env.expectStatus(t, "POST", "/api/boost/"+token,
		map[string]any{"amount": 5}, nil, http.StatusNotFound)
}

var _ = json.Marshal // keep encoding/json import when helpers change
