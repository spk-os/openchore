package api_test

// FR2-A: fingerprint-gated pack sync semantics. Real SQLite + the real
// embedded pack — the skip must be observable without re-parsing the
// catalog, so the skip path is proven by timing plus the handler's
// catalog staying nil (an un-skipped import would have loaded it).

import (
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/api"
	"github.com/liftedkilt/openchore/internal/store"
)

// syncPack runs the fingerprint-gated sync (the production startup
// hook's new path) and fails the test on any error.
func syncPack(t *testing.T, h *api.TaxonomyHandler, force bool) (*store.TaxonomyCounts, bool) {
	t.Helper()
	counts, skipped, err := h.SyncTaxonomyPack(t.Context(), force)
	if err != nil {
		t.Fatalf("SyncTaxonomyPack(force=%v) failed: %v", force, err)
	}
	return counts, skipped
}

// getPackSetting reads one of the sync bookkeeping keys.
func getPackSetting(t *testing.T, e *testEnv, key string) string {
	t.Helper()
	v, err := e.store.GetSetting(t.Context(), key)
	if err != nil {
		t.Fatalf("GetSetting(%q): %v", key, err)
	}
	return v
}

// TestTaxonomyPackSyncFirstImportFullRun covers case ①: a fresh DB has
// no fingerprint setting, so the first sync is a full import that
// writes both bookkeeping keys and reports skipped=false.
func TestTaxonomyPackSyncFirstImportFullRun(t *testing.T) {
	e := setupTest(t)
	h := api.NewTaxonomyHandler(e.store, nil)

	if v := getPackSetting(t, e, "taxonomy.pack_fingerprint"); v != "" {
		t.Fatalf("fresh DB already has a fingerprint: %q", v)
	}

	start := time.Now()
	counts, skipped := syncPack(t, h, false)
	fullDur := time.Since(start)

	if skipped {
		t.Fatal("first sync skipped, want full import")
	}
	if counts.Topics != 3108 {
		t.Errorf("topics = %d, want 3108", counts.Topics)
	}

	fp := getPackSetting(t, e, "taxonomy.pack_fingerprint")
	if len(fp) != 64 {
		t.Fatalf("fingerprint key not written (len %d): %q", len(fp), fp)
	}
	syncedAt := getPackSetting(t, e, "taxonomy.pack_synced_at")
	if syncedAt == "" {
		t.Fatal("taxonomy.pack_synced_at not written by full import")
	}
	if _, err := time.Parse(time.RFC3339, syncedAt); err != nil {
		t.Errorf("taxonomy.pack_synced_at %q is not RFC3339: %v", syncedAt, err)
	}
	t.Logf("full sync took %s", fullDur)
}

// TestTaxonomyPackSyncSkipOnMatch covers case ②: after a full import
// has recorded the fingerprint, a fresh handler (what a restart gets)
// hits the stored fingerprint, skips the catalog load + full import
// entirely (skipped=true, catalog never parsed, dramatically faster),
// and still reports the real DB counts.
func TestTaxonomyPackSyncSkipOnMatch(t *testing.T) {
	e := setupTest(t)
	h := api.NewTaxonomyHandler(e.store, nil)

	start := time.Now()
	first, firstSkipped := syncPack(t, h, false)
	fullDur := time.Since(start)
	if firstSkipped {
		t.Fatal("first sync skipped, want full import")
	}

	// A restart builds a new handler: the fingerprint is in the DB, the
	// in-memory catalog is gone. This is the state the skip must cover.
	restarted := api.NewTaxonomyHandler(e.store, nil)
	start = time.Now()
	second, secondSkipped := syncPack(t, restarted, false)
	skipDur := time.Since(start)

	if !secondSkipped {
		t.Fatal("second sync did not skip despite matching fingerprint")
	}
	if second.Topics != first.Topics || second.Questions != first.Questions || second.Standards != first.Standards {
		t.Fatalf("skip path counts differ from full: %+v vs %+v", second, first)
	}
	if second.Topics != 3108 {
		t.Errorf("topics = %d, want 3108 (DB counts on the skip path)", second.Topics)
	}
	if restarted.HasCatalog() {
		t.Fatal("skip path loaded the catalog — skip must not parse the 13MB pack")
	}
	if skipDur > fullDur/2 {
		t.Errorf("skip path not materially faster: full %s vs skip %s", fullDur, skipDur)
	}
	t.Logf("full sync %s vs skipped sync %s", fullDur, skipDur)
}

// TestTaxonomyPackSyncForceOverrides covers case ③: force=true runs
// the full import even with a matching fingerprint (the manual escape
// hatch), loading the catalog and reporting skipped=false.
func TestTaxonomyPackSyncForceOverrides(t *testing.T) {
	e := setupTest(t)
	h := api.NewTaxonomyHandler(e.store, nil)

	if _, skipped := syncPack(t, h, false); skipped {
		t.Fatal("first sync skipped, want full import")
	}
	if _, skipped := syncPack(t, h, false); !skipped {
		t.Fatal("second sync did not skip, want fingerprint skip first")
	}

	counts, skipped := syncPack(t, h, true)
	if skipped {
		t.Fatal("force=true skipped, want full re-import")
	}
	if counts.Topics != 3108 {
		t.Errorf("topics = %d, want 3108", counts.Topics)
	}
	if !h.HasCatalog() {
		t.Fatal("force path did not load the catalog, want full import")
	}

	// Force refreshes the bookkeeping keys too.
	if v := getPackSetting(t, e, "taxonomy.pack_fingerprint"); len(v) != 64 {
		t.Fatalf("fingerprint key not refreshed: %q", v)
	}
	if v := getPackSetting(t, e, "taxonomy.pack_synced_at"); v == "" {
		t.Fatal("taxonomy.pack_synced_at not refreshed by force import")
	}
}
