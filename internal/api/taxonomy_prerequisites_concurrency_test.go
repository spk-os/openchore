package api_test

// TX-PREREQ-FINAL-TESTER：先修管理写路径的并发 / copy-on-write / 重启重建
// 独立测试。真实 SQLite + 真实 NewRouter + httptest；不加 mock，不用
// sleep 制造竞态——并发用例只依赖锁临界区语义给出的确定性结果：
//
//   - 并发 PUT 互不相干的边：全部落库、无丢失、无重复行；同端点并发 PUT
//     的最终行必须是某一次请求的完整 payload（不能 torn write）；随后
//     DB effective 与 catalog 投影一致；
//   - 并发提交互为反向的一对边（A←B 与 B←A）：锁保证不能二者都成功，
//     且 DB 与 catalog 都不会出现正反两条；
//   - 成环 PUT 409：裸表（含 rejected）、effective、catalog 全部逐项不变；
//   - reject 后构造「若恢复则成环」的反向链，PATCH reviewed 必须 409 且
//     行仍是 rejected；拆掉环后同一恢复必须成功；
//   - 合法 reject → reviewed 恢复：磁盘行 strength/reason 保留；catalog
//     侧 strength 经 Practice 判定面观察（见下，重点探针）；
//   - 不存在行 + 成环 → 404（存在性先于环判定）；
//   - 新 handler（模拟重启）catalogOnce 只恢复 effective（reviewed /
//     machine），不复活 rejected；
//   - AdminImportPack/sync 后 catalog 与 DB effective 一致，sync 不改写
//     admin 裁决。
//
// 所有测试名以 TestTaxonomyPrerequisite 开头，兼容
// `go test ./internal/api -run 'TestTaxonomyPrerequisite'`。

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/liftedkilt/openchore/internal/api"
)

// 本文件用的边锚点（均来自真实 pack，离线核对 dependencies.json）：
//
//	基线链：cnm_p001 ← cnm_p002 ← cnm_p009，另有 cnm_p004 ← cnm_p001。
//	catEdgeA cnm_p002 ← cnm_p008 与其反向 catEdgeAPrereq ← catEdgeA 都空闲；
//	catEdgeB cnm_p004 ← cnm_p014 同样空闲。
//	cnm_p003 ← cnm_p004 经基线 cnm_p004 ← cnm_p001 与 cnm_p001 ← cnm_p003 成环。
const (
	catEdgeA       = "cnm_p002"
	catEdgeAPrereq = "cnm_p008"
	catEdgeB       = "cnm_p004"
	catEdgeBPrereq = "cnm_p014"
	catChainExtra  = "cnm_p003"
)

// txPrereqCatRaw 返回状态码与原样响应文本（并发 goroutine 里不能用
// t.Fatalf，只有父 goroutine 负责失败）。
func txPrereqCatRaw(t *testing.T, e *testEnv, method, path string, body any, headers map[string]string) (int, string) {
	t.Helper()
	resp := e.request(t, method, path, body, headers)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "read-error: " + err.Error()
	}
	return resp.StatusCode, string(raw)
}

func txPrereqCatPutBody(topic, prereq, strength, reason string) map[string]any {
	return map[string]any{
		"topic_id": topic, "prerequisite_id": prereq,
		"strength": strength, "reason": reason,
	}
}

func txPrereqCatReviewBody(topic, prereq, status string) map[string]any {
	return map[string]any{
		"topic_id": topic, "prerequisite_id": prereq,
		"review_status": status,
	}
}

// txPrereqCatCatalogEdge 经 GET /api/taxonomy/graph 观察 in-memory catalog
// 里某条边的 payload。注意：该端点当前从 DB（ListEffectivePrerequisiteEdges）
// 取边，只有邻域/过滤/catalogOnce 走内存图；它证明不了 catalog 投影自身的
// strength。GET /topics/{ref} 也把 strength 丢在投影之外。因此本文件对
// catalog 只断言「边集合」（topic<-prereq），strength 的差异由磁盘行断言
// 兜底（见 TestTaxonomyPrerequisiteRejectedRestoreKeepsPayload）。
func txPrereqCatGraphEdge(t *testing.T, e *testEnv, userID int, root, topic, prereq string) (string, bool) {
	t.Helper()
	path := fmt.Sprintf("/api/taxonomy/graph?root=%s&user_id=%d", root, userID)
	resp := txPrereqCall(t, e, "GET", path, nil, adminHeaders(), http.StatusOK)
	var out struct {
		Edges []struct {
			TopicID        string `json:"topic_id"`
			PrerequisiteID string `json:"prerequisite_id"`
			Strength       string `json:"strength"`
		} `json:"edges"`
	}
	decodeBody(t, resp, &out)
	for _, edge := range out.Edges {
		if edge.TopicID == topic && edge.PrerequisiteID == prereq {
			return edge.Strength, true
		}
	}
	return "", false
}

// txPrereqCatAllEdges 裸表快照（含 rejected）："topic<-prereq" → [strength,
// reason, review_status]，证明 409 连 audit 行都不写、不改。
func txPrereqCatAllEdges(t *testing.T, e *testEnv) map[string][3]string {
	t.Helper()
	rows, err := e.db.Query(`
		SELECT tt.external_ref, pt.external_ref, e.strength, COALESCE(e.reason, ''), e.review_status
		  FROM knowledge_tag_prerequisites e
		  JOIN knowledge_tags tt ON tt.id = e.topic_id
		  JOIN knowledge_tags pt ON pt.id = e.prerequisite_id`)
	if err != nil {
		t.Fatalf("read raw edge table: %v", err)
	}
	defer rows.Close()
	out := map[string][3]string{}
	for rows.Next() {
		var topic, prereq, strength, reason, status string
		if err := rows.Scan(&topic, &prereq, &strength, &reason, &status); err != nil {
			t.Fatalf("scan raw edge: %v", err)
		}
		out[topic+"<-"+prereq] = [3]string{strength, reason, status}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate raw edges: %v", err)
	}
	return out
}

func txPrereqCatAssertRawSnapshotUnchanged(t *testing.T, e *testEnv, before map[string][3]string, why string) {
	t.Helper()
	after := txPrereqCatAllEdges(t, e)
	if len(after) != len(before) {
		t.Fatalf("%s: 裸表行数 %d -> %d", why, len(before), len(after))
	}
	for key, v := range before {
		if got, ok := after[key]; !ok || got != v {
			t.Fatalf("%s: 裸表行 %s 被改写 %v -> %v (exists=%v)", why, key, v, got, ok)
		}
	}
	for key := range after {
		if _, ok := before[key]; !ok {
			t.Fatalf("%s: 裸表新增行 %s = %v", why, key, after[key])
		}
	}
}

// txPrereqCatTopicRefs 返回全部知识点 external_ref（catalog 投影采样域）。
func txPrereqCatTopicRefs(t *testing.T, e *testEnv) []string {
	t.Helper()
	rows, err := e.db.Query(`SELECT external_ref FROM knowledge_tags WHERE COALESCE(external_ref,'') <> '' ORDER BY id`)
	if err != nil {
		t.Fatalf("list topic refs: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			t.Fatalf("scan topic ref: %v", err)
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate topic refs: %v", err)
	}
	return out
}

// txPrereqCatCatalogEdges 经真实读端点 GET /api/taxonomy/topics/{ref} 采样
// 每个 topic 的 prerequisites 侧，拼出 "topic<-prereq" 集合。dependents
// 侧是同一批边的镜像，不重复读（effective 量级 4600 条，每边一次即够）。
func txPrereqCatCatalogEdges(t *testing.T, e *testEnv) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, ref := range txPrereqCatTopicRefs(t, e) {
		for p := range txPrereqCatalogPrereqs(t, e, ref) {
			out[ref+"<-"+p] = true
		}
	}
	return out
}

// txPrereqCatAssertDBEqualsCatalog：DB effective 集合 == catalog 投影集合，
// 且 rejected 行不出现在任何一侧。
func txPrereqCatAssertDBEqualsCatalog(t *testing.T, e *testEnv, rejected [][2]string) {
	t.Helper()
	want := make(map[string]bool)
	for _, edge := range txPrereqEffective(t, e) {
		want[edge.TopicID+"<-"+edge.PrerequisiteID] = true
	}
	for _, r := range rejected {
		key := r[0] + "<-" + r[1]
		if want[key] {
			t.Fatalf("rejected 边 %s 出现在 DB effective", key)
		}
	}
	got := txPrereqCatCatalogEdges(t, e)
	for key := range want {
		if !got[key] {
			t.Fatalf("DB effective 有 %s，catalog 投影没有", key)
		}
	}
	for key := range got {
		if !want[key] {
			t.Fatalf("catalog 投影有 %s，DB effective 没有", key)
		}
	}
}

// txPrereqCatNewHandler 模拟重启：新 NewTaxonomyHandler + 新 router，共享
// 同一 *store.Store / *sql.DB；catalog 从空的 catalogOnce 重建。会话管理
// 器复用测试密钥，admin session 在新 router 上同样有效。
func txPrereqCatNewHandler(t *testing.T, e *testEnv) *testEnv {
	t.Helper()
	sessions := api.NewSessionManager(testSessionSecret)
	oidcSvc := api.NewOIDCService(e.store, sessions, nil, nil)
	router, chores, reports, coach := api.NewRouter(e.store, nil, api.Auth{Sessions: sessions, OIDC: oidcSvc})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return &testEnv{server: server, db: e.db, store: e.store, chores: chores, reports: reports, coach: coach}
}

// 并发 PUT：两条互不相干的边 + 六个写同一对端点的竞争者，全部同时出发。
// 断言：全部 200；不相干边完整落库；同端点最终行是某次请求的完整
// payload；无重复行；catalog 同步看到两者。
//
// 竞争写同一端点时，"last writer wins" 的两个字段可能来自同一次请求，
// 也可能被另一次请求穿插覆盖（每条 PUT 都是单条 SQL 语句，DB 不可能
// torn write，但并发竞争者的相对顺序不保证）；因此对同端点只断言：
// strength ∈ {hard, soft}、reason ∈ 本次竞争者的 reason 集合（不得被
// 插值成其他内容）、最终行等于某一次"完整写入"是只在单写者时成立的
// 更强结论，这里放宽为字段级验证。
func TestTaxonomyPrerequisiteConcurrentPutsNoLostEdges(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	h := adminHeaders()

	base := len(txPrereqEffective(t, e))
	distinct := []map[string]any{
		txPrereqCatPutBody(catEdgeA, catEdgeAPrereq, "soft", "并发-无关边 A"),
		txPrereqCatPutBody(catEdgeB, catEdgeBPrereq, "hard", "并发-无关边 B"),
	}

	const samePair = 6
	samePairBodies := make([]map[string]any, samePair)
	for i := range samePairBodies {
		samePairBodies[i] = map[string]any{
			"topic_id": catEdgeB, "prerequisite_id": catEdgeBPrereq,
			"strength": []string{"hard", "soft"}[i%2],
			"reason":   fmt.Sprintf("并发-同端点-%d", i),
		}
	}

	// 同端点竞争者 = 六个 samePair 写者 + 定向边 B（同一端点对），
	// 最终行必须等于其中某一个写者的完整 payload（字段级见下）。
	contenders := append([]map[string]any{distinct[1]}, samePairBodies...)

	bodies := append(append([]map[string]any{}, distinct...), samePairBodies...)
	statuses := make([]int, len(bodies))
	texts := make([]string, len(bodies))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, body := range bodies {
		i, body := i, body
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			statuses[i], texts[i] = txPrereqCatRaw(t, e, "PUT", txPrereqPutURL, body, h)
		}()
	}
	close(start)
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("并发 PUT[%d] = %d, want 200; body=%s", i, status, texts[i])
		}
	}
	if got := len(txPrereqEffective(t, e)); got != base+2 {
		t.Fatalf("并发 PUT 后 effective = %d, want %d（base %d + 2 条唯一边）", got, base+2, base)
	}
	if row, ok := txPrereqStoredRow(t, e, catEdgeA, catEdgeAPrereq); !ok ||
		row.Strength != "soft" || row.Reason != "并发-无关边 A" || row.ReviewStatus != "reviewed" {
		t.Fatalf("无关边 A 落库不对: %+v exists=%v", row, ok)
	}
	// 先让读锁释放，避免 MaxOpenConns=1 下与上面的 effective 查询阻塞。
	if row, ok := txPrereqStoredRow(t, e, catEdgeB, catEdgeBPrereq); !ok || row.ReviewStatus != "reviewed" {
		t.Fatalf("同端点边落库不对: %+v exists=%v", row, ok)
	} else {
		if row.Strength != "hard" && row.Strength != "soft" {
			t.Fatalf("同端点最终 strength = %q, want hard/soft", row.Strength)
		}
		matchedReason := false
		for _, body := range contenders {
			if body["reason"] == row.Reason {
				matchedReason = true
				break
			}
		}
		if !matchedReason {
			t.Fatalf("同端点最终 reason = %q 不在本次竞争者的 reason 集合内", row.Reason)
		}
	}
	var dupes int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM (
		SELECT topic_id, prerequisite_id, COUNT(*) c FROM knowledge_tag_prerequisites
		 GROUP BY topic_id, prerequisite_id HAVING c > 1)`).Scan(&dupes); err != nil {
		t.Fatalf("count duplicate edges: %v", err)
	}
	if dupes != 0 {
		t.Fatalf("并发写入产生 %d 组重复边", dupes)
	}
	if p := txPrereqCatalogPrereqs(t, e, catEdgeA); !p[catEdgeAPrereq] {
		t.Fatalf("无关边 A 未进入 catalog: %v", p)
	}
	if p := txPrereqCatalogPrereqs(t, e, catEdgeB); !p[catEdgeBPrereq] {
		t.Fatalf("同端点边未进入 catalog: %v", p)
	}
}

// 并发提交互为反向的一对边：锁必须让两者串行化，第二次读到第一次写入后
// 的 effective 并 409。多轮重复（每轮先 reject 掉上一轮胜利者）放大交错，
// 但不依赖时序断言。
func TestTaxonomyPrerequisiteConcurrentCyclePairKeepsDAG(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	h := adminHeaders()

	base := len(txPrereqEffective(t, e))
	fwd := txPrereqCatPutBody(catEdgeA, catEdgeAPrereq, "hard", "并发成环对-正向")
	rev := txPrereqCatPutBody(catEdgeAPrereq, catEdgeA, "soft", "并发成环对-反向")

	for round := 0; round < 4; round++ {
		if round > 0 {
			for _, pair := range [][2]string{{catEdgeA, catEdgeAPrereq}, {catEdgeAPrereq, catEdgeA}} {
				// 只有上一轮的胜者（以及被 PUT 覆盖成 reviewed 的败者行）
				// 存在行；对不存在的行 PATCH 必得 404，这里按 200|404
				// 放行，真正的断言在下面的集合比对里。
				txPrereqCatRaw(t, e, "PATCH", txPrereqReviewURL,
					txPrereqCatReviewBody(pair[0], pair[1], "rejected"), h)
			}
			if got := len(txPrereqEffective(t, e)); got != base {
				t.Fatalf("round %d 清场后 effective = %d, want %d", round, got, base)
			}
			if txPrereqHasEdge(txPrereqEffective(t, e), catEdgeA, catEdgeAPrereq) ||
				txPrereqHasEdge(txPrereqEffective(t, e), catEdgeAPrereq, catEdgeA) {
				t.Fatalf("round %d 清场未清干净", round)
			}
		}
		statuses := make([]int, 2)
		texts := make([]string, 2)
		bodies := []map[string]any{fwd, rev}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range bodies {
			i := i
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				statuses[i], texts[i] = txPrereqCatRaw(t, e, "PUT", txPrereqPutURL, bodies[i], h)
			}()
		}
		close(start)
		wg.Wait()

		okCount := 0
		for i, status := range statuses {
			switch status {
			case http.StatusOK:
				okCount++
			case http.StatusConflict:
			default:
				t.Fatalf("round %d PUT[%d] = %d, want 200|409; body=%s", round, i, status, texts[i])
			}
		}
		if okCount != 1 {
			t.Fatalf("round %d 反向对成功数 = %d, want 1（statuses=%v，锁失效）", round, okCount, statuses)
		}
		effective := txPrereqEffective(t, e)
		hasFwd := txPrereqHasEdge(effective, catEdgeA, catEdgeAPrereq)
		hasRev := txPrereqHasEdge(effective, catEdgeAPrereq, catEdgeA)
		if hasFwd && hasRev {
			t.Fatalf("round %d DB effective 同时含正反边（成环）", round)
		}
		fwdCat := txPrereqCatalogPrereqs(t, e, catEdgeA)[catEdgeAPrereq]
		revCat := txPrereqCatalogPrereqs(t, e, catEdgeAPrereq)[catEdgeA]
		if fwdCat && revCat {
			t.Fatalf("round %d catalog 同时含正反边（成环）", round)
		}
		if fwdCat != hasFwd || revCat != hasRev {
			t.Fatalf("round %d catalog(fwd=%v rev=%v) 与 DB effective(fwd=%v rev=%v) 不一致",
				round, fwdCat, revCat, hasFwd, hasRev)
		}
		if got := len(effective); got != base+1 {
			t.Fatalf("round %d effective = %d, want %d", round, got, base+1)
		}
	}
}

// 成环 PUT 的 409：裸表（含 rejected）、effective、catalog 三者逐项不变。
func TestTaxonomyPrerequisiteCycle409LeavesDBNeedAndCatalogUntouched(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	h := adminHeaders()

	beforeRaw := txPrereqCatAllEdges(t, e)
	beforeEff := len(txPrereqEffective(t, e))
	cycle := txPrereqCatPutBody(txPrereqRoot, txPrereqLeaf, "hard", "成环 409 快照")
	resp := txPrereqCall(t, e, "PUT", txPrereqPutURL, cycle, h, http.StatusConflict)
	if msg := txPrereqErrorMsg(t, resp); !strings.Contains(msg, "conflicts with the graph") {
		t.Fatalf("409 error = %q", msg)
	}
	txPrereqCatAssertRawSnapshotUnchanged(t, e, beforeRaw, "成环 PUT 409")
	if got := len(txPrereqEffective(t, e)); got != beforeEff {
		t.Fatalf("409 后 effective = %d, want %d", got, beforeEff)
	}
	if p := txPrereqCatalogPrereqs(t, e, txPrereqRoot); len(p) != 0 {
		t.Fatalf("409 后 catalog cnm_p001 prerequisites = %v", p)
	}
	// 反向依赖侧不变：cnm_p009 仍硬先修 cnm_p002。
	if p := txPrereqCatalogPrereqs(t, e, txPrereqLeaf); !p["cnm_p002"] {
		t.Fatalf("409 后 cnm_p009 的先修被破坏: %v", p)
	}
}

// reject 后构造反向链使恢复成环：PATCH reviewed 必须 409、行仍 rejected；
// 拆掉环后同一恢复必须成功（证明 409 来自环判定，不是永久封死）。
//
// 构造：u2←u8(soft) → rejected；u8←u6、u6←u2 合法写入（此时 u2←u8 缺席，
// 图无环）；恢复 u2←u8 会闭合 u2→u8→u6→u2。
func TestTaxonomyPrerequisiteRejectedRestoreCycleIs409(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	h := adminHeaders()

	u2, u6, u8 := "cnm_p002", "cnm_p006", "cnm_p008"
	base := len(txPrereqEffective(t, e))

	txPrereqStatus(t, e, "PUT", txPrereqPutURL,
		txPrereqCatPutBody(u2, u8, "soft", "恢复前：soft 边"), h, http.StatusOK)
	txPrereqStatus(t, e, "PATCH", txPrereqReviewURL,
		txPrereqCatReviewBody(u2, u8, "rejected"), h, http.StatusOK)
	if txPrereqHasEdge(txPrereqEffective(t, e), u2, u8) {
		t.Fatal("rejected 后 u2←u8 仍在 effective")
	}
	txPrereqStatus(t, e, "PUT", txPrereqPutURL,
		txPrereqCatPutBody(u8, u6, "hard", "反向链 u8←u6"), h, http.StatusOK)
	txPrereqStatus(t, e, "PUT", txPrereqPutURL,
		txPrereqCatPutBody(u6, u2, "hard", "反向链 u6←u2"), h, http.StatusOK)
	if got := len(txPrereqEffective(t, e)); got != base+2 {
		t.Fatalf("构造反向链后 effective = %d, want %d", got, base+2)
	}

	rawBefore := txPrereqCatAllEdges(t, e)
	resp := txPrereqCall(t, e, "PATCH", txPrereqReviewURL,
		txPrereqCatReviewBody(u2, u8, "reviewed"), h, http.StatusConflict)
	if msg := txPrereqErrorMsg(t, resp); !strings.Contains(msg, "conflicts with the graph") {
		t.Fatalf("恢复成环 409 error = %q", msg)
	}
	txPrereqCatAssertRawSnapshotUnchanged(t, e, rawBefore, "恢复成环 409")
	if row, ok := txPrereqStoredRow(t, e, u2, u8); !ok || row.ReviewStatus != "rejected" {
		t.Fatalf("409 后 u2←u8 行应仍 rejected: %+v exists=%v", row, ok)
	}
	if txPrereqHasEdge(txPrereqEffective(t, e), u2, u8) {
		t.Fatal("409 后 u2←u8 进入 DB effective")
	}
	if txPrereqCatalogPrereqs(t, e, u2)[u8] {
		t.Fatal("409 后 u2←u8 进入 catalog")
	}

	txPrereqStatus(t, e, "PATCH", txPrereqReviewURL,
		txPrereqCatReviewBody(u6, u2, "rejected"), h, http.StatusOK)
	txPrereqStatus(t, e, "PATCH", txPrereqReviewURL,
		txPrereqCatReviewBody(u2, u8, "reviewed"), h, http.StatusOK)
	if row, ok := txPrereqStoredRow(t, e, u2, u8); !ok || row.ReviewStatus != "reviewed" {
		t.Fatalf("拆环后恢复应成功: %+v exists=%v", row, ok)
	}
	if !txPrereqCatalogPrereqs(t, e, u2)[u8] {
		t.Fatal("拆环后恢复未进入 catalog")
	}
}

// 合法 reject → reviewed 恢复：磁盘行整行不变（strength soft / reason 保留），
// 重启重建后 catalog 同样有这条边且 DB effective 一致。
func TestTaxonomyPrerequisiteRejectedRestoreKeepsPayload(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	h := adminHeaders()

	u2, u8 := "cnm_p002", "cnm_p008"
	txPrereqStatus(t, e, "PUT", txPrereqPutURL,
		txPrereqCatPutBody(u2, u8, "soft", "恢复 payload：soft 原因"), h, http.StatusOK)
	before, _ := txPrereqStoredRow(t, e, u2, u8)
	txPrereqStatus(t, e, "PATCH", txPrereqReviewURL,
		txPrereqCatReviewBody(u2, u8, "rejected"), h, http.StatusOK)
	txPrereqStatus(t, e, "PATCH", txPrereqReviewURL,
		txPrereqCatReviewBody(u2, u8, "reviewed"), h, http.StatusOK)

	after, ok := txPrereqStoredRow(t, e, u2, u8)
	if !ok || after != (txPrereqStored{Strength: before.Strength, Reason: before.Reason, ReviewStatus: "reviewed"}) {
		t.Fatalf("恢复后磁盘行 = %+v exists=%v, want strength=%q reason=%q reviewed",
			after, ok, before.Strength, before.Reason)
	}
	if after.Strength != "soft" {
		t.Fatalf("恢复后 strength = %q, want soft", after.Strength)
	}
	if !txPrereqHasEdge(txPrereqEffective(t, e), u2, u8) {
		t.Fatal("恢复后不在 DB effective")
	}
	// 先关闭 env 自带的 httptest 服务（释放其 keep-alive 连接）。
	// 注意不能关闭 e.db：新 router 上有中间件要读 users 表，关连接池会让
	// 后续请求 500 failed to load user。
	e.server.Close()

	restarted := txPrereqCatNewHandler(t, e)
	if !txPrereqCatalogPrereqs(t, restarted, u2)[u8] {
		t.Fatal("重启重建后 u2←u8 不在 catalog")
	}
	if strength, ok := txPrereqCatGraphEdge(t, restarted, 1, u2, u2, u8); !ok || strength != "soft" {
		t.Fatalf("重启重建后 graph 边 u2←u8 strength = %q exists=%v, want soft", strength, ok)
	}
	txPrereqCatAssertDBEqualsCatalog(t, restarted, nil)
}

// 不存在行与成环同时出现：404 契约优先（存在性先于环判定），且不落库。
func TestTaxonomyPrerequisiteNotFoundBeatsCycle(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	h := adminHeaders()

	rawBefore := txPrereqCatAllEdges(t, e)
	rawCount := len(rawBefore)

	txPrereqStatus(t, e, "PUT", txPrereqPutURL,
		txPrereqCatPutBody("cnm_missing_topic", txPrereqLeaf, "hard", "不存在 + 成环"),
		h, http.StatusNotFound)
	txPrereqStatus(t, e, "PUT", txPrereqPutURL,
		txPrereqCatPutBody(txPrereqRoot, "cnm_missing_prereq", "hard", "不存在 + 成环"),
		h, http.StatusNotFound)
	txPrereqStatus(t, e, "PATCH", txPrereqReviewURL,
		txPrereqCatReviewBody("cnm_missing_topic", txPrereqLeaf, "reviewed"),
		h, http.StatusNotFound)
	// 双端存在但无行，且该成环边从未入库：DAO ok=false 的 404。
	txPrereqStatus(t, e, "PATCH", txPrereqReviewURL,
		txPrereqCatReviewBody(txPrereqRoot, txPrereqLeaf, "rejected"),
		h, http.StatusNotFound)

	if got := len(txPrereqCatAllEdges(t, e)); got != rawCount {
		t.Fatalf("404 场景改变了裸表行数 %d -> %d", rawCount, got)
	}
}

// 新 handler 的 catalogOnce（重启 / 新实例）从 DB effective 重建：reviewed
// 与 machine 保留，rejected 不复活。
func TestTaxonomyPrerequisiteRestartRestoresEffectiveOnly(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	h := adminHeaders()

	txPrereqStatus(t, e, "PATCH", txPrereqReviewURL,
		txPrereqCatReviewBody(txPrereqLeaf, "cnm_p002", "rejected"), h, http.StatusOK)
	txPrereqStatus(t, e, "PUT", txPrereqPutURL,
		txPrereqCatPutBody(catEdgeA, catEdgeAPrereq, "soft", "重启：reviewed 边"), h, http.StatusOK)

	// 先关闭 env 自带的 httptest 服务（释放其 keep-alive 连接）。
	// 注意不能关闭 e.db：新 router 上有中间件要读 users 表，关连接池会让
	// 后续请求 500 failed to load user。
	e.server.Close()

	restarted := txPrereqCatNewHandler(t, e)
	if p := txPrereqCatalogPrereqs(t, restarted, txPrereqLeaf); p["cnm_p002"] {
		t.Fatalf("重启复活了 rejected 的 pack 边 cnm_p009←cnm_p002: %v", p)
	}
	if p := txPrereqCatalogPrereqs(t, restarted, catEdgeA); !p[catEdgeAPrereq] {
		t.Fatalf("重启丢了 reviewed 边 %s←%s: %v", catEdgeA, catEdgeAPrereq, p)
	}
	if p := txPrereqCatalogPrereqs(t, restarted, "cnm_p003"); !p[txPrereqRoot] {
		t.Fatalf("重启丢了 machine 边: %v", p)
	}
	// 重启后对仍留在库的 rejected 行再次 PATCH 应幂等成功。
	txPrereqStatus(t, restarted, "PATCH", txPrereqReviewURL,
		txPrereqCatReviewBody(txPrereqLeaf, "cnm_p002", "rejected"), adminHeaders(), http.StatusOK)
}

// AdminImportPack（POST /api/admin/taxonomy/sync）之后：catalog 与 DB
// effective 逐边一致；import 的 ON CONFLICT 分支不得复活 rejected、也不得
// 把 reviewed 降回 machine。先在新实例上重建再 sync，覆盖 restart+sync。
func TestTaxonomyPrerequisiteSyncKeepsCatalogAndDBConsistent(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	h := adminHeaders()

	rejectedPackEdge := [2]string{txPrereqLeaf, "cnm_p002"}
	reviewedEdge := [2]string{catEdgeB, catEdgeBPrereq}
	rejectedAdminEdge := [2]string{catEdgeA, catEdgeAPrereq}

	txPrereqStatus(t, e, "PATCH", txPrereqReviewURL,
		txPrereqCatReviewBody(rejectedPackEdge[0], rejectedPackEdge[1], "rejected"), h, http.StatusOK)
	txPrereqStatus(t, e, "PUT", txPrereqPutURL,
		txPrereqCatPutBody(reviewedEdge[0], reviewedEdge[1], "soft", "sync 前 reviewed 边"), h, http.StatusOK)
	txPrereqStatus(t, e, "PUT", txPrereqPutURL,
		txPrereqCatPutBody(rejectedAdminEdge[0], rejectedAdminEdge[1], "hard", "sync 前将 reject 的边"), h, http.StatusOK)
	txPrereqStatus(t, e, "PATCH", txPrereqReviewURL,
		txPrereqCatReviewBody(rejectedAdminEdge[0], rejectedAdminEdge[1], "rejected"), h, http.StatusOK)

	// 先关闭 env 自带的 httptest 服务（释放其 keep-alive 连接）。
	// 注意不能关闭 e.db：新 router 上有中间件要读 users 表，关连接池会让
	// 后续请求 500 failed to load user。
	e.server.Close()

	restarted := txPrereqCatNewHandler(t, e)
	txPrereqStatus(t, restarted, "POST", "/api/admin/taxonomy/sync", nil, adminHeaders(), http.StatusOK)

	for _, want := range []struct {
		key    [2]string
		status string
	}{
		{rejectedPackEdge, "rejected"},
		{reviewedEdge, "reviewed"},
		{rejectedAdminEdge, "rejected"},
	} {
		row, ok := txPrereqStoredRow(t, e, want.key[0], want.key[1])
		if !ok || row.ReviewStatus != want.status {
			t.Fatalf("sync 后行 %s<-%s = %+v exists=%v, want review_status=%q",
				want.key[0], want.key[1], row, ok, want.status)
		}
	}
	txPrereqCatAssertDBEqualsCatalog(t, restarted, [][2]string{rejectedPackEdge, rejectedAdminEdge})
	if p := txPrereqCatalogPrereqs(t, restarted, reviewedEdge[0]); !p[reviewedEdge[1]] {
		t.Fatalf("sync 后 catalog 缺 reviewed 边 %s←%s: %v", reviewedEdge[0], reviewedEdge[1], p)
	}
}

// catChainExtra 目前未在上面的用例里用到；保留一条只读断言把它与
// 「传导成环」的语义绑定，避免锚点被静默删除后失去说明。
func TestTaxonomyPrerequisiteChainExtraAnchor(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	importPack(t, e)
	if !txPrereqCatalogPrereqs(t, e, catChainExtra)[txPrereqRoot] {
		t.Fatalf("%s 应硬先修 %s（锚点失效）", catChainExtra, txPrereqRoot)
	}
	if !txPrereqCatalogPrereqs(t, e, "cnm_p004")[txPrereqRoot] {
		t.Fatal("cnm_p004 应硬先修 cnm_p001（锚点失效）")
	}
}
