package taxonomy

import (
	"strings"
	"testing"
)

/* TX-VERIFY-COMPARE-IMPL-V3：Python 链式比较语义严格行为测试。
   a op1 b op2 c == (a op1 b) and (b op2 c)：左到右惰性求值、每个操作数最多
   求值一次、某段为 false 时立即返回 false 且其后操作数不求值（只跳 token）。
   本文件只断言行为（值 / 是否有 error / error 类型），不改业务实现。 */

type chainCase struct {
	expr string
	want bool // 期望布尔值
}

// evalChainBool 严格求值：必须无 error 且返回布尔值。
func evalChainBool(t *testing.T, expr string) vValue {
	t.Helper()
	v, err := (Verifier{}).Eval(expr)
	if err != nil {
		t.Fatalf("Eval(%q) 不应报错，实际 error: %v", expr, err)
	}
	if v.kind != 'b' {
		t.Fatalf("Eval(%q) 应返回布尔，实际 kind=%c", expr, v.kind)
	}
	return v
}

// evalChainErr 严格求值：必须返回 error（VerifyError）。
func evalChainErr(t *testing.T, expr string) error {
	t.Helper()
	var (
		v   vValue
		err error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Eval(%q) panic: %v（应返回 error）", expr, r)
			}
		}()
		v, err = (Verifier{}).Eval(expr)
	}()
	if err == nil {
		t.Fatalf("Eval(%q) 应报错，实际成功返回 kind=%c value=%+v", expr, v.kind, v)
	}
	if _, ok := err.(*VerifyError); !ok {
		t.Fatalf("Eval(%q) 应返回 *VerifyError，实际 %T: %v", expr, err, err)
	}
	return err
}

// TestChainComparisonTruthTable 覆盖冻结四例与 true 链 / 不等于 / 各运算符边界。
func TestChainComparisonTruthTable(t *testing.T) {
	cases := []chainCase{
		// ---- 冻结四例 ----
		// 1<0 为 false → 短路，右侧 (1/0) 不求值：false 且无 error
		{"1<0<(1/0)", false},
		// 1/0 在链首即求值 → 除零 error（由 TestChainComparisonStrictErrors 断言）
		// 1<2 为 true → 继续求值 (1/0) → 除零 error（同上）

		// ---- true 链 ----
		{"1<2<3", true},
		{"1<2<3<4", true},
		{"3>2>1", true},
		{"5>4>3>2>1", true},
		{"1<=1<=1", true},
		{"1<=2>=2", true},
		{"1<3>2", true},
		{"1<2<=2<3", true},
		{"Fraction(1,3)<Fraction(1,2)<Fraction(2,3)", true},

		// ---- false 链（3<2<5 等）----
		{"3<2<5", false},
		{"3<2<1", false},
		{"1<2<2", false},
		{"3>2>2", false},
		{"5>4>3>2>5", false},
		{"1<=2>=3", false},
		{"1<3<2", false}, // 中段失败

		// ---- 不等于 / 等于 ----
		{"1!=2!=3", true},
		{"1!=2!=1", true},
		{"1!=1!=2", false}, // 首段失败
		{"2==2==2", true},
		{"2==2==3", false},
		// Python 语义：1 == 2 == 2 为 (1==2) and (2==2) = False（不是连等式）
		{"1==2==2", false},
		{"1==2!=3", false},
		// <> 是 != 的别名
		{"1<>2<>3", true},
		{"1<>1<>2", false},
		// 布尔值参与比较（True == 1）
		{"True==1<2", true},
		// 字符串比较
		{"'a'<'b'<'c'", true},
		{"'a'<'b'<'a'", false},
		// 负数与运算优先级：比较低于加减乘除
		{"-1<0<1", true},
		{"1+1<2+1<4", true},
		{"1*2<1+2<2*2", true},
	}
	for _, c := range cases {
		v := evalChainBool(t, c.expr)
		if v.b != c.want {
			t.Errorf("Eval(%q) = %v，期望 %v", c.expr, v.b, c.want)
		}
	}
}

// TestChainComparisonStrictErrors 冻结的 error 用例 + 每位置求值语义。
func TestChainComparisonStrictErrors(t *testing.T) {
	cases := []struct {
		expr    string
		wantSub string // error 文本应包含的子串
	}{
		// ---- 冻结两例 ----
		{"1/0<1<2", "除以 0"},   // 链首求值即失败
		{"1<2<(1/0)", "除以 0"}, // 前段为 true → 继续求值 → 失败
		// 中段操作数：1<2 求值后 (1/0)<3 的左操作数求值失败
		{"1<(1/0)<3", "除以 0"},
		// 首段为 true、第二段为 true、第三个操作数失败
		{"1<2<3<(1/0)", "除以 0"},
		// 非除零错误同样在“到达该操作数”时才发生：越界下标
		{"1<2<[1,2][5]", "下标越界"},
		{"1<2<factorial(-1)", "factorial"},
	}
	for _, c := range cases {
		err := evalChainErr(t, c.expr)
		if !strings.Contains(err.Error(), c.wantSub) {
			t.Errorf("Eval(%q) error = %q，期望包含 %q", c.expr, err.Error(), c.wantSub)
		}
	}
}

// TestChainComparisonShortCircuitNoError 证明 false 之后剩余操作数未求值：
// 右侧放“一旦求值必报错”的表达式（除零 / 越界 / 超额预算），结果仍是 false 且无 error。
func TestChainComparisonShortCircuitNoError(t *testing.T) {
	exprs := []string{
		"1<0<(1/0)",
		"1<0<1/0<2",
		"1<0<[1,2][5]",
		"3<2<5/0",
		"3<2<factorial(-1)",
		"3==4==(1/0)",
		"3!=3!=1/0",
		"1<0<1/0<(1/0)",
		// 深嵌套括号里的除零，短路扫描必须跨过配对括号
		"1<0<((1+1)/(2-2))",
		"1<0<[1/0, 2][0]",
		// 中间段失败：第三操作数不求值
		"1<3<0<1/0",
	}
	for _, expr := range exprs {
		v := evalChainBool(t, expr)
		if v.b != false {
			t.Errorf("Eval(%q) = %v，期望 false（短路后不得求值右侧）", expr, v.b)
		}
	}
}

// TestChainComparisonShortCircuitBudget false 后右侧表达式（即使完全合法）
// 也不得消耗求值预算：以 comb 循环步数耗尽预算的方式构造确定性重表达式。
func TestChainComparisonShortCircuitBudget(t *testing.T) {
	// 基线：该表达式单独求值因结果位数超限报错（确定性，可作后续对照）。
	heavy := "perm(100000,100000)"
	heavyErr := evalChainErr(t, heavy)
	if !strings.Contains(heavyErr.Error(), "结果过大") {
		t.Fatalf("前提不成立：Eval(%q) error = %q，期望结果过大", heavy, heavyErr.Error())
	}
	// 短路：1<0 为 false，右侧不应求值 → false 且无 error。
	short := "1<0<" + heavy
	if v := evalChainBool(t, short); v.b != false {
		t.Errorf("Eval(%q) = %v，期望 false 且无 error", short, v.b)
	}
	// 中间段失败：第二操作数之后再放重表达式。
	short2 := "3<2<5<" + heavy
	if v := evalChainBool(t, short2); v.b != false {
		t.Errorf("Eval(%q) = %v，期望 false 且无 error", short2, v.b)
	}
	// 对照组：链全为 true 时右侧重表达式必须被求值 → 报错。
	if err := evalChainErr(t, "1<2<"+heavy); !strings.Contains(err.Error(), "结果过大") {
		t.Errorf("Eval(%q) error = %q，期望求值右侧并报结果过大", "1<2<"+heavy, err.Error())
	}
	// 每个操作数最多求值一次：A < B <= A ... 不做重复求值的可观测验证
	// 用 a 作为中间操作数再与原地重表达式无关；这里退而验证同一操作数
	// (1+2) 作为中间项只比较其值，不影响结果（值语义回归）。
	if v := evalChainBool(t, "1<(1+2)<4"); !v.b {
		t.Errorf("1<(1+2)<4 = %v，期望 true", v.b)
	}
}

// TestChainComparisonParsePosition 验证 false 短路后“解析位置正确”：
// 剩余操作数的 token 被完整跳过后，and/or/if-else/括号/下标/函数参数/
// 列表逗号等上层结构仍从正确位置继续解析，不能产生“末尾多余内容”或错位。
func TestChainComparisonParsePosition(t *testing.T) {
	type numCase struct {
		expr string
		want string // big.Rat 规范形式
	}
	numCases := []numCase{
		// and：左侧 false 链整体为假 → false
		{"1<0<(1/0) and 0", "false"},
		{"0 or 1<0<(1/0)", "false"},
		{"1<0<(1/0) or 1<2<3", "true"},
		// if/else：条件位置的 false 链后必须停在 else
		{"1 if 1<0<(1/0) else 2", "2"},
		{"1 if 1<2<3 else 2", "1"},
		// 括号：false 链停在本层 ")"，外层继续
		{"(1<0<(1/0))", "false"},
		{"(1<0<(1/0)) or (2<1<(1/0))", "false"},
		{"(1<2<3) and (3<2<1/0)", "false"},
		// 函数参数：跳过停在逗号，后续参数正常求值
		{"max(1<0<(1/0), 5)", "5"},
		{"min(1<2<3, 1<0<(1/0), 2)", "false"},
		// 列表元素：跳过停在逗号
		{"5 if 1<0<(1/0) else 7", "7"},
		// 列表内元素（比较结果作元素值）
		{"len([1<0<(1/0), 1<2<3])", "2"},
		// 布尔结果作下标（true→1，false→0）
		{"[10,20][1<2<3]", "20"},
		{"[10,20][1<0<(1/0)]", "10"},
		// 嵌套下标中的比较链
		{"[[1,2],[3,4]][1<2<3][3<2<1/0]", "3"},
		// 三元 if 嵌套在比较操作数位置
		{"(5 if 1<0 else 9)<6<10", "false"}, // 已知缺陷：条件表达式不参与算术，取 value+0（见最终报告）
		// 推导式 if 条件中的 false 链（条件段边界定位）
		{"len([x for x in [1,2,3] if 1<0<(1/0)])", "0"},
		{"len([x for x in [1,2,3] if x<2<3])", "1"},
		// 与 not 组合：not 作用于 false 链整体
		{"1 if not (1<0<(1/0)) else 0", "1"},
	}
	for _, c := range numCases {
		v, err := (Verifier{}).Eval(c.expr)
		if err != nil {
			t.Errorf("Eval(%q) 不应报错（解析位置错位），实际: %v", c.expr, err)
			continue
		}
		var got string
		switch v.kind {
		case 'b':
			got = map[bool]string{true: "true", false: "false"}[v.b]
		case 'q':
			got = v.rat.RatString()
		default:
			got = "kind=" + string(v.kind)
		}
		if got != c.want {
			t.Errorf("Eval(%q) = %s，期望 %s", c.expr, got, c.want)
		}
	}

	// 已知缺陷已修复（strict 翻转）：false 链后接算术后缀（` 5+1`）属于
	// 语法非法（Python SyntaxError），短路只免除求值不免除语法校验。
	evalChainErr(t, "1<0<(1/0) 5+1")
	evalChainErr(t, "1<0<")
	evalChainErr(t, "1<0<2 3")
	evalChainErr(t, "0 and 1/0 5+1")
	// true 链后额外的 token 仍须报“末尾有多余内容”。
	// true 链后同样：未求值错误不能掩盖语法错误。
	if err := evalChainErr(t, "1<2<3 5"); !strings.Contains(err.Error(), "多余内容") {
		t.Errorf("Eval(\"1<2<3 5\") error = %q，期望末尾多余内容", err.Error())
	}
	// 非链单比较后的算术运算优先级：1<2+2 为 1<(2+2)。
	if v := evalChainBool(t, "1<2+2"); !v.b {
		t.Errorf("1<2+2 = %v，期望 true", v.b)
	}
	if v := evalChainBool(t, "5<2+1"); v.b {
		t.Errorf("5<2+1 = %v，期望 false", v.b)
	}
}

// TestChainComparisonAndOrBoundary 确认 and/or 边界：false 链的剩余段
// 只在 and/or/if/else 处停下（同一 parser 递归下降解析），
// "1<0<1 and X" 中的 X 正常参与。and/or 按 Python 返回操作数值。
func TestChainComparisonAndOrBoundary(t *testing.T) {
	// 布尔断言组：结果为布尔（比较链 / not / 括号包住）。
	boolCases := []struct {
		expr string
		want bool
	}{
		{"1<0<1 and 1<2<3", false},     // false and true → false
		{"1<0<1 or 1<2<3", true},       // false or true → true
		{"1<2<3 and 3<2<5", false},     // true and false → false
		{"1<2<3 or 3<2<5", true},       // true or false（右侧仍语法校验，无求值副作用）
		{"not 1<0<1", true},            // not 作用于整条链
		{"not 1<2<3", false},           // 同上
		{"1<2<3 == True", true},        // 链结果为布尔，与 True 比较
		{"1<2<3 == False", false},      //
		{"(1<0<(1/0)) == False", true}, // 显式括号后与 False 比较正确
		{"(1<0<(1/0)) + 1 == 1", true}, // 布尔 False 作数值 0（+1==1）
	}
	for _, k := range boolCases {
		v := evalChainBool(t, k.expr)
		if v.b != k.want {
			t.Errorf("Eval(%q) = %v，期望 %v", k.expr, v.b, k.want)
		}
	}
	// or 短路 + 右侧 false 链：1 or ... 返回左操作数 1（truthy），
	// 外层 == 1 验证返回的是操作数值 1 而非布尔 true（Python：1 == True
	// 但值必须是 1 本身，比较结果为 True）。
	if v := evalChainBool(t, "(1 or 1<0<(1/0)) == 1"); !v.b {
		t.Errorf("(1 or 1<0<(1/0)) == 1 应为 true（or 返回左操作数 1）")
	}
	// Python 操作数值语义：and 左 falsy 返回左操作数（0），
	// 外层 not 作用于该值（not 0 = True）验证 falsy 语义。
	if v := evalChainBool(t, "not (0 and 1<0<(1/0))"); !v.b {
		t.Errorf("not (0 and 1<0<(1/0)) = false，期望 true（and 返回左操作数 0，为 falsy）")
	}
	// 无括号的 `1<0<(1/0) == False` 按 Python 语法解析为 1<0<((1/0)==False)：
	// 第三操作数是 (1/0)==False 的布尔值，链的第二段已为 false（短路），
	// 结果为 false——不是链结果的 == 比较。与 Python 一致。
	if v := evalChainBool(t, "1<0<(1/0) == False"); v.b {
		t.Errorf("1<0<(1/0) == False = true，期望 false（Python：1<0<((1/0)==False)）")
	}
	// and 短路右操作数值：0 and ... 返回 0（kind=q），非布尔 false。
	v, err := (Verifier{}).Eval("0 and 1<0<(1/0)")
	if err != nil {
		t.Fatalf("Eval(\"0 and 1<0<(1/0)\"): %v", err)
	}
	if v.kind != 'q' || v.rat == nil || v.rat.RatString() != "0" {
		t.Errorf("0 and 1<0<(1/0) 应返回左操作数 0（kind=q），实际 kind=%c", v.kind)
	}
}
