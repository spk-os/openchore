package store

// 错题字段更新 + 知识标签替换的原子组合（TX-ERROR-TAGS-HTTP-CODER）。
// 家长端 PUT /admin/error-items/{id} 可同时改 subject 和 knowledge_tag_ids；
// ReplaceErrorItemTags 以库里现存 subject 校验标签，无法满足「同一次请求先改
// subject 再绑新学科标签」，因此这里把字段 UPDATE 与标签 DELETE+INSERT 放进
// 同一个短事务，校验以本次写入的 i.Subject 为准。

import (
	"context"
	"database/sql"
	"errors"

	"github.com/liftedkilt/openchore/internal/model"
)

// ErrErrorItemMissingOnUpdate 是 UpdateErrorItemWithTags 的缺失哨兵：事务开始时
// UPDATE 没有命中任何行（item 不存在）。与 ReplaceErrorItemTags 直接透传
// sql.ErrNoRows 不同，本方法先写字段后验标签，需要独立哨兵表达「整单回滚、
// 什么都没改」。调用方用 errors.Is 识别后映射 404，不做字符串匹配。
var ErrErrorItemMissingOnUpdate = errors.New("error item not found")

// UpdateErrorItemOnSubjectChange is the no-knowledge_tag_ids branch of a
// subject-changing PUT: the parent swapped the subject but left the tag
// list absent, so the field UPDATE and the cross-subject guard must be
// atomic. The guard exists because nothing else detaches the old links —
// error_item_tags has no trigger and its CASCADE only fires on row
// deletes, so a bare UPDATE would leave the old subject's tags pointing
// at an item whose subject now forbids them.
//
//   - RowsAffected==0 on the field UPDATE → ErrErrorItemMissingOnUpdate
//     (the item vanished between the handler's read and this tx; nothing
//     was changed).
//   - Any currently linked tag whose subject differs from the new
//     i.Subject → ErrKnowledgeTagMismatch and the whole tx rolls back:
//     the caller must resubmit knowledge_tag_ids in the same body (an
//     empty array clears the old subject's tags). Tags already sharing
//     the new subject survive untouched.
//
// Same single-writer rules as UpdateErrorItemWithTags: only tx statements
// in here, everything short.
func (s *Store) UpdateErrorItemOnSubjectChange(ctx context.Context, id int64, item *model.ErrorItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`UPDATE error_items SET subject = ?, ocr_text = ?, question_text = ?, answer_text = ?,
			wrong_answer_text = ?, analysis = ?, mistake_analysis = ?, mistake_status = ?,
			error_type = ?, difficulty = ?, source = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		item.Subject, item.OCRText, item.QuestionText, item.AnswerText,
		item.WrongAnswerText, item.Analysis, item.MistakeAnalysis, item.MistakeStatus,
		nullString(item.ErrorType), nullString(item.Difficulty), nullString(item.Source), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrErrorItemMissingOnUpdate
	}

	// 跨学科守卫：任一现存标签的 subject 与新值不同即整单回滚。空串
	// subject 的历史标签不等于任何学科键，同样按不兼容处理——宁可让家长
	// 显式重交 knowledge_tag_ids，也不静默留下与 item.Subject 冲突的链接。
	var incompatible int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM error_item_tags eit
		 JOIN knowledge_tags kt ON kt.id = eit.tag_id
		 WHERE eit.error_item_id = ? AND kt.subject <> ?`,
		id, item.Subject).Scan(&incompatible); err != nil {
		return err
	}
	if incompatible > 0 {
		return ErrKnowledgeTagMismatch
	}
	return tx.Commit()
}

// UpdateErrorItemWithTags 在一个短事务内原子完成两件事：
//  1. UpdateErrorItemFields 的字段 UPDATE（含 subject / mistake_status 等）；
//  2. tagIDs 的标签替换 —— nil 语义由调用方决定（API 层用 *[]int64 区分
//     「缺省不改」与「空数组清空」，到这一层时 nil 与 [] 都清空）。
//
// 校验以本次写入的 i.Subject 为准，因此「请求同时改 subject 和 tags」是合法
// 的：先 UPDATE subject，再按新 subject 校验标签。任何一个标签不存在、非正或
// 属于其他学科，整个事务回滚 —— 字段和旧标签都保持原样，失败以
// ErrKnowledgeTagMismatch 表达。item 不存在回滚为 ErrErrorItemMissingOnUpdate。
//
// 与 ReplaceErrorItemTags 相同的约束：连接池是单写者（MaxOpenConns=1），事务内
// 只能走 tx，任何 s.db / GetKnowledgeTag / listTagsForItem 调用都会等在自己
// 占着的连接上。所有语句保持短平快，不碰 knowledge_tags / error_items 之外的
// 长查询。
func (s *Store) UpdateErrorItemWithTags(ctx context.Context, id int64, item *model.ErrorItem, tagIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 字段更新与 UpdateErrorItemFields 同一条 SQL；RowsAffected==0 说明 item
	// 已经不存在（入口的 GetErrorItem 与事务之间它可能被删），整体回滚。
	res, err := tx.ExecContext(ctx,
		`UPDATE error_items SET subject = ?, ocr_text = ?, question_text = ?, answer_text = ?,
			wrong_answer_text = ?, analysis = ?, mistake_analysis = ?, mistake_status = ?,
			error_type = ?, difficulty = ?, source = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		item.Subject, item.OCRText, item.QuestionText, item.AnswerText,
		item.WrongAnswerText, item.Analysis, item.MistakeAnalysis, item.MistakeStatus,
		nullString(item.ErrorType), nullString(item.Difficulty), nullString(item.Source), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrErrorItemMissingOnUpdate
	}

	// 去重保序；id <= 0 不可能命中任何行，统一走 mismatch 哨兵拒绝。
	seen := make(map[int64]struct{}, len(tagIDs))
	ids := make([]int64, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		if tagID <= 0 {
			return ErrKnowledgeTagMismatch
		}
		if _, dup := seen[tagID]; dup {
			continue
		}
		seen[tagID] = struct{}{}
		ids = append(ids, tagID)
	}

	// 同学科校验：以本次写入的 item.Subject 为准，且必须在同一事务里读，避免
	// 校验后、写入前被并发改写（单写者下窗口极小，但事务读是一致性的底线）。
	for _, tagID := range ids {
		var tagSubject string
		err := tx.QueryRowContext(ctx,
			`SELECT subject FROM knowledge_tags WHERE id = ?`, tagID).Scan(&tagSubject)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrKnowledgeTagMismatch
		}
		if err != nil {
			return err
		}
		if tagSubject != item.Subject {
			return ErrKnowledgeTagMismatch
		}
	}

	// 替换：先删后插。去重已保证 (error_item_id, tag_id) 复合主键不会触发。
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM error_item_tags WHERE error_item_id = ?`, id); err != nil {
		return err
	}
	if len(ids) > 0 {
		q := `INSERT INTO error_item_tags (error_item_id, tag_id) VALUES `
		args := make([]any, 0, len(ids)*2)
		for i, tagID := range ids {
			if i > 0 {
				q += ", "
			}
			q += "(?, ?)"
			args = append(args, id, tagID)
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
