package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The kid's pet (P4-13): one named companion per kid, fed with points
// through the ledger, grown through cumulative feeding, never dying.
// Gated by the pet_enabled app setting, off by default.

// enablePets flips the pet_enabled setting on.
func enablePets(t *testing.T, env *testEnv) {
	t.Helper()
	env.expectStatus(t, "PUT", "/api/admin/settings/pet_enabled",
		map[string]any{"value": "true"}, adminHeaders(), http.StatusOK)
}

// hatch hatches the kid's pet and returns its state.
func hatch(t *testing.T, env *testEnv, headers map[string]string, name string) map[string]any {
	t.Helper()
	resp := env.expectStatus(t, "POST", "/api/pet/hatch",
		map[string]any{"name": name}, headers, http.StatusCreated)
	var pet map[string]any
	decodeBody(t, resp, &pet)
	return pet
}

// feed feeds the pet, expecting the given status.
func feed(t *testing.T, env *testEnv, headers map[string]string, expected int) map[string]any {
	t.Helper()
	resp := env.expectStatus(t, "POST", "/api/pet/feed", map[string]any{}, headers, expected)
	if expected != http.StatusOK {
		return nil
	}
	var pet map[string]any
	decodeBody(t, resp, &pet)
	return pet
}

func TestPetDisabledByDefault(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")

	// Off by default: the whole layer 404s so clients hide it.
	env.expectStatus(t, "GET", "/api/pet", nil, childHeaders(kid), http.StatusNotFound)
	env.expectStatus(t, "POST", "/api/pet/hatch", map[string]any{"name": "豆豆"}, childHeaders(kid), http.StatusNotFound)
	env.expectStatus(t, "POST", "/api/pet/feed", map[string]any{}, childHeaders(kid), http.StatusNotFound)
}

func TestPetLifecycleHatchFeedGrow(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	enablePets(t, env)

	// Before hatching: hatched=false, no pet row.
	resp := env.expectStatus(t, "GET", "/api/pet", nil, childHeaders(kid), http.StatusOK)
	var state map[string]any
	decodeBody(t, resp, &state)
	if state["hatched"] != false {
		t.Fatalf("expected hatched=false before hatching, got %v", state)
	}

	// Name validation.
	env.expectStatus(t, "POST", "/api/pet/hatch", map[string]any{"name": ""}, childHeaders(kid), http.StatusBadRequest)
	env.expectStatus(t, "POST", "/api/pet/hatch",
		map[string]any{"name": strings.Repeat("豆", 25)}, childHeaders(kid), http.StatusBadRequest)

	// Hatch: stage 1, fed_value 0, mood computed from the (empty) record.
	pet := hatch(t, env, childHeaders(kid), "豆豆")
	if pet["name"] != "豆豆" || pet["stage"] != float64(1) || pet["fed_value"] != float64(0) {
		t.Fatalf("unexpected hatched state: %v", pet)
	}
	if pet["mood"] != "happy" {
		t.Fatalf("expected happy mood with nothing scheduled, got %v", pet["mood"])
	}

	// No pet, no points: feeding must refuse before debiting anything.
	feed(t, env, childHeaders(kid), http.StatusUnprocessableEntity)
	if b := env.balance(t, kid); b != 0 {
		t.Fatalf("balance changed on refused feed: %d", b)
	}

	// Give the kid points, then feed: ledger debit + fed_value bump.
	env.expectStatus(t, "POST", "/api/points/adjust",
		map[string]any{"user_id": kid, "amount": 25, "note": "seed"}, adminHeaders(), http.StatusNoContent)
	pet = feed(t, env, childHeaders(kid), http.StatusOK)
	if pet["fed_value"] != float64(10) {
		t.Fatalf("fed_value = %v, want 10", pet["fed_value"])
	}
	if b := env.balance(t, kid); b != 15 {
		t.Fatalf("balance after feed = %d, want 15", b)
	}

	// The feeding is a pet_feed ledger row, not an adjustment.
	resp = env.expectStatus(t, "GET", fmt.Sprintf("/api/users/%d/points", kid), nil, nil, http.StatusOK)
	var pts map[string]any
	decodeBody(t, resp, &pts)
	txs := pts["transactions"].([]any)
	found := false
	for _, txRaw := range txs {
		tx := txRaw.(map[string]any)
		if tx["reason"] == "pet_feed" && tx["amount"] == float64(-10) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no pet_feed ledger row found in %v", txs)
	}
}

func TestPetStageGrowthAtThresholds(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	enablePets(t, env)
	hatch(t, env, childHeaders(kid), "豆豆")

	// Enough points for ten feedings: 10 × 10 = 100 fed_value → stage 2.
	env.expectStatus(t, "POST", "/api/points/adjust",
		map[string]any{"user_id": kid, "amount": 200, "note": "seed"}, adminHeaders(), http.StatusNoContent)

	var pet map[string]any
	for i := 0; i < 10; i++ {
		pet = feed(t, env, childHeaders(kid), http.StatusOK)
	}
	if pet["stage"] != float64(2) {
		t.Fatalf("stage after 100 fed_value = %v, want 2", pet["stage"])
	}
	if pet["next_stage_at"] != float64(300) {
		t.Fatalf("next_stage_at = %v, want 300", pet["next_stage_at"])
	}
	if p := int(pet["stage_progress"].(float64)); p != 0 {
		t.Fatalf("stage_progress = %d, want 0", p)
	}
}

func TestPetMoodFollowsChoreRecord(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	enablePets(t, env)

	// A required chore every day, uncompleted: today is incomplete → calm.
	makeBedSched := env.createScheduledChore(t, "Make bed", kid, map[string]any{"category": "required"})
	pet := hatch(t, env, childHeaders(kid), "豆豆")
	if pet["mood"] != "calm" {
		t.Fatalf("mood with today's required chore open = %v, want calm", pet["mood"])
	}

	// Complete the open one → happy. (pending counts as done: the pet
	// trusts the kid even before a parent signs off.)
	env.expectStatus(t, "POST", fmt.Sprintf("/api/schedules/%d/complete", makeBedSched),
		map[string]any{}, childHeaders(kid), http.StatusCreated)
	resp := env.expectStatus(t, "GET", "/api/pet", nil, childHeaders(kid), http.StatusOK)
	decodeBody(t, resp, &pet)
	if pet["mood"] != "happy" {
		t.Fatalf("mood after completing everything = %v, want happy", pet["mood"])
	}
}

// The parent status card reads any kid's pet through the admin endpoint
// (the prototype's "stat-pet" line: "豆豆 · 开心"). Same payload as the
// kid's own /pet, gated the same way by pet_enabled.
func TestAdminPetEndpointForKidsStatus(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	other := env.createChild(t, "Other")

	// Feature off → 404, so the status card hides the line.
	env.expectStatus(t, "GET", fmt.Sprintf("/api/admin/users/%d/pet", kid), nil, adminHeaders(), http.StatusNotFound)

	enablePets(t, env)
	// Not hatched yet → hatched=false.
	resp := env.expectStatus(t, "GET", fmt.Sprintf("/api/admin/users/%d/pet", kid), nil, adminHeaders(), http.StatusOK)
	var state map[string]any
	decodeBody(t, resp, &state)
	if state["hatched"] != false {
		t.Fatalf("expected hatched=false, got %v", state)
	}

	// The kid hatches; the admin view mirrors it, mood included.
	hatch(t, env, childHeaders(kid), "豆豆")
	resp = env.expectStatus(t, "GET", fmt.Sprintf("/api/admin/users/%d/pet", kid), nil, adminHeaders(), http.StatusOK)
	decodeBody(t, resp, &state)
	if state["name"] != "豆豆" || state["mood"] != "happy" {
		t.Fatalf("unexpected admin pet state: %v", state)
	}

	// Another kid's empty pet stays hatched=false.
	resp = env.expectStatus(t, "GET", fmt.Sprintf("/api/admin/users/%d/pet", other), nil, adminHeaders(), http.StatusOK)
	decodeBody(t, resp, &state)
	if state["hatched"] != false {
		t.Fatalf("expected hatched=false for other kid, got %v", state)
	}

	// A non-admin session must not read someone else's pet.
	env.expectStatus(t, "GET", fmt.Sprintf("/api/admin/users/%d/pet", kid), nil, childHeaders(other), http.StatusForbidden)
}
