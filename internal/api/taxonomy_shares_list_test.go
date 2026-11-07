package api_test

// TX-SHARE-LIST-API-TESTER: behaviour lock for
// GET /api/admin/taxonomy/shares?user_id=N (taxonomy_shares.go).
// Real SQLite + embedded pack + httptest, no mocks. Only the admin
// list endpoint is exercised here; the share lifecycle itself lives in
// TestTaxonomyShare* (taxonomy_test.go).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type shareListResponse struct {
	Shares []shareListItemResp `json:"shares"`
}

type shareListItemResp struct {
	ID         int64   `json:"id"`
	OwnerID    int64   `json:"owner_id"`
	SessionID  *int64  `json:"session_id"`
	ReportKind string  `json:"report_kind"`
	ExpiresAt  *string `json:"expires_at"`
	RevokedAt  *string `json:"revoked_at"`
	CreatedAt  string  `json:"created_at"`
	Status     string  `json:"status"`
}

// insertShare writes one diagnostic_shares row directly. Minimal local
// fixture used only to pin expires_at/revoked_at deterministically and
// to plant a sentinel snapshot that must never appear in the list.
func insertShare(t *testing.T, e *testEnv, ownerID, sessionID int64, tokenHash string, expiresAt, revokedAt *string) int64 {
	t.Helper()
	var exp, rev any
	if expiresAt != nil {
		exp = *expiresAt
	}
	if revokedAt != nil {
		rev = *revokedAt
	}
	res, err := e.db.Exec(`INSERT INTO diagnostic_shares
		(token_hash, owner_id, session_id, report_kind, snapshot_json, expires_at, revoked_at, created_by)
		VALUES (?, ?, ?, 'diagnosis', ?, ?, ?, ?)`,
		tokenHash, ownerID, sessionID, `{"summary":{"marker":"SNAPSHOT_SENTINEL"}}`, exp, rev, ownerID)
	if err != nil {
		t.Fatalf("insert share: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func shareTimeAgo(hours int) string {
	return time.Now().Add(-time.Duration(hours) * time.Hour).UTC().Format(time.RFC3339)
}

func fmtSecsPtr(s string) *string { return &s }

// shareNewSession creates one real screen session for the owner.
func shareNewSession(t *testing.T, e *testEnv, ownerHeaders map[string]string, seed string) int64 {
	t.Helper()
	detail := createDiagnosticSession(t, e, ownerHeaders, map[string]any{
		"mode":    "screen",
		"subject": "math",
		"targets": []string{"cnm_p001"},
		"limit":   2,
		"seed":    seed,
	})
	return int64(detail["session"].(map[string]any)["id"].(float64))
}

// listShares GETs the admin list expecting 200.
func listShares(t *testing.T, e *testEnv, headers map[string]string, query string) (string, shareListResponse, []map[string]any) {
	t.Helper()
	resp := e.expectStatus(t, "GET", "/api/admin/taxonomy/shares"+query, nil, headers, http.StatusOK)
	var raw json.RawMessage
	decodeBody(t, resp, &raw)
	var list shareListResponse
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode share list: %v (body=%s)", err, raw)
	}
	var rawTop struct {
		Shares []map[string]any `json:"shares"`
	}
	if err := json.Unmarshal(raw, &rawTop); err != nil {
		t.Fatalf("decode raw items: %v", err)
	}
	return string(raw), list, rawTop.Shares
}

func shareItemByID(t *testing.T, list shareListResponse, id int64) shareListItemResp {
	t.Helper()
	for _, it := range list.Shares {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("share %d missing from list", id)
	return shareListItemResp{}
}

// TestTaxonomyShareListStatuses pins the three statuses, the
// revoked>expired precedence, ordering, field set and leak checks.
func TestTaxonomyShareListStatuses(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小霞")
	other := e.createChild(t, "小刚")
	importPack(t, e)
	kidH := childHeaders(kid)

	sess1 := shareNewSession(t, e, kidH, "list-seed-1")

	// Two shares minted through the real endpoint (both active).
	mint := func(hours int) (int64, string) {
		resp := e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/share", sess1),
			map[string]any{"expires_in_hours": hours}, kidH, http.StatusCreated)
		var s struct {
			ShareID int64  `json:"share_id"`
			Token   string `json:"token"`
		}
		decodeBody(t, resp, &s)
		if s.ShareID <= 0 || s.Token == "" {
			t.Fatalf("mint: share_id=%d token=%q", s.ShareID, s.Token)
		}
		return s.ShareID, s.Token
	}
	activeA, tokenA := mint(24)
	activeB, tokenB := mint(48)

	// Deterministic rows: expired / revoked / revoked+expired.
	expiredID := insertShare(t, e, int64(kid), sess1, "hash-expired-"+t.Name(), fmtSecsPtr(shareTimeAgo(2)), nil)
	revokedID := insertShare(t, e, int64(kid), sess1, "hash-revoked-"+t.Name(), nil, fmtSecsPtr(shareTimeAgo(1)))
	revokedExpiredID := insertShare(t, e, int64(kid), sess1, "hash-rx-"+t.Name(),
		fmtSecsPtr(shareTimeAgo(2)), fmtSecsPtr(shareTimeAgo(1)))
	// Another kid's share must not bleed in.
	otherSess := shareNewSession(t, e, childHeaders(other), "list-seed-other")
	insertShare(t, e, int64(other), otherSess, "hash-other-"+t.Name(), nil, nil)

	raw, list, rawItems := listShares(t, e, adminHeaders(), "?user_id="+strconv.Itoa(kid))

	if len(list.Shares) != 5 {
		t.Fatalf("shares = %d, want 5: %s", len(list.Shares), raw)
	}
	ids := make([]int64, 0, len(list.Shares))
	for _, it := range list.Shares {
		ids = append(ids, it.ID)
	}
	if !sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] > ids[j] }) {
		t.Errorf("order = %v, want id DESC", ids)
	}
	if ids[0] != revokedExpiredID {
		t.Errorf("first id = %d, want newest %d", ids[0], revokedExpiredID)
	}

	cases := []struct {
		name   string
		id     int64
		status string
	}{
		{"active-24h", activeA, "active"},
		{"active-48h", activeB, "active"},
		{"expired", expiredID, "expired"},
		{"revoked", revokedID, "revoked"},
		{"revoked-beats-expired", revokedExpiredID, "revoked"},
	}
	for _, c := range cases {
		it := shareItemByID(t, list, c.id)
		if it.Status != c.status {
			t.Errorf("%s (id=%d): status = %q, want %q", c.name, c.id, it.Status, c.status)
		}
		if it.OwnerID != int64(kid) {
			t.Errorf("%s: owner_id = %d, want %d", c.name, it.OwnerID, kid)
		}
		if it.SessionID == nil || *it.SessionID != sess1 {
			t.Errorf("%s: session_id = %v, want %d", c.name, it.SessionID, sess1)
		}
		if it.ReportKind != "diagnosis" {
			t.Errorf("%s: report_kind = %q", c.name, it.ReportKind)
		}
		if it.CreatedAt == "" {
			t.Errorf("%s: created_at empty", c.name)
		}
		if c.status == "revoked" {
			if it.RevokedAt == nil {
				t.Errorf("%s: revoked_at nil", c.name)
			}
		} else if it.RevokedAt != nil {
			t.Errorf("%s: revoked_at = %v, want null", c.name, *it.RevokedAt)
		}
		if (c.status == "active" || c.id == expiredID) && it.ExpiresAt == nil {
			t.Errorf("%s: expires_at nil", c.name)
		}
	}

	// Revoking through the real DELETE flips status to revoked.
	e.expectStatus(t, "DELETE", fmt.Sprintf("/api/admin/taxonomy/shares/%d", activeA), nil, adminHeaders(), http.StatusOK)
	_, list, rawItems = listShares(t, e, adminHeaders(), "?user_id="+strconv.Itoa(kid))
	if it := shareItemByID(t, list, activeA); it.Status != "revoked" || it.RevokedAt == nil {
		t.Errorf("after DELETE: status=%q revoked_at=%v, want revoked+stamp", it.Status, it.RevokedAt)
	}

	// Exact field set on every item.
	wantKeys := map[string]bool{
		"id": true, "owner_id": true, "session_id": true, "report_kind": true,
		"expires_at": true, "revoked_at": true, "created_at": true, "status": true,
	}
	for _, item := range rawItems {
		if len(item) != len(wantKeys) {
			t.Errorf("item %v has %d keys, want %d", item["id"], len(item), len(wantKeys))
		}
		for k := range item {
			if !wantKeys[k] {
				t.Errorf("item %v unexpected key %q", item["id"], k)
			}
		}
	}

	// No sensitive content anywhere in the body.
	if strings.ContainsAny(raw, "\x00") {
		t.Errorf("body contains NUL")
	}
	lower := strings.ToLower(raw)
	for _, banned := range []string{"token_hash", "snapshot", "answer", "created_by", "hash-", "snapshot_sentinel"} {
		if strings.Contains(lower, banned) {
			t.Errorf("list body leaks %q: %s", banned, raw)
		}
	}
	for _, tok := range []string{tokenA, tokenB} {
		if strings.Contains(raw, tok) {
			t.Errorf("list body leaked raw token %q", tok)
		}
	}

	// A kid session is forbidden even for its own shares.
	resp := e.request(t, "GET", "/api/admin/taxonomy/shares?user_id="+strconv.Itoa(kid), nil, kidH)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("kid session: status = %d, want 403", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestTaxonomyShareListEmpty covers [] (never null) and the no-auth leg.
func TestTaxonomyShareListEmpty(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小空")
	importPack(t, e)

	raw, list, rawItems := listShares(t, e, adminHeaders(), "?user_id="+strconv.Itoa(kid))
	if list.Shares == nil {
		t.Fatalf("shares null, want []: %s", raw)
	}
	if len(list.Shares) != 0 || rawItems == nil {
		t.Fatalf("shares = %d rawItems=%v, want 0/[]: %s", len(list.Shares), rawItems, raw)
	}
	if !strings.Contains(strings.ReplaceAll(raw, " ", ""), `"shares":[]`) {
		t.Fatalf("body = %s, want \"shares\":[]", raw)
	}

	// Unminted user id: 200 + [].
	_, list, _ = listShares(t, e, adminHeaders(), "?user_id=99999")
	if list.Shares == nil || len(list.Shares) != 0 {
		t.Fatalf("unknown user shares = %v, want []", list.Shares)
	}

	// No auth: never 200.
	resp := e.request(t, "GET", "/api/admin/taxonomy/shares?user_id="+strconv.Itoa(kid), nil, nil)
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Errorf("anonymous: status = %d, want 401/403", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestTaxonomyShareListValidation covers the 400 legs and the error body.
func TestTaxonomyShareListValidation(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)

	bad := []string{
		"",
		"?user_id=",
		"?user_id=abc",
		"?user_id=0",
		"?user_id=-3",
		"?user_id=1.5",
		"?user_id=" + url.QueryEscape(" 2"),
		"?user_id=9223372036854775808", // overflows int64
	}
	for _, q := range bad {
		resp := e.request(t, "GET", "/api/admin/taxonomy/shares"+q, nil, adminHeaders())
		if resp.StatusCode != http.StatusBadRequest {
			resp.Body.Close()
			t.Errorf("%q: status = %d, want 400", q, resp.StatusCode)
			continue
		}
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if body["error"] == nil {
			t.Errorf("%q: body = %v, want {\"error\":...}", q, body)
		}
	}

	// Repeated param: Go keeps the first value, which is valid.
	resp := e.request(t, "GET", "/api/admin/taxonomy/shares?user_id=3&user_id=abc", nil, adminHeaders())
	if resp.StatusCode != http.StatusOK {
		t.Errorf("repeated param: status = %d, want 200 (first value wins)", resp.StatusCode)
	}
	resp.Body.Close()
}
