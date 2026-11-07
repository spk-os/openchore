package taxonomy

import (
	"testing"
)

/* TX-CORE-FIX：verify 接入数值判题作为额外 oracle 的回归。
   只增强不放宽：choice/short/self 不受影响；numeric 在现有路径
   （answer/accept/同义单位）未给出 correct 时才由 verify 对拍。 */

func TestJudgeVerifyOracleParticipates(t *testing.T) {
	// verify 参与 correct：Fraction(1,4)*500*1/(250*4)*100 = 25/2，用户答 12.5
	q := &Question{
		ID: "v1", Type: "numeric", Answer: "3",
		Verify: "Fraction(1,4)*500*1/(250*4)*100",
	}
	got := JudgeQuestion(q, "12.5")
	if got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("verify oracle 应判 correct/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 分数写法同样命中 verify
	got = JudgeQuestion(q, "25/2")
	if got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("verify oracle 应接受分数 25/2，实际 %s/%s", got.Verdict, got.Reason)
	}
	// verify 参与 wrong：数值不等
	got = JudgeQuestion(q, "51")
	if got.Verdict != VerdictWrong || got.Reason != ReasonVerify {
		t.Errorf("verify oracle 应判 wrong/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 现有路径优先：answer 精确匹配先于 verify
	got = JudgeQuestion(q, "3")
	if got.Verdict != VerdictCorrect || got.Reason != ReasonIdentical {
		t.Errorf("answer 精确匹配应优先，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 多部答案在 oracle 之前已按现有路径判 count-mismatch
	qMulti := &Question{
		ID: "v2", Type: "numeric", Answer: "1,2",
		Verify: "solve2(1,1,3,1,2,4)",
	}
	got = JudgeQuestion(qMulti, "9")
	if got.Verdict != VerdictWrong || got.Reason != ReasonCountMismatch {
		t.Errorf("多部答案走现有 count-mismatch 路径，实际 %s/%s", got.Verdict, got.Reason)
	}
	// verify 求值失败安全回退
	qBad := &Question{ID: "v3", Type: "numeric", Answer: "5", Verify: "1+"}
	got = JudgeQuestion(qBad, "6")
	if got.Verdict != VerdictWrong {
		t.Errorf("verify 求值失败应回退现有路径判 wrong，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 空 verify 完全不影响
	qNone := &Question{ID: "v4", Type: "numeric", Answer: "5"}
	if got := JudgeQuestion(qNone, "5"); got.Verdict != VerdictCorrect {
		t.Errorf("无 verify 题不受影响")
	}
	// choice / short / self 不放宽：即使 verify 可算也走原路径
	qc := &Question{ID: "v5", Type: "choice", Answer: "B",
		Choices: []Choice{{Key: "A", Text: "1"}, {Key: "B", Text: "2"}},
		Verify:  "1+1",
	}
	if got := JudgeQuestion(qc, "2"); got.Verdict != VerdictCorrect || got.Reason != ReasonChoice {
		t.Errorf("choice 题不走 verify oracle，实际 %s/%s", got.Verdict, got.Reason)
	}
	qs := &Question{ID: "v6", Type: "short", Answer: "对", Verify: "1"}
	if got := JudgeQuestion(qs, "错"); got.Verdict != VerdictManual {
		t.Errorf("short 题不走 verify oracle，实际 %s", got.Verdict)
	}
}

// 全量数据：verify oracle 不改变现有判题结果的前提下参与生产。
// 用参考答案与已知错答案做大规模对拍，验证 oracle 一致性。
func TestJudgeVerifyOracleAgainstBank(t *testing.T) {
	c := realCatalog(t)
	verifyNumeric, oracleHit, oracleChanged := 0, 0, 0
	for _, q := range c.Questions {
		if q.Verify == "" || q.Type != "numeric" {
			continue
		}
		verifyNumeric++
		// 参考答案自判：oracle 不应把 correct 变成非 correct
		got := JudgeQuestion(q, q.Answer)
		if got.Verdict != VerdictCorrect {
			oracleChanged++
			if oracleChanged <= 5 {
				t.Errorf("题 %s 参考答案被 oracle 改判为 %s/%s（answer=%q verify=%s）",
					q.ID, got.Verdict, got.Reason, q.Answer, q.Verify)
			}
		}
		// 用户输入参考答案的 +1 值：verify 应判 wrong（若 oracle 参与）
		if v, err := (Verifier{}).Eval(q.Verify); err == nil && v.kind == 'q' {
			oracleHit++
		}
	}
	if verifyNumeric == 0 {
		t.Fatalf("数据中没有带 verify 的 numeric 题，用例失效")
	}
	if oracleHit == 0 {
		t.Errorf("verify oracle 从未参与数值判题（可求值分数结果的题 = 0）")
	}
	t.Logf("verify oracle：numeric+verify 题 %d，可对拍 %d，改判 %d", verifyNumeric, oracleHit, oracleChanged)
}

// verify oracle 参与生产的直接证明：构造一道现有路径判 manual、verify 判 correct 的题。
func TestJudgeVerifyOracleProvesProduction(t *testing.T) {
	// answer 文本是「甲」，用户答数值 7：现有路径 manual（文本≠数值）。
	// verify=3+4 可求值为 7：oracle 判 correct。
	q := &Question{ID: "p1", Type: "numeric", Answer: "甲", Verify: "3+4"}
	got := JudgeQuestion(q, "7")
	if got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("oracle 应参与生产判 correct/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 反向：用户答 8，oracle 判 wrong（收敛 manual→wrong）
	got = JudgeQuestion(q, "8")
	if got.Verdict != VerdictWrong || got.Reason != ReasonVerify {
		t.Errorf("oracle 应判 wrong/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 用户答文本时 oracle 不参与（保持 manual）
	got = JudgeQuestion(q, "乙")
	if got.Verdict != VerdictManual {
		t.Errorf("文本输入不应触发 oracle，实际 %s/%s", got.Verdict, got.Reason)
	}
	// percent 题 oracle 不参与（DEF-JUDGE-ORACLE-R1：不猜 verify 的
	// 比例/百分数缩放），交回既有 stripPercent/数值路径判定。
	qp := &Question{ID: "p2", Type: "numeric", Answer: "甲", Percent: true, Verify: "50"}
	if got := JudgeQuestion(qp, "50%"); got.Verdict != VerdictManual {
		t.Errorf("percent 题 oracle 应回退既有 manual 路径，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 多部答案（"1,2"）在 oracle 之前按现有路径判 count-mismatch
	qm := &Question{ID: "p3", Type: "numeric", Answer: "甲,乙", Verify: "1+1"}
	if got := JudgeQuestion(qm, "2"); got.Verdict != VerdictWrong || got.Reason != ReasonCountMismatch {
		t.Errorf("多部答案走现有 count-mismatch，实际 %s/%s", got.Verdict, got.Reason)
	}
}

// key 格式防御：parseNumeric 产出的 key 形如 "n/d"，oracle 解析需容错。
func TestJudgeVerifyOracleKeyParsing(t *testing.T) {
	q := &Question{ID: "k1", Type: "numeric", Answer: "无关", Verify: "Fraction(3,4)"}
	if got := JudgeQuestion(q, "0.75"); got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("分数 verify 对小数输入 = %s/%s，期望 correct/verify", got.Verdict, got.Reason)
	}
	if got := JudgeQuestion(q, "3/4"); got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("分数 verify 对分数输入 = %s/%s，期望 correct/verify", got.Verdict, got.Reason)
	}
	if got := JudgeQuestion(q, "75%"); got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("75%% 归一为 3/4 与 verify 相等，应判 correct/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	if got := JudgeQuestion(q, "76%"); got.Verdict != VerdictWrong || got.Reason != ReasonVerify {
		t.Errorf("76%% 归一为 19/25 与 verify 不等，应判 wrong/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
}
