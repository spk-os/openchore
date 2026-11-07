package taxonomy

import (
	"math/big"
	"testing"
)

/* DEF 卡定向复验（TX-CORE-TEST 第二轮）。
   只冻结「已确认修复」的现行语义与「仍存」缺陷，不改业务实现。
   缺陷编号沿用第一轮 TEST 报告。 */

// DEF-1：带分数归一化保留整数部分。
func TestDef1MixedFraction(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"1 1/2", "1又1/2"},
		{"-2 3/4", "-2又3/4"},
		{"1又1/2", "1又1/2"},
	} {
		if got := NormalizeText(c.in); got != c.want {
			t.Errorf("NormalizeText(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
	chkAnswer(t, "带分数与小数", "1.5", "1 1/2", VerdictCorrect)
	chkAnswer(t, "带分数写法不同", "1又1/2", "1 1/2", VerdictCorrect)
}

// DEF-2/3/4：推导式、生成器、科学计数法、嵌套 Fraction、下标链均可求值。
func TestDef234VerifyCoverage(t *testing.T) {
	cases := []struct {
		expr string
		want string // 有理数规范形式；空表示只要求不报错
	}{
		{"0.9e3*2", "1800"},
		{"1.0e5*20/16", "125000"},
		{"4.6e3/(0.5*20)", "460"},
		{"2e-4", "1/5000"}, // float64 视图，允许 1e-12 误差
		{"3e8/1e8", "3"},
		{"-2.5e-1", "-1/4"},
		{"sum([x*x for x in range(1,5)])", "30"},
		{"sum(comb(4,k) for k in range(5))", "16"},
		{"sum(1 for n in range(2,21) if sum(1 for d in range(2,n) if n%d==0)==0)", "8"},
		{"sum([1 for t in range(36) if (t//6+t%6)%2==0])", "18"},
		{"Fraction(Fraction(5,2)**2-4, Fraction(1,2))", "9/2"},
		{"[[3,4],[2,1]][1>0][1>0]", "1"},
		{"sum([[63,582,63,417,298][i] for i in range(5) if [63,582,63,417,298][i]<=500 and i!=2])", "778"},
	}
	for _, c := range cases {
		v, err := (Verifier{}).Eval(c.expr)
		if err != nil {
			t.Errorf("Eval(%q): %v", c.expr, err)
			continue
		}
		got, ok := ratOf(v)
		want, _ := new(big.Rat).SetString(c.want)
		if !ok || want == nil {
			t.Errorf("Eval(%q) = %v kind=%c，期望 %s", c.expr, v, v.kind, c.want)
			continue
		}
		if d := new(big.Rat).Sub(got, want); d.Abs(d).Cmp(big.NewRat(1, 1000000000000)) > 0 {
			t.Errorf("Eval(%q) = %s，期望 %s", c.expr, got.RatString(), c.want)
		}
	}
	// 作用域仍应受限：推导式内引用外层未定义变量必须报错（非静默 0）。
	for _, bad := range []string{"[x for y in [1,2]]", "sum(x for y in range(3))"} {
		if _, err := (Verifier{}).Eval(bad); err == nil {
			t.Errorf("Eval(%q) 应报未定义变量错误", bad)
		}
	}
}

// DEF-5：字母识别仅在答案是单个字母时生效；否则走选项文字匹配，
// 数字选项文字（含全角/负数）按文字命中判定，不命中则 manual。
func TestDef5ChoiceTextPriority(t *testing.T) {
	q := choiceQ()
	chk(t, "数字文字命中错误选项", q, "-3", VerdictWrong)
	chk(t, "数字文字命中正确选项", q, "3", VerdictCorrect)
	fw := choiceQ()
	fw.Choices[0].Text = "－３"
	chk(t, "全角负数选项文字", fw, "-3", VerdictWrong)
	chk(t, "全角小写字母仍有效", q, "（b）", VerdictCorrect)
	nomatch := choiceQ()
	nomatch.Choices[0].Text = "5"
	chk(t, "无匹配选项文字的数字答案", nomatch, "-3", VerdictManual)
	textOpt := &Question{ID: "x", Type: "choice", Answer: "A",
		Choices: []Choice{{Key: "A", Text: "x=3"}, {Key: "B", Text: "x=2"}}}
	chk(t, "含字母的选项文字 a=b", textOpt, "x=3", VerdictCorrect)
	chk(t, "多字母文本不当作选项字母", textOpt, "AB", VerdictManual)

	c := realCatalog(t)
	checked := 0
	for _, q := range c.Questions {
		if q.Type != "choice" {
			continue
		}
		for _, ch := range q.Choices {
			if len([]rune(ch.Text)) <= 1 {
				continue // 单字母选项文字与字母格式天然歧义，另行说明
			}
			want := VerdictWrong
			if ch.Key == q.Answer {
				want = VerdictCorrect
			}
			if got := JudgeQuestion(q, ch.Text); got.Verdict != want {
				t.Errorf("全量：选项 %s 的文字 %q 判为 %s，期望 %s", ch.Key, ch.Text, got.Verdict, want)
				checked = 1 << 30 // 只报前若干条
			}
			checked++
			if checked > 40000 {
				break
			}
		}
		if checked > 40000 {
			break
		}
	}
}

// DEF-6（已修）：单变量答案变量名必须与参考一致；多变量按变量名对齐（顺序无关）。
func TestDef6VariableNames(t *testing.T) {
	chkAnswer(t, "单变量名不同", "y=3", "x=3", VerdictWrong)
	chkAnswer(t, "单变量名不同（分数）", "y=3/2", "x=3/2", VerdictWrong)
	chkAnswer(t, "单变量一致", "x=3", "x=3", VerdictCorrect)
	chkAnswer(t, "单变量值错", "x=4", "x=3", VerdictWrong)
	chk(t, "numeric 题单变量名不同", &Question{ID: "n", Type: "numeric", Answer: "x=3"}, "y=3", VerdictWrong)
	chk(t, "numeric 题单变量名一致", &Question{ID: "n", Type: "numeric", Answer: "x=3"}, "x=3", VerdictCorrect)
	// 多变量：变量名参与对齐，乱配判 wrong。
	chkAnswer(t, "多变量交换值", "x=2,y=3", "x=3,y=2", VerdictWrong)
	chkAnswer(t, "多变量换顺序", "y=2,x=3", "x=3,y=2", VerdictCorrect)
	chkAnswer(t, "多变量同名异值", "x=3,y=3", "x=3,y=2", VerdictWrong)
	chkAnswer(t, "两个变量写作 和 分隔", "x=3和y=2", "x=3,y=2", VerdictCorrect)
}

// DEF-6（已修）：单位缺失（一方有单位、一方没有）不再数值相等即判 correct，
// 按现有 manual 语义返回 unsure → 人工复核；真正的单位冲突仍判 wrong。
func TestDef6UnitlessStillAccepted(t *testing.T) {
	// 单位缺失：双方单位不同且一方为空 → unsure（manual，不自判正确）
	chkAnswer(t, "缺单位", "12", "12 m", VerdictManual)
	chkAnswer(t, "单位缺失方向相反", "12m", "12", VerdictManual)
	chkAnswer(t, "缺单位带全角", "12", "12米", VerdictManual)
	// 参考与用户都无单位：数值相等仍 correct（单位同为空，不属单位缺失）
	chkAnswer(t, "双方都无单位", "12", "12", VerdictCorrect)
	chkAnswer(t, "双方都无单位（分数）", "1/2", "0.5", VerdictCorrect)
	// 真正的单位冲突仍判 wrong，不能因收紧缺失而连带影响。
	chkAnswer(t, "单位冲突", "12 s", "12 m", VerdictWrong)
	chkAnswer(t, "单位冲突无空格", "12s", "12m", VerdictWrong)
	chkAnswer(t, "单位量级不同", "1 m", "100 cm", VerdictWrong)
	// 单位一致（含同义单位）仍 correct
	chkAnswer(t, "同义单位", "12米", "12 m", VerdictCorrect)
	chkAnswer(t, "面积同义单位", "25m^2", "25 平方米", VerdictCorrect)
	c := realCatalog(t)
	unitRef, bareRef := 0, 0
	for _, q := range c.Questions {
		if q.Type != "numeric" {
			continue
		}
		p := parseAnswer(q.Answer).Parts[0]
		switch {
		case p.UnitName != "":
			unitRef++
		case p.Kind == "num":
			bareRef++
		}
	}
	if bareRef == 0 {
		t.Errorf("全量数据中没有纯数值参考答案，无法评估缺单位收紧的影响")
	}
	// 数据侧现状：numeric 参考答案全部不带单位，收紧后「用户多带单位」从 correct 变 manual，
	// 需人工复核；纯数值用户答案不受影响（双方单位同为空）。
	t.Logf("DEF-6：带单位数值参考答案 %d 条 / 纯数值 %d 条", unitRef, bareRef)
}

// DEF-7：Prereqs 在 hard 优先、soft 之后（同强度按文件顺序）。
func TestDef7PrereqHardBeforeSoft(t *testing.T) {
	c := realCatalog(t)
	mixed := 0
	for id, es := range c.Prereqs {
		seenSoft := false
		for _, e := range es {
			if e.Strength == "soft" {
				seenSoft = true
				continue
			}
			if seenSoft {
				t.Errorf("%s 的先修顺序：soft 之后出现 hard（%s）", id, e.PrerequisiteID)
				break
			}
		}
		if len(es) > 1 {
			for _, e := range es {
				if e.Strength == "soft" {
					mixed++
					break
				}
			}
		}
	}
	if mixed == 0 {
		t.Errorf("没有含 soft 先修的知识点，顺序断言无意义")
	}
	t.Logf("DEF-7：含 soft 先修的知识点 %d 个", mixed)
	// 同时冻结：soft 先修不会在 hard 之前被排到栈顶（抽样目标 cnm_221）。
	for _, id := range []string{"cnm_221", "cnm_h216", "cnp_h120"} {
		es := c.Prereqs[id]
		seenSoft := false
		for _, e := range es {
			if e.Strength == "soft" {
				seenSoft = true
			} else if seenSoft {
				t.Errorf("%s 先修 soft 在 hard 之前，排序失效", id)
			}
		}
	}
}

// DEF-8：Undo 撤销 n 条后，记录与元数据等于「n 条作答前的历史」；
// 引擎按 Replay 重放，历史先修仍在栈中，因此未作答点应保持未测状态。
func TestDef8UndoCorrectionReplay(t *testing.T) {
	c := realCatalog(t)
	gt := mustGradeTarget(t, c, "八下", SubjectMath)
	s := mustSession(t, c, gt.IDs, &CreateSessionOptions{Mode: ModeScreen, Seed: "undo"})
	const rounds = 6
	var asked []string
	for i := 0; i < rounds; i++ {
		id := c.NextQuestion(s)
		asked = append(asked, id)
		ns, err := c.ApplyAnswerText(s, id, ResultWrong, "u-"+id)
		if err != nil {
			t.Fatal(err)
		}
		s = ns
	}
	if len(s.Answers) != rounds {
		t.Fatalf("记录数 = %d，期望 %d", len(s.Answers), rounds)
	}
	for n := 1; n <= rounds; n++ {
		u, err := c.Undo(s, n, nil)
		if err != nil {
			t.Fatalf("Undo(%d): %v", n, err)
		}
		keep := rounds - n
		if len(u.Answers) != keep {
			t.Errorf("Undo(%d) 记录数 = %d，期望 %d", n, len(u.Answers), keep)
			continue
		}
		for i := 0; i < keep; i++ {
			if u.Answers[i] != s.Answers[i] {
				t.Errorf("Undo(%d) 第 %d 条记录被改写：%+v != %+v", n, i, u.Answers[i], s.Answers[i])
			}
		}
		if u.Mode != s.Mode || u.Seed != s.Seed || len(u.Scope) != len(s.Scope) || len(u.Targets) != len(s.Targets) {
			t.Errorf("Undo(%d) 会话元数据漂移", n)
		}
		if got := c.NextQuestion(u); got != asked[keep] {
			t.Errorf("Undo(%d) 下一题 = %s，期望回到历史第 %d 题所在的 %s", n, got, keep, asked[keep])
		}
	}
	u, err := c.Undo(s, rounds+5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Answers) != 0 || len(u.Status) != 0 || len(u.Attempts) != 0 {
		t.Errorf("撤销超过记录数后仍有残留：%d/%d/%d", len(u.Answers), len(u.Status), len(u.Attempts))
	}

	// 更正第 0 题为 correct：记录数不变、该条被替换，结果等于显式 Replay，原会话不变。
	first := s.Answers[0]
	fixed, err := c.CorrectAnswer(s, 0, ResultCorrect, first.Text)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixed.Answers) != rounds {
		t.Fatalf("更正后记录数 = %d，期望 %d", len(fixed.Answers), rounds)
	}
	if fixed.Answers[0].Result != ResultCorrect || fixed.Answers[0].QID != first.QID {
		t.Errorf("第 0 条更正错误：%+v", fixed.Answers[0])
	}
	replayRecs := append([]AnswerRecord{{ID: first.ID, QID: first.QID, Result: ResultCorrect, Text: first.Text}}, s.Answers[1:]...)
	rp, err := c.Replay(s.Targets, replayRecs, &CreateSessionOptions{Mode: ModeScreen, Seed: "undo"})
	if err != nil {
		t.Fatal(err)
	}
	deepEqualJSON(t, "更正 = Replay", fixed, rp)
	if s.Answers[0].Result != ResultWrong {
		t.Errorf("CorrectAnswer 修改了原会话")
	}
	// 已知语义：重放沿用历史 QID，更正后同位置的题号可能不同（不重新选题）。
	if fixed.Answers[1].QID != s.Answers[1].QID {
		t.Logf("DEF-8：更正后重放沿用历史题目（%s vs %s，不重新选题）", fixed.Answers[1].QID, s.Answers[1].QID)
	}
}

// DEF-9：fixture 装配器不设 HasBank，生产 Load 设置。
func TestDef9HasBankAssembler(t *testing.T) {
	c := realCatalog(t)
	if !c.HasBank {
		t.Errorf("Load 后 HasBank 应为 true")
	}
	for _, tp := range c.Topics {
		if len(c.Bank[tp.ID]) == 0 {
			t.Errorf("Load 后知识点 %s 没有题库", tp.ID)
			break
		}
	}
	// 装配器路径：fixture 会话仍可出题（按题库实体），只是 HasBank=false。
	fc := smallCatalog()
	if fc.HasBank {
		t.Errorf("fixture 装配器不应设置 HasBank（若已修，请更新本断言与 DEF-9）")
	}
	if len(fc.Bank) == 0 {
		t.Errorf("fixture 装配器应仍按题库实体提供题目")
	}
}

// ratOf 把 'q'（有理数）或 'f'（科学计数法引入的 float64）统一转成有理数。
func ratOf(v vValue) (*big.Rat, bool) {
	switch v.kind {
	case 'q':
		return v.rat, v.rat != nil
	case 'f':
		r := new(big.Rat).SetFloat64(v.f)
		return r, r != nil
	}
	return nil, false
}
