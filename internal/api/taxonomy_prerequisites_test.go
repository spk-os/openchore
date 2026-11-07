package api_test

// TX-PREREQ-HTTP-TESTER：先修边管理端点的真实 NewRouter + 真 SQLite +
// httptest 集成测试。路由契约由协调者裁定（不再覆盖已移除的 {id}/POST
// 死路径）：
//
//	PUT   /api/admin/taxonomy/prerequisites
//	      {topic_id, prerequisite_id, strength, reason}
//	PATCH /api/admin/taxonomy/prerequisites/review
//	      {topic_id, prerequisite_id, review_status}
//
// DB（knowledge_tag_prerequisites + ListEffectivePrerequisiteEdges）是
// durable truth；GET /api/taxonomy/topics/{ref} 的 prerequisites 是 router
// 持有的 in-memory catalog 投影（tagNeighbors 读 cat.Prereqs，不读 DB
// edges）。两者合起来判定「生效状态」有没有被失败写入污染。

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/liftedkilt/openchore/internal/taxonomy"
)

const (
	txPrereqPutURL    = "/api/admin/taxonomy/prerequisites"
	txPrereqReviewURL = "/api/admin/taxonomy/prerequisites/review"

	// 真实 pack 的先修链：cnm_p009 -> cnm_p002 -> cnm_p001。
	// 反向追加 cnm_p001 -> cnm_p009 必然成环；追加
	// cnm_p009 -> cnm_p001 只是冗余边，仍是合法 DAG。
	txPrereqRoot = "cnm_p001"
	txPrereqLeaf = "cnm_p009"
)

type txPrereqMutation struct {
	Edge struct {
		TopicID        string `json:"topic_id"`
		PrerequisiteID string `json:"prerequisite_id"`
		Strength       string `json:"strength"`
		Reason         string `json:"reason"`
		ReviewStatus   string `json:"review_status"`
	} `json:"edge"`
	EffectiveEdges int `json:"effective_edges"`
}

type txPrereqStored struct {
	Strength     string
	Reason       string
	ReviewStatus string
}

// txPrereqCall 打真实 httptest server；状态码不符时把错误体一并打印，
// 方便直接作为缺陷复现输出。
func txPrereqCall(t *testing.T, e *testEnv, method, path string, body any, headers map[string]string, want int) *http.Response {
	t.Helper()
	resp := e.request(t, method, path, body, headers)
	if resp.StatusCode != want {
		var payload map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		t.Fatalf("%s %s: want %d, got %d, body=%v", method, path, want, resp.StatusCode, payload)
	}
	return resp
}

func txPrereqStatus(t *testing.T, e *testEnv, method, path string, body any, headers map[string]string, want int) {
	t.Helper()
	txPrereqCall(t, e, method, path, body, headers, want).Body.Close()
}

func txPrereqMutationBody(t *testing.T, e *testEnv, method, path string, body any, headers map[string]string) txPrereqMutation {
	t.Helper()
	resp := txPrereqCall(t, e, method, path, body, headers, http.StatusOK)
	var out txPrereqMutation
	decodeBody(t, resp, &out)
	return out
}

func txPrereqErrorMsg(t *testing.T, resp *http.Response) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	decodeBody(t, resp, &payload)
	return payload.Error
}

// txPrereqStoredRow 读未被 effective 过滤的裸行：rejected 行必须仍在库。
func txPrereqStoredRow(t *testing.T, e *testEnv, topicRef, prereqRef string) (txPrereqStored, bool) {
	t.Helper()
	var row txPrereqStored
	err := e.db.QueryRow(`
		SELECT e.strength, e.reason, e.review_status
		  FROM knowledge_tag_prerequisites e
		  JOIN knowledge_tags tt ON tt.id = e.topic_id
		  JOIN knowledge_tags pt ON pt.id = e.prerequisite_id
		 WHERE tt.external_ref = ? AND pt.external_ref = ?`, topicRef, prereqRef).
		Scan(&row.Strength, &row.Reason, &row.ReviewStatus)
	if err == sql.ErrNoRows {
		return txPrereqStored{}, false
	}
	if err != nil {
		t.Fatalf("read stored edge %s<-%s: %v", topicRef, prereqRef, err)
	}
	return row, true
}

func txPrereqEffective(t *testing.T, e *testEnv) []*taxonomy.Edge {
	t.Helper()
	edges, err := e.store.ListEffectivePrerequisiteEdges(t.Context())
	if err != nil {
		t.Fatalf("list effective edges: %v", err)
	}
	return edges
}

func txPrereqHasEdge(edges []*taxonomy.Edge, topicRef, prereqRef string) bool {
	for _, e := range edges {
		if e.TopicID == topicRef && e.PrerequisiteID == prereqRef {
			return true
		}
	}
	return false
}

// txPrereqCatalogPrereqs 通过真实 GET /topics/{ref} 观察 in-memory catalog。
func txPrereqCatalogPrereqs(t *testing.T, e *testEnv, ref string) map[string]bool {
	t.Helper()
	resp := txPrereqCall(t, e, "GET", "/api/taxonomy/topics/"+ref, nil, adminHeaders(), http.StatusOK)
	var detail struct {
		Prerequisites []struct {
			ExternalRef string `json:"external_ref"`
		} `json:"prerequisites"`
	}
	decodeBody(t, resp, &detail)
	out := make(map[string]bool, len(detail.Prerequisites))
	for _, p := range detail.Prerequisites {
		out[p.ExternalRef] = true
	}
	return out
}

// 权限：未登录 401、孩子 403、admin 通过真实 NewRouter 路由成功。
func TestTaxonomyPrerequisiteAuth(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "先修权限")
	importPack(t, e)

	put := map[string]any{
		"topic_id": txPrereqLeaf, "prerequisite_id": txPrereqRoot,
		"strength": "hard", "reason": "权限测试：admin 写入",
	}
	review := map[string]any{
		"topic_id": txPrereqLeaf, "prerequisite_id": txPrereqRoot,
		"review_status": "reviewed",
	}

	t.Run("anonymous_401", func(t *testing.T) {
		txPrereqStatus(t, e, "PUT", txPrereqPutURL, put, nil, http.StatusUnauthorized)
		txPrereqStatus(t, e, "PATCH", txPrereqReviewURL, review, nil, http.StatusUnauthorized)
	})
	t.Run("child_403", func(t *testing.T) {
		txPrereqStatus(t, e, "PUT", txPrereqPutURL, put, childHeaders(kid), http.StatusForbidden)
		txPrereqStatus(t, e, "PATCH", txPrereqReviewURL, review, childHeaders(kid), http.StatusForbidden)
	})
	if _, ok := txPrereqStoredRow(t, e, txPrereqLeaf, txPrereqRoot); ok {
		t.Fatal("401/403 请求不应写入先修边")
	}

	t.Run("admin_200", func(t *testing.T) {
		got := txPrereqMutationBody(t, e, "PUT", txPrereqPutURL, put, adminHeaders())
		if got.Edge.TopicID != txPrereqLeaf || got.Edge.PrerequisiteID != txPrereqRoot ||
			got.Edge.Strength != "hard" || got.Edge.Reason != "权限测试：admin 写入" ||
			got.Edge.ReviewStatus != "reviewed" {
			t.Fatalf("PUT response edge = %+v", got.Edge)
		}
		effective := txPrereqEffective(t, e)
		if got.EffectiveEdges != len(effective) || !txPrereqHasEdge(effective, txPrereqLeaf, txPrereqRoot) {
			t.Fatalf("PUT effective_edges = %d, DB = %d", got.EffectiveEdges, len(effective))
		}

		patched := txPrereqMutationBody(t, e, "PATCH", txPrereqReviewURL, review, adminHeaders())
		if patched.Edge.ReviewStatus != "reviewed" || patched.Edge.TopicID != txPrereqLeaf ||
			patched.Edge.PrerequisiteID != txPrereqRoot {
			t.Fatalf("PATCH response edge = %+v", patched.Edge)
		}
		if patched.Edge.Strength != "" || patched.Edge.Reason != "" {
			t.Fatalf("PATCH 不应回带 strength/reason: %+v", patched.Edge)
		}
		if row, ok := txPrereqStoredRow(t, e, txPrereqLeaf, txPrereqRoot); !ok ||
			row.ReviewStatus != "reviewed" || row.Strength != "hard" {
			t.Fatalf("stored edge = %+v, exists=%v", row, ok)
		}
	})
}

// 非法 ref（空/空白/缺失）/ 自环 / 非法 strength / 非法 review_status → 400。
func TestTaxonomyPrerequisiteInvalidInput(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	h := adminHeaders()
	before := len(txPrereqEffective(t, e))

	putCases := []struct {
		name string
		body map[string]any
	}{
		{"blank_topic", map[string]any{"topic_id": "  ", "prerequisite_id": txPrereqRoot, "strength": "hard", "reason": ""}},
		{"missing_prerequisite", map[string]any{"topic_id": txPrereqLeaf, "strength": "hard", "reason": ""}},
		{"bad_strength", map[string]any{"topic_id": txPrereqLeaf, "prerequisite_id": txPrereqRoot, "strength": "critical", "reason": ""}},
		{"self_loop", map[string]any{"topic_id": txPrereqRoot, "prerequisite_id": txPrereqRoot, "strength": "hard", "reason": ""}},
	}
	for _, tc := range putCases {
		t.Run("put_"+tc.name, func(t *testing.T) {
			txPrereqStatus(t, e, "PUT", txPrereqPutURL, tc.body, h, http.StatusBadRequest)
		})
	}
	reviewCases := []struct {
		name string
		body map[string]any
	}{
		{"blank_ref", map[string]any{"topic_id": txPrereqLeaf, "prerequisite_id": " ", "review_status": "rejected"}},
		{"missing_status", map[string]any{"topic_id": txPrereqLeaf, "prerequisite_id": txPrereqRoot}},
		{"bad_status", map[string]any{"topic_id": txPrereqLeaf, "prerequisite_id": txPrereqRoot, "review_status": "machine"}},
	}
	for _, tc := range reviewCases {
		t.Run("patch_"+tc.name, func(t *testing.T) {
			txPrereqStatus(t, e, "PATCH", txPrereqReviewURL, tc.body, h, http.StatusBadRequest)
		})
	}

	if after := len(txPrereqEffective(t, e)); after != before {
		t.Fatalf("400 请求改变了 effective 边数: %d -> %d", before, after)
	}
	if _, ok := txPrereqStoredRow(t, e, txPrereqLeaf, txPrereqRoot); ok {
		t.Fatal("400 请求不应写入先修边")
	}
}

// unknown ref / 无存储边：按生产冻结映射锁定 404（见返回摘要里的契约歧义说明）。
func TestTaxonomyPrerequisiteUnknownRef(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	h := adminHeaders()

	t.Run("put_unknown_topic", func(t *testing.T) {
		resp := txPrereqCall(t, e, "PUT", txPrereqPutURL, map[string]any{
			"topic_id": "cnm_missing_topic", "prerequisite_id": txPrereqRoot,
			"strength": "hard", "reason": "",
		}, h, http.StatusNotFound)
		if msg := txPrereqErrorMsg(t, resp); !strings.Contains(msg, "unknown topic_id") {
			t.Fatalf("error = %q", msg)
		}
	})
	t.Run("put_unknown_prerequisite", func(t *testing.T) {
		resp := txPrereqCall(t, e, "PUT", txPrereqPutURL, map[string]any{
			"topic_id": txPrereqLeaf, "prerequisite_id": "cnm_missing_prereq",
			"strength": "hard", "reason": "",
		}, h, http.StatusNotFound)
		if msg := txPrereqErrorMsg(t, resp); !strings.Contains(msg, "unknown prerequisite_id") {
			t.Fatalf("error = %q", msg)
		}
	})
	t.Run("patch_unknown_ref", func(t *testing.T) {
		resp := txPrereqCall(t, e, "PATCH", txPrereqReviewURL, map[string]any{
			"topic_id": "cnm_missing_topic", "prerequisite_id": txPrereqRoot,
			"review_status": "rejected",
		}, h, http.StatusNotFound)
		if msg := txPrereqErrorMsg(t, resp); !strings.Contains(msg, "unknown topic_id or prerequisite_id") {
			t.Fatalf("error = %q", msg)
		}
	})
	t.Run("patch_no_stored_edge", func(t *testing.T) {
		resp := txPrereqCall(t, e, "PATCH", txPrereqReviewURL, map[string]any{
			"topic_id": txPrereqRoot, "prerequisite_id": txPrereqLeaf,
			"review_status": "rejected",
		}, h, http.StatusNotFound)
		if msg := txPrereqErrorMsg(t, resp); !strings.Contains(msg, "prerequisite edge not found") {
			t.Fatalf("error = %q", msg)
		}
	})
}

// 成环 PUT 返回 409；proposed WithEdges 失败不得污染 DB、effective 与
// catalog，且 catalog 仍是 last good graph（后续合法写入仍成功）。
func TestTaxonomyPrerequisiteCycleDoesNotPollute(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	h := adminHeaders()

	before := txPrereqEffective(t, e)
	beforeCount := len(before)
	if txPrereqHasEdge(before, txPrereqRoot, txPrereqLeaf) {
		t.Fatal("pack 基线不应含反向边 cnm_p001<-cnm_p009")
	}
	if prereqs := txPrereqCatalogPrereqs(t, e, txPrereqRoot); len(prereqs) != 0 {
		t.Fatalf("cnm_p001 基线 prerequisites = %v, want empty", prereqs)
	}

	cycle := map[string]any{
		"topic_id": txPrereqRoot, "prerequisite_id": txPrereqLeaf,
		"strength": "hard", "reason": "成环候选：必须被 WithEdges 拒绝",
	}
	resp := txPrereqCall(t, e, "PUT", txPrereqPutURL, cycle, h, http.StatusConflict)
	msg := txPrereqErrorMsg(t, resp)
	if !strings.Contains(msg, "conflicts with the graph") || !strings.Contains(msg, "先修图存在环") {
		t.Fatalf("409 error = %q", msg)
	}

	if row, ok := txPrereqStoredRow(t, e, txPrereqRoot, txPrereqLeaf); ok {
		t.Fatalf("409 后 DB 被污染: %+v", row)
	}
	if after := txPrereqEffective(t, e); len(after) != beforeCount ||
		txPrereqHasEdge(after, txPrereqRoot, txPrereqLeaf) {
		t.Fatalf("409 后 effective 被污染: len=%d (before %d)", len(after), beforeCount)
	}
	if prereqs := txPrereqCatalogPrereqs(t, e, txPrereqRoot); len(prereqs) != 0 {
		t.Fatalf("409 后 catalog 被污染: cnm_p001 prerequisites = %v", prereqs)
	}

	// catalog 仍是 last good graph：一条合法边仍能通过 WithEdges 并 swap。
	valid := map[string]any{
		"topic_id": txPrereqLeaf, "prerequisite_id": txPrereqRoot,
		"strength": "soft", "reason": "409 之后的合法写入",
	}
	got := txPrereqMutationBody(t, e, "PUT", txPrereqPutURL, valid, h)
	if got.EffectiveEdges != beforeCount+1 {
		t.Fatalf("合法 PUT effective_edges = %d, want %d", got.EffectiveEdges, beforeCount+1)
	}
	if !txPrereqHasEdge(txPrereqEffective(t, e), txPrereqLeaf, txPrereqRoot) {
		t.Fatal("合法边未进入 effective")
	}
	if prereqs := txPrereqCatalogPrereqs(t, e, txPrereqLeaf); !prereqs[txPrereqRoot] {
		t.Fatalf("合法边未进入 catalog: cnm_p009 prerequisites = %v", prereqs)
	}
}

// rejected 不进 effective / catalog；rejected 后 admin 重新 Put 写 reviewed
// 且 strength/reason 整体替换（不复活旧 payload）。
func TestTaxonomyPrerequisiteRejectedAndReput(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	h := adminHeaders()

	beforeCount := len(txPrereqEffective(t, e))
	first := map[string]any{
		"topic_id": txPrereqLeaf, "prerequisite_id": txPrereqRoot,
		"strength": "hard", "reason": "首次录入：hard 先修",
	}
	put1 := txPrereqMutationBody(t, e, "PUT", txPrereqPutURL, first, h)
	if put1.EffectiveEdges != beforeCount+1 || put1.Edge.ReviewStatus != "reviewed" {
		t.Fatalf("first PUT = %+v", put1)
	}
	if row, ok := txPrereqStoredRow(t, e, txPrereqLeaf, txPrereqRoot); !ok ||
		row.ReviewStatus != "reviewed" || row.Strength != "hard" || row.Reason != "首次录入：hard 先修" {
		t.Fatalf("first stored = %+v, exists=%v", row, ok)
	}
	if prereqs := txPrereqCatalogPrereqs(t, e, txPrereqLeaf); !prereqs[txPrereqRoot] {
		t.Fatalf("first PUT 未进 catalog: %v", prereqs)
	}

	review := map[string]any{
		"topic_id": txPrereqLeaf, "prerequisite_id": txPrereqRoot,
		"review_status": "rejected",
	}
	rejected := txPrereqMutationBody(t, e, "PATCH", txPrereqReviewURL, review, h)
	if rejected.Edge.ReviewStatus != "rejected" || rejected.EffectiveEdges != beforeCount {
		t.Fatalf("rejected PATCH = %+v (before %d)", rejected, beforeCount)
	}
	// rejected 行保留在库（audit），但不进 DB effective，也不进 catalog。
	row, ok := txPrereqStoredRow(t, e, txPrereqLeaf, txPrereqRoot)
	if !ok || row.ReviewStatus != "rejected" {
		t.Fatalf("rejected 行应保留: %+v, exists=%v", row, ok)
	}
	if row.Strength != "hard" || row.Reason != "首次录入：hard 先修" {
		t.Fatalf("rejected 不应改写 strength/reason: %+v", row)
	}
	if txPrereqHasEdge(txPrereqEffective(t, e), txPrereqLeaf, txPrereqRoot) {
		t.Fatal("rejected 边仍进入 DB effective")
	}
	if prereqs := txPrereqCatalogPrereqs(t, e, txPrereqLeaf); prereqs[txPrereqRoot] {
		t.Fatalf("rejected 边仍进入 catalog: %v", prereqs)
	}

	// admin 重新 Put：reviewed 且 strength/reason 整体替换。
	reput := map[string]any{
		"topic_id": txPrereqLeaf, "prerequisite_id": txPrereqRoot,
		"strength": "soft", "reason": "改判后重录：soft 先修",
	}
	put2 := txPrereqMutationBody(t, e, "PUT", txPrereqPutURL, reput, h)
	if put2.Edge.ReviewStatus != "reviewed" || put2.Edge.Strength != "soft" ||
		put2.Edge.Reason != "改判后重录：soft 先修" || put2.EffectiveEdges != beforeCount+1 {
		t.Fatalf("re-PUT = %+v", put2)
	}
	row, ok = txPrereqStoredRow(t, e, txPrereqLeaf, txPrereqRoot)
	if !ok || row.ReviewStatus != "reviewed" || row.Strength != "soft" ||
		row.Reason != "改判后重录：soft 先修" || strings.Contains(row.Reason, "首次录入") {
		t.Fatalf("re-PUT 未整体替换: %+v, exists=%v", row, ok)
	}
	if !txPrereqHasEdge(txPrereqEffective(t, e), txPrereqLeaf, txPrereqRoot) {
		t.Fatal("re-PUT 未重回 DB effective")
	}
	if prereqs := txPrereqCatalogPrereqs(t, e, txPrereqLeaf); !prereqs[txPrereqRoot] {
		t.Fatalf("re-PUT 未重回 catalog: %v", prereqs)
	}
}
