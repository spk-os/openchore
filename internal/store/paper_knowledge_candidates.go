package store

// 试卷判题知识点候选检索 + AI external_ref → 本机标签的 additive 合并落库
// （任务卡 TX-AI-KC-CANDIDATE-DAO-CODER）。本文件是独立 DAO，不改
// error_items.go / taxonomy.go / llm / model / migration：
//
//  1. SearchKnowledgeCandidates —— 同学科、系统（is_system=1）、非空
//     external_ref 知识点的有界确定性候选检索，供判题 prompt 注入真实
//     候选集（llm 层已冻结 80 上限与严格白名单，本方法负责"选哪 80 个"）；
//  2. ResolveRefTagIDs —— external_ref → 本机 tag id 的同学科批量精确解析；
//  3. MergeErrorItemKnowledgeRefs —— 短事务内把解析成功的 ref 以并集语义
//     补插进 error_item_tags：只增不删（ReplaceErrorItemTags 的全替换会
//     覆盖家长并发编辑，AI 自动绑定冻结为 additive），不重置任何字段或
//     FSRS 状态，绝不创建新知识点。
//
// 行为测试由独立测试卡补齐（本卡验收仅 gofmt/build/vet，DAO 行为 pending）。
// 事务内只允许走 tx：连接池单写者（MaxOpenConns=1），事务里再借 s.db 会
// 等在自己占着的连接上（同 ReplaceErrorItemTags 的既有注释）。

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"unicode"
)

// maxPaperKnowledgeCandidates 是单次候选检索的默认值与硬上限，与 llm 层
// JudgeQuestionWithCandidates 的冻结上限（internal/llm maxKnowledgeCandidates
// = 80）保持一致：limit<=0 取 80，>80 截到 80，调用方无法借 DAO 突破判题
// 层的候选注入上限。
const maxPaperKnowledgeCandidates = 80

// KnowledgeCandidateRow 是候选检索的一行：本机 knowledge_tags 行 id、上游
// 稳定 external_ref、展示名、学科与导入顺序。ExternalRef/Name/Subject 与
// llm.KnowledgeCandidate 的 JSON 形状对齐，API 层可直接映射，但本结构
// 刻意不依赖 llm 包（store 不得 import AI 客户端层）。
type KnowledgeCandidateRow struct {
	ID          int64  `json:"id"`
	ExternalRef string `json:"external_ref"`
	Name        string `json:"name"`
	Subject     string `json:"subject"`
	SortOrder   int    `json:"sort_order"`
}

// SearchKnowledgeCandidates 检索判题用的同学科知识点候选。subject trim 后
// 为空返回空切片；结果始终非 nil（错误时返回 nil + err，调用方先看 err，
// 同 BrowseTopics 的既有约定）。排序有界且确定，共四档：
//
//   - 第 0 档：seedRefs 精确命中（大小写敏感）的 external_ref，按 seed 顺序
//     置前——调用方把已知高置信 ref（如上一题绑定点）作为种子传入；
//   - 第 1 档：questionText 精确包含的 external_ref；
//   - 第 2 档：questionText 精确包含的知识点名称；
//   - 第 3 档：名称关键词相关度（名称分词在文本分词集中的命中数）降序；
//   - 第 4 档：其余按 (sort_order, id) 稳定补齐。
//
// questionText 由调用方合并题干与 KnowledgePoints 自由文本（AI 判读的自由
// 文本知识点名往往比题干更接近体系名），本 DAO 只看这一个字符串。第 0-2
// 档为大小写精确的 strings.Contains；第 3 档是相关度启发式，ASCII 分词做
// 小写化。去重：seedRefs 按首次出现去重，行级无需去重（external_ref 有
// UNIQUE 部分索引）。候选行数以同学科系统知识点总量为上界（当前数据每学
// 科 ≤ ~800），排序比较器以 (sort_order, id) 收尾构成全序，不依赖 map 遍
// 历顺序。limit<=0 默认 80，>80 截到 80。
func (s *Store) SearchKnowledgeCandidates(ctx context.Context, subject, questionText string, seedRefs []string, limit int) ([]KnowledgeCandidateRow, error) {
	out := make([]KnowledgeCandidateRow, 0)
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return out, nil
	}
	if limit <= 0 {
		limit = maxPaperKnowledgeCandidates
	}
	if limit > maxPaperKnowledgeCandidates {
		limit = maxPaperKnowledgeCandidates
	}
	questionText = strings.TrimSpace(questionText)

	// 单条只读查询：同学科 + 系统知识点 + 非空 external_ref，按导入顺序
	// 返回（idx_kt_subject_grade 的 subject 前缀可用）。
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, subject, external_ref, sort_order
		 FROM knowledge_tags
		 WHERE subject = ? AND is_system = 1 AND external_ref IS NOT NULL AND external_ref <> ''
		 ORDER BY sort_order, id`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// 种子 ref：trim、去空、按首次出现去重（顺序即第 0 档的位次）。
	seedIndex := make(map[string]int, len(seedRefs))
	for _, ref := range seedRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, dup := seedIndex[ref]; !dup {
			seedIndex[ref] = len(seedIndex)
		}
	}
	var textTokens map[string]struct{}
	if questionText != "" {
		textTokens = kcTokenSet(questionText)
	}

	type rankedCandidate struct {
		row     KnowledgeCandidateRow
		tier    int // 0 seed / 1 ref-in-text / 2 name-in-text / 3 keyword / 4 filler
		seedIdx int // 第 0 档：seed 中的位次
		score   int // 第 3 档：关键词命中数
	}
	cands := make([]rankedCandidate, 0, 128)
	for rows.Next() {
		var r KnowledgeCandidateRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Subject, &r.ExternalRef, &r.SortOrder); err != nil {
			return nil, err
		}
		r.ExternalRef = strings.TrimSpace(r.ExternalRef)
		if r.ExternalRef == "" {
			continue // 空白 ref 不进候选（白名单路径要求非空）
		}
		c := rankedCandidate{row: r, tier: 4}
		if idx, ok := seedIndex[r.ExternalRef]; ok {
			c.tier, c.seedIdx = 0, idx
		} else if questionText != "" && strings.Contains(questionText, r.ExternalRef) {
			c.tier = 1
		} else if questionText != "" && r.Name != "" && strings.Contains(questionText, r.Name) {
			c.tier = 2
		} else if textTokens != nil {
			if hits := kcTokenHits(kcTokenSet(r.Name), textTokens); hits > 0 {
				c.tier, c.score = 3, hits
			}
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 全序比较：档位 → 档内主键（seed 位次 / 命中数）→ (sort_order, id)。
	// external_ref 唯一保证第 0 档 seedIdx 互异，id 唯一保证收尾终结，
	// 排序结果与输入顺序、map 遍历序无关。
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.tier == 0 && a.seedIdx != b.seedIdx {
			return a.seedIdx < b.seedIdx
		}
		if a.tier == 3 && a.score != b.score {
			return a.score > b.score
		}
		if a.row.SortOrder != b.row.SortOrder {
			return a.row.SortOrder < b.row.SortOrder
		}
		return a.row.ID < b.row.ID
	})

	if len(cands) > limit {
		cands = cands[:limit]
	}
	for _, c := range cands {
		out = append(out, c.row)
	}
	return out, nil
}

// kcRefQueryer 是 ref 解析所需的最小查询能力，*sql.DB 与 *sql.Tx 都满足：
// 公开入口 ResolveRefTagIDs 走 s.db，事务内合并 MergeErrorItemKnowledgeRefs
// 走 tx（单写者连接池约束，见文件头）。
type kcRefQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ResolveRefTagIDs 把 external_ref 批量解析成本机 knowledge_tag id。仅同学
// 科、系统内置（is_system=1，与 SearchKnowledgeCandidates 的候选过滤对齐）、
// 精确（SQLite BINARY 比较，大小写敏感）命中的 ref 进结果：未知 ref、异学
// 科、大小写不一致、家长/AI 自建行（is_system=0）一律不解析；数字串只在某
// 系统知识点 external_ref 恰为该数字时命中（本 DAO 从不把 ref 当数字主键解
// 释），且绝不创建知识点。external_ref 的 UNIQUE 部分索引下家长行可能占用
// 某 ref——宁可让该 ref 解析失败，也不绑到家长行。subject 或 refs 为空返回
// 空 map。未命中由"键不存在"表达，调用方用 `id, ok := m[ref]` 判断。
func (s *Store) ResolveRefTagIDs(ctx context.Context, subject string, refs []string) (map[string]int64, error) {
	return resolveRefTagIDs(ctx, s.db, subject, refs)
}

// resolveRefTagIDs 是 ResolveRefTagIDs 的查询器参数化形式（s.db / tx 双用）。
// 返回 map 的键即去重后的输入 ref：SQL 精确匹配保证扫描出的 external_ref
// 与输入逐字节相同。is_system=1 是解析的白名单边界：判题候选来自系统体系
// （Search 同款过滤），解析也只认系统行——家长自建行即使 external_ref 恰好
// 相同也不进结果。
func resolveRefTagIDs(ctx context.Context, q kcRefQueryer, subject string, refs []string) (map[string]int64, error) {
	out := make(map[string]int64)
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return out, nil
	}
	distinct := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		seen[ref] = struct{}{}
		distinct = append(distinct, ref)
	}
	if len(distinct) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(distinct))
	args := make([]any, 0, len(distinct)+1)
	args = append(args, subject)
	for i, ref := range distinct {
		placeholders[i] = "?"
		args = append(args, ref)
	}
	rows, err := q.QueryContext(ctx,
		`SELECT external_ref, id FROM knowledge_tags
		 WHERE subject = ? AND is_system = 1 AND external_ref IN (`+
			strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref string
		var id int64
		if err := rows.Scan(&ref, &id); err != nil {
			return nil, err
		}
		out[ref] = id
	}
	return out, rows.Err()
}

// ErrErrorItemSubjectMismatch 是 MergeErrorItemKnowledgeRefs 的学科冲突哨兵：
// 目标错题在库里的最终 subject（家长可能并发改过）与调用参数不一致，或调用
// 方传了空学科。item 不存在复用既有 ErrErrorItemNotFound。调用方用
// errors.Is 识别，不做字符串匹配。
var ErrErrorItemSubjectMismatch = errors.New("error item subject does not match the merge subject")

// MergeErrorItemKnowledgeRefs 把 AI 判读选中的 external_ref 以 additive 方式
// 合并进一条错题的知识标签：解析成功的 ref 只补插 error_item_tags 缺失链
// 接——不 DELETE、不整体替换（ReplaceErrorItemTags 的全替换会覆盖家长并发
// 编辑，AI 自动绑定冻结为 advisory 并集）、不重置任何字段/FSRS 状态、绝不
// 创建新知识点。重复 ref 与已有链接幂等（INSERT OR IGNORE）。解析沿
// ResolveRefTagIDs 的系统行白名单（is_system=1）：异学科、未知、空 ref 与
// 家长/AI 自建同 ref 行都被静默忽略。
//
// 返回 (resolved, attached)：resolved = 解析成功的不同 ref 数；attached =
// 实际新插入的链接数（链接已全部存在时 attached=0 而resolved>0）。异学科/
// 未知/空 ref 被静默忽略，被忽略数 = 去重去空后的输入 ref 数 − resolved，
// 是否记日志由 API 层决定。无任何有效 ref 时不动库、返回 resolved=0——
// API 层据此保留旧自由文本兜底路径。
//
// 一个短事务保证「验存在 + 验学科 + 解析 + 补插」原子：item 不存在回
// ErrErrorItemNotFound，学科不一致回 ErrErrorItemSubjectMismatch，两者整体
// 回滚（此时事务只读过，无写入可失）。事务内只走 tx——单写者连接池
// （MaxOpenConns=1）下任何 s.db 调用都会等在自己占着的连接上。
func (s *Store) MergeErrorItemKnowledgeRefs(ctx context.Context, itemID int64, subject string, refs []string) (resolved int, attached int, err error) {
	subject = strings.TrimSpace(subject)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	// 目标错题必须存在，且库里的最终 subject 与参数一致——错题的学科决定
	// 哪些知识点能挂上去。读取在事务内，与后续写入同一个一致性视图。
	var itemSubject string
	if err := tx.QueryRowContext(ctx,
		`SELECT subject FROM error_items WHERE id = ?`, itemID).Scan(&itemSubject); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, ErrErrorItemNotFound
		}
		return 0, 0, err
	}
	if strings.TrimSpace(itemSubject) != subject {
		return 0, 0, ErrErrorItemSubjectMismatch
	}

	// 同学科精确解析，只走 tx（见函数头）。map 键即去重后的有效 ref。
	resolvedRefs, err := resolveRefTagIDs(ctx, tx, subject, refs)
	if err != nil {
		return 0, 0, err
	}
	if len(resolvedRefs) == 0 {
		// 无有效 ref：不改库（deferred rollback 丢弃只读事务）。
		return 0, 0, nil
	}

	// 稳定插入顺序：按 ref 排序展开（map 遍历序随机；行序不影响结果，但
	// 确定行为更好审计）。INSERT OR IGNORE 对已有链接幂等，RowsAffected
	// 只数真正新插入的行。
	ordered := make([]string, 0, len(resolvedRefs))
	for ref := range resolvedRefs {
		ordered = append(ordered, ref)
	}
	sort.Strings(ordered)
	q := `INSERT OR IGNORE INTO error_item_tags (error_item_id, tag_id) VALUES `
	args := make([]any, 0, len(ordered)*2)
	for i, ref := range ordered {
		if i > 0 {
			q += ", "
		}
		q += "(?, ?)"
		args = append(args, itemID, resolvedRefs[ref])
	}
	res, err := tx.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, 0, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return len(resolvedRefs), int(n), nil
}

// kcTokenSet 把文本切成关键词集合（第 3 档相关度的度量基元）：ASCII 字母
// 数字连续段整段作一个 token（小写化），非 ASCII 字母/数字连续段切二元组
// （bigram；单字段成段时取该字本身），其余字符是分隔符。中文名"一元二次
// 方程"产出 5 个二元组，题干完整包含该短语时 5 个全命中——比短泛名（如
// "方程"）得分高，即"最具体的在前"。
func kcTokenSet(s string) map[string]struct{} {
	out := make(map[string]struct{})
	var ascii strings.Builder
	var cjkRun []rune
	flushASCII := func() {
		if ascii.Len() > 0 {
			out[strings.ToLower(ascii.String())] = struct{}{}
			ascii.Reset()
		}
	}
	flushCJK := func() {
		if len(cjkRun) == 0 {
			return
		}
		if len(cjkRun) == 1 {
			out[string(cjkRun)] = struct{}{}
		} else {
			for i := 0; i+1 < len(cjkRun); i++ {
				out[string(cjkRun[i:i+2])] = struct{}{}
			}
		}
		cjkRun = cjkRun[:0]
	}
	for _, r := range s {
		switch {
		case r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'):
			flushCJK()
			ascii.WriteByte(byte(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushASCII()
			cjkRun = append(cjkRun, r)
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return out
}

// kcTokenHits 数名称 token 在文本 token 集中的命中数（token 已去重）。
// 只累加整数，与 map 遍历顺序无关。
func kcTokenHits(nameTokens, textTokens map[string]struct{}) int {
	if len(nameTokens) == 0 || len(textTokens) == 0 {
		return 0
	}
	hits := 0
	for t := range nameTokens {
		if _, ok := textTokens[t]; ok {
			hits++
		}
	}
	return hits
}
