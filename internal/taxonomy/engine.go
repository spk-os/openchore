package taxonomy

import (
	"errors"
	"fmt"
	"sort"
)

/* 会话与诊断引擎：完整移植上游 diagnose/engine.js 的
   createSession / nextQuestion / nextItem / applyAnswer / replay /
   statusOf / summarize / pickQuestion。 */

// 会话与引擎常量（与上游一致）。
const (
	DefaultLimit   = 16 // 普通诊断默认题量上限
	ScreenLimit    = 40 // 筛查模式默认题量上限
	DefaultConfirm = 3  // 默认每个知识点判掌握所需的连续答对题数
)

// ConfirmChoices 界面可选的确认题数。
var ConfirmChoices = []int{2, 3, 5}

// ClampConfirm 界面输入收敛：只接受 2/3/5，其余回退默认 3。
func ClampConfirm(n int) int {
	for _, c := range ConfirmChoices {
		if n == c {
			return n
		}
	}
	return DefaultConfirm
}

// NormalizeConfirm 引擎内部：任意 1..10 的确认题数（测试可用 1）。
func NormalizeConfirm(n int) int {
	if n >= 1 && n <= 10 {
		return n
	}
	return DefaultConfirm
}

// 状态值（与上游完全一致的四种）。
const (
	StatusMastered = "mastered"
	StatusShaky    = "shaky"
	StatusWeak     = "weak"
	StatusUnknown  = "unknown"
)

// 作答结果。
const (
	ResultCorrect = "correct"
	ResultWrong   = "wrong"
	ResultSkip    = "skip"
)

// 会话模式。
const (
	ModeTarget = "target"
	ModeScreen = "screen"
)

// StatusEntry 是某知识点在会话里的判定。
type StatusEntry struct {
	S   string `json:"s"`   // mastered/shaky/weak
	Src string `json:"src"` // direct（直接作答判定）/ inferred（推断）
}

// AnswerRecord 是一次作答记录（answer log 条目，可序列化、可重放）。
type AnswerRecord struct {
	ID     string `json:"id"`
	QID    string `json:"qid,omitempty"`
	Result string `json:"result"`
	Text   string `json:"text,omitempty"`
}

// AttemptRecord 是某知识点的一次出题尝试。
type AttemptRecord struct {
	QID    string `json:"qid"`
	Result string `json:"result"`
}

// Session 是一次诊断会话：纯数据、可 JSON 序列化、可重放（Replay）。
type Session struct {
	Targets  []string                   `json:"targets"`
	Limit    int                        `json:"limit"`
	Confirm  int                        `json:"confirm"`
	Order    []string                   `json:"order"` // 诊断范围（拓扑序）
	Status   map[string]StatusEntry     `json:"status"`
	Stack    []string                   `json:"stack"` // 出题栈（后续要问的知识点，栈顶在前）
	Answers  []AnswerRecord             `json:"answers"`
	Attempts map[string][]AttemptRecord `json:"attempts"` // topicID -> 出过的题与结果
	Seed     string                     `json:"seed"`
	Avoid    []string                   `json:"avoid,omitempty"`
	Mode     string                     `json:"mode"`
	Scope    []string                   `json:"scope,omitempty"` // 筛查模式的范围（= 目标集合本身）
}

// cloneSession 深拷贝会话（applyAnswer 不修改入参的保证）。
func cloneSession(s *Session) *Session {
	out := &Session{
		Targets:  append([]string(nil), s.Targets...),
		Limit:    s.Limit,
		Confirm:  s.Confirm,
		Order:    append([]string(nil), s.Order...),
		Status:   make(map[string]StatusEntry, len(s.Status)),
		Stack:    append([]string(nil), s.Stack...),
		Answers:  append([]AnswerRecord(nil), s.Answers...),
		Attempts: make(map[string][]AttemptRecord, len(s.Attempts)),
		Seed:     s.Seed,
		Avoid:    append([]string(nil), s.Avoid...),
		Mode:     s.Mode,
	}
	if s.Scope != nil {
		out.Scope = append([]string(nil), s.Scope...)
	}
	for k, v := range s.Status {
		out.Status[k] = v
	}
	for k, v := range s.Attempts {
		out.Attempts[k] = append([]AttemptRecord(nil), v...)
	}
	return out
}

// CreateSessionOptions 是 CreateSession 的可选参数。
type CreateSessionOptions struct {
	Mode    string   // "target"（默认）或 "screen"
	Limit   int      // 题量上限；0 = 默认（target 16 / screen 40）
	Confirm int      // 判掌握所需连续答对题数；0 = 默认 3
	Seed    string   // 选题种子；"" = "default"
	Avoid   []string // 尽量避开的题目 id
}

// CreateSession 创建诊断会话。
//   - target 模式：范围 = 目标 + 全部祖先（拓扑序），答错沿先修回溯（hard 先于 soft）；
//   - screen 模式：范围 = 目标集合本身，回溯只在范围内进行。
//
// 离目标最近（拓扑序最靠后）的目标先问；其余目标作为后备留在栈里。
func (c *Catalog) CreateSession(targets []string, opts *CreateSessionOptions) (*Session, error) {
	if opts == nil {
		opts = &CreateSessionOptions{}
	}
	ts := dedupStrings(targets)
	if len(ts) == 0 {
		return nil, errors.New("至少需要一个目标知识点")
	}
	for _, id := range ts {
		if _, ok := c.ByID[id]; !ok {
			return nil, fmt.Errorf("未知知识点: %s", id)
		}
	}
	screen := opts.Mode == ModeScreen
	limit := opts.Limit
	if limit <= 0 {
		if screen {
			limit = ScreenLimit
		} else {
			limit = DefaultLimit
		}
	}
	confirm := NormalizeConfirm(opts.Confirm)
	var order []string
	var err error
	if screen {
		order, err = c.TopoSort(ts)
	} else {
		var sub *Subgraph
		sub, err = c.Subgraph(ts)
		if err == nil {
			order = sub.Nodes
		}
	}
	if err != nil {
		return nil, err
	}
	pos := make(map[string]int, len(order))
	for i, id := range order {
		pos[id] = i
	}
	stack := []string{}
	for _, id := range ts {
		if c.IsTestable(id) {
			stack = append(stack, id)
		}
	}
	// 离目标最近（拓扑序最靠后）的先问
	sort.SliceStable(stack, func(i, j int) bool { return pos[stack[j]] < pos[stack[i]] })
	s := &Session{
		Targets:  ts,
		Limit:    limit,
		Confirm:  confirm,
		Order:    order,
		Status:   map[string]StatusEntry{},
		Stack:    stack,
		Answers:  []AnswerRecord{},
		Attempts: map[string][]AttemptRecord{},
		Seed:     opts.Seed,
		Avoid:    dedupStrings(opts.Avoid),
		Mode:     ModeTarget,
	}
	if s.Seed == "" {
		s.Seed = "default"
	}
	if screen {
		s.Mode = ModeScreen
		s.Scope = append([]string(nil), ts...)
	}
	return s, nil
}

func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

/* ---- 可种子化的伪随机（mulberry32 + FNV-1a）：同一 (seed, key) 永远同一数 ---- */

func hashStr(str string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(str); i++ {
		h ^= uint32(str[i])
		h = (h * 16777619) & 0xffffffff
	}
	return h
}

// Mulberry32 返回一个 mulberry32 伪随机序列（与上游同算法）。
func Mulberry32(a uint32) func() float64 {
	return func() float64 {
		a = (a + 0x6D2B79F5) & 0xffffffff
		t := a
		t = imul(t^(t>>15), t|1)
		t ^= t + imul(t^(t>>7), t|61)
		return float64((t^(t>>14))&0xffffffff) / 4294967296.0
	}
}

func imul(a, b uint32) uint32 {
	return uint32(int32(a) * int32(b))
}
func rand01(seed, key string) float64 {
	f := Mulberry32(hashStr(seed + "|" + key))
	f()
	return f()
}

/* ---- 选题 ---- */

var diffRank = map[string]int{"basic": 0, "standard": 1, "challenge": 2, "advanced": 2}

func rankOf(q *Question) int {
	if r, ok := diffRank[q.Difficulty]; ok {
		return r
	}
	return 1
}

// NeedConfirm 该知识点判「掌握」所需的连续答对题数：
// 取 confirm 与题库题数的较小值（至少 1）。
func (c *Catalog) NeedConfirm(s *Session, id string) int {
	n := len(c.Bank[id])
	conf := DefaultConfirm
	if s != nil && s.Confirm > 0 {
		conf = s.Confirm
	}
	need := conf
	if n > 0 && n < need {
		need = n
	}
	if need < 1 {
		need = 1
	}
	return need
}

// pickQuestion 给知识点 id 选第 attempt 题。语义与上游一致：
//   - 已在本次会话里出过的题不再出；
//   - 第一题优先 standard，没有退 basic，尽量不出拓展；
//   - 连续答对、尚未错过：优先 challenge 确认掌握，再 standard，再 basic；
//   - 错过之后的确认题：优先 basic，其次与第一题同难度，再其次更容易的；
//   - avoid 里的题在「没出过」意义上优先避开（难度优先于 avoid）；
//   - 档内用 seed 决定的伪随机数选一道。
func (c *Catalog) pickQuestion(s *Session, id string, attempt int) *Question {
	all := c.Bank[id]
	if len(all) == 0 {
		return nil
	}
	tried := map[string]bool{}
	for _, a := range s.Attempts[id] {
		tried[a.QID] = true
	}
	var pool []*Question
	for _, q := range all {
		if !tried[q.ID] {
			pool = append(pool, q)
		}
	}
	if len(pool) == 0 {
		return nil
	}
	avoid := map[string]bool{}
	for _, a := range s.Avoid {
		avoid[a] = true
	}
	freshFirst := func(list []*Question) []*Question {
		var f []*Question
		for _, q := range list {
			if !avoid[q.ID] {
				f = append(f, q)
			}
		}
		if len(f) > 0 {
			return f
		}
		return list
	}
	filterRank := func(list []*Question, r int) []*Question {
		var out []*Question
		for _, q := range list {
			if rankOf(q) == r {
				out = append(out, q)
			}
		}
		return out
	}
	pickRank := func(list []*Question, ranks []int) []*Question {
		for _, r := range ranks {
			if hit := filterRank(list, r); len(hit) > 0 {
				return freshFirst(hit)
			}
		}
		return freshFirst(list)
	}
	prev := s.Attempts[id]
	hadFail := false
	for _, a := range prev {
		if a.Result != ResultCorrect {
			hadFail = true
			break
		}
	}
	var cand []*Question
	if attempt <= 1 || !hadFail {
		base := freshFirst(pool)
		if attempt <= 1 {
			cand = pickRank(base, []int{1, 0})
		} else {
			cand = pickRank(base, []int{2, 1, 0})
		}
	} else {
		var first *Question
		if len(prev) > 0 {
			for _, q := range all {
				if q.ID == prev[0].QID {
					first = q
					break
				}
			}
		}
		r0 := 1
		if first != nil {
			r0 = rankOf(first)
		}
		tiers := [][]*Question{filterRank(pool, 0), filterRank(pool, r0)}
		var easier []*Question
		for _, q := range pool {
			if rankOf(q) < r0 {
				easier = append(easier, q)
			}
		}
		tiers = append(tiers, easier)
		var hit []*Question
		for _, t := range tiers {
			if len(t) > 0 {
				hit = t
				break
			}
		}
		if hit != nil {
			cand = freshFirst(hit)
		} else {
			lo := rankOf(pool[0])
			for _, q := range pool[1:] {
				if r := rankOf(q); r < lo {
					lo = r
				}
			}
			cand = freshFirst(filterRank(pool, lo))
		}
	}
	idx := int(rand01(s.Seed, fmt.Sprintf("%s|%d", id, attempt)) * float64(len(cand)))
	if idx >= len(cand) {
		idx = len(cand) - 1
	}
	if idx < 0 {
		idx = 0
	}
	return cand[idx]
}

// PickQuestion 暴露选题逻辑（测试与诊断辅助用）。
func (c *Catalog) PickQuestion(s *Session, id string, attempt int) *Question {
	return c.pickQuestion(s, id, attempt)
}

/* ---- 出题推进 ---- */

// NextQuestion 返回下一个要考的知识点；无题可出返回 ""。
func (c *Catalog) NextQuestion(s *Session) string {
	if len(s.Answers) >= s.Limit {
		return ""
	}
	for _, id := range s.Stack {
		if _, done := s.Status[id]; !done && c.IsTestable(id) {
			return id
		}
	}
	return ""
}

// NextItem 返回下一个要出的具体题目；无题返回 nil。
type NextItemResult struct {
	TopicID  string
	Question *Question
	Attempt  int
}

func (c *Catalog) NextItem(s *Session) *NextItemResult {
	id := c.NextQuestion(s)
	if id == "" {
		return nil
	}
	attempt := len(s.Attempts[id]) + 1
	q := c.pickQuestion(s, id, attempt)
	if q == nil {
		return nil
	}
	return &NextItemResult{TopicID: id, Question: q, Attempt: attempt}
}

// ApplyAnswer 记录一次作答，返回新会话（不修改入参）。
//   - 连续答对满 NeedConfirm 题：该点及其所有尚未判定的祖先记 mastered（祖先 inferred）；
//   - 中途 wrong：再出一道确认题（没有可出的就直接判薄弱）；
//   - 确认题 correct：shaky，不向先修回溯；
//   - 确认题 wrong：weak，并把其先修（hard 先、soft 后）压到出题栈顶。
//   - skip：不计对也不计错，不出确认题，不产生任何新结论；
//     只追加作答记录，保留该点已有状态（含已有结论，见下）。
//
// 对已有结论的点用 correct/wrong 重新作答 = 重新开始（清空该点的状态与尝试）。
// skip 例外：skip 只追加作答记录，绝不触发重答重置——已有 Status、Attempts
// 等一律保留（否则 weak/mastered 点重答一次 skip 就被清成 unknown）。
func (c *Catalog) ApplyAnswer(s *Session, id, result string) (*Session, error) {
	switch result {
	case ResultCorrect, ResultWrong, ResultSkip:
	default:
		return nil, errors.New("作答结果必须是 correct / wrong / skip")
	}
	if _, ok := c.ByID[id]; !ok {
		return nil, fmt.Errorf("未知知识点: %s", id)
	}
	ns := cloneSession(s)
	if _, done := ns.Status[id]; done && result != ResultSkip {
		// 重答重置只对 correct/wrong 生效；skip 不得清空已有结论与历史尝试。
		delete(ns.Status, id)
		ns.Attempts[id] = nil
	}
	prev := ns.Attempts[id]
	q := c.pickQuestion(ns, id, len(prev)+1)
	rec := AnswerRecord{ID: id, Result: result}
	if q != nil {
		rec.QID = q.ID
	}
	ns.Answers = append(ns.Answers, rec)
	attempts := append(append([]AttemptRecord(nil), prev...), AttemptRecord{QID: rec.QID, Result: result})
	ns.Attempts[id] = attempts

	fails, corrects := 0, 0
	for _, a := range attempts {
		switch a.Result {
		case ResultCorrect:
			corrects++
		case ResultWrong:
			fails++
		}
		// ResultSkip 不计对也不计错
	}
	need := c.NeedConfirm(ns, id)
	if result == ResultSkip {
		// skip：不出确认题、不下结论、不推先修；已有状态保持原样，
		// 只追加作答/尝试记录。
	} else if result == ResultCorrect {
		if fails == 0 && corrects >= need {
			ns.Status[id] = StatusEntry{S: StatusMastered, Src: "direct"}
			for a := range c.ancestorSet(id) {
				if _, ok := ns.Status[a]; !ok {
					ns.Status[a] = StatusEntry{S: StatusMastered, Src: "inferred"}
				}
			}
		} else if fails > 0 {
			ns.Status[id] = StatusEntry{S: StatusShaky, Src: "direct"}
		}
		// 否则：连续答对尚未满额，继续出题
	} else if fails == 1 && c.pickQuestion(ns, id, len(attempts)+1) != nil {
		// 第一次没过：等确认题再下结论
	} else {
		ns.Status[id] = StatusEntry{S: StatusWeak, Src: "direct"}
		var scopeSet map[string]bool
		if ns.Scope != nil {
			scopeSet = map[string]bool{}
			for _, x := range ns.Scope {
				scopeSet[x] = true
			}
		}
		// hard 先于 soft；同级按文件顺序（Prereqs 已按此序构建）
		var push []string
		pushed := map[string]bool{}
		for _, e := range c.Prereqs[id] {
			p := e.PrerequisiteID
			if pushed[p] {
				continue
			}
			if !c.IsTestable(p) {
				continue
			}
			if _, done := ns.Status[p]; done {
				continue
			}
			if scopeSet != nil && !scopeSet[p] {
				continue
			}
			pushed[p] = true
			push = append(push, p)
		}
		var rest []string
		for _, x := range ns.Stack {
			if !pushed[x] {
				rest = append(rest, x)
			}
		}
		ns.Stack = append(append([]string(nil), push...), rest...)
	}
	return ns, nil
}

// ApplyAnswerText 记录一次带用户输入文本的作答（text 只用于记录）。
func (c *Catalog) ApplyAnswerText(s *Session, id, result, text string) (*Session, error) {
	ns, err := c.ApplyAnswer(s, id, result)
	if err != nil {
		return nil, err
	}
	if text != "" {
		ns.Answers[len(ns.Answers)-1].Text = text
	}
	return ns, nil
}

// Replay 由作答记录重建会话（用于刷新恢复、撤销、更正）。
func (c *Catalog) Replay(targets []string, answers []AnswerRecord, opts *CreateSessionOptions) (*Session, error) {
	s, err := c.CreateSession(targets, opts)
	if err != nil {
		return nil, err
	}
	for _, a := range answers {
		s, err = c.ApplyAnswerText(s, a.ID, a.Result, a.Text)
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Undo 撤销最后 n 次作答并重放。n 为负数是调用方错误，明确报错
// （不静默当作 0 或全部撤销）。
func (c *Catalog) Undo(s *Session, n int, opts *CreateSessionOptions) (*Session, error) {
	if n < 0 {
		return nil, fmt.Errorf("撤销次数不能为负: %d", n)
	}
	if n > len(s.Answers) {
		n = len(s.Answers)
	}
	opts = withSessionOpts(s, opts)
	return c.Replay(s.Targets, s.Answers[:len(s.Answers)-n], opts)
}

// CorrectAnswer 更正历史记录中第 idx 次作答的结果并重放。
// 允许把 correct 改为 wrong/skip 或反之；重放后的会话与
// 「从没写错过」的会话完全一致（Replay 保证）。
func (c *Catalog) CorrectAnswer(s *Session, idx int, result, text string) (*Session, error) {
	if idx < 0 || idx >= len(s.Answers) {
		return nil, fmt.Errorf("作答下标越界: %d", idx)
	}
	answers := append([]AnswerRecord(nil), s.Answers...)
	answers[idx].Result = result
	answers[idx].Text = text
	return c.Replay(s.Targets, answers, withSessionOpts(s, nil))
}

// withSessionOpts 从既有会话恢复 CreateSession 参数（供 Undo/CorrectAnswer 重放）。
func withSessionOpts(s *Session, opts *CreateSessionOptions) *CreateSessionOptions {
	if opts == nil {
		opts = &CreateSessionOptions{}
	} else {
		cp := *opts
		opts = &cp
	}
	if opts.Mode == "" {
		opts.Mode = s.Mode
	}
	if opts.Limit == 0 {
		opts.Limit = s.Limit
	}
	if opts.Confirm == 0 {
		opts.Confirm = s.Confirm
	}
	if opts.Seed == "" {
		opts.Seed = s.Seed
	}
	if len(opts.Avoid) == 0 {
		opts.Avoid = s.Avoid
	}
	return opts
}

// StatusOf 状态：mastered / shaky / weak / unknown。
// 只答错一题就遇到题量上限的点按 weak 算（见 IsUnconfirmed）。
func (c *Catalog) StatusOf(s *Session, id string) string {
	if st, ok := s.Status[id]; ok {
		return st.S
	}
	if c.IsUnconfirmed(s, id) {
		return StatusWeak
	}
	return StatusUnknown
}

// IsUnconfirmed 答错（wrong）过、但没来得及出确认题（题量用完）的点。
// skip 不算失败：只 skip 过的点仍是 unknown。
func (c *Catalog) IsUnconfirmed(s *Session, id string) bool {
	if _, ok := s.Status[id]; ok {
		return false
	}
	a := s.Attempts[id]
	if len(a) == 0 {
		return false
	}
	for _, x := range a {
		if x.Result == ResultWrong {
			return true
		}
	}
	return false
}

/* ---- 汇总 ---- */

// RootCause 是根因知识点：weak 且其直接先修中没有 weak 的点。
// JSON 契约为 snake_case（与前端 types.ts / e2e SUMMARY_ALIASES 一致）。
type RootCause struct {
	ID                string   `json:"id"`
	PendingPrereqs    []string `json:"pending_prereqs"`      // 先修里还没测到的（在范围内）
	OutOfScopePrereqs []string `json:"out_of_scope_prereqs"` // 筛查模式下范围之外的先修
	ExternalPrereqs   []string `json:"external_prereqs"`     // 外部/不可测节点（无诊断题）
}

// PathItem 是学习路径上的一项。
type PathItem struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	IsRoot   bool   `json:"is_root"`
	IsTarget bool   `json:"is_target"`
}

// Summary 是诊断结果汇总。
type Summary struct {
	Mode         string      `json:"mode"`
	Targets      []string    `json:"targets"`
	Asked        int         `json:"asked"`
	Limit        int         `json:"limit"`
	Finished     bool        `json:"finished"`
	LimitReached bool        `json:"limit_reached"`
	Mastered     []string    `json:"mastered"`
	Shaky        []string    `json:"shaky"`
	Weak         []string    `json:"weak"`
	Unconfirmed  []string    `json:"unconfirmed"`
	Untested     []string    `json:"untested"`
	Roots        []RootCause `json:"roots"`
	Path         []PathItem  `json:"path"`
}

// Summarize 汇总诊断结果：根因、学习路径、掌握/不稳/薄弱/未测。
// 学习路径 =（根因 ∪ 不稳点）的所有后代（范围内）∪ 自身，去掉已掌握，
// 按年级从低到高、同年级保持拓扑序。
func (c *Catalog) Summarize(s *Session) *Summary {
	nodes := s.Order
	st := func(id string) string { return c.StatusOf(s, id) }
	filterBy := func(status string) []string {
		var out []string
		for _, id := range nodes {
			if st(id) == status {
				out = append(out, id)
			}
		}
		return out
	}
	mastered := filterBy(StatusMastered)
	shaky := filterBy(StatusShaky)
	weak := filterBy(StatusWeak)
	var unconfirmed []string
	for _, id := range weak {
		if c.IsUnconfirmed(s, id) {
			unconfirmed = append(unconfirmed, id)
		}
	}
	untested := filterBy(StatusUnknown)

	var scopeSet map[string]bool
	if s.Scope != nil {
		scopeSet = map[string]bool{}
		for _, x := range s.Scope {
			scopeSet[x] = true
		}
	}
	inScope := func(id string) bool { return scopeSet == nil || scopeSet[id] }

	roots := []RootCause{}
	for _, id := range weak {
		hasWeakPre := false
		for _, e := range c.Prereqs[id] {
			if st(e.PrerequisiteID) == StatusWeak {
				hasWeakPre = true
				break
			}
		}
		if hasWeakPre {
			continue
		}
		rc := RootCause{ID: id}
		for _, e := range c.Prereqs[id] {
			p := e.PrerequisiteID
			if st(p) != StatusUnknown {
				continue
			}
			if !c.IsTestable(p) {
				rc.ExternalPrereqs = append(rc.ExternalPrereqs, p)
				continue
			}
			if inScope(p) {
				rc.PendingPrereqs = append(rc.PendingPrereqs, p)
			} else {
				rc.OutOfScopePrereqs = append(rc.OutOfScopePrereqs, p)
			}
		}
		roots = append(roots, rc)
	}
	// JSON 契约：slice 一律非 null（nil → 空 []），前端不再做 null 兜底。
	for i := range roots {
		roots[i].PendingPrereqs = nonNilStrings(roots[i].PendingPrereqs)
		roots[i].OutOfScopePrereqs = nonNilStrings(roots[i].OutOfScopePrereqs)
		roots[i].ExternalPrereqs = nonNilStrings(roots[i].ExternalPrereqs)
	}

	// 学习路径：（根因 ∪ 不稳点）的所有后代（范围内）∪ 自身，去掉已掌握
	inSub := map[string]bool{}
	for _, id := range nodes {
		inSub[id] = true
	}
	reach := map[string]bool{}
	queue := []string{}
	rootSet := map[string]bool{}
	for _, r := range roots {
		rootSet[r.ID] = true
		queue = append(queue, r.ID)
		reach[r.ID] = true
	}
	for _, id := range shaky {
		if !reach[id] {
			reach[id] = true
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, e := range c.DepsOf[id] {
			d := e.TopicID
			if inSub[d] && !reach[d] {
				reach[d] = true
				queue = append(queue, d)
			}
		}
	}
	targetSet := map[string]bool{}
	if s.Scope == nil {
		for _, t := range s.Targets {
			targetSet[t] = true
		}
	}
	topoPos := make(map[string]int, len(nodes))
	for i, id := range nodes {
		topoPos[id] = i
	}
	path := []PathItem{}
	for _, id := range nodes {
		if !reach[id] || st(id) == StatusMastered {
			continue
		}
		path = append(path, PathItem{
			ID:       id,
			Status:   st(id),
			IsRoot:   rootSet[id],
			IsTarget: targetSet[id],
		})
	}
	// 年级升序，同年级保持拓扑序（Go sort.SliceStable 保持相等元素的相对顺序）
	sort.SliceStable(path, func(i, j int) bool {
		return c.gradeRank(path[i].ID) < c.gradeRank(path[j].ID)
	})

	var pending []string
	for _, id := range s.Stack {
		if _, done := s.Status[id]; !done && c.IsTestable(id) {
			pending = append(pending, id)
		}
	}
	next := c.NextQuestion(s)
	return &Summary{
		Mode:         s.Mode,
		Targets:      nonNilStrings(append([]string(nil), s.Targets...)),
		Asked:        len(s.Answers),
		Limit:        s.Limit,
		Finished:     next == "",
		LimitReached: len(s.Answers) >= s.Limit && len(pending) > 0,
		Mastered:     nonNilStrings(mastered),
		Shaky:        nonNilStrings(shaky),
		Weak:         nonNilStrings(weak),
		Unconfirmed:  nonNilStrings(unconfirmed),
		Untested:     nonNilStrings(untested),
		Roots:        roots,
		Path:         path,
	}
}

// nonNilStrings 把 nil slice 规整为空 slice（JSON 序列化为 [] 而不是 null）。
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// ChapterSummaryRow 是按章汇总的一行。
type ChapterSummaryRow struct {
	Key         string   `json:"key"`
	Volume      string   `json:"volume"`
	ChapterNo   string   `json:"chapterNo"`
	ChapterName string   `json:"chapterName"`
	Label       string   `json:"label"`
	Total       int      `json:"total"`
	Mastered    int      `json:"mastered"`
	Shaky       int      `json:"shaky"`
	Weak        int      `json:"weak"`
	Untested    int      `json:"untested"`
	WeakIDs     []string `json:"weakIds"`
	ShakyIDs    []string `json:"shakyIds"`
	UntestedIDs []string `json:"untestedIds"`
}

// ChapterSummary 按章汇总：范围内每章的掌握/不稳/薄弱/未测数量与 id 列表。
// 章按教材顺序给出，薄弱的章排前面（weak 降序、shaky 降序、教材顺序）。
func (c *Catalog) ChapterSummary(s *Session) []ChapterSummaryRow {
	inOrder := map[string]bool{}
	for _, id := range s.Order {
		inOrder[id] = true
	}
	byKey := map[string]*ChapterSummaryRow{}
	var list []*ChapterSummaryRow
	for _, t := range c.Topics {
		if t.External || !inOrder[t.ID] {
			continue
		}
		ch := ParseChapter(t.Chapter)
		key := ch.Volume + " " + ch.ChapterNo + " " + ch.ChapterName
		row := byKey[key]
		if row == nil {
			row = &ChapterSummaryRow{
				Key:         key,
				Volume:      ch.Volume,
				ChapterNo:   ch.ChapterNo,
				ChapterName: ch.ChapterName,
				WeakIDs:     []string{},
				ShakyIDs:    []string{},
				UntestedIDs: []string{},
			}
			if ch.Volume != "" {
				row.Label = ch.Volume + " " + ch.ChapterNo + " " + ch.ChapterName
			} else {
				row.Label = ch.ChapterNo + " " + ch.ChapterName
			}
			byKey[key] = row
			list = append(list, row)
		}
		st := c.StatusOf(s, t.ID)
		row.Total++
		switch st {
		case StatusMastered:
			row.Mastered++
		case StatusShaky:
			row.Shaky++
			row.ShakyIDs = append(row.ShakyIDs, t.ID)
		case StatusWeak:
			row.Weak++
			row.WeakIDs = append(row.WeakIDs, t.ID)
		default:
			row.Untested++
			row.UntestedIDs = append(row.UntestedIDs, t.ID)
		}
	}
	out := make([]ChapterSummaryRow, len(list))
	for i, r := range list {
		out[i] = *r
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Weak != out[j].Weak {
			return out[i].Weak > out[j].Weak
		}
		if out[i].Shaky != out[j].Shaky {
			return out[i].Shaky > out[j].Shaky
		}
		return false
	})
	return out
}
