package api_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// P1: a chore can carry a reference photo ("this is what done looks like")
// that flows through create → update → the kid's scheduled view, and the
// AI reviewer receives it alongside the submission photo.

func TestChoreReferencePhotoCRUD(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")

	// Create with a reference photo.
	resp := env.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Tidy Desk", "category": "core", "points_value": 8,
		"requires_photo": true, "photo_source": "child",
		"reference_photo_url": "/uploads/ref_desk.jpg",
	}, adminHeaders(), http.StatusCreated)
	var chore map[string]any
	decodeBody(t, resp, &chore)
	if chore["reference_photo_url"] != "/uploads/ref_desk.jpg" {
		t.Fatalf("expected reference photo on create, got %+v", chore["reference_photo_url"])
	}

	// Omit the field on update: the reference photo must survive.
	env.expectStatus(t, "PUT", "/api/chores/1", map[string]any{
		"title": "Tidy Desk", "category": "core", "points_value": 9,
	}, adminHeaders(), http.StatusOK)
	resp = env.expectStatus(t, "GET", "/api/chores/1", nil, adminHeaders(), http.StatusOK)
	decodeBody(t, resp, &chore)
	if chore["reference_photo_url"] != "/uploads/ref_desk.jpg" {
		t.Fatalf("expected reference photo preserved when omitted on update, got %+v", chore["reference_photo_url"])
	}

	// Explicit empty string clears it.
	env.expectStatus(t, "PUT", "/api/chores/1", map[string]any{
		"title": "Tidy Desk", "category": "core",
		"reference_photo_url": "",
	}, adminHeaders(), http.StatusOK)
	resp = env.expectStatus(t, "GET", "/api/chores/1", nil, adminHeaders(), http.StatusOK)
	decodeBody(t, resp, &chore)
	if chore["reference_photo_url"] != "" {
		t.Fatalf("expected reference photo cleared, got %+v", chore["reference_photo_url"])
	}

	// Set it again and schedule the chore for today.
	env.expectStatus(t, "PUT", "/api/chores/1", map[string]any{
		"title": "Tidy Desk", "category": "core",
		"reference_photo_url": "/uploads/ref_desk.jpg",
	}, adminHeaders(), http.StatusOK)
	env.expectStatus(t, "POST", "/api/chores/1/schedules", map[string]any{
		"assigned_to": kidID,
		"day_of_week": int(time.Now().Weekday()),
	}, adminHeaders(), http.StatusCreated)

	// The kid's daily view carries the reference photo so the UI can show
	// the example before they shoot their completion photo.
	resp = env.expectStatus(t, "GET", fmt.Sprintf("/api/users/%d/chores?view=daily", kidID), nil, childHeaders(kidID), http.StatusOK)
	var chores []map[string]any
	decodeBody(t, resp, &chores)
	if len(chores) != 1 || chores[0]["reference_photo_url"] != "/uploads/ref_desk.jpg" {
		t.Fatalf("expected the scheduled chore to expose the reference photo, got %+v", chores)
	}
}

func TestCreateChoreDefaultsReferencePhotoEmpty(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)

	resp := env.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Dishes", "category": "core", "points_value": 5,
	}, adminHeaders(), http.StatusCreated)
	var chore map[string]any
	decodeBody(t, resp, &chore)
	if chore["reference_photo_url"] != "" {
		t.Fatalf("expected empty default reference photo, got %+v", chore["reference_photo_url"])
	}
}

// The scheduled-chores payload for a kid must carry the reference photo so
// PhotoSheet can show "this is what done looks like" before the shot.
func TestScheduledChoreIncludesReferencePhoto(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")

	env.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Make Bed", "category": "required", "points_value": 6,
		"requires_photo": true, "photo_source": "child",
		"reference_photo_url": "/uploads/ref_bed.jpg",
	}, adminHeaders(), http.StatusCreated)
	env.expectStatus(t, "POST", "/api/chores/1/schedules", map[string]any{
		"assigned_to": kidID,
		"day_of_week": int(time.Now().Weekday()),
	}, adminHeaders(), http.StatusCreated)

	today := time.Now().Format(model.DateFormat)
	sc, err := env.store.GetScheduledChoresForUser(t.Context(), int64(kidID), []string{today}, time.Now())
	if err != nil || len(sc) != 1 {
		t.Fatalf("expected one scheduled chore, got %v (%v)", sc, err)
	}
	if sc[0].ReferencePhotoURL != "/uploads/ref_bed.jpg" {
		t.Fatalf("expected reference photo in scheduled chore, got %q", sc[0].ReferencePhotoURL)
	}
}


// The pending-approvals list carries the chore's reference photo so the
// parent card can show the before/after pair.
func TestPendingCompletionCarriesReferencePhoto(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")

	env.expectStatus(t, "POST", "/api/chores", map[string]any{
		"title": "Fold Laundry", "category": "core", "points_value": 7,
		"requires_photo": true, "photo_source": "child", "requires_approval": true,
		"reference_photo_url": "/uploads/ref_laundry.jpg",
	}, adminHeaders(), http.StatusCreated)
	env.expectStatus(t, "POST", "/api/chores/1/schedules", map[string]any{
		"assigned_to": kidID,
		"day_of_week": int(time.Now().Weekday()),
	}, adminHeaders(), http.StatusCreated)

	env.expectStatus(t, "POST", "/api/schedules/1/complete", map[string]any{
		"completed_by":    kidID,
		"completion_date": time.Now().Format(model.DateFormat),
		"photo_url":       "/uploads/proof.jpg",
	}, childHeaders(kidID), http.StatusCreated)

	resp := env.expectStatus(t, "GET", "/api/completions/pending", nil, adminHeaders(), http.StatusOK)
	var pending []map[string]any
	decodeBody(t, resp, &pending)
	if len(pending) != 1 {
		t.Fatalf("expected one pending completion, got %d", len(pending))
	}
	if pending[0]["reference_photo_url"] != "/uploads/ref_laundry.jpg" {
		t.Fatalf("expected reference photo in pending row, got %+v", pending[0]["reference_photo_url"])
	}
}
