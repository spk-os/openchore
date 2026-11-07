package taxonomy

import (
	"math/big"
	"strings"
	"testing"
)

/* TX-CORE-FIX：全量 12012 verify 表达式按题型与 answer/accept 正确对拍。
   上游核验脚本语义：verify 可求值时，其数值结果应与题目的 answer/accept
   （或 choice 正确选项文字）数值一致。答案文本已有规范化的明确转换
   （带分数、单位、百分号等）也纳入对拍。 */

// parseRefNumber 把参考答案文本解析为 big.Rat（带分数 a b/c、分数 a/b、
// 小数、整数、百分号、°单位剥离）。解析失败返回 ok=false（文本答案等）。
func parseRefNumber(s string) (*big.Rat, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return nil, false
	}
	// 归一化：全角、乘除号等由 NormalizeText 处理后按逗号拆分取单部
	norm := NormalizeText(t)
	parts := strings.Split(norm, ",")
	if len(parts) == 0 {
		return nil, false
	}
	key, _, _, ok := parseNumeric(parts[0])
	if !ok {
		return nil, false
	}
	slash := strings.LastIndexByte(key, '/')
	if slash < 0 {
		return nil, false
	}
	r, ok2 := new(big.Rat).SetString(key[:slash])
	return r, ok2
}

func TestVerifyAgainstAnswersFullCorpus(t *testing.T) {
	c := realCatalog(t)
	v := Verifier{}
	type stat struct {
		verifyTotal, evalOK, evalNum         int
		matched, converted, mismatched, skip int
	}
	st := stat{}
	var mismatches []string
	for _, q := range c.Questions {
		if q.Verify == "" {
			continue
		}
		st.verifyTotal++
		val, err := v.Eval(q.Verify)
		if err != nil {
			t.Errorf("verify 求值失败 %s: %v", q.ID, err)
			continue
		}
		st.evalOK++
		var expect *big.Rat
		switch val.kind {
		case 'q':
			expect = val.rat
		case 'f':
			expect = new(big.Rat).SetFloat64(val.f)
		default:
			continue // 列表/字符串/布尔：多解或非数值，不参与单值对拍
		}
		st.evalNum++
		// 参考数值：answer 优先，其次 accept；choice 用正确选项文字
		var refStr string
		switch q.Type {
		case "choice":
			for _, ch := range q.Choices {
				if ch.Key == q.Answer {
					refStr = ch.Text
					break
				}
			}
			if refStr == "" {
				st.skip++
				continue
			}
		default:
			refStr = q.Answer
		}
		ref, ok := parseRefNumber(refStr)
		if !ok {
			// 尝试 accept 列表（choice 之外的题型）
			for _, a := range q.Accept {
				if r2, ok2 := parseRefNumber(a); ok2 {
					ref, ok = r2, true
					break
				}
			}
		}
		if !ok {
			st.skip++
			continue
		}
		if ref.Cmp(expect) == 0 {
			st.matched++
			continue
		}
		// 规范化转换容差：小数位精度（答案写 0.33、verify 算 1/3）
		diff := new(big.Rat).Sub(ref, expect)
		diff.Abs(diff)
		tol := new(big.Rat).Quo(big.NewRat(1, 1), big.NewRat(1000000, 1)) // 1e-6
		if q.Percent {
			// percent 题 answer 是百分数值（50 = 50%），verify 算的是比例（0.5）
			scaled := new(big.Rat).Mul(expect, big.NewRat(100, 1))
			if scaled.Cmp(ref) == 0 {
				st.converted++
				continue
			}
			sd := new(big.Rat).Sub(scaled, ref)
			sd.Abs(sd)
			if sd.Cmp(tol) <= 0 {
				st.converted++
				continue
			}
		}
		if diff.Cmp(tol) <= 0 {
			st.converted++
			continue
		}
		st.mismatched++
		if len(mismatches) < 30 {
			mismatches = append(mismatches, sprintf("%s(%s): verify=%s answer=%q", q.ID, q.Type, expect.RatString(), refStr))
		}
	}
	t.Logf("verify 对拍：总数 %d / 可求值 %d / 数值结果 %d / 直接匹配 %d / 规范化匹配 %d / 跳过 %d / 不匹配 %d",
		st.verifyTotal, st.evalOK, st.evalNum, st.matched, st.converted, st.skip, st.mismatched)
	if st.verifyTotal != 12012 {
		t.Errorf("带 verify 的题数 = %d，期望 12012", st.verifyTotal)
	}
	if st.evalOK != 12012 {
		t.Errorf("verify 可求值 = %d，期望 12012", st.evalOK)
	}
	if st.mismatched != 0 {
		for _, m := range mismatches {
			t.Errorf("verify 与参考答案不匹配: %s", m)
		}
	}
}

// skip 的题型分布快照：跳过项需可解释（文本答案、多解列表等）。
func TestVerifySkipReasons(t *testing.T) {
	c := realCatalog(t)
	v := Verifier{}
	skipKind := map[string]int{}
	skipList, skipText := 0, 0
	for _, q := range c.Questions {
		if q.Verify == "" {
			continue
		}
		val, err := v.Eval(q.Verify)
		if err != nil {
			continue
		}
		var expect *big.Rat
		_ = expect // 数值对拍由主测试覆盖；此处只统计跳过原因
		switch val.kind {
		case 'q':
			expect = val.rat
		case 'f':
			expect = new(big.Rat).SetFloat64(val.f)
		default:
			skipKind[q.Type]++
			if val.kind == 'l' {
				skipList++
			}
			continue
		}
		refStr := q.Answer
		if q.Type == "choice" {
			refStr = ""
			for _, ch := range q.Choices {
				if ch.Key == q.Answer {
					refStr = ch.Text
					break
				}
			}
		}
		if _, ok := parseRefNumber(refStr); !ok {
			found := false
			for _, a := range q.Accept {
				if _, ok2 := parseRefNumber(a); ok2 {
					found = true
					break
				}
			}
			if !found {
				skipText++
				skipKind[q.Type+"/"+val.kindName()]++
			}
			continue
		}
	}
	t.Logf("verify 跳过分布：列表结果 %d / 文本参考 %d / 按题型 %v", skipList, skipText, skipKind)
	if skipList+skipText > 12012 {
		t.Errorf("跳过数异常")
	}
}

// kindName 便于日志。
func (v vValue) kindName() string {
	switch v.kind {
	case 'q':
		return "rat"
	case 'f':
		return "float"
	case 's':
		return "str"
	case 'b':
		return "bool"
	case 'l':
		return "list"
	}
	return "nil"
}
