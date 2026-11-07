package taxonomy

import (
	"encoding/json"
	"fmt"
	"testing"
)

/* 全量数据上的诊断流程测试：目标模式、筛查模式、可种子化选题、
   undo / correct / replay / 序列化、年级目标解析、章节解析与按章汇总。
   数据基线见 catalog_data_test.go。 */

// applyAll 按引擎顺序连续作答，返回 [最终会话, 出题知识点序列]。
func applyAll(t *testing.T, c *Catalog, s *Session, result string, max int) (*Session, []string) {
	t.Helper()
	asked := []string{}
	for len(asked) < max {
		id := c.NextQuestion(s)
		if id == "" {
			break
		}
		asked = append(asked, id)
		ns, err := c.ApplyAnswer(s, id, result)
		if err != nil {
			t.Fatalf("ApplyAnswer(%s,%s): %v", id, result, err)
		}
		s = ns
	}
	return s, asked
}

// 按记录重放会话的作答（不依赖 ApplyAnswer 之外的路径）。
func sessionAnswers(s *Session) []AnswerRecord {
	return append([]AnswerRecord(nil), s.Answers...)
}

func TestRealTargetSessionShape(t *testing.T) {
	c := realCatalog(t)
	s := mustSession(t, c, []string{"cnm_221"}, nil)
	if s.Mode != ModeTarget {
		t.Errorf("默认模式 = %q，期望 %q", s.Mode, ModeTarget)
	}
	if s.Scope != nil {
		t.Errorf("目标模式的 Scope 应为 nil，实际 %v", s.Scope)
	}
	if s.Limit != DefaultLimit || s.Confirm != 3 || s.Seed != "default" {
		t.Errorf("默认 limit/confirm/seed = %d/%d/%q", s.Limit, s.Confirm, s.Seed)
	}
	// 范围 = 目标 + 全部祖先，拓扑序，目标在最后
	if want := append(sortedCopy(c.Ancestors([]string{"cnm_221"})), "cnm_221"); len(s.Order) != len(want) {
		t.Errorf("范围大小 = %d，期望目标+祖先 = %d", len(s.Order), len(want))
	}
	if s.Order[len(s.Order)-1] != "cnm_221" {
		t.Errorf("目标应排在拓扑序最后，实际 %v", tail(s.Order, 3))
	}
	assertTopo(t, c, s.Order)
	// 先问目标本身（可测时栈顶即目标），题目来自该知识点题库
	if got := c.NextQuestion(s); got != "cnm_221" {
		t.Errorf("第一题的知识点 = %s，期望目标 cnm_221", got)
	}
	item := c.NextItem(s)
	if item == nil || item.TopicID != "cnm_221" || item.Question == nil || item.Question.ID != "cnm_221_q1" || item.Attempt != 1 {
		t.Errorf("NextItem = %+v，期望 cnm_221 的 q1 第一次作答", item)
	}
}

func TestRealTargetCorrectMastersOnlyTarget(t *testing.T) {
	c := realCatalog(t)
	s := mustSession(t, c, []string{"cnm_221"}, nil)
	s, err := c.ApplyAnswer(s, "cnm_221", ResultCorrect)
	if err != nil {
		t.Fatal(err)
	}
	// 单题答对不够判定（需连续 confirm 次），状态仍是未知
	assertStatus(t, "cnm_221(1题)", c.StatusOf(s, "cnm_221"), StatusUnknown)

	// 从「目标 + 一个直接先修」建会话，答对目标：祖先结论按现有状态推断，不写祖先
	pre := c.Prereqs["cnm_221"][0].PrerequisiteID
	s2 := mustSession(t, c, []string{"cnm_221", pre}, &CreateSessionOptions{Confirm: 9, Limit: 50})
	s2, err = c.ApplyAnswer(s2, "cnm_221", ResultCorrect)
	if err != nil {
		t.Fatal(err)
	}
	sum := c.Summarize(s2)
	for _, id := range []string{"cnm_221", pre} {
		if containsStr(sum.Mastered, id) {
			t.Errorf("%s 不应被判定为 mastered（仅答对 1 题、确认阈值 9）", id)
		}
	}
	if len(sum.Mastered) != 0 {
		t.Errorf("Mastered 应为空，实际 %v", sum.Mastered)
	}
}

func TestRealTargetWrongBacktracksToActualPrereq(t *testing.T) {
	c := realCatalog(t)
	s := mustSession(t, c, []string{"cnm_221"}, nil)
	s, err := c.ApplyAnswer(s, "cnm_221", ResultWrong)
	if err != nil {
		t.Fatal(err)
	}
	// 目标还有可选题时，第一次答错先不判结论（unconfirmed），出同一知识点的下一题
	if !c.IsUnconfirmed(s, "cnm_221") {
		t.Errorf("目标答错 1 题后应为 unconfirmed")
	}
	if got := c.NextQuestion(s); got != "cnm_221" {
		t.Errorf("第二次仍应问 cnm_221，实际 %s", got)
	}
	s, err = c.ApplyAnswer(s, "cnm_221", ResultWrong)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, "cnm_221", c.StatusOf(s, "cnm_221"), StatusWeak)
	// 出题栈压入范围内、可测、未决的先修（hard 在前）；上游 buildGraph 按
	// (hard?0:1)*1e6 + topics 文件序 排序，cnm_221 的先修里 cnm_148（idx 441）先于 cnm_220（idx 518）
	if len(s.Stack) == 0 || s.Stack[0] != "cnm_148" {
		t.Errorf("答错后应回溯到先修 cnm_148，栈前部 %v", tail(s.Stack, 4))
	}
	for _, id := range s.Stack {
		if !c.IsTestable(id) {
			t.Errorf("栈里出现不可测的点 %s", id)
		}
		if !containsStr(c.Ancestors([]string{"cnm_221"}), id) && id != "cnm_221" {
			t.Errorf("栈里出现范围外的点 %s", id)
		}
	}
}

func TestRealTargetLimitStopsAt16(t *testing.T) {
	c := realCatalog(t)
	s := mustSession(t, c, []string{"cnm_221"}, nil)
	s, asked := applyAll(t, c, s, ResultWrong, 40)
	if len(asked) != DefaultLimit {
		t.Errorf("持续答错时应在 %d 题停下，实际 %d 题", DefaultLimit, len(asked))
	}
	sum := c.Summarize(s)
	if !sum.LimitReached {
		t.Errorf("LimitReached 应为 true（答到上限时栈里仍有待测点）")
	}
	// 持续答错时栈可能被耗尽（只剩不可测点），此时 Finished 也允许为 true
	if !sum.Finished && c.NextQuestion(s) != "" {
		t.Errorf("Summary.Finished=false 但 NextQuestion 仍有题")
	}
	if sum.Asked != DefaultLimit {
		t.Errorf("Asked = %d", sum.Asked)
	}
	if len(sum.Weak) == 0 || len(sum.Untested) == 0 {
		t.Errorf("weak/untested 不应为空：%d/%d", len(sum.Weak), len(sum.Untested))
	}
	// 范围 111 个点，16 题不可能测完
	if len(sum.Mastered)+len(sum.Weak)+len(sum.Untested) != len(s.Order) {
		t.Errorf("分类总数 %d != 范围 %d", len(sum.Mastered)+len(sum.Weak)+len(sum.Untested), len(s.Order))
	}
	if len(sum.Roots) == 0 {
		t.Errorf("全答错后应有根因")
	}
	for _, r := range sum.Roots {
		if containsStr(sum.Weak, r.ID) == false {
			t.Errorf("根因 %s 不在 weak 列表", r.ID)
		}
	}
	if len(sum.Path) == 0 {
		t.Errorf("学习路径不应为空")
	}
	// 路径按年级升序
	prev := -1
	for _, p := range sum.Path {
		gr := c.gradeRank(p.ID)
		if gr < prev {
			t.Errorf("学习路径年级未升序：%s(%d) 出现在 %d 之后", p.ID, gr, prev)
		}
		prev = gr
	}
}

func TestRealScreenSessionShape(t *testing.T) {
	c := realCatalog(t)
	gt := mustGradeTarget(t, c, "七上", SubjectMath)
	s := mustSession(t, c, gt.IDs, &CreateSessionOptions{Mode: ModeScreen})
	if s.Mode != ModeScreen {
		t.Errorf("Mode = %q", s.Mode)
	}
	if s.Limit != ScreenLimit {
		t.Errorf("筛查默认题量 = %d，期望 %d", s.Limit, ScreenLimit)
	}
	assertStrings(t, "Scope", s.Scope, gt.IDs)
	// Scope 保存调用方给定的目标（文件顺序），而 Order 是拓扑序，两者顺序可能不同
	assertStrings(t, "范围内知识点（排序后）", sortedCopy(s.Order), sortedCopy(gt.IDs))
	if len(s.Order) != len(gt.IDs) {
		t.Errorf("筛查范围 %d != 目标 %d（筛查模式不含祖先）", len(s.Order), len(gt.IDs))
	}
	assertTopo(t, c, s.Order)
	for _, id := range s.Order {
		if c.Topic(id).Grade != 7 || c.Topic(id).Semester != "上" {
			t.Errorf("筛查范围混入非七上知识点 %s", id)
		}
	}
	// 栈里只有目标本身，且按拓扑序倒序（最靠后的先问）
	assertStrings(t, "Stack", s.Stack, reverseCopy(s.Order))
	// 覆盖：一直答错最多 40 题
	s2, asked := applyAll(t, c, s, ResultWrong, 100)
	if len(asked) != ScreenLimit {
		t.Errorf("筛查上限应为 %d，实际 %d", ScreenLimit, len(asked))
	}
	sum := c.Summarize(s2)
	if !sum.LimitReached {
		t.Errorf("40 题后 LimitReached 应为 true（范围 %d 个点）", len(s.Order))
	}
	for _, id := range asked {
		if !containsStr(s.Scope, id) {
			t.Errorf("筛查模式问到了范围外的点 %s", id)
		}
	}
	for _, r := range sum.Roots {
		for _, p := range r.OutOfScopePrereqs {
			if containsStr(s.Scope, p) {
				t.Errorf("根因 %s 的 outOfScopePrereqs 含范围内点 %s", r.ID, p)
			}
		}
		for _, p := range r.PendingPrereqs {
			if !containsStr(s.Scope, p) {
				t.Errorf("根因 %s 的 pendingPrereqs 含范围外点 %s", r.ID, p)
			}
		}
	}
}

func TestRealScreenOutOfScopePrereqs(t *testing.T) {
	c := realCatalog(t)
	gt := mustGradeTarget(t, c, "九下", SubjectMath)
	s := mustSession(t, c, gt.IDs, &CreateSessionOptions{Mode: ModeScreen, Limit: 500})
	s, asked := applyAll(t, c, s, ResultWrong, 500)
	// 答错同一知识点会追问多题，题数 >= 知识点数；知识点须全覆盖
	if len(asked) < len(gt.IDs) {
		t.Fatalf("应覆盖范围内 %d 个点，实际只问 %d 题", len(gt.IDs), len(asked))
	}
	for _, id := range gt.IDs {
		if !containsStr(asked, id) {
			t.Errorf("范围内知识点 %s 未被问到", id)
		}
	}
	sum := c.Summarize(s)
	if !sum.Finished || sum.LimitReached {
		t.Errorf("栈耗尽后 Finished/LimitReached = %v/%v", sum.Finished, sum.LimitReached)
	}
	foundOut := false
	for _, r := range sum.Roots {
		for _, e := range c.Prereqs[r.ID] {
			p := e.PrerequisiteID
			inScope := containsStr(s.Scope, p)
			switch {
			case !c.IsTestable(p):
				if !containsStr(r.ExternalPrereqs, p) {
					t.Errorf("根因 %s 的外部先修 %s 未列在 externalPrereqs", r.ID, p)
				}
			case !inScope:
				// 九下知识点的初中先修（如七、八年级）在筛查范围外
				if !containsStr(r.OutOfScopePrereqs, p) {
					t.Errorf("根因 %s 的范围外先修 %s 未列在 outOfScopePrereqs", r.ID, p)
				}
				foundOut = true
			}
		}
	}
	if !foundOut {
		t.Errorf("九下范围内的根因应存在范围外先修（初中先修）")
	}
	// 筛查模式下 IsTarget 恒为 false（targetSet 只对目标模式填充）
	for _, p := range sum.Path {
		if p.IsTarget {
			t.Errorf("筛查模式路径项 %s 的 IsTarget 应为 false", p.ID)
		}
	}
}

func TestRealSeedDeterminismAndReplay(t *testing.T) {
	c := realCatalog(t)
	gt := mustGradeTarget(t, c, "七上", SubjectMath)
	// 选题序列（知识点#题号）；前 5 题故意答错以进入回退选题分支。
	// 注意：同一知识点上换种子只影响同难度候选的次序，多数知识点会落到同一个题；
	// 只答对时回退分支不触发，差异更小。
	script := func(seed string, firstWrong int) []string {
		s := mustSession(t, c, gt.IDs, &CreateSessionOptions{Mode: ModeScreen, Seed: seed})
		var asked []string
		for i := 0; i < 30; i++ {
			id := c.NextQuestion(s)
			if id == "" {
				break
			}
			asked = append(asked, c.NextItem(s).Question.ID)
			res := ResultCorrect
			if i < firstWrong {
				res = ResultWrong
			}
			ns, err := c.ApplyAnswer(s, id, res)
			if err != nil {
				t.Fatal(err)
			}
			s = ns
		}
		return asked
	}
	a1 := script("abc", 5)
	assertStrings(t, "同种子两次运行", script("abc", 5), a1)
	// Replay 必须与逐步作答完全一致
	s := mustSession(t, c, gt.IDs, &CreateSessionOptions{Mode: ModeScreen, Seed: "abc"})
	s, _ = applyAll(t, c, s, ResultWrong, 3)
	rs, err := c.Replay(s.Targets, sessionAnswers(s), &CreateSessionOptions{Mode: ModeScreen, Seed: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	deepEqualJSON(t, "Replay 与逐步一致", rs, s)
	// JSON 往返后可继续
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back Session
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if c.NextQuestion(&back) != c.NextQuestion(s) {
		t.Errorf("JSON 往返后下一题不一致")
	}
	deepEqualJSON(t, "JSON 往返", &back, s)
	// 不同种子应改变同难度候选的次序（至少存在一个差异）。
	// 现存语义下：真实题库每个标准难度组最多 3 题，rand01 落到同一 idx 时序列相同；
	// 换用不带回退的脚本差异极小，因此这里用带回退的脚本 + 多个种子。
	diff := false
	for _, seed := range []string{"1", "2", "3", "zz", "q", "hello", "42", "s7"} {
		if !eqStrings(script(seed, 5), a1) {
			diff = true
			break
		}
	}
	if !diff {
		t.Errorf("8 个不同种子的选题序列完全相同（种子未生效）")
	}
	// 种子只影响选题，不影响范围与题量
	assertStrings(t, "不同种子的目标", mustSession(t, c, gt.IDs, &CreateSessionOptions{Mode: ModeScreen, Seed: "x"}).Targets, gt.IDs)
}

func TestRealUndoCorrectReplay(t *testing.T) {
	c := realCatalog(t)
	gt := mustGradeTarget(t, c, "八下", SubjectMath)
	s := mustSession(t, c, gt.IDs, &CreateSessionOptions{Mode: ModeScreen, Seed: "undo"})
	var refs []*Session
	for i := 0; i < 4; i++ {
		id := c.NextQuestion(s)
		ns, err := c.ApplyAnswerText(s, id, ResultWrong, "user-"+id)
		if err != nil {
			t.Fatal(err)
		}
		s = ns
		refs = append(refs, s)
	}
	// undo 1 / 2 应回到对应历史状态
	for _, n := range []int{1, 2} {
		u, err := c.Undo(s, n, nil)
		if err != nil {
			t.Fatal(err)
		}
		deepEqualJSON(t, fmt.Sprintf("Undo(%d)", n), u, refs[len(refs)-1-n])
		if u.Mode != ModeScreen || u.Scope == nil || len(u.Scope) != len(s.Scope) {
			t.Errorf("Undo 后丢失筛查模式/范围")
		}
	}
	// undo 超过记录数 = 全部撤销
	u, err := c.Undo(s, 99, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Answers) != 0 || len(u.Status) != 0 || len(u.Attempts) != 0 {
		t.Errorf("撤销全部后仍有作答/状态：%d/%d/%d", len(u.Answers), len(u.Status), len(u.Attempts))
	}
	deepEqualJSON(t, "撤销全部等于新会话", u, mustSession(t, c, gt.IDs, &CreateSessionOptions{Mode: ModeScreen, Seed: "undo"}))
	// 更正第 0 题为 correct：历史答题记录按原顺序重放（冻结语义：重放沿用历史
	// 答题记录，因此后续记录的题目/知识点可能与原会话不同）。
	first := s.Answers[0].ID
	fixed, err := c.CorrectAnswer(s, 0, ResultCorrect, "user-"+first)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixed.Answers) != len(s.Answers) {
		t.Errorf("更正后条数 = %d，期望 %d", len(fixed.Answers), len(s.Answers))
	}
	if fixed.Answers[0].Result != ResultCorrect || fixed.Answers[0].QID != s.Answers[0].QID {
		t.Errorf("第 0 条未正确更正：%v", fixed.Answers[0])
	}
	// 更正 == 用同样记录直接 Replay
	rp, err := c.Replay(s.Targets, append([]AnswerRecord{{ID: first, QID: s.Answers[0].QID, Result: ResultCorrect, Text: "user-" + first}}, s.Answers[1:]...), &CreateSessionOptions{Mode: ModeScreen, Seed: "undo"})
	if err != nil {
		t.Fatal(err)
	}
	deepEqualJSON(t, "更正 = Replay", fixed, rp)
	// 反向更正：把一条 wrong 改回 wrong 等价于原会话
	same, err := c.CorrectAnswer(s, 1, ResultWrong, "user-"+s.Answers[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	deepEqualJSON(t, "更正为原值", same, s)
	// 更正为 skip 不再等价于 wrong：skip 不计 fail、不产生 weak/shaky 结论。
	// 重放沿用历史答题记录（QID/知识点沿用原会话），这里只断言 skip 条目本身中性。
	sk, err := c.CorrectAnswer(s, 1, ResultSkip, "x")
	if err != nil {
		t.Fatal(err)
	}
	if len(sk.Answers) != len(s.Answers) {
		t.Errorf("skip 更正后条数变了")
	}
	if sk.Answers[1].Result != ResultSkip || sk.Answers[1].Text != "x" || sk.Answers[1].ID != s.Answers[1].ID {
		t.Errorf("第 1 条应为 skip 且保留文本/知识点，实际 %v", sk.Answers[1])
	}
	target := s.Targets[0]
	skipOnly, err := c.Replay(s.Targets, []AnswerRecord{{ID: target, Result: ResultSkip}}, &CreateSessionOptions{Seed: "skip-vs-wrong"})
	if err != nil {
		t.Fatal(err)
	}
	// 对照：同一目标答 wrong 时，Go 引擎给出待确认/weak（不会像 skip 那样保持 unknown）。
	wrongOnly, err := c.Replay(s.Targets, []AnswerRecord{{ID: target, Result: ResultWrong}}, &CreateSessionOptions{Seed: "skip-vs-wrong"})
	if err != nil {
		t.Fatal(err)
	}
	if c.IsUnconfirmed(wrongOnly, target) == c.IsUnconfirmed(skipOnly, target) &&
		mustJSON(t, wrongOnly.Status) == mustJSON(t, skipOnly.Status) {
		t.Errorf("skip 不应与 wrong 等价：两者 IsUnconfirmed=%v Status=%s", c.IsUnconfirmed(skipOnly, target), mustJSON(t, skipOnly.Status))
	}
	if got := c.StatusOf(skipOnly, target); got != StatusUnknown {
		t.Errorf("skip 重放后 StatusOf(%s) = %s，期望 unknown", target, got)
	}
	// 更正保留文本；越界 / 非法结果报错
	if fixed.Answers[0].Text != "user-"+first {
		t.Errorf("更正的文本 = %q", fixed.Answers[0].Text)
	}
	if _, err := c.CorrectAnswer(s, -1, ResultCorrect, ""); err == nil {
		t.Errorf("下标 -1 应报错")
	}
	if _, err := c.CorrectAnswer(s, 4, ResultCorrect, ""); err == nil {
		t.Errorf("下标 4（共 4 条）应报错")
	}
	if _, err := c.CorrectAnswer(s, 0, "maybe", ""); err == nil {
		t.Errorf("非法结果应报错")
	}
	// 入参会话不被修改
	if len(s.Answers) != 4 || s.Answers[0].Result != ResultWrong {
		t.Errorf("正确性：入参会话被修改")
	}
}

func TestRealSessionTargetValidation(t *testing.T) {
	c := realCatalog(t)
	if _, err := c.CreateSession(nil, nil); err == nil {
		t.Errorf("空目标应报错")
	}
	if _, err := c.CreateSession([]string{"cnm_221", ""}, nil); err != nil {
		t.Errorf("空串应被忽略：%v", err)
	}
	if _, err := c.CreateSession([]string{"no_such_topic"}, nil); err == nil {
		t.Errorf("未知目标应报错")
	}
	// 重复目标去重
	s, err := c.CreateSession([]string{"cnm_221", "cnm_221"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertStrings(t, "Targets", s.Targets, []string{"cnm_221"})
}

func TestRealGradeTargets(t *testing.T) {
	c := realCatalog(t)
	type want struct {
		scope, subject string
		n              int
		volumes        []string
		label          string
	}
	cases := []want{
		{"一年级", SubjectMath, 48, []string{"一上", "一下"}, "一年级"},
		{"一年级", SubjectChinese, 79, []string{"一上", "一下"}, "一年级"},
		{"一年级", SubjectScience, 30, []string{"一上", "一下"}, "一年级"},
		{"一年级", SubjectMorality, 32, []string{"一上", "一下"}, "一年级"},
		{"七上", SubjectMath, 61, []string{"七上"}, "七年级上册"},
		{"七上", SubjectHistory, 21, []string{"七上"}, "七年级上册"},
		{"七上", SubjectGeography, 17, []string{"七上"}, "七年级上册"},
		{"七上", SubjectBiology, 22, []string{"七上"}, "七年级上册"},
		{"八上", SubjectPhysics, 28, []string{"八上"}, "八年级上册"},
		{"八下", SubjectGeography, 14, []string{"八下"}, "八年级下册"},
		{"八下", SubjectBiology, 13, []string{"八下"}, "八年级下册"},
		{"八下", SubjectPhysics, 24, []string{"八下"}, "八年级下册"},
		{"九上", SubjectChemistry, 27, []string{"九上"}, "九年级上册"},
		{"九下", SubjectChemistry, 18, []string{"九下"}, "九年级下册"},
		{"九下", SubjectHistory, 23, []string{"九下"}, "九年级下册"},
		{"九年级", SubjectMath, 82, []string{"九上", "九下", "九全"}, "九年级"},
		{"九全", SubjectPhysics, 40, []string{"九全"}, "九年级全一册"},
		{"高一", SubjectMath, 78, []string{"高一"}, "高一（必修第一册）"},
		{"高一必修", SubjectMath, 78, []string{"高一"}, "高一（必修第一册）"},
		{"高二必修", SubjectMath, 65, []string{"高二"}, "高二（必修第二册）"},
		{"选必一", SubjectMath, 53, []string{"选必一"}, "选必一（选择性必修第一册）"},
		{"选必二", SubjectMath, 33, []string{"选必二"}, "选必二（选择性必修第二册）"},
		{"选择性必修第二册", SubjectMath, 33, []string{"选必二"}, "选必二（选择性必修第二册）"},
		{"选必三", SubjectMath, 42, []string{"选必三"}, "选必三（选择性必修第三册）"},
		{"必修一", SubjectEnglish, 44, []string{"必修一"}, "必修一（必修第一册）"},
		{"选必四", SubjectEnglish, 36, []string{"选必四"}, "选必四（选择性必修第四册）"},
		{"选择性必修第四册", SubjectEnglish, 36, []string{"选必四"}, "选必四（选择性必修第四册）"},
	}
	for _, w := range cases {
		gt, err := c.GradeTargets(w.scope, w.subject)
		if err != nil {
			t.Errorf("GradeTargets(%s,%s): %v", w.scope, w.subject, err)
			continue
		}
		if gt == nil {
			t.Errorf("GradeTargets(%s,%s) = nil", w.scope, w.subject)
			continue
		}
		if len(gt.IDs) != w.n {
			t.Errorf("GradeTargets(%s,%s) = %d 个，期望 %d", w.scope, w.subject, len(gt.IDs), w.n)
		}
		assertStrings(t, "Volumes "+w.scope, gt.Volumes, w.volumes)
		if gt.Label != w.label {
			t.Errorf("Label(%s) = %q，期望 %q", w.scope, gt.Label, w.label)
		}
		for _, id := range gt.IDs {
			tp := c.Topic(id)
			if tp == nil || tp.External {
				t.Errorf("%s 返回了不存在/外部知识点 %s", w.scope, id)
				break
			}
			if w.subject != SubjectAll && c.SubjectOf(tp) != w.subject {
				t.Errorf("%s 混入其他学科 %s", w.scope, id)
				break
			}
			if !containsStr(w.volumes, ParseChapter(tp.Chapter).Volume) {
				t.Errorf("%s 混入非目标册次 %s（%s）", w.scope, id, tp.Chapter)
				break
			}
		}
		// ids 按 Topics 文件顺序
		for i := 1; i < len(gt.IDs); i++ {
			if c.Index[gt.IDs[i-1]] >= c.Index[gt.IDs[i]] {
				t.Errorf("%s 的 ids 未保持文件顺序：%s 在 %s 之前", w.scope, gt.IDs[i-1], gt.IDs[i])
				break
			}
		}
	}
	// 不认识的写法返回 nil；该学科无此册次返回 nil
	for _, w := range [][2]string{
		{"", SubjectMath}, {"十年级", SubjectMath}, {"七", SubjectMath}, {"七中", SubjectMath},
		{"七上", SubjectChemistry}, {"九下", SubjectScience}, {"九上", SubjectBiology}, {"八上", SubjectChemistry},
		{"选必四", SubjectMath},
	} {
		gt, err := c.GradeTargets(w[0], w[1])
		if err != nil {
			t.Errorf("GradeTargets(%q,%s) 不应报错：%v", w[0], w[1], err)
		}
		if gt != nil {
			t.Errorf("GradeTargets(%q,%s) 应为 nil，实际 %d 个", w[0], w[1], len(gt.IDs))
		}
	}
	// 学科缺省为 math
	if gt, _ := c.GradeTargets("七上", ""); gt == nil || len(gt.IDs) != 61 {
		t.Errorf("缺省学科应为 math（61 个）")
	}
	// SubjectAll 跨学科
	if gt, _ := c.GradeTargets("九全", SubjectAll); gt == nil || len(gt.IDs) != 69 {
		t.Errorf("九全 / all 应为 69 个")
	}
}

func TestRealParseChapter(t *testing.T) {
	cases := []struct {
		in   string
		want Chapter
	}{
		{"一年级上册 第1单元 5以内数的认识和加、减法", Chapter{Volume: "一上", ChapterNo: "第1单元", ChapterName: "5以内数的认识和加、减法"}},
		{"三年级下册 预备单元1 嗨，新朋友", Chapter{Volume: "三下", ChapterNo: "预备单元1", ChapterName: "嗨，新朋友"}},
		{"二年级上册 综合与实践 量一量比一比", Chapter{Volume: "二上", ChapterNo: "综合与实践", ChapterName: "量一量比一比"}},
		{"六年级下册 复习 数的认识", Chapter{Volume: "六下", ChapterNo: "复习", ChapterName: "数的认识"}},
		{"必修第一册 欢迎单元 初高中衔接", Chapter{Volume: "必修一", ChapterNo: "欢迎单元", ChapterName: "初高中衔接"}},
		{"选择性必修第三册 第2单元 探索世界", Chapter{Volume: "选必三", ChapterNo: "第2单元", ChapterName: "探索世界"}},
		{"高一 数学建模 函数模型", Chapter{Volume: "高一", ChapterNo: "数学建模", ChapterName: "函数模型"}},
		{"选必二 数学探究 圆锥曲线中的定点", Chapter{Volume: "选必二", ChapterNo: "数学探究", ChapterName: "圆锥曲线中的定点"}},
		{"八下 第二十章 勾股定理 20.1", Chapter{Volume: "八下", ChapterNo: "第二十章", ChapterName: "勾股定理", Section: "20.1"}},
		{"九上 第二十五章 一元二次方程", Chapter{Volume: "九上", ChapterNo: "第二十五章", ChapterName: "一元二次方程"}},
	}
	for _, ch := range cases {
		got := ParseChapter(ch.in)
		if got != ch.want {
			t.Errorf("ParseChapter(%q) = %+v，期望 %+v", ch.in, got, ch.want)
		}
	}
	// 无法解析：ChapterName 原样保留
	if got := ParseChapter("杂项"); got != (Chapter{ChapterName: "杂项"}) {
		t.Errorf("ParseChapter(杂项) = %+v", got)
	}
	// 全量章名均可解析出章号与章名
	c := realCatalog(t)
	bad := 0
	for _, tp := range c.Topics {
		ch := ParseChapter(tp.Chapter)
		if ch.Volume == "" || ch.ChapterNo == "" || ch.ChapterName == "" {
			bad++
			if bad <= 5 {
				t.Errorf("章名解析不完整：%s -> %+v", tp.Chapter, ch)
			}
		}
	}
}

func TestRealChapterSummary(t *testing.T) {
	c := realCatalog(t)
	gt := mustGradeTarget(t, c, "七上", SubjectMath)
	s := mustSession(t, c, gt.IDs, &CreateSessionOptions{Mode: ModeScreen, Limit: 500})
	// 未作答：全部 untested，6 个章按教材顺序
	rows := c.ChapterSummary(s)
	if len(rows) != 6 {
		t.Fatalf("七上数学章数 = %d，期望 6", len(rows))
	}
	total := 0
	for i, r := range rows {
		total += r.Total
		if r.Untested != r.Total || r.Mastered != 0 || r.Weak != 0 || r.Shaky != 0 {
			t.Errorf("章 %s 未作答时计数异常：%+v", r.Key, r)
		}
		if len(r.UntestedIDs) != r.Total || r.WeakIDs == nil || r.ShakyIDs == nil {
			t.Errorf("章 %s 的 id 列表异常", r.Key)
		}
		if i >= 2 && rows[i-1].Weak == rows[i].Weak && rows[i-1].Shaky == rows[i].Shaky {
			// 并列时保持教材顺序：章号递增
			if cmpChapterNo(rows[i-1].ChapterNo, rows[i].ChapterNo) >= 0 {
				t.Errorf("并列时未保持教材顺序：%s 在 %s 之后", rows[i-1].ChapterNo, rows[i].ChapterNo)
			}
		}
	}
	if total != len(gt.IDs) {
		t.Errorf("各章合计 %d != 范围 %d", total, len(gt.IDs))
	}
	if rows[0].Key != "七上 第一章 有理数" || rows[0].Label != "七上 第一章 有理数" {
		t.Errorf("第一章 = %q / %q", rows[0].Key, rows[0].Label)
	}

	// 全部答错：第一章 weak 数最多，排在最前
	s2, _ := applyAll(t, c, s, ResultWrong, 500)
	rows2 := c.ChapterSummary(s2)
	if len(rows2) != 6 {
		t.Fatalf("作答后章数 = %d", len(rows2))
	}
	for i, r := range rows2 {
		if i > 0 {
			p := rows2[i-1]
			if p.Weak < r.Weak || (p.Weak == r.Weak && p.Shaky < r.Shaky) {
				t.Errorf("章排序错误：%s(w%d) 不应排在 %s(w%d) 之前", p.Key, p.Weak, r.Key, r.Weak)
			}
		}
		if r.Weak != len(r.WeakIDs) || r.Untested != len(r.UntestedIDs) {
			t.Errorf("章 %s 计数与 id 列表不一致", r.Key)
		}
		if r.Total != r.Mastered+r.Shaky+r.Weak+r.Untested {
			t.Errorf("章 %s 分类合计 %d != Total %d", r.Key, r.Mastered+r.Shaky+r.Weak+r.Untested, r.Total)
		}
	}
	nWeak := 0
	for _, r := range rows2 {
		nWeak += len(r.WeakIDs)
	}
	if nWeak != len(c.Summarize(s2).Weak) {
		t.Errorf("各章 weak 合计 %d != Summary.Weak %d", nWeak, len(c.Summarize(s2).Weak))
	}
}

// cmpChapterNo 比较「第一章」「第十章」这类章号（按中文数字/阿拉伯数字的数值）。
func cmpChapterNo(a, b string) int {
	ka, kb := chapterNoValue(a), chapterNoValue(b)
	switch {
	case ka < kb:
		return -1
	case ka > kb:
		return 1
	}
	return 0
}

func chapterNoValue(s string) int {
	digits := map[rune]int{'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	v := 0
	for _, r := range s {
		if d, ok := digits[r]; ok {
			if v != 0 {
				v *= 10
			}
			v += d
			continue
		}
		if r >= '0' && r <= '9' {
			v = v*10 + int(r-'0')
		}
	}
	return v
}

func reverseCopy(in []string) []string {
	out := make([]string, len(in))
	for i := range in {
		out[i] = in[len(in)-1-i]
	}
	return out
}

// mustGradeTarget 取年级/册次目标，取不到即失败。
func mustGradeTarget(t *testing.T, c *Catalog, scope, subject string) *GradeTarget {
	t.Helper()
	gt, err := c.GradeTargets(scope, subject)
	if err != nil {
		t.Fatalf("GradeTargets(%s,%s): %v", scope, subject, err)
	}
	if gt == nil {
		t.Fatalf("GradeTargets(%s,%s) = nil", scope, subject)
	}
	return gt
}
