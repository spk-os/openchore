package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// P4-15: the "bonus drop" — with bonus_drop_enabled on, completing a
// chore can roll 1-5 surprise points on top of the regular credit, as
// its own ledger row (reason='bonus_drop'). With the setting off (the
// default) nothing ever drops.

// enableBonusDrop flips the bonus_drop_enabled setting on.
func enableBonusDrop(t *testing.T, env *testEnv) {
	t.Helper()
	env.expectStatus(t, "PUT", "/api/admin/settings/bonus_drop_enabled",
		map[string]any{"value": "true"}, adminHeaders(), http.StatusOK)
}

// completeOnce creates a fresh chore+schedule for the kid (one point each
// so the drop dominates the balance) and ticks it off for today, returning
// the completion response.
func completeOnce(t *testing.T, env *testEnv, kidID int, headers map[string]string) map[string]any {
	t.Helper()
	name := fmt.Sprintf("Drop chore %d", time.Now().UnixNano())
	env.expectStatus(t, "POST", "/api/chores",
		map[string]any{"title": name, "category": "required", "points_value": 1},
		adminHeaders(), http.StatusCreated)
	resp := env.expectStatus(t, "POST", "/api/chores/1/schedules",
		map[string]any{"assigned_to": kidID, "day_of_week": int(time.Now().Weekday())},
		adminHeaders(), http.StatusCreated)
	var sched map[string]any
	decodeBody(t, resp, &sched)
	scheduleID := int64(sched["id"].(float64))
	resp = env.expectStatus(t, "POST",
		fmt.Sprintf("/api/schedules/%d/complete", scheduleID),
		map[string]any{"completion_date": time.Now().Format(model.DateFormat)},
		headers, http.StatusCreated)
	var out map[string]any
	decodeBody(t, resp, &out)
	return out
}

func TestBonusDropNeverFiresWhenDisabled(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	// bonus_drop_enabled left off (the default).

	drops := 0
	for i := 0; i < 10; i++ {
		out := completeOnce(t, env, kidID, childHeaders(kidID))
		if d, ok := out["bonus_drop"].(float64); ok && d > 0 {
			drops++
		}
	}
	if drops != 0 {
		t.Fatalf("no drop may fire while the layer is off, saw %d", drops)
	}
	// Only the plain chore credits landed: 10 chores x 1 point.
	tx, err := env.store.ListPointTransactions(context.Background(), int64(kidID), 100)
	if err != nil {
		t.Fatalf("listing transactions: %v", err)
	}
	sum := 0
	for _, x := range tx {
		if x.Reason == model.ReasonBonusDrop {
			t.Fatalf("unexpected bonus_drop row while disabled: %+v", x)
		}
		sum += x.Amount
	}
	if sum != 10 {
		t.Fatalf("ledger sum = %d, want 10 (ten 1-point chores)", sum)
	}
}

func TestBonusDropRidesTheCompletionWhenEnabled(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidID := env.createChild(t, "Kid")
	enableBonusDrop(t, env)

	// Keep completing chores until the 10% roll lands — bounded, so a
	// statistical fluke cannot hang the suite (60 tries ≈ 1 - 0.9^60).
	var dropped map[string]any
	for i := 0; i < 60 && dropped == nil; i++ {
		out := completeOnce(t, env, kidID, childHeaders(kidID))
		if d, ok := out["bonus_drop"].(float64); ok && d >= 1 && d <= 5 {
			dropped = out
		}
	}
	if dropped == nil {
		t.Fatal("expected at least one bonus drop in 60 tries")
	}

	// The drop is on the ledger as its own row next to the credit.
	tx, err := env.store.ListPointTransactions(context.Background(), int64(kidID), 200)
	if err != nil {
		t.Fatalf("listing transactions: %v", err)
	}
	dropRows := 0
	for _, x := range tx {
		if x.Reason == model.ReasonBonusDrop {
			if x.Amount < 1 || x.Amount > 5 {
				t.Fatalf("drop out of range: %+v", x)
			}
			if !strings.Contains(x.Note, "Bonus") {
				t.Fatalf("drop note = %q", x.Note)
			}
			dropRows++
		}
	}
	if dropRows == 0 {
		t.Fatalf("expected a bonus_drop ledger row, got %+v", tx)
	}

	// And the balance reflects credit + drop together.
	resp := env.expectStatus(t, "GET", fmt.Sprintf("/api/users/%d/points", kidID), nil, childHeaders(kidID), http.StatusOK)
	var pts map[string]any
	decodeBody(t, resp, &pts)
	sum := 0
	for _, x := range tx {
		sum += x.Amount
	}
	if int(pts["balance"].(float64)) != sum {
		t.Fatalf("balance = %v, ledger says %d", pts["balance"], sum)
	}
}
