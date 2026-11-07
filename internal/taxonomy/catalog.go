// Package taxonomy 提供内置的中小学知识图谱（cn-math-taxonomy 全量数据）与
// 诊断引擎的纯领域实现：不接数据库、不渲染 UI、不执行任意代码。
//
// 数据通过 go:embed 内置（data/*.json，全量 3108 知识点 / 4625 先修边 /
// 12579 题目 / 774 课标条目），Load() 解析并校验后返回完整 Catalog。
// 诊断算法完整移植自上游 diagnose/engine.js：会话是可 JSON 序列化的纯数据，
// 出题、判题、汇总均为确定性纯函数。
package taxonomy

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
)

//go:embed data/*.json data/mappings/*.json
var dataFS embed.FS

// 数据文件名（相对包内 data/ 目录）。
const (
	topicsFile         = "data/topics.json"
	depsFile           = "data/dependencies.json"
	questionsFile      = "data/questions.json"
	standardsDataFile  = "data/cn-curriculum-standards.json"
	aliasesDataFile    = "data/id-aliases.json"
	mappingsDir        = "data/mappings"
	legacyQuestionType = "legacy" // 无题库时由 diagnostic_question 合成的题目的 type
)

// Topic 是知识点节点（topics.json 原字段全部保留）。
type Topic struct {
	ID                 string             `json:"id"`
	Name               string             `json:"name"`
	Domain             string             `json:"domain"`
	Grade              int                `json:"grade"`
	Semester           string             `json:"semester"`
	Chapter            string             `json:"chapter"`
	Type               string             `json:"type"` // conceptual/procedural/reasoning/application/modeling
	Description        string             `json:"description"`
	MasteryCriteria    []string           `json:"mastery_criteria"`
	DiagnosticQuestion DiagnosticQuestion `json:"diagnostic_question"`
	External           bool               `json:"external"`
	Standards          []StandardRef      `json:"standards"`
	RootReason         string             `json:"root_reason,omitempty"` // 仅图的起点有
	Subject            string             `json:"subject,omitempty"`     // math/english/physics/chinese/science/morality/history/geography/biology/chemistry
	MathLinks          []MathLink         `json:"mathLinks,omitempty"`   // 物理→数学的跨学科链接
	// mappings / aliases：上游 topics.json 目前没有这两个字段，但为保持「类型保留原字段」
	// 的契约，解析时宽松接收（见 loadRaw）。
	Mappings map[string]MappingEntry `json:"mappings,omitempty"`
	Aliases  []string                `json:"aliases,omitempty"`
}

// DiagnosticQuestion 是知识点内置诊断题。
type DiagnosticQuestion struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// StandardRef 是课标映射条目。
type StandardRef struct {
	ID         string `json:"id"`
	Confidence string `json:"confidence"`
	Note       string `json:"note,omitempty"`
}

// MathLink 是物理知识点指向数学知识点的链接。
type MathLink struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// MappingEntry 是其他教材版本的册/章对照（topics.mappings 的值）。
type MappingEntry struct {
	Grade   int    `json:"grade"`
	Term    string `json:"term"`
	Unit    string `json:"unit,omitempty"`
	Chapter string `json:"chapter,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Edge 是先修边：topicId 依赖 prerequisiteId（学 topicId 之前需要 prerequisiteId）。
type Edge struct {
	TopicID        string `json:"topicId"`
	PrerequisiteID string `json:"prerequisiteId"`
	Strength       string `json:"strength"` // hard/soft
	Reason         string `json:"reason"`
	// ReviewStatus 上游 dependencies.json 目前未写该字段；规范约定默认 "machine"，
	// Load 时对空值统一补全，使其成为可依赖的稳定字段。
	ReviewStatus string `json:"review_status,omitempty"`
}

// Choice 是选择题选项。figure 是不可信的内联 SVG 字符串，仅作为数据保留，本包不渲染。
type Choice struct {
	Key    string `json:"key"`
	Text   string `json:"text"`
	Figure string `json:"figure,omitempty"`
}

// Question 是题库中的一道题（questions.json 原字段全部保留）。
type Question struct {
	ID              string   `json:"id"`
	TopicID         string   `json:"topicId"`
	Difficulty      string   `json:"difficulty"` // basic/standard/challenge/advanced
	Type            string   `json:"type"`       // numeric/choice/short/self；合成题用 legacy
	Prompt          string   `json:"prompt"`
	Choices         []Choice `json:"choices,omitempty"`
	Answer          string   `json:"answer"`
	Accept          []string `json:"accept,omitempty"`
	Explanation     string   `json:"explanation"`
	Verify          string   `json:"verify,omitempty"` // 受限表达式字符串，仅由 VerifyEvaluator 解释，不执行
	Unordered       bool     `json:"unordered,omitempty"`
	Figure          string   `json:"figure,omitempty"` // 不可信 SVG 字符串数据，本包不渲染
	HideChoiceText  bool     `json:"hideChoiceText,omitempty"`
	Percent         bool     `json:"percent,omitempty"`
	FixedFromSource string   `json:"fixed_from_source,omitempty"`
}

// StandardItem 是课标条目（cn-curriculum-standards.json items 数组元素）。
type StandardItem struct {
	ID          string `json:"id"`
	Domain      string `json:"domain"`
	Section     string `json:"section"`
	Clause      string `json:"clause"`
	Text        string `json:"text"`
	PageOrRef   string `json:"page_or_ref"`
	Verbatim    bool   `json:"verbatim"`
	TextStatus  string `json:"text_status,omitempty"`
	Note        string `json:"note,omitempty"`
	WordingNote string `json:"wording_note,omitempty"`
	Theme       string `json:"theme,omitempty"`
	Elective    bool   `json:"elective,omitempty"` // 上游 3 条为 bool true
	Module      string `json:"module,omitempty"`
}

// standardsFileExtra 收录课标文件里各学科的 source 说明块（字段按上游 JSON 宽松保留）。
type standardsFileExtra struct {
	Title  string `json:"title"`
	Issuer string `json:"issuer"`
	Year   int    `json:"year"`
	Note   string `json:"note,omitempty"`
}

// standardsFile 是 cn-curriculum-standards.json 的顶层结构。
type standardsFile struct {
	Source        json.RawMessage    `json:"source"`
	Items         []StandardItem     `json:"items"`
	English       standardsFileExtra `json:"english"`
	Physics       standardsFileExtra `json:"physics"`
	PhysicsSenior standardsFileExtra `json:"physics_senior"`
	Chinese       standardsFileExtra `json:"chinese"`
	Science       standardsFileExtra `json:"science"`
	Morality      standardsFileExtra `json:"morality"`
	History       standardsFileExtra `json:"history"`
	Geography     standardsFileExtra `json:"geography"`
	Biology       standardsFileExtra `json:"biology"`
	Chemistry     standardsFileExtra `json:"chemistry"`
}

// AliasEntry 是旧 id（cnm_ext_*）到正式知识点的跳转。
type AliasEntry struct {
	Name    string   `json:"name"`
	Targets []string `json:"targets"`
}

// aliasesFile 是 id-aliases.json 的顶层结构。
type aliasesFile struct {
	Description string                `json:"description"`
	Aliases     map[string]AliasEntry `json:"aliases"`
}

// editionMapping 是 data/mappings/*.json（教材版本对照表）。
type editionMapping struct {
	Edition string `json:"edition"`
	Label   string `json:"label"`
	Status  string `json:"status"`
	Source  string `json:"source,omitempty"`
	Note    string `json:"note,omitempty"`
	Entries []struct {
		TopicID string `json:"topicId"`
		Grade   int    `json:"grade"`
		Term    string `json:"term"`
		Unit    string `json:"unit,omitempty"`
		Chapter string `json:"chapter,omitempty"`
		Note    string `json:"note,omitempty"`
	} `json:"entries"`
}

// Catalog 是 Load() 的结果：全量数据 + 校验保证的不变量。
// Topics/Edges/Questions 保持文件顺序；ByID 等索引为查找用。
type Catalog struct {
	Topics    []*Topic
	Edges     []*Edge
	Questions []*Question

	ByID         map[string]*Topic
	Index        map[string]int         // id -> Topics 中的下标（出题次序的确定性依据）
	Prereqs      map[string][]*Edge     // topicID -> 直接先修边（hard 在前、soft 在后，同强度按文件顺序）
	DepsOf       map[string][]*Edge     // prerequisiteID -> 直接后继边（按文件顺序）
	Bank         map[string][]*Question // topicID -> 该知识点的题（按题号升序）
	Standards    []StandardItem
	StandardByID map[string]*StandardItem
	Aliases      map[string]AliasEntry
	Mappings     map[string]*editionMapping // edition key -> 对照表（含 example 占位文件）
	HasBank      bool                       // 是否有文件级题库（false = 全部回退 diagnostic_question）

	anc   map[string]map[string]bool // 祖先集合缓存（Load 后惰性填充，ancMu 保护并发填充）
	ancMu sync.RWMutex
}

// ancestorSet 计算严格祖先集合（不含自身），带缓存。
// 图经 Load 的 DAG 校验，这里不再做环检测。并发安全：RWMutex double-check
// + 持写锁计算后写入（DAG 保证递归有限）。
// 封装保证：所有返回值都是缓存条目的独立拷贝，调用方修改返回 map
// 不会污染缓存或其他调用方（copyBoolSet）。
func (c *Catalog) ancestorSet(id string) map[string]bool {
	c.ancMu.RLock()
	m, ok := c.anc[id]
	c.ancMu.RUnlock()
	if ok {
		return copyBoolSet(m)
	}
	c.ancMu.Lock()
	defer c.ancMu.Unlock()
	if m, ok := c.anc[id]; ok { // double-check：等锁期间可能已被并发填充
		return copyBoolSet(m)
	}
	out := map[string]bool{}
	var visit func(string)
	visit = func(cur string) {
		for _, e := range c.Prereqs[cur] {
			p := e.PrerequisiteID
			if out[p] {
				continue
			}
			out[p] = true
			visit(p)
		}
	}
	visit(id)
	c.anc[id] = out
	return copyBoolSet(out) // out 已存入缓存，不能直接暴露
}

// copyBoolSet 复制 bool 集合，返回值与入参不共享底层存储；
// nil/空输入返回空 map（非 nil），对返回值的修改永远安全。
func copyBoolSet(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// loadJSON 从内嵌 FS 读一个 JSON 文件。
func loadJSON(name string, v any) error {
	raw, err := dataFS.ReadFile(name)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", name, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("解析 %s: %w", name, err)
	}
	return nil
}

// Load 解析内嵌的全量数据并校验，返回完整 Catalog。
// 校验内容：知识点 ID 唯一、边引用合法、图为 DAG（无环）、题目引用合法、
// 题目 ID 唯一且形如 <topicID>_qN、choice 题选项合法、边 review_status 默认补 machine。
// 任何校验失败都返回错误——内置数据损坏是程序性错误，不应静默降级。
func Load() (*Catalog, error) {
	var topics []*Topic
	if err := loadJSON(topicsFile, &topics); err != nil {
		return nil, err
	}
	var edges []*Edge
	if err := loadJSON(depsFile, &edges); err != nil {
		return nil, err
	}
	var questions []*Question
	if err := loadJSON(questionsFile, &questions); err != nil {
		return nil, err
	}
	var std standardsFile
	if err := loadJSON(standardsDataFile, &std); err != nil {
		return nil, err
	}
	var aliases aliasesFile
	if err := loadJSON(aliasesDataFile, &aliases); err != nil {
		return nil, err
	}

	c := &Catalog{
		Topics:       topics,
		Edges:        edges,
		Questions:    questions,
		ByID:         make(map[string]*Topic, len(topics)),
		Index:        make(map[string]int, len(topics)),
		Prereqs:      make(map[string][]*Edge, len(topics)),
		DepsOf:       make(map[string][]*Edge, len(topics)),
		Bank:         make(map[string][]*Question, len(topics)),
		Standards:    std.Items,
		StandardByID: make(map[string]*StandardItem, len(std.Items)),
		Aliases:      aliases.Aliases,
		Mappings:     make(map[string]*editionMapping),
	}

	// 1. 知识点 ID 唯一 + 建索引。
	for i, t := range topics {
		if t.ID == "" {
			return nil, fmt.Errorf("topics[%d] 缺少 id", i)
		}
		if _, dup := c.ByID[t.ID]; dup {
			return nil, fmt.Errorf("知识点 id 重复: %s", t.ID)
		}
		c.ByID[t.ID] = t
		c.Index[t.ID] = i
		c.Prereqs[t.ID] = nil
		c.DepsOf[t.ID] = nil
		c.Bank[t.ID] = nil
	}
	if len(topics) == 0 {
		return nil, errors.New("topics.json 为空")
	}

	// 2. 边引用合法 + 去重 + review_status 默认 machine。
	seenEdge := make(map[string]bool, len(edges))
	for i, e := range edges {
		if _, ok := c.ByID[e.TopicID]; !ok {
			return nil, fmt.Errorf("dependencies[%d] 未知 topicId: %s", i, e.TopicID)
		}
		if _, ok := c.ByID[e.PrerequisiteID]; !ok {
			return nil, fmt.Errorf("dependencies[%d] 未知 prerequisiteId: %s", i, e.PrerequisiteID)
		}
		if e.TopicID == e.PrerequisiteID {
			return nil, fmt.Errorf("dependencies[%d] 自环边: %s", i, e.TopicID)
		}
		key := e.TopicID + "<" + e.PrerequisiteID
		if seenEdge[key] {
			continue // 上游 buildGraph 对重复边静默去重，这里保持一致
		}
		seenEdge[key] = true
		if e.Strength != "hard" && e.Strength != "soft" {
			e.Strength = "hard" // 上游 buildGraph 同样把非 soft 归一为 hard
		}
		if e.ReviewStatus == "" {
			e.ReviewStatus = "machine"
		}
		c.Prereqs[e.TopicID] = append(c.Prereqs[e.TopicID], e)
		c.DepsOf[e.PrerequisiteID] = append(c.DepsOf[e.PrerequisiteID], e)
	}

	// 上游 buildGraph：先修按 hard 优先、再 soft，同强度按 topics 文件顺序，保证出题次序确定。
	edgeRank := func(e *Edge) int64 {
		if e.Strength == "hard" {
			return int64(c.Index[e.PrerequisiteID])
		}
		return int64(1e6) + int64(c.Index[e.PrerequisiteID])
	}
	for id, es := range c.Prereqs {
		if len(es) < 2 {
			continue
		}
		sort.SliceStable(es, func(i, j int) bool { return edgeRank(es[i]) < edgeRank(es[j]) })
		c.Prereqs[id] = es
	}
	for id, es := range c.DepsOf {
		if len(es) < 2 {
			continue
		}
		sort.SliceStable(es, func(i, j int) bool { return c.Index[es[i].TopicID] < c.Index[es[j].TopicID] })
		c.DepsOf[id] = es
	}

	// 3. 题库：引用合法、ID 唯一、非 external 知识点收录。
	seenQ := make(map[string]bool, len(questions))
	fromFile := 0
	for i, q := range questions {
		t, ok := c.ByID[q.TopicID]
		if !ok {
			return nil, fmt.Errorf("questions[%d] 未知 topicId: %s (%s)", i, q.TopicID, q.ID)
		}
		if seenQ[q.ID] {
			return nil, fmt.Errorf("题目 id 重复: %s", q.ID)
		}
		seenQ[q.ID] = true
		if t.External {
			continue // 上游：外部节点不收题
		}
		c.Bank[q.TopicID] = append(c.Bank[q.TopicID], q)
		fromFile++
	}
	c.HasBank = fromFile > 0

	// 没有题库（或某知识点没有题）时回退到 diagnostic_question，合成 legacy 题。
	for _, t := range topics {
		if t.External || len(c.Bank[t.ID]) > 0 {
			continue
		}
		if t.DiagnosticQuestion.Question != "" {
			c.Bank[t.ID] = append(c.Bank[t.ID], &Question{
				ID:          t.ID + "_q1",
				TopicID:     t.ID,
				Difficulty:  "standard",
				Type:        legacyQuestionType,
				Prompt:      t.DiagnosticQuestion.Question,
				Answer:      t.DiagnosticQuestion.Answer,
				Explanation: "",
			})
		}
	}

	// 4. 课标条目 ID 唯一。
	for i, s := range std.Items {
		if s.ID == "" {
			return nil, fmt.Errorf("standards items[%d] 缺少 id", i)
		}
		if _, dup := c.StandardByID[s.ID]; dup {
			return nil, fmt.Errorf("课标 id 重复: %s", s.ID)
		}
		c.StandardByID[s.ID] = &std.Items[i]
	}

	// 5. 教材版本对照表（全部内嵌文件，含 example 占位）。
	entries, err := dataFS.ReadDir(mappingsDir)
	if err != nil {
		return nil, fmt.Errorf("读取 %s: %w", mappingsDir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !hasJSONSuffix(ent.Name()) {
			continue
		}
		var m editionMapping
		if err := loadJSON(mappingsDir+"/"+ent.Name(), &m); err != nil {
			return nil, err
		}
		for _, me := range m.Entries {
			if _, ok := c.ByID[me.TopicID]; !ok {
				return nil, fmt.Errorf("mappings/%s 未知 topicId: %s", ent.Name(), me.TopicID)
			}
		}
		if m.Edition == "" {
			m.Edition = trimJSONSuffix(ent.Name())
		}
		c.Mappings[m.Edition] = &m
	}

	// 6. DAG 校验（Kahn 拓扑排序一遍，剩余节点即环）。
	if err := c.checkDAG(); err != nil {
		return nil, err
	}
	c.anc = make(map[string]map[string]bool)
	return c, nil
}

// WithEdges 以 copy-on-write 语义基于当前 Catalog 生成新 Catalog：
// Topics/Questions/ByID/Index/Bank/Standards/Aliases/Mappings 等只读内容
// 原样共享（节点与题库不可变），Edges/Prereqs/DepsOf 按入参完全重建，
// 祖先缓存（anc）为新空 map——旧 Catalog 可继续供在途请求安全读取，
// 新旧两图的缓存互不干扰。
//
// 语义与 Load 的边处理一致：
//   - 输入 Edge 逐条值拷贝，调用方后续修改入参不得污染新 Catalog；
//   - 合法端点 / 自环校验，重复边静默去重；
//   - strength 非 soft 归一为 hard；review_status 为空补 machine；
//   - review_status='rejected' 的边不进入生效集（Edges/Prereqs/DepsOf 均排除，
//     行是否保留在数据库由 store 层负责，本内存图只表达生效状态）；
//   - Prereqs hard 优先、soft 其后，同强度按 Topics 顺序（edgeRank）；
//   - 新图必须仍是 DAG（checkDAG），成环返回错误。
//
// 失败时返回 nil 和错误，receiver 及其缓存完全不变。
func (c *Catalog) WithEdges(edges []*Edge) (*Catalog, error) {
	nc := &Catalog{
		Topics:       c.Topics,
		Questions:    c.Questions,
		ByID:         c.ByID,
		Index:        c.Index,
		Bank:         c.Bank,
		Standards:    c.Standards,
		StandardByID: c.StandardByID,
		Aliases:      c.Aliases,
		Mappings:     c.Mappings,
		HasBank:      c.HasBank,
		Prereqs:      make(map[string][]*Edge, len(c.ByID)),
		DepsOf:       make(map[string][]*Edge, len(c.ByID)),
	}
	for id := range c.ByID {
		nc.Prereqs[id] = nil
		nc.DepsOf[id] = nil
	}
	seenEdge := make(map[string]bool, len(edges))
	nc.Edges = make([]*Edge, 0, len(edges))
	for i, e := range edges {
		if _, ok := c.ByID[e.TopicID]; !ok {
			return nil, fmt.Errorf("edges[%d] 未知 topicId: %s", i, e.TopicID)
		}
		if _, ok := c.ByID[e.PrerequisiteID]; !ok {
			return nil, fmt.Errorf("edges[%d] 未知 prerequisiteId: %s", i, e.PrerequisiteID)
		}
		if e.TopicID == e.PrerequisiteID {
			return nil, fmt.Errorf("edges[%d] 自环边: %s", i, e.TopicID)
		}
		if e.ReviewStatus == "rejected" {
			continue // 垃圾边保留在库，不进内存生效图
		}
		key := e.TopicID + "<" + e.PrerequisiteID
		if seenEdge[key] {
			continue // 与 Load 一致：重复边静默去重
		}
		seenEdge[key] = true
		ne := *e // 值拷贝：调用方后续改入参不影响新图
		if ne.Strength != "hard" && ne.Strength != "soft" {
			ne.Strength = "hard"
		}
		if ne.ReviewStatus == "" {
			ne.ReviewStatus = "machine"
		}
		nc.Edges = append(nc.Edges, &ne)
		nc.Prereqs[ne.TopicID] = append(nc.Prereqs[ne.TopicID], &ne)
		nc.DepsOf[ne.PrerequisiteID] = append(nc.DepsOf[ne.PrerequisiteID], &ne)
	}
	// 排序与 Load 同规则：Prereqs hard 在前（按先修点 Topics 序）、soft 其后；
	// DepsOf 按依赖点的 Topics 序。
	edgeRank := func(e *Edge) int64 {
		if e.Strength == "hard" {
			return int64(c.Index[e.PrerequisiteID])
		}
		return int64(1e6) + int64(c.Index[e.PrerequisiteID])
	}
	for id, es := range nc.Prereqs {
		if len(es) < 2 {
			continue
		}
		sort.SliceStable(es, func(i, j int) bool { return edgeRank(es[i]) < edgeRank(es[j]) })
		nc.Prereqs[id] = es
	}
	for id, es := range nc.DepsOf {
		if len(es) < 2 {
			continue
		}
		sort.SliceStable(es, func(i, j int) bool { return nc.Index[es[i].TopicID] < nc.Index[es[j].TopicID] })
		nc.DepsOf[id] = es
	}
	if err := nc.checkDAG(); err != nil {
		return nil, fmt.Errorf("重建先修图: %w", err)
	}
	nc.anc = make(map[string]map[string]bool)
	return nc, nil
}

// checkDAG 用 Kahn 算法验证整张先修图无环。
func (c *Catalog) checkDAG() error {
	indeg := make(map[string]int, len(c.Topics))
	for id := range c.ByID {
		indeg[id] = 0
	}
	for id, es := range c.Prereqs {
		indeg[id] = len(es)
	}
	queue := make([]string, 0, len(c.Topics))
	for _, t := range c.Topics {
		if indeg[t.ID] == 0 {
			queue = append(queue, t.ID)
		}
	}
	done := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		done++
		for _, e := range c.DepsOf[id] {
			indeg[e.TopicID]--
			if indeg[e.TopicID] == 0 {
				queue = append(queue, e.TopicID)
			}
		}
	}
	if done != len(c.Topics) {
		return errors.New("先修图存在环")
	}
	return nil
}

// QuestionByID 按 id 查找题库中的题（含 legacy 合成题）。
func (c *Catalog) QuestionByID(qid string) *Question {
	topicID := qid
	for i := len(qid) - 1; i >= 0; i-- {
		if qid[i] == '_' {
			topicID = qid[:i]
			break
		}
	}
	for _, q := range c.Bank[topicID] {
		if q.ID == qid {
			return q
		}
	}
	return nil
}

// Topic 查找知识点；不存在返回 nil。
func (c *Catalog) Topic(id string) *Topic { return c.ByID[id] }

// QuestionsOf 返回某知识点的全部题（无题返回 nil；含 legacy 合成题）。
func (c *Catalog) QuestionsOf(topicID string) []*Question { return c.Bank[topicID] }

func hasJSONSuffix(s string) bool {
	return len(s) > 5 && s[len(s)-5:] == ".json"
}

func trimJSONSuffix(s string) string {
	if hasJSONSuffix(s) {
		return s[:len(s)-5]
	}
	return s
}
