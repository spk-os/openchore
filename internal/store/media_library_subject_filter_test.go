package store_test

// 真实 SQLite 回归（卡 TX-DYNAMIC-SUBJECTS-TESTER）：媒体库学科筛选的冻结
// R2 编码——'' = 全部（无约束），'__unclassified__' = 仅 COALESCE(subject,'')=''
// 的行，其余为精确学科匹配；stats 不走 subject 过滤（chip 计数不随选中学科
// 回退），空串桶在 stats 里以 subject='' 呈现。所有断言基于真实迁移后的库，
// 不 mock SQL。

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestMediaLibrarySubjectFilterEncodings(t *testing.T) {
	st, db := setupStoreWithDB(t)
	ctx := context.Background()

	// chores.subject 自 036 起 NOT NULL DEFAULT ''，而 chores.id 却被 FOREIGN KEY
	// 约束成非空：场景 1 用空串覆盖 COALESCE 的 '' 分支。
	//（场景 2：chore 被删后的悬空联接 / NULL 分支见下方「约束说明」——生产 schema
	// 下无法构造，故不作为断言。）
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (1, 'Mia', 'child')`)
	mustExec(t, db, `INSERT INTO chores (id, title, category, subject, points_value, created_by) VALUES
		(1, 'Math workbook', 'core', 'math', 10, 1),
		(2, 'English reading', 'core', 'english', 10, 1),
		(3, 'Dishes', 'core', '', 5, 1),
		(4, 'Legacy', 'core', 'history', 5, 1)`)
	mustExec(t, db, `INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES
		(1, 1, 1, 1), (2, 2, 1, 1), (3, 3, 1, 1), (4, 4, 1, 1)`)
	today := time.Now().Format("2006-01-02")
	mustExec(t, db, fmt.Sprintf(`INSERT INTO chore_completions (id, chore_schedule_id, completed_by, status, completion_date) VALUES
		(1, 1, 1, 'pending', '%[1]s'),
		(2, 2, 1, 'pending', '%[1]s'),
		(3, 3, 1, 'pending', '%[1]s'),
		(4, 4, 1, 'pending', '%[1]s')`, today))
	mustExec(t, db, fmt.Sprintf(`INSERT INTO completion_attachments
		(id, completion_id, file_path, display_name, mime_type, size_bytes, mime_class, sha256, sort_order, created_at) VALUES
		(1, 1, '/uploads/m1.jpg', 'm1.jpg', 'image/jpeg', 10, 'image', 'ms1', 0, '%[1]s 10:00:00'),
		(2, 2, '/uploads/e1.png', 'e1.png', 'image/png', 10, 'image', 'ms2', 0, '%[1]s 10:00:00'),
		(3, 3, '/uploads/u1.webm', 'u1.webm', 'audio/webm', 10, 'audio', 'ms3', 0, '%[1]s 10:00:00'),
		(4, 4, '/uploads/n1.mp4', 'n1.mp4', 'video/mp4', 10, 'video', 'ms4', 0, '%[1]s 10:00:00'),
		(5, 1, '/uploads/gone.pdf', 'gone.pdf', 'application/pdf', 10, 'pdf', 'ms5', 1, '%[1]s 10:00:00')`, today))
	mustExec(t, db, `UPDATE completion_attachments SET deleted_at = '2026-10-17 00:00:00' WHERE id = 5`)
	// 约束说明：chore_schedules.chore_id → chores(id) ON DELETE CASCADE 且
	// chore_completions.chore_schedule_id → chore_schedules(id) ON DELETE CASCADE
	// （迁移 039），删 chore 会级联删掉完成与附件；而 chores.id（INTEGER PRIMARY KEY）
	// 不可置 NULL。因此生产 schema 下 COALESCE(c.subject,'') 的 NULL 分支不可达，
	// 残留的 chore 4 仍按 history 精确匹配。

	ids := func(t *testing.T, subject string) []int64 {
		t.Helper()
		items, err := st.ListMediaItems(ctx, subject, "", "", "", 0, 0, 100)
		if err != nil {
			t.Fatalf("ListMediaItems(%q): %v", subject, err)
		}
		out := make([]int64, 0, len(items))
		for _, it := range items {
			out = append(out, it.AttachmentID)
		}
		return out
	}
	eq := func(t *testing.T, got, want []int64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		set := map[int64]bool{}
		for _, g := range got {
			set[g] = true
		}
		for _, w := range want {
			if !set[w] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}

	// '' = 全部（软删的 5 不可见）。此前 '' 与未分类同值的回归即在此暴露。
	eq(t, ids(t, ""), []int64{1, 2, 3, 4})
	// '__unclassified__' = 仅 COALESCE 为空的行：只有空串学科 3，不含 math/english/history。
	eq(t, ids(t, "__unclassified__"), []int64{3})
	// 普通学科精确匹配。
	eq(t, ids(t, "math"), []int64{1})
	eq(t, ids(t, "english"), []int64{2})
	eq(t, ids(t, "history"), []int64{4})
	// 不存在的学科 → 空集，不回退成全部，也不混入未分类桶。
	eq(t, ids(t, "astronomy"), nil)

	// stats：学科维度不随 selected subject 回退，空串桶以 subject='' 呈现，
	// 软删不计。
	subjects, classes, total, err := st.MediaLibraryStats(ctx, "", "", "", 0)
	if err != nil {
		t.Fatalf("MediaLibraryStats: %v", err)
	}
	if total != 4 {
		t.Fatalf("stats total = %d, want 4 (soft-deleted excluded)", total)
	}
	gotStats := map[string]int{}
	for _, st := range subjects {
		if st.Subject == "__unclassified__" {
			t.Fatalf("stats must key the unclassified bucket as \"\", got sentinel")
		}
		gotStats[st.Subject] = st.Count
	}
	wantStats := map[string]int{"math": 1, "english": 1, "history": 1, "": 1}
	if len(gotStats) != len(wantStats) {
		t.Fatalf("stats subjects = %v, want %v", gotStats, wantStats)
	}
	for k, v := range wantStats {
		if gotStats[k] != v {
			t.Fatalf("stats[%q] = %d, want %d (all=%v)", k, gotStats[k], v, gotStats)
		}
	}
	if len(classes) != 3 {
		t.Fatalf("stats classes = %v, want image/audio/video", classes)
	}
}
