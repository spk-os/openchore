package store

import (
	"context"
	"errors"

	"github.com/liftedkilt/openchore/internal/model"
)

// Taxonomy practice → error book bridge (TX-PRACTICE-ERRORBOOK-STORE).
// The practice loop needs "the same wrong question lands in the error
// book exactly once": retrying or re-reviewing the same practice item
// must resolve to the same error_items row instead of minting a
// duplicate. (user_id, source) is the dedup key — source carries the
// stable practice-item identifier the caller guarantees. There is
// deliberately no UNIQUE index on the pair (the paper-analysis path
// creates items without one), so the guarantee lives here, enforced
// atomically.

// EnsureTaxonomyErrorItem resolves the error item for one practice
// mistake, creating it on first sight. The INSERT ... SELECT ... WHERE
// NOT EXISTS is a single atomic statement — SQLite serializes writers
// (single-writer pool, MaxOpenConns=1), so the check-then-insert race
// two separate statements would leave cannot happen. An existing row is
// never modified: mastery, the answer fields and the FSRS schedule stay
// exactly as they are. tagID > 0 additionally links the item to its
// knowledge point (idempotent). Returns the row id and whether this
// call created it.
func (s *Store) EnsureTaxonomyErrorItem(ctx context.Context, item *model.ErrorItem, tagID int64) (itemID int64, created bool, err error) {
	if item == nil {
		return 0, false, errors.New("nil error item")
	}
	if item.UserID <= 0 {
		return 0, false, errors.New("error item has no user")
	}
	if item.Source == "" {
		return 0, false, errors.New("error item has no source")
	}
	// mistake_status is NOT NULL with a CHECK over the three states; an
	// unset field degrades to the schema's own default rather than
	// tripping the constraint.
	mistakeStatus := item.MistakeStatus
	if mistakeStatus == "" {
		mistakeStatus = model.MistakeUnknown
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	// 1) Insert only when this (user_id, source) has no row yet.
	//    RowsAffected is 0 exactly when the NOT EXISTS guard swallowed
	//    the insert — that is the "already exists" path, and it touches
	//    nothing.
	res, err := tx.ExecContext(ctx,
		`INSERT INTO error_items (user_id, completion_id, analysis_id, chore_schedule_id, chore_title, errored_date,
			subject, original_image_url, ocr_text, question_text, answer_text, wrong_answer_text,
			analysis, mistake_analysis, mistake_status, error_type, difficulty, ai_confidence,
			paper_level, grade_semester, source, mastery_level, next_review_at)
		 SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM error_items WHERE user_id = ? AND source = ?)`,
		item.UserID, nullInt64(item.CompletionID), nullInt64(item.AnalysisID), nullInt64(item.ChoreScheduleID),
		nullString(item.ChoreTitle), nullString(item.ErroredDate), item.Subject, item.OriginalImageURL, item.OCRText,
		item.QuestionText, item.AnswerText, item.WrongAnswerText, item.Analysis, item.MistakeAnalysis, mistakeStatus,
		nullString(item.ErrorType), nullString(item.Difficulty), item.AIConfidence,
		nullString(item.PaperLevel), nullString(item.GradeSemester), nullString(item.Source),
		item.MasteryLevel, nullTime(item.NextReviewAt),
		item.UserID, item.Source)
	if err != nil {
		return 0, false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		created = true
	}

	// 2) Resolve the row id inside the same transaction — the statement
	//    above either added the row or it was already there. Oldest
	//    first: if legacy rows (the paper path has no dedup) left
	//    duplicates behind, the first-created row is the canonical one.
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM error_items WHERE user_id = ? AND source = ? ORDER BY id LIMIT 1`,
		item.UserID, item.Source).Scan(&itemID); err != nil {
		return 0, false, err
	}

	// 3) Link the knowledge point (INSERT OR IGNORE: re-ensuring the
	//    same item never duplicates the tag row).
	if tagID > 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO error_item_tags (error_item_id, tag_id) VALUES (?, ?)`,
			itemID, tagID); err != nil {
			return 0, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	item.ID = itemID
	return itemID, created, nil
}
