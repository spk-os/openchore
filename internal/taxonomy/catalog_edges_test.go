package taxonomy

import (
	"reflect"
	"sync"
	"testing"
)

/* TX-PREREQ-CORE：WithEdges copy-on-write 图快照。
   语义冻结：保留只读内容（Topics/Questions/ByID/Index/Bank/Standards/
   Aliases/Mappings/HasBank），重建 Edges/Prereqs/DepsOf，anc 为新空 map，
   绝不修改 receiver；输入 Edge 值拷贝；rejected 边不进生效集；
   hard 优先、soft 其后、同强度按 Topics 顺序；成环返回错误。
   fixture 复用 smallCatalog（8 节点小图），不触发全量 Load。 */

// edgePrereqIDs 提取某 topic 的 Prereqs 顺序（断言排序用）。
func edgePrereqIDs(c *Catalog, id string) []string {
	es := c.Prereqs[id]
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.PrerequisiteID
	}
	return out
}

// edgeDepIDs 提取某 topic 的 DepsOf（谁依赖它）顺序（断言排序用）。
func edgeDepIDs(c *Catalog, id string) []string {
	es := c.DepsOf[id]
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.TopicID
	}
	return out
}

// 新增 hard/soft 边生效 + Prereqs hard 优先排序 + rejected/重复边不生效。
func TestWithEdgesAppliesAndOrders(t *testing.T) {
	c := smallCatalog()
	// 原 smallEdges 里 T 有 B(soft)/D(hard)/C(hard) 三条边。
	// 重建：去掉 T<-B，加 rejected 的 T<-Z、soft 的 T<-E、重复的 T<-C、
	// 非法 strength 的 B<-E（归一 hard，不成环）。
	in := append(smallEdges()[0:5], smallEdges()[6:]...) // 去掉 T<-B
	in = append(in,
		&Edge{TopicID: "T", PrerequisiteID: "Z", Strength: "hard", ReviewStatus: "rejected"},
		&Edge{TopicID: "T", PrerequisiteID: "E", Strength: "soft"},
		&Edge{TopicID: "T", PrerequisiteID: "C", Strength: "hard"},               // 重复边
		&Edge{TopicID: "B", PrerequisiteID: "E", Strength: "weird", Reason: "x"}, // 归一 hard
	)
	nc, err := c.WithEdges(in)
	if err != nil {
		t.Fatalf("WithEdges: %v", err)
	}
	// Index：X0 F1 E2 C3 D4 B5 T6 Z7。T 的先修：hard C(3) D(4) 在前、soft E 最后。
	if got, want := edgePrereqIDs(nc, "T"), []string{"C", "D", "E"}; !reflect.DeepEqual(got, want) {
		t.Errorf("新图 Prereqs[T] = %v，期望 %v", got, want)
	}
	// rejected 不进生效集：任何结构的 T 先修都不含 Z，DepsOf[Z] 为空。
	for _, e := range nc.Prereqs["T"] {
		if e.PrerequisiteID == "Z" {
			t.Errorf("rejected 边 T<-Z 进入了生效集")
		}
	}
	if len(nc.DepsOf["Z"]) != 0 {
		t.Errorf("rejected 边 T<-Z 进入了 DepsOf[Z]：%v", nc.DepsOf["Z"])
	}
	// B 的先修：F(hard,1) 与 E(归一 hard,2)，按 Topics 序 F 在前。
	if got, want := edgePrereqIDs(nc, "B"), []string{"F", "E"}; !reflect.DeepEqual(got, want) {
		t.Errorf("新图 Prereqs[B] = %v，期望 %v（weird 归一 hard）", got, want)
	}
	if e := nc.Prereqs["B"][1]; e.Strength != "hard" || e.ReviewStatus != "machine" || e.Reason != "x" {
		t.Errorf("归一/补全后的边 = %+v", e)
	}
	// Edges 生效集 = 7 条保留边 + 1(T<-E) + 1(B<-E) = 9；重复、rejected 不计入。
	if got, want := len(nc.Edges), 9; got != want {
		t.Errorf("新图 Edges 数 = %d，期望 %d", got, want)
	}
	// DepsOf 方向同样重建：E 的后继按依赖点 Index 排 C(3) B(5) T(6)。
	if got, want := edgeDepIDs(nc, "E"), []string{"C", "B", "T"}; !reflect.DeepEqual(got, want) {
		t.Errorf("新图 DepsOf[E] = %v，期望 %v", got, want)
	}
}

// copy-on-write：只读内容共享指针，边结构完全独立；原 Catalog 一字不改。
func TestWithEdgesDoesNotMutateReceiver(t *testing.T) {
	c := smallCatalog()
	before := snapshotGraph(t, c)
	// 变更：T 的边换成 Z<-X 一条（原 T 边全部移除）。
	alt := []*Edge{{TopicID: "Z", PrerequisiteID: "X", Strength: "soft"}}
	nc, err := c.WithEdges(alt)
	if err != nil {
		t.Fatalf("WithEdges: %v", err)
	}
	after := snapshotGraph(t, c)
	if !reflect.DeepEqual(before, after) {
		t.Error("WithEdges 修改了原 Catalog（Edges/Prereqs/DepsOf/anc 应不变）")
	}
	// 共享只读内容。
	if nc.Topics[0] != c.Topics[0] || nc.ByID["T"] != c.ByID["T"] || nc.Bank["T"][0] != c.Bank["T"][0] {
		t.Error("只读内容应共享同一批指针")
	}
	// 新图按 alt 重建：T 的先修全没了，新图只有 Z<-X 一条边。
	if len(nc.Prereqs["T"]) != 0 || len(nc.DepsOf["E"]) != 0 {
		t.Errorf("新图 T 先修 = %d 条 / DepsOf[E] = %d 条，期望 0/0", len(nc.Prereqs["T"]), len(nc.DepsOf["E"]))
	}
	if got, want := edgePrereqIDs(nc, "Z"), []string{"X"}; !reflect.DeepEqual(got, want) {
		t.Errorf("新图 Prereqs[Z] = %v，期望 %v", got, want)
	}
	if got, want := edgeDepIDs(nc, "X"), []string{"Z"}; !reflect.DeepEqual(got, want) {
		t.Errorf("新图 DepsOf[X] = %v，期望 %v", got, want)
	}
	if got, want := len(nc.Edges), 1; got != want {
		t.Errorf("新图 Edges 数 = %d，期望 %d", got, want)
	}
}

// 输入 Edge 拷贝语义：WithEdges 之后改入参与新图无关联。
func TestWithEdgesCopiesInputEdges(t *testing.T) {
	c := smallCatalog()
	in := []*Edge{{TopicID: "T", PrerequisiteID: "B", Strength: "hard", Reason: "r1"}}
	nc, err := c.WithEdges(in)
	if err != nil {
		t.Fatalf("WithEdges: %v", err)
	}
	got := nc.Prereqs["T"][0]
	if got.Reason != "r1" || got.Strength != "hard" {
		t.Fatalf("新图边 = %+v", got)
	}
	// 调用方修改输入切片与元素。
	in[0].Reason = "MUTATED"
	in[0].Strength = "soft"
	in[0].TopicID = "Z"
	if got.Reason != "r1" || got.Strength != "hard" || got.TopicID != "T" {
		t.Errorf("调用方修改入参污染了新图：%+v", got)
	}
	if e := nc.Edges[0]; e != got {
		t.Errorf("Edges 与 Prereqs 应指向同一个拷贝对象")
	}
}

// 成环：返回错误，receiver 完全不变，不产生半成品新图。
func TestWithEdgesRejectsCycle(t *testing.T) {
	c := smallCatalog()
	before := snapshotGraph(t, c)
	// T<-C 存在，加 C<-T 直接成 2 元环。
	in := append(smallEdges(), &Edge{TopicID: "C", PrerequisiteID: "T", Strength: "hard"})
	nc, err := c.WithEdges(in)
	if err == nil {
		t.Fatal("成环边集未报错")
	}
	if nc != nil {
		t.Errorf("失败时应返回 nil Catalog，实际 %v", nc)
	}
	if want := "重建先修图: 先修图存在环"; err.Error() != want {
		t.Errorf("错误信息 = %q，期望 %q", err.Error(), want)
	}
	after := snapshotGraph(t, c)
	if !reflect.DeepEqual(before, after) {
		t.Error("失败路径修改了原 Catalog")
	}
	// 非法端点同样报错且不改原图。
	for name, bad := range map[string]*Edge{
		"未知 topic":        {TopicID: "NOPE", PrerequisiteID: "X", Strength: "hard"},
		"未知 prerequisite": {TopicID: "T", PrerequisiteID: "NOPE", Strength: "hard"},
		"自环":              {TopicID: "T", PrerequisiteID: "T", Strength: "hard"},
	} {
		if _, err := c.WithEdges([]*Edge{bad}); err == nil {
			t.Errorf("%s：非法边未报错", name)
		}
	}
	if !reflect.DeepEqual(before, snapshotGraph(t, c)) {
		t.Error("非法边路径修改了原 Catalog")
	}
}

// 新旧图祖先结果独立：旧图祖先缓存不受新图重建影响，新图按新边集计算。
func TestWithEdgesAncestorCachesIndependent(t *testing.T) {
	c := smallCatalog()
	oldAncestors := c.ancestorSet("T") // 预填旧缓存
	if !reflect.DeepEqual(oldAncestors, wantAncestorsT()) {
		t.Fatalf("旧图 ancestorSet(T) = %v，期望 %v", oldAncestors, wantAncestorsT())
	}
	// 重建：去掉 T<-C，祖先里应消失 C 和 E（E 仅经 C 可达）。
	in := []*Edge{
		{TopicID: "F", PrerequisiteID: "X", Strength: "hard"},
		{TopicID: "E", PrerequisiteID: "F", Strength: "hard"},
		{TopicID: "D", PrerequisiteID: "F", Strength: "hard"},
		{TopicID: "B", PrerequisiteID: "F", Strength: "hard"},
		{TopicID: "T", PrerequisiteID: "B", Strength: "soft"},
		{TopicID: "T", PrerequisiteID: "D", Strength: "hard"},
	}
	nc, err := c.WithEdges(in)
	if err != nil {
		t.Fatalf("WithEdges: %v", err)
	}
	gotNew := nc.ancestorSet("T")
	if want := map[string]bool{"X": true, "F": true, "B": true, "D": true}; !reflect.DeepEqual(gotNew, want) {
		t.Errorf("新图 ancestorSet(T) = %v，期望 %v", gotNew, want)
	}
	// 旧图缓存未被新计算触碰。
	if again := c.ancestorSet("T"); !reflect.DeepEqual(again, wantAncestorsT()) {
		t.Errorf("旧图祖先被新图污染：%v", again)
	}
	// 污染新图返回值不影响旧图，反之亦然。
	gotNew["Z"] = true
	delete(gotNew, "X")
	if !reflect.DeepEqual(c.ancestorSet("T"), wantAncestorsT()) {
		t.Error("改写新图祖先结果影响了旧图")
	}
	if again := nc.ancestorSet("T"); !reflect.DeepEqual(again, map[string]bool{"X": true, "F": true, "B": true, "D": true}) {
		t.Errorf("改写返回值后新图缓存被污染：%v", again)
	}
	// Ancestors 公开 API 在新图上按新边集出结果（按 Topics 序：X F D B）。
	if ids := nc.Ancestors([]string{"T"}); !reflect.DeepEqual(ids, []string{"X", "F", "D", "B"}) {
		t.Errorf("新图 Ancestors(T) = %v", ids)
	}
}

// 空边集：non-nil map/slice 约定保持，图仍可用（全点零边）。
func TestWithEdgesEmptySetKeepsConventions(t *testing.T) {
	c := smallCatalog()
	nc, err := c.WithEdges(nil)
	if err != nil {
		t.Fatalf("WithEdges(nil): %v", err)
	}
	if nc.Prereqs == nil || nc.DepsOf == nil || nc.Edges == nil {
		t.Fatal("Prereqs/DepsOf/Edges 必须为 non-nil")
	}
	if len(nc.Prereqs) != len(c.ByID) || len(nc.DepsOf) != len(c.ByID) {
		t.Errorf("Prereqs/DepsOf 键数 = %d/%d，期望各 %d", len(nc.Prereqs), len(nc.DepsOf), len(c.ByID))
	}
	for id := range c.ByID {
		if nc.Prereqs[id] != nil {
			t.Errorf("知识点 %s 的 Prereqs 应为 nil（无边时），实际 %v", id, nc.Prereqs[id])
		}
		if nc.DepsOf[id] != nil {
			t.Errorf("知识点 %s 的 DepsOf 应为 nil（无边时），实际 %v", id, nc.DepsOf[id])
		}
		if len(nc.Prereqs[id]) != 0 || len(nc.DepsOf[id]) != 0 {
			t.Errorf("知识点 %s 空边集却有边：%v / %v", id, nc.Prereqs[id], nc.DepsOf[id])
		}
	}
	if got := nc.ancestorSet("T"); len(got) != 0 || got == nil {
		t.Errorf("空边集 ancestorSet(T) = %v，期望非 nil 空 map", got)
	}
	if ids := nc.Ancestors([]string{"T"}); len(ids) != 0 {
		t.Errorf("空边集 Ancestors(T) = %v，期望空", ids)
	}
	if _, err := nc.TopoSort([]string{"T", "C"}); err != nil {
		t.Errorf("空边集 TopoSort: %v", err)
	}
}

// 并发：旧图读热点 + 新图读热点同时进行（copy-on-write 的核心场景），
// -race 下无数据竞争。
func TestWithEdgesConcurrentOldAndNew(t *testing.T) {
	c := smallCatalog()
	in := append(smallEdges(), &Edge{TopicID: "Z", PrerequisiteID: "T", Strength: "hard"})
	nc, err := c.WithEdges(in)
	if err != nil {
		t.Fatalf("WithEdges: %v", err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				if got := c.ancestorSet("T"); !reflect.DeepEqual(got, wantAncestorsT()) {
					t.Errorf("旧图并发祖先 = %v", got)
				}
				return
			}
			want := map[string]bool{"C": true, "D": true, "B": true, "E": true, "F": true, "X": true, "T": true}
			if got := nc.ancestorSet("Z"); !reflect.DeepEqual(got, want) {
				t.Errorf("新图并发祖先 = %v，期望 %v", got, want)
			}
		}(i)
	}
	close(start)
	wg.Wait()
}

// snapshotGraph 序列化图的全部可变结构（Edges/Prereqs/DepsOf/anc），
// 用于「receiver 不被修改」的精确对比。anc 需要先填满才有意义，
// 这里顺带预填所有非空先修点的祖先缓存。
func snapshotGraph(t *testing.T, c *Catalog) map[string]any {
	t.Helper()
	for id := range c.ByID {
		c.ancestorSet(id)
	}
	snapshotPrereqs := func(m map[string][]*Edge) map[string][]Edge {
		out := map[string][]Edge{}
		for id, es := range m {
			vals := make([]Edge, len(es))
			for i, e := range es {
				vals[i] = *e
			}
			out[id] = vals
		}
		return out
	}
	ancCopy := map[string]map[string]bool{}
	for id, m := range c.anc {
		ancCopy[id] = copyBoolSet(m)
	}
	edges := make([]Edge, len(c.Edges))
	for i, e := range c.Edges {
		edges[i] = *e
	}
	return map[string]any{
		"edges":   edges,
		"prereqs": snapshotPrereqs(c.Prereqs),
		"depsof":  snapshotPrereqs(c.DepsOf),
		"anc":     ancCopy,
	}
}
