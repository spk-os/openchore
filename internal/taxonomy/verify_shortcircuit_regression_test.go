package taxonomy

import (
	"strings"
	"testing"
	"time"
)

/* DEF-VERIFY-SHORTCIRCUIT-R1 回归：短路吞 junk / or 不短路 / maxSkip
   静默残留 / and-or 操作数语义 / 裸 = 契约 / NaN 比较 / range MinInt64
   回绕 / IfExp else 惰性。
   全部断言当前修复后的语义（旧 xfail 已翻转），不得删除转绿。 */

// evalReg 在时限内求值（防死循环导致测试挂死）。
func evalReg(t *testing.T, expr string) (vValue, error) {
	t.Helper()
	type res struct {
		v   vValue
		err error
	}
	done := make(chan res, 1)
	go func() {
		v, err := (Verifier{}).Eval(expr)
		done <- res{v, err}
	}()
	select {
	case r := <-done:
		return r.v, r.err
	case <-time.After(5 * time.Second):
		t.Fatalf("Eval(%q) 超过 5s（预算失效/死循环）", expr)
		return vValue{}, nil
	}
}

// wantErrSub 断言 VerifyError 且文本包含子串。
func wantErrSub(t *testing.T, expr, sub string) {
	t.Helper()
	_, err := evalReg(t, expr)
	if err == nil {
		t.Errorf("Eval(%q) 应报错（期望含 %q），实际成功", expr, sub)
		return
	}
	if _, ok := err.(*VerifyError); !ok {
		t.Errorf("Eval(%q) 应返回 *VerifyError，实际 %T: %v", expr, err, err)
		return
	}
	if !strings.Contains(err.Error(), sub) {
		t.Errorf("Eval(%q) error = %q，期望包含 %q", expr, err.Error(), sub)
	}
}

// wantBool 断言求值成功且返回布尔值。
func wantBool(t *testing.T, expr string, want bool) {
	t.Helper()
	v, err := evalReg(t, expr)
	if err != nil {
		t.Errorf("Eval(%q) 不应报错: %v", expr, err)
		return
	}
	if v.kind != 'b' || v.b != want {
		t.Errorf("Eval(%q) = kind %c b=%v，期望布尔 %v", expr, v.kind, v.b, want)
	}
}

// wantNum 断言求值成功且返回指定分数值。
func wantNum(t *testing.T, expr, want string) {
	t.Helper()
	v, err := evalReg(t, expr)
	if err != nil {
		t.Errorf("Eval(%q) 不应报错: %v", expr, err)
		return
	}
	if v.kind != 'q' || v.rat == nil || v.rat.RatString() != want {
		t.Errorf("Eval(%q) = kind %c %v，期望分数 %s", expr, v.kind, v.rat, want)
	}
}

// wantStr 断言求值成功且返回指定字符串。
func wantStr(t *testing.T, expr, want string) {
	t.Helper()
	v, err := evalReg(t, expr)
	if err != nil {
		t.Errorf("Eval(%q) 不应报错: %v", expr, err)
		return
	}
	if v.kind != 's' || v.s != want {
		t.Errorf("Eval(%q) = kind %c %q，期望字符串 %q", expr, v.kind, v.s, want)
	}
}

// TestShortCircuitJunkMustError 短路后的非法 junk / 悬挂运算符 /
// 未求值错误不能掩盖语法错误：全部必须 VerifyError（旧实现静默返回 false）。
func TestShortCircuitJunkMustError(t *testing.T) {
	cases := []struct{ expr, sub string }{
		// 比较链短路后接算术后缀：` 5+1` 是语法非法（Python SyntaxError）
		{"1<0<(1/0) 5+1", "多余内容"},
		{"1<0<(1/0) 5+1 2", "多余内容"},
		// 悬挂比较符：`1<0<` 后无操作数
		{"1<0<", "记号"},
		// 链中段直接相邻的数字（无运算符连接）
		{"1<0<2 3", "多余内容"},
		// and 短路后接 junk
		{"0 and 1/0 5+1", "多余内容"},
		// or 短路后接 junk
		{"1 or 1/0 5+1", "多余内容"},
		// IfExp 未选中分支的 junk
		{"1 if True else 1/0 5", "多余内容"},
		{"1 if 1<2<3 else 1/0 5", "多余内容"},
	}
	for _, c := range cases {
		wantErrSub(t, c.expr, c.sub)
	}
}

// TestShortCircuitStillSkipsRuntimeErrors 短路语义保留：短路分支中的
// 运行时错误（除零/越界/超预算）不被触发。
func TestShortCircuitStillSkipsRuntimeErrors(t *testing.T) {
	wantBool(t, "1<0<(1/0)", false)
	// and/or 短路：右侧除零不触发（返回操作数值，Python 语义）
	wantNum(t, "0 and 1/0", "0")
	wantNum(t, "1 or 1/0", "1")
	// and/or 返回操作数值（Python 语义）
	wantNum(t, "0 and 1/0", "0")
	wantNum(t, "1 or 1/0", "1")
	wantNum(t, "1 and 2", "2")
	wantNum(t, "0 or 5", "5")
	wantStr(t, "'' or 'x'", "x")
	// 链中段失败后第三操作数不求值
	wantBool(t, "1<3<0<1/0", false)
	// 深嵌套括号里的除零
	wantBool(t, "1<0<((1+1)/(2-2))", false)
	// 短路预算：右侧重表达式不求值（perm 结果位数超限的错误不发生）
	wantBool(t, "1<0<perm(100000,100000)", false)
}

// TestOrShortCircuitPythonSemantics `or` 短路 + 操作数值语义：
// 左 truthy 返回左操作数（不求值右侧），否则返回右操作数。
func TestOrShortCircuitPythonSemantics(t *testing.T) {
	// 左 truthy：右侧不执行（1/0 不报除零）
	wantNum(t, "1 or 1/0", "1")
	wantNum(t, "2 or 3", "2") // 旧实现错误返回 true
	wantStr(t, "'a' or 'b'", "a")
	// 左 falsy：返回右操作数值
	wantNum(t, "0 or 5", "5")
	wantNum(t, "0 or 0 or 9", "9")
	wantStr(t, "'' or 'x'", "x")
	// 多段链：第一个 truthy 为准
	wantNum(t, "0 or 0 or 7", "7")
	wantNum(t, "0 or 8 or 9", "8")
	// 右侧语法仍校验（junk 报错）但求值不发生
	wantErrSub(t, "1 or 1/0 5+1", "多余内容")
	// 旧缺陷修复：`1 or 2` 返回 2（操作数）而非 true
	if v, _ := evalReg(t, "1 or 2"); v.kind != 'q' || v.rat.RatString() != "1" {
		t.Errorf("1 or 2 应返回左操作数 1（kind=q），实际 kind=%c %v", v.kind, v.rat)
	}
}

// TestAndShortCircuitPythonSemantics `and` 短路 + 操作数值语义：
// 左 falsy 返回左操作数（不求值右侧），否则返回右操作数。
func TestAndShortCircuitPythonSemantics(t *testing.T) {
	// 左 falsy：右侧不执行
	wantNum(t, "0 and 1/0", "0")
	wantStr(t, "'' and 1/0", "")
	// 左 truthy：返回右操作数值
	wantNum(t, "1 and 2", "2") // 旧实现错误返回 true
	wantNum(t, "1 and 2 and 3", "3")
	wantNum(t, "3 and 0", "0")
	// 多段链
	wantNum(t, "1 and 2 and 0", "0")
	wantNum(t, "1 and 1 and 7", "7")
	// 右侧语法仍校验
	wantErrSub(t, "0 and 1/0 5+1", "多余内容")
	// 链回环：and 的 falsy 返回值参与外层 or
	wantNum(t, "(0 and 1/0) or 5", "5")
}

// TestIfExpLazyBranches IfExp 惰性：只执行选中分支，未选中分支仍语法校验
// （else 可参与算术——返回值不再是布尔包装）。
func TestIfExpLazyBranches(t *testing.T) {
	// 条件真：else 分支不求值（除零不报错）
	wantNum(t, "1 if 1<2<3 else 1/0", "1")
	wantNum(t, "1 if True else 1/0", "1")
	// 条件假：then 不执行（else 求值），then 里的运行时错误不触发——
	// 注：then 前缀值会先求值（`X if cond else Y` 的 X 是 orTest 一部分），
	// Python 同样先求值再判条件，因此这里只断言 else 惰性。
	wantNum(t, "1 if 3<2<1 else 4", "4")
	wantNum(t, "2 if False else 4", "4")
	// else 参与算术（旧实现 IfExp 结果被当布尔值 +0）
	wantNum(t, "(1 if True else 9)+1", "2")
	wantNum(t, "(5 if False else 9)+1", "10")
	wantNum(t, "(5 if False else 9)-4", "5")
	// 嵌套 IfExp
	wantNum(t, "1 if True else (2 if False else 3)", "1")
	wantNum(t, "3 if False else (2 if 5==1 or 5==2 else 8)", "8")
	// 未选中分支语法校验：junk 报错
	wantErrSub(t, "1 if True else 1/0 5", "多余内容")
	// 未选中分支里的越界/除零不触发
	wantNum(t, "9 if True else [1,2][5]", "9")
	// 真实求值时条件必须布尔（校验模式下不做该检查，真实路径保持）
	wantErrSub(t, "1 if 0 else 2", "if 条件必须是布尔")
	// 题库真实用例（cnm_h181_q1 曾在校验模式下被"条件必须是布尔"误杀）
	wantNum(t, "2 if abs(3-4)<5<3+4 else (1 if 5==abs(3-4) or 5==3+4 else 0)", "2")
}

// TestBareEqualsRejected 裸 `=` 按表达式契约明确拒绝（不静默当 ==）。
// 题库当前 0 条裸 = 表达式（questions.json 无 " = " 出现）。
func TestBareEqualsRejected(t *testing.T) {
	wantErrSub(t, "1 = 2", "裸 =")
	wantErrSub(t, "[1,2][0] = 5", "裸 =")
	// == 仍正常
	wantBool(t, "1==2", false)
	wantBool(t, "1==1", true)
	// 函数调用的关键字参数拒绝路径保持
	wantErrSub(t, "sqrt(x=4)", "关键字参数")
}

// TestNaNComparisons NaN 比较遵循 IEEE 754：== false、!= true、
// < <= > >= 全 false。不得把 NaN 当相等（旧实现 log(-1)==log(-1) 为 true）。
func TestNaNComparisons(t *testing.T) {
	wantBool(t, "log(-1)==log(-1)", false)
	wantBool(t, "log(-1)!=log(-1)", true)
	wantBool(t, "log(-1)<log(-1)", false)
	wantBool(t, "log(-1)<=log(-1)", false)
	wantBool(t, "log(-1)>log(-1)", false)
	wantBool(t, "log(-1)>=log(-1)", false)
	// NaN 与普通数值
	wantBool(t, "log(-1)==1", false)
	wantBool(t, "log(-1)!=1", true)
	wantBool(t, "1!=log(-1)", true)
	wantBool(t, "1==log(-1)", false)
	// NaN 在链中：该段 false → 短路
	wantBool(t, "1<log(-1)<1/0", false)
	// 正常比较不受影响
	wantBool(t, "1<2", true)
	wantBool(t, "2<=2", true)
}

// TestRangeInt64BoundaryDeterministic range 边界确定性：
// int64 边界（含 MinInt64/MaxInt64 量级，经运算构造绕过字面量上限）、
// 负 step、stop-start 溢出、空 range、超大 range——必须有确定预算错误或
// 正确结果，禁止回绕死循环或错误巨大分配。
func TestRangeInt64BoundaryDeterministic(t *testing.T) {
	// 巨大跨度：超 maxListLen 必须快速报错
	for _, expr := range []string{
		"len(range(-(2**62),2**62))",
		"len(range(-(2**62),0))",
		"len(range(-(2**40),0))",
		"len(range(-(2**63),0))",
		"len(range(-10**18,10**18))",
		"len(range(10**18,0,-1))",
		"len(range(10**12))",
	} {
		wantErrSub(t, expr, "超限")
	}
	// int64 边界附近的小跨度：正确计数（big.Int 预算，无回绕）
	wantNum(t, "len(range(-(2**63),-(2**63)+10))", "10")
	wantNum(t, "len(range(-(2**63),-(2**63)))", "0")
	wantNum(t, "sum(range(-(2**63),-(2**63)+3))", "-27670116110564327421")
	wantNum(t, "len(range((2**63)-1,0,-(2**62)))", "2")
	wantNum(t, "len(range(-(2**16),0))", "65536")
	// 负 step 正确性（lo > hi 且 step < 0）
	wantNum(t, "len(range(10,0,-3))", "4")
	wantNum(t, "sum(range(10,0,-3))", "22")
	wantNum(t, "len(range(-50,-100,-10))", "5")
	wantNum(t, "sum(range(-50,-100,-10))", "-350")
	wantNum(t, "len(range(-10**12,-2*10**12,-10**11))", "10")
	// 空 range
	wantNum(t, "len(range(0))", "0")
	wantNum(t, "len(range(5,0))", "0")
	wantNum(t, "len(range(0,5,-1))", "0")
	wantNum(t, "len(range(5,0,1))", "0")
	// 参数类型与 step=0
	wantErrSub(t, "range(0.5,3)", "整数")
	wantErrSub(t, "range(1,5,0)", "step=0")
	wantErrSub(t, "range(True)", "整数")
}

// TestShortCircuitNoMaxSkipResidue 废弃 maxSkip 静默 return：
// 长短路分支不再受 400 token 跳过预算限制（完整解析无预算残留），
// 语法错误照样报。
func TestShortCircuitNoMaxSkipResidue(t *testing.T) {
	// 长比较链短路（>400 token 的场景受 maxExprLen=600 限制，构造接近上限的链）
	long := "1<0<" + strings.Repeat("1+", 150) + "1"
	if len(long) > maxExprLen {
		t.Fatalf("测试表达式超长：%d", len(long))
	}
	wantBool(t, long, false)
	// 同样长度的链 + 尾部 junk：报错（旧 maxSkip 超限静默 return 后残值）
	wantErrSub(t, long+" 5", "多余内容")
	// 长链语法错误（悬挂比较符）
	wantErrSub(t, "1<0<"+strings.Repeat("1+", 150), "记号")
}

/* DEF-VERIFY-COMPREHENSION-SYNTAX-R1 回归：推导式 if 条件的静态语法
   漏检——旧实现条件只做 token 跳过（skipCondTokens），空迭代时条件
   从不被解析，非法 junk（"x 5+1"）静默返回 0。修复后：每个 if 条件
   由同一 parser 静态完整语法校验（suppressEval，不求值），迭代为空
   也必须报语法错误；合法但运行时才失败的边界（1/0）仍按 Python
   语义：空迭代不执行条件 → 0，非空迭代执行条件 → 除零报错。
   期望值均与真实 Python（3.x）逐一核对过。 */

// TestComprehensionCondStaticSyntaxIllegal 非法条件静态必报：
// 空迭代与非空迭代同样校验（不再依赖逐元素重解析兜底）。
func TestComprehensionCondStaticSyntaxIllegal(t *testing.T) {
	cases := []struct{ expr, sub string }{
		// 卡面缺陷：空迭代 + 条件 junk 静默通过（旧实现返回 0）
		{"len([x for x in range(0) if x 5+1])", "多余内容"},
		{"len([x for x in range(0) if x 5])", "多余内容"},
		{"len([1 for x in range(0) if x 5])", "多余内容"},
		// 非空迭代同类：条件 junk 不再被吞
		{"len([1 for x in range(3) if x 5])", "多余内容"},
		{"len([1 for x in range(3) if x 5+1])", "多余内容"},
		// 悬挂比较符（条件解析失败）
		{"len([x for x in range(3) if x<])", "记号"},
		{"len([x for x in range(0) if x<])", "记号"},
		{"len([x for x in range(0) if x+])", "记号"},
		// 生成器（sum(...) 参数推导式）同形态
		{"sum(1 for x in range(0) if x 5)", "多余内容"},
		{"sum(1 for x in range(3) if x 5)", "多余内容"},
		// 嵌套推导式：外层条件里的内层条件 junk 同样报
		{"max([m for m in range(-20,20) if len([x for x in range(-30,30) if x 5 and x<m])==2])", "多余内容"},
		// 条件中裸 = 拒绝（同 Python 表达式契约）
		{"len([x for x in range(3) if x = 1])", "裸 ="},
	}
	for _, c := range cases {
		wantErrSub(t, c.expr, c.sub)
	}
}

// TestComprehensionCondStaticSyntaxLegal 合法条件静态校验通过且语义
// 与 Python 一致（期望值经真实 Python 核对）。
func TestComprehensionCondStaticSyntaxLegal(t *testing.T) {
	// 空迭代：条件永不执行（Python 返回 0，不触发除零）
	wantNum(t, "len([x for x in range(0) if 1/0])", "0")
	wantNum(t, "len([x for x in range(0) if 1/0 and x<5])", "0")
	// 非空迭代：条件执行 → 运行时除零按 Python 报错
	wantErrSub(t, "len([x for x in range(3) if 1/0])", "除以 0")
	// 合法条件正常过滤（含比较链 / and / or / 函数 / 嵌套推导式）
	wantNum(t, "len([x for x in range(3) if x<2<3])", "2")
	wantNum(t, "len([x for x in [1,2,3] if 1<0<(1/0)])", "0")
	wantNum(t, "len([x for x in range(1,21) if x%2==0 and x%3!=0])", "7")
	wantNum(t, "len([x for x in range(1,21) if x%2==0 or x%15==0])", "11")
	// 多层 for（笛卡尔积）+ 条件引用前序变量
	wantNum(t, "len([10*a+b for a in (1,2,3) for b in (1,2,3) if a!=b and (a+b)%3==0])", "2")
	wantNum(t, "sum([a-b for a in [-5,5] for b in [-2,2] if a<b])", "-10")
	// 嵌套推导式在条件中（题库真实形态）
	wantNum(t, "sum(1 for n in range(2,21) if sum(1 for d in range(2,n) if n%d==0)==0)", "8")
	wantNum(t, "max([m for m in range(-20,20) if len([x for x in range(-30,30) if x>2 and x<m])==2])", "5")
	wantNum(t, "len([c for c in [len([x for x in range(1,7) if x>6]), len([x for x in range(1,7) if x<7]), len([x for x in range(1,7) if x==3]), len([x for x in range(1,7) if x%2==0])] if 0<c<6])", "2")
}

// TestComprehensionMultiIfAndForAfterIf 多 if 链（if c1 if c2 == c1 and c2）
// 与 if 后再接 for 子句：条件段边界停在深度 0 的 for/if 关键字，
// 后续子句不吞进条件区间。期望值经真实 Python 核对。
func TestComprehensionMultiIfAndForAfterIf(t *testing.T) {
	// 多 if 链 == and 过滤
	wantNum(t, "len([1 for x in range(10) if x>2 if x<5])", "2")
	wantNum(t, "len([x for x in range(6) if x%2==0 if x>0])", "2")
	// 同一 for 子句：if 后接 for（后续子句变量独立绑定）
	wantNum(t, "len([1 for a in range(3) if a>0 for b in range(3) if b<a])", "3")
	wantNum(t, "len([1 for a in range(3) if a>0 for b in range(a)])", "3")
	// 多 if 中的 junk：第二段条件静态校验仍报
	wantErrSub(t, "len([1 for x in range(10) if x>2 if x 5])", "多余内容")
	wantErrSub(t, "len([1 for a in range(3) if a>0 for b in range(3) if b 5])", "多余内容")
	// 多 if 中空迭代边界：最后一段条件含运行时错误但迭代为空 → 0
	wantNum(t, "len([1 for x in range(0) if x>2 if 1/0])", "0")
}
