package taxonomy

import (
	"encoding/json"
	"fmt"
	"sort"
	"testing"
)

/* 手造小图的测试装配器：按 Load 的同一套不变量构造 Catalog，
   让引擎测试不依赖全量数据、可读性更高。
   mustCatalog(t) 在构造失败时 t.Fatalf；catalog() 返回错误。 */

type testSpec struct {
	topics    []*Topic
	edges     []*Edge
	questions []*Question
	aliases   map[string]AliasEntry
}

func (sp testSpec) mustCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := sp.catalog()
	if err != nil {
		t.Fatalf("构造手造图: %v", err)
	}
	return c
}

func (sp testSpec) catalog() (*Catalog, error) {
	c := &Catalog{
		Topics:       sp.topics,
		Edges:        sp.edges,
		Questions:    sp.questions,
		ByID:         map[string]*Topic{},
		Index:        map[string]int{},
		Prereqs:      map[string][]*Edge{},
		DepsOf:       map[string][]*Edge{},
		Bank:         map[string][]*Question{},
		Aliases:      sp.aliases,
		StandardByID: map[string]*StandardItem{},
		anc:          map[string]map[string]bool{},
	}
	for i, tp := range sp.topics {
		if _, dup := c.ByID[tp.ID]; dup {
			return nil, fmt.Errorf("重复知识点 %s", tp.ID)
		}
		c.ByID[tp.ID] = tp
		c.Index[tp.ID] = i
	}
	seenEdge := map[string]bool{}
	// split 出 hard 与 soft 两组，保证 Prereqs 里 hard 在前（与 Load 的排序规则一致）。
	var hard, soft []*Edge
	for _, e := range sp.edges {
		if _, ok := c.ByID[e.TopicID]; !ok {
			return nil, fmt.Errorf("边引用未知 topicId %s", e.TopicID)
		}
		if _, ok := c.ByID[e.PrerequisiteID]; !ok {
			return nil, fmt.Errorf("边引用未知 prerequisiteId %s", e.PrerequisiteID)
		}
		if e.Strength == "" {
			e.Strength = "hard"
		}
		if e.ReviewStatus == "" {
			e.ReviewStatus = "machine"
		}
		key := e.TopicID + "<" + e.PrerequisiteID
		if seenEdge[key] {
			continue
		}
		seenEdge[key] = true
		c.DepsOf[e.PrerequisiteID] = append(c.DepsOf[e.PrerequisiteID], e)
		if e.Strength == "soft" {
			soft = append(soft, e)
		} else {
			hard = append(hard, e)
		}
	}
	for _, e := range append(hard, soft...) {
		c.Prereqs[e.TopicID] = append(c.Prereqs[e.TopicID], e)
	}
	for _, q := range sp.questions {
		tp, ok := c.ByID[q.TopicID]
		if !ok {
			return nil, fmt.Errorf("题目 %s 引用未知 topicId %s", q.ID, q.TopicID)
		}
		if tp.External {
			continue
		}
		c.Bank[q.TopicID] = append(c.Bank[q.TopicID], q)
	}
	for _, tp := range sp.topics {
		if !tp.External && len(c.Bank[tp.ID]) == 0 && tp.DiagnosticQuestion.Question != "" {
			c.Bank[tp.ID] = append(c.Bank[tp.ID], &Question{
				ID:         tp.ID + "_q1",
				TopicID:    tp.ID,
				Difficulty: "standard",
				Type:       legacyQuestionType,
				Prompt:     tp.DiagnosticQuestion.Question,
				Answer:     tp.DiagnosticQuestion.Answer,
			})
		}
	}
	if err := c.checkDAG(); err != nil {
		return nil, err
	}
	return c, nil
}

/* 小图 1（回退模式：每个知识点仅一道 legacy 题）
 *   X(外部) <- F <- E <- C <--hard-- T
 *              F <- D      <--hard-- T
 *              F <- B      <--soft-- T
 *   Z 与 T 无关
 * topics 顺序：X F E C D B T Z
 */
func smallTopics() []*Topic {
	mk := func(id string, grade int, extra func(*Topic)) *Topic {
		tp := &Topic{ID: id, Name: id, Grade: grade, Semester: "上",
			DiagnosticQuestion: DiagnosticQuestion{Question: id + "?", Answer: "1"}}
		if extra != nil {
			extra(tp)
		}
		return tp
	}
	return []*Topic{
		mk("X", 1, func(t *Topic) { t.External = true; t.DiagnosticQuestion = DiagnosticQuestion{} }),
		mk("F", 2, nil), mk("E", 3, nil), mk("C", 4, nil),
		mk("D", 5, nil), mk("B", 6, nil), mk("T", 7, nil), mk("Z", 7, nil),
	}
}

func smallEdges() []*Edge {
	return []*Edge{
		{TopicID: "F", PrerequisiteID: "X", Strength: "hard"},
		{TopicID: "E", PrerequisiteID: "F", Strength: "hard"},
		{TopicID: "C", PrerequisiteID: "E", Strength: "hard"},
		{TopicID: "D", PrerequisiteID: "F", Strength: "hard"},
		{TopicID: "B", PrerequisiteID: "F", Strength: "hard"},
		{TopicID: "T", PrerequisiteID: "B", Strength: "soft"}, // 故意把 soft 写在 hard 前面
		{TopicID: "T", PrerequisiteID: "D", Strength: "hard"},
		{TopicID: "T", PrerequisiteID: "C", Strength: "hard"},
	}
}

// smallCatalog 返回小图 1 的 Catalog（小图无环，构造不会失败）。
func smallCatalog() *Catalog {
	c, err := testSpec{topics: smallTopics(), edges: smallEdges()}.catalog()
	if err != nil {
		panic(err)
	}
	return c
}

// runScript 按引擎的出题顺序作答，返回 [最终会话, 出题的知识点顺序]。
func runScript(t *testing.T, c *Catalog, s *Session, script map[string]string) (*Session, []string) {
	t.Helper()
	asked := []string{}
	for guard := 0; guard < 100; guard++ {
		id := c.NextQuestion(s)
		if id == "" {
			return s, asked
		}
		asked = append(asked, id)
		ans, ok := script[id]
		if !ok {
			t.Fatalf("脚本里没有 %s 的答案（已问 %v）", id, asked)
		}
		ns, err := c.ApplyAnswer(s, id, ans)
		if err != nil {
			t.Fatalf("ApplyAnswer(%s,%s): %v", id, ans, err)
		}
		s = ns
	}
	t.Fatalf("出题循环超过 100 次，疑似死循环（已问 %v）", asked)
	return nil, nil
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assertStrings(t *testing.T, name string, got, want []string) {
	t.Helper()
	if !eqStrings(got, want) {
		t.Errorf("%s = %v，期望 %v", name, got, want)
	}
}

func assertStatus(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s 状态 = %q，期望 %q", name, got, want)
	}
}

func idsOfPath(s *Summary) []string {
	out := make([]string, len(s.Path))
	for i, p := range s.Path {
		out[i] = p.ID
	}
	return out
}

func idsOfRoots(s *Summary) []string {
	out := make([]string, len(s.Roots))
	for i, r := range s.Roots {
		out[i] = r.ID
	}
	return out
}

func mustSession(t *testing.T, c *Catalog, targets []string, opts *CreateSessionOptions) *Session {
	t.Helper()
	s, err := c.CreateSession(targets, opts)
	if err != nil {
		t.Fatalf("CreateSession(%v): %v", targets, err)
	}
	return s
}

// assertTopo 校验 order 中每个先修都排在依赖它的点之前（仅考虑 order 内部节点）。
func assertTopo(t *testing.T, c *Catalog, order []string) {
	t.Helper()
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	for _, id := range order {
		for _, e := range c.Prereqs[id] {
			if p, ok := pos[e.PrerequisiteID]; ok && p >= pos[id] {
				t.Errorf("拓扑序错误：%s（先修）应排在 %s 之前", e.PrerequisiteID, id)
			}
		}
	}
}

// deepEqualJSON 比较两个会话的 JSON 表示（用于「不修改入参 / 可序列化」断言）。
func deepEqualJSON(t *testing.T, name string, a, b any) {
	t.Helper()
	ja, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("%s 序列化: %v", name, err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("%s 序列化: %v", name, err)
	}
	if string(ja) != string(jb) {
		t.Errorf("%s 不一致", name)
	}
}
