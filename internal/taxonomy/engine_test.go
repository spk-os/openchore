package taxonomy

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

/* 移植上游 tests/test_engine.js 的手造小图 / 手造题库用例。 */

func TestSmallGraphAncestorsAndTopo(t *testing.T) {
	c := smallCatalog()

	cases := map[string][]string{
		"T": {"B", "C", "D", "E", "F", "X"},
		"E": {"F", "X"},
		"X": {},
	}
	for id, want := range cases {
		got := sortedCopy(c.Ancestors([]string{id}))
		if !eqStrings(got, want) {
			t.Errorf("Ancestors(%s) = %v，期望 %v", id, got, want)
		}
	}
	sub, err := c.Subgraph([]string{"T"})
	if err != nil {
		t.Fatal(err)
	}
	assertStrings(t, "Subgraph(T)", sortedCopy(sub.Nodes), []string{"B", "C", "D", "E", "F", "T", "X"})
	if sub.Nodes[0] != "X" || sub.Nodes[len(sub.Nodes)-1] != "T" {
		t.Errorf("Subgraph(T) 首尾应为 X..T，实际 %v", sub.Nodes)
	}
	assertTopo(t, c, sub.Nodes)

	multi, err := c.Subgraph([]string{"C", "D"})
	if err != nil {
		t.Fatal(err)
	}
	assertStrings(t, "Subgraph(C,D)", sortedCopy(multi.Nodes), []string{"C", "D", "E", "F", "X"})
	assertTopo(t, c, multi.Nodes)
}

func TestSmallGraphPrereqStrengthOrder(t *testing.T) {
	c := smallCatalog()
	var got []string
	for _, e := range c.Prereqs["T"] {
		got = append(got, e.PrerequisiteID+":"+e.Strength)
	}
	// 注意：上游 engine.js 排序键为 strength*1e6+index[id]（index 以 id 为键），
	// 小图里 index["C"] 等为 undefined → NaN，V8 视为相等而保持插入序，因此上游得
	// [C,D,B]。Go 的 Load 未排序，这里如实断言文件顺序（hard 内部保持文件序）。
	assertStrings(t, "Prereqs[T]", got, []string{"D:hard", "C:hard", "B:soft"})
	if !c.IsTestable("T") || c.IsTestable("X") {
		t.Errorf("IsTestable 应为：T 可测、X（外部）不可测")
	}
	if c.Topic("F").External {
		t.Errorf("F 不应是外部节点")
	}
}

func TestSmallGraphAnswerCorrectMastersAncestors(t *testing.T) {
	c := smallCatalog()
	s := mustSession(t, c, []string{"T"}, nil)
	if q := c.NextQuestion(s); q != "T" {
		t.Fatalf("第一题应问目标 T，实际 %s", q)
	}
	s, err := c.ApplyAnswer(s, "T", ResultCorrect)
	if err != nil {
		t.Fatal(err)
	}
	if q := c.NextQuestion(s); q != "" {
		t.Errorf("答对后不应再出题，实际 %s", q)
	}
	for _, id := range []string{"B", "C", "D", "E", "F", "X", "T"} {
		assertStatus(t, id, c.StatusOf(s, id), StatusMastered)
	}
	if s.Status["T"].Src != "direct" || s.Status["F"].Src != "inferred" {
		t.Errorf("T 应为 direct、F 应为 inferred，实际 %v / %v", s.Status["T"], s.Status["F"])
	}
	assertStatus(t, "Z", c.StatusOf(s, "Z"), StatusUnknown)
	sum := c.Summarize(s)
	assertStrings(t, "weak", sum.Weak, nil)
	assertStrings(t, "roots", idsOfRoots(sum), nil)
	assertStrings(t, "path", idsOfPath(sum), nil)
	if sum.Asked != 1 {
		t.Errorf("asked = %d，期望 1", sum.Asked)
	}
}

func TestSmallGraphWrongBacktracksHardFirstDepthFirst(t *testing.T) {
	c := smallCatalog()
	s := mustSession(t, c, []string{"T"}, nil)
	script := map[string]string{"T": ResultWrong, "D": ResultWrong, "F": ResultCorrect, "C": ResultWrong, "E": ResultCorrect, "B": ResultCorrect}
	s, asked := runScript(t, c, s, script)
	// hard 先于 soft，同级按文件顺序（D 在 C 前）：T→D→F→C→E→B
	assertStrings(t, "出题顺序", asked, []string{"T", "D", "F", "C", "E", "B"})
	if q := c.NextQuestion(s); q != "" {
		t.Errorf("结束后不应再出题，实际 %s", q)
	}

	// 逐步检查 hard 先修先问
	s = mustSession(t, c, []string{"T"}, nil)
	s, _ = c.ApplyAnswer(s, "T", ResultWrong)
	if q := c.NextQuestion(s); q != "D" {
		t.Errorf("T 答错后应先问第一个 hard 先修 D，实际 %s", q)
	}
	s, _ = c.ApplyAnswer(s, "D", ResultCorrect)
	if q := c.NextQuestion(s); q != "C" {
		t.Errorf("D 答对后应问下一个 hard 先修 C，实际 %s", q)
	}
	s, _ = c.ApplyAnswer(s, "C", ResultCorrect)
	if q := c.NextQuestion(s); q != "B" {
		t.Errorf("hard 先修问完后应问 soft 先修 B，实际 %s", q)
	}
}

// V2：skip 与 wrong 不再等价。skip 状态中性（不判 weak/shaky/mastered、不推先修），
// wrong 判 weak 并回溯先修；两者作答后会话状态必须不同。
func TestSmallGraphSkipNotEqualsWrong(t *testing.T) {
	c := smallCatalog()
	sSkip, _ := c.ApplyAnswer(mustSession(t, c, []string{"T"}, nil), "T", ResultSkip)
	sWrong, _ := c.ApplyAnswer(mustSession(t, c, []string{"T"}, nil), "T", ResultWrong)

	if _, ok := sSkip.Status["T"]; ok {
		t.Errorf("skip 后 T 不应有结论，实际 %v", sSkip.Status["T"])
	}
	if _, ok := sWrong.Status["T"]; !ok {
		t.Errorf("wrong 后 T 应有结论")
	}
	ja, jb := mustJSON(t, sSkip.Status), mustJSON(t, sWrong.Status)
	if ja == jb {
		t.Errorf("skip 与 wrong 的会话状态不应相同：%s", ja)
	}

	// skip 只记录作答：Answers/Attempts 保留，且不产生 weak/shaky/mastered。
	// 小图无文件级题库，回退的诊断题 ID 为 <id>_q1，故 QID 与 wrong 一致。
	if n := len(sSkip.Answers); n != 1 || sSkip.Answers[0].ID != "T" || sSkip.Answers[0].Result != ResultSkip || sSkip.Answers[0].QID != "T_q1" {
		t.Errorf("skip 后 Answers 应保留 1 条记录（QID=T_q1），实际 %v", sSkip.Answers)
	}
	if at := sSkip.Attempts["T"]; len(at) != 1 || at[0].Result != ResultSkip || at[0].QID != "T_q1" {
		t.Errorf("skip 后 Attempts[T] 应保留 1 条记录（QID=T_q1），实际 %v", at)
	}
	assertStatus(t, "StatusOf(T)", c.StatusOf(sSkip, "T"), StatusUnknown)
	if c.IsUnconfirmed(sSkip, "T") {
		t.Errorf("skip 后 IsUnconfirmed 应为 false")
	}
	sum := c.Summarize(sSkip)
	assertStrings(t, "skip weak", sum.Weak, nil)
	assertStrings(t, "skip shaky", sum.Shaky, nil)
	assertStrings(t, "skip mastered", sum.Mastered, nil)
	assertStrings(t, "skip roots", idsOfRoots(sum), nil)
	assertStrings(t, "skip path", idsOfPath(sum), nil)
	// 目标仍待测 → 继续问 T，不出确认结论。
	if q := c.NextQuestion(sSkip); q != "T" {
		t.Errorf("skip 后应继续问目标 T，实际 %q", q)
	}
	// 小图无题库，NextItem 无具体题可出；但不变量是：skip 后不存在任何新结论，
	// 也没有因 skip 被推到栈上的先修。
	if it := c.NextItem(sSkip); it != nil {
		t.Errorf("小图无题库时 NextItem 应为 nil（诊断题只在 NextQuestion 层出），实际 %+v", it)
	}
	if len(sSkip.Stack) != 1 || sSkip.Stack[0] != "T" {
		t.Errorf("skip 后 Stack 应保持 [T]，实际 %v", sSkip.Stack)
	}
}

// skip 不回溯先修：T 答错后再 skip，不会把先修压到出题栈，也不判 weak。
// 小图无题库（第一题 wrong 即判 weak 并压栈），故 wrong+skip 场景用带题库的 bankGraph。
func TestSmallGraphSkipNoBacktrackAfterWrong(t *testing.T) {
	c := smallCatalog()
	s, err := c.ApplyAnswer(mustSession(t, c, []string{"T"}, nil), "T", ResultWrong)
	if err != nil {
		t.Fatal(err)
	}
	// 既有 wrong 语义（不变）：无题可出确认题 → 直接 weak + hard 先修压栈。
	if q := c.NextQuestion(s); q != "D" {
		t.Fatalf("wrong 后应回溯到 hard 先修 D，实际 %s", q)
	}
	if len(s.Stack) == 0 || s.Stack[0] != "D" {
		t.Fatalf("wrong 后 Stack 顶应为 D，实际 %v", s.Stack)
	}

	c = bankGraph(t)
	s = mustSession(t, c, []string{"T"}, &CreateSessionOptions{Confirm: 1})
	s, err = c.ApplyAnswer(s, "T", ResultWrong)
	if err != nil {
		t.Fatal(err)
	}
	if q := c.NextQuestion(s); q != "T" {
		t.Fatalf("wrong 后应出确认题 T，实际 %s", q)
	}
	if len(s.Stack) != 1 || s.Stack[0] != "T" {
		t.Fatalf("wrong 后（未到确认结果前）Stack 应只有 T，实际 %v", s.Stack)
	}
	s, err = c.ApplyAnswer(s, "T", ResultSkip)
	if err != nil {
		t.Fatal(err)
	}
	if q := c.NextQuestion(s); q != "T" {
		t.Errorf("wrong+skip 后不应跳去先修，应继续 T，实际 %q", q)
	}
	if len(s.Stack) != 1 || s.Stack[0] != "T" {
		t.Errorf("wrong+skip 后 Stack 不应被压入先修，实际 %v", s.Stack)
	}
	// 冻结语义：skip 本身不新增 fail、不直接形成结论/回溯，但不会撤销此前 wrong 留下的待确认状态。
	// 因此 wrong+skip 后：仍无结论、IsUnconfirmed 仍为 true（来自历史 wrong）、StatusOf=weak（待确认）、
	// Stack 不变（skip 不推先修）。
	if st, ok := s.Status["T"]; ok {
		t.Errorf("wrong+skip 不应直接形成结论，实际 %v", st)
	}
	if !c.IsUnconfirmed(s, "T") {
		t.Errorf("历史 wrong 的待确认状态应保留，IsUnconfirmed 应为 true")
	}
	if at := s.Attempts["T"]; len(at) != 2 || at[0].Result != ResultWrong || at[1].Result != ResultSkip {
		t.Errorf("wrong+skip 的 Attempts 应保留 2 条，实际 %v", at)
	}
	assertStatus(t, "StatusOf(T)", c.StatusOf(s, "T"), StatusWeak)
	// skip 不推先修（Stack 已有断言），先修不应被实际出题。
	for _, p := range []string{"P1", "P2", "R"} {
		if len(s.Attempts[p]) != 0 {
			t.Errorf("%s 不应被出题，实际 %v", p, s.Attempts[p])
		}
	}
}

// 全 skip 的会话：所有点保持 unknown；skip 不推先修，出题栈始终只剩目标 T。
// 注意：hadFail 选题策略会把非 correct 视为失败用于变体轮换，因此 T 的题会被逐轮放弃，
// 直到 pickQuestion 返回 nil 才结束（这是选题策略，不是 mastery 判定）。
func TestSmallGraphAllSkipStaysUnknown(t *testing.T) {
	c := smallCatalog()
	s := mustSession(t, c, []string{"T"}, nil)
	for guard := 0; guard < 100; guard++ {
		id := c.NextQuestion(s)
		if id == "" {
			break
		}
		if id != "T" {
			t.Fatalf("全 skip 后不应出目标以外（先修）的题，实际 %s", id)
		}
		ns, err := c.ApplyAnswer(s, id, ResultSkip)
		if err != nil {
			t.Fatal(err)
		}
		s = ns
	}
	if n := len(s.Answers); n == 0 || n >= 100 {
		t.Fatalf("全 skip 应产生有限条作答，实际 %d", n)
	}
	for _, a := range s.Answers {
		if a.ID != "T" || a.Result != ResultSkip {
			t.Errorf("全 skip 作答记录应只有 T/skip，实际 %v", a)
		}
	}
	for _, id := range []string{"T", "D", "F", "C", "E", "B", "X"} {
		assertStatus(t, id, c.StatusOf(s, id), StatusUnknown)
	}
	if len(s.Status) != 0 {
		t.Errorf("全 skip 后不应有任何结论，实际 %v", s.Status)
	}
	if len(s.Stack) != 1 || s.Stack[0] != "T" {
		t.Errorf("全 skip 后 Stack 应只有 T（未推先修），实际 %v", s.Stack)
	}
	sum := c.Summarize(s)
	assertStrings(t, "weak", sum.Weak, nil)
	assertStrings(t, "shaky", sum.Shaky, nil)
	assertStrings(t, "mastered", sum.Mastered, nil)
	assertStrings(t, "roots", idsOfRoots(sum), nil)
	assertStrings(t, "path", idsOfPath(sum), nil)
	// 汇总一致性：skip 只追加 Answers，不应让 Asked 与作答数脱节。
	if sum.Asked != len(s.Answers) {
		t.Errorf("Asked = %d，期望等于 Answers 条数 %d", sum.Asked, len(s.Answers))
	}
	if !sum.Finished {
		t.Errorf("无题可出后 Finished 应为 true")
	}
	// 全 skip 会一直作答直到无题可出；若作答数已等于上限则 LimitReached 必须为 true
	// （与 Asked 一致），否则必须为 false。
	if want := sum.Asked >= sum.Limit; sum.LimitReached != want {
		t.Errorf("LimitReached = %v，期望 %v（asked=%d limit=%d）", sum.LimitReached, want, sum.Asked, sum.Limit)
	}
	// 未测点：全部范围节点，没有任何点被测出结论。
	want := []string{"B", "C", "D", "E", "F", "T", "X"}
	assertStrings(t, "untested", sortedCopy(sum.Untested), want)
	if q := c.NextQuestion(s); q != "" {
		t.Errorf("结束后不应再出题，实际 %s", q)
	}
}

// DEF-ENGINE-SKIP-RESET：已有结论的点重答 skip 不得清空 Status/Attempts，skip 只追加一条。
// 冻结语义：skip 状态中性 —— 保留历史结论（及由它派生的 Weak/Roots/Unconfirmed），不 unknown 化。
func TestSmallGraphSkipKeepsConcludedStatus(t *testing.T) {
	c := smallCatalog()
	// weak 点：T wrong（无题库 → 直接 weak 并压栈 D）；D wrong → D weak。
	s, err := c.ApplyAnswer(mustSession(t, c, []string{"T"}, nil), "T", ResultWrong)
	if err != nil {
		t.Fatal(err)
	}
	s, err = c.ApplyAnswer(s, "D", ResultWrong)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, "前置 StatusOf(T)", c.StatusOf(s, "T"), StatusWeak)
	assertStatus(t, "前置 StatusOf(D)", c.StatusOf(s, "D"), StatusWeak)
	before := mustJSON(t, s.Status)
	sumBefore := c.Summarize(s)
	rootsBefore := idsOfRoots(sumBefore)
	attemptsBefore := len(s.Attempts["T"])
	answersBefore := len(s.Answers)

	s, err = c.ApplyAnswer(s, "T", ResultSkip)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustJSON(t, s.Status); got != before {
		t.Errorf("weak 点重答 skip 后 Status 应保持 %s，实际 %s", before, got)
	}
	assertStatus(t, "skip 后 StatusOf(T)", c.StatusOf(s, "T"), StatusWeak)
	if len(s.Attempts["T"]) != attemptsBefore+1 {
		t.Errorf("Attempts[T] 应只追加 1 条（%d → %d），实际 %v", attemptsBefore, len(s.Attempts["T"]), s.Attempts["T"])
	}
	if n := len(s.Answers); n != answersBefore+1 || s.Answers[n-1].Result != ResultSkip || s.Answers[n-1].ID != "T" {
		t.Errorf("Answers 应只追加 1 条 T/skip，实际 %v", s.Answers)
	}
	sum := c.Summarize(s)
	assertStrings(t, "weak", sum.Weak, sumBefore.Weak)
	assertStrings(t, "roots", idsOfRoots(sum), rootsBefore)
	if sum.Asked != len(s.Answers) {
		t.Errorf("Asked = %d，期望等于 Answers 条数 %d", sum.Asked, len(s.Answers))
	}

	// mastered 点：Z 答对即 mastered；重答 skip 后仍 mastered，历史 Attempts 保留且只追加一条。
	m, err := c.ApplyAnswer(mustSession(t, c, []string{"Z"}, nil), "Z", ResultCorrect)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, "前置 StatusOf(Z)", c.StatusOf(m, "Z"), StatusMastered)
	mSumBefore := c.Summarize(m)
	m, err = c.ApplyAnswer(m, "Z", ResultSkip)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, "skip 后 StatusOf(Z)", c.StatusOf(m, "Z"), StatusMastered)
	if m.Status["Z"].Src != "direct" {
		t.Errorf("Z 应保持 direct 结论，实际 %v", m.Status["Z"])
	}
	if c.IsUnconfirmed(m, "Z") {
		t.Errorf("mastered 点重答 skip 后不应变成待确认")
	}
	at := m.Attempts["Z"]
	if len(at) != 2 || at[0].Result != ResultCorrect || at[1].Result != ResultSkip {
		t.Errorf("Z 的 Attempts 应保留 correct 并追加 skip，实际 %v", at)
	}
	if len(m.Answers) != 2 || m.Answers[0].Result != ResultCorrect || m.Answers[1].Result != ResultSkip {
		t.Errorf("Z 的 Answers 应保留 correct 并追加 skip，实际 %v", m.Answers)
	}
	mSum := c.Summarize(m)
	assertStrings(t, "mastered", mSum.Mastered, mSumBefore.Mastered)
	assertStrings(t, "weak", mSum.Weak, mSumBefore.Weak)
	assertStrings(t, "untested", sortedCopy(mSum.Untested), sortedCopy(mSumBefore.Untested))
}

func TestSmallGraphSharedPrereqAskedOnce(t *testing.T) {
	c := smallCatalog()
	script := map[string]string{"T": ResultWrong, "D": ResultWrong, "F": ResultWrong, "X": ResultCorrect, "C": ResultWrong, "E": ResultWrong, "B": ResultWrong}
	_, asked := runScript(t, c, mustSession(t, c, []string{"T"}, nil), script)
	assertStrings(t, "出题顺序", asked, []string{"T", "D", "F", "C", "E", "B"})
	seen := map[string]bool{}
	for _, id := range asked {
		if seen[id] {
			t.Errorf("知识点 %s 被重复提问", id)
		}
		seen[id] = true
	}
}

func TestSmallGraphLimitAndPendingPrereqs(t *testing.T) {
	c := smallCatalog()
	s := mustSession(t, c, []string{"T"}, &CreateSessionOptions{Limit: 2})
	s, asked := runScript(t, c, s, map[string]string{"T": ResultWrong, "D": ResultWrong})
	assertStrings(t, "出题顺序", asked, []string{"T", "D"})
	if q := c.NextQuestion(s); q != "" {
		t.Errorf("达到上限后不应再出题，实际 %s", q)
	}
	sum := c.Summarize(s)
	if sum.Asked != 2 || sum.Limit != 2 {
		t.Errorf("asked/limit = %d/%d，期望 2/2", sum.Asked, sum.Limit)
	}
	if !sum.LimitReached {
		t.Errorf("limitReached 应为 true")
	}
	if !sum.Finished {
		t.Errorf("出题栈只剩不可测的 X 时 finished 应为 true（与上游 nextQuestion 语义一致）")
	}
	assertStrings(t, "untested", sortedCopy(sum.Untested), []string{"B", "C", "E", "F", "X"})
	assertStrings(t, "weak", sortedCopy(sum.Weak), []string{"D", "T"})
	assertStrings(t, "roots", idsOfRoots(sum), []string{"D"})
	assertStrings(t, "roots[0].pendingPrereqs", sum.Roots[0].PendingPrereqs, []string{"F"})
}

func TestSmallGraphDefaultLimit(t *testing.T) {
	c := smallCatalog()
	if DefaultLimit != 16 {
		t.Errorf("DefaultLimit = %d，期望 16", DefaultLimit)
	}
	if s := mustSession(t, c, []string{"T"}, nil); s.Limit != 16 {
		t.Errorf("默认 limit = %d，期望 16", s.Limit)
	}
	_, asked := runScript(t, c, mustSession(t, c, []string{"T"}, &CreateSessionOptions{Limit: 1}),
		map[string]string{"T": ResultWrong})
	assertStrings(t, "limit=1 出题", asked, []string{"T"})
}

func TestSmallGraphRootAndPath(t *testing.T) {
	c := smallCatalog()
	s, _ := runScript(t, c, mustSession(t, c, []string{"T"}, nil),
		map[string]string{"T": ResultWrong, "C": ResultWrong, "E": ResultCorrect, "D": ResultCorrect, "B": ResultCorrect})
	sum := c.Summarize(s)
	assertStrings(t, "weak", sortedCopy(sum.Weak), []string{"C", "T"})
	assertStrings(t, "roots", idsOfRoots(sum), []string{"C"})
	assertStrings(t, "path", idsOfPath(sum), []string{"C", "T"})
	if !sum.Path[0].IsRoot {
		t.Errorf("path[0]（C）应标记为根因")
	}
	if !sum.Path[1].IsTarget {
		t.Errorf("path[1]（T）应标记为目标")
	}
	assertStrings(t, "mastered", sortedCopy(sum.Mastered), []string{"B", "D", "E", "F", "X"})
	if !sum.Finished || sum.LimitReached {
		t.Errorf("finished=%v limitReached=%v，期望 true/false", sum.Finished, sum.LimitReached)
	}
}

func TestSmallGraphMultipleRootsTopologicalPath(t *testing.T) {
	c := smallCatalog()
	s, _ := runScript(t, c, mustSession(t, c, []string{"T"}, nil),
		map[string]string{"T": ResultWrong, "C": ResultWrong, "E": ResultWrong, "F": ResultCorrect, "D": ResultWrong, "B": ResultCorrect})
	sum := c.Summarize(s)
	assertStrings(t, "roots", sortedCopy(idsOfRoots(sum)), []string{"D", "E"})
	assertStrings(t, "path", sortedCopy(idsOfPath(sum)), []string{"C", "D", "E", "T"})
	assertTopo(t, c, idsOfPath(sum))
	for _, p := range sum.Path {
		if p.Status == StatusMastered {
			t.Errorf("path 不应包含已掌握点 %s", p.ID)
		}
	}
	var roots []string
	for _, p := range sum.Path {
		if p.IsRoot {
			roots = append(roots, p.ID)
		}
	}
	assertStrings(t, "path 内根因", sortedCopy(roots), []string{"D", "E"})
}

func TestSmallGraphExternalPrereqMarked(t *testing.T) {
	c := smallCatalog()
	s, asked := runScript(t, c, mustSession(t, c, []string{"T"}, nil),
		map[string]string{"T": ResultWrong, "C": ResultWrong, "E": ResultWrong, "F": ResultWrong, "D": ResultWrong, "B": ResultWrong})
	for _, id := range asked {
		if id == "X" {
			t.Fatalf("外部节点 X 不应出题")
		}
	}
	sum := c.Summarize(s)
	assertStrings(t, "roots", idsOfRoots(sum), []string{"F"})
	if len(sum.Roots) != 1 {
		t.Fatalf("roots 数量 = %d，期望 1", len(sum.Roots))
	}
	assertStrings(t, "externalPrereqs", sum.Roots[0].ExternalPrereqs, []string{"X"})
	assertStrings(t, "pendingPrereqs", sum.Roots[0].PendingPrereqs, nil)
	assertStrings(t, "untested", sum.Untested, []string{"X"})
}

func TestSmallGraphMultiTarget(t *testing.T) {
	c := smallCatalog()
	s := mustSession(t, c, []string{"C", "T", "D"}, nil)
	if q := c.NextQuestion(s); q != "T" {
		t.Errorf("多目标时离目标最近的 T 应先问，实际 %s", q)
	}
	s, _ = c.ApplyAnswer(s, "T", ResultCorrect)
	if q := c.NextQuestion(s); q != "" {
		t.Errorf("T 答对后其余祖先目标已被覆盖，实际 %s", q)
	}
	assertStrings(t, "path", idsOfPath(c.Summarize(s)), nil)

	_, asked := runScript(t, c, mustSession(t, c, []string{"T", "Z"}, nil),
		map[string]string{"T": ResultCorrect, "Z": ResultCorrect})
	assertStrings(t, "无关目标都应被问", sortedCopy(asked), []string{"T", "Z"})
}

func TestSmallGraphDirectVerdictNotOverwritten(t *testing.T) {
	c := smallCatalog()
	s, _ := c.ApplyAnswer(mustSession(t, c, []string{"T"}, nil), "E", ResultWrong)
	s, err := c.ApplyAnswer(s, "T", ResultCorrect)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, "E", c.StatusOf(s, "E"), StatusWeak)
	assertStatus(t, "C", c.StatusOf(s, "C"), StatusMastered)
}

func TestSmallGraphApplyAnswerPureReplayAndErrors(t *testing.T) {
	c := smallCatalog()
	s0 := mustSession(t, c, []string{"T"}, nil)
	s1, err := c.ApplyAnswer(s0, "T", ResultWrong)
	if err != nil {
		t.Fatal(err)
	}
	if len(s0.Answers) != 0 || len(s0.Attempts["T"]) != 0 || len(s0.Status) != 0 {
		t.Errorf("ApplyAnswer 不应修改入参：answers=%v attempts=%v status=%v", s0.Answers, s0.Attempts["T"], s0.Status)
	}
	s2, err := c.ApplyAnswer(s1, "C", ResultCorrect)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Replay([]string{"T"}, []AnswerRecord{{ID: "T", Result: ResultWrong}, {ID: "C", Result: ResultCorrect}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	deepEqualJSON(t, "Replay 与逐步作答", r, s2)

	// 可 JSON 往返
	raw, err := json.Marshal(s2)
	if err != nil {
		t.Fatal(err)
	}
	var back Session
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	deepEqualJSON(t, "会话 JSON 往返", &back, s2)

	if _, err := c.ApplyAnswer(s0, "T", "maybe"); err == nil {
		t.Errorf("非法作答结果应报错")
	}
	if _, err := c.CreateSession([]string{"nope"}, nil); err == nil {
		t.Errorf("未知目标应报错")
	}
	if _, err := c.CreateSession(nil, nil); err == nil {
		t.Errorf("空目标应报错")
	}
}

/* ---- 手造题库（每题难度可控） ---- */

var diffCode = map[string]string{"b": "basic", "s": "standard", "c": "challenge", "a": "advanced"}

func mq(topicID string, n int, diff string) *Question {
	return &Question{
		ID: topicID + "_q" + itoa(n), TopicID: topicID, Difficulty: diffCode[diff],
		Type: "numeric", Prompt: topicID + " 第 " + itoa(n) + " 题", Answer: "1", Explanation: "解析",
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// mustJSON 把任意值序列化为 JSON 字符串（测试内比较用）。
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	return string(b)
}

// bankGraph 每个知识点 4 题：q1 standard、q2 standard、q3 basic、q4 advanced，
// 没有 diagnostic_question（纯题库）。
func bankGraph(t *testing.T) *Catalog {
	t.Helper()
	ids := []string{"R", "P1", "P2", "T"}
	var topics []*Topic
	for _, id := range ids {
		topics = append(topics, &Topic{ID: id, Name: id, Grade: 1, Semester: "上"})
	}
	edges := []*Edge{
		{TopicID: "P1", PrerequisiteID: "R", Strength: "hard"},
		{TopicID: "T", PrerequisiteID: "P1", Strength: "hard"},
		{TopicID: "T", PrerequisiteID: "P2", Strength: "hard"},
	}
	var qs []*Question
	for _, id := range ids {
		qs = append(qs, mq(id, 1, "s"), mq(id, 2, "s"), mq(id, 3, "b"), mq(id, 4, "a"))
	}
	return testSpec{topics: topics, edges: edges, questions: qs}.mustCatalog(t)
}

// singleGraph(qs...) 构造只有 M 一个知识点、题集为 qs 的图。
func singleGraph(t *testing.T, qs ...*Question) *Catalog {
	t.Helper()
	return testSpec{topics: []*Topic{{ID: "M", Name: "M", Grade: 1}}, questions: qs}.mustCatalog(t)
}

// runBank 按 script[topicId][attempt-1] 作答，返回 [会话, "T#1" 形式序列]。
func runBank(t *testing.T, c *Catalog, s *Session, script map[string][]string) (*Session, []string) {
	t.Helper()
	seq := []string{}
	for guard := 0; guard < 100; guard++ {
		it := c.NextItem(s)
		if it == nil {
			return s, seq
		}
		seq = append(seq, it.TopicID+"#"+itoa(it.Attempt))
		ans := script[it.TopicID]
		if it.Attempt-1 >= len(ans) {
			t.Fatalf("脚本缺少 %s 第 %d 题（序列 %v）", it.TopicID, it.Attempt, seq)
		}
		ns, err := c.ApplyAnswer(s, it.TopicID, ans[it.Attempt-1])
		if err != nil {
			t.Fatalf("ApplyAnswer: %v", err)
		}
		s = ns
	}
	t.Fatalf("出题循环超限，序列 %v", seq)
	return nil, nil
}

func TestBankGraphFlags(t *testing.T) {
	g := smallCatalog()
	g2 := bankGraph(t)
	// 冻结语义：测试装配器不设 HasBank（只有生产 Load 会设），此处按题库实体断言；
	// 真实 Load 后 HasBank=true 由 TestBankQuestionInvariants 断言。
	if len(g2.Questions) == 0 || len(g.Questions) != 0 {
		t.Errorf("题库图应有题目、回退图不应有")
	}
	if len(g2.Bank["T"]) != 4 {
		t.Errorf("题库图 T 应有 4 题，实际 %d", len(g2.Bank["T"]))
	}
	if g.Bank["T"][0].Type != legacyQuestionType {
		t.Errorf("回退图应有 legacy 合成题，实际 %s", g.Bank["T"][0].Type)
	}
}

func TestBankConfirmOne(t *testing.T) {
	c := bankGraph(t)
	s, seq := runBank(t, c, mustSession(t, c, []string{"T"}, &CreateSessionOptions{Confirm: 1}),
		map[string][]string{"T": {ResultCorrect}})
	assertStrings(t, "序列", seq, []string{"T#1"})
	for _, id := range []string{"T", "P1", "P2", "R"} {
		assertStatus(t, id, c.StatusOf(s, id), StatusMastered)
	}
	if s.Status["T"].Src != "direct" || s.Status["R"].Src != "inferred" {
		t.Errorf("src：T 应 direct、R 应 inferred")
	}
}

func TestBankDefaultConfirmThree(t *testing.T) {
	c := bankGraph(t)
	s := mustSession(t, c, []string{"T"}, nil)
	if s.Confirm != 3 || c.NeedConfirm(s, "T") != 3 {
		t.Fatalf("默认 confirm=3 / needConfirm=3，实际 %d / %d", s.Confirm, c.NeedConfirm(s, "T"))
	}
	var err error
	s, err = c.ApplyAnswer(s, "T", ResultCorrect)
	if err != nil {
		t.Fatal(err)
	}
	if _, done := s.Status["T"]; done {
		t.Errorf("只答对 1 题不应判掌握")
	}
	if q := c.NextQuestion(s); q != "T" {
		t.Errorf("应继续问 T，实际 %s", q)
	}
	if it := c.NextItem(s); it == nil || it.Attempt != 2 {
		t.Errorf("nextItem attempt 应为 2")
	}
	s, _ = c.ApplyAnswer(s, "T", ResultCorrect)
	if _, done := s.Status["T"]; done {
		t.Errorf("只答对 2 题不应判掌握")
	}
	s, _ = c.ApplyAnswer(s, "T", ResultCorrect)
	assertStatus(t, "T", c.StatusOf(s, "T"), StatusMastered)
	for _, id := range []string{"P1", "P2", "R"} {
		assertStatus(t, id, c.StatusOf(s, id), StatusMastered)
	}
	if q := c.NextQuestion(s); q != "" {
		t.Errorf("结束后不应再出题，实际 %s", q)
	}
}

func TestBankNeedConfirmCappedByBankSize(t *testing.T) {
	c := singleGraph(t, mq("M", 1, "s"), mq("M", 2, "b"))
	s := mustSession(t, c, []string{"M"}, &CreateSessionOptions{Confirm: 3})
	if n := c.NeedConfirm(s, "M"); n != 2 {
		t.Fatalf("题库只有 2 题时 needConfirm 应为 2，实际 %d", n)
	}
	s, _ = c.ApplyAnswer(s, "M", ResultCorrect)
	if _, done := s.Status["M"]; done {
		t.Errorf("答对 1 题不应掌握")
	}
	s, _ = c.ApplyAnswer(s, "M", ResultCorrect)
	assertStatus(t, "M", c.StatusOf(s, "M"), StatusMastered)
}

func TestBankClampConfirm(t *testing.T) {
	cases := map[int]int{2: 2, 3: 3, 5: 5, 4: 3, 1: 3, 0: 3, 10: 3}
	for in, want := range cases {
		if got := ClampConfirm(in); got != want {
			t.Errorf("ClampConfirm(%d) = %d，期望 %d", in, got, want)
		}
	}
	c := bankGraph(t)
	if s := mustSession(t, c, []string{"T"}, &CreateSessionOptions{Confirm: 5}); s.Confirm != 5 {
		t.Errorf("confirm=5 应生效，实际 %d", s.Confirm)
	}
}

func TestBankWrongThenSecondQuestionNoBacktrack(t *testing.T) {
	c := bankGraph(t)
	s, err := c.ApplyAnswer(mustSession(t, c, []string{"T"}, nil), "T", ResultWrong)
	if err != nil {
		t.Fatal(err)
	}
	if q := c.NextQuestion(s); q != "T" {
		t.Errorf("第一题错后应继续问 T，实际 %s", q)
	}
	it := c.NextItem(s)
	if it == nil || it.Attempt != 2 {
		t.Fatalf("nextItem attempt 应为 2")
	}
	if it.Question.ID == s.Answers[0].QID {
		t.Errorf("不能再出同一道题 %s", it.Question.ID)
	}
	if _, done := s.Status["T"]; done {
		t.Errorf("此时不应有结论")
	}
	if !c.IsUnconfirmed(s, "T") {
		t.Errorf("答错待确认的点 IsUnconfirmed 应为 true")
	}
}

func TestBankWrongWrongWeakBacktracks(t *testing.T) {
	c := bankGraph(t)
	s, seq := runBank(t, c, mustSession(t, c, []string{"T"}, &CreateSessionOptions{Confirm: 1}), map[string][]string{
		"T":  {ResultWrong, ResultWrong},
		"P1": {ResultWrong, ResultWrong},
		"R":  {ResultCorrect},
		"P2": {ResultCorrect},
	})
	assertStrings(t, "序列", seq, []string{"T#1", "T#2", "P1#1", "P1#2", "R#1", "P2#1"})
	assertStatus(t, "T", c.StatusOf(s, "T"), StatusWeak)
	assertStatus(t, "P1", c.StatusOf(s, "P1"), StatusWeak)
	assertStatus(t, "R", c.StatusOf(s, "R"), StatusMastered)
	sum := c.Summarize(s)
	assertStrings(t, "roots", idsOfRoots(sum), []string{"P1"})
	assertStrings(t, "shaky", sum.Shaky, nil)
}

func TestBankWrongSkipNoWeakNoBacktrack(t *testing.T) {
	c := bankGraph(t)
	// 记录 wrong 后、skip 前出题栈的可观测状态。
	s, err := c.ApplyAnswer(mustSession(t, c, []string{"T"}, &CreateSessionOptions{Confirm: 1}), "T", ResultWrong)
	if err != nil {
		t.Fatal(err)
	}
	if q := c.NextQuestion(s); q != "T" {
		t.Fatalf("wrong 后应继续问 T 的确认题，实际 %s", q)
	}
	stackBefore := append([]string(nil), s.Stack...)
	// 第二答是确认题 skip。冻结语义：skip 不新增 fail、不直接 weak 化、不回溯先修，
	// 也不撤销历史 wrong 的待确认状态——因此 IsUnconfirmed 仍 true、仍可能继续出 T 的确认题。
	s, seq := runBank(t, c, s, map[string][]string{"T": {ResultWrong, ResultSkip, ResultSkip, ResultSkip}})
	if len(seq) < 1 || seq[0] != "T#2" {
		t.Errorf("skip 应按顺序作答，实际序列 %v", seq)
	}
	for _, x := range seq {
		if x != "T#2" && x != "T#3" && x != "T#4" {
			t.Errorf("skip 后只应继续出 T 的确认题，实际序列 %v", seq)
		}
	}
	// 待确认状态来自历史 wrong，skip 不得清除它。
	if !c.IsUnconfirmed(s, "T") {
		t.Errorf("历史 wrong 的待确认状态应保留，IsUnconfirmed 应为 true")
	}
	// skip 本身不得形成结论、不得把点 weak 化写入 Status、不得推先修。
	if st, ok := s.Status["T"]; ok {
		t.Errorf("wrong+skip 不应写入结论（skip 不 weak 化），实际 %v", st)
	}
	if len(s.Stack) != len(stackBefore) || s.Stack[0] != stackBefore[0] {
		t.Errorf("skip 不应回溯压栈（%v → %v）", stackBefore, s.Stack)
	}
	// skip 不新增 fail：Attempts 中 wrong 恰好 1 条，其余全是 skip。
	at := s.Attempts["T"]
	if len(at) != len(s.Answers) || at[0].Result != ResultWrong || at[1].Result != ResultSkip {
		t.Errorf("Attempts[T] 应完整保留全部作答（首条 wrong、次条 skip），实际 %v", at)
	}
	wrongs := 0
	for i, a := range at {
		if a.Result == ResultWrong {
			wrongs++
			if i != 0 {
				t.Errorf("skip 之后不应再出现 wrong，实际 %v", at)
			}
		}
	}
	if wrongs != 1 {
		t.Errorf("skip 不应新增 fail，wrong 计数应仍为 1，实际 %d（%v）", wrongs, at)
	}
	for i := 1; i < len(s.Answers); i++ {
		if s.Answers[i].Result != ResultSkip || s.Answers[i].ID != "T" {
			t.Errorf("skip 之后的作答应仍是 T/skip，实际 %v", s.Answers[i])
		}
	}
	// 不回溯 P1：skip 没有把先修推到栈上，也没有实际出先修题。
	if len(s.Attempts["P1"]) != 0 {
		t.Errorf("P1 不应被出题，实际 %v", s.Attempts["P1"])
	}
	// 未形成结论（仅历史 wrong 待确认）→ 不进 shaky；不出现 weak 根因列表（无 direct weak）。
	sum := c.Summarize(s)
	assertStrings(t, "shaky", sum.Shaky, nil)
	assertStrings(t, "mastered", sum.Mastered, nil)
}

func TestBankWrongThenCorrectShaky(t *testing.T) {
	c := bankGraph(t)
	s, seq := runBank(t, c, mustSession(t, c, []string{"T"}, nil),
		map[string][]string{"T": {ResultWrong, ResultCorrect}})
	assertStrings(t, "序列", seq, []string{"T#1", "T#2"})
	assertStatus(t, "T", c.StatusOf(s, "T"), StatusShaky)
	if q := c.NextQuestion(s); q != "" {
		t.Errorf("shaky 后不应回溯，实际 %s", q)
	}
	for _, id := range []string{"P1", "P2", "R"} {
		assertStatus(t, id, c.StatusOf(s, id), StatusUnknown)
	}
	sum := c.Summarize(s)
	assertStrings(t, "shaky", sum.Shaky, []string{"T"})
	assertStrings(t, "weak", sum.Weak, nil)
	assertStrings(t, "roots", idsOfRoots(sum), nil)
	assertStrings(t, "untested", sortedCopy(sum.Untested), []string{"P1", "P2", "R"})
	if len(sum.Path) != 1 || sum.Path[0].ID != "T" || sum.Path[0].Status != StatusShaky {
		t.Errorf("不稳点应进学习路径，实际 %v", sum.Path)
	}
}

func TestBankShakyPrereqInPath(t *testing.T) {
	c := bankGraph(t)
	s, _ := runBank(t, c, mustSession(t, c, []string{"T"}, &CreateSessionOptions{Confirm: 1}), map[string][]string{
		"T":  {ResultWrong, ResultWrong},
		"P1": {ResultWrong, ResultCorrect},
		"P2": {ResultWrong, ResultWrong},
		"R":  {ResultCorrect},
	})
	assertStatus(t, "P1", c.StatusOf(s, "P1"), StatusShaky)
	assertStatus(t, "P2", c.StatusOf(s, "P2"), StatusWeak)
	sum := c.Summarize(s)
	assertStrings(t, "roots", idsOfRoots(sum), []string{"P2"})
	assertStrings(t, "path", sortedCopy(idsOfPath(sum)), []string{"P1", "P2", "T"})
	for _, p := range sum.Path {
		if p.ID == "P1" && p.Status != StatusShaky {
			t.Errorf("P1 在路径里应为 shaky")
		}
	}
	assertTopo(t, c, idsOfPath(sum))
}

func TestBankDifficultySelection(t *testing.T) {
	c := bankGraph(t)
	for i := 0; i < 60; i++ {
		s0 := mustSession(t, c, []string{"P2"}, &CreateSessionOptions{Seed: "k" + itoa(i)})
		first := c.NextItem(s0)
		if first == nil || first.Question.Difficulty != "standard" {
			t.Fatalf("有 standard 时第一题应为 standard，实际 %+v", first)
		}
		sw, _ := c.ApplyAnswer(s0, "P2", ResultWrong)
		if it := c.NextItem(sw); it == nil || it.Question.ID != "P2_q3" {
			t.Fatalf("答错后应出唯一的 basic（P2_q3），实际 %+v", it)
		}
		sr, _ := c.ApplyAnswer(s0, "P2", ResultCorrect)
		if it := c.NextItem(sr); it == nil || it.Question.Difficulty != "advanced" {
			t.Fatalf("答对后应出拓展档，实际 %+v", it)
		}
	}
	// 没有 standard 时第一题退到 basic（不出拓展）
	gb := singleGraph(t, mq("M", 1, "b"), mq("M", 2, "b"), mq("M", 3, "a"))
	for i := 0; i < 20; i++ {
		it := gb.NextItem(mustSession(t, gb, []string{"M"}, &CreateSessionOptions{Seed: itoa(i)}))
		if it == nil || it.Question.Difficulty != "basic" {
			t.Fatalf("没有 standard 时第一题应为 basic，实际 %+v", it)
		}
	}
	// 没有 basic 时，答错后取同难度 standard
	g3 := singleGraph(t, mq("M", 1, "s"), mq("M", 2, "s"), mq("M", 3, "s"), mq("M", 4, "a"))
	for i := 0; i < 20; i++ {
		s1, _ := g3.ApplyAnswer(mustSession(t, g3, []string{"M"}, &CreateSessionOptions{Seed: itoa(i)}), "M", ResultWrong)
		if it := g3.NextItem(s1); it == nil || it.Question.Difficulty != "standard" {
			t.Fatalf("没有 basic 时答错后应出 standard，实际 %+v", it)
		}
	}
	// 只有拓展档时才出拓展
	g4 := singleGraph(t, mq("M", 1, "a"), mq("M", 2, "a"))
	s4, _ := g4.ApplyAnswer(mustSession(t, g4, []string{"M"}, &CreateSessionOptions{Seed: "1"}), "M", ResultWrong)
	if it := g4.NextItem(s4); it == nil || it.Question.Difficulty != "advanced" {
		t.Fatalf("只有拓展档时应出 advanced，实际 %+v", it)
	}
}

func TestBankSeedDeterminism(t *testing.T) {
	c := bankGraph(t)
	script := map[string][]string{
		"T": {ResultWrong, ResultWrong}, "P1": {ResultWrong, ResultWrong},
		"R": {ResultWrong, ResultWrong}, "P2": {ResultWrong, ResultWrong},
	}
	trace := func(seed string) string {
		s, _ := runBank(t, c, mustSession(t, c, []string{"T"}, &CreateSessionOptions{Seed: seed}), script)
		var ids []string
		for _, a := range s.Answers {
			ids = append(ids, a.QID)
		}
		return strings.Join(ids, ",")
	}
	if trace("abc") != trace("abc") {
		t.Errorf("同一种子应产生相同序列")
	}
	if trace("42") != trace("42") {
		t.Errorf("同一种子应产生相同序列")
	}
	variants := map[string]bool{}
	for i := 0; i < 40; i++ {
		variants[trace("s"+itoa(i))] = true
	}
	if len(variants) <= 5 {
		t.Errorf("不同种子应产生不同题目序列，实际只有 %d 种", len(variants))
	}
	s2, _ := runBank(t, c, mustSession(t, c, []string{"T"}, &CreateSessionOptions{Seed: "9"}),
		map[string][]string{"T": {ResultWrong, ResultCorrect}})
	r, err := c.Replay([]string{"T"}, s2.Answers, &CreateSessionOptions{Seed: "9"})
	if err != nil {
		t.Fatal(err)
	}
	deepEqualJSON(t, "Replay(seed=9)", r, s2)
}

func TestBankAvoid(t *testing.T) {
	c := bankGraph(t)
	avoid := []string{"T_q1", "T_q2"}
	for i := 0; i < 40; i++ {
		s0 := mustSession(t, c, []string{"T"}, &CreateSessionOptions{Seed: itoa(i), Avoid: avoid})
		it := c.NextItem(s0)
		if it == nil {
			t.Fatal("应出题")
		}
		if it.Question.ID == "T_q1" || it.Question.ID == "T_q2" {
			t.Fatalf("第一题应避开出过的题，实际 %s", it.Question.ID)
		}
	}
	all := []string{"T_q1", "T_q2", "T_q3", "T_q4"}
	s1 := mustSession(t, c, []string{"T"}, &CreateSessionOptions{Seed: "1", Avoid: all})
	if it := c.NextItem(s1); it == nil || !containsStr(all, it.Question.ID) {
		t.Fatalf("全部避开时也不能卡住")
	}
	// 第二题：basic 优先于 avoid
	hit := 0
	for i := 0; i < 40; i++ {
		s0 := mustSession(t, c, []string{"T"}, &CreateSessionOptions{Seed: itoa(i), Avoid: []string{"T_q3"}})
		if c.NextItem(s0).Question.Difficulty != "standard" {
			continue
		}
		s2, _ := c.ApplyAnswer(s0, "T", ResultWrong)
		if it := c.NextItem(s2); it == nil || it.Question.ID != "T_q3" {
			t.Fatalf("答错后应出唯一的 basic T_q3，实际 %+v", it)
		}
		hit++
	}
	if hit <= 5 {
		t.Errorf("standard 第一题的种子样本过少：%d", hit)
	}
	s3, _ := runBank(t, c, mustSession(t, c, []string{"T"}, nil), map[string][]string{
		"T": {ResultWrong, ResultWrong}, "P1": {ResultWrong, ResultWrong},
		"P2": {ResultWrong, ResultWrong}, "R": {ResultWrong, ResultWrong}})
	seen := map[string]bool{}
	for _, a := range s3.Answers {
		if seen[a.QID] {
			t.Errorf("会话内重复出题 %s", a.QID)
		}
		seen[a.QID] = true
	}
}

func TestBankLimitCountsQuestions(t *testing.T) {
	c := bankGraph(t)
	s, seq := runBank(t, c, mustSession(t, c, []string{"T"}, &CreateSessionOptions{Limit: 3}), map[string][]string{
		"T": {ResultWrong, ResultWrong}, "P1": {ResultWrong, ResultWrong},
		"R": {ResultWrong, ResultWrong}, "P2": {ResultWrong, ResultWrong}})
	assertStrings(t, "序列", seq, []string{"T#1", "T#2", "P1#1"})
	if len(s.Answers) != 3 || c.NextQuestion(s) != "" {
		t.Errorf("达到题量上限应停：answers=%d next=%s", len(s.Answers), c.NextQuestion(s))
	}
	assertStatus(t, "P1", c.StatusOf(s, "P1"), StatusWeak)
	sum := c.Summarize(s)
	assertStrings(t, "unconfirmed", sum.Unconfirmed, []string{"P1"})
	if !sum.LimitReached {
		t.Errorf("limitReached 应为 true")
	}
	assertStrings(t, "untested", sortedCopy(sum.Untested), []string{"P2", "R"})
}

func TestBankSingleQuestionTopicWrongImmediatelyWeak(t *testing.T) {
	c := testSpec{
		topics: []*Topic{{ID: "T", Grade: 1}, {ID: "P1", Grade: 1}, {ID: "P2", Grade: 1}, {ID: "R", Grade: 1}},
		edges: []*Edge{
			{TopicID: "P1", PrerequisiteID: "R"}, {TopicID: "T", PrerequisiteID: "P1"}, {TopicID: "T", PrerequisiteID: "P2"},
		},
		questions: []*Question{mq("T", 1, "s"), mq("P1", 1, "s"), mq("P2", 1, "s"), mq("R", 1, "s")},
	}.mustCatalog(t)
	s, err := c.ApplyAnswer(mustSession(t, c, []string{"T"}, nil), "T", ResultWrong)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, "T", c.StatusOf(s, "T"), StatusWeak)
	if q := c.NextQuestion(s); q != "P1" {
		t.Errorf("应回溯到 P1，实际 %s", q)
	}
}

func TestBankApplyAnswerPureWithText(t *testing.T) {
	c := bankGraph(t)
	s0 := mustSession(t, c, []string{"T"}, &CreateSessionOptions{Seed: "5"})
	s1, err := c.ApplyAnswerText(s0, "T", ResultWrong, "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(s0.Answers) != 0 || s0.Attempts["T"] != nil {
		t.Errorf("ApplyAnswerText 不应修改入参")
	}
	if s1.Answers[0].Text != "42" {
		t.Errorf("答题记录应带用户答案，实际 %q", s1.Answers[0].Text)
	}
	if !strings.HasPrefix(s1.Answers[0].QID, "T_q") {
		t.Errorf("答题记录应带 qid，实际 %q", s1.Answers[0].QID)
	}
	s2, err := c.ApplyAnswer(s1, "T", ResultCorrect)
	if err != nil {
		t.Fatal(err)
	}
	if len(s1.Answers) != 1 || s1.Answers[0].Result != ResultWrong {
		t.Errorf("ApplyAnswer 不应修改入参 s1")
	}
	if len(s2.Attempts["T"]) != 2 {
		t.Errorf("s2 应有 2 次尝试，实际 %v", s2.Attempts["T"])
	}
}

/* ---- PRNG ---- */

func TestMulberry32Deterministic(t *testing.T) {
	a := Mulberry32(123)
	b := Mulberry32(123)
	for i := 0; i < 100; i++ {
		x, y := a(), b()
		if x != y {
			t.Fatalf("同种子第 %d 个输出不同：%v vs %v", i, x, y)
		}
		if x < 0 || x >= 1 {
			t.Fatalf("输出应在 [0,1)，实际 %v", x)
		}
	}
	if rand01("s", "T|1") != rand01("s", "T|1") {
		t.Errorf("rand01 应确定")
	}
}

/* ---- Undo / CorrectAnswer ---- */

func TestUndoCorrectReplay(t *testing.T) {
	c := bankGraph(t)
	s0 := mustSession(t, c, []string{"T"}, &CreateSessionOptions{Seed: "u"})
	s1, _ := c.ApplyAnswer(s0, "T", ResultWrong)
	s2, _ := c.ApplyAnswer(s1, "T", ResultCorrect)

	u, err := c.Undo(s2, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	deepEqualJSON(t, "Undo 1 次 = 重放到 s1", u, s1)
	u2, err := c.Undo(s2, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	deepEqualJSON(t, "Undo 2 次 = 初始会话", u2, s0)
	uAll, err := c.Undo(s2, 99, nil)
	if err != nil {
		t.Fatal(err)
	}
	deepEqualJSON(t, "Undo 超过记录数 = 初始会话", uAll, s0)
	if len(s2.Answers) != 2 {
		t.Errorf("Undo 不应修改入参")
	}

	// 更正第 1 次作答为 correct：重放后应与「直接答对再答对」一致
	ca, err := c.CorrectAnswer(s2, 0, ResultCorrect, "")
	if err != nil {
		t.Fatal(err)
	}
	ref := mustSession(t, c, []string{"T"}, &CreateSessionOptions{Seed: "u"})
	ref, _ = c.ApplyAnswer(ref, "T", ResultCorrect)
	ref, _ = c.ApplyAnswer(ref, "T", ResultCorrect)
	deepEqualJSON(t, "更正第 1 次作答", ca, ref)
	if _, err := c.CorrectAnswer(s2, 5, ResultCorrect, ""); err == nil {
		t.Errorf("越界下标应报错")
	}
	if _, err := c.CorrectAnswer(s2, 0, "maybe", ""); err == nil {
		t.Errorf("非法结果应报错")
	}
}

func TestUndoPreservesScreenMode(t *testing.T) {
	c := bankGraph(t)
	s := mustSession(t, c, []string{"T", "P1", "P2", "R"}, &CreateSessionOptions{Mode: ModeScreen, Limit: 2, Seed: "z"})
	s, _ = c.ApplyAnswer(s, "T", ResultWrong)
	s, _ = c.ApplyAnswer(s, "T", ResultCorrect)
	u, err := c.Undo(s, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if u.Mode != ModeScreen || u.Limit != 2 || u.Seed != "z" || len(u.Scope) != 4 {
		t.Errorf("Undo 应保留 mode/limit/seed/scope，实际 mode=%s limit=%d seed=%s scope=%v", u.Mode, u.Limit, u.Seed, u.Scope)
	}
}

func TestVerdictHelpers(t *testing.T) {
	c := bankGraph(t)
	s := mustSession(t, c, []string{"T"}, nil)
	if s.Status == nil || s.Attempts == nil || s.Answers == nil {
		t.Errorf("会话的 status/attempts/answers 应为非 nil（可 JSON 序列化）")
	}
	if _, err := c.Replay([]string{"T"}, []AnswerRecord{{ID: "nope", Result: ResultCorrect}}, nil); err == nil {
		t.Errorf("重放未知知识点应报错")
	}
}

// TestHasBankEmpty 验证无任何题目时 HasBank=false 且不可测。
func TestHasBankEmpty(t *testing.T) {
	c := testSpec{topics: []*Topic{{ID: "A"}}}.mustCatalog(t)
	if c.HasBank {
		t.Errorf("无题时 HasBank 应为 false")
	}
	if c.IsTestable("A") {
		t.Errorf("无题知识点不可测")
	}
	s := mustSession(t, c, []string{"A"}, nil)
	if n := c.NextQuestion(s); n != "" {
		t.Errorf("不可测目标不应出题，实际 %s", n)
	}
	c.NextItem(s)
	if it := c.NextItem(s); it != nil {
		t.Errorf("不可测目标 NextItem 应为 nil")
	}
	if c.PickQuestion(s, "A", 1) != nil {
		t.Errorf("无题时 PickQuestion 应为 nil")
	}
	if c.PickQuestion(s, "missing", 1) != nil {
		t.Errorf("未知知识点 PickQuestion 应为 nil")
	}
}
