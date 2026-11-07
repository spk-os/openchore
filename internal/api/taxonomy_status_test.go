package api_test

import (
	"math"
	"net/http"
	"testing"

	"github.com/liftedkilt/openchore/internal/taxonomy"
)

// Card FR2-B-TESTER: integration tests for the read-only
// GET /api/admin/taxonomy/status endpoint (real SQLite DB + httptest, no
// mocks). The response contract is frozen: fingerprint reconciliation,
// nullable last_sync_at, pack row counts, and the FR-11 binding ratio
// (in-system KCs vs fallback tags).

const taxonomyStatusPath = "/api/admin/taxonomy/status"

// taxonomyStatusPayload mirrors taxonomyStatusResponse field for field.
// last_sync_at is a *string so a JSON null is distinguishable from "".
type taxonomyStatusPayload struct {
	Fingerprint      string  `json:"fingerprint"`
	FingerprintMatch bool    `json:"fingerprint_match"`
	LastSyncAt       *string `json:"last_sync_at"`
	TopicCount       int64   `json:"topic_count"`
	EdgeCount        int64   `json:"edge_count"`
	QuestionCount    int64   `json:"question_count"`
	StandardCount    int64   `json:"standard_count"`
	Binding          *struct {
		Numerator   int64    `json:"numerator"`
		Denominator int64    `json:"denominator"`
		Ratio       *float64 `json:"ratio"`
		Scope       string   `json:"scope"`
		Criterion   string   `json:"criterion"`
		BySubject   map[string]struct {
			Numerator   int64    `json:"numerator"`
			Denominator int64    `json:"denominator"`
			Ratio       *float64 `json:"ratio"`
		} `json:"by_subject"`
	} `json:"binding"`
}

// getTaxonomyStatus requests the status endpoint as admin and decodes it.
func getTaxonomyStatus(t *testing.T, e *testEnv) *taxonomyStatusPayload {
	t.Helper()
	resp := e.expectStatus(t, "GET", taxonomyStatusPath, nil, adminHeaders(), http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type: got %q, want application/json", ct)
	}
	var out taxonomyStatusPayload
	decodeBody(t, resp, &out)
	return &out
}

// mustExec runs a setup INSERT and fails the test on error.
func mustExec(t *testing.T, e *testEnv, query string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(query, args...); err != nil {
		t.Fatalf("setup exec failed: %v\nquery: %s", err, query)
	}
}

func assertRatio(t *testing.T, label string, got *float64, wantNumerator, wantDenominator int64) {
	t.Helper()
	if wantDenominator == 0 {
		if got != nil {
			t.Fatalf("%s: expected null ratio for zero denominator, got %v", label, *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("%s: expected ratio %d/%d, got null", label, wantNumerator, wantDenominator)
	}
	want := float64(wantNumerator) / float64(wantDenominator)
	if math.Abs(*got-want) > 1e-9 {
		t.Fatalf("%s: ratio got %v, want %v", label, *got, want)
	}
}

func TestAdminTaxonomyStatus(t *testing.T) {
	t.Run("unsynced empty pack", func(t *testing.T) {
		e := setupTest(t)
		e.createAdmin(t)

		var raw map[string]any
		resp := e.expectStatus(t, "GET", taxonomyStatusPath, nil, adminHeaders(), http.StatusOK)
		decodeBody(t, resp, &raw)

		// last_sync_at must be JSON null, not "" or absent.
		v, ok := raw["last_sync_at"]
		if !ok {
			t.Fatalf("last_sync_at key missing from response")
		}
		if v != nil {
			t.Fatalf("last_sync_at: expected JSON null, got %#v", v)
		}

		fp, err := taxonomy.PackFingerprint()
		if err != nil {
			t.Fatalf("PackFingerprint: %v", err)
		}
		if got := raw["fingerprint"]; got != fp {
			t.Fatalf("fingerprint: got %v, want %q", got, fp)
		}
		if raw["fingerprint_match"] != false {
			t.Fatalf("fingerprint_match: got %v, want false", raw["fingerprint_match"])
		}
		for _, k := range []string{"topic_count", "edge_count", "question_count", "standard_count"} {
			if got := raw[k]; got != float64(0) {
				t.Fatalf("%s: got %v, want 0", k, got)
			}
		}

		binding, ok := raw["binding"].(map[string]any)
		if !ok {
			t.Fatalf("binding: expected object, got %#v", raw["binding"])
		}
		if binding["numerator"] != float64(0) || binding["denominator"] != float64(0) {
			t.Fatalf("binding counts: got numerator=%v denominator=%v, want 0/0",
				binding["numerator"], binding["denominator"])
		}
		if binding["ratio"] != nil {
			t.Fatalf("binding.ratio: expected JSON null, got %#v", binding["ratio"])
		}
		if binding["scope"] != "global" {
			t.Fatalf("binding.scope: got %v, want global", binding["scope"])
		}
		if binding["criterion"] != "error_item_tags" {
			t.Fatalf("binding.criterion: got %v, want error_item_tags", binding["criterion"])
		}
		bySubject, ok := binding["by_subject"].(map[string]any)
		if !ok {
			t.Fatalf("by_subject: expected JSON object, got %#v", binding["by_subject"])
		}
		if len(bySubject) != 0 {
			t.Fatalf("by_subject: expected empty object, got %#v", bySubject)
		}
	})

	t.Run("synced fingerprint match", func(t *testing.T) {
		e := setupTest(t)
		e.createAdmin(t)

		fp, err := taxonomy.PackFingerprint()
		if err != nil {
			t.Fatalf("PackFingerprint: %v", err)
		}
		const syncedAt = "2026-11-07T10:00:00Z"
		mustExec(t, e, `INSERT INTO app_settings (key, value) VALUES ('taxonomy.pack_fingerprint', ?)`, fp)
		mustExec(t, e, `INSERT INTO app_settings (key, value) VALUES ('taxonomy.pack_synced_at', ?)`, syncedAt)

		out := getTaxonomyStatus(t, e)
		if !out.FingerprintMatch {
			t.Fatalf("fingerprint_match: got false, want true")
		}
		if out.Fingerprint != fp {
			t.Fatalf("fingerprint: got %q, want %q", out.Fingerprint, fp)
		}
		if out.LastSyncAt == nil {
			t.Fatalf("last_sync_at: got null, want %q", syncedAt)
		}
		if *out.LastSyncAt != syncedAt {
			t.Fatalf("last_sync_at: got %q, want %q", *out.LastSyncAt, syncedAt)
		}
	})

	t.Run("synced fingerprint mismatch", func(t *testing.T) {
		e := setupTest(t)
		e.createAdmin(t)

		fp, err := taxonomy.PackFingerprint()
		if err != nil {
			t.Fatalf("PackFingerprint: %v", err)
		}
		stale := fp
		if stale == "" {
			stale = "stale"
		} else {
			// Flip the first hex char so the stored value differs.
			c := byte('0')
			if stale[0] == '0' {
				c = '1'
			}
			stale = string(c) + stale[1:]
		}
		mustExec(t, e, `INSERT INTO app_settings (key, value) VALUES ('taxonomy.pack_fingerprint', ?)`, stale)
		mustExec(t, e, `INSERT INTO app_settings (key, value) VALUES ('taxonomy.pack_synced_at', '2026-11-07T10:00:00Z')`)

		out := getTaxonomyStatus(t, e)
		if out.FingerprintMatch {
			t.Fatalf("fingerprint_match: got true with stale stored fingerprint, want false")
		}
		if out.Fingerprint != fp {
			t.Fatalf("fingerprint: got %q, want embedded %q", out.Fingerprint, fp)
		}
	})

	t.Run("counts reflect inserted rows", func(t *testing.T) {
		e := setupTest(t)
		e.createAdmin(t)

		// 3 in-system KCs (is_system=1, external_ref set) + 1 fallback tag
		// (is_system=0, no external_ref) which must NOT count as a topic.
		tags := [][3]any{
			{"KC A", "math", "cnm_a"},
			{"KC B", "math", "cnm_b"},
			{"KC C", "chinese", "cnm_c"},
			{"fallback", "math", nil},
		}
		for i, tg := range tags {
			mustExec(t, e,
				`INSERT INTO knowledge_tags (name, subject, is_system, external_ref) VALUES (?, ?, ?, ?)`,
				tg[0], tg[1], b2i(i < 3), tg[2])
		}
		// Two prerequisite edges (ids 1..3 exist).
		mustExec(t, e, `INSERT INTO knowledge_tag_prerequisites (topic_id, prerequisite_id, strength) VALUES (1, 2, 'hard')`)
		mustExec(t, e, `INSERT INTO knowledge_tag_prerequisites (topic_id, prerequisite_id, strength) VALUES (1, 3, 'soft')`)
		// Three questions.
		mustExec(t, e, `INSERT INTO knowledge_questions (topic_id, type, prompt) VALUES (1, 'numeric', 'q1')`)
		mustExec(t, e, `INSERT INTO knowledge_questions (topic_id, type, prompt) VALUES (2, 'short', 'q2')`)
		mustExec(t, e, `INSERT INTO knowledge_questions (topic_id, type, prompt) VALUES (3, 'choice', 'q3')`)
		// Two curriculum standards.
		mustExec(t, e, `INSERT INTO curriculum_standards (external_ref, subject) VALUES ('cms-2022:1.NA.01', 'math')`)
		mustExec(t, e, `INSERT INTO curriculum_standards (external_ref, subject) VALUES ('cms-2022:2.NA.02', 'chinese')`)

		out := getTaxonomyStatus(t, e)
		if out.TopicCount != 3 {
			t.Fatalf("topic_count: got %d, want 3", out.TopicCount)
		}
		if out.EdgeCount != 2 {
			t.Fatalf("edge_count: got %d, want 2", out.EdgeCount)
		}
		if out.QuestionCount != 3 {
			t.Fatalf("question_count: got %d, want 3", out.QuestionCount)
		}
		if out.StandardCount != 2 {
			t.Fatalf("standard_count: got %d, want 2", out.StandardCount)
		}
		// No error items yet: binding is empty but non-null.
		if out.Binding == nil {
			t.Fatalf("binding: got null, want object")
		}
		if out.Binding.Denominator != 0 || out.Binding.Ratio != nil {
			t.Fatalf("binding: got denominator=%d ratio=%v, want 0/null",
				out.Binding.Denominator, out.Binding.Ratio)
		}
	})

	t.Run("FR-11 binding ratio grouped by error item subject", func(t *testing.T) {
		e := setupTest(t)
		e.createAdmin(t)
		kidID := e.createChild(t, "Kid")

		// Tags: 2 in-system KCs + 1 fallback. Deliberately give the in-system
		// tags a subject different from the error items so a grouping-by-tag-
		// subject bug would surface as wrong by_subject keys.
		mustExec(t, e, `INSERT INTO knowledge_tags (id, name, subject, is_system, external_ref) VALUES (1, 'KC1', 'science', 1, 'cnm_1')`)
		mustExec(t, e, `INSERT INTO knowledge_tags (id, name, subject, is_system, external_ref) VALUES (2, 'KC2', 'science', 1, 'cnm_2')`)
		mustExec(t, e, `INSERT INTO knowledge_tags (id, name, subject, is_system, external_ref) VALUES (3, 'fallback', 'science', 0, NULL)`)

		// Error items: math x2, chinese x1.
		mustExec(t, e, `INSERT INTO error_items (id, user_id, subject, original_image_url) VALUES (1, ?, 'math', 'x.jpg')`, kidID)
		mustExec(t, e, `INSERT INTO error_items (id, user_id, subject, original_image_url) VALUES (2, ?, 'math', 'x.jpg')`, kidID)
		mustExec(t, e, `INSERT INTO error_items (id, user_id, subject, original_image_url) VALUES (3, ?, 'chinese', 'x.jpg')`, kidID)

		// Bindings:
		//   math#1 -> KC1 (in-system), fallback (denominator only)
		//   math#2 -> KC2 (in-system)
		//   chinese#3 -> KC1 (in-system), fallback
		mustExec(t, e, `INSERT INTO error_item_tags (error_item_id, tag_id) VALUES (1, 1)`)
		mustExec(t, e, `INSERT INTO error_item_tags (error_item_id, tag_id) VALUES (1, 3)`)
		mustExec(t, e, `INSERT INTO error_item_tags (error_item_id, tag_id) VALUES (2, 2)`)
		mustExec(t, e, `INSERT INTO error_item_tags (error_item_id, tag_id) VALUES (3, 1)`)
		mustExec(t, e, `INSERT INTO error_item_tags (error_item_id, tag_id) VALUES (3, 3)`)

		// Expected: denominator 5, numerator 3 (math 2/3, chinese 1/2).
		out := getTaxonomyStatus(t, e)
		if out.Binding == nil {
			t.Fatalf("binding: got null, want object")
		}
		if out.Binding.Denominator != 5 {
			t.Fatalf("binding.denominator: got %d, want 5", out.Binding.Denominator)
		}
		if out.Binding.Numerator != 3 {
			t.Fatalf("binding.numerator: got %d, want 3", out.Binding.Numerator)
		}
		assertRatio(t, "binding.ratio", out.Binding.Ratio, 3, 5)

		bySubject := out.Binding.BySubject
		if len(bySubject) != 2 {
			t.Fatalf("by_subject: got %d keys %v, want 2 (math, chinese)", len(bySubject), bySubject)
		}
		if _, ok := bySubject["science"]; ok {
			t.Fatalf("by_subject: grouped by tag subject 'science'; must group by error_items.subject")
		}
		mathSB, ok := bySubject["math"]
		if !ok {
			t.Fatalf("by_subject: missing 'math', got %v", bySubject)
		}
		if mathSB.Numerator != 2 || mathSB.Denominator != 3 {
			t.Fatalf("math: got %d/%d, want 2/3", mathSB.Numerator, mathSB.Denominator)
		}
		assertRatio(t, "math.ratio", mathSB.Ratio, 2, 3)
		chineseSB, ok := bySubject["chinese"]
		if !ok {
			t.Fatalf("by_subject: missing 'chinese', got %v", bySubject)
		}
		if chineseSB.Numerator != 1 || chineseSB.Denominator != 2 {
			t.Fatalf("chinese: got %d/%d, want 1/2", chineseSB.Numerator, chineseSB.Denominator)
		}
		assertRatio(t, "chinese.ratio", chineseSB.Ratio, 1, 2)

		// Fallback zero-regression: the fallback tag counts in the
		// denominator only (5 total - 3 in-system = 2 fallback rows), and
		// error_item_tags rows are untouched (5 still) — a second GET is
		// idempotent and the table is unchanged.
		if out.Binding.Denominator-out.Binding.Numerator != 2 {
			t.Fatalf("fallback bindings: got %d, want 2", out.Binding.Denominator-out.Binding.Numerator)
		}
		var n int
		if err := e.db.QueryRow(`SELECT COUNT(*) FROM error_item_tags`).Scan(&n); err != nil {
			t.Fatalf("count error_item_tags: %v", err)
		}
		if n != 5 {
			t.Fatalf("error_item_tags after GET: got %d rows, want 5", n)
		}
		out2 := getTaxonomyStatus(t, e)
		if out2.Binding.Denominator != out.Binding.Denominator || out2.Binding.Numerator != out.Binding.Numerator {
			t.Fatalf("second GET not idempotent: %d/%d vs %d/%d",
				out2.Binding.Numerator, out2.Binding.Denominator,
				out.Binding.Numerator, out.Binding.Denominator)
		}
	})

	t.Run("permission matrix", func(t *testing.T) {
		e := setupTest(t)
		e.createAdmin(t)
		kidID := e.createChild(t, "Kid")

		cases := []struct {
			name      string
			headers   map[string]string
			wantCode  int
			wantError bool
		}{
			{"anonymous", nil, http.StatusUnauthorized, true},
			{"child", childHeaders(kidID), http.StatusForbidden, true},
			{"admin", adminHeaders(), http.StatusOK, false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				resp := e.request(t, "GET", taxonomyStatusPath, nil, tc.headers)
				if resp.StatusCode != tc.wantCode {
					resp.Body.Close()
					t.Fatalf("status: got %d, want %d", resp.StatusCode, tc.wantCode)
				}
				var body map[string]any
				decodeBody(t, resp, &body)
				if tc.wantError {
					msg, ok := body["error"].(string)
					if !ok || msg == "" {
						t.Fatalf("error body: got %#v, want non-empty {\"error\": \"...\"}", body)
					}
				} else if _, ok := body["fingerprint"]; !ok {
					t.Fatalf("admin body: missing fingerprint key, got %#v", body)
				}
			})
		}
	})
}

// b2i converts a bool into the integer SQLite stores for is_system.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
