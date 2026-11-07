package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/taxonomy"
)

// Taxonomy persistence (TX-BE-CODER). This file owns: the ten-subject
// word list seed, the idempotent importer of the embedded cn-math-
// taxonomy pack (taxonomy.Load() → upsert by external_ref, batched
// short transactions), the browse/search DAOs for knowledge_tags /
// knowledge_questions / prerequisites / standards, and the
// external_ref ↔ DB id mapping helpers the API layer uses to bridge
// the engine's string ids onto the 042 integer FKs.
//
// Idempotency contract (specs/taxonomy tasks T2): external_ref is the
// only key; rows with is_system=0 are never touched; every batch is
// its own transaction (SQLite is single-writer — MaxOpenConns=1 — so
// keeping each batch short is what keeps reads unblocked).

// importBatchSize is the per-transaction row cap (spec: ≤500 行/批).
const importBatchSize = 500

// taxonomySource labels imported rows' provenance.
const taxonomySource = "cn-math-taxonomy"

// SeedSubjects writes the ten-subject word list (subjects table).
// Idempotent: keyed on subjects.key, upsert updates label/sort/enabled
// but never clobbers a different sb_code someone verified by hand
// (INSERT ... ON CONFLICT keeps the existing sb_code).
func (s *Store) SeedSubjects(ctx context.Context, subjects []model.Subject) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, sub := range subjects {
		var sbCode any
		if sub.SBCode != "" {
			sbCode = sub.SBCode
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO subjects (key, label_zh, sb_code, sort_order, enabled)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(key) DO UPDATE SET
			   label_zh = excluded.label_zh,
			   sort_order = excluded.sort_order,
			   enabled = excluded.enabled`,
			sub.Key, sub.LabelZh, sbCode, sub.SortOrder, boolToInt(sub.Enabled)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListSubjects returns the word list, sort_order first. Always a
// non-nil slice (a missing seed reads as []).
func (s *Store) ListSubjects(ctx context.Context) ([]model.Subject, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, label_zh, sb_code, sort_order, enabled FROM subjects ORDER BY sort_order, key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Subject, 0, 10)
	for rows.Next() {
		var sub model.Subject
		var sbCode sql.NullString
		var enabled int
		if err := rows.Scan(&sub.Key, &sub.LabelZh, &sbCode, &sub.SortOrder, &enabled); err != nil {
			return nil, err
		}
		if sbCode.Valid {
			sub.SBCode = sbCode.String
		}
		sub.Enabled = enabled == 1
		out = append(out, sub)
	}
	return out, rows.Err()
}

// semesterToInt maps the pack's "上"/"下"/"全" onto the INTEGER column
// (1=上 2=下; "全" spans both, stored NULL = 不限).
func semesterToInt(s string) any {
	switch s {
	case "上":
		return 1
	case "下":
		return 2
	default: // "全" or empty
		return nil
	}
}

// importTx wraps one short transaction of the import. The batcher
// (txBatcher below) is the only constructor: it guarantees the pool's
// single connection is released when the import returns.
type importTx struct {
	tx *sql.Tx
}

// upsertTag inserts or updates one imported knowledge point by
// external_ref. is_system=0 rows are never touched (the guard is in
// the WHERE clause, not just convention).
func (t *importTx) upsertTag(ctx context.Context, cat *taxonomy.Catalog, tp *taxonomy.Topic) (int64, error) {
	subject := cat.SubjectOf(tp)
	var parent any // imported pack is flat; parent_id stays NULL
	var grade any
	if tp.Grade > 0 {
		grade = tp.Grade
	}
	masteryJSON, _ := json.Marshal(tp.MasteryCriteria)
	diagJSON, _ := json.Marshal(tp.DiagnosticQuestion)
	standardsJSON, _ := json.Marshal(tp.Standards)
	aliasesJSON := "[]"
	if len(tp.Aliases) > 0 {
		b, _ := json.Marshal(tp.Aliases)
		aliasesJSON = string(b)
	}
	mappingsJSON := "{}"
	if len(tp.Mappings) > 0 {
		b, _ := json.Marshal(tp.Mappings)
		mappingsJSON = string(b)
	}
	if _, err := t.tx.ExecContext(ctx,
		`INSERT INTO knowledge_tags
		   (name, subject, parent_id, sort_order, is_system, grade, semester, domain, chapter,
		    topic_type, description, source, external_ref, standards_json, mappings_json, aliases_json,
		    mastery_criteria_json, diagnostic_question_json)
		 VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(external_ref) WHERE external_ref IS NOT NULL DO UPDATE SET
		   name = excluded.name,
		   subject = excluded.subject,
		   sort_order = excluded.sort_order,
		   is_system = 1,
		   grade = excluded.grade,
		   semester = excluded.semester,
		   domain = excluded.domain,
		   chapter = excluded.chapter,
		   topic_type = excluded.topic_type,
		   description = excluded.description,
		   source = excluded.source,
		   standards_json = excluded.standards_json,
		   mappings_json = excluded.mappings_json,
		   aliases_json = excluded.aliases_json,
		   mastery_criteria_json = excluded.mastery_criteria_json,
		   diagnostic_question_json = excluded.diagnostic_question_json
		 WHERE knowledge_tags.is_system = 1`,
		tp.Name, subject, parent, tp.Grade, grade, semesterToInt(tp.Semester), tp.Domain, tp.Chapter,
		tp.Type, tp.Description, taxonomySource, tp.ID, string(standardsJSON), mappingsJSON, aliasesJSON,
		string(masteryJSON), string(diagJSON)); err != nil {
		return 0, err
	}
	// Resolve the row id by the idempotency key, never by last_insert_rowid:
	// on the DO UPDATE branch that value is stale (an UPDATE does not reset
	// it), which corrupted the FK map on the second (idempotent) import.
	var id int64
	qerr := t.tx.QueryRowContext(ctx,
		`SELECT id FROM knowledge_tags WHERE external_ref = ?`, tp.ID).Scan(&id)
	return id, qerr
}

// upsertQuestion inserts/updates one bank question by external_ref.
func (t *importTx) upsertQuestion(ctx context.Context, q *taxonomy.Question, topicRowID int64, sortOrder int) error {
	choicesJSON, _ := json.Marshal(q.Choices)
	acceptJSON, _ := json.Marshal(q.Accept)
	answerJSON, _ := json.Marshal(q.Answer)
	sortOrderField := sortOrder
	if q.FixedFromSource != "" {
		// keep a traceable source marker without inventing a column
		_ = q.FixedFromSource
	}
	_, err := t.tx.ExecContext(ctx,
		`INSERT INTO knowledge_questions
		   (external_ref, topic_id, difficulty, type, prompt, choices_json, answer_json, accept_json,
		    explanation, verify, unordered, figure, hide_choice_text, source, sort_order)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(external_ref) WHERE external_ref IS NOT NULL DO UPDATE SET
		   topic_id = excluded.topic_id,
		   difficulty = excluded.difficulty,
		   type = excluded.type,
		   prompt = excluded.prompt,
		   choices_json = excluded.choices_json,
		   answer_json = excluded.answer_json,
		   accept_json = excluded.accept_json,
		   explanation = excluded.explanation,
		   verify = excluded.verify,
		   unordered = excluded.unordered,
		   figure = excluded.figure,
		   hide_choice_text = excluded.hide_choice_text,
		   sort_order = excluded.sort_order,
		   updated_at = CURRENT_TIMESTAMP`,
		q.ID, topicRowID, q.Difficulty, q.Type, q.Prompt, string(choicesJSON),
		string(answerJSON), string(acceptJSON), q.Explanation, q.Verify,
		boolToInt(q.Unordered), q.Figure, boolToInt(q.HideChoiceText),
		taxonomySource, sortOrderField)
	return err
}

// upsertStandard inserts/updates one curriculum-standard clause by
// external_ref. Returns the row id.
func (t *importTx) upsertStandard(ctx context.Context, st *taxonomy.StandardItem, subject string) (int64, error) {
	extra, _ := json.Marshal(map[string]any{
		"verbatim":     st.Verbatim,
		"text_status":  st.TextStatus,
		"note":         st.Note,
		"wording_note": st.WordingNote,
		"theme":        st.Theme,
		"elective":     st.Elective,
		"module":       st.Module,
	})
	if _, err := t.tx.ExecContext(ctx,
		`INSERT INTO curriculum_standards (external_ref, subject, domain, section, clause, text, page_or_ref, extra_json, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(external_ref) WHERE external_ref IS NOT NULL DO UPDATE SET
		   subject = excluded.subject,
		   domain = excluded.domain,
		   section = excluded.section,
		   clause = excluded.clause,
		   text = excluded.text,
		   page_or_ref = excluded.page_or_ref,
		   extra_json = excluded.extra_json,
		   updated_at = CURRENT_TIMESTAMP`,
		st.ID, subject, st.Domain, st.Section, st.Clause, st.Text, st.PageOrRef, string(extra), taxonomySource); err != nil {
		return 0, err
	}
	// Same rule as upsertTag: the row id comes from the key, never from
	// last_insert_rowid (stale on the DO UPDATE branch).
	var id int64
	qerr := t.tx.QueryRowContext(ctx,
		`SELECT id FROM curriculum_standards WHERE external_ref = ?`, st.ID).Scan(&id)
	return id, qerr
}

// upsertTagStandard links a knowledge point to a standard clause.
func (t *importTx) upsertTagStandard(ctx context.Context, tagRowID, stdRowID int64, confidence, note string) error {
	if confidence == "" {
		confidence = "exact"
	}
	if confidence != "exact" && confidence != "inferred" {
		confidence = "inferred"
	}
	_, err := t.tx.ExecContext(ctx,
		`INSERT INTO knowledge_tag_standards (tag_id, standard_id, confidence, note)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(tag_id, standard_id) DO UPDATE SET
		   confidence = excluded.confidence,
		   note = excluded.note`,
		tagRowID, stdRowID, confidence, note)
	return err
}

// upsertPrerequisite writes one prerequisite edge.
func (t *importTx) upsertPrerequisite(ctx context.Context, topicRowID, prereqRowID int64, e *taxonomy.Edge) error {
	reviewStatus := e.ReviewStatus
	if reviewStatus == "" {
		reviewStatus = "machine"
	}
	_, err := t.tx.ExecContext(ctx,
		`INSERT INTO knowledge_tag_prerequisites (topic_id, prerequisite_id, strength, reason, review_status)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(topic_id, prerequisite_id) DO UPDATE SET
		   strength = excluded.strength,
		   reason = excluded.reason`,
		topicRowID, prereqRowID, e.Strength, e.Reason, reviewStatus)
	return err
}

// ImportTaxonomy imports the full embedded pack. Subjects are seeded
// first (the API layer passes the ten-word list), then every topic /
// edge / question / standard / link upserts by external_ref in batched
// short transactions. Re-running is a no-op upsert; is_system=0 rows
// are never modified (they don't carry external_ref and the update
// clause guards is_system = 1).
func (s *Store) ImportTaxonomy(ctx context.Context, cat *taxonomy.Catalog, subjects []model.Subject) error {
	if cat == nil {
		return errors.New("nil taxonomy catalog")
	}
	if err := s.SeedSubjects(ctx, subjects); err != nil {
		return fmt.Errorf("seeding subjects: %w", err)
	}

	// batcher holds the one open transaction of the import. commit()
	// closes the current batch and immediately opens the next, so a
	// pass never writes on a committed transaction; the trailing empty
	// batch is rolled back at the end — the single-writer pool's only
	// connection must not stay pinned by a dangling transaction once
	// the import returns.
	batcher := &txBatcher{}
	defer batcher.close()
	if err := batcher.begin(ctx, s); err != nil {
		return err
	}
	commit := func() error { return batcher.commitAndReopen(ctx, s) }

	// Pass 1: knowledge tags. idByExt maps external_ref → DB row id for
	// the FK targets every later pass needs.
	idByExt := make(map[string]int64, len(cat.Topics))
	inBatch := 0
	for _, tp := range cat.Topics {
		id, err := batcher.upsertTag(ctx, cat, tp)
		if err != nil {
			return fmt.Errorf("importing topic %s: %w", tp.ID, err)
		}
		idByExt[tp.ID] = id
		inBatch++
		if inBatch >= importBatchSize {
			if err := commit(); err != nil {
				return err
			}
			inBatch = 0
		}
	}
	if err := batcher.commitIfDirty(ctx, s, &inBatch); err != nil {
		return err
	}

	// Pass 2: prerequisite edges.
	for _, e := range cat.Edges {
		topicRow, ok := idByExt[e.TopicID]
		if !ok {
			continue
		}
		prereqRow, ok := idByExt[e.PrerequisiteID]
		if !ok {
			continue
		}
		if err := batcher.upsertPrerequisite(ctx, topicRow, prereqRow, e); err != nil {
			return fmt.Errorf("importing edge %s<%s: %w", e.TopicID, e.PrerequisiteID, err)
		}
		inBatch++
		if inBatch >= importBatchSize {
			if err := commit(); err != nil {
				return err
			}
			inBatch = 0
		}
	}
	if err := batcher.commitIfDirty(ctx, s, &inBatch); err != nil {
		return err
	}

	// Pass 3: bank questions (only the real file questions — the
	// engine's synthesized legacy questions never touch the DB).
	for _, q := range cat.Questions {
		topicRow, ok := idByExt[q.TopicID]
		if !ok {
			continue
		}
		if err := batcher.upsertQuestion(ctx, q, topicRow, 0); err != nil {
			return fmt.Errorf("importing question %s: %w", q.ID, err)
		}
		inBatch++
		if inBatch >= importBatchSize {
			if err := commit(); err != nil {
				return err
			}
			inBatch = 0
		}
	}
	if err := batcher.commitIfDirty(ctx, s, &inBatch); err != nil {
		return err
	}

	// Pass 4: curriculum standards, then per-topic links.
	stdIDByExt := make(map[string]int64, len(cat.Standards))
	for si := range cat.Standards {
		st := &cat.Standards[si]
		id, err := batcher.upsertStandard(ctx, st, standardSubject(st.ID))
		if err != nil {
			return fmt.Errorf("importing standard %s: %w", st.ID, err)
		}
		stdIDByExt[st.ID] = id
		inBatch++
		if inBatch >= importBatchSize {
			if err := commit(); err != nil {
				return err
			}
			inBatch = 0
		}
	}
	if err := batcher.commitIfDirty(ctx, s, &inBatch); err != nil {
		return err
	}

	for _, tp := range cat.Topics {
		tagRow, ok := idByExt[tp.ID]
		if !ok {
			continue
		}
		for _, ref := range tp.Standards {
			stdRow, ok := stdIDByExt[ref.ID]
			if !ok {
				continue
			}
			if err := batcher.upsertTagStandard(ctx, tagRow, stdRow, ref.Confidence, ref.Note); err != nil {
				return fmt.Errorf("importing tag standard %s→%s: %w", tp.ID, ref.ID, err)
			}
			inBatch++
			if inBatch >= importBatchSize {
				if err := commit(); err != nil {
					return err
				}
				inBatch = 0
			}
		}
	}
	// Last batch: commit whatever is left, then release the fresh
	// trailing transaction (the rollback of an empty batch is a no-op
	// that frees the pool's one connection).
	if err := batcher.commitIfDirty(ctx, s, &inBatch); err != nil {
		return err
	}
	return batcher.rollback()
}

// txBatcher owns the import's single open transaction across the
// batched passes (commit-and-reopen keeps every batch ≤500 rows while
// never leaving the pool's one connection pinned after the import).
// The upsert helpers are promoted onto it so every write rides the
// one managed transaction.
type txBatcher struct {
	tx  *sql.Tx
	err error // first failure; further calls are no-ops
}

func (b *txBatcher) begin(ctx context.Context, s *Store) error {
	if b.err != nil {
		return b.err
	}
	b.tx, b.err = s.db.BeginTx(ctx, nil)
	return b.err
}

// commitAndReopen commits the current batch and opens the next (the
// caller keeps writing without seeing a closed transaction).
func (b *txBatcher) commitAndReopen(ctx context.Context, s *Store) error {
	if b.err != nil {
		return b.err
	}
	if err := b.tx.Commit(); err != nil {
		b.err = err
		return err
	}
	return b.begin(ctx, s)
}

// commitIfDirty commits the current batch when it holds rows and
// reopens a fresh one, leaving the pass boundary clean.
func (b *txBatcher) commitIfDirty(ctx context.Context, s *Store, inBatch *int) error {
	if b.err != nil {
		return b.err
	}
	if *inBatch == 0 {
		return nil
	}
	if err := b.commitAndReopen(ctx, s); err != nil {
		return err
	}
	*inBatch = 0
	return nil
}

// rollback discards the trailing empty transaction (freeing the
// connection) — a failure here is reported but never masks an earlier
// one.
func (b *txBatcher) rollback() error {
	if b.tx == nil {
		return nil
	}
	err := b.tx.Rollback()
	if err != nil && err != sql.ErrTxDone {
		return err
	}
	b.tx = nil
	return nil
}

// close is the defer safety net: a panic or early return still
// releases the connection.
func (b *txBatcher) close() {
	if b.tx != nil {
		_ = b.tx.Rollback()
		b.tx = nil
	}
}

// asImportTx reuses the importTx upsert helpers on the batcher's
// managed transaction.
func (b *txBatcher) asImportTx() *importTx { return &importTx{tx: b.tx} }

// upsertTag inserts or updates one imported knowledge point by
// external_ref. is_system=0 rows are never touched (the guard is in
// the WHERE clause, not just convention).
func (b *txBatcher) upsertTag(ctx context.Context, cat *taxonomy.Catalog, tp *taxonomy.Topic) (int64, error) {
	return b.asImportTx().upsertTag(ctx, cat, tp)
}

// upsertQuestion inserts/updates one bank question by external_ref.
func (b *txBatcher) upsertQuestion(ctx context.Context, q *taxonomy.Question, topicRowID int64, sortOrder int) error {
	return b.asImportTx().upsertQuestion(ctx, q, topicRowID, sortOrder)
}

// upsertStandard inserts/updates one curriculum-standard clause by
// external_ref.
func (b *txBatcher) upsertStandard(ctx context.Context, st *taxonomy.StandardItem, subject string) (int64, error) {
	return b.asImportTx().upsertStandard(ctx, st, subject)
}

// upsertTagStandard links a knowledge point to a standard clause.
func (b *txBatcher) upsertTagStandard(ctx context.Context, tagRowID, stdRowID int64, confidence, note string) error {
	return b.asImportTx().upsertTagStandard(ctx, tagRowID, stdRowID, confidence, note)
}

// upsertPrerequisite writes one prerequisite edge.
func (b *txBatcher) upsertPrerequisite(ctx context.Context, topicRowID, prereqRowID int64, e *taxonomy.Edge) error {
	return b.asImportTx().upsertPrerequisite(ctx, topicRowID, prereqRowID, e)
}

// standardSubject infers a standard's subject from its id prefix
// (cms- math, ces- english, cps- physics, ccs- chinese, css- science,
// cds- morality, chs- history, cgs- geography, cbs- biology,
// cks- chemistry).
func standardSubject(id string) string {
	switch {
	case strings.HasPrefix(id, "cms-"):
		return taxonomy.SubjectMath
	case strings.HasPrefix(id, "ces-"):
		return taxonomy.SubjectEnglish
	case strings.HasPrefix(id, "cps-"):
		return taxonomy.SubjectPhysics
	case strings.HasPrefix(id, "ccs-"):
		return taxonomy.SubjectChinese
	case strings.HasPrefix(id, "css-"):
		return taxonomy.SubjectScience
	case strings.HasPrefix(id, "cds-"):
		return taxonomy.SubjectMorality
	case strings.HasPrefix(id, "chs-"):
		return taxonomy.SubjectHistory
	case strings.HasPrefix(id, "cgs-"):
		return taxonomy.SubjectGeography
	case strings.HasPrefix(id, "cbs-"):
		return taxonomy.SubjectBiology
	case strings.HasPrefix(id, "cks-"):
		return taxonomy.SubjectChemistry
	}
	return ""
}

// --- external_ref ↔ DB id bridges ---

// TagIDByExternalRef resolves one external_ref to its DB row id.
func (s *Store) TagIDByExternalRef(ctx context.Context, externalRef string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM knowledge_tags WHERE external_ref = ?`, externalRef).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// MapTagIDsByExternalRef resolves a whole external_ref list in one
// query round (the practice/diagnostic write paths batch their FK
// lookups this way). Unknown refs map to 0 and are the caller's call.
func (s *Store) MapTagIDsByExternalRef(ctx context.Context, refs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(refs))
	args := make([]any, len(refs))
	for i, r := range refs {
		placeholders[i] = "?"
		args[i] = r
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT external_ref, id FROM knowledge_tags WHERE external_ref IN (`+
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

// QuestionIDByExternalRef resolves a question external_ref to its DB
// row id (diagnostic_attempts.question_id is an integer FK).
func (s *Store) QuestionIDByExternalRef(ctx context.Context, externalRef string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM knowledge_questions WHERE external_ref = ?`, externalRef).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// --- browse / search ---

// TopicBrowseRow is one browse/search hit (GET /api/taxonomy/topics).
type TopicBrowseRow struct {
	model.KnowledgeTag
	// QuestionCount is the number of bank questions on this tag.
	QuestionCount int `json:"question_count"`
}

// BrowseTopics filters knowledge_tags by subject/grade/domain and a
// free-text name query, keyset-paginated on (sort_order, id) — the
// stable order the import wrote. limit <= 0 means 50, capped at 200.
// Always returns a non-nil slice.
func (s *Store) BrowseTopics(ctx context.Context, subject string, grade *int, q, domain string, cursor string, limit int) ([]TopicBrowseRow, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	conds := []string{"is_system = 1"}
	args := []any{}
	if subject != "" {
		conds = append(conds, "subject = ?")
		args = append(args, subject)
	}
	if grade != nil {
		conds = append(conds, "grade = ?")
		args = append(args, *grade)
	}
	if domain != "" {
		conds = append(conds, "domain = ?")
		args = append(args, domain)
	}
	if kw := strings.TrimSpace(q); kw != "" {
		conds = append(conds, "(name LIKE ? OR external_ref LIKE ?)")
		args = append(args, "%"+kw+"%", "%"+kw+"%")
	}
	// Keyset pagination: cursor is "<sort_order>:<id>".
	if cursor != "" {
		parts := strings.SplitN(cursor, ":", 2)
		if len(parts) == 2 {
			var so, id int64
			if n, err := fmt.Sscanf(parts[0], "%d", &so); n == 1 && err == nil {
				if n2, err2 := fmt.Sscanf(parts[1], "%d", &id); n2 == 1 && err2 == nil {
					conds = append(conds, "(sort_order, id) > (?, ?)")
					args = append(args, so, id)
				}
			}
		}
	}
	where := strings.Join(conds, " AND ")
	rows, err := s.db.QueryContext(ctx,
		`SELECT kt.id, kt.name, kt.subject, kt.parent_id, kt.code, kt.sort_order, kt.is_system,
		        kt.grade, kt.semester, kt.domain, kt.chapter, kt.topic_type, kt.description,
		        kt.source, kt.external_ref, kt.mastery_criteria_json, kt.diagnostic_question_json,
		        (SELECT COUNT(*) FROM knowledge_questions kq WHERE kq.topic_id = kt.id)
		 FROM knowledge_tags kt WHERE `+where+`
		 ORDER BY kt.sort_order, kt.id LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := make([]TopicBrowseRow, 0, limit)
	nextCursor := ""
	for rows.Next() {
		var r TopicBrowseRow
		var parentID sql.NullInt64
		var code sql.NullString
		var isSystem int
		var grade sql.NullInt64
		var semester sql.NullInt64
		var masteryJSON, diagJSON sql.NullString
		if err := rows.Scan(&r.ID, &r.Name, &r.Subject, &parentID, &code, &r.SortOrder, &isSystem,
			&grade, &semester, &r.Domain, &r.Chapter, &r.TopicType, &r.Description,
			&r.Source, &r.ExternalRef, &masteryJSON, &diagJSON, &r.QuestionCount); err != nil {
			return nil, "", err
		}
		if parentID.Valid {
			v := parentID.Int64
			r.ParentID = &v
		}
		if code.Valid {
			r.Code = code.String
		}
		if grade.Valid {
			v := int(grade.Int64)
			r.Grade = &v
		}
		if semester.Valid {
			v := int(semester.Int64)
			r.Semester = &v
		}
		if masteryJSON.Valid {
			r.MasteryJSON = masteryJSON.String
		}
		if diagJSON.Valid {
			r.DiagnosticJSON = diagJSON.String
		}
		r.IsSystem = isSystem == 1
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) > limit {
		last := out[limit-1]
		nextCursor = fmt.Sprintf("%d:%d", last.SortOrder, last.ID)
		out = out[:limit]
	}
	return out, nextCursor, nil
}

// GetTopicByExternalRef loads one imported knowledge point.
func (s *Store) GetTopicByExternalRef(ctx context.Context, externalRef string) (*model.KnowledgeTag, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, subject, parent_id, code, sort_order, is_system, grade, semester,
		        domain, chapter, topic_type, description, source, external_ref,
		        mastery_criteria_json, diagnostic_question_json
		 FROM knowledge_tags WHERE external_ref = ?`, externalRef)
	return scanKnowledgeTag(row)
}

// scanKnowledgeTag decodes one full knowledge_tags row.
func scanKnowledgeTag(row interface{ Scan(...any) error }) (*model.KnowledgeTag, error) {
	var t model.KnowledgeTag
	var parentID sql.NullInt64
	var code sql.NullString
	var isSystem int
	var grade, semester sql.NullInt64
	var masteryJSON, diagJSON sql.NullString
	err := row.Scan(&t.ID, &t.Name, &t.Subject, &parentID, &code, &t.SortOrder, &isSystem,
		&grade, &semester, &t.Domain, &t.Chapter, &t.TopicType, &t.Description,
		&t.Source, &t.ExternalRef, &masteryJSON, &diagJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if parentID.Valid {
		v := parentID.Int64
		t.ParentID = &v
	}
	if code.Valid {
		t.Code = code.String
	}
	if grade.Valid {
		v := int(grade.Int64)
		t.Grade = &v
	}
	if semester.Valid {
		v := int(semester.Int64)
		t.Semester = &v
	}
	if masteryJSON.Valid {
		t.MasteryJSON = masteryJSON.String
	}
	if diagJSON.Valid {
		t.DiagnosticJSON = diagJSON.String
	}
	t.IsSystem = isSystem == 1
	return &t, nil
}

// ListPrerequisites returns one tag's prerequisite edges (topic_id is
// the tag itself). Effective-graph semantics (DEF-PREREQ-READ-FILTER):
// rejected edges are filtered out and both endpoints must resolve
// through knowledge_tags with a non-empty external_ref — the same
// caliber as ListEffectivePrerequisiteEdges, so no rejected or
// ref-less edge leaks through this read. Ids stay integers (the
// model/API contract is unchanged).
func (s *Store) ListPrerequisites(ctx context.Context, tagID int64) ([]model.KnowledgeTagPrerequisite, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.topic_id, e.prerequisite_id, e.strength, e.reason, e.review_status
		 FROM knowledge_tag_prerequisites e
		 JOIN knowledge_tags tt ON tt.id = e.topic_id
		 JOIN knowledge_tags pt ON pt.id = e.prerequisite_id
		 WHERE e.topic_id = ? AND e.review_status <> 'rejected'
		   AND COALESCE(tt.external_ref, '') <> ''
		   AND COALESCE(pt.external_ref, '') <> ''
		 ORDER BY e.rowid`, tagID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.KnowledgeTagPrerequisite, 0, 4)
	for rows.Next() {
		var e model.KnowledgeTagPrerequisite
		var reason sql.NullString
		if err := rows.Scan(&e.TagID, &e.PrerequisiteID, &e.Strength, &reason, &e.ReviewStatus); err != nil {
			return nil, err
		}
		if reason.Valid {
			e.Reason = reason.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListDependents returns the edges pointing at a tag (who depends on
// it) — the graph detail view's downstream list. Effective-graph
// semantics (DEF-PREREQ-READ-FILTER): rejected edges are filtered out
// and both endpoints must resolve through knowledge_tags with a
// non-empty external_ref, mirroring ListEffectivePrerequisiteEdges.
// Ids stay integers (the model/API contract is unchanged).
func (s *Store) ListDependents(ctx context.Context, tagID int64) ([]model.KnowledgeTagPrerequisite, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.topic_id, e.prerequisite_id, e.strength, e.reason, e.review_status
		 FROM knowledge_tag_prerequisites e
		 JOIN knowledge_tags tt ON tt.id = e.topic_id
		 JOIN knowledge_tags pt ON pt.id = e.prerequisite_id
		 WHERE e.prerequisite_id = ? AND e.review_status <> 'rejected'
		   AND COALESCE(tt.external_ref, '') <> ''
		   AND COALESCE(pt.external_ref, '') <> ''
		 ORDER BY e.rowid`, tagID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.KnowledgeTagPrerequisite, 0, 4)
	for rows.Next() {
		var e model.KnowledgeTagPrerequisite
		var reason sql.NullString
		if err := rows.Scan(&e.TagID, &e.PrerequisiteID, &e.Strength, &reason, &e.ReviewStatus); err != nil {
			return nil, err
		}
		if reason.Valid {
			e.Reason = reason.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListQuestionsForTopic returns the tag's bank questions in sort order.
func (s *Store) ListQuestionsForTopic(ctx context.Context, topicRowID int64) ([]model.KnowledgeQuestion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, topic_id, external_ref, difficulty, type, prompt, choices_json, answer_json,
		        accept_json, explanation, verify, unordered, figure, hide_choice_text, source, sort_order
		 FROM knowledge_questions WHERE topic_id = ? ORDER BY sort_order, id`, topicRowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.KnowledgeQuestion, 0, 4)
	for rows.Next() {
		var q model.KnowledgeQuestion
		var externalRef, choices, answer, accept, explanation, verify, figure, source sql.NullString
		var unordered, hideChoice int
		if err := rows.Scan(&q.ID, &q.TopicID, &externalRef, &q.Difficulty, &q.Type, &q.Prompt,
			&choices, &answer, &accept, &explanation, &verify, &unordered, &figure, &hideChoice,
			&source, &q.SortOrder); err != nil {
			return nil, err
		}
		if externalRef.Valid {
			q.ExternalRef = externalRef.String
		}
		if choices.Valid {
			q.ChoicesJSON = choices.String
		}
		if answer.Valid {
			q.AnswerJSON = answer.String
		}
		if accept.Valid {
			q.AcceptJSON = accept.String
		}
		if explanation.Valid {
			q.Explanation = explanation.String
		}
		if verify.Valid {
			q.Verify = verify.String
		}
		if figure.Valid {
			q.Figure = figure.String
		}
		if source.Valid {
			q.Source = source.String
		}
		q.Unordered = unordered == 1
		q.HideChoiceText = hideChoice == 1
		out = append(out, q)
	}
	return out, rows.Err()
}

// ListStandardsForTopic returns the curriculum-standard clauses linked
// to one tag.
func (s *Store) ListStandardsForTopic(ctx context.Context, tagID int64) ([]model.CurriculumStandard, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT cs.id, cs.external_ref, cs.subject, cs.domain, cs.section, cs.clause, cs.text, cs.page_or_ref
		 FROM knowledge_tag_standards kts
		 JOIN curriculum_standards cs ON cs.id = kts.standard_id
		 WHERE kts.tag_id = ? ORDER BY cs.id`, tagID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.CurriculumStandard, 0, 2)
	for rows.Next() {
		var st model.CurriculumStandard
		var externalRef, domain, section, clause, pageOrRef sql.NullString
		if err := rows.Scan(&st.ID, &externalRef, &st.Subject, &domain, &section, &clause, &st.Body, &pageOrRef); err != nil {
			return nil, err
		}
		if externalRef.Valid {
			st.ExternalRef = externalRef.String
		}
		if domain.Valid {
			st.Domain = domain.String
		}
		if section.Valid {
			st.Section = section.String
		}
		if clause.Valid {
			st.Clause = clause.String
		}
		if pageOrRef.Valid {
			st.PageOrRef = pageOrRef.String
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// --- import reconciliation (T2 acceptance numbers) ---

// TaxonomyCounts is the reconciliation snapshot the status/import
// responses report.
type TaxonomyCounts struct {
	Topics        int64 `json:"topics"`
	Prerequisites int64 `json:"prerequisites"`
	HardEdges     int64 `json:"hard_edges"`
	SoftEdges     int64 `json:"soft_edges"`
	Questions     int64 `json:"questions"`
	Standards     int64 `json:"standards"`
	TagStandards  int64 `json:"tag_standards"`
}

// CountTaxonomy reconciles the imported rows (the spec's baseline:
// 3108 / 4625 (hard 2478, soft 2147) / 12579 / 774 / 3372).
func (s *Store) CountTaxonomy(ctx context.Context) (*TaxonomyCounts, error) {
	c := &TaxonomyCounts{}
	err := s.db.QueryRowContext(ctx,
		`SELECT
		   (SELECT COUNT(*) FROM knowledge_tags WHERE is_system = 1 AND external_ref IS NOT NULL),
		   (SELECT COUNT(*) FROM knowledge_tag_prerequisites),
		   (SELECT COUNT(*) FROM knowledge_tag_prerequisites WHERE strength = 'hard'),
		   (SELECT COUNT(*) FROM knowledge_tag_prerequisites WHERE strength = 'soft'),
		   (SELECT COUNT(*) FROM knowledge_questions),
		   (SELECT COUNT(*) FROM curriculum_standards),
		   (SELECT COUNT(*) FROM knowledge_tag_standards)`).
		Scan(&c.Topics, &c.Prerequisites, &c.HardEdges, &c.SoftEdges, &c.Questions, &c.Standards, &c.TagStandards)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// --- subject stats for GET /api/taxonomy/subjects ---

// SubjectStat is one subject row with its browse counts.
type SubjectStat struct {
	Key           string `json:"id"`
	LabelZh       string `json:"name"`
	SBCode        string `json:"sb_code"`
	TopicCount    int64  `json:"topic_count"`
	QuestionCount int64  `json:"question_count"`
	Grades        []int  `json:"grades"`
}

// SubjectStats joins the word list with per-subject counts and grade
// ranges (imported rows only).
func (s *Store) SubjectStats(ctx context.Context) ([]SubjectStat, error) {
	// enabled = 1 keeps this list consistent with validSubject: the
	// subject picker must never offer a subject whose vocabulary row
	// the household switched off.
	rows, err := s.db.QueryContext(ctx,
		`SELECT sub.key, sub.label_zh, COALESCE(sub.sb_code, ''),
		        (SELECT COUNT(*) FROM knowledge_tags kt WHERE kt.subject = sub.key AND kt.is_system = 1),
		        (SELECT COUNT(*) FROM knowledge_questions kq
		          JOIN knowledge_tags kt2 ON kt2.id = kq.topic_id WHERE kt2.subject = sub.key)
		 FROM subjects sub WHERE sub.enabled = 1 ORDER BY sub.sort_order, sub.key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SubjectStat, 0, 10)
	for rows.Next() {
		var st SubjectStat
		if err := rows.Scan(&st.Key, &st.LabelZh, &st.SBCode, &st.TopicCount, &st.QuestionCount); err != nil {
			return nil, err
		}
		st.Grades = []int{}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Grade ranges per subject, one extra query over the small set.
	for i := range out {
		grows, err := s.db.QueryContext(ctx,
			`SELECT DISTINCT grade FROM knowledge_tags WHERE subject = ? AND grade IS NOT NULL ORDER BY grade`, out[i].Key)
		if err != nil {
			return nil, err
		}
		grades := []int{}
		for grows.Next() {
			var g int
			if err := grows.Scan(&g); err != nil {
				grows.Close()
				return nil, err
			}
			grades = append(grades, g)
		}
		grows.Close()
		out[i].Grades = grades
	}
	return out, nil
}

// KCTagIDsForUser returns the tag row ids a kid has mastery rows for
// (the blind-spot view's starting set).
func (s *Store) KCTagIDsForUser(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tag_id FROM kc_mastery WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int64, 0, 8)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ExternalRefsForTagIDs resolves DB tag ids back to external_refs (the
// engine's id space).
func (s *Store) ExternalRefsForTagIDs(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, COALESCE(external_ref, '') FROM knowledge_tags WHERE id IN (`+
			strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var ref string
		if err := rows.Scan(&id, &ref); err != nil {
			return nil, err
		}
		out[id] = ref
	}
	return out, rows.Err()
}

// GetKnowledgeTag loads one tag by DB id (the blind-spot and practice
// paths need name/subject from the row).
func (s *Store) GetKnowledgeTag(ctx context.Context, id int64) (*model.KnowledgeTag, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, subject, parent_id, code, sort_order, is_system, grade, semester,
		        domain, chapter, topic_type, description, source, external_ref,
		        mastery_criteria_json, diagnostic_question_json
		 FROM knowledge_tags WHERE id = ?`, id)
	return scanKnowledgeTag(row)
}

// ListAllLearnerEvents returns the user's complete event stream, id
// ascending (stable insertion order), no LIMIT and no time-window
// filter — the restore path needs the whole append-only log, not the
// profile page's newest-N slice (ListLearnerEvents). result_json /
// context_json are nullable columns but the model keeps plain strings,
// so COALESCE maps NULL onto "" instead of failing the whole export
// scan. Empty result is a non-nil slice so callers can range without a
// nil guard.
func (s *Store) ListAllLearnerEvents(ctx context.Context, userID int64) ([]model.LearnerEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, verb, object_type, object_id,
		        COALESCE(result_json, ''), COALESCE(context_json, ''), occurred_at, created_at
		 FROM learner_events WHERE user_id = ? ORDER BY id ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.LearnerEvent, 0, 64)
	for rows.Next() {
		var e model.LearnerEvent
		if err := rows.Scan(&e.ID, &e.UserID, &e.Verb, &e.ObjectType, &e.ObjectID,
			&e.ResultJSON, &e.ContextJSON, &e.OccurredAt, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListReviewItemsByUser returns every review card of one user, id
// ascending, no LIMIT and no state/suspended filters — the restore path
// wants the full deck (suspended cards and future dues included), not
// the live queue (ListDueReviewItems filters and caps). Columns come
// from reviewItemColumns / scanReviewItem, which map the 18
// review_items columns one-to-one onto model.ReviewItem (ErrorItem is
// a joined-in field, never a column). Empty result is a non-nil slice.
func (s *Store) ListReviewItemsByUser(ctx context.Context, userID int64) ([]model.ReviewItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+reviewItemColumns+` FROM review_items WHERE user_id = ? ORDER BY id ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.ReviewItem, 0, 64)
	for rows.Next() {
		i, err := scanReviewItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}
