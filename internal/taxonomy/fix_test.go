package taxonomy

import (
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"
)

/* TX-CORE-FIX 回归：P0 并发安全与内置函数 arity、Undo 负数参数。
   与 verify_test.go / judge_test.go 互补，聚焦修复点。 */

// P0-1：ancestorSet 惰性缓存并发读写防护（RWMutex double-check）。
// 64 goroutine 并发冷启动填充；-race 下无 data race。
func TestFixAncestorSetConcurrent64(t *testing.T) {
	c := realCatalog(t)
	const workers = 64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			id := c.Topics[(i*97)%len(c.Topics)].ID
			_ = c.Ancestors([]string{id, "cnm_221"})
		}(i)
	}
	close(start)
	wg.Wait()
}

// P0-2：factorial / isqrt 零参数不再 panic（args[0] 越界），统一 arity 校验报错。
func TestFixBuiltinArityNoPanic(t *testing.T) {
	cases := []string{
		"factorial()", "factorial(3,4)", "isqrt()", "isqrt(4,9)",
		"comb(5)", "perm(5,1,2)", "gcd()", "lcm(4)", "abs()", "round()",
		"sum()", "sum(1,2)", "len()", "len([1],2)", "sorted()", "range()",
		"range(1,2,3,4)", "floor()", "ceil()", "int()", "float()",
		"mean()", "median()", "mode()", "pvariance()", "variance()",
		"solve2(1,2)", "roots2(1,2)", "sqrt()", "sin()", "atan2(1)",
		"log(1,2,3)", "Fraction(1,2,3)", "min()", "max()",
	}
	for _, expr := range cases {
		if _, err := (Verifier{}).Eval(expr); err == nil {
			t.Errorf("Eval(%q) 零参/错参应报错", expr)
		}
	}
	// 正常 arity 仍工作
	ok := []struct {
		expr string
		want string
	}{
		{"factorial(0)", "1"},
		{"factorial(5)", "120"},
		{"isqrt(0)", "0"},
		{"isqrt(17)", "4"},
		{"comb(5,2)", "10"},
		{"perm(5,2)", "20"},
		{"gcd(12,8)", "4"},
		{"lcm(4,6)", "12"},
	}
	for _, k := range ok {
		v, err := (Verifier{}).Eval(k.expr)
		if err != nil {
			t.Errorf("Eval(%q): %v", k.expr, err)
			continue
		}
		if v.kind != 'q' || v.rat == nil || v.rat.RatString() != k.want {
			t.Errorf("Eval(%q) = %v，期望 %s", k.expr, v.rat, k.want)
		}
	}
}

// P1：Eval 资源预算——病态表达式必须快速报错（防 OOM / 秒级 CPU），
// 不 panic、不放宽；12012 全量 verify 不受影响（TestBankVerifyExpressions 断言）。
func TestFixEvalResourceBudget(t *testing.T) {
	cases := []string{
		// range/iter 总步数与列表分配
		"len(range(1000000000))",
		"len(range(0,1000000000,1))",
		"len(range(1,2**63))",
		"sum([x for x in range(10**9)])",
		"len([x for x in range(100000) for y in range(100000)])",
		// 字符串分配
		"len('a'*100000000)",
		"len(('a'*50000)*10000)",
		// factorial/comb/perm 参数与结果位
		"len(str(factorial(10**9)))",
		"factorial(999999999999)",
		"comb(10**9,5)",
		"perm(10**9,5)",
		// 幂结果位
		"((10**12)**400)**400", // 嵌套放大，4801 位幂后再 400 次幂，超限
		"10**400**400",         // 深度嵌套幂
		// 步数预算：长表达式 + 推导式重复求值
		"sum([1 for t in range(10**7)])",
		"sum(1 for t in range(10**7))",
	}
	for _, expr := range cases {
		done := make(chan error, 1)
		go func() {
			_, err := (Verifier{}).Eval(expr)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("Eval(%q) 病态表达式未触发预算限制", expr)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("Eval(%q) 超过 5s，预算失效", expr)
		}
	}
	// 合法大结果仍可用（10^12 ** 400 ≈ 4801 位，在预算内）
	v, err := (Verifier{}).Eval("(10**12)**400")
	if err != nil {
		t.Errorf("合法幂 (10**12)**400 不应被预算拒绝: %v", err)
	} else if v.kind != 'q' || len(v.rat.Num().String()) < 4800 {
		t.Errorf("(10**12)**400 结果位数异常: %d", len(v.rat.Num().String()))
	}
	// factorial 上限内正常
	if v, err := (Verifier{}).Eval("factorial(100)"); err != nil || v.rat.RatString() == "" {
		t.Errorf("factorial(100) 应正常求值: %v", err)
	}
}

// P0：verifyOracle 非有限浮点拒绝——verify 结果为 Inf/NaN 时
// big.Rat.SetFloat64 返回 nil，后续 Cmp 会 panic（如 10.0**400 / 1e999）。
// 修复后 oracle 不参与（返回 false），判题安全回退现有路径，不 panic。
func TestFixVerifyOracleNonFinite(t *testing.T) {
	for _, verify := range []string{"10.0**400", "1e999", "-1e999"} {
		q := &Question{ID: "nf", Type: "numeric", Answer: "甲", Verify: verify}
		// 不 panic 即通过；oracle 应拒绝参与
		got := JudgeQuestion(q, "5")
		if got.Verdict != VerdictManual {
			t.Errorf("verify=%s 非有限结果 oracle 应回退 manual，实际 %s/%s", verify, got.Verdict, got.Reason)
		}
		// oracle 显式返回 false（无结论）
		if _, ok := verifyOracle("5", q); ok {
			t.Errorf("verify=%s oracle 不应给出结论", verify)
		}
	}
}

// P1：range 元素数用 big.Int 跨度计算——int64 减法 hi-lo 在
// lo=MinInt64/hi=MaxInt64 边界回绕为负，绕过 maxListLen 检查直接进循环。
// 修复后任何溢出边界先报错，MaxInt64/MinInt64 参数安全拒绝。
func TestFixRangeInt64Overflow(t *testing.T) {
	bad := []string{
		"len(range(9223372036854775807))",                                           // hi = MaxInt64
		"len(range(0,9223372036854775807))",                                         // hi = MaxInt64
		"len(range(-9223372036854775808,0))",                                        // lo = MinInt64
		"len(range(-9223372036854775808,9223372036854775807))",                      // 全跨度
		"len(range(9223372036854775807,0,-1))",                                      // 反向全跨度
		"len(range(-9223372036854775808,-9223372036854775807,9223372036854775807))", // step=MaxInt64
		"len(range(0,10**19))",                                                      // 超 int64
	}
	for _, expr := range bad {
		done := make(chan error, 1)
		go func() {
			_, err := (Verifier{}).Eval(expr)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("Eval(%q) 未触发 range 预算限制", expr)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("Eval(%q) 超过 5s，预算失效", expr)
		}
	}
	// 合法 range 仍工作
	ok := []struct {
		expr string
		want int
	}{
		{"len(range(0))", 0},
		{"len(range(10))", 10},
		{"len(range(2,10,3))", 3},
		{"len(range(10,0,-3))", 4},
		{"len(range(-5,-1))", 4},
	}
	for _, k := range ok {
		v, err := (Verifier{}).Eval(k.expr)
		if err != nil {
			t.Errorf("Eval(%q): %v", k.expr, err)
			continue
		}
		if v.kind != 'q' || v.rat.Num().Int64() != int64(k.want) {
			t.Errorf("Eval(%q) = %v，期望 %d", k.expr, v.rat, k.want)
		}
	}
}

// P1：range 参数必须是精确整数，float/分数参数不静默截断。
func TestFixRangeRequiresIntegerArgs(t *testing.T) {
	for _, expr := range []string{
		"range(0.5,3)", "range(3.7)", "range(1,5.5)", "range(1,10,0.5)",
		"range(1/2)", "range(Fraction(1,2))", "range(True)", "range('3')",
	} {
		if _, err := (Verifier{}).Eval(expr); err == nil {
			t.Errorf("Eval(%q) 非整数参数应报错", expr)
		}
	}
	// 整数值的分数形式仍拒绝（Fraction(4,2) 数值是 2 但类型是精确有理数——
	// 按整数语义处理，IsInt() 检查通过则接受，与 Python range(Fraction(4,2)) 报错一致）
	if _, err := (Verifier{}).Eval("range(Fraction(4,2))"); err != nil {
		t.Errorf("range(Fraction(4,2)) 整数值应接受: %v", err)
	}
}

// P1：comb/perm 循环计步——k 循环每轮一步预算，跨调用累计；
// 单次大 k 结果位数超限拦截，多次小 k 调用累计步数超限拦截。
func TestFixCombPermStepBudget(t *testing.T) {
	for _, k := range []struct {
		expr    string
		wantErr bool
	}{
		{"perm(99999,99998)", true},   // k≈10^5：结果 456569 位 > maxFactDigits，快速报错
		{"perm(100000,100000)", true}, // n^n ≈ 5*10^5 位，同样超位数上限
		{"comb(100000,50000)", false}, // 30101 位结果（在位限内），5*10^4 步 < 200000，单次合法
	} {
		if _, err := (Verifier{}).Eval(k.expr); (err != nil) != k.wantErr {
			t.Errorf("Eval(%q) err=%v，期望报错=%v", k.expr, err, k.wantErr)
		}
	}
	// 循环计步跨调用累计：200 次 comb(100000,50000)（每次 5*10^4 步）
	// 累计 10^7 步 >> maxEvalSteps，必须在秒级内报错（修复前只靠
	// 结果位数上限，200 次调用每次都全量计算 30101 位结果）
	expr := "sum([comb(100000,50000) for _ in range(200)])"
	done := make(chan error, 1)
	go func() {
		_, err := (Verifier{}).Eval(expr)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Errorf("200 次 comb 调用应触发步数预算")
		}
	case <-time.After(5 * time.Second):
		t.Errorf("200 次 comb 调用超过 5s，预算失效")
	}
	// 单调用大 k 步数也有限：comb(100000,50000) 的 5*10^4 步 < 200000，正常通过
	// 小 k 正常且数值正确
	if v, err := (Verifier{}).Eval("comb(100000,3)"); err != nil || v.kind != 'q' {
		t.Errorf("comb(100000,3) 小 k 应正常: %v %v", v.kind, err)
	} else if want := new(big.Rat).SetInt64(166661666700000); v.rat.Cmp(want) != 0 {
		t.Errorf("comb(100000,3) 数值不符: %v", v.rat)
	}
	if v, err := (Verifier{}).Eval("perm(100000,3)"); err != nil || v.kind != 'q' {
		t.Errorf("perm(100000,3) 小 k 应正常: %v %v", v.kind, err)
	} else if want := new(big.Rat).SetInt64(999970000200000); v.rat.Cmp(want) != 0 {
		t.Errorf("perm(100000,3) 数值不符: %v", v.rat)
	}
}

// P1：verifyOracle 带单位输入不对拍——3cm 与无单位期望 3 数值相等但语义不同，
// oracle 必须回退现有路径，不可误判。
func TestFixVerifyOracleUnitMismatch(t *testing.T) {
	// verify 期望纯数值 3；用户答 3cm（单位 cm）
	q := &Question{ID: "u1", Type: "numeric", Answer: "甲", Verify: "1+2"}
	got := JudgeQuestion(q, "3cm")
	if got.Verdict != VerdictManual {
		t.Errorf("3cm vs 无单位期望 3：oracle 应回退，实际 %s/%s", got.Verdict, got.Reason)
	}
	if _, ok := verifyOracle("3cm", q); ok {
		t.Errorf("带单位输入 oracle 不应给出结论")
	}
	// 同样 3m、3° 等带单位输入都不对拍（% 在非 percent 题里 parseNumeric
	// 会折算成 0.03 数值，但题不是 percent 题时不该把 30% 当 0.3 对拍——
	// 这里只测单位输入，% 语义由 percent 题分支处理）
	for _, in := range []string{"3m", "3°"} {
		if _, ok := verifyOracle(in, q); ok {
			t.Errorf("输入 %q 不应触发 oracle", in)
		}
	}
	// 无单位纯数值正常对拍
	if got := JudgeQuestion(q, "3"); got.Verdict != VerdictCorrect || got.Reason != ReasonVerify {
		t.Errorf("纯数值 3 应判 correct/verify，实际 %s/%s", got.Verdict, got.Reason)
	}
	// percent 题：DEF-JUDGE-ORACLE-R1 后 oracle 一律不参与（不猜 verify 的
	// 比例/百分数缩放）；answer=甲 无可用数值参考，回退既有 manual 路径。
	qp := &Question{ID: "u2", Type: "numeric", Answer: "甲", Percent: true, Verify: "50"}
	if got := JudgeQuestion(qp, "50%"); got.Verdict != VerdictManual {
		t.Errorf("percent 题 oracle 应回退既有 manual 路径，实际 %s/%s", got.Verdict, got.Reason)
	}
	// percent 题 deterministic 路径不受影响：answer 是可比较数值时照常判 correct
	qp2 := &Question{ID: "u3", Type: "numeric", Answer: "50", Percent: true, Verify: "50"}
	if got := JudgeQuestion(qp2, "50%"); got.Verdict != VerdictCorrect {
		t.Errorf("percent 题 answer=50 user=50%% 应判 correct，实际 %s/%s", got.Verdict, got.Reason)
	}
}

// P0：str*int 字符串重复预算——strings.Repeat 前必须检查结果字节数，
// 防 len('a'*(10**12)) 直接分配 1TB。超限快速报错，不 OOM。
func TestFixStrRepeatBudget(t *testing.T) {
	bad := []string{
		"len('a'*(10**12))",
		"len(('ab')*(10**12))",
		"len('a'*1000000000000)",
		"len('ab'*500000000)",
		"len(('a'*1000)*1000000)", // 嵌套 repeat：内层合法，外层超限
		"len('a'*(-1))",
	}
	for _, expr := range bad {
		done := make(chan error, 1)
		go func() {
			_, err := (Verifier{}).Eval(expr)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("Eval(%q) 未触发 str*int 预算限制", expr)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("Eval(%q) 超过 5s，预算失效", expr)
		}
	}
	// 合法 repeat 仍工作（len/下标只支持列表，字符串用比较验证）
	ok := []struct {
		expr string
		want bool
	}{
		{"'a'*3 == 'aaa'", true},
		{"'a'*0 == ''", true},
		{"'ab'*2 == 'abab'", true},
		{"'a'*3 == 'aa'", false},
	}
	for _, k := range ok {
		v, err := (Verifier{}).Eval(k.expr)
		if err != nil {
			t.Errorf("Eval(%q): %v", k.expr, err)
			continue
		}
		if v.kind != 'b' || v.b != k.want {
			t.Errorf("Eval(%q) 结果异常: %v/%v，期望 %v", k.expr, v.kind, v.b, k.want)
		}
	}
}

// P2：ratSqrt 就地改写回归——big.Int.Mul 就地改写接收者，平方验证曾把
// rn/rd 改写成平方值后返回（ratSqrt(16) 返回 16 而非 4），roots2 的
// disc>1（4/16/25/36）全错。修复后各 disc 档位全部正确。
func TestFixRatSqrtInPlaceMutation(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"roots2(1,0,-4)", "[-2, 2]"},  // disc=16
		{"roots2(1,-5,6)", "[2, 3]"},   // disc=25
		{"roots2(1,-4,4)", "[2]"},      // disc=0，重根单元素
		{"roots2(1,0,-36)", "[-6, 6]"}, // disc=144
		{"solve2(1,1,3,1,2,4)", "[2, 1]"},
	}
	for _, k := range cases {
		v, err := (Verifier{}).Eval(k.expr)
		if err != nil {
			t.Errorf("Eval(%q): %v", k.expr, err)
			continue
		}
		var got string
		switch v.kind {
		case 'q':
			got = v.rat.RatString()
		case 'l':
			parts := make([]string, 0, len(v.list))
			for _, it := range v.list {
				if it.kind != 'q' {
					t.Errorf("Eval(%q) 列表含非分数项", k.expr)
				}
				parts = append(parts, it.rat.RatString())
			}
			got = "[" + strings.Join(parts, ", ") + "]"
		default:
			t.Errorf("Eval(%q) 结果类型 %q 异常", k.expr, v.kind)
			continue
		}
		if got != k.want {
			t.Errorf("Eval(%q) = %s，期望 %s", k.expr, got, k.want)
		}
	}
	// sqrt() 走浮点路径（返回 float），ratSqrt 只在 roots2 内部参与求根
	if v, err := (Verifier{}).Eval("sqrt(16)"); err != nil || v.kind != 'f' || v.f != 4 {
		t.Errorf("sqrt(16) 应为浮点 4: %v %v", v, err)
	}
}

// 合法但步数多的表达式仍通过（12012 全量的最大迭代规模为 range(100)，见下）。
func TestFixEvalBudgetAllowsRealWorkload(t *testing.T) {
	exprs := []string{
		// 全量数据里最大的真实表达式（数据基线：最大 range(100)）
		"min([x for x in range(100) if x>6 and x>4])",
		"[Fraction(len([1 for t in range(36) if (t//6+t%6)%2==0]),36), Fraction(len([1 for t in range(36) if (t//6+1)*(t%6+1)%2==0]),36)]",
		"max([(x-30)*(160-2*x) for x in range(30,51)])",
		"sum(1 for n in range(2,21) if sum(1 for d in range(2,n) if n%d==0)==0)",
		"Fraction(len([1 for t in range(27) if t//9!=t//3%3 and t//3%3!=t%3 and t//9!=t%3]),27)",
		"sum([x*x for x in range(1,1000)])", // 999 元素推导式，留裕量
		"sum([[63,582,63,417,298][i] for i in range(5) if [63,582,63,417,298][i]<=500 and i!=2])",
	}
	for _, expr := range exprs {
		if _, err := (Verifier{}).Eval(expr); err != nil {
			t.Errorf("Eval(%q) 合法工作负载被预算误杀: %v", expr, err)
		}
	}
}
