package api_test

import (
	"testing"
)

// 任务②：迁移 040 在全新库上生效——error_items 多出三条追溯列，老查询
// （scanErrorItem 全列扫描）不报错。
func TestMigration040Columns(t *testing.T) {
	env := setupTest(t)
	var cnt int
	if err := env.db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('error_items') WHERE name IN ('chore_schedule_id','chore_title','errored_date')`,
	).Scan(&cnt); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if cnt != 3 {
		t.Fatalf("expected 3 new columns, got %d", cnt)
	}
}
