package taxonomy

import (
	"fmt"
	"sync"
	"testing"
)

/* 全量数据不变量：计数、DAG、引用完整性、学科分布。
   数据基线来自上游 cn-math-taxonomy（TX-CORE-CODER 卡）。 */

var (
	onceReal   sync.Once
	realCat    *Catalog
	realCatErr error
)

// realCatalog 加载嵌入的全量数据（只加载一次）。
func realCatalog(t *testing.T) *Catalog {
	t.Helper()
	onceReal.Do(func() { realCat, realCatErr = Load() })
	if realCatErr != nil {
		t.Fatalf("Load(): %v", realCatErr)
	}
	return realCat
}

func TestCatalogLoadCounts(t *testing.T) {
	c := realCatalog(t)
	checks := []struct {
		name      string
		got, want int
	}{
		{"topics", len(c.Topics), 3108},
		{"edges", len(c.Edges), 4625},
		{"questions", len(c.Questions), 12579},
		{"standards", len(c.Standards), 774},
		{"aliases", len(c.Aliases), 18},
		{"mappings", len(c.Mappings), 4},
		{"ByID", len(c.ByID), len(c.Topics)},
		{"Index", len(c.Index), len(c.Topics)},
	}
	for _, ch := range checks {
		if ch.got != ch.want {
			t.Errorf("%s = %d，期望 %d", ch.name, ch.got, ch.want)
		}
	}
	hard, soft := 0, 0
	for _, e := range c.Edges {
		switch e.Strength {
		case "hard":
			hard++
		case "soft":
			soft++
		default:
			t.Errorf("边 %s<-%s 强度非法：%q", e.TopicID, e.PrerequisiteID, e.Strength)
		}
		if e.ReviewStatus == "" {
			t.Errorf("边 %s<-%s 缺少 review_status", e.TopicID, e.PrerequisiteID)
		}
	}
	if hard != 2478 || soft != 2147 {
		t.Errorf("hard/soft = %d/%d，期望 2478/2147", hard, soft)
	}
	external := 0
	for _, tp := range c.Topics {
		if tp.External {
			external++
		}
	}
	if external != 0 {
		t.Errorf("外部知识点 = %d，期望 0", external)
	}
}

func TestCatalogSubjectCounts(t *testing.T) {
	c := realCatalog(t)
	want := map[string]int{
		"math": 870, "english": 791, "chinese": 493, "physics": 269, "science": 222,
		"morality": 159, "history": 134, "biology": 66, "geography": 59, "chemistry": 45,
	}
	got := map[string]int{}
	for _, tp := range c.Topics {
		got[c.SubjectOf(tp)]++
	}
	if len(got) != 10 {
		t.Errorf("学科数 = %d，期望 10：%v", len(got), got)
	}
	for s, n := range want {
		if got[s] != n {
			t.Errorf("学科 %s 知识点 = %d，期望 %d", s, got[s], n)
		}
	}
	// math 的先修边数与年级分布
	mathIDs := map[string]bool{}
	for _, tp := range c.Topics {
		if c.SubjectOf(tp) == "math" {
			mathIDs[tp.ID] = true
		}
	}
	mathEdges := 0
	for _, e := range c.Edges {
		if mathIDs[e.TopicID] {
			mathEdges++
		}
	}
	if mathEdges != 1459 {
		t.Errorf("math 先修边 = %d，期望 1459", mathEdges)
	}
	buckets := map[string]int{}
	for _, tp := range c.Topics {
		if c.SubjectOf(tp) != "math" {
			continue
		}
		switch {
		case tp.Grade <= 6:
			buckets["<=6"]++
		case tp.Grade <= 9:
			buckets["7-9"]++
		case tp.Grade <= 11:
			buckets["10-11"]++
		default:
			buckets[">=12"]++
		}
		if tp.Grade >= 10 {
			buckets[">=10"]++
		}
	}
	for k, n := range map[string]int{"<=6": 284, "7-9": 315, "10-11": 143, ">=12": 128, ">=10": 271} {
		if buckets[k] != n {
			t.Errorf("math 年级桶 %s = %d，期望 %d", k, buckets[k], n)
		}
	}
}

func TestCatalogGraphIntegrity(t *testing.T) {
	c := realCatalog(t)
	seen := map[string]bool{}
	for _, e := range c.Edges {
		if e.TopicID == e.PrerequisiteID {
			t.Errorf("自环：%s", e.TopicID)
		}
		if c.ByID[e.TopicID] == nil {
			t.Errorf("边引用未知 topicId %s", e.TopicID)
		}
		if c.ByID[e.PrerequisiteID] == nil {
			t.Errorf("边引用未知 prerequisiteId %s", e.PrerequisiteID)
		}
		key := e.TopicID + "<" + e.PrerequisiteID
		if seen[key] {
			t.Errorf("重复边：%s", key)
		}
		seen[key] = true
	}
	// 引用完整性由 Load 保证，这里再兜底一次 DAG 校验（Kahn 拓扑排序）。
	if err := c.checkDAG(); err != nil {
		t.Errorf("DAG 校验：%v", err)
	}
	// Ancestors 与独立 BFS 一致
	for _, tp := range c.Topics {
		want := map[string]bool{}
		queue := []string{tp.ID}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, e := range c.Prereqs[cur] {
				if !want[e.PrerequisiteID] {
					want[e.PrerequisiteID] = true
					queue = append(queue, e.PrerequisiteID)
				}
			}
		}
		got := c.ancestorSet(tp.ID)
		if len(got) != len(want) {
			t.Errorf("Ancestors(%s) 大小 %d，独立 BFS 为 %d", tp.ID, len(got), len(want))
			continue
		}
		for id := range want {
			if !got[id] {
				t.Errorf("Ancestors(%s) 缺少 %s", tp.ID, id)
				break
			}
		}
	}
}

func TestCatalogSubgraphs(t *testing.T) {
	c := realCatalog(t)
	sample := []string{"cnm_221", "cne_p042", "cns_p014", "cnp_h082", "cnc_p191"}
	for _, id := range sample {
		if c.ByID[id] == nil {
			t.Errorf("样例目标 %s 不存在", id)
			continue
		}
		sub, err := c.Subgraph([]string{id})
		if err != nil {
			t.Errorf("Subgraph(%s): %v", id, err)
			continue
		}
		if len(sub.Nodes) == 0 || sub.Nodes[len(sub.Nodes)-1] != id {
			t.Errorf("Subgraph(%s) 应以目标结尾，实际 %v", id, tail(sub.Nodes, 3))
		}
		assertTopo(t, c, sub.Nodes)
		for _, n := range sub.Nodes {
			if c.Topic(n).External {
				t.Errorf("Subgraph(%s) 含外部节点 %s", id, n)
			}
		}
	}
	// 高中科目必须依赖初中先修
	senior := 0
	for _, tp := range c.Topics {
		if c.SubjectOf(tp) != "math" || tp.Grade < 10 {
			continue
		}
		hasJunior := false
		for a := range c.ancestorSet(tp.ID) {
			if c.ByID[a].Grade <= 9 {
				hasJunior = true
				break
			}
		}
		if hasJunior {
			senior++
		}
	}
	if senior < 100 {
		t.Errorf("高中数学点中带初中先修的数量 = %d，期望 > 100", senior)
	}
}

func tail(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return xs[len(xs)-n:]
}

// TestCatalogAncestorSetConcurrent 并发填充祖先缓存：64 goroutine 对同一
// Catalog 并发调 Ancestors/ancestorSet，验证 RWMutex double-check 下无并发
// 读写（配合 -race 运行；空缓存起跑保证各 goroutine 都会写 map）。
func TestCatalogAncestorSetConcurrent(t *testing.T) {
	c := realCatalog(t)
	// 各 goroutine 取不同起点，保证并发写不同 key + 部分相同 key
	const workers = 64
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ids := []string{c.Topics[i%len(c.Topics)].ID}
			if i%3 == 0 {
				ids = append(ids, "cnm_221") // 公共热点 key，制造并发写同 key
			}
			want := map[string]bool{}
			for _, id := range ids {
				queue := []string{id}
				for len(queue) > 0 {
					cur := queue[0]
					queue = queue[1:]
					for _, e := range c.Prereqs[cur] {
						if !want[e.PrerequisiteID] {
							want[e.PrerequisiteID] = true
							queue = append(queue, e.PrerequisiteID)
						}
					}
				}
			}
			got := c.Ancestors(ids)
			if len(got) != len(want) {
				errs <- fmt.Errorf("worker %d: Ancestors(%v) = %d 个，独立 BFS 为 %d", i, ids, len(got), len(want))
				return
			}
			gotSet := map[string]bool{}
			for _, id := range got {
				gotSet[id] = true
			}
			for id := range want {
				if !gotSet[id] {
					errs <- fmt.Errorf("worker %d: Ancestors(%v) 缺少 %s", i, ids, id)
					return
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestCatalogQuestionsCoverAllTopics(t *testing.T) {
	c := realCatalog(t)
	bankTotal := 0
	for _, tp := range c.Topics {
		if len(c.Bank[tp.ID]) == 0 {
			t.Errorf("知识点 %s 没有题目", tp.ID)
		}
		bankTotal += len(c.Bank[tp.ID])
	}
	if bankTotal != len(c.Questions) {
		t.Errorf("题库题目合计 %d，期望等于 questions 总数 %d", bankTotal, len(c.Questions))
	}
	legacy := 0
	for _, q := range c.Questions {
		if q.Type == legacyQuestionType {
			legacy++
		}
	}
	if legacy != 0 {
		t.Errorf("全量数据不应有合成的 legacy 题，实际 %d", legacy)
	}
}
