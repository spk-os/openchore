package api_test

// DEF-LEARNING-EXPORT-HTTP-R1-TESTER：学习数据导出/恢复的独立 HTTP 验收。
//
// 全程走生产 NewRouter + 真实 SQLite + httptest，没有任何 mock / 假 handler：
//
//	GET  /api/taxonomy/export        （孩子只能读自己，admin 必须显式 user_id）
//	POST /api/admin/taxonomy/import  （学习文档恢复，显式 target_user_id）
//	POST /api/admin/taxonomy/sync    （内置数据包重导入，语义与 /import 不同）
//
// 关键用例是“源机”和“目标机”两个独立内存库：源机导出真实文档，目标机用相同
// external_ref、不同本机 id 通过 HTTP 导入，逐节验证映射、幂等与“绝不用
// payload.user.id 作为回退目标”。
//
// 契约注意：events 的源 ObjectID 是本机对象 id（0=可恢复且目标也必须是 0；
// 非 0=本机对象引用，跨机无法验证，必须显式 skipped）。ObjectRef 只是
// topic 元数据，恢复侧绝不把它写回 object_id；kc_ids 仍按稳定 ref 重映射。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/api"
	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// ---------------------------------------------------------------------------
// 通用请求 / 断言 helper
// ---------------------------------------------------------------------------

func txlRequest(t *testing.T, e *testEnv, method, path string, body []byte, headers map[string]string) (int, []byte, http.Header) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, e.server.URL+path, rd)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	return resp.StatusCode, b, resp.Header
}

func txlJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func txlWantStatus(t *testing.T, e *testEnv, method, path string, body []byte,
	headers map[string]string, want int) ([]byte, http.Header) {
	t.Helper()
	code, b, hdr := txlRequest(t, e, method, path, body, headers)
	if code != want {
		t.Fatalf("%s %s: want %d, got %d: %s", method, path, want, code, b)
	}
	return b, hdr
}

func txlErrorMsg(t *testing.T, body []byte) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("error body is not JSON: %s", body)
	}
	return m["error"]
}

func txlExec(t *testing.T, db *sql.DB, q string, args ...any) int64 {
	t.Helper()
	res, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func txlCount(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", q, err)
	}
	return n
}

func txlAt(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

// txlDoc 是手工构造的最小合法学习文档（所有切片显式非 nil，JSON 不会出 null）。
// userID 是诱饵：导入时必须使用显式 target_user_id，绝不回退到它。
func txlDoc(targetUserID, userID int64) map[string]any {
	return map[string]any{
		"schema_version": 1,
		"pack_version":   "cnm-2026",
		"exported_at":    "2026-04-01T12:00:00Z",
		"target_user_id": targetUserID,
		"user":           map[string]any{"id": userID, "name": "诱饵", "role": model.RoleChild},
		"mastery":        []any{},
		"events":         []any{},
		"diagnostics":    []any{},
		"attempts":       []any{},
		"review_items":   []any{},
	}
}

func txlSeedTag(t *testing.T, db *sql.DB, name, ref string) int64 {
	t.Helper()
	return txlExec(t, db,
		`INSERT INTO knowledge_tags (name, subject, external_ref) VALUES (?, 'math', ?)`, name, ref)
}

// ---------------------------------------------------------------------------
// 1) GET /api/taxonomy/export：权限、显式目标、响应头、文档形状
// ---------------------------------------------------------------------------

func TestTaxonomyLearningExportHTTPContract(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t) // id 1
	kidA := e.createChild(t, "甲")
	kidB := e.createChild(t, "乙")

	// kidB 有自己的掌握度行；kidA 的导出绝不能带上它。
	tagB := txlSeedTag(t, e.db, "乙的加法", "txl-kid-b-add")
	txlExec(t, e.db,
		`INSERT INTO kc_mastery (user_id, tag_id, correct_count, total_count, mastery, low_confidence, last_event_at)
		 VALUES (?, ?, 9, 10, 0.91, 0, '2026-03-01 09:00:00')`, kidB, tagB)
	txlExec(t, e.db,
		`INSERT INTO learner_events (user_id, verb, object_type, object_id, occurred_at)
		 VALUES (?, 'answered', 'review_item', 0, '2026-03-01 09:00:00')`, kidB)

	const path = "/api/taxonomy/export"

	t.Run("unauthenticated", func(t *testing.T) {
		code, b, _ := txlRequest(t, e, "GET", path, nil, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d: %s", code, b)
		}
	})

	t.Run("kid defaults to self with no-store headers", func(t *testing.T) {
		b, hdr := txlWantStatus(t, e, "GET", path, nil, sessionHeaders(kidA), http.StatusOK)
		if got := hdr.Get("Content-Disposition"); !strings.Contains(got, "attachment") ||
			!strings.Contains(got, "openchore-learning-export.json") {
			t.Fatalf("Content-Disposition = %q", got)
		}
		if got := hdr.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q, want no-store", got)
		}
		if got := hdr.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
		}
		var doc model.LearningExport
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("decode export: %v / %s", err, b)
		}
		if doc.SchemaVersion != 1 {
			t.Fatalf("schema_version = %d, want 1", doc.SchemaVersion)
		}
		if strings.TrimSpace(doc.PackVersion) == "" {
			t.Fatalf("pack_version must not be blank: %+v", doc)
		}
		if doc.ExportedAt.IsZero() {
			t.Fatalf("exported_at must be non-zero: %+v", doc)
		}
		if doc.TargetUserID != 0 {
			t.Fatalf("export must not name a restore target, got %d", doc.TargetUserID)
		}
		if doc.User.ID != int64(kidA) || doc.User.Role != model.RoleChild || doc.User.Name == "" {
			t.Fatalf("export user = %+v, want kidA=%d", doc.User, kidA)
		}
		if doc.Mastery == nil || doc.Events == nil || doc.Diagnostics == nil ||
			doc.Attempts == nil || doc.ReviewItems == nil {
			t.Fatalf("slices must be non-nil (JSON [] not null): %s", b)
		}
		if len(doc.Mastery) != 0 || len(doc.Events) != 0 {
			t.Fatalf("kidA export leaked kidB rows: mastery=%+v events=%+v", doc.Mastery, doc.Events)
		}
	})

	t.Run("kid with own user_id", func(t *testing.T) {
		b, _ := txlWantStatus(t, e, "GET", fmt.Sprintf("%s?user_id=%d", path, kidA),
			nil, sessionHeaders(kidA), http.StatusOK)
		var doc model.LearningExport
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if doc.User.ID != int64(kidA) {
			t.Fatalf("user.id = %d, want %d", doc.User.ID, kidA)
		}
	})

	t.Run("kid cannot read another user", func(t *testing.T) {
		b, _ := txlWantStatus(t, e, "GET", fmt.Sprintf("%s?user_id=%d", path, kidB),
			nil, sessionHeaders(kidA), http.StatusForbidden)
		if msg := txlErrorMsg(t, b); msg == "" {
			t.Fatalf("missing error message: %s", b)
		}
	})

	t.Run("admin must name a target", func(t *testing.T) {
		b, _ := txlWantStatus(t, e, "GET", path, nil, adminHeaders(), http.StatusBadRequest)
		if msg := txlErrorMsg(t, b); msg == "" {
			t.Fatalf("missing error message: %s", b)
		}
	})

	for _, raw := range []string{"abc", "0", "-1", "1.5"} {
		raw := raw
		t.Run("admin invalid user_id "+raw, func(t *testing.T) {
			b, _ := txlWantStatus(t, e, "GET", path+"?user_id="+raw, nil,
				adminHeaders(), http.StatusBadRequest)
			if msg := txlErrorMsg(t, b); msg == "" {
				t.Fatalf("missing error message: %s", b)
			}
		})
	}

	t.Run("admin unknown target is 404", func(t *testing.T) {
		b, _ := txlWantStatus(t, e, "GET", path+"?user_id=999999", nil,
			adminHeaders(), http.StatusNotFound)
		if msg := txlErrorMsg(t, b); msg != "user not found" {
			t.Fatalf("error = %q, want 'user not found'", msg)
		}
	})

	t.Run("admin names any existing user", func(t *testing.T) {
		b, _ := txlWantStatus(t, e, "GET", fmt.Sprintf("%s?user_id=%d", path, kidB),
			nil, adminHeaders(), http.StatusOK)
		var doc model.LearningExport
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if doc.User.ID != int64(kidB) || doc.User.Name != "乙" {
			t.Fatalf("admin export user = %+v, want kidB=%d", doc.User, kidB)
		}
		if len(doc.Mastery) != 1 || doc.Mastery[0].ExternalRef != "txl-kid-b-add" {
			t.Fatalf("kidB export mastery = %+v", doc.Mastery)
		}
	})
}

// txlSeedAPIToken 通过真实 store 写入一条 API token（明文只留在测试里，
// 库里只有 sha256），返回 Authorization 头。NewRouter 的 RequireUserOrToken
// 会把它识别成 id=0 的合成 admin。
func txlSeedAPIToken(t *testing.T, e *testEnv, name string) map[string]string {
	t.Helper()
	raw := fmt.Sprintf("txl-token-%s-%d", name, time.Now().UnixNano())
	sum := sha256.Sum256([]byte(raw))
	if err := e.store.CreateAPIToken(context.Background(), &model.APIToken{
		Name: name, TokenHash: hex.EncodeToString(sum[:]),
	}); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	return map[string]string{"Authorization": "Bearer " + raw}
}

// ---------------------------------------------------------------------------
// 1b) 显式目标边界（真实 NewRouter + 真 API token）：非 admin 显式
//     user_id<=0 或他人 → 403；admin / token 必须给正数显式目标。
// ---------------------------------------------------------------------------

func TestTaxonomyLearningExportTargetBoundaryOverHTTP(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t) // id 1
	kidA := e.createChild(t, "甲")
	kidB := e.createChild(t, "乙")
	txlSeedTag(t, e.db, "甲的加法", "txl-boundary-a")
	txlExec(t, e.db,
		`INSERT INTO kc_mastery (user_id, tag_id, mastery, last_event_at)
		 VALUES (?, (SELECT id FROM knowledge_tags WHERE external_ref = 'txl-boundary-a'), 0.5, '2026-03-01 09:00:00')`, kidA)

	const path = "/api/taxonomy/export"
	token := txlSeedAPIToken(t, e, "boundary")

	t.Run("kid omitted user_id exports self", func(t *testing.T) {
		b, _ := txlWantStatus(t, e, "GET", path, nil, sessionHeaders(kidA), http.StatusOK)
		var doc model.LearningExport
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if doc.User.ID != int64(kidA) || len(doc.Mastery) != 1 {
			t.Fatalf("kid self export = user %d mastery %+v", doc.User.ID, doc.Mastery)
		}
	})

	t.Run("kid explicit own id is 200", func(t *testing.T) {
		txlWantStatus(t, e, "GET", fmt.Sprintf("%s?user_id=%d", path, kidA), nil,
			sessionHeaders(kidA), http.StatusOK)
	})

	for _, raw := range []string{"0", "-1", "-42", "abc", fmt.Sprintf("%d", kidB)} {
		raw := raw
		t.Run("kid explicit user_id "+raw+" is 403", func(t *testing.T) {
			b, _ := txlWantStatus(t, e, "GET", path+"?user_id="+raw, nil,
				sessionHeaders(kidA), http.StatusForbidden)
			if msg := txlErrorMsg(t, b); msg == "" {
				t.Fatalf("missing error message: %s", b)
			}
		})
	}

	t.Run("kid probe never touches store: no kidB rows leak in 403 body", func(t *testing.T) {
		_, b, _ := txlRequest(t, e, "GET", fmt.Sprintf("%s?user_id=%d", path, kidB), nil, sessionHeaders(kidA))
		if strings.Contains(string(b), `"user"`) || strings.Contains(string(b), "乙") {
			t.Fatalf("403 body leaked a document: %s", b)
		}
	})

	t.Run("api token must name explicit positive target", func(t *testing.T) {
		b, _ := txlWantStatus(t, e, "GET", path, nil, token, http.StatusBadRequest)
		if msg := txlErrorMsg(t, b); msg != "user_id is required" {
			t.Fatalf("token no user_id: error = %q", msg)
		}
		for _, raw := range []string{"0", "-1", "abc"} {
			b, _ := txlWantStatus(t, e, "GET", path+"?user_id="+raw, nil, token, http.StatusBadRequest)
			if msg := txlErrorMsg(t, b); msg != "invalid user_id" {
				t.Fatalf("token user_id=%s: error = %q", raw, msg)
			}
		}
		b, hdr := txlWantStatus(t, e, "GET", fmt.Sprintf("%s?user_id=%d", path, kidA), nil, token, http.StatusOK)
		var doc model.LearningExport
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if doc.User.ID != int64(kidA) || len(doc.Mastery) != 1 {
			t.Fatalf("token export of kidA = user %d mastery %+v", doc.User.ID, doc.Mastery)
		}
		// token 路径同样不能弱化下载头。
		if hdr.Get("Cache-Control") != "no-store" || hdr.Get("X-Content-Type-Options") != "nosniff" ||
			!strings.Contains(hdr.Get("Content-Disposition"), "attachment") {
			t.Fatalf("token export headers weakened: %v", hdr)
		}
	})

	t.Run("api token unknown target is still 404", func(t *testing.T) {
		b, _ := txlWantStatus(t, e, "GET", path+"?user_id=999999", nil, token, http.StatusNotFound)
		if msg := txlErrorMsg(t, b); msg != "user not found" {
			t.Fatalf("error = %q", msg)
		}
	})

	t.Run("api token import requires an existing explicit target", func(t *testing.T) {
		// token 是合成 admin：导入允许，但 target_user_id 必须显式且存在。
		body := txlJSON(t, txlDoc(int64(kidA), int64(kidB)))
		txlWantStatus(t, e, "POST", "/api/admin/taxonomy/import", body, token, http.StatusOK)
		if n := txlCount(t, e.db, `SELECT COUNT(*) FROM kc_mastery WHERE user_id = ?`, kidB); n != 0 {
			t.Fatalf("payload user.id fallback wrote %d rows to kidB", n)
		}
		b, _ := txlWantStatus(t, e, "POST", "/api/admin/taxonomy/import",
			txlJSON(t, txlDoc(999999, int64(kidA))), token, http.StatusBadRequest)
		if msg := txlErrorMsg(t, b); msg != "target user does not exist on this server" {
			t.Fatalf("unknown target: error = %q", msg)
		}
	})

	t.Run("revoked api token is 401", func(t *testing.T) {
		revoked := txlSeedAPIToken(t, e, "revoked")
		var id int64
		if err := e.db.QueryRow(`SELECT id FROM api_tokens WHERE name = 'revoked'`).Scan(&id); err != nil {
			t.Fatalf("lookup token: %v", err)
		}
		if err := e.store.RevokeAPIToken(context.Background(), id); err != nil {
			t.Fatalf("RevokeAPIToken: %v", err)
		}
		code, b, _ := txlRequest(t, e, "GET", fmt.Sprintf("%s?user_id=%d", path, kidA), nil, revoked)
		if code != http.StatusUnauthorized {
			t.Fatalf("revoked token: want 401, got %d: %s", code, b)
		}
	})
}

// ---------------------------------------------------------------------------
// 2) 导出时 DB 故障：500 且响应不泄露 SQL/存储细节
// ---------------------------------------------------------------------------
func TestTaxonomyLearningExportStoreFailureDoesNotLeak(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "甲")

	h := api.NewTaxonomyHandler(e.store, nil)
	sessions := api.NewSessionManager(testSessionSecret)
	srv := httptest.NewServer(api.RequireUserOrToken(e.store, sessions)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 鉴权已完成；在 handler 前关库，真实触发 store 层错误。
		_ = e.db.Close()
		h.ExportLearningDocument(w, r)
	})))
	defer srv.Close()

	req, err := http.NewRequest("GET", srv.URL+"/api/taxonomy/export", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+sessionToken(kid))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", resp.StatusCode, b)
	}
	msg := txlErrorMsg(t, b)
	if msg != "failed to build export" {
		t.Fatalf("error = %q, want generic 'failed to build export'", msg)
	}
	low := strings.ToLower(string(b))
	if strings.Contains(low, "sql") || strings.Contains(low, "database") || strings.Contains(low, "closed") {
		t.Fatalf("response leaked storage detail: %s", b)
	}
}

// ---------------------------------------------------------------------------
// 3) POST /api/admin/taxonomy/import：权限 + 严格 JSON + 元数据 + 显式目标
// ---------------------------------------------------------------------------

func TestTaxonomyLearningImportHTTPValidation(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t) // id 1
	kid := e.createChild(t, "甲")
	const path = "/api/admin/taxonomy/import"

	valid := txlJSON(t, txlDoc(int64(kid), int64(kid)))

	t.Run("non-admin is forbidden", func(t *testing.T) {
		code, b, _ := txlRequest(t, e, "POST", path, valid, sessionHeaders(kid))
		if code != http.StatusForbidden {
			t.Fatalf("want 403, got %d: %s", code, b)
		}
	})
	t.Run("unauthenticated", func(t *testing.T) {
		code, b, _ := txlRequest(t, e, "POST", path, valid, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d: %s", code, b)
		}
	})

	unknown := txlDoc(int64(kid), int64(kid))
	unknown["surprise"] = "schema drift"
	unknownNested := txlDoc(int64(kid), int64(kid))
	unknownNested["user"] = map[string]any{"id": kid, "surprise": "drift"}

	oversize := []byte(fmt.Sprintf(`{"schema_version":1,"target_user_id":%d,"pack_version":"%s"}`,
		kid, strings.Repeat("a", 20<<20+1024)))

	strictCases := []struct {
		name string
		body []byte
	}{
		{"empty body", []byte{}},
		{"whitespace only", []byte("  \n")},
		{"malformed JSON", []byte(`{"schema_version":1,`)},
		{"wrong type", []byte(`{"schema_version":"one"}`)},
		{"unknown top-level field", txlJSON(t, unknown)},
		{"unknown nested field", txlJSON(t, unknownNested)},
		{"trailing second JSON", append(append([]byte{}, valid...), []byte(`{"schema_version":1}`)...)},
		{"trailing garbage", append(append([]byte{}, valid...), []byte(" xyz")...)},
		{"over 20MB", oversize},
	}
	for _, tc := range strictCases {
		tc := tc
		t.Run("strict/"+tc.name, func(t *testing.T) {
			b, _ := txlWantStatus(t, e, "POST", path, tc.body, adminHeaders(), http.StatusBadRequest)
			if msg := txlErrorMsg(t, b); msg == "" {
				t.Fatalf("missing error message: %s", b)
			}
		})
	}

	metaCases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"schema_version 0", func(p map[string]any) { p["schema_version"] = 0 }},
		{"schema_version -1", func(p map[string]any) { p["schema_version"] = -1 }},
		{"schema_version 2", func(p map[string]any) { p["schema_version"] = 2 }},
		{"schema_version 99", func(p map[string]any) { p["schema_version"] = 99 }},
		{"pack_version blank", func(p map[string]any) { p["pack_version"] = "" }},
		{"pack_version whitespace", func(p map[string]any) { p["pack_version"] = "   \t\n" }},
		{"exported_at omitted", func(p map[string]any) { delete(p, "exported_at") }},
		{"exported_at zero", func(p map[string]any) { p["exported_at"] = "0001-01-01T00:00:00Z" }},
		{"target omitted", func(p map[string]any) { delete(p, "target_user_id") }},
		{"target 0", func(p map[string]any) { p["target_user_id"] = 0 }},
		{"target negative", func(p map[string]any) { p["target_user_id"] = -5 }},
		{"target nonexistent", func(p map[string]any) { p["target_user_id"] = 999999 }},
	}
	for _, tc := range metaCases {
		tc := tc
		t.Run("meta/"+tc.name, func(t *testing.T) {
			p := txlDoc(int64(kid), int64(kid))
			tc.mutate(p)
			b, _ := txlWantStatus(t, e, "POST", path, txlJSON(t, p), adminHeaders(), http.StatusBadRequest)
			if msg := txlErrorMsg(t, b); msg == "" {
				t.Fatalf("missing error message: %s", b)
			}
		})
	}

	// 所有被拒的请求都不得落任何学习行；诱饵 user.id=kid 也绝不能成为目标。
	for _, table := range []string{"kc_mastery", "learner_events", "review_items", "diagnostic_sessions", "diagnostic_attempts"} {
		if n := txlCount(t, e.db, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Fatalf("rejected imports wrote %d rows into %s", n, table)
		}
	}
}

// ---------------------------------------------------------------------------
// 4) 导入时 DB 故障：500 且不泄露存储细节
// ---------------------------------------------------------------------------

func TestTaxonomyLearningImportStoreFailureDoesNotLeak(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "甲")

	h := api.NewTaxonomyHandler(e.store, nil)
	sessions := api.NewSessionManager(testSessionSecret)
	srv := httptest.NewServer(api.RequireUserOrToken(e.store, sessions)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = e.db.Close() // 鉴权后关库：GetUser 真实返回 DB 错误
		h.ImportLearningDocument(w, r)
	})))
	defer srv.Close()

	body := txlJSON(t, txlDoc(1, int64(kid))) // target=admin(id1)，存在性查询会先失败
	req, err := http.NewRequest("POST", srv.URL+"/api/admin/taxonomy/import", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+sessionToken(1))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d: %s", resp.StatusCode, b)
	}
	msg := txlErrorMsg(t, b)
	if msg != "failed to load target user" {
		t.Fatalf("error = %q, want generic 'failed to load target user'", msg)
	}
	low := strings.ToLower(string(b))
	if strings.Contains(low, "sql") || strings.Contains(low, "database") || strings.Contains(low, "closed") {
		t.Fatalf("response leaked storage detail: %s", b)
	}
}

// ---------------------------------------------------------------------------
// 5) store 哨兵错误身份（handler 用 errors.Is 映射 400）
// ---------------------------------------------------------------------------

func TestTaxonomyLearningImportSentinelErrors(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "甲")

	payload := func(schema int, target int64) *model.LearningExport {
		return &model.LearningExport{
			SchemaVersion: schema,
			PackVersion:   "cnm-2026",
			ExportedAt:    txlAt(2026, 4, 1, 12, 0),
			TargetUserID:  target,
			Mastery:       []model.LearningMastery{},
			Events:        []model.LearningEvent{},
			Diagnostics:   []model.DiagnosticSession{},
			Attempts:      []model.DiagnosticAttempt{},
			ReviewItems:   []model.LearningReviewItem{},
		}
	}

	if _, err := e.store.RestoreLearning(context.Background(), int64(kid), payload(0, int64(kid))); !errors.Is(err, store.ErrLearningRestoreUnsupportedSchema) {
		t.Fatalf("schema 0: want errors.Is ErrLearningRestoreUnsupportedSchema, got %v", err)
	}
	if _, err := e.store.RestoreLearning(context.Background(), 999999, payload(1, 999999)); !errors.Is(err, store.ErrLearningRestoreTargetNotFound) {
		t.Fatalf("missing target: want errors.Is ErrLearningRestoreTargetNotFound, got %v", err)
	}
	// 目标必须存在且 schema 合法才可能继续；错误路径不得写入任何行。
	for _, table := range []string{"kc_mastery", "learner_events", "review_items", "diagnostic_sessions"} {
		if n := txlCount(t, e.db, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Fatalf("sentinel error path wrote %d rows into %s", n, table)
		}
	}
}

// ---------------------------------------------------------------------------
// 6) /sync 与 /import 语义不混：pack 重导入 vs 学习文档恢复
// ---------------------------------------------------------------------------

func TestTaxonomyLearningImportAndSyncAreDistinct(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t) // id 1
	kid := e.createChild(t, "甲")

	const syncPath = "/api/admin/taxonomy/sync"
	const importPath = "/api/admin/taxonomy/import"

	t.Run("sync is admin-only", func(t *testing.T) {
		code, b, _ := txlRequest(t, e, "POST", syncPath, nil, sessionHeaders(kid))
		if code != http.StatusForbidden {
			t.Fatalf("kid /sync: want 403, got %d: %s", code, b)
		}
		code, b, _ = txlRequest(t, e, "POST", syncPath, nil, nil)
		if code != http.StatusUnauthorized {
			t.Fatalf("anon /sync: want 401, got %d: %s", code, b)
		}
	})

	// /sync 是 pack 重导入：没有 schema/mastery 等恢复字段，只有 counts。
	syncBody, _ := txlWantStatus(t, e, "POST", syncPath, nil, adminHeaders(), http.StatusOK)
	var syncRes map[string]json.RawMessage
	if err := json.Unmarshal(syncBody, &syncRes); err != nil {
		t.Fatalf("sync response is not JSON: %v / %s", err, syncBody)
	}
	for _, k := range []string{"imported", "skipped", "counts"} {
		if _, ok := syncRes[k]; !ok {
			t.Fatalf("sync response missing %q: %s", k, syncBody)
		}
	}
	for _, k := range []string{"mastery", "events", "diagnostics", "attempts", "review_items"} {
		if _, ok := syncRes[k]; ok {
			t.Fatalf("sync response carries restore-only field %q (semantics aliased): %s", k, syncBody)
		}
	}

	// /import 空 body 必须 400（学习文档恢复，不是 pack 路径）。
	t.Run("import empty body is 400", func(t *testing.T) {
		b, _ := txlWantStatus(t, e, "POST", importPath, []byte{}, adminHeaders(), http.StatusBadRequest)
		if msg := txlErrorMsg(t, b); msg == "" {
			t.Fatalf("missing error message: %s", b)
		}
	})

	// 同一份合法学习文档：/import 返回 RestoreResult；/sync 仍返回 pack counts 且不恢复学习行。
	doc := txlDoc(int64(kid), int64(kid))
	importBody, _ := txlWantStatus(t, e, "POST", importPath, txlJSON(t, doc), adminHeaders(), http.StatusOK)
	var restoreRes model.LearningRestoreResult
	if err := json.Unmarshal(importBody, &restoreRes); err != nil {
		t.Fatalf("import response is not RestoreResult: %v / %s", err, importBody)
	}
	var importRaw map[string]json.RawMessage
	if err := json.Unmarshal(importBody, &importRaw); err != nil {
		t.Fatalf("decode import raw: %v", err)
	}
	if _, ok := importRaw["mastery"]; !ok {
		t.Fatalf("import response missing mastery section: %s", importBody)
	}
	if _, ok := importRaw["counts"]; ok {
		t.Fatalf("import response leaked pack counts: %s", importBody)
	}
	if restoreRes.Imported != 0 || restoreRes.Skipped != 0 {
		t.Fatalf("empty document restore counts = %+v, want 0/0", restoreRes)
	}

	againBody, _ := txlWantStatus(t, e, "POST", syncPath, txlJSON(t, doc), adminHeaders(), http.StatusOK)
	var again map[string]json.RawMessage
	if err := json.Unmarshal(againBody, &again); err != nil {
		t.Fatalf("sync-with-doc response is not JSON: %v / %s", err, againBody)
	}
	if _, ok := again["counts"]; !ok {
		t.Fatalf("/sync with learning document did not run the pack path: %s", againBody)
	}
	if _, ok := again["mastery"]; ok {
		t.Fatalf("/sync with learning document ran the restore path: %s", againBody)
	}
	if n := txlCount(t, e.db, "SELECT COUNT(*) FROM learner_events WHERE user_id = ?", kid); n != 0 {
		t.Fatalf("/sync restored %d learning events; paths are aliased", n)
	}
}

// ---------------------------------------------------------------------------
// 7) 跨机真实往返：两个独立 SQLite 库，源机导出 → 目标机 HTTP 导入
// ---------------------------------------------------------------------------

type txlSourceSeed struct {
	kid, other                                int
	userID                                    int64
	tagMastery, tagTopic, question, errorItem int64
	sessionID                                 int64
	manualErrorID, dupErrorID                 int64
	eventZeroOccurred, eventNonZeroOccurred   time.Time
}

// txlSeedSource 在源机写入：mastery、两个事件（ObjectID=0 与非 0）、一张唯一的
// taxonomy error_item FSRS 卡 + 一张 manual 卡 + 一张重复 source 卡（后两张必须
// 在导出阶段就被拒绝），以及带 overlay 的诊断会话和 3 条作答。
func txlSeedSource(t *testing.T, e *testEnv, kid, other int) txlSourceSeed {
	t.Helper()
	tagMastery := txlSeedTag(t, e.db, "源机加法", "cnm-add")
	tagTopic := txlSeedTag(t, e.db, "源机主题", "cnm-topic-a")
	tagOther := txlSeedTag(t, e.db, "别的孩子标签", "cnm-other")

	question := txlExec(t, e.db,
		`INSERT INTO knowledge_questions (external_ref, topic_id, type, prompt, answer_json)
		 VALUES ('cnm-q-add-1', ?, 'numeric', '3+4=?', '"7"')`, tagTopic)

	txlExec(t, e.db,
		`INSERT INTO kc_mastery (user_id, tag_id, correct_count, total_count, mastery, low_confidence, last_event_at)
		 VALUES (?, ?, 7, 9, 0.73, 0, '2026-03-01 09:00:00')`, kid, tagMastery)
	// 另一个孩子的数据，导出 kid 时绝不能出现。
	txlExec(t, e.db,
		`INSERT INTO kc_mastery (user_id, tag_id, correct_count, total_count, mastery, low_confidence, last_event_at)
		 VALUES (?, ?, 1, 1, 0.5, 1, '2026-03-01 09:00:00')`, other, tagOther)

	errorItem := txlExec(t, e.db,
		`INSERT INTO error_items (user_id, original_image_url, source) VALUES (?, 'src/q17.png', 'taxonomy:cnm-q-add-1')`, kid)
	manualError := txlExec(t, e.db,
		`INSERT INTO error_items (user_id, original_image_url, source) VALUES (?, 'src/manual.png', '期中考试')`, kid)
	dupError := txlExec(t, e.db,
		`INSERT INTO error_items (user_id, original_image_url, source) VALUES (?, 'src/dup1.png', 'taxonomy:cnm-q-dup')`, kid)
	txlExec(t, e.db,
		`INSERT INTO error_items (user_id, original_image_url, source) VALUES (?, 'src/dup2.png', 'taxonomy:cnm-q-dup')`, kid)

	txlExec(t, e.db,
		`INSERT INTO review_items (user_id, source_type, source_id, front_text, back_text, subject,
		     due, stability, difficulty, scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended, created_at)
		 VALUES (?, 'error_item', ?, '3+4=?', '7', 'math',
		     '2026-06-15 10:30:00', 18.25, 7.5, 12, 6, 2, 'Review', '2026-03-01 09:00:00', 2, 1, '2026-01-05 08:00:00')`,
		kid, errorItem)
	txlExec(t, e.db,
		`INSERT INTO review_items (user_id, source_type, source_id, front_text, back_text, subject,
		     due, stability, difficulty, scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended, created_at)
		 VALUES (?, 'error_item', ?, 'manual', 'x', 'math',
		     '2026-07-01 10:00:00', 1.5, 2.5, 1, 1, 0, 'New', '2026-02-01 09:00:00', 0, 0, '2026-01-01 08:00:00')`,
		kid, manualError)
	txlExec(t, e.db,
		`INSERT INTO review_items (user_id, source_type, source_id, front_text, back_text, subject,
		     due, stability, difficulty, scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended, created_at)
		 VALUES (?, 'error_item', ?, 'dup', 'x', 'math',
		     '2026-07-02 10:00:00', 2.5, 3.5, 1, 1, 0, 'New', '2026-02-02 09:00:00', 0, 0, '2026-01-02 08:00:00')`,
		kid, dupError)

	eventZeroOccurred := txlAt(2026, 3, 2, 9, 0)
	eventNonZeroOccurred := txlAt(2026, 3, 3, 9, 0)
	txlExec(t, e.db,
		`INSERT INTO learner_events (user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at)
		 VALUES (?, 'answered', 'review_item', 0, '{"success":true}', ?, ?, ?)`,
		kid, fmt.Sprintf(`{"topic_id":"cnm-topic-a","kc_ids":[%d]}`, tagTopic),
		eventZeroOccurred.UTC().Format(time.DateTime), eventZeroOccurred.UTC().Format(time.DateTime))
	txlExec(t, e.db,
		`INSERT INTO learner_events (user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at)
		 VALUES (?, 'answered', 'review_item', ?, '{"success":false}', ?, ?, ?)`,
		kid, tagMastery, fmt.Sprintf(`{"topic_id":"cnm-topic-a","kc_ids":[%d]}`, tagMastery),
		eventNonZeroOccurred.UTC().Format(time.DateTime), eventNonZeroOccurred.UTC().Format(time.DateTime))
	txlExec(t, e.db,
		`INSERT INTO learner_events (user_id, verb, object_type, object_id, occurred_at)
		 VALUES (?, 'answered', 'review_item', 0, '2026-02-01 09:00:00')`, other)

	sessionID := txlExec(t, e.db,
		`INSERT INTO diagnostic_sessions (user_id, subject, mode, status, scope_json, state_json, summary_json,
		     voided_at, voided_by, restored_at, created_at, updated_at, completed_at)
		 VALUES (?, 'math', 'diagnose', 'completed', '{"grade":2}', '{"step":3}', '{"mastered":["cnm-add"]}',
		     '2026-04-01 10:03:00', ?, '2026-04-01 10:04:00', '2026-04-01 10:00:00', '2026-04-01 10:05:00', '2026-04-01 10:05:00')`,
		kid, kid)
	// seq1/2 有 question+topic；seq3 的 refs 为空（应落 NULL，不得猜 id）。
	txlExec(t, e.db,
		`INSERT INTO diagnostic_attempts (session_id, question_id, topic_id, seq, role, answer_json, correct, status, voided_at, corrected_by_at)
		 VALUES (?, ?, ?, 1, 'probe', '"12"', 1, 'answered', NULL, NULL)`,
		sessionID, question, tagTopic)
	txlExec(t, e.db,
		`INSERT INTO diagnostic_attempts (session_id, question_id, topic_id, seq, role, answer_json, correct, status, voided_at, corrected_by_at)
		 VALUES (?, ?, ?, 2, 'probe', '"3"', 0, 'voided', '2026-04-01 10:02:00', NULL)`,
		sessionID, question, tagTopic)
	txlExec(t, e.db,
		`INSERT INTO diagnostic_attempts (session_id, question_id, topic_id, seq, role, answer_json, correct, status, voided_at, corrected_by_at)
		 VALUES (?, NULL, NULL, 3, 'confirm', '"self"', 0, 'corrected', NULL, '2026-04-01 10:05:00')`,
		sessionID)

	return txlSourceSeed{
		kid: kid, other: other, userID: int64(kid),
		tagMastery: tagMastery, tagTopic: tagTopic, question: question, errorItem: errorItem,
		sessionID: sessionID, manualErrorID: manualError, dupErrorID: dupError,
		eventZeroOccurred: eventZeroOccurred, eventNonZeroOccurred: eventNonZeroOccurred,
	}
}

type txlTargetSeed struct {
	kid, decoy                       int
	tagMastery, tagTopic, question   int64
	errorItem, manualError, dupError int64
	dummySession                     int64
}

// txlSeedTarget 在目标机写入相同 external_ref 但不同本机 id 的内容；decoy
// 用户与源机孩子同号（证明 payload.user.id 不是隐式目标），并预置一条其它
// 会话抬高 AUTOINCREMENT，确保恢复会话使用新的本机 id。
func txlSeedTarget(t *testing.T, e *testEnv, kid, decoy int) txlTargetSeed {
	t.Helper()
	decoyTag := txlSeedTag(t, e.db, "目标诱饵标签", "cnm-decoy")
	tagMastery := txlSeedTag(t, e.db, "目标机加法", "cnm-add")
	tagTopic := txlSeedTag(t, e.db, "目标机主题", "cnm-topic-a")

	decoyQuestion := txlExec(t, e.db,
		`INSERT INTO knowledge_questions (external_ref, topic_id, type, prompt, answer_json)
		 VALUES ('cnm-q-decoy', ?, 'choice', 'decoy', '[]')`, decoyTag)
	_ = decoyQuestion
	question := txlExec(t, e.db,
		`INSERT INTO knowledge_questions (external_ref, topic_id, type, prompt, answer_json)
		 VALUES ('cnm-q-add-1', ?, 'numeric', '3+4=?', '"7"')`, tagTopic)

	// 同一 source 先给诱饵用户建一条，再给目标用户建一条：恢复必须选目标用户的。
	decoyError := txlExec(t, e.db,
		`INSERT INTO error_items (user_id, original_image_url, source) VALUES (?, 'decoy/q17.png', 'taxonomy:cnm-q-add-1')`, decoy)
	_ = decoyError
	errorItem := txlExec(t, e.db,
		`INSERT INTO error_items (user_id, original_image_url, source) VALUES (?, 'tgt/q17.png', 'taxonomy:cnm-q-add-1')`, kid)
	manualError := txlExec(t, e.db,
		`INSERT INTO error_items (user_id, original_image_url, source) VALUES (?, 'tgt/manual.png', '期中考试')`, kid)
	dupError := txlExec(t, e.db,
		`INSERT INTO error_items (user_id, original_image_url, source) VALUES (?, 'tgt/dup.png', 'taxonomy:cnm-q-dup')`, kid)

	dummySession := txlExec(t, e.db,
		`INSERT INTO diagnostic_sessions (user_id, subject, mode, status, scope_json, state_json, created_at, updated_at)
		 VALUES (?, 'math', 'diagnose', 'active', '{}', '{}', '2025-01-01 00:00:00', '2025-01-01 00:00:00')`, kid)

	return txlTargetSeed{
		kid: kid, decoy: decoy, tagMastery: tagMastery, tagTopic: tagTopic, question: question,
		errorItem: errorItem, manualError: manualError, dupError: dupError, dummySession: dummySession,
	}
}

func TestTaxonomyLearningCrossMachineRoundTrip(t *testing.T) {
	// 两台完全独立的真实 SQLite 库 / 两个真实 NewRouter。
	src := setupTest(t)
	src.createAdmin(t) // id 1
	srcKid := src.createChild(t, "源机孩子")
	srcOther := src.createChild(t, "源机另一个孩子")

	dst := setupTest(t)
	dst.createAdmin(t) // id 1
	dstDecoy := dst.createChild(t, "目标机诱饵")
	dstKid := dst.createChild(t, "目标机孩子")

	if srcKid != dstDecoy {
		t.Fatalf("fixture: source kid id %d must equal target decoy id %d (proves user.id is a decoy), got mismatch", srcKid, dstDecoy)
	}
	ss := txlSeedSource(t, src, srcKid, srcOther)
	ts := txlSeedTarget(t, dst, dstKid, dstDecoy)

	// --- 源机导出（真实 HTTP）---
	exportBody, hdr := txlWantStatus(t, src, "GET",
		fmt.Sprintf("/api/taxonomy/export?user_id=%d", srcKid), nil, adminHeaders(), http.StatusOK)
	if got := hdr.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Content-Disposition/Cache-Control broken: %q", got)
	}
	var doc model.LearningExport
	if err := json.Unmarshal(exportBody, &doc); err != nil {
		t.Fatalf("decode source export: %v / %s", err, exportBody)
	}
	if doc.User.ID != int64(srcKid) || doc.TargetUserID != 0 {
		t.Fatalf("source export user/target = %d/%d, want %d/0", doc.User.ID, doc.TargetUserID, srcKid)
	}
	if len(doc.Mastery) != 1 || doc.Mastery[0].ExternalRef != "cnm-add" {
		t.Fatalf("source export mastery = %+v", doc.Mastery)
	}
	if len(doc.Events) != 2 {
		t.Fatalf("source export events = %d, want 2: %+v", len(doc.Events), doc.Events)
	}
	if doc.Events[0].ObjectID != 0 || doc.Events[1].ObjectID != ss.tagMastery {
		t.Fatalf("source event ObjectIDs must be preserved verbatim (0 / %d), got %+v", ss.tagMastery, doc.Events)
	}
	if len(doc.ReviewItems) != 1 || doc.ReviewItems[0].SourceRef != "taxonomy:cnm-q-add-1" ||
		doc.ReviewItems[0].SourceType != model.ReviewSourceErrorItem {
		t.Fatalf("source export must carry only the unique taxonomy error card, got %+v", doc.ReviewItems)
	}
	if len(doc.Diagnostics) != 1 || len(doc.Attempts) != 3 {
		t.Fatalf("source export diagnostics/attempts = %d/%d", len(doc.Diagnostics), len(doc.Attempts))
	}
	if doc.Diagnostics[0].ID != ss.sessionID {
		t.Fatalf("source session id = %d, want %d", doc.Diagnostics[0].ID, ss.sessionID)
	}

	// --- 目标机 HTTP 导入：显式 target_user_id，payload.user.id 是诱饵 ---
	doc.TargetUserID = int64(dstKid)
	importBody := txlJSON(t, doc)
	importResp, _ := txlWantStatus(t, dst, "POST", "/api/admin/taxonomy/import",
		importBody, adminHeaders(), http.StatusOK)
	var res model.LearningRestoreResult
	if err := json.Unmarshal(importResp, &res); err != nil {
		t.Fatalf("decode restore result: %v / %s", err, importResp)
	}

	t.Run("explicit target only", func(t *testing.T) {
		if doc.User.ID != int64(dstDecoy) {
			t.Fatalf("fixture: doc.user.id %d must equal decoy %d", doc.User.ID, dstDecoy)
		}
		for _, table := range []string{"kc_mastery", "learner_events", "review_items", "diagnostic_sessions"} {
			if n := txlCount(t, dst.db, "SELECT COUNT(*) FROM "+table+" WHERE user_id = ?", dstDecoy); n != 0 {
				t.Fatalf("payload.user.id=%d received %d rows in %s (implicit fallback)", dstDecoy, n, table)
			}
		}
	})

	t.Run("mastery remaps external_ref and restores values", func(t *testing.T) {
		if res.Mastery.Imported != 1 || res.Mastery.Skipped != 0 {
			t.Fatalf("mastery stat = %+v, want 1/0", res.Mastery)
		}
		if n := txlCount(t, dst.db, "SELECT COUNT(*) FROM kc_mastery WHERE user_id = ?", dstKid); n != 1 {
			t.Fatalf("target has %d mastery rows, want 1", n)
		}
		var tagID int64
		var cc, tc int
		var mastery float64
		var low int
		var last sql.NullTime
		if err := dst.db.QueryRow(
			`SELECT tag_id, correct_count, total_count, mastery, low_confidence, last_event_at
			 FROM kc_mastery WHERE user_id = ?`, dstKid).Scan(&tagID, &cc, &tc, &mastery, &low, &last); err != nil {
			t.Fatalf("read target mastery: %v", err)
		}
		if tagID != ts.tagMastery {
			t.Fatalf("mastery tag = %d, want local target tag %d (source %d / decoy %d)", tagID, ts.tagMastery, ss.tagMastery, 1)
		}
		if cc != 7 || tc != 9 || mastery != 0.73 || low != 0 {
			t.Fatalf("mastery values = %d/%d/%f/%d, want verbatim 7/9/0.73/0", cc, tc, mastery, low)
		}
		if !last.Valid || !last.Time.UTC().Equal(txlAt(2026, 3, 1, 9, 0)) {
			t.Fatalf("last_event_at = %v", last)
		}
	})

	t.Run("error item FSRS card maps to target user's row and keeps state", func(t *testing.T) {
		if res.ReviewItems.Imported != 1 || res.ReviewItems.Skipped != 0 {
			t.Fatalf("review stat = %+v, want 1/0", res.ReviewItems)
		}
		if n := txlCount(t, dst.db, "SELECT COUNT(*) FROM review_items WHERE user_id = ?", dstKid); n != 1 {
			t.Fatalf("target has %d review cards, want 1", n)
		}
		var sourceType string
		var sourceID int64
		var front, back, subject, state string
		var due, created time.Time
		var stability, difficulty float64
		var scheduledDays, reps, lapses, remainingSteps, suspended int
		var lastReview sql.NullTime
		if err := dst.db.QueryRow(
			`SELECT source_type, source_id, front_text, back_text, subject, due, stability, difficulty,
			        scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended, created_at
			 FROM review_items WHERE user_id = ?`, dstKid).Scan(
			&sourceType, &sourceID, &front, &back, &subject, &due, &stability, &difficulty,
			&scheduledDays, &reps, &lapses, &state, &lastReview, &remainingSteps, &suspended, &created); err != nil {
			t.Fatalf("read target card: %v", err)
		}
		if sourceType != model.ReviewSourceErrorItem || sourceID != ts.errorItem {
			t.Fatalf("card source = %s/%d, want error_item/%d (target user's row, not decoy)", sourceType, sourceID, ts.errorItem)
		}
		if !due.UTC().Equal(txlAt(2026, 6, 15, 10, 30)) || stability != 18.25 || difficulty != 7.5 {
			t.Fatalf("due/stability/difficulty = %v/%v/%v", due, stability, difficulty)
		}
		if scheduledDays != 12 || reps != 6 || lapses != 2 || state != model.ReviewStateReview ||
			remainingSteps != 2 || suspended != 1 {
			t.Fatalf("FSRS fields = days:%d reps:%d lapses:%d state:%s steps:%d suspended:%d",
				scheduledDays, reps, lapses, state, remainingSteps, suspended)
		}
		if !lastReview.Valid || !lastReview.Time.UTC().Equal(txlAt(2026, 3, 1, 9, 0)) ||
			!created.UTC().Equal(txlAt(2026, 1, 5, 8, 0)) {
			t.Fatalf("last_review/created_at = %v/%v", lastReview, created)
		}
		if front != "3+4=?" || back != "7" || subject != "math" {
			t.Fatalf("card text = %q/%q/%q", front, back, subject)
		}
		// manual / 重复 source 的卡在导出阶段就被拒绝，目标机上不得出现。
		for _, decoySource := range []int64{ts.manualError, ts.dupError} {
			if n := txlCount(t, dst.db, "SELECT COUNT(*) FROM review_items WHERE source_id = ?", decoySource); n != 0 {
				t.Fatalf("card wrongly attached to manual/duplicate error source %d", decoySource)
			}
		}
	})

	t.Run("event ObjectID contract", func(t *testing.T) {
		// 契约：源 ObjectID=0 → 恢复且目标 object_id=0；源 ObjectID!=0 → 显式 skipped。
		if res.Events.Imported != 1 || res.Events.Skipped != 1 {
			t.Errorf("events stat = imported %d / skipped %d, want 1/1 (non-zero source ObjectID must skip)",
				res.Events.Imported, res.Events.Skipped)
		}
		if res.Imported != 7 || res.Skipped != 1 {
			t.Errorf("totals = imported %d / skipped %d, want 7/1", res.Imported, res.Skipped)
		}
		if n := txlCount(t, dst.db, "SELECT COUNT(*) FROM learner_events WHERE user_id = ?", dstKid); n != 1 {
			t.Errorf("target event count = %d, want 1 (non-zero source ObjectID must not be restored)", n)
		}
		var objectID int64
		var contextJSON string
		err := dst.db.QueryRow(
			`SELECT object_id, COALESCE(context_json, '') FROM learner_events
			 WHERE user_id = ? AND result_json = '{"success":true}'`, dstKid).Scan(&objectID, &contextJSON)
		if err != nil {
			t.Errorf("read restored ObjectID=0 event: %v", err)
		} else {
			if objectID != 0 {
				t.Errorf("restored source ObjectID=0 event has target object_id=%d, want 0 (must not map ObjectRef into it)", objectID)
			}
			var ctx struct {
				KCIDs []int64 `json:"kc_ids"`
			}
			if err := json.Unmarshal([]byte(contextJSON), &ctx); err != nil {
				t.Errorf("context_json after remap: %q: %v", contextJSON, err)
			} else if len(ctx.KCIDs) != 1 || ctx.KCIDs[0] != ts.tagTopic {
				t.Errorf("context kc_ids = %v, want [%d] (target local id)", ctx.KCIDs, ts.tagTopic)
			}
		}
		if n := txlCount(t, dst.db,
			`SELECT COUNT(*) FROM learner_events WHERE user_id = ? AND result_json = '{"success":false}'`, dstKid); n != 0 {
			t.Errorf("source ObjectID!=0 event was restored %d times instead of skipped", n)
		}
		if n := txlCount(t, dst.db,
			`SELECT COUNT(*) FROM learner_events WHERE user_id = ? AND object_id IN (?, ?, ?)`,
			dstKid, ss.tagMastery, ss.tagTopic, ss.question); n != 0 {
			t.Errorf("%d restored events point at a source-machine local id", n)
		}
	})

	t.Run("diagnostics remap session id and keep shell/overlay", func(t *testing.T) {
		if res.Diagnostics.Imported != 1 || res.Diagnostics.Skipped != 0 {
			t.Fatalf("diagnostics stat = %+v, want 1/0", res.Diagnostics)
		}
		if n := txlCount(t, dst.db, "SELECT COUNT(*) FROM diagnostic_sessions WHERE user_id = ?", dstKid); n != 2 {
			t.Fatalf("target sessions = %d, want dummy + 1 restored", n)
		}
		var newID, userID, voidedBy int64
		var subject, mode, status, scope, state, summary string
		var voidedAt, restoredAt, created, updated, completed sql.NullTime
		err := dst.db.QueryRow(
			`SELECT id, user_id, subject, mode, status, scope_json, state_json, COALESCE(summary_json, ''),
			        COALESCE(voided_by, -1), voided_at, restored_at, created_at, updated_at, completed_at
			 FROM diagnostic_sessions WHERE user_id = ? AND id <> ?`, dstKid, ts.dummySession).Scan(
			&newID, &userID, &subject, &mode, &status, &scope, &state, &summary,
			&voidedBy, &voidedAt, &restoredAt, &created, &updated, &completed)
		if err != nil {
			t.Fatalf("read restored session: %v", err)
		}
		if newID == ss.sessionID || newID == 0 {
			t.Fatalf("restored session id = %d, must be a fresh local id (source %d)", newID, ss.sessionID)
		}
		if userID != int64(dstKid) || subject != "math" || mode != "diagnose" || status != "completed" {
			t.Fatalf("session shell = user:%d subject:%s mode:%s status:%s", userID, subject, mode, status)
		}
		if scope != `{"grade":2}` || state != `{"step":3}` || summary != `{"mastered":["cnm-add"]}` {
			t.Fatalf("scope/state/summary = %s / %s / %s", scope, state, summary)
		}
		if voidedBy != -1 {
			t.Fatalf("voided_by = %d, want NULL (source-machine id has no target mapping)", voidedBy)
		}
		if !voidedAt.Valid || !voidedAt.Time.UTC().Equal(txlAt(2026, 4, 1, 10, 3)) ||
			!restoredAt.Valid || !restoredAt.Time.UTC().Equal(txlAt(2026, 4, 1, 10, 4)) ||
			!created.Valid || !created.Time.UTC().Equal(txlAt(2026, 4, 1, 10, 0)) ||
			!updated.Valid || !updated.Time.UTC().Equal(txlAt(2026, 4, 1, 10, 5)) ||
			!completed.Valid || !completed.Time.UTC().Equal(txlAt(2026, 4, 1, 10, 5)) {
			t.Fatalf("session timestamps = voided:%v restored:%v created:%v updated:%v completed:%v",
				voidedAt, restoredAt, created, updated, completed)
		}
	})

	t.Run("attempts remap question/topic ids and keep seq/status/overlay", func(t *testing.T) {
		if res.Attempts.Imported != 3 || res.Attempts.Skipped != 0 {
			t.Fatalf("attempts stat = %+v, want 3/0", res.Attempts)
		}
		var newID int64
		if err := dst.db.QueryRow(
			`SELECT id FROM diagnostic_sessions WHERE user_id = ? AND id <> ?`, dstKid, ts.dummySession).Scan(&newID); err != nil {
			t.Fatalf("find restored session: %v", err)
		}
		if n := txlCount(t, dst.db, "SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ?", newID); n != 3 {
			t.Fatalf("restored attempts = %d, want 3", n)
		}
		rows, err := dst.db.Query(
			`SELECT seq, role, COALESCE(question_id, -1), COALESCE(topic_id, -1), answer_json, correct, status, voided_at, corrected_by_at
			 FROM diagnostic_attempts WHERE session_id = ? ORDER BY seq`, newID)
		if err != nil {
			t.Fatalf("query attempts: %v", err)
		}
		defer rows.Close()
		for rows.Next() {
			var seq, qID, tID int
			var role, answer, status string
			var correct sql.NullInt64
			var voidedAt, correctedAt sql.NullTime
			if err := rows.Scan(&seq, &role, &qID, &tID, &answer, &correct, &status, &voidedAt, &correctedAt); err != nil {
				t.Fatalf("scan attempt: %v", err)
			}
			switch seq {
			case 1:
				if qID != int(ts.question) || tID != int(ts.tagTopic) || role != "probe" ||
					answer != `"12"` || !correct.Valid || correct.Int64 != 1 || status != "answered" ||
					voidedAt.Valid || correctedAt.Valid {
					t.Errorf("attempt 1 = q%d/t%d role:%s ans:%s correct:%v status:%s voided:%v corrected:%v",
						qID, tID, role, answer, correct, status, voidedAt, correctedAt)
				}
			case 2:
				if qID != int(ts.question) || tID != int(ts.tagTopic) || role != "probe" ||
					answer != `"3"` || !correct.Valid || correct.Int64 != 0 || status != "voided" ||
					!voidedAt.Valid || !voidedAt.Time.UTC().Equal(txlAt(2026, 4, 1, 10, 2)) || correctedAt.Valid {
					t.Errorf("attempt 2 = q%d/t%d role:%s ans:%s correct:%v status:%s voided:%v corrected:%v",
						qID, tID, role, answer, correct, status, voidedAt, correctedAt)
				}
			case 3:
				if qID != -1 || tID != -1 || role != "confirm" || answer != `"self"` ||
					!correct.Valid || correct.Int64 != 0 || status != "corrected" || voidedAt.Valid ||
					!correctedAt.Valid || !correctedAt.Time.UTC().Equal(txlAt(2026, 4, 1, 10, 5)) {
					t.Errorf("attempt 3 = q%d/t%d role:%s ans:%s correct:%v status:%s voided:%v corrected:%v (empty refs must be NULL)",
						qID, tID, role, answer, correct, status, voidedAt, correctedAt)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("attempt rows: %v", err)
		}
	})

	t.Run("repeat import is idempotent", func(t *testing.T) {
		before := map[string]int{
			"mastery":     txlCount(t, dst.db, "SELECT COUNT(*) FROM kc_mastery WHERE user_id = ?", dstKid),
			"events":      txlCount(t, dst.db, "SELECT COUNT(*) FROM learner_events WHERE user_id = ?", dstKid),
			"cards":       txlCount(t, dst.db, "SELECT COUNT(*) FROM review_items WHERE user_id = ?", dstKid),
			"diagnostics": txlCount(t, dst.db, "SELECT COUNT(*) FROM diagnostic_sessions WHERE user_id = ?", dstKid),
			"attempts":    txlCount(t, dst.db, "SELECT COUNT(*) FROM diagnostic_attempts"),
		}
		secondBody, _ := txlWantStatus(t, dst, "POST", "/api/admin/taxonomy/import",
			importBody, adminHeaders(), http.StatusOK)
		var second model.LearningRestoreResult
		if err := json.Unmarshal(secondBody, &second); err != nil {
			t.Fatalf("decode second restore: %v / %s", err, secondBody)
		}
		if second.Imported != 0 {
			t.Fatalf("repeat import imported %d rows, want 0", second.Imported)
		}
		// 重复导入时，文档里的每一行（首次 imported + 首次 skipped）都应被跳过。
		if second.Skipped != res.Imported+res.Skipped {
			t.Fatalf("repeat import skipped %d, want first imported+skipped %d (counts must be real)",
				second.Skipped, res.Imported+res.Skipped)
		}
		after := map[string]int{
			"mastery":     txlCount(t, dst.db, "SELECT COUNT(*) FROM kc_mastery WHERE user_id = ?", dstKid),
			"events":      txlCount(t, dst.db, "SELECT COUNT(*) FROM learner_events WHERE user_id = ?", dstKid),
			"cards":       txlCount(t, dst.db, "SELECT COUNT(*) FROM review_items WHERE user_id = ?", dstKid),
			"diagnostics": txlCount(t, dst.db, "SELECT COUNT(*) FROM diagnostic_sessions WHERE user_id = ?", dstKid),
			"attempts":    txlCount(t, dst.db, "SELECT COUNT(*) FROM diagnostic_attempts"),
		}
		for k := range before {
			if after[k] != before[k] {
				t.Fatalf("repeat import changed %s rows: %d -> %d", k, before[k], after[k])
			}
		}
		if n := txlCount(t, dst.db, "SELECT COUNT(*) FROM learner_events WHERE user_id = ?", dstDecoy); n != 0 {
			t.Fatalf("repeat import wrote onto decoy user %d", dstDecoy)
		}
	})
}
