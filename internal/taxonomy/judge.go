package taxonomy

import (
	"math"
	"math/big"
	"regexp"
	"sort"
	"strings"
)

/* 判题：移植上游 diagnose/engine.js 的 normalizeText / parseAnswer /
   judgeAnswer / judgeQuestion。只在“可靠”时给 correct/wrong，其余返回 manual
   （让用户对照参考答案自评）。不执行任何题目自带代码。 */

// Verdict 判定结果。
const (
	VerdictCorrect = "correct"
	VerdictWrong   = "wrong"
	VerdictManual  = "manual"
)

// JudgeResult 是判题结果：verdict + 原因（上游 reason 字段）。
type JudgeResult struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

var (
	manualEmpty = JudgeResult{VerdictManual, ReasonEmpty}
)

// 判题原因。
const (
	ReasonEmpty           = "empty"
	ReasonIdentical       = "identical"
	ReasonEquivalent      = "equivalent"
	ReasonAccept          = "accept"
	ReasonNumericMismatch = "numeric-mismatch"
	ReasonUnsure          = "unsure"
	ReasonChoice          = "choice"
	ReasonUnrecognized    = "unrecognized"
	ReasonSelf            = "self"
	ReasonNoReference     = "no-reference"
	ReasonCountMismatch   = "count-mismatch"
	ReasonVerify          = "verify" // verify 表达式作为额外 oracle 判定
)

/* ---- 规范化 ---- */

var (
	reSups          = regexp.MustCompile(`²|³`)
	reDegLike       = regexp.MustCompile(`[º˚∘゜]`)
	reMinusLike     = regexp.MustCompile(`[−‐-―﹣]`)
	reEnumComma     = regexp.MustCompile(`、`)
	reEnumPeriod    = regexp.MustCompile(`。`)
	reSmartQuote    = regexp.MustCompile(`[“”‘’]`)
	reTimesLike     = regexp.MustCompile(`[×·⋅∙]`)
	reDivSign       = regexp.MustCompile(`÷`)
	reLe            = regexp.MustCompile(`[≦⩽]`)
	reGe            = regexp.MustCompile(`[≧⩾]`)
	reLeEq          = regexp.MustCompile(`<=`)
	reGeEq          = regexp.MustCompile(`>=`)
	reBracket       = regexp.MustCompile(`[\[\]{}]`)
	reDegree        = regexp.MustCompile(`(\d)\s*度`)
	reMixedFraction = regexp.MustCompile(`(\d)\s+(\d+\s*/\s*\d+)`)
	reWhitespace    = regexp.MustCompile(`\s+`)
	reAnswerSep     = regexp.MustCompile(`[和或与及]`)
	reAnswerPrefix  = regexp.MustCompile(`^(答案|答)[:=]`)
	reOrWord        = regexp.MustCompile(`或者`)
	reTrailingPunct = regexp.MustCompile(`[.!]+$`)
	reThousands     = regexp.MustCompile(`^[+-]?\d{1,3}(,\d{3})+(\.\d+)?$`)
)

// NormalizeText 答案规范化：上标、全角→半角、中文标点、乘除号、带分数、
// 千分位等。与上游 normalizeText 逐步等价。
func NormalizeText(input string) string {
	s := input
	s = strings.Replace(s, "²", "^2", -1)
	s = strings.Replace(s, "³", "^3", -1)
	s = reDegLike.ReplaceAllString(s, "°")
	s = normalizeNFKC(s)
	s = reMinusLike.ReplaceAllString(s, "-")
	s = reEnumComma.ReplaceAllString(s, ",")
	s = reEnumPeriod.ReplaceAllString(s, ".")
	s = reSmartQuote.ReplaceAllString(s, `"`)
	s = reTimesLike.ReplaceAllString(s, "*")
	s = reDivSign.ReplaceAllString(s, "/")
	s = reLe.ReplaceAllString(s, "≤")
	s = reGe.ReplaceAllString(s, "≥")
	s = reLeEq.ReplaceAllString(s, "≤")
	s = reGeEq.ReplaceAllString(s, "≥")
	s = reBracket.ReplaceAllStringFunc(s, func(c string) string {
		if c == "[" || c == "{" {
			return "("
		}
		return ")"
	})
	s = reDegree.ReplaceAllString(s, "$1°")
	// 注意：$1 后面直接跟中文会被 Go regexp 解析成变量名「1又」（不存在），必须用花括号。
	s = reMixedFraction.ReplaceAllString(s, "${1}又${2}")
	s = reWhitespace.ReplaceAllString(s, "")
	s = strings.ToLower(s)
	s = reAnswerPrefix.ReplaceAllString(s, "")
	s = reOrWord.ReplaceAllString(s, "或")
	s = reTrailingPunct.ReplaceAllString(s, "")
	if reThousands.MatchString(s) {
		s = strings.Replace(s, ",", "", -1)
	}
	return s
}

// normalizeNFKC 只做判题需要的全角→半角映射（ASCII 全角、全角标点、
// 希腊/度符号不在此列）。Go 标准库没有 NFKC；这里覆盖上游数据实际用到的字符。
func normalizeNFKC(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if nr, ok := nfkcMap[r]; ok {
			b.WriteRune(nr)
			continue
		}
		// 全角 ASCII（！～）→ 半角
		if r >= 0xFF01 && r <= 0xFF5E {
			b.WriteRune(r - 0xFEE0)
			continue
		}
		// 全角空格
		if r == 0x3000 {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// nfkcMap 覆盖上游题库/答案中出现的 NFKC 特例。
var nfkcMap = map[rune]rune{
	'＝': '=', '＜': '<', '＞': '>', '＋': '+', '－': '-',
	'／': '/', '＼': '\\', '％': '%', '＊': '*', '＃': '#', '＆': '&',
	'｜': '|', '～': '~', '＾': '^', '＿': '_',
}

/* ---- 数值解析（big.Rat 精确分数） ---- */

// ParsedPart 是答案的一个部分（逗号/分号分隔后）。
type ParsedPart struct {
	Variable  string // "x"、"x(y)" 或 ""
	Kind      string // num / tuple / text
	Key       string // num: "n/d"；tuple: "t:k1,k2"；text: 原文
	UnitName  string
	UnitKnown bool
}

// ParsedAnswer 是 parseAnswer 的结果。
type ParsedAnswer struct {
	Norm  string
	Parts []ParsedPart
}

// splitTop 按顶层分隔符切分（跳过括号内）。
func splitTop(s string, seps string) []string {
	var parts []string
	depth := 0
	cur := strings.Builder{}
	for _, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		}
		if depth == 0 && strings.ContainsRune(seps, c) {
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(c)
	}
	parts = append(parts, cur.String())
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// unitAliases 与上游 UNIT_ALIASES 一致。
var unitAliases = map[string]string{
	"米": "m", "m": "m", "厘米": "cm", "cm": "cm", "毫米": "mm", "mm": "mm",
	"千米": "km", "公里": "km", "km": "km",
	"千克": "kg", "公斤": "kg", "kg": "kg", "克": "g", "g": "g",
	"秒": "s", "s": "s", "分钟": "min", "min": "min", "小时": "h", "h": "h",
	"平方米": "m^2", "m^2": "m^2", "平方厘米": "cm^2", "cm^2": "cm^2",
	"立方米": "m^3", "m^3": "m^3", "立方厘米": "cm^3", "cm^3": "cm^3",
	"元": "元", "度": "°", "°": "°",
	"m/s": "m/s", "米/秒": "m/s", "km/h": "km/h", "千米/时": "km/h",
	"kg/m^3": "kg/m^3", "g/cm^3": "g/cm^3",
	"pa": "Pa", "帕": "Pa", "帕斯卡": "Pa", "kpa": "kPa",
	"hz": "Hz", "赫兹": "Hz", "db": "dB", "分贝": "dB",
	"kw*h": "kW·h", "千瓦时": "kW·h", "ml": "mL", "毫升": "mL",
	"n/kg": "N/kg", "j/kg": "J/kg",
}

var reCJKUnit = regexp.MustCompile(`^[一-龥]{1,4}$`)
var reDegreeRest = regexp.MustCompile(`^[一-龥]{0,2}$`)
var reMixedCn = regexp.MustCompile(`^([+-]?)(?:(\d+)又)?(\d+(?:\.\d+)?|\.\d+)(?:/(\d+(?:\.\d+)?|\.\d+))?(%|°)?(.*)$`)
var reVarAssign = regexp.MustCompile(`^([a-z](?:\([a-z]\))?)=(.+)$`)
var reTupleWrap = regexp.MustCompile(`^\((.+)\)$`)

// canonUnit 规范化单位。ok=false 表示不像单位（不是“数+单位”）。
func canonUnit(u string) (name string, known bool, ok bool) {
	if u == "" {
		return "", true, true
	}
	if n, ok2 := unitAliases[u]; ok2 {
		return n, true, true
	}
	if reCJKUnit.MatchString(u) {
		return u, false, true
	}
	return "", false, false
}

// decToRatio 把（已规范化的）十进制串转成 big.Rat。
func decToRatio(str string) (*big.Rat, bool) {
	r, ok := new(big.Rat).SetString(str)
	return r, ok
}

// isWrapped 整串被一对括号包裹。
func isWrapped(t string) bool {
	if len(t) < 2 || t[0] != '(' || t[len(t)-1] != ')' {
		return false
	}
	depth := 0
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i < len(t)-1 {
				return false
			}
		}
	}
	return depth == 0
}

// parseNumeric 解析数值：整数、小数、分数 a/b、带分数（“1又1/2”）、百分数、
// 带符号/括号；末尾可带单位。失败返回 ok=false。
func parseNumeric(str string) (key string, unitName string, unitKnown bool, ok bool) {
	t := str
	neg := false
	for guard := 0; guard < 4; guard++ {
		if isWrapped(t) {
			t = t[1 : len(t)-1]
			continue
		}
		if len(t) > 1 && (t[0] == '-' || t[0] == '+') && isWrapped(t[1:]) {
			if t[0] == '-' {
				neg = !neg
			}
			t = t[2 : len(t)-1]
			continue
		}
		break
	}
	m := reMixedCn.FindStringSubmatch(t)
	if m == nil {
		return "", "", false, false
	}
	sign, whole, numS, denS, mark, rest := m[1], m[2], m[3], m[4], m[5], m[6]
	if whole != "" && denS == "" {
		return "", "", false, false
	}
	var val *big.Rat
	switch {
	case whole != "":
		n, ok1 := decToRatio(numS)
		d, ok2 := decToRatio(denS)
		if !ok1 || !ok2 || d.Sign() == 0 {
			return "", "", false, false
		}
		f := new(big.Rat).Quo(n, d)
		w, _ := decToRatio(whole)
		// 上游：val = w + f（整数部分 + 分数部分）
		val = new(big.Rat).Add(w, f)
	case denS != "":
		n, ok1 := decToRatio(numS)
		d, ok2 := decToRatio(denS)
		if !ok1 || !ok2 || d.Sign() == 0 {
			return "", "", false, false
		}
		val = new(big.Rat).Quo(n, d)
	default:
		var ok1 bool
		val, ok1 = decToRatio(numS)
		if !ok1 {
			return "", "", false, false
		}
	}
	if sign == "-" {
		neg = !neg
	}
	n := new(big.Rat).Set(val)
	if neg {
		n.Neg(n)
	}
	d := new(big.Rat).Set(big.NewRat(1, 1))
	unitName, unitKnown = "", true
	switch mark {
	case "%":
		n.Quo(n, new(big.Rat).Mul(d, big.NewRat(100, 1)))
		if rest != "" {
			return "", "", false, false
		}
	case "°":
		unitName, unitKnown = "°", true
		if !reDegreeRest.MatchString(rest) {
			return "", "", false, false
		}
	default:
		if rest != "" {
			un, uk, ok2 := canonUnit(rest)
			if !ok2 {
				return "", "", false, false
			}
			unitName, unitKnown = un, uk
		}
	}
	return n.RatString() + "/" + d.Num().String(), unitName, unitKnown, true
}

// analyzePart 分析答案的一个部分：变量名、数值/元组/文本。
func analyzePart(p string) ParsedPart {
	variable := ""
	body := p
	if m := reVarAssign.FindStringSubmatch(p); m != nil {
		variable, body = m[1], m[2]
	}
	if key, un, uk, ok := parseNumeric(body); ok {
		return ParsedPart{Variable: variable, Kind: "num", Key: key, UnitName: un, UnitKnown: uk}
	}
	if m := reTupleWrap.FindStringSubmatch(body); m != nil {
		items := splitTop(m[1], ",")
		keys := make([]string, 0, len(items))
		allOK := len(items) > 1
		for _, it := range items {
			k, _, _, ok := parseNumeric(it)
			if !ok {
				allOK = false
				break
			}
			keys = append(keys, k)
		}
		if allOK {
			return ParsedPart{Variable: variable, Kind: "tuple", Key: "t:" + strings.Join(keys, ",")}
		}
	}
	return ParsedPart{Variable: variable, Kind: "text", Key: body}
}

// parseAnswer 完整解析一个答案串。
func parseAnswer(input string) ParsedAnswer {
	norm := NormalizeText(input)
	norm2 := reAnswerSep.ReplaceAllString(norm, ",")
	parts := splitTop(norm2, ",;")
	out := make([]ParsedPart, 0, len(parts))
	for _, p := range parts {
		out = append(out, analyzePart(p))
	}
	return ParsedAnswer{Norm: norm, Parts: out}
}

// partCompare 比较 two parts：eq / ne / unsure。
// 冻结语义（DEF-6）：变量名必须与参考一致（y=3 vs x=3 不等价）；
// 单位缺失（一方有单位、一方没有）不自判正确，返回 unsure 交人工复核。
func partCompare(a, b ParsedPart) string {
	if a.Kind != b.Kind || a.Key != b.Key {
		return "ne"
	}
	if a.Variable != b.Variable {
		return "ne"
	}
	if a.UnitName == b.UnitName {
		return "eq"
	}
	if a.UnitName == "" || b.UnitName == "" {
		return "unsure" // DEF-6：单位缺失转人工
	}
	if a.UnitKnown && b.UnitKnown {
		return "ne"
	}
	return "unsure"
}

// sameMultiset 集合比较（忽略顺序），返回 eq / unsure / ""（不匹配）。
func sameMultiset(as, bs []ParsedPart, ignoreVar bool) string {
	if len(as) != len(bs) {
		return ""
	}
	left := append([]ParsedPart(nil), bs...)
	fuzzy := false
	for _, a := range as {
		hit := -1
		for j, l := range left {
			if !ignoreVar && a.Variable != l.Variable {
				continue
			}
			c := partCompare(a, l)
			if c == "ne" {
				continue
			}
			if c == "unsure" {
				fuzzy = true
			}
			hit = j
			break
		}
		if hit < 0 {
			return ""
		}
		left = append(left[:hit], left[hit+1:]...)
	}
	if fuzzy {
		return "unsure"
	}
	return "eq"
}

func allNumeric(parts []ParsedPart) bool {
	for _, p := range parts {
		if p.Kind != "num" && p.Kind != "tuple" {
			return false
		}
	}
	return true
}

// compareParsed 结构化比较。
func compareParsed(u, r ParsedAnswer) string {
	var res string
	if len(u.Parts) == len(r.Parts) {
		refVars := make([]string, len(r.Parts))
		distinct := len(r.Parts) > 1
		seen := map[string]bool{}
		for i, p := range r.Parts {
			refVars[i] = p.Variable
			if p.Variable == "" {
				distinct = false
			} else if seen[p.Variable] {
				distinct = false
			} else {
				seen[p.Variable] = true
			}
		}
		if distinct {
			allU, allNoU := true, true
			for _, p := range u.Parts {
				if p.Variable == "" {
					allU = false
				} else {
					allNoU = false
				}
			}
			if allU {
				res = sameMultiset(u.Parts, r.Parts, false)
			} else if allNoU {
				res = "eq"
				for i := range u.Parts {
					c := partCompare(u.Parts[i], r.Parts[i])
					if c == "ne" {
						res = ""
						break
					}
					if c == "unsure" {
						res = "unsure"
					}
				}
			} else {
				return "unsure"
			}
		} else {
			res = sameMultiset(u.Parts, r.Parts, true)
		}
	}
	if res == "eq" {
		return "equal"
	}
	if res == "unsure" {
		return "unsure"
	}
	if allNumeric(u.Parts) && allNumeric(r.Parts) {
		return "different"
	}
	return "unsure"
}

// JudgeAnswer 只按参考答案文本判（上游旧接口）。
func JudgeAnswer(user, reference string) JudgeResult {
	u := parseAnswer(user)
	r := parseAnswer(reference)
	if u.Norm == "" {
		return JudgeResult{VerdictManual, ReasonEmpty}
	}
	if u.Norm == r.Norm {
		return JudgeResult{VerdictCorrect, ReasonIdentical}
	}
	if len(r.Parts) == 0 {
		return JudgeResult{VerdictManual, ReasonNoReference}
	}
	switch compareParsed(u, r) {
	case "equal":
		return JudgeResult{VerdictCorrect, ReasonEquivalent}
	case "different":
		return JudgeResult{VerdictWrong, ReasonNumericMismatch}
	}
	return JudgeResult{VerdictManual, ReasonUnsure}
}

/* ---- 按题型判题 ---- */

var reChoiceLetter = regexp.MustCompile(`^[(（]?([A-Za-z])[)）.．、:：]?$`)

func judgeChoice(user string, q *Question) JudgeResult {
	raw := strings.TrimSpace(user)
	if raw == "" {
		return manualEmpty
	}
	letter := ""
	if m := reChoiceLetter.FindStringSubmatch(raw); m != nil {
		letter = strings.ToUpper(m[1])
	} else {
		n := NormalizeText(raw)
		for _, c := range q.Choices {
			if NormalizeText(c.Text) == n {
				letter = c.Key
				break
			}
		}
	}
	if letter == "" {
		return JudgeResult{VerdictManual, ReasonUnrecognized}
	}
	found := false
	for _, c := range q.Choices {
		if c.Key == letter {
			found = true
			break
		}
	}
	if !found {
		return JudgeResult{VerdictManual, ReasonUnrecognized}
	}
	if letter == q.Answer {
		return JudgeResult{VerdictCorrect, ReasonChoice}
	}
	return JudgeResult{VerdictWrong, ReasonChoice}
}

// refsCarryUnit 检查题目全部可用参考（q.Answer 与 q.Accept）的单位语义：
// 任一参考的任一数值段（含元组元素、°）带单位即返回 true（DEF-JUDGE-ORACLE-R1）。
// 带单位参考的题，纯数值 oracle 无法对拍单位语义（3cm ≠ 3），
// 参与判 correct/wrong 都会绕过既有的单位缺失/人工判定路径。
func refsCarryUnit(q *Question) bool {
	for _, ref := range append([]string{q.Answer}, q.Accept...) {
		if refCarriesUnit(ref) {
			return true
		}
	}
	return false
}

// refCarriesUnit 检查单个参考答案文本是否带单位：与 parseAnswer 同样
// 规范化并按顶层分隔拆段，每段去掉变量赋值后由 segCarriesUnit 判定。
func refCarriesUnit(ref string) bool {
	norm2 := reAnswerSep.ReplaceAllString(NormalizeText(ref), ",")
	for _, seg := range splitTop(norm2, ",;") {
		body := seg
		if m := reVarAssign.FindStringSubmatch(seg); m != nil {
			body = m[2]
		}
		if segCarriesUnit(body) {
			return true
		}
	}
	return false
}

// segCarriesUnit 判定一个答案段是否带单位：数值段直接看解析出的单位；
// 元组段递归检查各元素（ParsedPart 的 tuple key 不保留单位，需重扫原文）。
// 文本式参考（DEF-JUDGE-TEXT-UNIT-R1）如「5千克苹果」「约3小时」解析不出
// 数值段，但数字后紧跟已知单位词时同样携带单位语义，安全阻断 oracle；
// 无法可靠解析的文本参考保守阻断（不能被误判为可自动裁决）。
func segCarriesUnit(body string) bool {
	if _, un, _, ok := parseNumeric(body); ok {
		return un != ""
	}
	if m := reTupleWrap.FindStringSubmatch(body); m != nil {
		for _, it := range splitTop(m[1], ",") {
			if segCarriesUnit(it) {
				return true
			}
		}
		return false
	}
	// 文本段：数字后紧跟单位词（含 unitAliases 键与 1-4 汉字疑似单位）→ 阻断。
	if reDigitUnitWord.MatchString(body) {
		return true
	}
	// 其余无法可靠解析的文本（含数字但无单位形态）保守阻断；
	// 纯文本（无数字）交回既有 text 比较路径，不影响 oracle 参与。
	return hasDigit.MatchString(body)
}

// hasDigit 段中含 ASCII 数字（NormalizeText 已把全角数字转半角）。
var hasDigit = regexp.MustCompile(`\d`)

// reDigitUnitWord 数字后紧跟单位词：unitAliases 的多字符键按长度降序排列，
// 避免「千克」被单字符候选抢先吃掉；1-4 汉字疑似单位兜底（上游 canonUnit 同款）。
var reDigitUnitWord = regexp.MustCompile(`\d(?:` + digitUnitWordAlts() + `)`)

func digitUnitWordAlts() string {
	keys := make([]string, 0, len(unitAliases)+1)
	for k := range unitAliases {
		if k != "" && !strings.ContainsAny(k, `\.+*?()|[]{}^$`) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	quoted := make([]string, 0, len(keys))
	for _, k := range keys {
		quoted = append(quoted, regexp.QuoteMeta(k))
	}
	return strings.Join(quoted, "|") + `|[一-龥]{1,4}`
}

// verifyOracle 用题目自带的受限 verify 表达式作为数值判题的额外 oracle。
// 返回 (结果, true) 表示 oracle 给出结论；(_, false) 表示 oracle 不可用
// （无表达式、求值失败、结果非数值、或用户输入不是可比较的单一数值）。
// 求值失败静默回退（verify 是辅助证据，不是硬依赖）。
//
// 适用边界（DEF-JUDGE-ORACLE-R1，oracle 参与前判定，任一命中即 ok=false，
// 交回既有 numeric 单位/人工路径决定，不得自行判 correct/wrong）：
//   - q.Percent 题：verify 可能写成比例（0.5）也可能写成百分数（50），
//     oracle 不猜缩放语义，一律交回既有 stripPercent/数值比较路径；
//   - 参考带单位：q.Answer 与 q.Accept 任一可用参考解析出带单位的数值时，
//     单位语义（3cm = 0.03m）超出纯数值对拍能力——answer=3cm、verify=3、
//     用户答 3 时强行对拍会绕过 DEF-6 的单位缺失转人工误判 correct/verify。
func verifyOracle(user string, q *Question) (JudgeResult, bool) {
	if q.Verify == "" {
		return JudgeResult{}, false
	}
	// percent 题一律不参与：不猜 verify 的 0.5/50 缩放。
	if q.Percent {
		return JudgeResult{}, false
	}
	// 任一可用参考带单位：oracle 退出，由既有单位/人工路径决定。
	if refsCarryUnit(q) {
		return JudgeResult{}, false
	}
	v, err := (Verifier{}).Eval(q.Verify)
	if err != nil {
		return JudgeResult{}, false // 求值失败：安全回退现有判题路径
	}
	// verify 结果需为数值（分数/浮点/布尔）；列表/字符串（多解/文本）不参与单值对拍。
	// 非有限浮点（Inf/NaN，如 10.0**400 / 1e999）必须拒绝：SetFloat64 对它们返回 nil，
	// 后续 Cmp 会 panic；oracle 直接不可用，回退现有判题路径。
	var expect *big.Rat
	switch v.kind {
	case 'q':
		expect = v.rat
	case 'f':
		if math.IsNaN(v.f) || math.IsInf(v.f, 0) {
			return JudgeResult{}, false
		}
		r := new(big.Rat).SetFloat64(v.f)
		if r == nil {
			return JudgeResult{}, false
		}
		expect = r
	case 'b':
		if v.b {
			expect = big.NewRat(1, 1)
		} else {
			expect = new(big.Rat)
		}
	default:
		return JudgeResult{}, false
	}
	// 用户输入需为单一数值部分（数值/元组均要求单部）。
	// 带单位的输入（3cm）不与无单位期望对拍：单位语义（3cm = 0.03m）超出
	// 纯数值 oracle 能力，强行对拍会把 3cm 误判成 3。oracle 回退现有路径。
	u := parseAnswer(user)
	if u.Norm == "" || len(u.Parts) != 1 || u.Parts[0].Kind != "num" {
		return JudgeResult{}, false
	}
	if u.Parts[0].UnitName != "" {
		return JudgeResult{}, false
	}
	ur, ok := new(big.Rat).SetString(u.Parts[0].Key[:strings.LastIndexByte(u.Parts[0].Key, '/')])
	if !ok {
		return JudgeResult{}, false
	}
	if ur.Cmp(expect) == 0 {
		return JudgeResult{VerdictCorrect, ReasonVerify}, true
	}
	return JudgeResult{VerdictWrong, ReasonVerify}, true
}

func judgeNumeric(user string, q *Question) JudgeResult {
	u := parseAnswer(user)
	if u.Norm == "" {
		return manualEmpty
	}
	refs := append([]string{q.Answer}, q.Accept...)
	for _, ref := range refs {
		if parseAnswer(ref).Norm == u.Norm {
			return JudgeResult{VerdictCorrect, ReasonIdentical}
		}
	}
	main := parseAnswer(q.Answer)
	if !q.Unordered && len(main.Parts) > 1 {
		// 多个答案且顺序有关：逐项按顺序比较（变量名忽略）
		if len(u.Parts) != len(main.Parts) {
			if allNumeric(u.Parts) {
				return JudgeResult{VerdictWrong, ReasonCountMismatch}
			}
			return JudgeResult{VerdictManual, ReasonUnsure}
		}
		res := "eq"
		for i := range u.Parts {
			c := partCompare(u.Parts[i], main.Parts[i])
			if c == "ne" {
				res = "ne"
				break
			}
			if c == "unsure" {
				res = "unsure"
			}
		}
		if res == "eq" {
			return JudgeResult{VerdictCorrect, ReasonEquivalent}
		}
		if res == "ne" && allNumeric(u.Parts) && allNumeric(main.Parts) {
			return JudgeResult{VerdictWrong, ReasonNumericMismatch}
		}
		return JudgeResult{VerdictManual, ReasonUnsure}
	}
	r := JudgeAnswer(user, q.Answer)
	if r.Verdict == VerdictCorrect {
		return r
	}
	for i := 1; i < len(refs); i++ {
		if JudgeAnswer(user, refs[i]).Verdict == VerdictCorrect {
			return JudgeResult{VerdictCorrect, ReasonAccept}
		}
	}
	// verify oracle：现有路径未能给出 correct 时，用受限表达式对拍。
	// 只增强不放宽：oracle 判 wrong 时收敛 unsure→wrong（更准），
	// 判 correct 时给出 correct（等价写法/单位同义等现有遗漏场景）。
	if vr, ok := verifyOracle(user, q); ok {
		return vr
	}
	return r
}

func judgeShort(user string, q *Question) JudgeResult {
	u := NormalizeText(user)
	if u == "" {
		return manualEmpty
	}
	refs := append([]string{q.Answer}, q.Accept...)
	for i, ref := range refs {
		if NormalizeText(ref) == u {
			if i == 0 {
				return JudgeResult{VerdictCorrect, ReasonIdentical}
			}
			return JudgeResult{VerdictCorrect, ReasonAccept}
		}
	}
	for _, ref := range refs {
		if JudgeAnswer(user, ref).Verdict == VerdictCorrect {
			return JudgeResult{VerdictCorrect, ReasonEquivalent}
		}
	}
	return JudgeResult{VerdictManual, ReasonUnsure}
}

// stripPercent percent 题去掉末尾 %/％（全角已由 NormalizeText 处理）。
func stripPercent(user string) string {
	return regexp.MustCompile(`\s*[%％]$`).ReplaceAllString(strings.TrimSpace(user), "")
}

// JudgeQuestion 按题型判题：choice 比字母（或选项文字）；
// numeric 数值等价（percent 题接受 50 和 50%）；short 规范化精确匹配 + accept；
// self 永远自评；legacy 回退 JudgeAnswer。
func JudgeQuestion(q *Question, user string) JudgeResult {
	switch q.Type {
	case "choice":
		return judgeChoice(user, q)
	case "numeric":
		if q.Percent {
			return judgeNumeric(stripPercent(user), q)
		}
		return judgeNumeric(user, q)
	case "short":
		return judgeShort(user, q)
	case "self":
		return JudgeResult{VerdictManual, ReasonSelf}
	default:
		return JudgeAnswer(user, q.Answer)
	}
}
