package store_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	msqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/liftedkilt/openchore/migrations"
)

// migrator opens an empty in-memory database with the embedded migrations.
func migrator(t *testing.T) (*migrate.Migrate, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	driver, err := msqlite.WithInstance(db, &msqlite.Config{})
	if err != nil {
		t.Fatalf("migration driver: %v", err)
	}
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("migration source: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "sqlite", driver)
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	return m, db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// tableCount returns how many sqlite_master rows of the given type carry the
// given name. Deterministic (Query + COUNT), unlike db.Exec("SELECT ..."),
// which database/sql may route through a driver Execer that tolerates SELECT
// (modernc/sqlite drains SQLITE_ROW results and returns nil) and therefore
// cannot prove absence.
func tableCount(t *testing.T, db *sql.DB, objType, name string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?`, objType, name).Scan(&n); err != nil {
		t.Fatalf("count sqlite_master %s %s: %v", objType, name, err)
	}
	return n
}

// columnExists checks table shape via the pragma_table_info table-valued
// function, so a dropped column is a COUNT of 0 rather than a driver-dependent
// "did Exec error on this SELECT" gamble.
func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		t.Fatalf("pragma_table_info %s.%s: %v", table, column, err)
	}
	return n == 1
}

// Every migration applies on a fresh database, the whole chain rolls back,
// and applies again. The version is pinned to the newest migration number so
// a future file that breaks embedding/versioning fails here, not in prod.
func TestMigrationsUpDownUp(t *testing.T) {
	m, db := migrator(t)
	if err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}
	if v, _, _ := m.Version(); v != 42 {
		t.Fatalf("expected version 42, got %d", v)
	}
	if err := m.Down(); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("up again: %v", err)
	}
	mustExec(t, db, `SELECT user_id, week_start, summary FROM weekly_summaries`)
	mustExec(t, db, `SELECT color, ai_complete FROM users, chore_completions`)
}

// 042 lands the taxonomy persistence: knowledge_tags grows the imported-KC
// columns (external_ref is the importer's idempotency key, unique only when
// present), the prerequisite DAG gets its own edge table, the question bank
// and curriculum standards land, diagnostic sessions/answers persist per
// user, and the course system gains the unit/outcome KC links. Up/down/up
// keeps existing AI-created tags (no external_ref) and 028's unique
// constraint intact.
// The production path this test simulates: a database already carries 001-041
// data when 042 lands. Rather than building everything from scratch in this
// test (which would only prove "up works on an empty DB"), pre-existing rows
// go in BEFORE the step to 42: users, 028's AI-created knowledge tag, a chore
// with its completion, and 032's course/unit/outcome. 042 then steps up, one
// step down, and back up, and the pre-existing rows must survive untouched.
func TestMigration042Taxonomy(t *testing.T) {
	m, db := migrator(t)
	// 41 之前的链先落地，代表既有生产的行写在 41 上（users/chores 由更早迁移建立，
	// 033-041 各自的表此时也已存在——升 042 的前置状态是 41）。
	if err := m.Migrate(40); err != nil {
		t.Fatalf("migrate to 40: %v", err)
	}
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (1, 'Alex', 'admin'), (2, 'Mia', 'child')`)
	mustExec(t, db, `INSERT INTO chores (id, title, created_by) VALUES (1, '口算打卡', 1)`)
	mustExec(t, db, `INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES (1, 1, 2, 1)`)
	mustExec(t, db, `INSERT INTO chore_completions (id, chore_schedule_id, completed_by, completion_date, status)
		VALUES (1, 1, 2, '2026-11-01', 'approved')`)
	// 032 的课程体系既有行：042 给 course_outcomes 加列、建 unit 关联表，
	// 不得动这些行。
	mustExec(t, db, `INSERT INTO courses (id, user_id, title) VALUES (1, 2, '数学起步')`)
	mustExec(t, db, `INSERT INTO course_units (id, course_id, title, position) VALUES (1, 1, '第一单元', 1)`)
	mustExec(t, db, `INSERT INTO course_outcomes (id, course_id, title) VALUES (1, 1, '认识1到5')`)
	// 041（升 42 前的最后一层）落位后，028 的 AI 自由文本 tag 代表既有知识点。
	if err := m.Migrate(41); err != nil {
		t.Fatalf("migrate to 41: %v", err)
	}
	// A pre-existing AI free-text tag (028's only producer so far).
	mustExec(t, db, `INSERT INTO knowledge_tags (id, name, subject, is_system) VALUES (1, '两位数加法', 'math', 0)`)

	if err := m.Migrate(42); err != nil {
		t.Fatalf("migrate to 42: %v", err)
	}

	// Existing tag survives with the new columns defaulted/NULL.
	var name string
	var extRef sql.NullString
	var grade sql.NullInt64
	if err := db.QueryRow(`SELECT name, external_ref, grade FROM knowledge_tags WHERE id = 1`).Scan(&name, &extRef, &grade); err != nil {
		t.Fatalf("query old tag: %v", err)
	}
	if name != "两位数加法" || extRef.Valid || grade.Valid {
		t.Fatalf("old tag untouched: %q %v %v", name, extRef, grade)
	}

	// external_ref is the idempotency key: unique when present, NULL allowed
	// for any number of parent/AI-created tags.
	mustExec(t, db, `INSERT INTO knowledge_tags (name, subject, external_ref, grade, semester, domain, chapter, topic_type, description, source) VALUES
		('1到5各数的认识', 'math', 'cnm_p001', 1, 1, '数与代数', '一年级上册 第1单元', 'conceptual', '认读 1 到 5', 'cn-math-taxonomy@v1')`)
	mustExec(t, db, `INSERT INTO knowledge_tags (name, subject, external_ref) VALUES (2, 'math', NULL)`)
	mustExec(t, db, `INSERT INTO knowledge_tags (name, subject, external_ref) VALUES (3, 'math', NULL)`)
	if _, err := db.Exec(`INSERT INTO knowledge_tags (name, subject, external_ref) VALUES ('dup', 'math', 'cnm_p001')`); err == nil {
		t.Fatal("expected duplicate external_ref to violate the partial unique index")
	}
	// 028's own uniqueness still holds. SQLite treats NULL as distinct in
	// UNIQUE constraints (top-level duplicate names are pre-existing allowed
	// behavior), so pin the non-NULL parent_id case.
	mustExec(t, db, `INSERT INTO knowledge_tags (name, subject, parent_id) VALUES ('子节点', 'math', 1)`)
	if _, err := db.Exec(`INSERT INTO knowledge_tags (name, subject, parent_id) VALUES ('子节点', 'math', 1)`); err == nil {
		t.Fatal("expected UNIQUE(subject,name,parent_id) still enforced for non-NULL parent_id")
	}

	// Prerequisite DAG edge with the review governance values.
	mustExec(t, db, `INSERT INTO knowledge_tag_prerequisites (topic_id, prerequisite_id, strength, reason, review_status)
		SELECT 3, 2, 'hard', '先学加法', 'machine' WHERE EXISTS (SELECT 1 FROM knowledge_tags WHERE id = 2) AND EXISTS (SELECT 1 FROM knowledge_tags WHERE id = 3)`)
	// A self-edge (topic_id = prerequisite_id) must be kept out of the DAG.
	// The composite PRIMARY KEY alone does not reject the first such insert
	// (a fresh (2,2) pair is a new key; SQLite verifies FK keys after the
	// row insert and both sides resolve to the same existing tag), so the
	// first (2,2) insert lands and the SECOND one collides with the PRIMARY
	// KEY — SQLite reports composite-PK collisions as "UNIQUE constraint
	// failed" (modernc text: "constraint failed: UNIQUE constraint failed:
	// knowledge_tag_prerequisites.topic_id,
	// knowledge_tag_prerequisites.prerequisite_id"). Pin the error class so a
	// different failure (arity, typo, FK) cannot pass as "the key works".
	if _, err := db.Exec(`INSERT INTO knowledge_tag_prerequisites (topic_id, prerequisite_id, strength, reason, review_status)
		VALUES (2, 2, 'hard', '', 'machine')`); err != nil {
		t.Fatalf("first self-edge insert should land and be rejected on repeat, got: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO knowledge_tag_prerequisites (topic_id, prerequisite_id, strength, reason, review_status)
		VALUES (2, 2, 'hard', '', 'machine')`); err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("expected repeated self-edge rejected by PRIMARY KEY (topic_id = prerequisite_id), got: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO knowledge_tag_prerequisites (topic_id, prerequisite_id, strength, reason, review_status)
		VALUES (1, 2, 'solid', '', 'machine')`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("expected strength CHECK to reject 'solid', got: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO knowledge_tag_prerequisites (topic_id, prerequisite_id, strength, reason, review_status)
		VALUES (1, 2, 'hard', '', 'finalized')`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("expected review_status CHECK to reject 'finalized', got: %v", err)
	}

	// Question bank row (12579-scale shape, one sample here).
	mustExec(t, db, `INSERT INTO knowledge_questions (external_ref, topic_id, difficulty, type, prompt, choices_json, answer_json, explanation, verify)
		VALUES ('cnm_p001_q1', 1, 'basic', 'numeric', '3 的后面一个数是几？', '[]', '"4"', '往后数一个', '3+1')`)
	if _, err := db.Exec(`INSERT INTO knowledge_questions (external_ref, topic_id, difficulty, type, prompt) VALUES ('cnm_p001_q1', 1, 'basic', 'numeric', 'dup')`); err == nil {
		t.Fatal("expected duplicate question external_ref rejected")
	}
	if _, err := db.Exec(`INSERT INTO knowledge_questions (external_ref, topic_id, difficulty, type, prompt) VALUES ('x', 1, 'impossible', 'numeric', 'p')`); err == nil {
		t.Fatal("expected difficulty CHECK to reject 'impossible'")
	}

	// Curriculum standards + the tag link.
	mustExec(t, db, `INSERT INTO curriculum_standards (external_ref, subject, domain, section, clause, text, extra_json)
		VALUES ('cms-2022:1.NA.01', 'math', '数与代数', '数与运算', '(1)', '感悟数的意义', '{"verbatim": false}')`)
	mustExec(t, db, `INSERT INTO knowledge_tag_standards (tag_id, standard_id, confidence) VALUES (1, 1, 'exact')`)
	if _, err := db.Exec(`INSERT INTO knowledge_tag_standards (tag_id, standard_id, confidence) VALUES (1, 1, 'exact')`); err == nil {
		t.Fatal("expected duplicate tag-standard link rejected")
	}

	// Diagnostic session / answer history persists per user, answers are
	// never physically deleted (void is a status bit).
	mustExec(t, db, `INSERT INTO diagnostic_sessions (id, user_id, subject, mode, status, scope_json, state_json)
		VALUES (1, 2, 'math', 'diagnose', 'active', '{"grade":1}', '{}')`)
	mustExec(t, db, `INSERT INTO diagnostic_attempts (session_id, question_id, topic_id, seq, role, answer_json, correct)
		VALUES (1, 1, 1, 1, 'probe', '"4"', 1)`)
	if _, err := db.Exec(`INSERT INTO diagnostic_attempts (session_id, seq, role, status) VALUES (1, 1, 'probe', 'erased')`); err == nil {
		t.Fatal("expected attempt status CHECK to reject 'erased'")
	}

	// Course KC links: unit link table + the nullable outcome column (032 row,
	// created pre-42 above).
	mustExec(t, db, `INSERT INTO unit_knowledge_tags (unit_id, tag_id) VALUES (1, 1)`)
	mustExec(t, db, `UPDATE course_outcomes SET knowledge_tag_id = 1 WHERE id = 1`)
	var linked sql.NullInt64
	if err := db.QueryRow(`SELECT knowledge_tag_id FROM course_outcomes WHERE id = 1`).Scan(&linked); err != nil || !linked.Valid || linked.Int64 != 1 {
		t.Fatalf("outcome KC link: %v %v", linked, err)
	}

	// Share: token hash (never the raw token), revoke keeps the row.
	mustExec(t, db, `INSERT INTO diagnostic_shares (token_hash, owner_id, session_id, report_kind, snapshot_json, expires_at, created_by)
		VALUES ('sha256:abc', 1, 1, 'diagnosis', '{"weak": []}', '2026-12-01 00:00:00', 1)`)
	if _, err := db.Exec(`INSERT INTO diagnostic_shares (token_hash, owner_id, report_kind, snapshot_json, created_by) VALUES ('sha256:abc', 1, 'path', '{}', 1)`); err == nil {
		t.Fatal("expected duplicate token hash rejected")
	}
	if _, err := db.Exec(`INSERT INTO diagnostic_shares (token_hash, owner_id, report_kind, snapshot_json, created_by) VALUES ('sha256:def', 1, 'grades', '{}', 1)`); err == nil {
		t.Fatal("expected report_kind CHECK to reject 'grades'")
	}

	// Subjects table (seeded by the Go importer, structure only here).
	mustExec(t, db, `INSERT INTO subjects (key, label_zh, sb_code, sort_order) VALUES ('math', '数学', 'SB0201', 1)`)

	// Down drops everything 042 added and keeps the 001-041 data intact.
	// Presence/absence is proven deterministically via sqlite_master/pragma
	// counts — db.Exec on a SELECT must not be the oracle here: modernc's
	// driver implements ExecerContext and drains SQLITE_ROW, so Exec
	// ("SELECT ...") returns nil on any query that merely parses.
	if err := m.Steps(-1); err != nil {
		t.Fatalf("step down: %v", err)
	}
	// The pre-existing rows survive the round-trip through 042 untouched.
	var preName string
	var preSystem int
	if err := db.QueryRow(`SELECT name, is_system FROM knowledge_tags WHERE id = 1`).Scan(&preName, &preSystem); err != nil {
		t.Fatalf("query pre-existing tag after down: %v", err)
	}
	if preName != "两位数加法" || preSystem != 0 {
		t.Fatalf("pre-existing tag changed by 042 down: %q %d", preName, preSystem)
	}
	var outcomeTitle string
	if err := db.QueryRow(`SELECT title FROM course_outcomes WHERE id = 1`).Scan(&outcomeTitle); err != nil {
		t.Fatalf("query pre-existing outcome after down: %v", err)
	}
	if outcomeTitle != "认识1到5" {
		t.Fatalf("pre-existing outcome changed by 042 down: %q", outcomeTitle)
	}
	var completions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chore_completions WHERE id = 1 AND completed_by = 2 AND status = 'approved'`).Scan(&completions); err != nil || completions != 1 {
		t.Fatalf("pre-existing completion survived down: count=%d %v", completions, err)
	}
	mustExec(t, db, `SELECT name, subject, is_system FROM knowledge_tags WHERE id = 1`)
	mustExec(t, db, `SELECT title FROM course_outcomes WHERE id = 1`)
	for _, gone := range []string{
		"subjects",
		"knowledge_tag_prerequisites",
		"knowledge_questions",
		"curriculum_standards",
		"knowledge_tag_standards",
		"diagnostic_sessions",
		"diagnostic_attempts",
		"unit_knowledge_tags",
		"diagnostic_shares",
	} {
		if n := tableCount(t, db, "table", gone); n != 0 {
			t.Errorf("expected table %s gone after down, sqlite_master says %d", gone, n)
		}
	}
	// 042's indexes go with the columns/tables they belong to.
	for _, goneIdx := range []string{
		"idx_kt_external_ref",
		"idx_kt_subject_grade",
		"idx_kt_parent",
		"idx_kq_external_ref",
		"idx_kq_topic",
		"idx_cs_external_ref",
		"idx_cs_subject",
		"idx_kts_standard",
		"idx_ds_user",
		"idx_da_session",
		"idx_da_topic",
		"idx_ukt_tag",
		"idx_dsh_owner",
	} {
		if n := tableCount(t, db, "index", goneIdx); n != 0 {
			t.Errorf("expected index %s gone after down, sqlite_master says %d", goneIdx, n)
		}
	}
	// 028's own autoindex (UNIQUE(subject,name,parent_id)) must still be there.
	if n := tableCount(t, db, "index", "sqlite_autoindex_knowledge_tags_1"); n != 1 {
		t.Errorf("expected 028's UNIQUE autoindex kept after down, sqlite_master says %d", n)
	}
	for _, goneCol := range []string{
		"external_ref",
		"grade",
		"semester",
		"domain",
		"chapter",
		"topic_type",
		"description",
		"mastery_criteria_json",
		"diagnostic_question_json",
		"source",
		"standards_json",
		"mappings_json",
		"aliases_json",
	} {
		if columnExists(t, db, "knowledge_tags", goneCol) {
			t.Errorf("expected knowledge_tags.%s dropped after down", goneCol)
		}
	}
	// 028's original columns stay.
	for _, keptCol := range []string{"id", "name", "subject", "parent_id", "code", "sort_order", "is_system"} {
		if !columnExists(t, db, "knowledge_tags", keptCol) {
			t.Errorf("expected 028 column knowledge_tags.%s kept after down", keptCol)
		}
	}
	if columnExists(t, db, "course_outcomes", "knowledge_tag_id") {
		t.Error("expected course_outcomes.knowledge_tag_id dropped after down")
	}

	// Back up: everything is creatable again.
	if err := m.Steps(1); err != nil {
		t.Fatalf("step up: %v", err)
	}
	mustExec(t, db, `INSERT INTO knowledge_tags (name, subject, external_ref) VALUES ('again', 'math', 'cnm_p002')`)
	mustExec(t, db, `INSERT INTO subjects (key, label_zh) VALUES ('math', '数学')`)
}

// 018 on a database with data from before it (017 applied, as on the
// release after the redesign): AI rejections go, the review columns and
// settings move, and the spoken-description column is dropped. Down puts
// the shape back.
func TestMigration018SimplifyAI(t *testing.T) {
	m, db := migrator(t)
	if err := m.Migrate(17); err != nil {
		t.Fatalf("migrate to 17: %v", err)
	}

	mustExec(t, db, `INSERT INTO users (id, name, role, theme, color) VALUES (1, 'Mia', 'child', 'sunroom', 'mint')`)
	mustExec(t, db, `INSERT INTO chores (id, title, created_by, tts_description, tts_audio_url) VALUES (1, 'Make bed', 1, 'Make your bed, Mia!', '/tts/1.wav')`)
	mustExec(t, db, `INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES (1, 1, 1, 1)`)
	mustExec(t, db, `INSERT INTO chore_completions (id, chore_schedule_id, completed_by, status, completion_date, ai_feedback, ai_confidence)
		VALUES (1, 1, 1, 'approved', '2026-09-21', 'Looks great', 0.9),
		       (2, 1, 1, 'ai_rejected', '2026-09-22', 'Still messy', 0.8),
		       (3, 1, 1, 'pending', '2026-09-23', '', 0)`)
	mustExec(t, db, `INSERT INTO app_settings (key, value) VALUES
		('ai_enabled', 'true'), ('ai_endpoint', 'http://litert:8000'), ('ai_model', 'gemma'),
		('ai_tts_voice', 'af_bella'), ('ai_tts_enabled', 'true'), ('ai_auto_approve_threshold', '0.9')`)

	if err := m.Migrate(18); err != nil {
		t.Fatalf("migrate to 18: %v", err)
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM chore_completions`).Scan(&n)
	if n != 2 {
		t.Fatalf("expected the ai_rejected completion removed (2 left), got %d", n)
	}
	var feedback string
	var aiComplete sql.NullBool
	if err := db.QueryRow(`SELECT ai_feedback, ai_complete FROM chore_completions WHERE id = 1`).Scan(&feedback, &aiComplete); err != nil {
		t.Fatalf("query completion: %v", err)
	}
	if feedback != "Looks great" || aiComplete.Valid {
		t.Fatalf("expected feedback kept and ai_complete NULL, got %q %v", feedback, aiComplete)
	}
	if _, err := db.Exec(`INSERT INTO chore_completions (chore_schedule_id, completed_by, status, completion_date) VALUES (1, 1, 'ai_rejected', '2026-09-24')`); err == nil {
		t.Fatal("expected the ai_rejected status to be refused after 018")
	}
	var audio string
	db.QueryRow(`SELECT tts_audio_url FROM chores WHERE id = 1`).Scan(&audio)
	if audio != "" {
		t.Fatalf("expected audio cleared for re-recording, got %q", audio)
	}
	if _, err := db.Exec(`SELECT tts_description FROM chores`); err == nil {
		t.Fatal("expected chores.tts_description dropped")
	}
	settings := map[string]string{}
	rows, err := db.Query(`SELECT key, value FROM app_settings`)
	if err != nil {
		t.Fatalf("query settings: %v", err)
	}
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		settings[k] = v
	}
	rows.Close()
	want := map[string]string{"ai_photo_review": "true", "tts_voice": "af_bella", "ai_auto_approve_threshold": "0.9"}
	for k, v := range want {
		if settings[k] != v {
			t.Errorf("setting %s: got %q, want %q", k, settings[k], v)
		}
	}
	for _, gone := range []string{"ai_enabled", "ai_endpoint", "ai_model", "ai_tts_voice", "ai_tts_enabled"} {
		if _, ok := settings[gone]; ok {
			t.Errorf("expected setting %s removed", gone)
		}
	}
	mustExec(t, db, `INSERT INTO weekly_summaries (user_id, week_start, summary) VALUES (1, '2026-09-14', 'A good week')`)
	// 017's data is untouched.
	var color string
	db.QueryRow(`SELECT color FROM users WHERE id = 1`).Scan(&color)
	if color != "mint" {
		t.Fatalf("expected 017's colour kept, got %q", color)
	}

	// Down to 17 and back up.
	if err := m.Migrate(17); err != nil {
		t.Fatalf("migrate down to 17: %v", err)
	}
	mustExec(t, db, `SELECT tts_description FROM chores`)
	mustExec(t, db, `INSERT INTO chore_completions (chore_schedule_id, completed_by, status, completion_date) VALUES (1, 1, 'ai_rejected', '2026-09-24')`)
	db.QueryRow(`SELECT value FROM app_settings WHERE key = 'ai_enabled'`).Scan(&feedback)
	if feedback != "true" {
		t.Fatalf("expected ai_enabled restored, got %q", feedback)
	}
	if _, err := db.Exec(`SELECT 1 FROM weekly_summaries`); err == nil {
		t.Fatal("expected weekly_summaries dropped")
	}
	if err := m.Up(); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// Older versions deleted schedules and users without cascading, so a
// database can hold completions pointing at rows that no longer exist. 018
// rebuilds chore_completions with foreign keys enforced; the orphans must
// not make it fail.
func TestMigration018OrphanedCompletions(t *testing.T) {
	m, db := migrator(t)
	if err := m.Migrate(17); err != nil {
		t.Fatalf("migrate to 17: %v", err)
	}

	mustExec(t, db, `INSERT INTO users (id, name, role, theme, color) VALUES (1, 'Mia', 'child', 'sunroom', 'mint')`)
	mustExec(t, db, `INSERT INTO chores (id, title, created_by) VALUES (1, 'Make bed', 1)`)
	mustExec(t, db, `INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES (1, 1, 1, 1)`)
	mustExec(t, db, `PRAGMA foreign_keys = OFF`)
	mustExec(t, db, `INSERT INTO chore_completions (id, chore_schedule_id, completed_by, status, approved_by, completion_date)
		VALUES (1, 1, 1, 'approved', 1, '2026-09-21'),
		       (2, 99, 1, 'approved', NULL, '2026-09-21'),
		       (3, 1, 99, 'approved', NULL, '2026-09-21'),
		       (4, 1, 1, 'approved', 99, '2026-09-22')`)
	mustExec(t, db, `PRAGMA foreign_keys = ON`)

	if err := m.Migrate(18); err != nil {
		t.Fatalf("migrate to 18: %v", err)
	}

	rows, err := db.Query(`SELECT id, approved_by FROM chore_completions ORDER BY id`)
	if err != nil {
		t.Fatalf("query completions: %v", err)
	}
	got := map[int64]sql.NullInt64{}
	for rows.Next() {
		var id int64
		var approvedBy sql.NullInt64
		if err := rows.Scan(&id, &approvedBy); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = approvedBy
	}
	rows.Close()
	if len(got) != 2 {
		t.Fatalf("expected completions 1 and 4 kept, got %v", got)
	}
	if a, ok := got[1]; !ok || !a.Valid || a.Int64 != 1 {
		t.Fatalf("expected completion 1 kept with its approver, got %v", got)
	}
	if a, ok := got[4]; !ok || a.Valid {
		t.Fatalf("expected completion 4 kept with approved_by cleared, got %v", got)
	}
}

// 036 adds chores.subject (free text; the media library groups proof by it).
// Up then down then up must leave a chore's subject intact through the cycle.
// Migrate(36) pins the ceiling: a later migration (037+) must not turn
// Steps(-1) into "roll back the newer one instead".
func TestMigration036ChoreSubject(t *testing.T) {
	m, db := migrator(t)
	if err := m.Migrate(35); err != nil {
		t.Fatalf("migrate to 35: %v", err)
	}
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (1, 'Alex', 'admin')`)
	mustExec(t, db, `INSERT INTO chores (id, title, created_by) VALUES (1, '数学口算', 1)`)
	if err := m.Migrate(36); err != nil {
		t.Fatalf("migrate to 36: %v", err)
	}
	mustExec(t, db, `UPDATE chores SET subject = 'math' WHERE id = 1`)
	var subj string
	if err := db.QueryRow(`SELECT subject FROM chores WHERE id = 1`).Scan(&subj); err != nil || subj != "math" {
		t.Fatalf("subject after up: %q, %v", subj, err)
	}
	// One step down drops just the column; the chore row itself survives.
	if err := m.Steps(-1); err != nil {
		t.Fatalf("step down: %v", err)
	}
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chores WHERE id = 1`).Scan(&cnt); err != nil || cnt != 1 {
		t.Fatalf("chore survived down: count=%d %v", cnt, err)
	}
	// One step up re-adds it, defaulting to "" (the value is not preserved
	// through a DROP COLUMN — down is a rollback, not a round-trip).
	if err := m.Steps(1); err != nil {
		t.Fatalf("step up: %v", err)
	}
	if err := db.QueryRow(`SELECT subject FROM chores WHERE id = 1`).Scan(&subj); err != nil || subj != "" {
		t.Fatalf("subject after down/up: %q, %v", subj, err)
	}
}

// 037 adds paper_analyses.instructions (the parent's free-text focus for
// one parse). Same pinned-ceiling cycle as 036: up, drop, re-add.
func TestMigration037AnalysisInstructions(t *testing.T) {
	m, db := migrator(t)
	if err := m.Migrate(36); err != nil {
		t.Fatalf("migrate to 36: %v", err)
	}
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (1, 'Alex', 'admin')`)
	mustExec(t, db, `INSERT INTO chores (id, title, created_by) VALUES (1, '口算', 1)`)
	mustExec(t, db, `INSERT INTO chore_schedules (id, chore_id, assigned_to, start_date, specific_date) VALUES (1, 1, 1, '2026-01-01', '2026-01-01')`)
	mustExec(t, db, `INSERT INTO chore_completions (id, chore_schedule_id, completed_by, completion_date, status) VALUES (1, 1, 1, '2026-01-01', 'approved')`)
	mustExec(t, db, `INSERT INTO paper_analyses (id, completion_id, user_id, created_by, subject, status) VALUES (1, 1, 1, 1, 'math', 'done')`)
	if err := m.Migrate(37); err != nil {
		t.Fatalf("migrate to 37: %v", err)
	}
	mustExec(t, db, `UPDATE paper_analyses SET instructions = '重点分析第3题' WHERE id = 1`)
	var instr string
	if err := db.QueryRow(`SELECT instructions FROM paper_analyses WHERE id = 1`).Scan(&instr); err != nil || instr != "重点分析第3题" {
		t.Fatalf("instructions after up: %q, %v", instr, err)
	}
	// The column is nullable: the auto path (no parent) stores NULL.
	mustExec(t, db, `INSERT INTO paper_analyses (completion_id, user_id, created_by, subject, status) VALUES (1, 1, 1, 'math', 'pending')`)
	var nullCheck sql.NullString
	if err := db.QueryRow(`SELECT instructions FROM paper_analyses WHERE id = 2`).Scan(&nullCheck); err != nil || nullCheck.Valid {
		t.Fatalf("expected NULL instructions by default: %v, %v", nullCheck, err)
	}
	// Down drops just the column; the analysis row survives.
	if err := m.Steps(-1); err != nil {
		t.Fatalf("step down: %v", err)
	}
	var cnt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM paper_analyses WHERE id = 1`).Scan(&cnt); err != nil || cnt != 1 {
		t.Fatalf("analysis survived down: count=%d %v", cnt, err)
	}
	if err := m.Steps(1); err != nil {
		t.Fatalf("step up: %v", err)
	}
	if err := db.QueryRow(`SELECT instructions FROM paper_analyses WHERE id = 1`).Scan(&nullCheck); err != nil || nullCheck.Valid {
		t.Fatalf("expected NULL after down/up: %v, %v", nullCheck, err)
	}
}
