package store_test

// 7.16-TESTER §B：042 taxonomy 真库（磁盘文件）回滚演练。
//
// migrations_test.go 的 TestMigration042Taxonomy 跑在 :memory:，且只按名字
// 清点 13 个索引（漏 idx_ktp_prereq / idx_ktp_topic）。本文件在 t.TempDir()
// 的真实磁盘库上重放同一路径：Migrate(41) → 写 001-041 代表数据 →
// Migrate(42) → 校验 9 表 / 15 个显式索引 → 降回 41 → 15 索引与 9 表全部
// 消失、PRAGMA foreign_key_check 0 行、sqlite_master 与全新 41 基线全量逐行
// 一致 → 再次升 42。自带 migrator/DSN，不改既有的 migrator(t)（:memory:）。
//
// 另外显式验证 mustExec 的 Exec(SELECT) 边界（验收第 6 条）：判断
// db.Exec("SELECT ...") 是否吞错/是否受结果集为空影响，只记录结论，不据此
// 修改 migrations_test.go。

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	msqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/liftedkilt/openchore/migrations"
)

// 042 up.sql 中的全部 9 张新表。
var tax042Tables = []string{
	"subjects",
	"knowledge_tag_prerequisites",
	"knowledge_questions",
	"curriculum_standards",
	"knowledge_tag_standards",
	"diagnostic_sessions",
	"diagnostic_attempts",
	"unit_knowledge_tags",
	"diagnostic_shares",
}

// 042 up.sql 中的全部 15 个显式索引（3 个 UNIQUE + 12 个普通索引），
// 不照抄 migrations_test.go:287-300 的 13 个名字。
var tax042Indexes = []string{
	"idx_kt_external_ref",  // up:45 UNIQUE ... WHERE external_ref IS NOT NULL
	"idx_kt_subject_grade", // up:48
	"idx_kt_parent",        // up:50
	"idx_ktp_prereq",       // up:65（既有测试未覆盖）
	"idx_ktp_topic",        // up:66（既有测试未覆盖）
	"idx_kq_external_ref",  // up:94 UNIQUE
	"idx_kq_topic",         // up:97
	"idx_cs_external_ref",  // up:115 UNIQUE
	"idx_cs_subject",       // up:117
	"idx_kts_standard",     // up:128
	"idx_ds_user",          // up:153
	"idx_da_session",       // up:173
	"idx_da_topic",         // up:174
	"idx_ukt_tag",          // up:183
	"idx_dsh_owner",        // up:205
}

// openDiskMigrations 在 t.TempDir() 下新建真实磁盘库（非 :memory:）。
// t.TempDir 由 testing 框架在用例结束时自动清理，仓库内不留文件。
func openDiskMigrations(t *testing.T, name string) (*migrate.Migrate, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	dsn := "file:" + path + "?_foreign_keys=on&_busy_timeout=5000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open disk db %s: %v", dsn, err)
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

// seed041Rows 写入 001-041 的代表性既有数据（与 TestMigration042Taxonomy
// 同源，但全部存在性断言用 QueryRow().Scan()）。
func seed041Rows(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO users (id, name, role) VALUES (1, 'Alex', 'admin'), (2, 'Mia', 'child')`,
		`INSERT INTO chores (id, title, created_by) VALUES (1, '口算打卡', 1)`,
		`INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES (1, 1, 2, 1)`,
		`INSERT INTO chore_completions (id, chore_schedule_id, completed_by, completion_date, status)
			VALUES (1, 1, 2, '2026-11-01', 'approved')`,
		`INSERT INTO courses (id, user_id, title) VALUES (1, 2, '数学起步')`,
		`INSERT INTO course_units (id, course_id, title, position) VALUES (1, 1, '第一单元', 1)`,
		`INSERT INTO course_outcomes (id, course_id, title) VALUES (1, 1, '认识1到5')`,
		`INSERT INTO knowledge_tags (id, name, subject, is_system) VALUES (1, '两位数加法', 'math', 0)`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed 041 (%s): %v", q, err)
		}
	}
}

// seed042Rows 写入 042 新表的一行代表数据（用于 down 后 FK 检查与再次 up）。
func seed042Rows(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`INSERT INTO subjects (key, label_zh, sb_code, sort_order) VALUES ('math', '数学', 'SB0201', 1)`,
		`INSERT INTO knowledge_tags (id, name, subject, external_ref, grade) VALUES (2, '1到5各数的认识', 'math', 'cnm_p001', 1)`,
		`INSERT INTO knowledge_tags (id, name, subject, external_ref) VALUES (3, '6到9各数的认识', 'math', 'cnm_p002')`,
		`INSERT INTO knowledge_tag_prerequisites (topic_id, prerequisite_id, strength, review_status) SELECT 3, 2, 'hard', 'machine'
			WHERE EXISTS (SELECT 1 FROM knowledge_tags WHERE id = 2) AND EXISTS (SELECT 1 FROM knowledge_tags WHERE id = 3)`,
		`INSERT INTO knowledge_questions (external_ref, topic_id, difficulty, type, prompt, verify) SELECT 'cnm_p001_q1', 2, 'basic', 'numeric', 'p', '2-1'`,
		`INSERT INTO curriculum_standards (external_ref, subject, domain, section, clause, text, extra_json)
			SELECT 'cms-2022:1.NA.01', 'math', '数与代数', '数与运算', '(1)', 't', '{}'`,
		`INSERT INTO knowledge_tag_standards (tag_id, standard_id, confidence) SELECT 2, 1, 'exact'`,
		`INSERT INTO unit_knowledge_tags (unit_id, tag_id) SELECT 1, 2 WHERE EXISTS (SELECT 1 FROM course_units WHERE id = 1)`,
		`UPDATE course_outcomes SET knowledge_tag_id = 2 WHERE id = 1`,
		`INSERT INTO diagnostic_sessions (id, user_id, subject, mode, status, scope_json, state_json) SELECT 1, 2, 'math', 'diagnose', 'active', '{}', '{}'`,
		`INSERT INTO diagnostic_attempts (session_id, question_id, topic_id, seq, role, answer_json, correct) SELECT 1, 1, 2, 1, 'probe', '"4"', 1`,
		`INSERT INTO diagnostic_shares (token_hash, owner_id, session_id, report_kind, snapshot_json, expires_at, created_by)
			SELECT 'sha256:abc', 1, 1, 'diagnosis', '{"weak": []}', '2026-12-01 00:00:00', 1`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed 042 (%s): %v", q, err)
		}
	}
}

// objectCount 走 QueryRow().Scan() 计数，不依赖 Exec(SELECT) 的返回值。
func objectCount(t *testing.T, db *sql.DB, objType, name string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?`, objType, name).Scan(&n); err != nil {
		t.Fatalf("count sqlite_master %s %s: %v", objType, name, err)
	}
	return n
}

// schemaRow 是 sqlite_master 的一行 (type, name, tbl_name, sql)。
type schemaRow struct {
	Type, Name, TblName string
	SQL                 string
}

// schemaSnapshot 全量（非计数）读出 sqlite_master。
func schemaSnapshot(t *testing.T, db *sql.DB) []schemaRow {
	t.Helper()
	rows, err := db.Query(`SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	defer rows.Close()
	var out []schemaRow
	for rows.Next() {
		var r schemaRow
		if err := rows.Scan(&r.Type, &r.Name, &r.TblName, &r.SQL); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (r schemaRow) String() string {
	return fmt.Sprintf("(%s, %s, %s, %q)", r.Type, r.Name, r.TblName, r.SQL)
}

// diffSchema 返回两边的差异行（期望 vs 实际）。
func diffSchema(want, got []schemaRow) []string {
	wantSet := make(map[string]bool, len(want))
	for _, r := range want {
		wantSet[r.String()] = true
	}
	gotSet := make(map[string]bool, len(got))
	for _, r := range got {
		gotSet[r.String()] = true
	}
	var out []string
	for _, r := range want {
		if !gotSet[r.String()] {
			out = append(out, "missing after down: "+r.String())
		}
	}
	for _, r := range got {
		if !wantSet[r.String()] {
			out = append(out, "unexpected after down: "+r.String())
		}
	}
	return out
}

// baselineAt41 在独立磁盘库上只跑到 41，作为 sqlite_master 的黄金基线。
func baselineAt41(t *testing.T) []schemaRow {
	t.Helper()
	m, db := openDiskMigrations(t, "baseline.db")
	if err := m.Migrate(41); err != nil {
		t.Fatalf("baseline migrate to 41: %v", err)
	}
	return schemaSnapshot(t, db)
}

// TestMigration042DiskRollback 是 §B 的验收主用例：磁盘真库上的 042 回滚演练。
func TestMigration042DiskRollback(t *testing.T) {
	m, db := openDiskMigrations(t, "taxonomy.db")

	// 验收 2（前半）：Migrate(41) → 写 001-041 代表数据。
	if err := m.Migrate(41); err != nil {
		t.Fatalf("migrate to 41: %v", err)
	}
	seed041Rows(t, db)

	// 验收 2（中段）：升 42，9 表 / 15 索引按全量名单存在。
	if err := m.Migrate(42); err != nil {
		t.Fatalf("migrate to 42: %v", err)
	}
	if v, _, _ := m.Version(); v != 42 {
		t.Fatalf("expected version 42 after up, got %d", v)
	}
	for _, tbl := range tax042Tables {
		if n := objectCount(t, db, "table", tbl); n != 1 {
			t.Errorf("042 up: expected table %s present, sqlite_master says %d", tbl, n)
		}
	}
	for _, idx := range tax042Indexes {
		if n := objectCount(t, db, "index", idx); n != 1 {
			t.Errorf("042 up: expected explicit index %s present, sqlite_master says %d", idx, n)
		}
	}
	// 显式索引的 tbl_name 也要落在 042 自己的对象上（防同名索引挂错表）。
	for _, idx := range tax042Indexes {
		var tbl string
		if err := db.QueryRow(`SELECT tbl_name FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&tbl); err != nil {
			t.Fatalf("index %s tbl_name: %v", idx, err)
		}
		if tbl != "knowledge_tags" && !containsString(tax042Tables, tbl) {
			t.Errorf("index %s attached to unexpected table %q", idx, tbl)
		}
	}
	seed042Rows(t, db)

	// 验收 2（后半）：降回 41。
	if err := m.Migrate(41); err != nil {
		t.Fatalf("migrate back to 41: %v", err)
	}
	if v, _, _ := m.Version(); v != 41 {
		t.Fatalf("expected version 41 after down, got %d", v)
	}
	for _, tbl := range tax042Tables {
		if n := objectCount(t, db, "table", tbl); n != 0 {
			t.Errorf("042 down: expected table %s gone, sqlite_master says %d", tbl, n)
		}
	}
	for _, idx := range tax042Indexes {
		if n := objectCount(t, db, "index", idx); n != 0 {
			t.Errorf("042 down: expected explicit index %s gone, sqlite_master says %d", idx, n)
		}
	}
	// 028 自带的 UNIQUE autoindex 必须原样保留。
	if n := objectCount(t, db, "index", "sqlite_autoindex_knowledge_tags_1"); n != 1 {
		t.Errorf("expected 028 UNIQUE autoindex kept after down, sqlite_master says %d", n)
	}

	// 001-041 数据可回读（QueryRow().Scan()，不用 Exec(SELECT) 当 oracle）。
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
	var unitTitle string
	if err := db.QueryRow(`SELECT title FROM course_units WHERE id = 1`).Scan(&unitTitle); err != nil || unitTitle != "第一单元" {
		t.Fatalf("pre-existing unit survived down: %q %v", unitTitle, err)
	}

	// 验收 3：down 后 PRAGMA foreign_key_check 必须 0 行。
	fkRows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("pragma foreign_key_check: %v", err)
	}
	fkViolations := 0
	for fkRows.Next() {
		fkViolations++
		var tbl, parent string
		var rowid sql.NullInt64
		var fkid int
		if err := fkRows.Scan(&tbl, &rowid, &parent, &fkid); err != nil {
			fkRows.Close()
			t.Fatalf("scan foreign_key_check row: %v", err)
		}
		t.Logf("foreign_key_check violation: table=%s rowid=%v parent=%s fkid=%d", tbl, rowid, parent, fkid)
	}
	if err := fkRows.Err(); err != nil {
		fkRows.Close()
		t.Fatalf("iterate foreign_key_check: %v", err)
	}
	fkRows.Close()
	t.Logf("PRAGMA foreign_key_check after down: %d rows", fkViolations)
	if fkViolations != 0 {
		t.Fatalf("PRAGMA foreign_key_check after down: expected 0 rows, got %d", fkViolations)
	}

	// 验收 4：sqlite_master 与全新 41 基线全量逐行比对。
	want := baselineAt41(t)
	got := schemaSnapshot(t, db)
	diffs := diffSchema(want, got)
	t.Logf("sqlite_master full compare: baseline %d rows, down-ed %d rows, diff %d", len(want), len(got), len(diffs))
	for _, d := range diffs {
		t.Errorf("sqlite_master diff: %s", d)
	}

	// 验收 5：再次 up 成功，且 9 表 / 15 索引重新出现。
	if err := m.Migrate(42); err != nil {
		t.Fatalf("migrate 42 again after down: %v", err)
	}
	if v, _, _ := m.Version(); v != 42 {
		t.Fatalf("expected version 42 after second up, got %d", v)
	}
	for _, tbl := range tax042Tables {
		if n := objectCount(t, db, "table", tbl); n != 1 {
			t.Errorf("042 up again: expected table %s present, sqlite_master says %d", tbl, n)
		}
	}
	for _, idx := range tax042Indexes {
		if n := objectCount(t, db, "index", idx); n != 1 {
			t.Errorf("042 up again: expected explicit index %s present, sqlite_master says %d", idx, n)
		}
	}
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// TestExecSelectFalseNegative 是验收 6：显式测量 db.Exec("SELECT ...") 的
// 边界。只记录实测行为（是否返回 error、是否受结果集为空影响），不据此
// 修改 migrations_test.go；mustExec 的调用点是否因此产生假阴性由主会话裁决。
func TestExecSelectFalseNegative(t *testing.T) {
	m, db := openDiskMigrations(t, "execselect.db")
	if err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}

	// (a) 有结果集的 SELECT。
	_, errNonEmpty := db.Exec(`SELECT user_id, week_start, summary FROM weekly_summaries`)
	t.Logf("Exec(SELECT ... weekly_summaries, empty table): err=%v", errNonEmpty)

	// (b) 零结果集的 SELECT。
	_, errZero := db.Exec(`SELECT 1 WHERE 0`)
	t.Logf("Exec(SELECT 1 WHERE 0): err=%v", errZero)

	// (c) 引用已不存在的列/表 —— 编译期错误，即便是 SELECT 也应报错。
	_, errBadCol := db.Exec(`SELECT no_such_column FROM weekly_summaries`)
	t.Logf("Exec(SELECT no_such_column ...): err=%v", errBadCol)
	_, errBadTbl := db.Exec(`SELECT 1 FROM no_such_table`)
	t.Logf("Exec(SELECT ... FROM no_such_table): err=%v", errBadTbl)

	// (d) 对照：QueryRow().Scan() 对同一不存在对象的反应。
	var dummy int
	errScanBadCol := db.QueryRow(`SELECT no_such_column FROM weekly_summaries`).Scan(&dummy)
	t.Logf("QueryRow(SELECT no_such_column ...).Scan: err=%v", errScanBadCol)

	// 结论只做事实断言，不预设「是否假阴性」的判定口径：
	// 语法/编译错误在 SELECT 下同样必须被 Exec 报出（否则 mustExec 连拼写错都吞）。
	if errBadCol == nil && errScanBadCol != nil {
		t.Errorf("Exec(SELECT) swallowed a compile error that Query+Scan surfaced: Exec err=nil, Scan err=%v", errScanBadCol)
	}
	if errBadTbl == nil {
		t.Errorf("Exec(SELECT FROM missing table) returned nil error")
	}
	if errBadCol != nil && !strings.Contains(strings.ToLower(errBadCol.Error()), "no_such_column") {
		t.Logf("Exec bad column error text: %v", errBadCol)
	}
}
