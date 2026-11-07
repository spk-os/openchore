package taxonomy

import (
	"reflect"
	"testing"
)

/* 受限表达式求值器（verify 字段）的单测。
   迁移自临时测试 verify_quick_test.go / comp_probe_test.go 中有价值的断言。 */

func evalRat(t *testing.T, expr string) *vValue {
	t.Helper()
	v, err := (Verifier{}).Eval(expr)
	if err != nil {
		t.Fatalf("Eval(%q): %v", expr, err)
	}
	return &v
}

func TestVerifierBasicArithmetic(t *testing.T) {
	cases := []struct {
		expr string
		want string // big.Rat 规范形式
	}{
		{"3+1", "4"},
		{"Fraction(1,4)", "1/4"},
		{"500*1/(250*4)*100", "50"},
		{"2**10", "1024"},
		{"-Fraction(1,2)+1", "1/2"},
		{"7//2", "3"},
		{"7%3", "1"},
	}
	for _, c := range cases {
		v := evalRat(t, c.expr)
		if v.kind != 'q' || v.rat == nil || v.rat.RatString() != c.want {
			t.Errorf("Eval(%q) = kind %c rat %v，期望分数 %s", c.expr, v.kind, v.rat, c.want)
		}
	}
	v := evalRat(t, "'4'")
	if v.kind != 's' || v.s != "4" {
		t.Errorf("字符串字面量应为 's' 类型 \"4\"，实际 kind=%c s=%q", v.kind, v.s)
	}
	tv := evalRat(t, "1<2")
	if tv.kind != 'b' || !tv.b {
		t.Errorf("1<2 应为 true")
	}
}

func TestVerifierComprehension(t *testing.T) {
	// 迁移自 comp_probe_test.go：推导式 + 过滤 + if/else
	v := evalRat(t, "[x for x in [-3,0,Fraction(5,2),Fraction(-1,2)] if x<0]")
	if v.kind != 'l' || len(v.list) != 2 {
		t.Fatalf("推导式应得到 2 个负值，实际 kind=%c len=%d", v.kind, len(v.list))
	}
	if v.list[0].rat.RatString() != "-3" || v.list[1].rat.RatString() != "-1/2" {
		t.Errorf("推导式结果 = %v / %v，期望 -3 / -1/2", v.list[0].rat, v.list[1].rat)
	}
	// 模板引用迭代变量 + sum 包裹（修复后应正常求值）
	v2 := evalRat(t, "sum([x*x for x in range(1,5)])")
	if v2.kind != 'q' || v2.rat.RatString() != "30" {
		t.Errorf("sum 推导式 = %v，期望 30", v2.rat)
	}
	// 多 for 子句 + if 条件（笛卡尔积）
	v3 := evalRat(t, "len([10*a+b for a in (1,2,3) for b in (1,2,3) if a!=b and (a+b)%3==0])")
	if v3.kind != 'q' || v3.rat.RatString() != "2" {
		t.Errorf("多 for 推导式长度 = %v，期望 2", v3.rat)
	}
	// 生成器表达式（裸，无方括号）
	v4 := evalRat(t, "sum(1 for n in range(2,5) if n%2==0)")
	if v4.kind != 'q' || v4.rat.RatString() != "2" {
		t.Errorf("生成器 sum = %v，期望 2", v4.rat)
	}
	// 元组解包
	v5 := evalRat(t, "sum(1 for a,b in ((2,3),(5,5)) if a>=b)")
	if v5.kind != 'q' || v5.rat.RatString() != "1" {
		t.Errorf("元组解包生成器 = %v，期望 1", v5.rat)
	}
	// 嵌套 Fraction（Fraction 参数可为 Fraction 运算结果）
	v6 := evalRat(t, "Fraction(Fraction(5,2)**2-4, Fraction(1,2))")
	if v6.kind != 'q' || v6.rat.RatString() != "9/2" {
		t.Errorf("嵌套 Fraction = %v，期望 9/2", v6.rat)
	}
	// 科学计数法（float 字面量）
	v7 := evalRat(t, "0.9e3*2")
	if v7.kind != 'f' || v7.f != 1800 {
		t.Errorf("科学计数法 = %v，期望 1800", v7.f)
	}
	// 下标含比较表达式 + 布尔下标链（True→1，与 Python 一致：[2,1][1]=1）
	v8 := evalRat(t, "[[3,4],[2,1]][1>0][1>0]")
	if v8.kind != 'q' || v8.rat.RatString() != "1" {
		t.Errorf("布尔下标链 = %v，期望 1", v8.rat)
	}
	// and 短路（右边除零不触发）
	v9 := evalRat(t, "[x for x in [0,1] if x!=0 and 1/x==1]")
	if v9.kind != 'l' || len(v9.list) != 1 || v9.list[0].rat.RatString() != "1" {
		t.Errorf("and 短路推导式 = %v，期望 [1]", v9.list)
	}
	// 迭代区间引用前序变量（惰性求值）
	v10 := evalRat(t, "sum(1 for a in range(2,5) for b in range(1,a) if b<a)")
	if v10.kind != 'q' {
		t.Errorf("惰性 iter 推导式求值失败 kind=%c", v10.kind)
	}
	v11 := evalRat(t, "1 if 2>1 else 0")
	if v11.rat == nil || v11.rat.RatString() != "1" {
		t.Errorf("if/else 应为 1")
	}
}

func TestVerifierWhitelist(t *testing.T) {
	bad := []string{
		"__import__('os')", "open('x')", "eval('1')", "x", "1+", "lambda: 1",
		"[].append(1)", "a.b",
		"1 + " + repeat("1", 400),
	}
	for _, expr := range bad {
		if _, err := (Verifier{}).Eval(expr); err == nil {
			t.Errorf("Eval(%q) 应报错", expr)
		}
	}
	long := repeat("1+", 400) + "1"
	if _, err := (Verifier{}).Eval(long); err == nil {
		t.Errorf("超过长度上限的表达式应报错")
	}
}

func TestVerifierSortedAndList(t *testing.T) {
	v := evalRat(t, "sorted([3,1,2])[1]")
	if v.rat == nil || v.rat.RatString() != "2" {
		t.Errorf("sorted(...)[1] 应为 2，实际 %v", v.rat)
	}
	v = evalRat(t, "len([1,2,3])")
	if v.rat == nil || v.rat.RatString() != "3" {
		t.Errorf("len 应为 3")
	}
}

func TestVerifierDeterministic(t *testing.T) {
	exprs := []string{"Fraction(1,3)*3", "sqrt(2)**2", "abs(-5)", "max(1,2,3)", "min([4,2,6])"}
	for _, e := range exprs {
		a, err := (Verifier{}).Eval(e)
		if err != nil {
			t.Errorf("Eval(%q): %v", e, err)
			continue
		}
		b, _ := (Verifier{}).Eval(e)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("Eval(%q) 两次结果不同", e)
		}
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
