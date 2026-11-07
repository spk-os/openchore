package api_test

// TX-GRAPH-API-TESTER：知识图谱邻域端点的真实 HTTP 契约测试。
// 复用 setupTest 的真实 SQLite（042 迁移 + 内嵌 pack 全量导入）与
// httptest，自建 RequireUserOrToken 中间件包装
// NewTaxonomyHandler.GetGraph；不依赖 router.go 里另一张卡正在补的
// 路由注册。全程无 mock、无假数据返回。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/liftedkilt/openchore/internal/api"
)

const (
	graphRootRef = "cnm_p001" // pack 锚点：math / grade 1 / 数与代数
	graphCap     = 250        // 冻结契约：graphMaxNodes
)

// --- 响应投影：测试侧独立声明，与 api 包的 graphResponse 形状一致 ---
// Mastery 用指针，才能区分 JSON null（无 kc_mastery 行）与真实的 0。

type graphTestNode struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Subject string   `json:"subject"`
	Grade   *int     `json:"grade"`
	Domain  string   `json:"domain"`
	Chapter string   `json:"chapter"`
	Mastery *float64 `json:"mastery"`
}

type graphTestEdge struct {
	TopicID        string `json:"topic_id"`
	PrerequisiteID string `json:"prerequisite_id"`
	Strength       string `json:"strength"`
	Reason         string `json:"reason"`
	ReviewStatus   string `json:"review_status"`
}

type graphTestResponse struct {
	Nodes     []graphTestNode `json:"nodes"`
	Edges     []graphTestEdge `json:"edges"`
	Truncated bool            `json:"truncated"`
}

type graphEnv struct {
	env     *testEnv
	srv     *httptest.Server
	handler *api.TaxonomyHandler
}

// setupGraph 起一个只服务 GetGraph 的 httptest server：真实 DB、真实
// pack、真实会话中间件。路由尚未注册不影响本卡。
func setupGraph(t *testing.T) *graphEnv {
	t.Helper()
	e := setupTest(t)
	e.createAdmin(t)
	h := api.NewTaxonomyHandler(e.store, nil)
	if _, err := h.ImportPack(t.Context()); err != nil {
		t.Fatalf("pack import failed: %v", err)
	}
	sessions := api.NewSessionManager(testSessionSecret)
	srv := httptest.NewServer(api.RequireUserOrToken(e.store, sessions)(http.HandlerFunc(h.GetGraph)))
	t.Cleanup(srv.Close)
	return &graphEnv{env: e, srv: srv, handler: h}
}

func graphV(pairs ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.Set(pairs[i], pairs[i+1])
	}
	return v
}

func graphWith(v url.Values, pairs ...string) url.Values {
	out := url.Values{}
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	for i := 0; i+1 < len(pairs); i += 2 {
		out.Set(pairs[i], pairs[i+1])
	}
	return out
}

// graphGet 发一次真实 GET，校验状态码并返回解码结果与原始字节
// （原始字节用于断言 mastery:null 与截断稳定性）。
func graphGet(t *testing.T, srv *httptest.Server, q url.Values, headers map[string]string, want int) (graphTestResponse, []byte) {
	t.Helper()
	target := srv.URL + "/api/taxonomy/graph"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != want {
		t.Fatalf("GET %s: want %d, got %d, body=%s", target, want, resp.StatusCode, raw)
	}
	if want != http.StatusOK {
		if !bytes.Contains(raw, []byte(`"error"`)) {
			t.Fatalf("error response is not JSON error: %s", raw)
		}
		return graphTestResponse{}, raw
	}
	var out graphTestResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode graph response: %v (body=%s)", err, raw)
	}
	if out.Nodes == nil || out.Edges == nil {
		t.Fatalf("nodes/edges must be [] not null: %s", raw)
	}
	return out, raw
}

func graphNodeSet(t *testing.T, out graphTestResponse) map[string]bool {
	t.Helper()
	m := make(map[string]bool, len(out.Nodes))
	for _, n := range out.Nodes {
		if m[n.ID] {
			t.Fatalf("duplicate node %s in graph response", n.ID)
		}
		m[n.ID] = true
	}
	return m
}

func graphEdgeKey(e graphTestEdge) [2]string {
	return [2]string{e.TopicID, e.PrerequisiteID}
}

// graphCompareEdges 逐字段比对 served edges 与 DB 有效边集合（含方向、
// strength/reason/review_status 透传），并拒绝重复边。
func graphCompareEdges(t *testing.T, got, want []graphTestEdge) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("edges = %d, want %d", len(got), len(want))
	}
	gm := make(map[[2]string]graphTestEdge, len(got))
	for _, e := range got {
		k := graphEdgeKey(e)
		if _, dup := gm[k]; dup {
			t.Fatalf("duplicate served edge %v", k)
		}
		gm[k] = e
	}
	for _, w := range want {
		g, ok := gm[graphEdgeKey(w)]
		if !ok {
			t.Errorf("missing edge topic=%s prereq=%s", w.TopicID, w.PrerequisiteID)
			continue
		}
		if g != w {
			t.Errorf("edge %v = %+v, want %+v", graphEdgeKey(w), g, w)
		}
	}
}

// graphEffectiveEdges 直接用 SQL 读 DB 作为独立 oracle，不复用生产 store
// 方法：rejected 与空 external_ref 端点的边必须不在这里。
func graphEffectiveEdges(t *testing.T, e *testEnv) []graphTestEdge {
	t.Helper()
	rows, err := e.db.Query(`SELECT tt.external_ref, pt.external_ref, e.strength, e.reason, e.review_status
		FROM knowledge_tag_prerequisites e
		JOIN knowledge_tags tt ON tt.id = e.topic_id
		JOIN knowledge_tags pt ON pt.id = e.prerequisite_id
		WHERE e.review_status <> 'rejected'
		  AND COALESCE(tt.external_ref, '') <> ''
		  AND COALESCE(pt.external_ref, '') <> ''
		ORDER BY e.topic_id, e.prerequisite_id`)
	if err != nil {
		t.Fatalf("query effective edges: %v", err)
	}
	defer rows.Close()
	out := []graphTestEdge{}
	for rows.Next() {
		var x graphTestEdge
		if err := rows.Scan(&x.TopicID, &x.PrerequisiteID, &x.Strength, &x.Reason, &x.ReviewStatus); err != nil {
			t.Fatalf("scan effective edge: %v", err)
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate effective edges: %v", err)
	}
	return out
}

// graphEdgesWithin 是「只服务两端都在 kept 内的边」的期望集合。
func graphEdgesWithin(all []graphTestEdge, kept map[string]bool) []graphTestEdge {
	out := []graphTestEdge{}
	for _, e := range all {
		if kept[e.TopicID] && kept[e.PrerequisiteID] {
			out = append(out, e)
		}
	}
	return out
}

func graphAssertNodeSet(t *testing.T, out graphTestResponse, want map[string]bool) {
	t.Helper()
	got := graphNodeSet(t, out)
	if len(got) != len(want) {
		t.Fatalf("nodes = %d, want %d (got %v)", len(got), len(want), got)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("node %s missing from graph", id)
		}
	}
}

func graphExec(t *testing.T, e *testEnv, query string, args ...any) {
	t.Helper()
	if _, err := e.db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func graphTagID(t *testing.T, e *testEnv, ref string) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRow(`SELECT id FROM knowledge_tags WHERE external_ref = ?`, ref).Scan(&id); err != nil {
		t.Fatalf("tag id for %s: %v", ref, err)
	}
	return id
}

// graphInsertEdge 插一条指定方向的有效/被拒边（fixture，非生产写入路径）。
func graphInsertEdge(t *testing.T, e *testEnv, topicRef, prereqRef, strength, status string) {
	t.Helper()
	graphExec(t, e, `INSERT INTO knowledge_tag_prerequisites
		(topic_id, prerequisite_id, strength, reason, review_status) VALUES (?, ?, ?, ?, ?)`,
		graphTagID(t, e, topicRef), graphTagID(t, e, prereqRef), strength, "graph fixture", status)
}

func graphSetEdgeStatus(t *testing.T, e *testEnv, topicRef, prereqRef, status string) {
	t.Helper()
	graphExec(t, e, `UPDATE knowledge_tag_prerequisites SET review_status = ?
		WHERE topic_id = ? AND prerequisite_id = ?`, status,
		graphTagID(t, e, topicRef), graphTagID(t, e, prereqRef))
}

// TestTaxonomyGraphAuthAndParams 覆盖权限/resolveTarget 与全部入参校验。
func TestTaxonomyGraphAuthAndParams(t *testing.T) {
	g := setupGraph(t)
	kid := g.env.createChild(t, "图谱小明")
	other := g.env.createChild(t, "图谱小红")
	base := graphV("root", graphRootRef)
	kidH := childHeaders(kid)

	t.Run("unauthenticated is 401", func(t *testing.T) {
		graphGet(t, g.srv, base, nil, http.StatusUnauthorized)
	})

	t.Run("kid resolves to self", func(t *testing.T) {
		out, _ := graphGet(t, g.srv, base, kidH, http.StatusOK)
		if len(out.Nodes) == 0 || !graphNodeSet(t, out)[graphRootRef] {
			t.Fatalf("kid graph missing root: %+v", out.Nodes)
		}
	})

	t.Run("kid cannot act as another user", func(t *testing.T) {
		graphGet(t, g.srv, graphWith(base, "user_id", fmt.Sprint(other)), kidH, http.StatusForbidden)
	})

	t.Run("kid naming self is fine", func(t *testing.T) {
		graphGet(t, g.srv, graphWith(base, "user_id", fmt.Sprint(kid)), kidH, http.StatusOK)
	})

	t.Run("admin must name a user", func(t *testing.T) {
		graphGet(t, g.srv, base, adminHeaders(), http.StatusBadRequest)
	})

	t.Run("admin names a real kid", func(t *testing.T) {
		graphGet(t, g.srv, graphWith(base, "user_id", fmt.Sprint(kid)), adminHeaders(), http.StatusOK)
	})

	t.Run("admin unknown or malformed user_id is 400", func(t *testing.T) {
		// 逐值独立子测试：一个值失败不会掩盖其余值的判定结果。
		for _, bad := range []string{"999999", "abc", "-1", "0"} {
			t.Run("user_id="+bad, func(t *testing.T) {
				graphGet(t, g.srv, graphWith(base, "user_id", bad), adminHeaders(), http.StatusBadRequest)
			})
		}
	})

	t.Run("root is required and trimmed", func(t *testing.T) {
		graphGet(t, g.srv, graphV(), kidH, http.StatusBadRequest)
		graphGet(t, g.srv, graphV("root", "   "), kidH, http.StatusBadRequest)
		out, _ := graphGet(t, g.srv, graphV("root", "  cnm_p001  "), kidH, http.StatusOK)
		if !graphNodeSet(t, out)[graphRootRef] {
			t.Fatalf("trimmed root not resolved: %v", graphNodeSet(t, out))
		}
	})

	t.Run("unknown root is 404", func(t *testing.T) {
		graphGet(t, g.srv, graphV("root", "no_such_topic"), kidH, http.StatusNotFound)
	})

	t.Run("depth validation and default", func(t *testing.T) {
		for _, bad := range []string{"0", "3", "-1", "abc", "2.0"} {
			graphGet(t, g.srv, graphWith(base, "depth", bad), kidH, http.StatusBadRequest)
		}
		d1, _ := graphGet(t, g.srv, base, kidH, http.StatusOK) // 省略 = 1
		one, _ := graphGet(t, g.srv, graphWith(base, "depth", "1"), kidH, http.StatusOK)
		two, _ := graphGet(t, g.srv, graphWith(base, "depth", "2"), kidH, http.StatusOK)
		if len(d1.Nodes) != len(one.Nodes) || len(d1.Nodes) != 8 {
			t.Fatalf("default depth nodes = %d, depth=1 nodes = %d, want 8", len(d1.Nodes), len(one.Nodes))
		}
		if len(two.Nodes) != 19 {
			t.Fatalf("depth=2 nodes = %d, want 19", len(two.Nodes))
		}
		if two.Truncated {
			t.Errorf("small graph must not be truncated")
		}
	})

	t.Run("grade validation", func(t *testing.T) {
		graphGet(t, g.srv, graphWith(base, "grade", "abc"), kidH, http.StatusBadRequest)
		graphGet(t, g.srv, graphWith(base, "grade", " 1"), kidH, http.StatusBadRequest)
		// 数值但与 root 矛盾：root 自身也必须匹配过滤条件。
		graphGet(t, g.srv, graphWith(base, "grade", "99"), kidH, http.StatusBadRequest)
		out, _ := graphGet(t, g.srv, graphWith(base, "grade", "1"), kidH, http.StatusOK)
		for _, n := range out.Nodes {
			if n.Grade == nil || *n.Grade != 1 {
				t.Errorf("grade filter leaked node %s grade=%v", n.ID, n.Grade)
			}
		}
	})

	t.Run("root contradicting filters is 400", func(t *testing.T) {
		for _, q := range []url.Values{
			graphWith(base, "subject", "english"),
			graphWith(base, "domain", "语音"),
			graphWith(base, "grade", "3"),
			graphWith(base, "subject", "math", "domain", "图形与几何", "grade", "1"),
		} {
			graphGet(t, g.srv, q, kidH, http.StatusBadRequest)
		}
	})

	t.Run("subject and domain filters are trimmed and projected", func(t *testing.T) {
		out, _ := graphGet(t, g.srv, graphWith(base, "subject", "  math  "), kidH, http.StatusOK)
		if len(out.Nodes) != 8 {
			t.Fatalf("subject-filtered depth1 nodes = %d, want 8", len(out.Nodes))
		}
		for _, n := range out.Nodes {
			if n.Subject != "math" {
				t.Errorf("node %s subject = %q, want math", n.ID, n.Subject)
			}
		}
		out, _ = graphGet(t, g.srv, graphWith(base, "domain", "  数与代数  "), kidH, http.StatusOK)
		if len(out.Nodes) != 6 { // root + 5 个同 domain 的一跳邻居
			t.Fatalf("domain-filtered depth1 nodes = %d, want 6", len(out.Nodes))
		}
		for _, n := range out.Nodes {
			if n.Domain != "数与代数" {
				t.Errorf("node %s domain = %q, want 数与代数", n.ID, n.Domain)
			}
		}
		for _, n := range out.Nodes {
			if n.ID == graphRootRef {
				if n.Name != "1到5各数的认识" || n.Chapter == "" || n.Grade == nil || *n.Grade != 1 {
					t.Errorf("root node projection wrong: %+v", n)
				}
			}
		}
	})
}

// TestTaxonomyGraphBFSAndDirection 用 pack 硬锚点锁定无向 BFS 的分层
// 结果、只保留 kept 内边、以及 served edge 保持 topic→prerequisite 方向。
func TestTaxonomyGraphBFSAndDirection(t *testing.T) {
	g := setupGraph(t)
	kid := g.env.createChild(t, "图谱小方")
	h := childHeaders(kid)
	allEdges := graphEffectiveEdges(t, g.env)

	d1 := []string{"cnm_p002", "cnm_p003", "cnm_p004", "cnm_p005", "cnm_p008", "cnm_p014", "cnm_p015"}
	d2 := []string{"cnm_p006", "cnm_p009", "cnm_p010", "cnm_p016", "cnm_p022", "cnm_p023",
		"cnm_p040", "cnm_p049", "cnm_p221", "cnm_p222", "cnm_p254"}

	t.Run("depth1 exact neighborhood", func(t *testing.T) {
		out, _ := graphGet(t, g.srv, graphV("root", graphRootRef), h, http.StatusOK)
		want := map[string]bool{graphRootRef: true}
		for _, id := range d1 {
			want[id] = true
		}
		graphAssertNodeSet(t, out, want)
		graphCompareEdges(t, out.Edges, graphEdgesWithin(allEdges, want))
		if len(out.Edges) != 7 {
			t.Fatalf("depth1 edges = %d, want 7", len(out.Edges))
		}
		// pack 里 root 只作为 prerequisite 端出现。
		for _, e := range out.Edges {
			if e.PrerequisiteID != graphRootRef || e.TopicID == graphRootRef {
				t.Errorf("depth1 edge direction wrong: %+v", e)
			}
		}
	})

	t.Run("depth2 exact neighborhood", func(t *testing.T) {
		out, _ := graphGet(t, g.srv, graphV("root", graphRootRef, "depth", "2"), h, http.StatusOK)
		want := map[string]bool{graphRootRef: true}
		for _, id := range append(append([]string{}, d1...), d2...) {
			want[id] = true
		}
		graphAssertNodeSet(t, out, want)
		graphCompareEdges(t, out.Edges, graphEdgesWithin(allEdges, want))
		// 每条 served edge 的两端必须都在 kept 内（远端节点不能借边漏出）。
		kept := graphNodeSet(t, out)
		for _, e := range out.Edges {
			if !kept[e.TopicID] || !kept[e.PrerequisiteID] {
				t.Errorf("edge escapes kept set: %+v", e)
			}
		}
	})

	t.Run("stored direction kept on undirected expansion", func(t *testing.T) {
		// cnm_p002 的无向邻域：先修 cnm_p001（root 在 prerequisite 端）
		// 与后继 cnm_p009（root 在 topic 端）。
		out, _ := graphGet(t, g.srv, graphV("root", "cnm_p002"), h, http.StatusOK)
		want := map[string]bool{"cnm_p001": true, "cnm_p002": true, "cnm_p009": true}
		graphAssertNodeSet(t, out, want)
		graphCompareEdges(t, out.Edges, graphEdgesWithin(allEdges, want))
		if len(out.Edges) != 2 {
			t.Fatalf("cnm_p002 edges = %d, want 2", len(out.Edges))
		}
		byKey := map[[2]string]bool{}
		for _, e := range out.Edges {
			byKey[graphEdgeKey(e)] = true
			if e.TopicID == "cnm_p001" {
				t.Errorf("prerequisite endpoint served as topic (direction flipped): %+v", e)
			}
			if e.TopicID == "cnm_p009" && e.PrerequisiteID == "cnm_p002" {
				// 正确的后继边，下面用集合断言。
			}
		}
		if !byKey[[2]string{"cnm_p002", "cnm_p001"}] || !byKey[[2]string{"cnm_p009", "cnm_p002"}] {
			t.Errorf("wrong edge set (reversed or missing): %v", byKey)
		}
		if byKey[[2]string{"cnm_p001", "cnm_p002"}] || byKey[[2]string{"cnm_p002", "cnm_p009"}] {
			t.Errorf("reversed edge leaked: %v", byKey)
		}
	})
}

// graphResetFixture 把 fixture 造成的边/标签/状态恢复为导入后的基线，
// 让同一 DB 上的子测试互相独立。
func graphResetFixture(t *testing.T, e *testEnv) {
	t.Helper()
	graphExec(t, e, `DELETE FROM knowledge_tags WHERE external_ref IS NULL AND name LIKE 'graph fixture%'`)
	graphExec(t, e, `DELETE FROM knowledge_tag_prerequisites WHERE reason = 'graph fixture'`)
	graphExec(t, e, `UPDATE knowledge_tag_prerequisites SET review_status = 'machine' WHERE review_status <> 'machine'`)
}

// TestTaxonomyGraphEffectiveEdges 证明读的是 DB 有效边集（不是静态 pack
// 边）：rejected 边、空 external_ref 端点边都被排除，人工新增边保留存储
// 方向并可再次被 rejected 抹掉。
func TestTaxonomyGraphEffectiveEdges(t *testing.T) {
	g := setupGraph(t)
	kid := g.env.createChild(t, "图谱小边")
	h := childHeaders(kid)
	q := graphV("root", graphRootRef)

	t.Run("baseline from imported pack", func(t *testing.T) {
		graphResetFixture(t, g.env)
		out, _ := graphGet(t, g.srv, q, h, http.StatusOK)
		if len(out.Nodes) != 8 || len(out.Edges) != 7 {
			t.Fatalf("baseline nodes/edges = %d/%d, want 8/7", len(out.Nodes), len(out.Edges))
		}
	})

	t.Run("rejected pack edge disappears from graph", func(t *testing.T) {
		graphResetFixture(t, g.env)
		graphSetEdgeStatus(t, g.env, "cnm_p002", graphRootRef, "rejected")
		out, _ := graphGet(t, g.srv, q, h, http.StatusOK)
		if graphNodeSet(t, out)["cnm_p002"] {
			t.Errorf("rejected edge still brought cnm_p002 into the graph")
		}
		if len(out.Nodes) != 7 || len(out.Edges) != 6 {
			t.Errorf("after rejection nodes/edges = %d/%d, want 7/6", len(out.Nodes), len(out.Edges))
		}
		for _, e := range out.Edges {
			if e.TopicID == "cnm_p002" && e.PrerequisiteID == graphRootRef {
				t.Errorf("rejected edge served: %+v", e)
			}
			if e.ReviewStatus == "rejected" {
				t.Errorf("rejected edge leaked into served edges: %+v", e)
			}
		}
	})

	t.Run("ref-less endpoint edge creates no ghost node", func(t *testing.T) {
		graphResetFixture(t, g.env)
		graphExec(t, g.env, `INSERT INTO knowledge_tags
			(name, subject, parent_id, code, sort_order, is_system, external_ref, source)
			VALUES ('graph fixture free text', 'math', NULL, NULL, 0, 0, NULL, '')`)
		var freeID int64
		if err := g.env.db.QueryRow(`SELECT id FROM knowledge_tags WHERE name = 'graph fixture free text'`).Scan(&freeID); err != nil {
			t.Fatalf("free text tag: %v", err)
		}
		graphExec(t, g.env, `INSERT INTO knowledge_tag_prerequisites
			(topic_id, prerequisite_id, strength, reason, review_status) VALUES (?, ?, 'hard', 'graph fixture', 'machine')`,
			freeID, graphTagID(t, g.env, graphRootRef))
		out, _ := graphGet(t, g.srv, q, h, http.StatusOK)
		if len(out.Nodes) != 8 || len(out.Edges) != 7 {
			t.Errorf("ref-less edge changed graph: nodes/edges = %d/%d, want 8/7", len(out.Nodes), len(out.Edges))
		}
		for _, n := range out.Nodes {
			if n.ID == "" {
				t.Errorf("ghost node with empty id: %+v", n)
			}
		}
	})

	t.Run("dynamic reviewed edge keeps stored direction then rejection removes it", func(t *testing.T) {
		graphResetFixture(t, g.env)
		// 方向与 pack 相反：root 作为 topic 端，cnm_p007 作为 prerequisite 端。
		graphInsertEdge(t, g.env, graphRootRef, "cnm_p007", "soft", "reviewed")
		out, _ := graphGet(t, g.srv, q, h, http.StatusOK)
		if !graphNodeSet(t, out)["cnm_p007"] || len(out.Nodes) != 9 || len(out.Edges) != 8 {
			t.Fatalf("manual edge not visible: nodes/edges = %d/%d, want 9/8", len(out.Nodes), len(out.Edges))
		}
		var found *graphTestEdge
		for i := range out.Edges {
			e := out.Edges[i]
			if graphEdgeKey(e) == [2]string{graphRootRef, "cnm_p007"} {
				found = &e
			}
			if graphEdgeKey(e) == [2]string{"cnm_p007", graphRootRef} {
				t.Errorf("manual edge direction flipped: %+v", e)
			}
		}
		if found == nil {
			t.Fatalf("manual edge missing from served edges")
		}
		if found.Strength != "soft" || found.ReviewStatus != "reviewed" || found.Reason != "graph fixture" {
			t.Errorf("manual edge payload wrong: %+v", *found)
		}
		graphSetEdgeStatus(t, g.env, graphRootRef, "cnm_p007", "rejected")
		out, _ = graphGet(t, g.srv, q, h, http.StatusOK)
		if graphNodeSet(t, out)["cnm_p007"] || len(out.Nodes) != 8 || len(out.Edges) != 7 {
			t.Errorf("re-rejected manual edge still visible: nodes/edges = %d/%d, want 8/7", len(out.Nodes), len(out.Edges))
		}
	})
}

// TestTaxonomyGraphFilterNoTraversal 验证被过滤掉的邻居是「不可见且不
// 可穿越」：隐藏跳板另一侧的节点不得借它进入邻域；跳板可见时同一条边
// 立刻可达（证明不是边缺失造成的假阴性）。
func TestTaxonomyGraphFilterNoTraversal(t *testing.T) {
	g := setupGraph(t)
	kid := g.env.createChild(t, "图谱小滤")
	h := childHeaders(kid)
	const (
		hidden = "cnm_p014" // 图形与几何，被 domain=数与代数 过滤
		beyond = "cnm_p007" // 数与代数/grade 1，且不与 root 或任何可见一跳邻居相连
	)
	// 新增一条只经过 hidden 才能到达 beyond 的边。
	graphInsertEdge(t, g.env, beyond, hidden, "soft", "reviewed")

	filtered := graphV("root", graphRootRef, "depth", "2",
		"subject", "math", "domain", "数与代数", "grade", "1")

	t.Run("filtered-out node is invisible and not traversed", func(t *testing.T) {
		out, _ := graphGet(t, g.srv, filtered, h, http.StatusOK)
		got := graphNodeSet(t, out)
		want := map[string]bool{graphRootRef: true,
			"cnm_p002": true, "cnm_p003": true, "cnm_p004": true, "cnm_p005": true, "cnm_p008": true,
			"cnm_p006": true, "cnm_p009": true, "cnm_p010": true, "cnm_p016": true}
		graphAssertNodeSet(t, out, want)
		if got[hidden] {
			t.Errorf("filtered-out jump node %s visible", hidden)
		}
		if got[beyond] {
			t.Errorf("expansion traversed invisible node %s to reach %s", hidden, beyond)
		}
		for _, n := range out.Nodes {
			if n.Subject != "math" || n.Domain != "数与代数" || n.Grade == nil || *n.Grade != 1 {
				t.Errorf("node violates filters: %+v", n)
			}
		}
		graphCompareEdges(t, out.Edges, graphEdgesWithin(graphEffectiveEdges(t, g.env), want))
	})

	t.Run("same edge is reachable when the jump node is visible", func(t *testing.T) {
		// 只留 subject=math：hidden 可见，于是 depth=2 应把 beyond 带进来。
		out, _ := graphGet(t, g.srv, graphV("root", graphRootRef, "depth", "2", "subject", "math"), h, http.StatusOK)
		if !graphNodeSet(t, out)[hidden] || !graphNodeSet(t, out)[beyond] {
			t.Fatalf("fixture edge ineffective: hidden=%v beyond=%v, nodes=%v",
				graphNodeSet(t, out)[hidden], graphNodeSet(t, out)[beyond], graphNodeSet(t, out))
		}
		var found bool
		for _, e := range out.Edges {
			if graphEdgeKey(e) == [2]string{beyond, hidden} {
				found = true
			}
		}
		if !found {
			t.Errorf("fixture edge not served when visible: %+v", out.Edges)
		}
	})
}

// TestTaxonomyGraphMastery 覆盖 mastery 的 null/真值语义与 resolved
// target：无 kc_mastery 行必须序列化 null（不是伪造 0.5），真值按目标
// 用户读取，X-User-ID 永不参与身份判定。
func TestTaxonomyGraphMastery(t *testing.T) {
	g := setupGraph(t)
	kid := g.env.createChild(t, "图谱小掌")
	other := g.env.createChild(t, "图谱小他")
	base := graphV("root", graphRootRef)

	// 先确认「全 null」并锁定 JSON 里真的写了 null 而不是省略字段。
	out, raw := graphGet(t, g.srv, base, childHeaders(kid), http.StatusOK)
	if !bytes.Contains(raw, []byte(`"mastery":null`)) {
		t.Fatalf("no mastery:null in response: %s", raw)
	}
	for _, n := range out.Nodes {
		if n.Mastery != nil {
			t.Errorf("node %s mastery = %v, want null (no kc_mastery row)", n.ID, *n.Mastery)
		}
	}

	// 写入 kid 的真实 rollup：cnm_p001=0.875、cnm_p002=0.25，其余仍无行。
	writeMastery := func(user int, ref string, v float64) {
		graphExec(t, g.env, `INSERT INTO kc_mastery
			(user_id, tag_id, correct_count, total_count, mastery, low_confidence)
			SELECT ?, id, 7, 8, ?, 0 FROM knowledge_tags WHERE external_ref = ?`, user, v, ref)
	}
	writeMastery(kid, graphRootRef, 0.875)
	writeMastery(kid, "cnm_p002", 0.25)
	writeMastery(other, graphRootRef, 0.125)

	assertMastery := func(t *testing.T, out graphTestResponse, want map[string]float64) {
		t.Helper()
		got := map[string]*float64{}
		for _, n := range out.Nodes {
			got[n.ID] = n.Mastery
		}
		for ref, v := range want {
			if got[ref] == nil {
				t.Errorf("%s mastery = null, want %v", ref, v)
				continue
			}
			if *got[ref] != v {
				t.Errorf("%s mastery = %v, want %v", ref, *got[ref], v)
			}
		}
		for _, n := range out.Nodes {
			if _, ok := want[n.ID]; !ok && n.Mastery != nil {
				t.Errorf("%s mastery = %v, want null", n.ID, *n.Mastery)
			}
		}
	}

	t.Run("kid sees own real rollups", func(t *testing.T) {
		out, _ := graphGet(t, g.srv, base, childHeaders(kid), http.StatusOK)
		assertMastery(t, out, map[string]float64{graphRootRef: 0.875, "cnm_p002": 0.25})
	})

	t.Run("admin resolving another user reads that user's rollups", func(t *testing.T) {
		out, _ := graphGet(t, g.srv, graphWith(base, "user_id", fmt.Sprint(other)), adminHeaders(), http.StatusOK)
		assertMastery(t, out, map[string]float64{graphRootRef: 0.125})
	})

	t.Run("X-User-ID is not trusted", func(t *testing.T) {
		h := childHeaders(kid)
		h["X-User-ID"] = fmt.Sprint(other)
		out, _ := graphGet(t, g.srv, base, h, http.StatusOK)
		assertMastery(t, out, map[string]float64{graphRootRef: 0.875, "cnm_p002": 0.25})
		// 孩子即使带上别人的 X-User-ID，也不能借它切目标：
		// user_id=other 仍按会话身份判为 403。
		graphGet(t, g.srv, graphWith(base, "user_id", fmt.Sprint(other)), h, http.StatusForbidden)
	})
}

// TestTaxonomyGraphCap250 构造 root 的 300 个有效一跳邻居，验证 250 上
// 限、truncated 标志、按 external_ref 排序的稳定截断、以及只服务 kept
// 内边；249 条时恰好不截断。
func TestTaxonomyGraphCap250(t *testing.T) {
	g := setupGraph(t)
	kid := g.env.createChild(t, "图谱小截")
	h := childHeaders(kid)
	q := graphV("root", graphRootRef)
	rootID := graphTagID(t, g.env, graphRootRef)

	// 清掉 root 的 pack 边，改为完全由 fixture 控制的扇出。
	graphExec(t, g.env, `DELETE FROM knowledge_tag_prerequisites WHERE topic_id = ? OR prerequisite_id = ?`, rootID, rootID)
	rows, err := g.env.db.Query(`SELECT external_ref FROM knowledge_tags
		WHERE external_ref IS NOT NULL AND external_ref <> '' AND external_ref <> ?
		ORDER BY external_ref LIMIT 300`, graphRootRef)
	if err != nil {
		t.Fatalf("select fan candidates: %v", err)
	}
	refs := []string{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			t.Fatalf("scan candidate: %v", err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate candidates: %v", err)
	}
	rows.Close()
	if len(refs) != 300 {
		t.Fatalf("fan candidates = %d, want 300", len(refs))
	}
	first249, extra := refs[:graphCap-1], refs[graphCap-1:]

	insertFan := func(list []string) {
		for _, ref := range list {
			id := graphTagID(t, g.env, ref)
			graphExec(t, g.env, `INSERT INTO knowledge_tag_prerequisites
				(topic_id, prerequisite_id, strength, reason, review_status)
				VALUES (?, ?, 'soft', 'graph fixture', 'reviewed')`, rootID, id)
		}
	}
	wantKept := func() map[string]bool {
		m := map[string]bool{graphRootRef: true}
		for _, ref := range first249 {
			m[ref] = true
		}
		return m
	}
	assertCapped := func(t *testing.T, stage string, wantTruncated bool) []byte {
		t.Helper()
		out, raw := graphGet(t, g.srv, q, h, http.StatusOK)
		if len(out.Nodes) != graphCap {
			t.Fatalf("%s: nodes = %d, want %d", stage, len(out.Nodes), graphCap)
		}
		if out.Truncated != wantTruncated {
			t.Errorf("%s: truncated = %v, want %v", stage, out.Truncated, wantTruncated)
		}
		want := wantKept()
		graphAssertNodeSet(t, out, want)
		graphCompareEdges(t, out.Edges, graphEdgesWithin(graphEffectiveEdges(t, g.env), want))
		// 被截掉的节点绝不能借边漏出。
		kept := graphNodeSet(t, out)
		for _, e := range out.Edges {
			if !kept[e.TopicID] || !kept[e.PrerequisiteID] {
				t.Errorf("%s: edge escapes kept set: %+v", stage, e)
			}
		}
		var fanServed int
		for _, e := range out.Edges {
			if e.TopicID == graphRootRef && e.Reason == "graph fixture" {
				fanServed++
			}
		}
		if fanServed != graphCap-1 {
			t.Errorf("%s: root fan edges served = %d, want %d", stage, fanServed, graphCap-1)
		}
		return raw
	}

	t.Run("249 neighbors exactly fills cap without truncation", func(t *testing.T) {
		insertFan(first249)
		assertCapped(t, "249", false)
	})

	var cappedRaw []byte
	t.Run("250th neighbor trips the cap deterministically", func(t *testing.T) {
		insertFan(extra[:1])
		cappedRaw = assertCapped(t, "250", true)
	})

	t.Run("300 neighbors keep the same cut and byte-identical response", func(t *testing.T) {
		insertFan(extra[1:])
		raw := assertCapped(t, "300", true)
		if !bytes.Equal(raw, cappedRaw) {
			t.Fatalf("truncation is not stable: 250-neighbor and 300-neighbor responses differ")
		}
	})
}
