package taxonomy

import (
	"math"
	"testing"
)

/* DEF-JUDGE-ORACLE-R1 回归：verifyOracle 的 judge 侧适用边界。
   冻结契约：
   - oracle 参与前同时检查 q.Answer 与 q.Accept 所有可用参考；任一有效参考
     带单位（含 °）时 verifyOracle 返回 ok=false，交回既有 numeric
     单位/人工路径（不自判 correct/wrong）；用户带单位的既有回退不变。
   - q.Percent==true 时 verifyOracle 一律 ok=false（不猜 0.5 与 50 的缩放），
     由既有 stripPercent/单位逻辑判定；deterministic 路径不变。
   - 无单位、非 percent 的既有 oracle（含 12012 corpus）行为不变。
   - 最终 NaN/Inf 回退 guard 保持（嵌套比较 NaN 由并行 verify 卡修复）。 */

// 探针 1（reviewer 探针复现）：answer 带单位 "3cm"、verify=3、用户答 3。
// 修复前 oracle 对拍 3==3 判 correct/verify，绕过 DEF-6 单位缺失转人工。
func TestJudgeOracleUnitRefBlocksOracle(t *testing.T) {
	q := &Question{Type: "numeric", Answer: "3cm", Verify: "3"}
	if _, ok := verifyOracle("3", q); ok {
		t.Fatalf("answer 带单位时 oracle 不应参与（对拍 3==3 会绕过单位缺失人工复核）")
	}
	// 用户答案落回既有 manual/unsure（单位缺失不自判正确）
	got := JudgeQuestion(q, "3")
	if got.Verdict != VerdictManual || got.Reason != ReasonUnsure {
		t.Errorf("answer=3cm verify=3 user=3 应回退既有 manual/unsure，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 用户带单位的既有回退不变：3cm vs 3cm 走 identical（oracle 未参与）
	got = JudgeQuestion(q, "3cm")
	if got.Verdict != VerdictCorrect || got.Reason != ReasonIdentical {
		t.Errorf("answer=3cm verify=3 user=3cm 应判 correct/identical，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 用户带其它单位仍判 wrong（单位冲突，既有路径）
	got = JudgeQuestion(q, "3m")
	if got.Verdict != VerdictWrong {
		t.Errorf("answer=3cm user=3m 应判 wrong，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 度单位（°）同样是单位语义，oracle 退出
	qDeg := &Question{Type: "numeric", Answer: "90°", Verify: "90"}
	if _, ok := verifyOracle("90", qDeg); ok {
		t.Errorf("answer 带角度单位时 oracle 不应参与")
	}
	if got := JudgeQuestion(qDeg, "90"); got.Verdict != VerdictManual {
		t.Errorf("answer=90° verify=90 user=90 应回退 manual，实际 %s/%s", got.Verdict, got.Reason)
	}
}

// accept 列表带单位同样触发 oracle 退出：参考可用集含单位语义。
func TestJudgeOracleUnitAcceptBlocksOracle(t *testing.T) {
	q := &Question{Type: "numeric", Answer: "甲", Accept: []string{"3cm"}, Verify: "3"}
	if _, ok := verifyOracle("3", q); ok {
		t.Fatalf("accept 带单位时 oracle 不应参与")
	}
	got := JudgeQuestion(q, "3")
	if got.Verdict != VerdictManual || got.Reason != ReasonUnsure {
		t.Errorf("accept=3cm verify=3 user=3 应回退既有 manual/unsure，实际 %s/%s", got.Verdict, got.Reason)
	}
	// accept 与用户单位一致：既有路径判 correct（identical 归一化匹配在前，
	// oracle 退出不影响正确性——answer=甲 走 accept 的 Norm 全等分支）
	got = JudgeQuestion(q, "3cm")
	if got.Verdict != VerdictCorrect {
		t.Errorf("accept=3cm user=3cm 应判 correct，实际 %s/%s", got.Verdict, got.Reason)
	}
	// accept 多项中只有一项带单位也触发（所有可用参考都要检查）
	q2 := &Question{Type: "numeric", Answer: "3", Accept: []string{"4", "5cm"}, Verify: "3"}
	if _, ok := verifyOracle("3", q2); ok {
		t.Errorf("accept 任一项带单位时 oracle 不应参与")
	}
	if got := JudgeQuestion(q2, "3"); got.Verdict != VerdictCorrect || got.Reason != ReasonIdentical {
		t.Errorf("answer=3 accept含5cm user=3 仍应 correct/identical（answer 精确匹配在前），实际 %s/%s", got.Verdict, got.Reason)
	}
}

// 探针 2（reviewer 探针复现）：percent 题的 verify 缩放歧义。
// Percent=true、Answer="50%"、Verify="0.5"、用户答 50%：
// 修复前 oracle 对拍 0.5 vs 0.5（stripPercent 后用户 50% 数值折算为 0.5）
// 判 correct/verify，与既有 percent 路径（答 50% 与参考 50% 比例语义
// 数值不等判 wrong）冲突。修复后 oracle 一律退出。
func TestJudgeOraclePercentBlocksOracle(t *testing.T) {
	// 比例形式 verify（0.5）：oracle 退出，用户 50% 回既有路径
	q := &Question{Type: "numeric", Answer: "50%", Verify: "0.5", Percent: true}
	if _, ok := verifyOracle("50%", q); ok {
		t.Fatalf("percent 题 oracle 不应参与（不猜 0.5 与 50 的缩放）")
	}
	got := JudgeQuestion(q, "50%")
	if got.Verdict != VerdictWrong || got.Reason != ReasonNumericMismatch {
		t.Errorf("Percent=true answer=50%% verify=0.5 user=50%% 应回退既有 wrong/numeric-mismatch，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 用户答 0.5：既有路径数值等价（50% 折算 0.5）判 correct/equivalent，oracle 退出后不变
	got = JudgeQuestion(q, "0.5")
	if got.Verdict != VerdictCorrect || got.Reason != ReasonEquivalent {
		t.Errorf("Percent=true answer=50%% verify=0.5 user=0.5 应回退既有 correct/equivalent，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 用户答 50（不带 %）：既有路径 50 与 50% 数值不等（50 vs 0.5）判
	// wrong/numeric-mismatch（percent 题参考 50% 是比例语义，不放大用户答案）
	got = JudgeQuestion(q, "50")
	if got.Verdict != VerdictWrong {
		t.Errorf("Percent=true answer=50%% user=50 应判 wrong（既有路径），实际 %s/%s", got.Verdict, got.Reason)
	}

	// 放大形式 verify（50）：同样不猜缩放，oracle 退出。
	// answer=甲（文本）时既有 numeric 路径无可用数值参考，percent 题整体
	// 落 manual/unsure（与修复前 percent 无 verify 的行为一致），不再被
	// oracle 自行收敛 correct/wrong。
	qScaled := &Question{Type: "numeric", Answer: "甲", Verify: "50", Percent: true}
	if _, ok := verifyOracle("51", qScaled); ok {
		t.Errorf("percent 题（放大 verify）oracle 不应参与")
	}
	if got := JudgeQuestion(qScaled, "50%"); got.Verdict != VerdictManual {
		t.Errorf("Percent=true answer=甲 verify=50 user=50%% 应回退既有 manual，实际 %s/%s", got.Verdict, got.Reason)
	}
	if got := JudgeQuestion(qScaled, "51"); got.Verdict != VerdictManual {
		t.Errorf("Percent=true answer=甲 verify=50 user=51 应回退既有 manual，实际 %s/%s", got.Verdict, got.Reason)
	}

	// percent 题的 deterministic 正确路径不变：answer 精确/等价仍 correct
	qOk := &Question{Type: "numeric", Answer: "50", Percent: true, Verify: "500*1/(250*4)*100"}
	for _, u := range []string{"50", "50%", "50％"} {
		if got := JudgeQuestion(qOk, u); got.Verdict != VerdictCorrect {
			t.Errorf("percent 题 %q 应判 correct，实际 %s/%s", u, got.Verdict, got.Reason)
		}
	}
	if got := JudgeQuestion(qOk, "51"); got.Verdict != VerdictWrong {
		t.Errorf("percent 题 51 应判 wrong，实际 %s/%s", got.Verdict, got.Reason)
	}
}

// 无单位、非 percent 的既有 oracle 行为不变：普通对拍照常参与。
func TestJudgeOraclePlainUnaffected(t *testing.T) {
	// 普通数值 oracle：现有路径 manual（文本参考）→ oracle 判 correct/wrong
	q := &Question{Type: "numeric", Answer: "甲", Verify: "3+4"}
	if got := JudgeQuestion(q, "7"); got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("普通 oracle 应判 correct/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	if got := JudgeQuestion(q, "8"); got.Verdict != VerdictWrong || got.Reason != ReasonVerify {
		t.Errorf("普通 oracle 应判 wrong/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 分数 verify
	qFrac := &Question{Type: "numeric", Answer: "无关", Verify: "Fraction(1,4)*500*1/(250*4)*100"}
	if got := JudgeQuestion(qFrac, "12.5"); got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("分数 verify 应判 correct/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	if got := JudgeQuestion(qFrac, "51"); got.Verdict != VerdictWrong || got.Reason != ReasonVerify {
		t.Errorf("分数 verify 错值应判 wrong/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 用户带单位的既有回退不变（非 percent、参考不带单位）：3cm 不对拍无单位期望 3
	qUnitUser := &Question{Type: "numeric", Answer: "甲", Verify: "1+2"}
	if _, ok := verifyOracle("3cm", qUnitUser); ok {
		t.Errorf("带单位用户输入 oracle 不应给出结论")
	}
	if got := JudgeQuestion(qUnitUser, "3cm"); got.Verdict != VerdictManual {
		t.Errorf("用户带单位应回退 manual，实际 %s/%s", got.Verdict, got.Reason)
	}
	if got := JudgeQuestion(qUnitUser, "3"); got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("纯数值 3 应判 correct/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 全量数据锚点：12012 verify 题中 numeric+verify 的参考答案自判不被改动
	c := realCatalog(t)
	numericVerify := 0
	for _, bankQ := range c.Questions {
		if bankQ.Type != "numeric" || bankQ.Verify == "" {
			continue
		}
		numericVerify++
		if got := JudgeQuestion(bankQ, bankQ.Answer); got.Verdict != VerdictCorrect {
			t.Errorf("题 %s 参考答案自判 = %s/%s（answer=%q percent=%v verify=%s）",
				bankQ.ID, got.Verdict, got.Reason, bankQ.Answer, bankQ.Percent, bankQ.Verify)
		}
	}
	if numericVerify == 0 {
		t.Fatalf("数据中没有 numeric+verify 题，锚点失效")
	}
	t.Logf("既有 oracle 不受影响：numeric+verify 题 %d 条参考答案全部自判 correct", numericVerify)
}

// 最终 NaN/Inf 回退 guard 保持：verify 结果为非有限浮点时 oracle 必须退出，
// 不能进入 big.Rat 对拍（SetFloat64 返回 nil → Cmp panic）。
func TestJudgeOracleNonFiniteGuard(t *testing.T) {
	for _, verify := range []string{"10.0**400", "1e999", "-1e999"} {
		q := &Question{Type: "numeric", Answer: "甲", Verify: verify}
		if _, ok := verifyOracle("5", q); ok {
			t.Errorf("verify=%s 非有限结果 oracle 不应给出结论", verify)
		}
		if got := JudgeQuestion(q, "5"); got.Verdict != VerdictManual {
			t.Errorf("verify=%s 非有限结果应回退 manual，实际 %s/%s", verify, got.Verdict, got.Reason)
		}
	}
	// NaN 路径探针：浮点函数产生 NaN（log(0)）同样被 guard 拦下
	v, err := (Verifier{}).Eval("log(0)")
	if err != nil {
		t.Fatalf("探针表达式应可求值: %v", err)
	}
	if !math.IsNaN(v.f) {
		t.Fatalf("探针表达式应产生 NaN，实际 kind=%c f=%v", v.kind, v.f)
	}
	qNaN := &Question{Type: "numeric", Answer: "甲", Verify: "log(0)"}
	if _, ok := verifyOracle("5", qNaN); ok {
		t.Errorf("NaN verify 结果 oracle 不应给出结论")
	}
	if got := JudgeQuestion(qNaN, "5"); got.Verdict != VerdictManual {
		t.Errorf("NaN verify 结果应回退 manual，实际 %s/%s", got.Verdict, got.Reason)
	}
}

// 集成：judgeNumeric 的 oracle 挂载点顺序不变——answer/accept 精确与等价
// 匹配优先于 oracle，oracle 只在既有路径未给出 correct 时参与。
func TestJudgeOracleOrderingPreserved(t *testing.T) {
	// answer 精确匹配优先于 verify（即使 verify 值不同）
	q := &Question{Type: "numeric", Answer: "3", Verify: "99"}
	if got := JudgeQuestion(q, "3"); got.Verdict != VerdictCorrect || got.Reason != ReasonIdentical {
		t.Errorf("answer 精确匹配应优先，实际 %s/%s", got.Verdict, got.Reason)
	}
	// accept 等价路径优先于 oracle（identical 归一化匹配在前）
	qAcc := &Question{Type: "numeric", Answer: "甲", Accept: []string{"7"}, Verify: "99"}
	if got := JudgeQuestion(qAcc, "7"); got.Verdict != VerdictCorrect {
		t.Errorf("accept 匹配应优先于 oracle，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 多部答案 count-mismatch 仍在 oracle 之前
	qMulti := &Question{Type: "numeric", Answer: "1,2", Verify: "solve2(1,1,3,1,2,4)"}
	if got := JudgeQuestion(qMulti, "9"); got.Verdict != VerdictWrong || got.Reason != ReasonCountMismatch {
		t.Errorf("多部答案走 count-mismatch，实际 %s/%s", got.Verdict, got.Reason)
	}
	// verify 求值失败静默回退
	qBad := &Question{Type: "numeric", Answer: "5", Verify: "1+"}
	if got := JudgeQuestion(qBad, "6"); got.Verdict != VerdictWrong {
		t.Errorf("verify 求值失败应回退现有路径，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 空 verify 完全不影响
	qNone := &Question{Type: "numeric", Answer: "5"}
	if got := JudgeQuestion(qNone, "5"); got.Verdict != VerdictCorrect {
		t.Errorf("无 verify 题不受影响，实际 %s/%s", got.Verdict, got.Reason)
	}
	// key 解析回归：parseNumeric 的 "n/d" key 形态照常对拍
	qKey := &Question{Type: "numeric", Answer: "无关", Verify: "Fraction(3,4)"}
	if got := JudgeQuestion(qKey, "0.75"); got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("key 解析应判 correct/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
}

// 单位检查辅助：refsCarryUnit 覆盖 Answer/Accept 全部可用参考。
func TestJudgeOracleRefsCarryUnit(t *testing.T) {
	cases := []struct {
		name      string
		answer    string
		accept    []string
		wantCarry bool
	}{
		{"纯数值 answer", "3", nil, false},
		{"answer 带长度单位", "3cm", nil, true},
		{"answer 带中文单位", "12米", nil, true},
		{"answer 带角度", "90°", nil, true},
		{"answer 带度中文", "90度", nil, true},
		{"answer 文本", "甲", nil, false},
		{"accept 带单位", "3", []string{"5cm"}, true},
		{"accept 全纯数值", "3", []string{"4", "5"}, false},
		{"answer 分数无单位", "1/2", nil, false},
		{"answer 百分数", "50%", nil, false},
		// DEF-JUDGE-TEXT-UNIT-R1：文本式单位参考同样命中（数字后紧跟单位词）
		{"answer 文本式质量单位", "5千克苹果", nil, true},
		{"answer 文本式时间单位", "约3小时", nil, true},
		{"accept 文本式单位", "甲", []string{"约3小时"}, true},
		{"answer 文本含数字无单位", "第3章", nil, true}, // 无法可靠解析的文本参考保守阻断
		{"answer 纯文本无数字", "增大", nil, false},
		{"answer 变量赋值纯数值", "x=3", nil, false},
		{"answer 变量赋值带单位", "y=5kg", nil, true},
	}
	for _, c := range cases {
		q := &Question{Type: "numeric", Answer: c.answer, Accept: c.accept}
		if got := refsCarryUnit(q); got != c.wantCarry {
			t.Errorf("%s：refsCarryUnit = %v，期望 %v", c.name, got, c.wantCarry)
		}
	}
}

// DEF-JUDGE-TEXT-UNIT-R1：文本式单位参考（如「5千克苹果」「约3小时」）
// 上一轮 segCarriesUnit 只识别「数+单位」的纯数值段，文本式参考漏检时
// oracle 会拿 verify 数值直接对拍，绕过单位语义。修复后同样阻断。
func TestJudgeOracleTextUnitRefBlocksOracle(t *testing.T) {
	// answer 文本式单位：oracle 退出，用户纯数值回退既有 manual/unsure
	q := &Question{Type: "numeric", Answer: "5千克苹果", Verify: "5"}
	if _, ok := verifyOracle("5", q); ok {
		t.Fatalf("answer=5千克苹果（文本式单位）时 oracle 不应参与（5==5 绕过单位语义）")
	}
	if got := JudgeQuestion(q, "5"); got.Verdict != VerdictManual || got.Reason != ReasonUnsure {
		t.Errorf("answer=5千克苹果 verify=5 user=5 应回退既有 manual/unsure，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 用户整串一致仍走 identical（既有回退不变）
	if got := JudgeQuestion(q, "5千克苹果"); got.Verdict != VerdictCorrect || got.Reason != ReasonIdentical {
		t.Errorf("user=5千克苹果 应判 correct/identical，实际 %s/%s", got.Verdict, got.Reason)
	}
	// accept 文本式单位同样阻断
	qAcc := &Question{Type: "numeric", Answer: "甲", Accept: []string{"约3小时"}, Verify: "3"}
	if _, ok := verifyOracle("3", qAcc); ok {
		t.Errorf("accept=约3小时（文本式单位）时 oracle 不应参与")
	}
	if got := JudgeQuestion(qAcc, "3"); got.Verdict != VerdictManual {
		t.Errorf("accept=约3小时 verify=3 user=3 应回退既有 manual，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 普通纯文本答案（无数字、无单位）不阻断：oracle 照常参与
	qText := &Question{Type: "numeric", Answer: "甲", Verify: "3+4"}
	if got := JudgeQuestion(qText, "7"); got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("answer=甲（纯文本）oracle 应照常 correct/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	if got := JudgeQuestion(qText, "8"); got.Verdict != VerdictWrong || got.Reason != ReasonVerify {
		t.Errorf("answer=甲（纯文本）oracle 应照常 wrong/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	// 纯数值参考不受影响：oracle 照常
	qNum := &Question{Type: "numeric", Answer: "3", Accept: []string{"4"}, Verify: "3"}
	if got := JudgeQuestion(qNum, "3"); got.Verdict != VerdictCorrect || got.Reason != ReasonIdentical {
		t.Errorf("纯数值 answer oracle 前仍 identical，实际 %s/%s", got.Verdict, got.Reason)
	}
	qNum2 := &Question{Type: "numeric", Answer: "无关", Verify: "Fraction(1,4)"}
	if got := JudgeQuestion(qNum2, "0.25"); got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("纯数值 verify oracle 应照常 correct/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
}
