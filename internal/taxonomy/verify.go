package taxonomy

import (
	"fmt"
	"math/big"
	"strings"
)

/* 受限 verify 表达式解释器：移植上游 scripts/verify_questions.py 的
   AST 白名单求值语义（四则、**、//、%、比较、推导式、元组/列表、下标、
   IfExp），提供 Fraction/sqrt/pi/... 等白名单函数。
   不允许 import、属性访问、赋值、任意代码执行；不依赖任何运行时。 */

// VerifyError 表示 verify 表达式非法或求值失败。
type VerifyError struct{ msg string }

func (e *VerifyError) Error() string { return e.msg }

func verifyErrf(format string, args ...any) error {
	return &VerifyError{msg: sprintf(format, args...)}
}

// Verifier 是受限表达式求值器。表达式集合在题库中固定，解释器按白名单
// 递归下降解析，不编译不执行任何外来代码。
type Verifier struct{}

// vValue 是解释器内部的值：分数（big.Rat）、浮点、字符串、布尔、列表。
type vValue struct {
	kind byte // 'q' rat, 'f' float, 's' string, 'b' bool, 'l' list, 'n' nil
	rat  *big.Rat
	f    float64
	s    string
	b    bool
	list []vValue
}

func vRat(r *big.Rat) vValue  { return vValue{kind: 'q', rat: r} }
func vFloat(f float64) vValue { return vValue{kind: 'f', f: f} }
func vStr(s string) vValue    { return vValue{kind: 's', s: s} }
func vBool(b bool) vValue     { return vValue{kind: 'b', b: b} }
func vList(l []vValue) vValue { return vValue{kind: 'l', list: l} }

// vNum 统一数值视图。
func (v vValue) isNum() bool { return v.kind == 'q' || v.kind == 'f' || v.kind == 'b' }
func (v vValue) asFloat() float64 {
	switch v.kind {
	case 'q':
		f, _ := v.rat.Float64()
		return f
	case 'f':
		return v.f
	case 'b':
		if v.b {
			return 1
		}
	}
	return 0
}

/* ---- 词法 ---- */

type vTokKind byte

const (
	tkEOF vTokKind = iota
	tkNum
	tkStr
	tkName
	tkOp
)

type vToken struct {
	kind    vTokKind
	s       string // 原文或名字
	num     *big.Rat
	isFloat bool // 数字字面量是否为 Python float 形式（含 . 或 e/E）
}

// vOps 优先匹配多字符运算符；裸 `=` 单字符由 default 分支产出
// （Python 表达式语法无 `=`，comparison 层按契约拒绝）。
var vOps = []string{"**", "//", "<=", ">=", "==", "!=", "<>", "->"}

type vLexer struct {
	src  string
	pos  int
	toks []vToken
}

func lex(expr string) ([]vToken, error) {
	l := &vLexer{src: expr}
	for {
		l.skipSpace()
		if l.pos >= len(l.src) {
			break
		}
		c := l.src[l.pos]
		switch {
		case c >= '0' && c <= '9' || c == '.' && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1]):
			start := l.pos
			for l.pos < len(l.src) && (isDigit(l.src[l.pos]) || l.src[l.pos] == '.') {
				l.pos++
			}
			// 科学计数法：数字后跟 e/E（可带 +- 号）再跟数字（Python 浮点字面量）
			if l.pos < len(l.src) && (l.src[l.pos] == 'e' || l.src[l.pos] == 'E') {
				expEnd := -1
				j := l.pos + 1
				if j < len(l.src) && (l.src[j] == '+' || l.src[j] == '-') {
					j++
				}
				if j < len(l.src) && isDigit(l.src[j]) {
					for j < len(l.src) && isDigit(l.src[j]) {
						j++
					}
					expEnd = j
				}
				if expEnd > 0 {
					l.pos = expEnd
				}
			}
			numStr := l.src[start:l.pos]
			r, ok := new(big.Rat).SetString(numStr)
			if !ok {
				return nil, verifyErrf("非法数字: %s", numStr)
			}
			// 含小数点或 e/E 的字面量在 Python 里是 float 常量（不受 10^12 整数上限约束），
			// 用 float64 视图保留该语义；纯整数字面量保持有理数精确表示。
			isFloatLit := strings.ContainsAny(numStr, ".eE")
			l.toks = append(l.toks, vToken{kind: tkNum, num: r, isFloat: isFloatLit})
		case c == '"' || c == '\'':
			start := l.pos
			l.pos++
			var sb strings.Builder
			closed := false
			for l.pos < len(l.src) {
				if l.src[l.pos] == '\\' && l.pos+1 < len(l.src) {
					l.pos++
					sb.WriteByte(l.src[l.pos])
					l.pos++
					continue
				}
				if l.src[l.pos] == c {
					l.pos++
					closed = true
					break
				}
				sb.WriteByte(l.src[l.pos])
				l.pos++
			}
			if !closed {
				return nil, verifyErrf("字符串未闭合 @%d", start)
			}
			l.toks = append(l.toks, vToken{kind: tkStr, s: sb.String()})
		case isIdentStart(c):
			start := l.pos
			for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
				l.pos++
			}
			l.toks = append(l.toks, vToken{kind: tkName, s: l.src[start:l.pos]})
		default:
			matched := ""
			for _, op := range vOps {
				if strings.HasPrefix(l.src[l.pos:], op) {
					matched = op
					break
				}
			}
			if matched == "" {
				if strings.ContainsRune("+-*/%(),[]<>!=&|~^", rune(c)) {
					matched = string(c)
				} else {
					return nil, verifyErrf("非法字符 %q @%d", string(c), l.pos)
				}
			}
			l.toks = append(l.toks, vToken{kind: tkOp, s: matched})
			l.pos += len(matched)
		}
	}
	l.toks = append(l.toks, vToken{kind: tkEOF})
	return l.toks, nil
}

func (l *vLexer) skipSpace() {
	for l.pos < len(l.src) && (l.src[l.pos] == ' ' || l.src[l.pos] == '\t' || l.src[l.pos] == '\n' || l.src[l.pos] == '\r') {
		l.pos++
	}
}

func isDigit(c byte) bool      { return c >= '0' && c <= '9' }
func isIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isIdentPart(c byte) bool  { return isIdentStart(c) || isDigit(c) }

/* ---- 语法与求值（递归下降，白名单严格限定） ---- */

type vParser struct {
	toks  []vToken
	pos   int
	env   map[string]vValue // 绑定变量（推导式目标名）
	steps int               // 资源预算：求值步数计数

	// validateOnly 为 true 时进入"语法校验"模式：走与真实求值完全相同的
	// 递归下降解析路径消费 token，但跳过一切求值点（函数调用、除法、
	// range、下标、比较结果计算等）。用于短路/惰性分支：右侧或未选中
	// 分支必须完成语法解析（非法 junk、悬挂运算符、未闭合括号都要报
	// VerifyError），只是不求值（除零等运行时错误不发生）。
	// 关键不变量：同一 token 区间在 validateOnly 与真实求值下消费路径
	// 完全一致（短路分支同样顺序解析整个剩余链），位置才能对齐。
	validateOnly bool
}

// suppressEval 进入语法校验模式，返回恢复函数（配对调用）。
func (p *vParser) suppressEval() func() {
	old := p.validateOnly
	p.validateOnly = true
	return func() { p.validateOnly = old }
}

// vNil 占位值：validateOnly 模式下各产生式的返回值（不求值，值无意义）。
func vNil() vValue { return vValue{kind: 'n'} }

// 白名单函数名（与上游 NAMES 一致）。
var vAllowedNames = map[string]bool{
	"Fraction": true, "sqrt": true, "pi": true, "e": true, "sin": true, "cos": true,
	"tan": true, "asin": true, "acos": true, "atan": true, "atan2": true,
	"radians": true, "degrees": true, "log": true, "log10": true, "log2": true,
	"exp": true, "factorial": true, "comb": true, "perm": true, "gcd": true,
	"lcm": true, "abs": true, "round": true, "sum": true, "min": true, "max": true,
	"len": true, "sorted": true, "range": true, "floor": true, "ceil": true,
	"isqrt": true, "int": true, "float": true, "mean": true, "median": true,
	"pvariance": true, "variance": true, "mode": true, "solve2": true, "roots2": true,
	"True": true, "False": true, "None": true,
}

const maxExprLen = 600
const maxConst = "1000000000000" // 10^12

/* ---- 资源预算（防 OOM / 秒级 CPU 消耗） ----
   Eval 是无 context 的纯函数（JudgeQuestion 内联调用），预算内部计数：
   超限返回 VerifyError，不 panic、不放宽。 */

const (
	maxEvalSteps  = 200000  // 求值步数（每个二元运算/函数调用/元素产出计 1 步）
	maxListLen    = 100000  // 列表/字符串最大元素数（range/推导式产出）
	maxStrAlloc   = 1000000 // 字符串分配上限（str*int 结果字节数）
	maxFactArg    = 10000   // factorial 参数上限
	maxCombPermN  = 100000  // comb/perm 的 n 上限
	maxFactDigits = 100000  // factorial/comb/perm 结果十进制位数上限
	maxPowResult  = 100000  // 幂结果有理数位上限（分子+分母十进制位数）
)

// step 计一步求值开销，超预算返回错误。
func (p *vParser) step() error {
	p.steps++
	if p.steps > maxEvalSteps {
		return verifyErrf("求值步数超限")
	}
	return nil
}

// Eval 求值一个受限 verify 表达式。
func (Verifier) Eval(expr string) (vValue, error) {
	if len(expr) > maxExprLen {
		return vValue{}, verifyErrf("表达式过长")
	}
	toks, err := lex(expr)
	if err != nil {
		return vValue{}, err
	}
	p := &vParser{toks: toks, env: map[string]vValue{}}
	v, err := p.expr()
	if err != nil {
		return vValue{}, err
	}
	if p.cur().kind != tkEOF {
		return vValue{}, verifyErrf("表达式末尾有多余内容: %s", p.cur().s)
	}
	return v, nil
}

func (p *vParser) cur() vToken { return p.toks[p.pos] }
func (p *vParser) next() vToken {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}
func (p *vParser) isOp(s string) bool {
	t := p.cur()
	return t.kind == tkOp && t.s == s
}
func (p *vParser) eatOp(s string) bool {
	if p.isOp(s) {
		p.next()
		return true
	}
	return false
}

// lookAheadFor 判断 token 区间 [from, 闭包 token) 内是否出现顶层 "for"
// 子句（推导式探测：模板解析失败但结构是推导式时重走用）。
// 顶层 = 不在嵌套括号/方括号内。闭包 token（")" / "]"）由调用方给出
// 上下文（expr 解析失败时 pos 可能停在模板中间，需从区间头部扫描）。
func (p *vParser) lookAheadFor(from int) bool {
	depth := 0
	for i := from; i < len(p.toks); i++ {
		t := p.toks[i]
		if t.kind == tkEOF {
			return false
		}
		if t.kind == tkOp {
			switch t.s {
			case "(", "[":
				depth++
			case ")", "]":
				if depth == 0 {
					// 到达本层闭包：没找到 for
					return false
				}
				depth--
			}
			continue
		}
		if depth == 0 && t.kind == tkName && t.s == "for" {
			return true
		}
	}
	return false
}

// expr: or_test [if or_test else expr] | or_test
// IfExp 惰性：条件为真只执行 then 分支、为假只执行 else 分支，
// 未选中分支走 suppressEval 做完整语法解析（token 消费与真实求值一致），
// 非法语法仍报 VerifyError，运行时错误（除零等）不发生。
func (p *vParser) expr() (vValue, error) {
	v, err := p.orTest()
	if err != nil {
		return vValue{}, err
	}
	if p.cur().kind == tkName && p.cur().s == "if" {
		p.next()
		cond, err := p.orTest()
		if err != nil {
			return vValue{}, err
		}
		if !(p.cur().kind == tkName && p.cur().s == "else") {
			return vValue{}, verifyErrf("if 表达式缺少 else")
		}
		p.next()
		// validateOnly 模式（外层短路/未选中分支）下：then 已由上方 orTest
		// 解析，条件真假无法判定（cond 是占位值），else 分支按同一 expr()
		// 语法解析后整体返回占位值。不在校验模式下做"条件必须是布尔"检查
		// ——占位值恒非布尔，真实求值时才由下方检查保证布尔性。
		if p.validateOnly {
			if _, err := p.expr(); err != nil {
				return vValue{}, err
			}
			return vNil(), nil
		}
		if cond.kind != 'b' {
			return vValue{}, verifyErrf("if 条件必须是布尔")
		}
		// 只解析选中分支；未选中分支 suppressEval 完整语法解析（校验不出错、
		// 不求值）。两个分支都解析过，语句消费总量与真实求值一致。
		thenVal := v
		if cond.b {
			restore := p.suppressEval()
			_, err = p.expr()
			restore()
			if err != nil {
				return vValue{}, err
			}
			return thenVal, nil
		}
		// 条件为假：then 已解析（它就是 orTest），else 分支真实求值。
		elseVal, err := p.expr()
		if err != nil {
			return vValue{}, err
		}
		return elseVal, nil
	}
	return v, nil
}

// orTest: Python `or` 短路：左操作数 truthy 时直接返回左操作数值（不求值
// 右侧——`1 or 1/0` 返回 1 不报除零），否则返回右操作数值（`0 or 5` 返回 5，
// 不是 true）。右侧走同一递归下降解析：truthy 时 suppressEval 完整语法
// 解析（非法语法报 VerifyError，求值不发生）。
func (p *vParser) orTest() (vValue, error) {
	v, err := p.andTest()
	if err != nil {
		return vValue{}, err
	}
	for p.cur().kind == tkName && p.cur().s == "or" {
		p.next() // 吃掉 or
		b, err := vTruthy(v)
		if err != nil {
			return vValue{}, err
		}
		if b {
			// 短路：右侧不求值，但语法必须完整（suppressEval 走同一 parser）。
			restore := p.suppressEval()
			_, err = p.andTest()
			restore()
			if err != nil {
				return vValue{}, err
			}
			continue // v 保持左操作数值（Python 返回左操作数）
		}
		r, err := p.andTest()
		if err != nil {
			return vValue{}, err
		}
		v = r // 左 falsy：结果为右操作数值
	}
	return v, nil
}

// andTest: Python `and` 短路：左操作数 falsy 时返回左操作数值（不求值
// 右侧——`0 and 1/0` 返回 0），否则返回右操作数值（`1 and 2` 返回 2）。
// 右侧走同一递归下降解析：falsy 时 suppressEval 完整语法解析。
func (p *vParser) andTest() (vValue, error) {
	v, err := p.notTest()
	if err != nil {
		return vValue{}, err
	}
	for p.cur().kind == tkName && p.cur().s == "and" {
		p.next() // 吃掉 and
		b, err := vTruthy(v)
		if err != nil {
			return vValue{}, err
		}
		if !b {
			// 短路：右侧不求值，但语法必须完整。
			restore := p.suppressEval()
			_, err = p.notTest()
			restore()
			if err != nil {
				return vValue{}, err
			}
			continue // v 保持左操作数值（Python 返回左操作数）
		}
		r, err := p.notTest()
		if err != nil {
			return vValue{}, err
		}
		v = r // 左 truthy：结果为右操作数值
	}
	return v, nil
}

func (p *vParser) notTest() (vValue, error) {
	if p.cur().kind == tkName && p.cur().s == "not" {
		p.next()
		v, err := p.notTest()
		if err != nil {
			return vValue{}, err
		}
		b, err := vTruthy(v)
		if err != nil {
			return vValue{}, err
		}
		return vBool(!b), nil
	}
	return p.comparison()
}

// comparison: Python 链式比较语义（1<2<3 == (1<2) and (2<3)）：
// 操作数从左到右惰性求值，每个最多一次；某段比较为 false 时立即返回
// false，其后剩余链段只做语法解析不求值（1<0<(1/0) 为 false 且无除零，
// 1<2<(1/0) 才报除零）。剩余链段走同一递归下降 parser（suppressEval）：
// 非法 junk（"1<0<2 3"、"1<0<"）与悬挂比较符必须报 VerifyError，
// 不能被静默吞掉。
func (p *vParser) comparison() (vValue, error) {
	left, err := p.arith()
	if err != nil {
		return vValue{}, err
	}
	prev := left
	hasComp := false
	for {
		var op string
		if p.cur().kind == tkOp {
			switch p.cur().s {
			case "<", ">", "<=", ">=", "==", "!=", "<>", "=":
				op = p.cur().s
			}
		} else if p.cur().kind == tkName && (p.cur().s == "in" || p.cur().s == "not") {
			return vValue{}, verifyErrf("暂不支持 in / not in 比较")
		}
		if op == "" {
			break // 非比较运算符：比较结束，交还上层
		}
		// 裸 `=`：Python 表达式语法不存在（只有赋值语句）。按表达式契约
		// 明确拒绝，不静默当作 ==。单个 = 已由 vOps/lexer 产出 tkOp "="。
		if op == "=" {
			return vValue{}, verifyErrf("不允许赋值/裸 =（比较请用 ==）")
		}
		if op == "<>" {
			op = "!="
		}
		p.next()
		// 右操作数：剩余链段（已确定 false）走 suppressEval 只做语法解析；
		// 真实求值路径照常求值。这里用同一 arith 调用保证 token 消费一致。
		r, err := p.arith()
		if err != nil {
			return vValue{}, err
		}
		if p.validateOnly {
			// 校验模式：不求值，继续解析整条链的剩余段。
			hasComp = true
			prev = r
			continue
		}
		c, err := vCompare(prev, r)
		if err != nil {
			return vValue{}, err
		}
		var ok bool
		if c == 2 {
			// NaN 比较（vCompare 特殊标记）：除 != 外所有比较运算符均为 false。
			ok = op == "!="
		} else {
			switch op {
			case "<":
				ok = c < 0
			case ">":
				ok = c > 0
			case "<=":
				ok = c <= 0
			case ">=":
				ok = c >= 0
			case "==":
				ok = c == 0
			case "!=":
				ok = c != 0
			}
		}
		hasComp = true
		if !ok {
			// 短路：剩余链段（比较运算符 + 操作数）只做语法解析，不求值
			// （右侧可能除零/越界，Python 同样不求值）。
			restore := p.suppressEval()
			for {
				var op2 string
				if p.cur().kind == tkOp {
					switch p.cur().s {
					case "<", ">", "<=", ">=", "==", "!=", "<>", "=":
						op2 = p.cur().s
					}
				}
				if op2 == "" {
					break
				}
				if op2 == "=" {
					restore()
					return vValue{}, verifyErrf("不允许赋值/裸 =（比较请用 ==）")
				}
				p.next()
				if _, err := p.arith(); err != nil {
					restore()
					return vValue{}, err
				}
			}
			restore()
			return vBool(false), nil
		}
		prev = r
	}
	if !hasComp {
		// 无比较运算符：直接返回左操作数（算术结果）
		return left, nil
	}
	if p.validateOnly {
		return vNil(), nil
	}
	return vBool(true), nil
}

// arith: 加减。validateOnly 模式下只解析 token，运算点不求值（值恒为
// vNil 占位），消费路径与真实求值一致。
func (p *vParser) arith() (vValue, error) {
	v, err := p.term()
	if err != nil {
		return vValue{}, err
	}
	for {
		if !p.validateOnly {
			if err := p.step(); err != nil {
				return vValue{}, err
			}
		}
		if p.eatOp("+") {
			r, err := p.term()
			if err != nil {
				return vValue{}, err
			}
			if p.validateOnly {
				v = vNil()
				continue
			}
			v, err = vAdd(v, r)
			if err != nil {
				return vValue{}, err
			}
		} else if p.eatOp("-") {
			r, err := p.term()
			if err != nil {
				return vValue{}, err
			}
			if p.validateOnly {
				v = vNil()
				continue
			}
			v, err = vSub(v, r)
			if err != nil {
				return vValue{}, err
			}
		} else {
			break
		}
	}
	return v, nil
}

// term: 乘除。validateOnly 模式下除法/模不做除零检查（右侧可能含 1/0）。
func (p *vParser) term() (vValue, error) {
	v, err := p.factor()
	if err != nil {
		return vValue{}, err
	}
	for {
		if !p.validateOnly {
			if err := p.step(); err != nil {
				return vValue{}, err
			}
		}
		switch {
		case p.eatOp("*"):
			r, err := p.factor()
			if err != nil {
				return vValue{}, err
			}
			if p.validateOnly {
				v = vNil()
				continue
			}
			v, err = vMul(v, r)
			if err != nil {
				return vValue{}, err
			}
		case p.eatOp("//"):
			r, err := p.factor()
			if err != nil {
				return vValue{}, err
			}
			if p.validateOnly {
				v = vNil()
				continue
			}
			v, err = vFloorDiv(v, r)
			if err != nil {
				return vValue{}, err
			}
		case p.eatOp("/"):
			r, err := p.factor()
			if err != nil {
				return vValue{}, err
			}
			if p.validateOnly {
				v = vNil()
				continue
			}
			v, err = vDiv(v, r)
			if err != nil {
				return vValue{}, err
			}
		case p.eatOp("%"):
			r, err := p.factor()
			if err != nil {
				return vValue{}, err
			}
			if p.validateOnly {
				v = vNil()
				continue
			}
			v, err = vMod(v, r)
			if err != nil {
				return vValue{}, err
			}
		default:
			return v, nil
		}
	}
}

// factor: 一元正负、幂。validateOnly 模式下不求值。
func (p *vParser) factor() (vValue, error) {
	if p.eatOp("-") {
		v, err := p.factor()
		if err != nil {
			return vValue{}, err
		}
		if p.validateOnly {
			return vNil(), nil
		}
		return vNeg(v)
	}
	if p.eatOp("+") {
		return p.factor()
	}
	return p.power()
}

func (p *vParser) power() (vValue, error) {
	v, err := p.atom()
	if err != nil {
		return vValue{}, err
	}
	// 后缀下标链（上游 ALLOWED_NODES 的 Subscript）：sorted(...)[2] 等
	for p.isOp("[") {
		v, err = p.subscript(v)
		if err != nil {
			return vValue{}, err
		}
	}
	if p.eatOp("**") {
		// 右结合
		r, err := p.factor()
		if err != nil {
			return vValue{}, err
		}
		if p.validateOnly {
			return vNil(), nil
		}
		return vPow(v, r)
	}
	return v, nil
}

// atom: 数字/字符串/名字/名字(参数)/元组/列表/下标/推导式/括号。
// validateOnly 模式：常量上限检查保留（语法层约束），白名单名字检查保留
// （推导式变量探测依赖它），但推导式/调用不求值——推导式在 validateOnly
// 下只解析结构（产出空列表），调用直接返回占位值。
func (p *vParser) atom() (vValue, error) {
	t := p.cur()
	switch t.kind {
	case tkNum:
		p.next()
		// 上游只限制整数字面量 ≤ 10^12；float 字面量（含 . / e/E，Python 恒为 float）
		// 不受此限制。float 字面量按 float64 语义求值（与 Python eval 一致）。
		if t.isFloat {
			f, _ := t.num.Float64()
			return vFloat(f), nil
		}
		if t.num.Cmp(new(big.Rat).SetInt(mustParseBig(maxConst))) > 0 {
			return vValue{}, verifyErrf("整数常量过大")
		}
		return vRat(t.num), nil
	case tkStr:
		p.next()
		return vStr(t.s), nil
	case tkName:
		p.next()
		switch t.s {
		case "True":
			return vBool(true), nil
		case "False":
			return vBool(false), nil
		case "None":
			return vValue{kind: 'n'}, nil
		}
		// 推导式绑定变量
		if v, ok := p.env[t.s]; ok {
			return v, nil
		}
		if !vAllowedNames[t.s] {
			// 未绑定且不在白名单：可能是推导式模板里的迭代变量（解析探测阶段
			// 尚未绑定）。交给上层探测逻辑判断：如果后续是 for 子句就按推导式走。
			return vValue{}, verifyErrf("不允许的名字: %s", t.s)
		}
		if p.isOp("(") {
			if p.validateOnly {
				// 校验模式：参数语法照常解析，但不执行函数（range/函数
				// 求值与副作用都不发生）。
				if err := p.validateCallArgs(); err != nil {
					return vValue{}, err
				}
				return vNil(), nil
			}
			return p.call(t.s)
		}
		if p.validateOnly {
			// 校验模式常量：pi/e 等占位（vConst 白名单外名字报错路径
			// 不变——不在白名单的名字已在上面拦截）。
			switch t.s {
			case "pi", "e":
				return vNil(), nil
			}
			return vNil(), nil
		}
		return vConst(t.s)
	case tkOp:
		switch t.s {
		case "(":
			p.next()
			// 空 tuple "()"
			if p.eatOp(")") {
				return vList(nil), nil
			}
			// 推导式回退探测：解析首元素前记录起点；若是推导式则重走
			tmplStart := p.pos
			first, err := p.expr()
			if err != nil {
				if p.lookAheadFor(tmplStart) {
					p.pos = tmplStart
					return p.comprehension(tmplStart, ")")
				}
				return vValue{}, err
			}
			if p.cur().kind == tkName && p.cur().s == "for" {
				p.pos = tmplStart
				return p.comprehension(tmplStart, ")")
			}
			if p.eatOp(",") {
				items := []vValue{first}
				for {
					if p.eatOp(")") {
						break
					}
					it, err := p.expr()
					if err != nil {
						return vValue{}, err
					}
					items = append(items, it)
					if !p.eatOp(",") {
						if !p.eatOp(")") {
							return vValue{}, verifyErrf("元组缺少 )")
						}
						break
					}
				}
				if p.validateOnly {
					return vNil(), nil
				}
				return vList(items), nil
			}
			if !p.eatOp(")") {
				return vValue{}, verifyErrf("括号表达式缺少 )")
			}
			return first, nil
		case "[":
			p.next()
			if p.eatOp("]") {
				return vList(nil), nil
			}
			tmplStart := p.pos
			first, err := p.expr()
			if err != nil {
				// 模板首元素可能引用了尚未绑定的推导式变量：
				// 若接下来是 for，则按推导式重走；否则返回原错误
				if p.lookAheadFor(tmplStart) {
					p.pos = tmplStart
					return p.comprehension(tmplStart, "]")
				}
				return vValue{}, err
			}
			if p.cur().kind == tkName && p.cur().s == "for" {
				p.pos = tmplStart
				return p.comprehension(tmplStart, "]")
			}
			items := []vValue{first}
			for {
				if p.eatOp("]") {
					break
				}
				if !p.eatOp(",") {
					return vValue{}, verifyErrf("列表缺少 , 或 ]")
				}
				if p.eatOp("]") {
					break
				}
				it, err := p.expr()
				if err != nil {
					return vValue{}, err
				}
				items = append(items, it)
			}
			if p.validateOnly {
				return vNil(), nil
			}
			return vList(items), nil
		}
	}
	return vValue{}, verifyErrf("意外的记号 %q", t.s)
}

// validateCallArgs 校验模式下的调用参数解析：语法结构与 call() 一致
// （关键字参数检测、生成器探测、逗号分隔与闭括号），但不执行 vCall。
func (p *vParser) validateCallArgs() error {
	p.next() // (
	if !p.eatOp(")") {
		for {
			if p.cur().kind == tkName {
				// 关键字参数检测：name 后跟 '='（不是 '=='）
				save := p.pos
				p.next()
				if p.isOp("=") {
					return verifyErrf("不允许关键字参数")
				}
				p.pos = save
			}
			argStart := p.pos
			_, err := p.expr()
			gen := false
			if err != nil {
				if p.lookAheadFor(argStart) {
					p.pos = argStart
					if _, err = p.comprehension(argStart, ")"); err != nil {
						return err
					}
					gen = true
				} else {
					return err
				}
			} else if p.cur().kind == tkName && p.cur().s == "for" {
				p.pos = argStart
				if _, err = p.comprehension(argStart, ")"); err != nil {
					return err
				}
				gen = true
			}
			if gen {
				break
			}
			if !p.eatOp(",") {
				if !p.eatOp(")") {
					return verifyErrf("调用缺少 )")
				}
				break
			}
			if p.eatOp(")") {
				break
			}
		}
	}
	return nil
}

// skipCondTokens 推进推导式 if 条件的 token 到 closer / 下一个
// 深度 0 的 "if" / "for" 关键字之前，不求值。用配对深度扫描替代
// orTest 预求值：orTest 会因迭代变量未绑定而误报 "不允许的名字"。
// 条件的真实语法校验由 comprehension 内逐条 if 静态解析完成。
// maxSkip 防御异常表达式造成过长扫描。
func (p *vParser) skipCondTokens() {
	const maxSkip = 200
	depth := 0
	for n := 0; n < maxSkip; n++ {
		t := p.cur()
		if t.kind == tkEOF {
			return
		}
		if depth == 0 && t.kind == tkName && (t.s == "for" || t.s == "if") {
			return
		}
		if t.kind == tkOp {
			switch t.s {
			case "(", "[":
				depth++
			case ")", "]":
				if depth == 0 {
					// closer 到达：条件段结束
					return
				}
				depth--
			}
		}
		p.next()
	}
}

// compClause 是推导式的一个 for 子句：目标变量（可多个，元组解包）+ 惰性
// 迭代区间 + 零或多个 if 条件区间（if c1 if c2 == if c1 and c2，Python 链式过滤）。
type compClause struct {
	vars      []string // 迭代变量名（元组解包时多个）
	iterStart int      // 迭代表达式的起始 token
	iterEnd   int      // 迭代表达式结束（不含）
	conds     []condRange
}

// condRange 是一个 if 条件的 token 区间 [start, end)。
type condRange struct {
	start int
	end   int
}

// skipIterTokens 跳过 for 子句迭代表达式的 token（不求值），停在
// "if" / "for" 关键字或本层闭合（")" / "]"）处。
func (p *vParser) skipIterTokens() {
	const maxSkip = 400
	depth := 0
	for n := 0; n < maxSkip; n++ {
		t := p.cur()
		if t.kind == tkEOF {
			return
		}
		if depth == 0 && t.kind == tkName {
			switch t.s {
			case "if", "for":
				return
			}
		}
		if t.kind == tkOp {
			switch t.s {
			case "(", "[":
				depth++
			case ")", "]":
				if depth == 0 {
					return
				}
				depth--
			}
		}
		p.next()
	}
}

// subToks 截取 token 区间 [start, end) 并在尾部补 tkEOF 哨兵，返回的
// 子流起点对齐 0（pos 语义随之重置），供 walk 的子解析器（模板 / 迭代 /
// 条件）使用。修复前直接切 p.toks[:end] 且 pos 用原始下标：pos 是全量
// token 流的绝对位置，切片后下标不对齐（迭代子流 pos=iterStart ≥ 切片
// 长度时首次 cur() 即越界）；区间尾 token 又被 next() 永久停留——尾 token
// 是悬挂运算符的话（"[1+ for b in range(3)]" 的模板 "1+"），factor 的
// eatOp 反复命中同一 token 无限递归直至栈溢出。与顶层 Eval 的 lex()
// 输出（恒以 tkEOF 收尾）保持同一不变量：子解析器总能在 EOF 处正常
// 停住或报"意外的记号"。调用方 pos 必须用 start-start=0 起步。
func (p *vParser) subToks(start, end int) []vToken {
	return append(append([]vToken(nil), p.toks[start:end]...), vToken{kind: tkEOF})
}

// comprehension: 模板以 token 区间 [tmplStart, tmplEnd) 表示，
// 对迭代变量逐个绑定后重新解析求值。支持多级 for 子句（含元组解包）
// 与可选 if 条件（与上游 ALLOWED_NODES 的 comprehension/GeneratorExp 同义）。
func (p *vParser) comprehension(tmplStart int, closer string) (vValue, error) {
	// 跳过模板表达式：cur 此时在模板首 token（探测入口未消费模板）。
	// 模板可能引用未绑定变量，不能求值；用配对深度扫描跳到顶层 "for"。
	// 深度从 0 起：模板自身的括号会增减深度，只有回到 0 层后的 for 才是子句。
	depth := 0
	tmplEnd := -1
	for {
		t := p.cur()
		if t.kind == tkEOF {
			return vValue{}, verifyErrf("推导式缺少 for")
		}
		if depth == 0 && t.kind == tkName && t.s == "for" {
			break
		}
		if t.kind == tkOp {
			switch t.s {
			case "(", "[":
				depth++
			case ")", "]":
				if depth == 0 {
					// 推导式未闭合就遇到外层 closer
					return vValue{}, verifyErrf("推导式缺少 for")
				}
				depth--
			}
		}
		p.next()
	}
	tmplEnd = p.pos // 模板段终点 = 顶层 for 的位置
	if tmplEnd == tmplStart {
		// 空模板（"[for a in r]" / "(for a in r)" / "sum(for a in r)"）：
		// Python SyntaxError。此处提前报错，避免 walk 用空 token 区间构造
		// tmplSub（toks[:tmplEnd] 且 pos==tmplStart==len，无 tkEOF 哨兵）
		// 首次 cur() 越界 panic。
		return vValue{}, verifyErrf("推导式缺少模板表达式")
	}
	// 解析所有 for 子句：for v1[,v2...] in iter [if cond] (for ... )*
	// 迭代表达式只记录 token 区间（惰性求值）：后面的 iter 可能引用
	// 前面子句的迭代变量（for a in ... for b in range(1,a)），
	// 必须在逐层绑定时再解析。条件段同样只记录区间。
	var clauses []*compClause
	for {
		if !(p.cur().kind == tkName && p.cur().s == "for") {
			return vValue{}, verifyErrf("推导式缺少 for")
		}
		p.next() // 吃掉 for
		if p.cur().kind != tkName {
			return vValue{}, verifyErrf("for 缺少变量名")
		}
		cl := &compClause{}
		// 目标变量：单名或元组解包 a,b
		cl.vars = append(cl.vars, p.next().s)
		for p.eatOp(",") {
			if p.cur().kind != tkName {
				return vValue{}, verifyErrf("for 缺少变量名")
			}
			cl.vars = append(cl.vars, p.next().s)
		}
		if p.cur().kind != tkName || p.cur().s != "in" {
			return vValue{}, verifyErrf("推导式缺少 in")
		}
		p.next()
		cl.iterStart = p.pos
		// 迭代表达式区间：到 "if" / "for" / closer 前为止。
		// 用配对扫描跳过（惰性：解析留到 walk 该层时，届时外层变量已绑定）。
		p.skipIterTokens()
		cl.iterEnd = p.pos
		if cl.iterEnd == cl.iterStart {
			// 空迭代表达式（"[for A in]"：in 后直接 closer / if / for）：
			// Python SyntaxError。必须在此解析层报错——walk 会用
			// toks[:cl.iterEnd] + pos==cl.iterStart 构造 iterSub，空区间
			// 不含 tkEOF 哨兵，首次 cur() 即越界 panic。
			return vValue{}, verifyErrf("for 子句缺少迭代表达式")
		}
		// 可选 if 条件（可多个：if c1 if c2 == if c1 and c2）。
		// 每个 if 条件即使迭代为空也必须静态完整语法校验：
		// 非法 junk（"x 5+1"）、悬挂运算符（"x<"）不能被静默吞掉。
		// 绑定本子句变量占位（vNil），用精确条件区间 + EOF 哨兵构造
		// 子 token 流，suppressEval 走同一 orTest 解析（不求值：除零/
		// 越界/函数副作用都不发生），解析后必须消费到 EOF。
		for p.cur().kind == tkName && p.cur().s == "if" {
			p.next()
			cStart := p.pos
			p.skipCondTokens()
			cEnd := p.pos
			if cEnd == cStart {
				// 空 if 条件（"[x for a in r if ]"）：Python SyntaxError。
				// 提前在解析层报错——walk 会用空 token 区间构造 condSub，
				// 无 tkEOF 哨兵，首次 cur() 即越界 panic。
				return vValue{}, verifyErrf("推导式缺少 if 条件表达式")
			}
			if cEnd > cStart {
				var bound []string
				bind := func(vars []string) {
					for _, v2 := range vars {
						if _, ok := p.env[v2]; !ok {
							p.env[v2] = vNil()
							bound = append(bound, v2)
						}
					}
				}
				for _, c2 := range clauses {
					bind(c2.vars)
				}
				bind(cl.vars)
				subToks := append(append([]vToken(nil), p.toks[cStart:cEnd]...), vToken{kind: tkEOF})
				condSub := &vParser{toks: subToks, env: p.env, steps: p.steps}
				restore := condSub.suppressEval()
				_, cerr := condSub.orTest()
				restore()
				p.steps = condSub.steps
				for _, v2 := range bound {
					delete(p.env, v2)
				}
				if cerr != nil {
					return vValue{}, cerr
				}
				if condSub.pos != len(subToks)-1 {
					return vValue{}, verifyErrf("推导式条件末尾有多余内容: %s", condSub.cur().s)
				}
			}
			cl.conds = append(cl.conds, condRange{start: cStart, end: cEnd})
		}
		clauses = append(clauses, cl)
		if !(p.cur().kind == tkName && p.cur().s == "for") {
			break
		}
	}
	if !p.eatOp(closer) {
		return vValue{}, verifyErrf("推导式缺少闭合 %s", closer)
	}
	// validateOnly（外层短路/条件静态校验路径）：只解析子句结构，
	// 不执行 walk——迭代变量在真实求值时才绑定，walk 里的惰性
	// iter/条件求值此刻必因占位值误报（range(2,x) 的 x=vNil 非整数）。
	// 结构与各 if 条件语法已由上方逐段校验，返回占位值。
	if p.validateOnly {
		return vNil(), nil
	}
	// 模板段终点 = 跳模板扫描时记录的顶层 for 位置（嵌套推导式的 for 在
	// 更深括号层，不会被误认为本层模板终点）。
	// 逐层展开子句（笛卡尔积），对每个绑定组合求值条件与模板
	var items []vValue
	var walk func(ci int, env map[string]vValue) error
	walk = func(ci int, env map[string]vValue) error {
		if ci == len(clauses) {
			// 预算：产出元素数上限
			if len(items) >= maxListLen {
				return verifyErrf("推导式产出元素超限")
			}
			// 最后一个子句的全部 if 条件在产出元素前逐个过滤
			// （多个 if 链 == and）。
			if len(clauses) > 0 {
				last := clauses[len(clauses)-1]
				skip := false
				for _, cr := range last.conds {
					condSub := &vParser{toks: p.subToks(cr.start, cr.end), pos: 0, env: env, steps: p.steps}
					cv, err := condSub.orTest()
					p.steps = condSub.steps
					if err != nil {
						return err
					}
					b, err := vTruthy(cv)
					if err != nil {
						return err
					}
					if !b {
						skip = true
						break
					}
				}
				if skip {
					return nil
				}
			}
			tmplSub := &vParser{toks: p.subToks(tmplStart, tmplEnd), pos: 0, env: env, steps: p.steps}
			tv, err := tmplSub.expr()
			p.steps = tmplSub.steps
			if err != nil {
				return err
			}
			items = append(items, tv)
			return nil
		}
		cl := clauses[ci]
		// 惰性解析迭代表达式：此时前序子句的变量已绑定到 env，
		// 支持 for a in ... for b in range(1,a) 的嵌套引用。
		iterSub := &vParser{toks: p.subToks(cl.iterStart, cl.iterEnd), pos: 0, env: env, steps: p.steps}
		iter, err := iterSub.expr()
		p.steps = iterSub.steps
		if err != nil {
			return err
		}
		seq, err := vIterate(iter)
		if err != nil {
			return err
		}
		for _, item := range seq {
			if len(cl.vars) == 1 {
				env[cl.vars[0]] = item
			} else if item.kind == 'l' {
				// 元组解包
				if len(item.list) != len(cl.vars) {
					return verifyErrf("元组解包数量不符")
				}
				for i, v := range cl.vars {
					env[v] = item.list[i]
				}
			} else {
				return verifyErrf("元组解包需要元组")
			}
		outer:
			// 本层全部 if 条件（多个 if 链 == and）
			for _, cr := range cl.conds {
				condSub := &vParser{toks: p.subToks(cr.start, cr.end), pos: 0, env: env, steps: p.steps}
				cv, err := condSub.orTest()
				p.steps = condSub.steps
				if err != nil {
					return err
				}
				b, err := vTruthy(cv)
				if err != nil {
					return err
				}
				if !b {
					continue outer
				}
			}
			if err := walk(ci+1, env); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(0, p.env); err != nil {
		// 清理绑定
		for _, cl := range clauses {
			for _, v := range cl.vars {
				delete(p.env, v)
			}
		}
		return vValue{}, err
	}
	for _, cl := range clauses {
		for _, v := range cl.vars {
			delete(p.env, v)
		}
	}
	return vList(items), nil
}

// 下标：atom 后跟 [idx]。idx 支持完整表达式（含比较，如 [[3,4],[2,1]][1>0]）。
// validateOnly 模式只解析 idx 语法与 "]"，不做类型/越界检查（短路分支的
// 右侧可能含越界下标，Python 同样不求值）。
func (p *vParser) subscript(base vValue) (vValue, error) {
	if !p.eatOp("[") {
		return base, nil
	}
	idx, err := p.expr()
	if err != nil {
		return vValue{}, err
	}
	if !p.eatOp("]") {
		return vValue{}, verifyErrf("下标缺少 ]")
	}
	if p.validateOnly {
		return vNil(), nil
	}
	if base.kind != 'l' {
		return vValue{}, verifyErrf("下标只支持列表")
	}
	// Python 真值下标：[[3,4],[2,1]][True] → 下标 1。布尔/整数均接受。
	i := 0
	switch idx.kind {
	case 'q':
		if !idx.rat.IsInt() {
			return vValue{}, verifyErrf("下标必须是整数")
		}
		i = int(idx.rat.Num().Int64())
	case 'b':
		if idx.b {
			i = 1
		}
	default:
		return vValue{}, verifyErrf("下标必须是整数或布尔")
	}
	if i < 0 {
		i += len(base.list)
	}
	if i < 0 || i >= len(base.list) {
		return vValue{}, verifyErrf("下标越界")
	}
	return base.list[i], nil
}

// call 白名单函数调用（只允许直接调用白名单名字，不允许关键字参数）。
func (p *vParser) call(name string) (vValue, error) {
	if err := p.step(); err != nil {
		return vValue{}, err
	}
	p.next() // (
	var args []vValue
	if !p.eatOp(")") {
		for {
			if p.cur().kind == tkName {
				// 关键字参数检测：name 后跟 '='（不是 '=='）
				save := p.pos
				p.next()
				if p.isOp("=") {
					return vValue{}, verifyErrf("不允许关键字参数")
				}
				p.pos = save
			}
			argStart := p.pos
			a, err := p.expr()
			gen := false
			if err != nil {
				// 生成器表达式探测：sum(1 for n in range(...) ...) 的参数
				// 以 "expr for ..." 形式出现。解析首元素失败或成功后遇 for，
				// 都按生成器重走（comprehension 会跳过模板到 for）。
				if p.lookAheadFor(argStart) {
					p.pos = argStart
					a, err = p.comprehension(argStart, ")")
					if err != nil {
						return vValue{}, err
					}
					gen = true
				} else {
					return vValue{}, err
				}
			} else if p.cur().kind == tkName && p.cur().s == "for" {
				p.pos = argStart
				a, err = p.comprehension(argStart, ")")
				if err != nil {
					return vValue{}, err
				}
				gen = true
			}
			args = append(args, a)
			if gen {
				// 生成器表达式的 ")" 由 comprehension 作为 closer 消费，
				// 它同时也是调用的 ")"（sum(x for ...) 中间不缺括号）。
				break
			}
			if !p.eatOp(",") {
				if !p.eatOp(")") {
					return vValue{}, verifyErrf("调用缺少 )")
				}
				break
			}
			if p.eatOp(")") {
				break
			}
		}
	}
	return vCall(p, name, args)
}

/* ---- 常量与函数实现 ---- */

func vConst(name string) (vValue, error) {
	switch name {
	case "pi":
		return vFloat(3.141592653589793), nil
	case "e":
		return vFloat(2.718281828459045), nil
	}
	return vValue{}, verifyErrf("非常量名字: %s", name)
}

func vCall(p *vParser, name string, args []vValue) (vValue, error) {
	switch name {
	case "Fraction":
		if len(args) != 1 && len(args) != 2 {
			return vValue{}, verifyErrf("Fraction 参数数")
		}
		// Python Fraction 接受任意 Rational/数字：参数可为 int/float/Fraction/表达式结果。
		toRat := func(a vValue) (*big.Rat, bool) {
			switch a.kind {
			case 'q':
				return new(big.Rat).Set(a.rat), true
			case 'f':
				return new(big.Rat).SetFloat64(a.f), true
			case 'b':
				if a.b {
					return big.NewRat(1, 1), true
				}
				return new(big.Rat), true
			}
			return nil, false
		}
		if len(args) == 2 {
			// Fraction(num, den)：分子/分母可为任意数值（含 Fraction 运算结果）
			nr, ok1 := toRat(args[0])
			dr, ok2 := toRat(args[1])
			if !ok1 || !ok2 || dr.Sign() == 0 {
				return vValue{}, verifyErrf("Fraction 参数非法")
			}
			return vRat(new(big.Rat).Quo(nr, dr)), nil
		}
		switch args[0].kind {
		case 'q':
			return vRat(new(big.Rat).Set(args[0].rat)), nil
		case 'f':
			return vRat(new(big.Rat).SetFloat64(args[0].f)), nil
		case 's':
			r, ok := new(big.Rat).SetString(args[0].s)
			if !ok {
				return vValue{}, verifyErrf("Fraction 字符串非法")
			}
			return vRat(r), nil
		}
		return vValue{}, verifyErrf("Fraction 参数非法")
	case "sqrt":
		if len(args) != 1 {
			return vValue{}, verifyErrf("sqrt 参数数")
		}
		x := args[0].asFloat()
		if x < 0 {
			return vValue{}, verifyErrf("sqrt 负数")
		}
		return vFloat(floatSqrt(x)), nil
	case "sin", "cos", "tan", "asin", "acos", "atan", "exp", "log10", "log2", "degrees", "radians":
		if len(args) != 1 {
			return vValue{}, verifyErrf("%s 参数数", name)
		}
		return vFloat(floatFn1(name, args[0].asFloat())), nil
	case "atan2", "log":
		if len(args) < 1 || len(args) > 2 {
			return vValue{}, verifyErrf("%s 参数数", name)
		}
		if name == "log" {
			if len(args) == 2 {
				return vFloat(floatLog(args[0].asFloat()) / floatLog(args[1].asFloat())), nil
			}
			return vFloat(floatLog(args[0].asFloat())), nil
		}
		if len(args) != 2 {
			return vValue{}, verifyErrf("atan2 参数数")
		}
		return vFloat(floatAtan2(args[0].asFloat(), args[1].asFloat())), nil
	case "factorial":
		if len(args) != 1 {
			return vValue{}, verifyErrf("factorial 参数数")
		}
		n, ok := args[0].toInt()
		if !ok || n.Sign() < 0 || !n.IsInt64() {
			return vValue{}, verifyErrf("factorial 参数")
		}
		// 预算：参数上限 + 结果位数上限（防大整数乘法 OOM/CPU）
		if n.Int64() > maxFactArg {
			return vValue{}, verifyErrf("factorial 参数过大")
		}
		res := big.NewInt(1)
		for i := int64(2); i <= n.Int64(); i++ {
			res.Mul(res, big.NewInt(i))
			if len(res.Bits())*64 > maxFactDigits*4 {
				return vValue{}, verifyErrf("factorial 结果过大")
			}
		}
		return vRat(new(big.Rat).SetInt(res)), nil
	case "comb", "perm":
		if len(args) != 2 {
			return vValue{}, verifyErrf("%s 参数数", name)
		}
		a, ok1 := args[0].toInt()
		b, ok2 := args[1].toInt()
		if !ok1 || !ok2 || a.Sign() < 0 || b.Sign() < 0 || !a.IsInt64() || !b.IsInt64() {
			return vValue{}, verifyErrf("%s 参数", name)
		}
		// 预算：n 上限 + 结果位数上限
		if a.Int64() > maxCombPermN {
			return vValue{}, verifyErrf("%s 参数过大", name)
		}
		res, err := intComb(p, name, a.Int64(), b.Int64())
		if err != nil {
			return vValue{}, err
		}
		if len(res.Bits())*64 > maxFactDigits*4 {
			return vValue{}, verifyErrf("%s 结果过大", name)
		}
		return vRat(new(big.Rat).SetInt(res)), nil
	case "gcd", "lcm":
		if len(args) < 2 {
			return vValue{}, verifyErrf("%s 参数数", name)
		}
		acc, ok := args[0].toInt()
		if !ok {
			return vValue{}, verifyErrf("%s 参数", name)
		}
		for _, a := range args[1:] {
			x, ok2 := a.toInt()
			if !ok2 {
				return vValue{}, verifyErrf("%s 参数", name)
			}
			acc = intGCDLCM(name, acc, x)
		}
		return vRat(new(big.Rat).SetInt(acc)), nil
	case "abs":
		if len(args) != 1 {
			return vValue{}, verifyErrf("abs 参数数")
		}
		if args[0].kind == 'q' {
			r := new(big.Rat).Abs(args[0].rat)
			return vRat(r), nil
		}
		return vFloat(floatAbs(args[0].asFloat())), nil
	case "round":
		if len(args) < 1 || len(args) > 2 {
			return vValue{}, verifyErrf("round 参数数")
		}
		nd := 0
		if len(args) == 2 {
			n, ok := args[1].toInt()
			if !ok || !n.IsInt64() {
				return vValue{}, verifyErrf("round 参数")
			}
			nd = int(n.Int64())
		}
		return vFloat(floatRound(args[0].asFloat(), nd)), nil
	case "sum":
		if len(args) != 1 || args[0].kind != 'l' {
			return vValue{}, verifyErrf("sum 参数")
		}
		acc := vValue{kind: 'q', rat: new(big.Rat)}
		for _, v := range args[0].list {
			var err error
			acc, err = vAdd(acc, v)
			if err != nil {
				return vValue{}, err
			}
		}
		return acc, nil
	case "min", "max":
		if len(args) == 0 {
			return vValue{}, verifyErrf("%s 参数", name)
		}
		var list []vValue
		if len(args) == 1 {
			if args[0].kind != 'l' {
				return vValue{}, verifyErrf("%s 参数", name)
			}
			list = args[0].list
		} else {
			list = args
		}
		if len(list) == 0 {
			return vValue{}, verifyErrf("%s 空序列", name)
		}
		best := list[0]
		for _, v := range list[1:] {
			c, err := vCompare(v, best)
			if err != nil {
				return vValue{}, err
			}
			if (name == "min" && c < 0) || (name == "max" && c > 0) {
				best = v
			}
		}
		return best, nil
	case "len":
		if len(args) != 1 || args[0].kind != 'l' {
			return vValue{}, verifyErrf("len 参数")
		}
		return vRat(new(big.Rat).SetInt64(int64(len(args[0].list)))), nil
	case "sorted":
		if len(args) != 1 || args[0].kind != 'l' {
			return vValue{}, verifyErrf("sorted 参数")
		}
		out := append([]vValue(nil), args[0].list...)
		for i := 1; i < len(out); i++ {
			for j := i; j > 0; j-- {
				c, err := vCompare(out[j], out[j-1])
				if err != nil {
					return vValue{}, err
				}
				if c >= 0 {
					break
				}
				out[j], out[j-1] = out[j-1], out[j]
			}
		}
		return vList(out), nil
	case "range":
		if len(args) < 1 || len(args) > 3 {
			return vValue{}, verifyErrf("range 参数数")
		}
		var lo, hi, step int64 = 0, 0, 1
		nums := make([]int64, len(args))
		for i, a := range args {
			// 参数必须是精确整数：float 参数（如 range(0.5,3)）不得静默截断
			if a.kind == 'f' {
				return vValue{}, verifyErrf("range 参数必须是整数")
			}
			n, ok := a.toInt()
			if !ok || !n.IsInt64() || !a.rat.IsInt() {
				return vValue{}, verifyErrf("range 参数必须是整数")
			}
			nums[i] = n.Int64()
		}
		switch len(nums) {
		case 1:
			hi = nums[0]
		case 2:
			lo, hi = nums[0], nums[1]
		case 3:
			lo, hi, step = nums[0], nums[1], nums[2]
		}
		if step == 0 {
			return vValue{}, verifyErrf("range step=0")
		}
		// 预算：元素数用 big.Int 跨度计算（hi-lo / lo-hi 的 int64 减法可溢出：
		// lo=MinInt64、hi=MaxInt64 时回绕为负，绕过 maxListLen 检查直接进循环）。
		span := new(big.Int)
		cntBig := new(big.Int)
		if step > 0 && hi > lo {
			span.Sub(big.NewInt(hi), big.NewInt(lo)) // > 0，无溢出风险（big.Int）
			cntBig = cntBig.Sub(span, big.NewInt(1))
			cntBig = cntBig.Div(cntBig, big.NewInt(step))
			cntBig = cntBig.Add(cntBig, big.NewInt(1))
		} else if step < 0 && hi < lo {
			span.Sub(big.NewInt(lo), big.NewInt(hi)) // > 0
			cntBig = cntBig.Sub(span, big.NewInt(1))
			cntBig = cntBig.Div(cntBig, big.NewInt(-step))
			cntBig = cntBig.Add(cntBig, big.NewInt(1))
		}
		// -step 与 span 的 big.Int 除法无溢出，但 cntBig 若超 int64（空 range
		// 之外的所有巨大跨度场景）在此统一拒绝，杜绝任何回绕进循环。
		if !cntBig.IsInt64() || cntBig.Int64() > maxListLen {
			return vValue{}, verifyErrf("range 产出元素超限")
		}
		cnt := cntBig.Int64()
		var out []vValue
		if step > 0 {
			for i := lo; i < hi; i += step { // cnt ≤ maxListLen 已保证循环有限
				out = append(out, vRat(new(big.Rat).SetInt64(i)))
			}
		} else {
			for i := lo; i > hi; i += step {
				out = append(out, vRat(new(big.Rat).SetInt64(i)))
			}
		}
		if int64(len(out)) != cnt {
			return vValue{}, verifyErrf("range 元素数与预算不符")
		}
		return vList(out), nil
	case "floor", "ceil":
		if len(args) != 1 {
			return vValue{}, verifyErrf("%s 参数数", name)
		}
		f := args[0].asFloat()
		if name == "floor" {
			return vRat(new(big.Rat).SetInt64(int64(floatFloor(f)))), nil
		}
		return vRat(new(big.Rat).SetInt64(int64(floatCeil(f)))), nil
	case "isqrt":
		if len(args) != 1 {
			return vValue{}, verifyErrf("isqrt 参数数")
		}
		n, ok := args[0].toInt()
		if !ok || n.Sign() < 0 {
			return vValue{}, verifyErrf("isqrt 参数")
		}
		// 整数平方根（牛顿法）
		x := new(big.Int).Set(n)
		if x.Cmp(big.NewInt(2)) < 0 {
			return vRat(new(big.Rat).SetInt(x)), nil
		}
		y := new(big.Int).Rsh(x, 1)
		for {
			nx := new(big.Int).Rsh(new(big.Int).Add(y, new(big.Int).Quo(x, y)), 1)
			if nx.Cmp(y) >= 0 {
				break
			}
			y = nx
		}
		return vRat(new(big.Rat).SetInt(y)), nil
	case "int":
		if len(args) != 1 {
			return vValue{}, verifyErrf("int 参数数")
		}
		if args[0].kind == 'q' {
			// Python int(Fraction)：向零截断（38.9→38、-38.9→-38），
			// 不是取分子。修复前取 Num() 导致 cnm_p135_q* 对拍错误。
			n := new(big.Int).Quo(args[0].rat.Num(), args[0].rat.Denom())
			return vRat(new(big.Rat).SetInt(n)), nil
		}
		return vRat(new(big.Rat).SetInt64(int64(args[0].asFloat()))), nil
	case "float":
		if len(args) != 1 {
			return vValue{}, verifyErrf("float 参数数")
		}
		return vFloat(args[0].asFloat()), nil
	case "mean", "median", "pvariance", "variance", "mode":
		if len(args) != 1 || args[0].kind != 'l' {
			return vValue{}, verifyErrf("%s 参数", name)
		}
		return vStat(name, args[0].list)
	case "solve2":
		if len(args) != 6 {
			return vValue{}, verifyErrf("solve2 参数数")
		}
		var nums [6]*big.Rat
		for i, a := range args {
			if a.kind != 'q' {
				return vValue{}, verifyErrf("solve2 参数")
			}
			nums[i] = a.rat
		}
		// a1x+b1y=c1, a2x+b2y=c2
		a1, b1, c1, a2, b2, c2 := nums[0], nums[1], nums[2], nums[3], nums[4], nums[5]
		det := new(big.Rat).Sub(new(big.Rat).Mul(a1, b2), new(big.Rat).Mul(a2, b1))
		if det.Sign() == 0 {
			return vValue{}, verifyErrf("方程组无唯一解")
		}
		x := new(big.Rat).Quo(new(big.Rat).Sub(new(big.Rat).Mul(c1, b2), new(big.Rat).Mul(c2, b1)), det)
		y := new(big.Rat).Quo(new(big.Rat).Sub(new(big.Rat).Mul(a1, c2), new(big.Rat).Mul(a2, c1)), det)
		return vList([]vValue{vRat(x), vRat(y)}), nil
	case "roots2":
		if len(args) != 3 {
			return vValue{}, verifyErrf("roots2 参数数")
		}
		var nums [3]*big.Rat
		for i, a := range args {
			if a.kind != 'q' {
				return vValue{}, verifyErrf("roots2 参数")
			}
			nums[i] = a.rat
		}
		a, b, c := nums[0], nums[1], nums[2]
		if a.Sign() == 0 {
			return vValue{}, verifyErrf("roots2 a=0")
		}
		disc := new(big.Rat).Sub(new(big.Rat).Mul(b, b), new(big.Rat).Mul(new(big.Rat).Mul(a, c), big.NewRat(4, 1)))
		if disc.Sign() < 0 {
			return vList(nil), nil
		}
		// 有理平方根：x = (-b ± r) / 2a（求根公式；修复前误写 (b ± r)/2a）
		if r, ok := ratSqrt(disc); ok {
			twoA := new(big.Rat).Mul(a, big.NewRat(2, 1))
			negB := new(big.Rat).Neg(b)
			x1 := new(big.Rat).Quo(new(big.Rat).Sub(negB, r), twoA)
			x2 := new(big.Rat).Quo(new(big.Rat).Add(negB, r), twoA)
			lo, hi := x1, x2
			if lo.Cmp(hi) > 0 {
				lo, hi = hi, lo
			}
			if lo.Cmp(hi) == 0 {
				return vList([]vValue{vRat(lo)}), nil
			}
			return vList([]vValue{vRat(lo), vRat(hi)}), nil
		}
		// 无理根：浮点近似（与上游一致）
		bf, _ := b.Float64()
		af, _ := a.Float64()
		df, _ := disc.Float64()
		s := floatSqrt(df)
		x1 := (-bf - s) / (2 * af)
		x2 := (-bf + s) / (2 * af)
		if x1 > x2 {
			x1, x2 = x2, x1
		}
		return vList([]vValue{vFloat(x1), vFloat(x2)}), nil
	}
	return vValue{}, verifyErrf("不允许的函数: %s", name)
}

// toInt 返回整数 big.Int 视图（分数需为整数）。
func (v vValue) toInt() (*big.Int, bool) {
	if v.kind == 'q' && v.rat.IsInt() {
		return v.rat.Num(), true
	}
	if v.kind == 'f' {
		return big.NewInt(int64(v.f)), true
	}
	return nil, false
}

/* ---- 运算实现 ---- */

func numPair(a, b vValue) (af, bf float64, ar, br *big.Rat, exact bool) {
	if a.kind == 'q' && b.kind == 'q' {
		return 0, 0, a.rat, b.rat, true
	}
	return a.asFloat(), b.asFloat(), nil, nil, false
}

func vAdd(a, b vValue) (vValue, error) {
	if a.kind == 's' || b.kind == 's' {
		if a.kind == 's' && b.kind == 's' {
			return vStr(a.s + b.s), nil
		}
		if a.kind == 's' && b.kind == 'l' {
			// str + list：Python 会报错；上游表达式不会走到
			return vValue{}, verifyErrf("str + list 不支持")
		}
	}
	if a.kind == 'l' && b.kind == 'l' {
		return vList(append(append([]vValue(nil), a.list...), b.list...)), nil
	}
	af, bf, ar, br, exact := numPair(a, b)
	if exact {
		return vRat(new(big.Rat).Add(ar, br)), nil
	}
	return vFloat(af + bf), nil
}

func vSub(a, b vValue) (vValue, error) {
	af, bf, ar, br, exact := numPair(a, b)
	if a.kind == 'l' && b.kind == 'l' {
		// 列表差集（Python list - list 不存在；不支持）
		return vValue{}, verifyErrf("list - list 不支持")
	}
	if exact {
		return vRat(new(big.Rat).Sub(ar, br)), nil
	}
	return vFloat(af - bf), nil
}

func vMul(a, b vValue) (vValue, error) {
	if a.kind == 's' || b.kind == 's' {
		// str * int
		if a.kind == 's' && b.kind == 'q' && b.rat.IsInt() {
			s, n := a.s, b.rat.Num()
			if n.Sign() < 0 {
				return vValue{}, verifyErrf("str * 负数")
			}
			return vStrRepeat(s, n)
		}
		if b.kind == 's' && a.kind == 'q' && a.rat.IsInt() {
			s, n := b.s, a.rat.Num()
			if n.Sign() < 0 {
				return vValue{}, verifyErrf("str * 负数")
			}
			return vStrRepeat(s, n)
		}
		return vValue{}, verifyErrf("str 乘法参数")
	}
	af, bf, ar, br, exact := numPair(a, b)
	if exact {
		return vRat(new(big.Rat).Mul(ar, br)), nil
	}
	return vFloat(af * bf), nil
}

// vStrRepeat str * int：strings.Repeat 前先做预算检查，防 len('a'*(10**12)) OOM。
// 用 n > maxStrAlloc/len(s) 而非 len(s)*n，避免 int 乘法溢出（溢出后负数
// 可绕过 <= 上限的判断）。空串结果恒为空串，不经过 Repeat 分配。
func vStrRepeat(s string, n *big.Int) (vValue, error) {
	if !n.IsInt64() || n.Int64() > int64(maxStrAlloc) {
		return vValue{}, verifyErrf("str*int 重复次数超限")
	}
	if s == "" {
		return vStr(""), nil
	}
	if n.Int64() > int64(maxStrAlloc/int(len(s))) {
		return vValue{}, verifyErrf("str*int 结果超限")
	}
	return vStr(strings.Repeat(s, int(n.Int64()))), nil
}

func vDiv(a, b vValue) (vValue, error) {
	af, bf, ar, br, exact := numPair(a, b)
	if exact {
		if br.Sign() == 0 {
			return vValue{}, verifyErrf("除以 0")
		}
		return vRat(new(big.Rat).Quo(ar, br)), nil
	}
	if bf == 0 {
		return vValue{}, verifyErrf("除以 0")
	}
	return vFloat(af / bf), nil
}

func vFloorDiv(a, b vValue) (vValue, error) {
	af, bf, ar, br, exact := numPair(a, b)
	if exact {
		if br.Sign() == 0 {
			return vValue{}, verifyErrf("除以 0")
		}
		q := new(big.Rat).Quo(ar, br)
		if q.IsInt() {
			return vRat(new(big.Rat).SetInt(q.Num())), nil
		}
		// floor 语义
		f, _ := q.Float64()
		return vRat(new(big.Rat).SetInt64(int64(floatFloor(f)))), nil
	}
	if bf == 0 {
		return vValue{}, verifyErrf("除以 0")
	}
	return vRat(new(big.Rat).SetInt64(int64(floatFloor(af / bf)))), nil
}

func vMod(a, b vValue) (vValue, error) {
	af, bf, ar, br, exact := numPair(a, b)
	if exact {
		if br.Sign() == 0 {
			return vValue{}, verifyErrf("模 0")
		}
		// Python 语义：符号跟随除数
		q := new(big.Rat).Quo(ar, br)
		if q.IsInt() {
			m := new(big.Rat).Sub(ar, new(big.Rat).Mul(q, br))
			return vRat(m), nil
		}
		qf, _ := q.Float64()
		qi := int64(floatFloor(qf))
		qInt := new(big.Rat).SetInt64(qi)
		m := new(big.Rat).Sub(ar, new(big.Rat).Mul(qInt, br))
		return vRat(m), nil
	}
	if bf == 0 {
		return vValue{}, verifyErrf("模 0")
	}
	m := af - bf*floatFloor(af/bf)
	return vFloat(m), nil
}

func vNeg(a vValue) (vValue, error) {
	if a.kind == 'q' {
		return vRat(new(big.Rat).Neg(a.rat)), nil
	}
	return vFloat(-a.asFloat()), nil
}

func vPow(a, b vValue) (vValue, error) {
	// 常量上限（与上游 400 一致）。指数用 big.Int 比较而非 int64：
	// 10**400**400 的指数是 400**400（远超 int64），Int64() 截断后可能
	// 恰好落在 [-400,400]，必须先做数值比较。
	if b.kind == 'q' && b.rat.IsInt() {
		lim := new(big.Int)
		if b.rat.Num().Cmp(lim.SetInt64(400)) > 0 || b.rat.Num().Cmp(lim.SetInt64(-400)) < 0 {
			return vValue{}, verifyErrf("指数过大")
		}
		e := b.rat.Num().Int64() // 已确认在 [-400,400]，无截断
		if a.kind == 'q' {
			if e >= 0 {
				p := new(big.Rat).SetInt64(1)
				base := new(big.Rat).Set(a.rat)
				for i := int64(0); i < e; i++ {
					p.Mul(p, base)
					// 预算：结果位数上限（10^12 ** 400 ≈ 4800 位，留裕量防嵌套幂放大）
					if ratDigits(p) > maxPowResult {
						return vValue{}, verifyErrf("幂结果过大")
					}
				}
				return vRat(p), nil
			}
			// 负指数
			if a.rat.Sign() == 0 {
				return vValue{}, verifyErrf("0 的负指数")
			}
			p := new(big.Rat).SetInt64(1)
			base := new(big.Rat).Set(a.rat)
			for i := int64(0); i < -e; i++ {
				p.Mul(p, base)
				if ratDigits(p) > maxPowResult {
					return vValue{}, verifyErrf("幂结果过大")
				}
			}
			return vRat(new(big.Rat).Quo(big.NewRat(1, 1), p)), nil
		}
	}
	return vFloat(floatPow(a.asFloat(), b.asFloat())), nil
}

// ratDigits 有理数十进制位数（分子+分母），幂结果预算用。
func ratDigits(r *big.Rat) int {
	return len(new(big.Int).Abs(r.Num()).String()) + len(new(big.Int).Abs(r.Denom()).String())
}

func vCompare(a, b vValue) (int, error) {
	if a.kind == 's' && b.kind == 's' {
		return strings.Compare(a.s, b.s), nil
	}
	if a.kind == 'b' || b.kind == 'b' {
		ab := 0
		bb := 0
		if x, err := vTruthy(a); err != nil {
			return 0, err
		} else if x {
			ab = 1
		}
		if x, err := vTruthy(b); err != nil {
			return 0, err
		} else if x {
			bb = 1
		}
		switch {
		case ab < bb:
			return -1, nil
		case ab > bb:
			return 1, nil
		}
		return 0, nil
	}
	af, bf, ar, br, exact := numPair(a, b)
	if exact {
		return ar.Cmp(br), nil
	}
	// NaN 语义（IEEE 754 / Python）：NaN 与任何值（含 NaN）既不小于、不大于
	// 也不相等——log(-1)==log(-1) 必须是 false，!= 是 true。
	// vCompare 的三值返回（-1/0/1）无法表达"六个比较全 false"，因此
	// NaN 参与时返回特殊标记 2，由 comparison() 按 op 解码。
	if isNaN(af) || isNaN(bf) {
		return 2, nil
	}
	switch {
	case af < bf:
		return -1, nil
	case af > bf:
		return 1, nil
	}
	return 0, nil
}

// isNaN 浮点 NaN 判定（verify 不引入 math 大段，独立小函数）。
func isNaN(f float64) bool { return f != f }

func vTruthy(v vValue) (bool, error) {
	switch v.kind {
	case 'b':
		return v.b, nil
	case 'q':
		return v.rat.Sign() != 0, nil
	case 'f':
		return v.f != 0, nil
	case 's':
		return v.s != "", nil
	case 'l':
		return len(v.list) > 0, nil
	case 'n':
		return false, nil
	}
	return false, verifyErrf("真值判断不支持该类型")
}

func vIterate(v vValue) ([]vValue, error) {
	if v.kind != 'l' {
		return nil, verifyErrf("只能迭代列表")
	}
	return v.list, nil
}

func ratSqrt(r *big.Rat) (*big.Rat, bool) {
	if r.Sign() < 0 {
		return nil, false
	}
	n := new(big.Int).Set(r.Num())
	d := new(big.Int).Set(r.Denom())
	// big.Int.Mul 就地改写接收者：验证平方必须用临时变量，否则 rn/rd
	// 被改写为平方值后返回错误结果（ratSqrt(16) 曾返回 16 而非 4）。
	rn := new(big.Int).Sqrt(n)
	rd := new(big.Int).Sqrt(d)
	if new(big.Int).Mul(rn, rn).Cmp(n) != 0 {
		return nil, false
	}
	if new(big.Int).Mul(rd, rd).Cmp(d) != 0 {
		return nil, false
	}
	return new(big.Rat).SetFrac(rn, rd), true
}

func vStat(name string, list []vValue) (vValue, error) {
	if len(list) == 0 {
		return vValue{}, verifyErrf("空序列统计")
	}
	var floats []float64
	var rats []*big.Rat
	allRat := true
	for _, v := range list {
		if v.kind != 'q' {
			allRat = false
		}
		floats = append(floats, v.asFloat())
		if v.kind == 'q' {
			rats = append(rats, v.rat)
		}
	}
	switch name {
	case "mean":
		if allRat {
			sum := new(big.Rat)
			for _, r := range rats {
				sum.Add(sum, r)
			}
			return vRat(new(big.Rat).Quo(sum, new(big.Rat).SetInt64(int64(len(rats))))), nil
		}
		s := 0.0
		for _, f := range floats {
			s += f
		}
		return vFloat(s / float64(len(floats))), nil
	case "median":
		sorted := append([]float64(nil), floats...)
		for i := 1; i < len(sorted); i++ {
			for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
				sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
			}
		}
		n := len(sorted)
		if n%2 == 1 {
			return vFloat(sorted[n/2]), nil
		}
		return vFloat((sorted[n/2-1] + sorted[n/2]) / 2), nil
	case "pvariance", "variance":
		m := 0.0
		for _, f := range floats {
			m += f
		}
		m /= float64(len(floats))
		s := 0.0
		for _, f := range floats {
			s += (f - m) * (f - m)
		}
		if name == "pvariance" {
			return vFloat(s / float64(len(floats))), nil
		}
		if len(floats) < 2 {
			return vValue{}, verifyErrf("variance 样本不足")
		}
		return vFloat(s / float64(len(floats)-1)), nil
	case "mode":
		// Python 3.8+ statistics.mode：多众数等频时返回"第一个遇到的"
		// 元素（顺序敏感）。修复前用 Go map 迭代挑最大计数，等频元素
		// 间随 map 迭代序随机漂移——mode([0,82]) 两次求值可能分别返回
		// 0 / 82，违反同输入确定一致不变量。改为按列表顺序扫一遍：
		// 计数并列时保留先出现者，与 Python 语义一致且确定。
		bestIdx, bestN := -1, 0
		for i, fi := range floats {
			n := 0
			for _, fj := range floats {
				if fj == fi {
					n++
				}
			}
			if n > bestN {
				bestIdx, bestN = i, n
			}
		}
		if bestIdx < 0 {
			return vValue{}, verifyErrf("mode 无结果")
		}
		return vFloat(floats[bestIdx]), nil
	}
	return vValue{}, verifyErrf("未知统计函数 %s", name)
}

/* ---- 底层数学（避免引入 math 大段；只用必要函数） ---- */

func floatSqrt(x float64) float64 { return mathSqrt(x) }
func floatAbs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
func floatFloor(x float64) float64 {
	if x >= 0 || x == float64(int64(x)) {
		return float64(int64(x))
	}
	return float64(int64(x) - 1)
}
func floatCeil(x float64) float64 {
	if x <= 0 || x == float64(int64(x)) {
		return float64(int64(x))
	}
	return float64(int64(x) + 1)
}
func floatRound(x float64, nd int) float64 {
	p := floatPow(10, float64(nd))
	v := x * p
	// Python round 是银行家舍入；上游表达式用到 round 的场景按四舍五入近似
	if v-floorOf(v) >= 0.5 {
		return (floorOf(v) + 1) / p
	}
	return floorOf(v) / p
}
func floorOf(x float64) float64 { return floatFloor(x) }

func mustParseBig(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return big.NewInt(0)
	}
	return n
}

// intComb 组合数/排列数。每轮循环计一步预算（k 可达 10^5，
// 大 k 时循环本身也是 CPU 开销，不能只靠结果位数上限兜底）。
func intComb(p *vParser, name string, n, k int64) (*big.Int, error) {
	if k > n {
		return big.NewInt(0), nil
	}
	res := big.NewInt(1)
	if name == "comb" {
		for i := int64(0); i < k; i++ {
			if err := p.step(); err != nil {
				return nil, err
			}
			res.Mul(res, big.NewInt(n-i))
			res.Div(res, big.NewInt(i+1))
		}
		return res, nil
	}
	for i := int64(0); i < k; i++ {
		if err := p.step(); err != nil {
			return nil, err
		}
		res.Mul(res, big.NewInt(n-i))
	}
	return res, nil
}

func intGCDLCM(name string, a, b *big.Int) *big.Int {
	g := new(big.Int).GCD(nil, nil, new(big.Int).Abs(a), new(big.Int).Abs(b))
	if name == "gcd" {
		return g
	}
	if g.Sign() == 0 {
		return big.NewInt(0)
	}
	l := new(big.Int).Mul(new(big.Int).Abs(a), new(big.Int).Abs(b))
	return l.Div(l, g)
}

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
