package taxonomy

import (
	"regexp"
	"strings"
	"testing"
)

/* 全量题库不变量与判题回环：
   - 每个知识点都有足够的题、题号规范、选项完整；
   - 参考答案自判为 correct（choice / numeric / short / legacy）；
   - percent 题接受 / 不接受；
   - verify 表达式可在受限求值器中运行（推导式/生成器/科学计数法/嵌套 Fraction 全支持）。
   数据基线：12579 题 / 10 学科（catalog_data_test.go）。 */

var (
	reQID     = regexp.MustCompile(`^([a-z]+_[a-z0-9]+)_q([1-9]\d*)$`)
	reNumAns  = regexp.MustCompile(`^[+-]?\d+(\.\d+)?$|^[+-]?\d+/\d+$`)
	validDiff = map[string]bool{"basic": true, "standard": true, "challenge": true, "advanced": true}
	validType = map[string]bool{"choice": true, "numeric": true, "short": true, "self": true}
)

func TestBankQuestionInvariants(t *testing.T) {
	c := realCatalog(t)
	seen := map[string]bool{}
	dupes := 0
	typeCount := map[string]int{}
	topicsWithBasic, topicsWithStandard := 0, 0
	for _, q := range c.Questions {
		if _, ok := c.ByID[q.TopicID]; !ok {
			t.Errorf("题目 %s 的 topicId %s 不存在", q.ID, q.TopicID)
			continue
		}
		m := reQID.FindStringSubmatch(q.ID)
		if m == nil {
			t.Errorf("题号不规范：%s", q.ID)
			continue
		}
		if m[1] != q.TopicID {
			t.Errorf("题号 %s 与 topicId %s 不符", q.ID, q.TopicID)
		}
		if seen[q.ID] {
			dupes++
			if dupes <= 5 {
				t.Errorf("重复题号 %s", q.ID)
			}
		}
		seen[q.ID] = true
		if !validType[q.Type] {
			t.Errorf("题 %s 类型非法 %q", q.ID, q.Type)
		}
		if !validDiff[q.Difficulty] {
			t.Errorf("题 %s 难度非法 %q", q.ID, q.Difficulty)
		}
		typeCount[q.Type]++
		if strings.TrimSpace(q.Prompt) == "" {
			t.Errorf("题 %s 缺少题干", q.ID)
		}
		if strings.TrimSpace(q.Answer) == "" && q.Type != "self" {
			t.Errorf("题 %s 缺少参考答案", q.ID)
		}
		if q.Type == "choice" {
			if len(q.Choices) < 3 || len(q.Choices) > 4 {
				t.Errorf("选择题 %s 选项数 = %d", q.ID, len(q.Choices))
			}
			keys := map[string]bool{}
			hit := false
			for _, ch := range q.Choices {
				if keys[ch.Key] {
					t.Errorf("选择题 %s 选项 key 重复 %s", q.ID, ch.Key)
				}
				keys[ch.Key] = true
				if strings.TrimSpace(ch.Text) == "" {
					t.Errorf("选择题 %s 选项 %s 文本为空", q.ID, ch.Key)
				}
				if ch.Key == q.Answer {
					hit = true
				}
			}
			if !hit {
				t.Errorf("选择题 %s 答案 %q 不在选项 key 中", q.ID, q.Answer)
			}
		}
		if q.Type == "numeric" && !q.Percent && !reNumAns.MatchString(strings.TrimSpace(q.Answer)) {
			// 数值题也允许文本答案（如「五」），这里只统计不判错
			t.Logf("数值题 %s 的答案不是纯数值：%q（按字符串比对）", q.ID, q.Answer)
		}
		if q.Verify != "" && len(q.Verify) > maxExprLen {
			t.Errorf("题 %s 的 verify 超过长度上限", q.ID)
		}
	}
	// 每个知识点至少 3 题，且含 basic 与 standard 难度（保证回退选题有空间）
	tooFew := 0
	for _, tp := range c.Topics {
		qs := c.Bank[tp.ID]
		if len(qs) < 3 {
			tooFew++
			if tooFew <= 5 {
				t.Errorf("知识点 %s（%v）只有 %d 题", tp.ID, tp.Grade, len(qs))
			}
		}
		hasBasic, hasStd := false, false
		for _, q := range qs {
			switch q.Difficulty {
			case "basic":
				hasBasic = true
			case "standard":
				hasStd = true
			}
		}
		if hasBasic {
			topicsWithBasic++
		}
		if hasStd {
			topicsWithStandard++
		}
	}
	if topicsWithBasic != len(c.Topics) {
		t.Errorf("有 basic 题的知识点 %d / %d", topicsWithBasic, len(c.Topics))
	}
	if topicsWithStandard != len(c.Topics) {
		t.Errorf("有 standard 题的知识点 %d / %d", topicsWithStandard, len(c.Topics))
	}
	// 题型分布（数据基线）
	if typeCount["choice"] != 9701 || typeCount["numeric"] != 2849 || typeCount["short"] != 23 || typeCount["self"] != 6 {
		t.Errorf("题型分布 = %v，期望 choice 9701 / numeric 2849 / short 23 / self 6", typeCount)
	}
	// HasBank：Load 读到真实题库后必须为 true
	if !c.HasBank {
		t.Errorf("Load 后 HasBank 应为 true（catalog.go Load 末尾 c.HasBank = fromFile > 0）")
	}
}

func TestBankAnswersJudgeCorrect(t *testing.T) {
	c := realCatalog(t)
	fails := 0
	for _, q := range c.Questions {
		got := JudgeQuestion(q, q.Answer)
		want := VerdictCorrect
		if q.Type == "self" {
			want = VerdictManual // 自评题永远 manual
		}
		if got.Verdict != want {
			fails++
			if fails <= 10 {
				t.Errorf("题 %s（%s，percent=%v）以参考答案作答判为 %s/%s，期望 %s；答案=%q 选项=%v",
					q.ID, q.Type, q.Percent, got.Verdict, got.Reason, want, q.Answer, q.Choices)
			}
		}
	}
}

func TestBankPercentVariants(t *testing.T) {
	c := realCatalog(t)
	n := 0
	for _, q := range c.Questions {
		if !q.Percent {
			continue
		}
		n++
		for _, u := range []string{q.Answer + "%", q.Answer + "％", q.Answer + " %"} {
			if got := JudgeQuestion(q, u); got.Verdict != VerdictCorrect {
				t.Errorf("percent 题 %s 不接受 %q：%s/%s", q.ID, u, got.Verdict, got.Reason)
			}
		}
	}
	if n == 0 {
		t.Fatalf("数据中没有 percent 题，用例失效")
	}
}

func TestBankChoiceLetterAnswer(t *testing.T) {
	c := realCatalog(t)
	checked := 0
	for _, q := range c.Questions {
		if q.Type != "choice" {
			continue
		}
		checked++
		letter := strings.ToLower(q.Answer)
		if got := JudgeQuestion(q, letter); got.Verdict != VerdictCorrect {
			t.Errorf("选择题 %s 不接受小写字母 %q：%s/%s", q.ID, letter, got.Verdict, got.Reason)
		}
		if got := JudgeQuestion(q, "（"+q.Answer+"）"); got.Verdict != VerdictCorrect {
			t.Errorf("选择题 %s 不接受全角括号 %q：%s/%s", q.ID, q.Answer, got.Verdict, got.Reason)
		}
		// 选一个不同 key，必须判错
		for _, ch := range q.Choices {
			if ch.Key != q.Answer {
				if got := JudgeQuestion(q, ch.Key); got.Verdict != VerdictWrong {
					t.Errorf("选择题 %s 对错误选项 %s 判为 %s", q.ID, ch.Key, got.Verdict)
				}
				break
			}
		}
	}
	if checked != 9701 {
		t.Errorf("选择题数 = %d，期望 9701", checked)
	}
}

func TestBankVerifyExpressions(t *testing.T) {
	c := realCatalog(t)
	v := Verifier{}
	total, ok := 0, 0
	for _, q := range c.Questions {
		if q.Verify == "" {
			continue
		}
		total++
		_, err := v.Eval(q.Verify)
		if err == nil {
			ok++
			continue
		}
		t.Errorf("verify 表达式无法求值（%s）：%v\n  %s", q.ID, err, q.Verify)
	}
	if total < 500 {
		t.Errorf("带 verify 的题数 = %d，期望至少 500（当前数据基线约 12000）", total)
	}
	// 数据基线：全部 verify 表达式可求值（上游核验脚本 auto_verified 12012 / mismatch 0）。
	if ok != 12012 || total != 12012 {
		t.Errorf("verify 可求值 = %d / %d，期望 12012 / 12012（全量可求值）", ok, total)
	}
}

func TestBankVerifySelfConsistency(t *testing.T) {
	// 抽样：同一表达式求值两次结果一致，且数值题 verify 与参考答案不矛盾的概率很高。
	// 这里只做确定性检查（与 verify_test.go 的单例互补，覆盖真实数据的前 500 条）。
	c := realCatalog(t)
	v := Verifier{}
	n := 0
	for _, q := range c.Questions {
		if q.Verify == "" {
			continue
		}
		a, err := v.Eval(q.Verify)
		if err != nil {
			continue
		}
		b, err := v.Eval(q.Verify)
		if err != nil || !sameVValue(a, b) {
			t.Errorf("verify 两次求值不一致（%s）", q.ID)
		}
		n++
		if n >= 500 {
			break
		}
	}
	if n < 500 {
		t.Errorf("可求值样本不足：%d", n)
	}
}

func sameVValue(a, b vValue) bool {
	if a.kind != b.kind {
		return false
	}
	switch a.kind {
	case 'q':
		if a.rat == nil || b.rat == nil {
			return a.rat == b.rat
		}
		return a.rat.Cmp(b.rat) == 0
	case 's':
		return a.s == b.s
	case 'b':
		return a.b == b.b
	case 'l':
		if len(a.list) != len(b.list) {
			return false
		}
		for i := range a.list {
			if !sameVValue(a.list[i], b.list[i]) {
				return false
			}
		}
		return true
	default:
		return a.rat != nil && b.rat != nil && a.rat.Cmp(b.rat) == 0
	}
}

func TestBankDiagnosticAnswersPerTopic(t *testing.T) {
	c := realCatalog(t)
	bad := 0
	for _, tp := range c.Topics {
		dq := tp.DiagnosticQuestion
		if strings.TrimSpace(dq.Question) == "" || strings.TrimSpace(dq.Answer) == "" {
			bad++
			if bad <= 5 {
				t.Errorf("知识点 %s 缺少诊断题/答案", tp.ID)
			}
			continue
		}
		// 诊断题是 legacy 类型，按 JudgeAnswer 自判应正确（文本完全相同时）
		if got := JudgeAnswer(dq.Answer, dq.Answer); got.Verdict != VerdictCorrect {
			t.Errorf("知识点 %s 的诊断答案自判 = %s", tp.ID, got.Verdict)
		}
	}
}

func TestBankPickQuestionUsesBank(t *testing.T) {
	c := realCatalog(t)
	gt := mustGradeTarget(t, c, "七上", SubjectMath)
	s := mustSession(t, c, gt.IDs, &CreateSessionOptions{Mode: ModeScreen, Seed: "bank"})
	for i := 0; i < 20; i++ {
		id := c.NextQuestion(s)
		if id == "" {
			break
		}
		item := c.NextItem(s)
		if item == nil || item.Question == nil {
			t.Fatalf("NextItem 为空（%s）", id)
		}
		inBank := false
		for _, q := range c.Bank[id] {
			if q.ID == item.Question.ID {
				inBank = true
				break
			}
		}
		if !inBank {
			t.Errorf("出题 %s 不属于知识点 %s 的题库", item.Question.ID, id)
		}
		if item.Attempt != len(s.Attempts[id])+1 {
			t.Errorf("%s 的 attempt = %d，已答 %d 次", id, item.Attempt, len(s.Attempts[id]))
		}
		ns, err := c.ApplyAnswer(s, id, ResultCorrect)
		if err != nil {
			t.Fatal(err)
		}
		s = ns
	}
}
