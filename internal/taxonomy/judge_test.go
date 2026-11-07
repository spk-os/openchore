package taxonomy

import "testing"

/* 判题单测：移植上游 tests/test_engine.js 中 judge 相关用例。 */

func chk(t *testing.T, name string, q *Question, user, wantVerdict string) {
	t.Helper()
	got := JudgeQuestion(q, user)
	if got.Verdict != wantVerdict {
		t.Errorf("%s：JudgeQuestion(%q) = %s(%s)，期望 %s", name, user, got.Verdict, got.Reason, wantVerdict)
	}
}

func chkAnswer(t *testing.T, name, user, ref, want string) {
	t.Helper()
	if got := JudgeAnswer(user, ref); got.Verdict != want {
		t.Errorf("%s：JudgeAnswer(%q,%q) = %s(%s)，期望 %s", name, user, ref, got.Verdict, got.Reason, want)
	}
}

func TestJudgeNormalizeText(t *testing.T) {
	cases := map[string]string{
		" 答案：3 ": "3",
		"答:3":    "3",
		"３":      "3",
		"1 度":    "1°",
		"x²":     "x^2",
		"1、2":    "1,2",
		"1。5":    "1.5",
		"2×3":    "2*3",
		"6÷2":    "6/2",
		"1≦2":    "1≤2",
		"1<=2":   "1≤2",
		"[1,2]":  "(1,2)",
		"{a}":    "(a)",
		"1 1/2":  "1又1/2", // 带分数：整数部分保留（与上游 normalizeText 一致）
		"“AB”":   `"ab"`,
		"A 或者 B": "a或b",
		"3.":     "3",
		"1,000":  "1000",
		"1,0000": "1,0000", // 不是合法千分位，保留
		"Ｘ＝３":    "x=3",
		"50％":    "50%",
		"－３":     "-3",
		"A−B":    "a-b",
		"3 º":    "3°",
	}
	for in, want := range cases {
		if got := NormalizeText(in); got != want {
			t.Errorf("NormalizeText(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func choiceQ() *Question {
	return &Question{
		ID: "q", Type: "choice", Answer: "B",
		Choices: []Choice{{Key: "A", Text: "-3"}, {Key: "B", Text: "3"}, {Key: "C", Text: "1/3"}, {Key: "D", Text: "9"}},
	}
}

func TestJudgeChoice(t *testing.T) {
	q := choiceQ()
	chk(t, "小写", q, "b", VerdictCorrect)
	chk(t, "带空格", q, " b ", VerdictCorrect)
	chk(t, "全角括号", q, "（B）", VerdictCorrect)
	chk(t, "点号", q, "B.", VerdictCorrect)
	chk(t, "选项文字", q, "3", VerdictCorrect)
	chk(t, "错误选项", q, "A", VerdictWrong)
	chk(t, "错误选项文字", q, "1/3", VerdictWrong)
	chk(t, "不存在的选项", q, "E", VerdictManual)
	chk(t, "乱写", q, "不知道", VerdictManual)
	chk(t, "空", q, "", VerdictManual)
	// 冻结语义：字母格式只匹配单个字母；「-3」这类数字答案走选项文字匹配，
	// 命中选项文字（含全角）即按该选项判——命中 A 而答案为 B → wrong。
	fw := choiceQ()
	fw.Choices[0].Text = "－３"
	chk(t, "负数选项文字（全角）", fw, "-3", VerdictWrong)
	fw2 := choiceQ()
	fw2.Choices[1].Text = "２５"
	chk(t, "全角选项文字", fw2, "25", VerdictCorrect)
	// 数字答案不命中任何选项文字时保持 manual（不误当选项 A）
	fw3 := choiceQ()
	fw3.Choices[0].Text = "5"
	chk(t, "无匹配选项的数字答案", fw3, "-3", VerdictManual)
}

func TestJudgeNumeric(t *testing.T) {
	num := func(ans string, accept ...string) *Question {
		return &Question{ID: "n", Type: "numeric", Answer: ans, Accept: accept}
	}
	chk(t, "相同", num("-1/2"), "-0.5", VerdictCorrect)
	chk(t, "带分数", num("-1/2"), "-1又1/2", VerdictWrong)
	chk(t, "小数等价", num("1/2"), "0.5", VerdictCorrect)
	chk(t, "分数错", num("1/2"), "1/3", VerdictWrong)
	chk(t, "accept 列表", num("1/2", "0.5"), "0.5", VerdictCorrect)
	chk(t, "负号变体", num("-3"), "－３", VerdictCorrect)
	chk(t, "空", num("3"), "", VerdictManual)
	q := num("1,2")
	chk(t, "无序列表（逆序）", &Question{ID: "u", Type: "numeric", Answer: "1,2", Unordered: true}, "2,1", VerdictCorrect)
	chk(t, "有序列表（逆序）", q, "2,1", VerdictWrong)
	chk(t, "有序列表（正序）", q, "1,2", VerdictCorrect)
	chk(t, "有序列表缺项", q, "1", VerdictWrong)
	chk(t, "文本答案相同", num("五"), "五", VerdictCorrect)
	chk(t, "文本答案不同", num("五"), "六", VerdictManual)
}

func TestJudgeNumericPercent(t *testing.T) {
	q := &Question{ID: "p", Type: "numeric", Answer: "50", Percent: true}
	for _, u := range []string{"50", "50%", "50 %", "50％"} {
		chk(t, "percent 接受 "+u, q, u, VerdictCorrect)
	}
	chk(t, "percent 不接受 1/2", q, "0.5", VerdictWrong)
	chk(t, "percent 错值", q, "51", VerdictWrong)
}

func TestJudgeShortAndSelf(t *testing.T) {
	q := &Question{ID: "s", Type: "short", Answer: "平行四边形", Accept: []string{"paraphrase"}}
	chk(t, "相同", q, " 平行四边形 ", VerdictCorrect)
	chk(t, "accept", q, "Paraphrase", VerdictCorrect)
	chk(t, "不同", q, "梯形", VerdictManual)
	chk(t, "空", q, "", VerdictManual)
	self := &Question{ID: "self", Type: "self", Answer: "x"}
	chk(t, "self 永远自评", self, "x", VerdictManual)
}

func TestJudgeLegacyFallsBackToAnswer(t *testing.T) {
	q := &Question{ID: "l", Type: legacyQuestionType, Answer: "3"}
	chk(t, "legacy 数值等价", q, "3.0", VerdictCorrect)
	q2 := &Question{ID: "l", Type: legacyQuestionType, Answer: "0.5"}
	chk(t, "legacy 等价分数", q2, "1/2", VerdictCorrect)
}

func TestJudgeAnswers(t *testing.T) {
	chkAnswer(t, "分数小数", "0.5", "1/2", VerdictCorrect)
	chkAnswer(t, "带分数中文（已写又）", "1又1/2", "1又1/2", VerdictCorrect)
	chkAnswer(t, "百分数", "50%", "1/2", VerdictCorrect)
	chkAnswer(t, "角度", "90 度", "90°", VerdictCorrect)
	chkAnswer(t, "千分位", "1,000", "1000", VerdictCorrect)
	chkAnswer(t, "带单位中文", "12米", "12 m", VerdictCorrect)
	chkAnswer(t, "带单位全角", "25 平方米", "25 m²", VerdictCorrect)
	chkAnswer(t, "单位不同", "12 cm", "12 m", VerdictWrong)
	chkAnswer(t, "有单位无单位（DEF-6：单位缺失不自判正确，转人工）", "12", "12 m", VerdictManual)
	chkAnswer(t, "面积单位", "25m^2", "25 平方米", VerdictCorrect)
	chkAnswer(t, "无序集合", "2,1", "1,2", VerdictCorrect)
	chkAnswer(t, "变量赋值", "x=3", "x=3", VerdictCorrect)
	chkAnswer(t, "变量不同（DEF-6：单变量名必须一致）", "x=3", "y=3", VerdictWrong)
	chkAnswer(t, "变量值错", "x=3", "x=4", VerdictWrong)
	chkAnswer(t, "坐标", "(1,2)", "(1,2)", VerdictCorrect)
	chkAnswer(t, "文本不同", "三角形", "正方形", VerdictManual)
	chkAnswer(t, "空参考", "3", "", VerdictManual)
	chkAnswer(t, "同一文本", "答案：3", "3", VerdictCorrect)
}

func TestJudgeNoPanicOnWeirdInput(t *testing.T) {
	refs := []string{"3", "1/2", "a,b", "x=3", "50%", "12 m", "五", ""}
	users := []string{"", ".", ",", "()", "((", "x=", "1/0", "-", "+", "0%", "%", " ', ", "1,2,3", "非常长的答案" + repeat("啊", 200)}
	for _, r := range refs {
		for _, u := range users {
			for _, typ := range []string{"numeric", "short", "legacy"} {
				q := &Question{Type: typ, Answer: r}
				v := JudgeQuestion(q, u).Verdict
				if v != VerdictCorrect && v != VerdictWrong && v != VerdictManual {
					t.Errorf("非法 verdict %q（type=%s ref=%q user=%q）", v, typ, r, u)
				}
			}
		}
	}
}
