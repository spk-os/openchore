package taxonomy

import (
	"fmt"
	"math"
	"testing"
)

/* NFR-3：verify 求值器 fuzz 测试（卡 7.3-CODER）。
   覆盖白名单受限求值器 (Verifier{}).Eval 的安全性不变量：
   1) 任意输入不 panic、不 hang（go test -fuzz 默认每输入超时兜底，
      加 fuzzMaxInputLen 输入长度上限保护）；
   2) 返回的 error 必须是合法 error（非空文本、Error() 可安全调用）；
   3) 同输入两次 Eval 结果确定一致（error 文本相同；成功值深度相同）；
   4) 注入表达式全部拒绝（T11 Acceptance 口径）——固定注入样本由
      TestVerifierFuzzInjectionRejected 逐条可计数断言，同时全部进入
      fuzz 种子语料供变异引擎探索。
   种子语料覆盖：合法算术/分数/推导式、非法注入（脚本片段、函数调用、
   属性访问、超长表达式、裸 =），以及 verify_shortcircuit_regression_test.go
   中已回归的短路边界样本与推导式条件静态校验边界样本（多 if 链、
   if 后接 for、空迭代 if 1/0、条件末尾多余内容）。
   fuzz 语料由 go test 写入 $GOCACHE/fuzz（仓库外），本文件不依赖 testdata。 */

// fuzzMaxInputLen fuzz 输入长度上限保护：Eval 入口对超过 maxExprLen(600)
// 的输入直接拒绝，这里设更高的独立上限，用于约束 fuzz 引擎处理超大
// 语料的拷贝/转换开销，并保护 lexer 复杂度未来退化时的最坏耗时。
const fuzzMaxInputLen = 4096

// wantMinInjectionCases 注入样本数量下限（可计数断言：样本被误删时测试失败）。
const wantMinInjectionCases = 24

// fuzzSeedsValid 合法表达式种子（Eval 成功）：算术/分数/比较/白名单函数/
// 推导式/生成器/IfExp/科学计数法/None，全部取自既有用例已验证的语义。
var fuzzSeedsValid = []string{
	"3+1",
	"Fraction(1,4)",
	"500*1/(250*4)*100",
	"2**10",
	"-Fraction(1,2)+1",
	"7//2",
	"7%3",
	"'4'",
	"0.9e3*2",
	"1<2",
	"1==2",
	"sorted([3,1,2])[1]",
	"len([1,2,3])",
	"abs(-5)",
	"max(1,2,3)",
	"min([4,2,6])",
	"sqrt(2)**2",
	"Fraction(Fraction(5,2)**2-4, Fraction(1,2))",
	"sum([x*x for x in range(1,5)])",
	"len([10*a+b for a in (1,2,3) for b in (1,2,3) if a!=b and (a+b)%3==0])",
	"sum(1 for n in range(2,5) if n%2==0)",
	"sum(1 for a,b in ((2,3),(5,5)) if a>=b)",
	"[x for x in [-3,0,Fraction(5,2),Fraction(-1,2)] if x<0]",
	"(1 if True else 9)+1",
	"len(range(-(2**16),0))",
	"sum(range(-(2**63),-(2**63)+3))",
	"len(range(10,0,-3))",
	"mean([1,2,3])",
	"median([3,1,2])",
	"mode([1,1,2])",
	"gcd(12,18)",
	"lcm(4,6)",
	"factorial(5)",
	"comb(5,2)",
	"roots2(1,-3,2)",
	"isqrt(16)",
	"round(3.14159, 2)",
	"None",
	"True and False",
}

// fuzzInjectionCases 固定注入样本（Eval 必须 error）：脚本片段、白名单外
// 函数调用、属性访问、裸 =、关键字参数、超长表达式两条路径、残缺语法。
var fuzzInjectionCases = []string{
	// 脚本片段（解释器只支持表达式，语句一律拒绝）
	"import os",
	"def f(): pass",
	"while True: pass",
	"for i in range(3): pass",
	// 函数调用注入（白名单外函数）
	"__import__('os')",
	"eval('1')",
	"exec('pass')",
	"open('x')",
	"print('hello')",
	"globals()",
	"getattr(x, 'a')",
	"compile('1', 's', 'eval')",
	"input()",
	// 属性访问（lexer 无 '.' 运算符，直接非法字符）
	"a.b",
	"os.system('id')",
	"().__class__",
	"[].append(1)",
	// 裸 =（表达式契约拒绝，不静默当 ==）
	"1 = 2",
	"[1,2][0] = 5",
	"len([x for x in range(3) if x = 1])",
	// 关键字参数
	"sqrt(x=4)",
	// 超长表达式（整数常量过大 / 总长超限两条路径）
	"1 + " + fuzzRepeat("1", 400),
	fuzzRepeat("1+", 400) + "1",
	// 残缺/非法语法与非法答案文本
	"",
	"x",
	"1+",
	"lambda: 1",
	"Fraction('1/0')",
	"{'a': 1}",
	"0x41",
}

// fuzzShortcircuitSeeds 短路边界种子（对齐 verify_shortcircuit_regression_test.go
// 已回归样本）：成功样本（短路/惰性/NaN 语义）与必须报错的 junk 样本混合。
var fuzzShortcircuitSeeds = []string{
	// 成功：短路跳过运行时错误
	"1<0<(1/0)",
	"1<3<0<1/0",
	"1<0<((1+1)/(2-2))",
	"1<0<perm(100000,100000)",
	"0 and 1/0",
	"1 or 1/0",
	"1 and 2",
	"0 or 5",
	"'' or 'x'",
	// 成功：NaN 比较（IEEE 754）与链中短路
	"1<log(-1)<1/0",
	"log(-1)==log(-1)",
	// 成功：IfExp 惰性分支
	"1 if True else 1/0",
	"1 if 1<2<3 else 1/0",
	"1 if True else (2 if False else 3)",
	"2 if abs(3-4)<5<3+4 else (1 if 5==abs(3-4) or 5==3+4 else 0)",
	// 成功：长短路链（无 maxSkip 残留）
	"1<0<" + fuzzRepeat("1+", 150) + "1",
	// 必报 error：短路后的 junk 不能被静默吞掉
	"1<0<(1/0) 5+1",
	"1<0<(1/0) 5+1 2",
	"1<0<",
	"1<0<2 3",
	"0 and 1/0 5+1",
	"1 or 1/0 5+1",
	"1 if True else 1/0 5",
	"1 if 1<2<3 else 1/0 5",
	"1<0<" + fuzzRepeat("1+", 150) + "1 5",
	"1<0<" + fuzzRepeat("1+", 150),
	// 必报 error：IfExp 条件非布尔（真实求值路径）
	"1 if 0 else 2",
}

// fuzzCompCondSeeds 推导式条件边界种子（对齐 DEF-VERIFY-COMPREHENSION-SYNTAX-R1
// 回归样本）：多 if 链、if 后接 for、空迭代 if 1/0、条件末尾多余内容。
var fuzzCompCondSeeds = []string{
	// 成功：多 if 链 / if 后接 for
	"len([1 for x in range(10) if x>2 if x<5])",
	"len([x for x in range(6) if x%2==0 if x>0])",
	"len([1 for a in range(3) if a>0 for b in range(3) if b<a])",
	"len([1 for a in range(3) if a>0 for b in range(a)])",
	// 成功：空迭代边界（条件含 1/0 但迭代为空 → 不执行）
	"len([1 for x in range(0) if x>2 if 1/0])",
	"len([x for x in range(0) if 1/0])",
	"len([x for x in range(0) if 1/0 and x<5])",
	// 成功：合法条件过滤与嵌套推导式
	"len([x for x in range(1,21) if x%2==0 and x%3!=0])",
	"sum(1 for n in range(2,21) if sum(1 for d in range(2,n) if n%d==0)==0)",
	"max([m for m in range(-20,20) if len([x for x in range(-30,30) if x>2 and x<m])==2])",
	// 必报 error：条件静态语法校验（空迭代/非空迭代同样必报）
	"len([x for x in range(0) if x 5+1])",
	"len([x for x in range(0) if x 5])",
	"len([1 for x in range(0) if x 5])",
	"len([1 for x in range(3) if x 5])",
	"len([1 for x in range(3) if x 5+1])",
	"len([x for x in range(3) if x<])",
	"len([x for x in range(0) if x<])",
	"len([x for x in range(0) if x+])",
	"sum(1 for x in range(0) if x 5)",
	"sum(1 for x in range(3) if x 5)",
	"max([m for m in range(-20,20) if len([x for x in range(-30,30) if x 5 and x<m])==2])",
	"len([1 for x in range(10) if x>2 if x 5])",
	"len([1 for a in range(3) if a>0 for b in range(3) if b 5])",
	// 必报 error：非空迭代执行条件 → 运行时除零
	"len([x for x in range(3) if 1/0])",
}

// fuzzRepeat 构造重复字符串（超长注入种子用）。不复用其它测试文件的
// repeat helper，避免对只读文件产生编译依赖。
func fuzzRepeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

// fuzzSameValue 深度比较两次 Eval 的成功值是否完全一致。
// 'f' 用位级比较：同输入两次求值的浮点结果（含 NaN payload、-0.0）
// 必须位一致，== 对 NaN 恒 false 不能用于确定性判定。
// 'q' 的 rat 为 nil 时按指针相等判定（避免 nil 解引用 panic），
// 同输入两次求值应产生同为 nil 或同非 nil 的结果。
func fuzzSameValue(a, b vValue) bool {
	if a.kind != b.kind {
		return false
	}
	switch a.kind {
	case 'q':
		if a.rat == nil || b.rat == nil {
			return a.rat == b.rat
		}
		return a.rat.Cmp(b.rat) == 0
	case 'f':
		return math.Float64bits(a.f) == math.Float64bits(b.f)
	case 's':
		return a.s == b.s
	case 'b':
		return a.b == b.b
	case 'l':
		if len(a.list) != len(b.list) {
			return false
		}
		for i := range a.list {
			if !fuzzSameValue(a.list[i], b.list[i]) {
				return false
			}
		}
		return true
	case 'n':
		return true
	}
	return false
}

// FuzzVerifyExpr verify 求值器 fuzz 入口：以 fuzz 输入为表达式调用
// (Verifier{}).Eval，校验不 panic / 不 hang / error 合法 / 同输入两次
// Eval 结果确定一致。注入拒绝的可计数断言见 TestVerifierFuzzInjectionRejected。
func FuzzVerifyExpr(f *testing.F) {
	for _, s := range fuzzSeedsValid {
		f.Add(s)
	}
	for _, s := range fuzzInjectionCases {
		f.Add(s)
	}
	for _, s := range fuzzShortcircuitSeeds {
		f.Add(s)
	}
	for _, s := range fuzzCompCondSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, expr string) {
		// 输入长度上限保护：超长输入直接跳过（Eval 对超过 maxExprLen 的
		// 输入本就拒绝，此处限制 fuzz 引擎处理超大语料的开销）。
		if len(expr) > fuzzMaxInputLen {
			t.Skip()
		}
		v1, err1 := (Verifier{}).Eval(expr)
		v2, err2 := (Verifier{}).Eval(expr)
		// 不变量：同输入两次 Eval 的成功/失败必须一致。
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("Eval(%q) 两次求值成功性不一致: err1=%v err2=%v", expr, err1, err2)
		}
		if err1 != nil {
			// 不变量：error 必须合法（非空文本）且文本确定。
			if err1.Error() == "" {
				t.Fatalf("Eval(%q) 返回空 error 文本", expr)
			}
			if err1.Error() != err2.Error() {
				t.Fatalf("Eval(%q) 两次 error 文本不一致: %q vs %q", expr, err1.Error(), err2.Error())
			}
			return
		}
		// 不变量：成功值可确定性重放（同输入两次 Eval 深度一致）。
		if !fuzzSameValue(v1, v2) {
			t.Fatalf("Eval(%q) 两次成功值不一致: kind=%c vs kind=%c", expr, v1.kind, v2.kind)
		}
	})
}

// TestVerifierFuzzInjectionRejected T11 Acceptance 口径的可计数断言：
// 固定注入样本全部被 Eval 拒绝（返回非空 error）。样本数量有下限断言，
// 每个样本一个子测试，共 len(fuzzInjectionCases) 条可计数断言。
func TestVerifierFuzzInjectionRejected(t *testing.T) {
	if len(fuzzInjectionCases) < wantMinInjectionCases {
		t.Fatalf("注入样本数 %d 低于下限 %d", len(fuzzInjectionCases), wantMinInjectionCases)
	}
	for i, expr := range fuzzInjectionCases {
		t.Run(fmt.Sprintf("inject%02d", i), func(t *testing.T) {
			v, err := (Verifier{}).Eval(expr)
			if err == nil {
				t.Fatalf("Eval(%q) 应返回 error（注入必须拒绝），实际成功 kind=%c", expr, v.kind)
			}
			if err.Error() == "" {
				t.Fatalf("Eval(%q) 返回空 error 文本", expr)
			}
		})
	}
}
