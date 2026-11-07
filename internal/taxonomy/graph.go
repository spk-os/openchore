package taxonomy

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

// ancestorSet 见 catalog.go（并发安全的带缓存祖先集合）。

// Ancestors 返回一组知识点的全部严格祖先（不含自身），确定性顺序按 Topics 文件顺序。
func (c *Catalog) Ancestors(ids []string) []string {
	set := map[string]bool{}
	for _, id := range ids {
		for a := range c.ancestorSet(id) {
			set[a] = true
		}
	}
	return c.sortedByIndex(set)
}

// sortedByIndex 把 id 集合按 Topics 文件顺序排序（引擎确定性的基础）。
func (c *Catalog) sortedByIndex(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return c.Index[out[i]] < c.Index[out[j]] })
	return out
}

// TopoSort 对节点子集做拓扑排序（先修在前）；同层按 Topics 文件顺序，确定性。
// 子集有环时报错（Load 已保证全图无环，子集不会成环，防御性保留）。
func (c *Catalog) TopoSort(ids []string) ([]string, error) {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	indeg := make(map[string]int, len(set))
	var ready []string
	for id := range set {
		n := 0
		for _, e := range c.Prereqs[id] {
			if set[e.PrerequisiteID] {
				n++
			}
		}
		indeg[id] = n
		if n == 0 {
			ready = append(ready, id)
		}
	}
	byIndex := func(a, b string) bool { return c.Index[a] < c.Index[b] }
	order := make([]string, 0, len(set))
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool { return byIndex(ready[i], ready[j]) })
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, e := range c.DepsOf[id] {
			d := e.TopicID
			if !set[d] {
				continue
			}
			indeg[d]--
			if indeg[d] == 0 {
				ready = append(ready, d)
			}
		}
	}
	if len(order) != len(set) {
		return nil, errors.New("拓扑排序失败：子图存在环")
	}
	return order, nil
}

// Subgraph 返回目标集合的先修子图：目标 + 所有祖先，拓扑序。
type Subgraph struct {
	Nodes []string
	Set   map[string]bool
}

func (c *Catalog) Subgraph(targets []string) (*Subgraph, error) {
	set := map[string]bool{}
	for _, t := range targets {
		set[t] = true
	}
	for a := range c.ancestorMulti(targets) {
		set[a] = true
	}
	nodes, err := c.TopoSort(c.sortedByIndex(set))
	if err != nil {
		return nil, err
	}
	return &Subgraph{Nodes: nodes, Set: set}, nil
}

func (c *Catalog) ancestorMulti(ids []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		for a := range c.ancestorSet(id) {
			out[a] = true
		}
	}
	return out
}

// IsTestable 该知识点是否可测：非 external 且有题（含 legacy 合成题）。
func (c *Catalog) IsTestable(id string) bool {
	t, ok := c.ByID[id]
	return ok && !t.External && len(c.Bank[id]) > 0
}

// SubjectOf 推断知识点的学科：显式 subject 字段优先，否则按 id 前缀
// （cne_ 英语、cnp_ 物理、cnc_ 语文、cns_ 科学、cnd_ 道德与法治、cnh_ 历史、
// cng_ 地理、cnb_ 生物、cnk_ 化学），其余算数学。与上游 subjectOf 一致。
func (c *Catalog) SubjectOf(t *Topic) string {
	if t == nil {
		return SubjectMath
	}
	switch t.Subject {
	case SubjectEnglish, SubjectMath, SubjectPhysics, SubjectChinese, SubjectScience,
		SubjectMorality, SubjectHistory, SubjectGeography, SubjectBiology, SubjectChemistry:
		return t.Subject
	}
	switch {
	case strings.HasPrefix(t.ID, "cne_"):
		return SubjectEnglish
	case strings.HasPrefix(t.ID, "cnp_"):
		return SubjectPhysics
	case strings.HasPrefix(t.ID, "cnc_"):
		return SubjectChinese
	case strings.HasPrefix(t.ID, "cns_"):
		return SubjectScience
	case strings.HasPrefix(t.ID, "cnd_"):
		return SubjectMorality
	case strings.HasPrefix(t.ID, "cnh_"):
		return SubjectHistory
	case strings.HasPrefix(t.ID, "cng_"):
		return SubjectGeography
	case strings.HasPrefix(t.ID, "cnb_"):
		return SubjectBiology
	case strings.HasPrefix(t.ID, "cnk_"):
		return SubjectChemistry
	}
	return SubjectMath
}

// 十学科常量。
const (
	SubjectMath      = "math"
	SubjectChinese   = "chinese"
	SubjectEnglish   = "english"
	SubjectPhysics   = "physics"
	SubjectChemistry = "chemistry"
	SubjectScience   = "science"
	SubjectMorality  = "morality"
	SubjectHistory   = "history"
	SubjectGeography = "geography"
	SubjectBiology   = "biology"
	SubjectAll       = "all"
)

// gradeRank 年级数字 ×2 + 学期，用于学习路径排序；无 grade 返回 0。
func (c *Catalog) gradeRank(id string) int {
	t := c.ByID[id]
	if t == nil {
		return 0
	}
	r := t.Grade * 2
	if t.Semester == "下" {
		r++
	}
	return r
}

// Chapter 解析结果：volume 册次、chapterNo 章号、chapterName 章名、section 节。
type Chapter struct {
	Volume      string `json:"volume"`
	ChapterNo   string `json:"chapterNo"`
	ChapterName string `json:"chapterName"`
	Section     string `json:"section"`
}

var (
	reChPrimary    = regexp.MustCompile(`^([一二三四五六七八九])年级([上下全])册 (第\d+单元|预备单元\d) (.+)$`)
	reChPrimaryI   = regexp.MustCompile(`^([一二三四五六])年级([上下])册 (综合与实践) (.+)$`)
	reChPrimaryRev = regexp.MustCompile(`^([一二三四五六])年级([上下])册 (复习) (.+)$`)
	reChEnHs       = regexp.MustCompile(`^(必修第([一二三])册|选择性必修第([一二三四])册) (欢迎单元|第\d+单元) (.+)$`)
	reChHsMI       = regexp.MustCompile(`^(高[一二]|选必[一二三]) (数学建模|数学探究) (\S+)$`)
	reChGeneric    = regexp.MustCompile(`^(\S+) (第\S+章) (\S+?)(?: (\S+))?$`)
)

// ParseChapter 解析人教版章节名。与上游 parseChapter 同一组正则、同一优先级：
// 小学「一年级上册 第1单元 …」/ 综合与实践 / 复习；高中英语按册名；
// 高中建模探究不拆节；其余「册次 第N章 章名 节」。
func ParseChapter(chapter string) Chapter {
	s := chapter
	if m := reChPrimary.FindStringSubmatch(s); m != nil {
		return Chapter{Volume: m[1] + m[2], ChapterNo: m[3], ChapterName: m[4]}
	}
	if m := reChPrimaryI.FindStringSubmatch(s); m != nil {
		return Chapter{Volume: m[1] + m[2], ChapterNo: m[3], ChapterName: m[4]}
	}
	if m := reChPrimaryRev.FindStringSubmatch(s); m != nil {
		return Chapter{Volume: m[1] + m[2], ChapterNo: m[3], ChapterName: m[4]}
	}
	if m := reChEnHs.FindStringSubmatch(s); m != nil {
		vol := "选必" + m[3]
		if m[2] != "" {
			vol = "必修" + m[2]
		}
		return Chapter{Volume: vol, ChapterNo: m[4], ChapterName: m[5]}
	}
	if m := reChHsMI.FindStringSubmatch(s); m != nil {
		return Chapter{Volume: m[1], ChapterNo: m[2], ChapterName: m[3]}
	}
	if m := reChGeneric.FindStringSubmatch(s); m != nil {
		return Chapter{Volume: m[1], ChapterNo: m[2], ChapterName: m[3], Section: m[4]}
	}
	return Chapter{ChapterName: s}
}

// GradeTargets 返回整年级 / 整册的知识点集合。
// scope 例：'一年级'（=一上+一下）、'七上'、'九全'、'高一'、'选必二'、
// '必修一'（英语）、'选择性必修第四册'。subject 缺省 math；
// 'english'/'physics'/…/  'all' 限定学科。不认识的写法返回 nil。
// 结果（ids 按 Topics 文件顺序，只含正式知识点）。
type GradeTarget struct {
	IDs     []string
	Volumes []string
	Label   string
}

var (
	reGradeWholeYear = regexp.MustCompile(`^([一二三四五六七八九])年级$`)
	reGradeHalf      = regexp.MustCompile(`^([一二三四五六七八九])([上下])$`)
	reGradeWholeBook = regexp.MustCompile(`^([一二三四五六七八九])全$`)
)

// hsBooks 高中数学/英语共用的册名映射（键 -> {短名, 标签}）。
var hsBooks = map[string][2]string{
	"高一":       {"高一", "高一（必修第一册）"},
	"高一必修":     {"高一", "高一（必修第一册）"},
	"高二":       {"高二", "高二（必修第二册）"},
	"高二必修":     {"高二", "高二（必修第二册）"},
	"选必一":      {"选必一", "选必一（选择性必修第一册）"},
	"选择性必修第一册": {"选必一", "选必一（选择性必修第一册）"},
	"选必二":      {"选必二", "选必二（选择性必修第二册）"},
	"选择性必修第二册": {"选必二", "选必二（选择性必修第二册）"},
	"选必三":      {"选必三", "选必三（选择性必修第三册）"},
	"选择性必修第三册": {"选必三", "选必三（选择性必修第三册）"},
	"必修一":      {"必修一", "必修一（必修第一册）"},
	"必修第一册":    {"必修一", "必修一（必修第一册）"},
	"必修二":      {"必修二", "必修二（必修第二册）"},
	"必修第二册":    {"必修二", "必修二（必修第二册）"},
	"必修三":      {"必修三", "必修三（必修第三册）"},
	"必修第三册":    {"必修三", "必修三（必修第三册）"},
	"选必四":      {"选必四", "选必四（选择性必修第四册）"},
	"选择性必修第四册": {"选必四", "选必四（选择性必修第四册）"},
}

// GradeTargets 解析 scope 并按学科过滤知识点。无匹配返回 (nil, nil)。
func (c *Catalog) GradeTargets(scope, subject string) (*GradeTarget, error) {
	if subject == "" {
		subject = SubjectMath
	}
	key := strings.TrimSpace(scope)
	var volumes []string
	label := ""
	if b, ok := hsBooks[key]; ok {
		volumes = []string{b[0]}
		label = b[1]
	} else if m := reGradeWholeYear.FindStringSubmatch(key); m != nil {
		volumes = []string{m[1] + "上", m[1] + "下"}
		if m[1] == "九" {
			volumes = append(volumes, "九全")
		}
		label = m[1] + "年级"
	} else if m := reGradeWholeBook.FindStringSubmatch(key); m != nil {
		volumes = []string{m[1] + "全"}
		label = m[1] + "年级全一册"
	} else if m := reGradeHalf.FindStringSubmatch(key); m != nil {
		volumes = []string{key}
		if m[2] == "上" {
			label = m[1] + "年级上册"
		} else {
			label = m[1] + "年级下册"
		}
	} else {
		return nil, nil
	}
	ids := make([]string, 0)
	for _, t := range c.Topics {
		if t.External {
			continue
		}
		if subject != SubjectAll && c.SubjectOf(t) != subject {
			continue
		}
		vol := ParseChapter(t.Chapter).Volume
		if !containsStr(volumes, vol) {
			continue
		}
		ids = append(ids, t.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return &GradeTarget{IDs: ids, Volumes: volumes, Label: label}, nil
}

// ResolveTargets 把 URL / 选择器里的 id 展开成图中的知识点。
// aliases 形如 Load() 提供的 c.Aliases（cnm_ext_* -> targets）。
// 未知 id 原样保留，交给 CreateSession 报错。
func ResolveTargets(rawIDs []string, aliases map[string]AliasEntry) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, id := range rawIDs {
		if id == "" {
			continue
		}
		var targets []string
		if a, ok := aliases[id]; ok && len(a.Targets) > 0 {
			targets = a.Targets
		} else {
			targets = []string{id}
		}
		for _, tid := range targets {
			if !seen[tid] {
				seen[tid] = true
				out = append(out, tid)
			}
		}
	}
	return out
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
