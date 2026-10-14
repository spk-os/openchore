package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// Error items / wrong-question loop (L1). The paper analysis row is the
// async job's progress record (the parent polls it); error_items hold the
// extracted wrong questions; knowledge_tags is the adjacency-list store
// for knowledge points. See migrations/028 for the shape rationale
// (wrong-notebook schema, field for field).

var ErrAnalysisNotFound = errors.New("paper analysis not found")
var ErrErrorItemNotFound = errors.New("error item not found")

const analysisColumns = `id, completion_id, user_id, created_by, subject, status, stage, progress, total_questions, wrong_count, error, summary, created_at, updated_at`

const errorItemColumns = `id, user_id, completion_id, analysis_id, subject, original_image_url, ocr_text, question_text, answer_text, wrong_answer_text, analysis, mistake_analysis, mistake_status, error_type, difficulty, ai_confidence, paper_level, grade_semester, source, mastery_level, next_review_at, created_at, updated_at`

func scanAnalysis(row interface{ Scan(...any) error }) (*model.PaperAnalysis, error) {
	a := &model.PaperAnalysis{}
	var errMsg, summary sql.NullString
	if err := row.Scan(&a.ID, &a.CompletionID, &a.UserID, &a.CreatedBy, &a.Subject,
		&a.Status, &a.Stage, &a.Progress, &a.TotalQuestions, &a.WrongCount,
		&errMsg, &summary, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	if errMsg.Valid {
		a.Error = errMsg.String
	}
	if summary.Valid {
		a.Summary = summary.String
	}
	return a, nil
}

func scanErrorItem(row interface{ Scan(...any) error }) (*model.ErrorItem, error) {
	i := &model.ErrorItem{}
	var completionID, analysisID sql.NullInt64
	var ocrText, questionText, answerText, wrongAnswer, analysis, mistakeAnalysis sql.NullString
	var errorType, difficulty, paperLevel, gradeSemester, source sql.NullString
	var aiConfidence sql.NullFloat64
	var nextReview sql.NullTime
	if err := row.Scan(&i.ID, &i.UserID, &completionID, &analysisID, &i.Subject,
		&i.OriginalImageURL, &ocrText, &questionText, &answerText, &wrongAnswer,
		&analysis, &mistakeAnalysis, &i.MistakeStatus, &errorType, &difficulty,
		&aiConfidence, &paperLevel, &gradeSemester, &source,
		&i.MasteryLevel, &nextReview, &i.CreatedAt, &i.UpdatedAt); err != nil {
		return nil, err
	}
	if completionID.Valid {
		i.CompletionID = &completionID.Int64
	}
	if analysisID.Valid {
		i.AnalysisID = &analysisID.Int64
	}
	if ocrText.Valid {
		i.OCRText = ocrText.String
	}
	if questionText.Valid {
		i.QuestionText = questionText.String
	}
	if answerText.Valid {
		i.AnswerText = answerText.String
	}
	if wrongAnswer.Valid {
		i.WrongAnswerText = wrongAnswer.String
	}
	if analysis.Valid {
		i.Analysis = analysis.String
	}
	if mistakeAnalysis.Valid {
		i.MistakeAnalysis = mistakeAnalysis.String
	}
	if errorType.Valid {
		i.ErrorType = errorType.String
	}
	if difficulty.Valid {
		i.Difficulty = difficulty.String
	}
	if aiConfidence.Valid {
		i.AIConfidence = aiConfidence.Float64
	}
	if paperLevel.Valid {
		i.PaperLevel = paperLevel.String
	}
	if gradeSemester.Valid {
		i.GradeSemester = gradeSemester.String
	}
	if source.Valid {
		i.Source = source.String
	}
	if nextReview.Valid {
		i.NextReviewAt = &nextReview.Time
	}
	return i, nil
}

// CreatePaperAnalysis starts an async analysis record.
func (s *Store) CreatePaperAnalysis(ctx context.Context, a *model.PaperAnalysis) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO paper_analyses (completion_id, user_id, created_by, subject) VALUES (?, ?, ?, ?)`,
		a.CompletionID, a.UserID, a.CreatedBy, a.Subject)
	if err != nil {
		return err
	}
	a.ID, _ = res.LastInsertId()
	return nil
}

// GetPaperAnalysis fetches one analysis row.
func (s *Store) GetPaperAnalysis(ctx context.Context, id int64) (*model.PaperAnalysis, error) {
	a, err := scanAnalysis(s.db.QueryRowContext(ctx,
		`SELECT `+analysisColumns+` FROM paper_analyses WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return a, err
}

// ListPaperAnalysesForCompletion lists the analyses started against one
// completion (newest first) — the "analyze again" flow shows what ran.
func (s *Store) ListPaperAnalysesForCompletion(ctx context.Context, completionID int64) ([]model.PaperAnalysis, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+analysisColumns+` FROM paper_analyses WHERE completion_id = ? ORDER BY id DESC`, completionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PaperAnalysis
	for rows.Next() {
		a, err := scanAnalysis(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// UpdatePaperAnalysisProgress writes the runner's stage/progress snapshot.
func (s *Store) UpdatePaperAnalysisProgress(ctx context.Context, id int64, stage string, progress, totalQuestions, wrongCount int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE paper_analyses SET status = ?, stage = ?, progress = ?, total_questions = ?, wrong_count = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		model.AnalysisRunning, stage, progress, totalQuestions, wrongCount, id)
	return err
}

// FinishPaperAnalysis records the terminal state: done with the summary,
// or failed with the error message. Summary stays NULL on failure.
func (s *Store) FinishPaperAnalysis(ctx context.Context, id int64, status, summary, errMsg string, totalQuestions, wrongCount int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE paper_analyses SET status = ?, stage = ?, summary = ?, error = ?, total_questions = ?, wrong_count = ?, progress = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, model.StageDone, nullString(summary), nullString(errMsg), totalQuestions, wrongCount, 100, id)
	return err
}

// CreateErrorItem inserts one extracted wrong question.
func (s *Store) CreateErrorItem(ctx context.Context, i *model.ErrorItem) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO error_items (user_id, completion_id, analysis_id, subject, original_image_url, ocr_text,
			question_text, answer_text, wrong_answer_text, analysis, mistake_analysis, mistake_status,
			error_type, difficulty, ai_confidence, paper_level, grade_semester, source, mastery_level, next_review_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.UserID, nullInt64(i.CompletionID), nullInt64(i.AnalysisID), i.Subject, i.OriginalImageURL, i.OCRText,
		i.QuestionText, i.AnswerText, i.WrongAnswerText, i.Analysis, i.MistakeAnalysis, i.MistakeStatus,
		nullString(i.ErrorType), nullString(i.Difficulty), i.AIConfidence,
		nullString(i.PaperLevel), nullString(i.GradeSemester), nullString(i.Source),
		i.MasteryLevel, nullTime(i.NextReviewAt))
	if err != nil {
		return err
	}
	i.ID, _ = res.LastInsertId()
	return nil
}

// GetErrorItem fetches one wrong question, with its knowledge tags.
func (s *Store) GetErrorItem(ctx context.Context, id int64) (*model.ErrorItem, error) {
	i, err := scanErrorItem(s.db.QueryRowContext(ctx,
		`SELECT `+errorItemColumns+` FROM error_items WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tags, err := s.listTagsForItem(ctx, i.ID)
	if err != nil {
		return nil, err
	}
	i.Tags = tags
	return i, nil
}

// ListErrorItems lists a kid's wrong questions. masteryLevel < 0 means no
// filter; subject "" means all subjects. Newest first.
func (s *Store) ListErrorItems(ctx context.Context, userID int64, masteryLevel int, subject string, limit int) ([]model.ErrorItem, error) {
	q := `SELECT ` + errorItemColumns + ` FROM error_items WHERE user_id = ?`
	args := []any{userID}
	if masteryLevel >= 0 {
		q += ` AND mastery_level = ?`
		args = append(args, masteryLevel)
	}
	if subject != "" {
		q += ` AND subject = ?`
		args = append(args, subject)
	}
	q += ` ORDER BY id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ErrorItem
	for rows.Next() {
		i, err := scanErrorItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Hydrate tags in one query per page, not per row.
	if len(out) > 0 {
		tags, err := s.listTagsForItems(ctx, out)
		if err != nil {
			return nil, err
		}
		for idx := range out {
			out[idx].Tags = tags[out[idx].ID]
		}
	}
	return out, nil
}

// UpdateErrorItemMastery moves an item along the mastery machine and
// stamps the next review (L4 owns the real schedule; this is the
// bookkeeping the list views read).
func (s *Store) UpdateErrorItemMastery(ctx context.Context, id int64, mastery int, nextReview *time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE error_items SET mastery_level = ?, next_review_at = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		mastery, nullTime(nextReview), id)
	return err
}

// UpdateErrorItemFields lets a parent correct what the AI read (the
// manual queue in the spec's "unknown" degrade path).
func (s *Store) UpdateErrorItemFields(ctx context.Context, id int64, i *model.ErrorItem) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE error_items SET subject = ?, ocr_text = ?, question_text = ?, answer_text = ?,
			wrong_answer_text = ?, analysis = ?, mistake_analysis = ?, mistake_status = ?,
			error_type = ?, difficulty = ?, source = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		i.Subject, i.OCRText, i.QuestionText, i.AnswerText,
		i.WrongAnswerText, i.Analysis, i.MistakeAnalysis, i.MistakeStatus,
		nullString(i.ErrorType), nullString(i.Difficulty), nullString(i.Source), id)
	return err
}

// FindOrCreateKnowledgeTag resolves a knowledge point by name within the
// subject, creating it (AI/parent-created, is_system=0) when missing. The
// UNIQUE(subject, name, parent_id) index keeps top-level names unique.
func (s *Store) FindOrCreateKnowledgeTag(ctx context.Context, name, subject string, parentID *int64) (int64, error) {
	name = trimSpaces(name)
	if name == "" {
		return 0, errors.New("empty knowledge tag name")
	}
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM knowledge_tags WHERE subject = ? AND name = ? AND parent_id IS ?`, subject, name, parentID).
		Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO knowledge_tags (name, subject, parent_id) VALUES (?, ?, ?)`,
		name, subject, nullInt64(parentID))
	if err != nil {
		// Lost a create race (single-writer DB makes this unlikely, but a
		// UNIQUE violation from a same-transaction insert is possible).
		if isUniqueConstraintErr(err) {
			if err2 := s.db.QueryRowContext(ctx,
				`SELECT id FROM knowledge_tags WHERE subject = ? AND name = ? AND parent_id IS ?`, subject, name, parentID).
				Scan(&id); err2 == nil {
				return id, nil
			}
		}
		return 0, err
	}
	return res.LastInsertId()
}

// AttachErrorItemTag links an error item to a knowledge point.
func (s *Store) AttachErrorItemTag(ctx context.Context, errorItemID, tagID int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO error_item_tags (error_item_id, tag_id) VALUES (?, ?)`, errorItemID, tagID)
	return err
}

// listTagsForItem loads one item's tags.
func (s *Store) listTagsForItem(ctx context.Context, errorItemID int64) ([]model.KnowledgeTag, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT kt.id, kt.name, kt.subject, kt.parent_id, kt.code, kt.sort_order, kt.is_system
		 FROM error_item_tags eit JOIN knowledge_tags kt ON kt.id = eit.tag_id
		 WHERE eit.error_item_id = ? ORDER BY kt.sort_order, kt.name`, errorItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTags(rows)
}

// listTagsForItems loads tags for a whole page of items in one query.
func (s *Store) listTagsForItems(ctx context.Context, items []model.ErrorItem) (map[int64][]model.KnowledgeTag, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT eit.error_item_id, kt.id, kt.name, kt.subject, kt.parent_id, kt.code, kt.sort_order, kt.is_system
		 FROM error_item_tags eit JOIN knowledge_tags kt ON kt.id = eit.tag_id
		 ORDER BY kt.sort_order, kt.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]model.KnowledgeTag)
	for rows.Next() {
		var itemID int64
		var parentID sql.NullInt64
		var code sql.NullString
		var isSystem int
		var t model.KnowledgeTag
		if err := rows.Scan(&itemID, &t.ID, &t.Name, &t.Subject, &parentID, &code, &t.SortOrder, &isSystem); err != nil {
			return nil, err
		}
		if parentID.Valid {
			t.ParentID = &parentID.Int64
		}
		if code.Valid {
			t.Code = code.String
		}
		t.IsSystem = isSystem == 1
		out[itemID] = append(out[itemID], t)
	}
	return out, rows.Err()
}

func scanTags(rows interface {
	Next() bool
	Scan(...any) error
}) ([]model.KnowledgeTag, error) {
	var out []model.KnowledgeTag
	for rows.Next() {
		var parentID sql.NullInt64
		var code sql.NullString
		var isSystem int
		var t model.KnowledgeTag
		if err := rows.Scan(&t.ID, &t.Name, &t.Subject, &parentID, &code, &t.SortOrder, &isSystem); err != nil {
			return nil, err
		}
		if parentID.Valid {
			t.ParentID = &parentID.Int64
		}
		if code.Valid {
			t.Code = code.String
		}
		t.IsSystem = isSystem == 1
		out = append(out, t)
	}
	return out, nil
}

// WeakTagStat is one knowledge point's error tally — the diagnosis
// report's "知识点薄弱榜" row.
type WeakTagStat struct {
	TagID      int64  `json:"tag_id"`
	Name       string `json:"name"`
	Subject    string `json:"subject"`
	WrongCount int    `json:"wrong_count"`
}

// WeakKnowledgeTags aggregates error counts per knowledge tag for one
// kid (the diagnosis report's weak-point ranking).
func (s *Store) WeakKnowledgeTags(ctx context.Context, userID int64, limit int) ([]WeakTagStat, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT kt.id, kt.name, kt.subject, COUNT(*) AS wrong_count
		 FROM error_item_tags eit
		 JOIN knowledge_tags kt ON kt.id = eit.tag_id
		 JOIN error_items ei ON ei.id = eit.error_item_id
		 WHERE ei.user_id = ? AND ei.mastery_level < ?
		 GROUP BY kt.id, kt.name, kt.subject
		 ORDER BY wrong_count DESC, kt.name
		 LIMIT ?`, userID, model.MasteryMastered, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WeakTagStat
	for rows.Next() {
		var w WeakTagStat
		if err := rows.Scan(&w.TagID, &w.Name, &w.Subject, &w.WrongCount); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ErrorTypeStat is one error type's count — the diagnosis report's
// error-type distribution.
type ErrorTypeStat struct {
	ErrorType string `json:"error_type"`
	Count     int    `json:"count"`
}

// ErrorTypeDistribution aggregates error_type counts for one kid.
func (s *Store) ErrorTypeDistribution(ctx context.Context, userID int64) ([]ErrorTypeStat, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT error_type, COUNT(*) FROM error_items
		 WHERE user_id = ? AND error_type IS NOT NULL
		 GROUP BY error_type ORDER BY COUNT(*) DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ErrorTypeStat
	for rows.Next() {
		var e ErrorTypeStat
		if err := rows.Scan(&e.ErrorType, &e.Count); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// nullInt64 maps nil to SQL NULL (nullable FK columns).
func nullInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// nullTime maps nil to SQL NULL (nullable timestamp columns).
func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return dbTime(*t)
}

// dbTime renders a timestamp for SQLite storage. The modernc driver's
// own time.Time binding stores RFC3339Nano (9 fractional digits), which
// SQLite's date()/strftime() refuse to parse (>3 digits) — every
// date(reviewed_at) aggregate would come back NULL. RFC3339 (seconds,
// explicit zone) parses cleanly, scans back into time.Time with the
// zone intact, and sorts lexicographically in time order — but only if
// every binding on the column uses it, so all timestamp binds go
// through here (see the time-binding pitfall, CLAUDE.md §坑速查).
func dbTime(t time.Time) string {
	return t.Format(time.RFC3339)
}

// trimSpaces is strings.TrimSpace without importing strings here.
func trimSpaces(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
