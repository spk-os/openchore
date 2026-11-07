package api_test

// HTTP 层回归（卡 TX-DYNAMIC-SUBJECTS-TESTER）：/api/admin/media 的学科筛选
// 编码必须原样透传给 store——'' = 全部、'__unclassified__' = 仅未分类、
// 普通学科精确匹配；stats 不受学科筛选影响（chip 计数全程可见），且学科
// 为空时 stats 以 subject='' 作为桶 key（不是 '__unclassified__'）。

import (
	"net/http"
	"testing"
)

func TestMediaLibrarySubjectEncodingHTTP(t *testing.T) {
	e := setupTest(t)
	_, _, _, _ = mediaFixture(t, e)

	// 说明：本响应里 total 是 class 维度的合计（stats 口径，不随 subject 变化，
	// 已由既有 TestMediaLibraryFilters 断言），所以这里一律用 len(Items) 断言
	// subject 过滤。'' （缺省/显式空）= 全部：math×2 + english×1；dishes 的 pdf
	// 在夹具里被软删，不进结果。
	all := listMedia(t, e, "")
	if len(all.Items) != 3 {
		t.Fatalf("no subject: %d items, want 3", len(all.Items))
	}
	explicitEmpty := listMedia(t, e, "?subject=")
	if len(explicitEmpty.Items) != len(all.Items) {
		t.Fatalf("subject= must equal 'all': %d items, want %d", len(explicitEmpty.Items), len(all.Items))
	}

	// '__unclassified__' 只回未分类行，不把它误当全部。
	unclassified := listMedia(t, e, "?subject=__unclassified__")
	if len(unclassified.Items) != 0 {
		t.Fatalf("subject=__unclassified__: expected no live unclassified rows, got %+v", unclassified.Items)
	}

	// 普通学科精确匹配。
	math := listMedia(t, e, "?subject=math")
	if len(math.Items) != 2 {
		t.Fatalf("subject=math: expected 2 items, got %d", len(math.Items))
	}
	for _, it := range math.Items {
		if it.Subject != "math" {
			t.Fatalf("subject=math returned %q", it.Subject)
		}
	}
	unknown := listMedia(t, e, "?subject=astronomy")
	if len(unknown.Items) != 0 {
		t.Fatalf("subject=astronomy: expected no items, got %d", len(unknown.Items))
	}

	// stats 不回退：任何学科筛选下 subjects 都不随 selected subject 变化，
	// 空桶 key 是 ''（前端用它给「未分类」chip 计数）。
	wantCounts := map[string]int{"math": 2, "english": 1}
	for _, q := range []string{"", "?subject=math", "?subject=__unclassified__", "?subject=astronomy"} {
		resp := listMedia(t, e, q)
		got := map[string]int{}
		for _, s := range resp.Subjects {
			if s.Subject == "__unclassified__" {
				t.Fatalf("%s: stats keyed the unclassified bucket with the sentinel", q)
			}
			got[s.Subject] = s.Count
		}
		for k, v := range wantCounts {
			if got[k] != v {
				t.Fatalf("%s: stats[%q] = %d, want %d (all=%v)", q, k, got[k], v, got)
			}
		}
	}

	// 未分类桶有活跃行时：stats 里出现 subject=""，而不是 sentinel；并且
	// '__unclassified__' 过滤能取到它。Dishes 的附件落在一张无学科的 chore 上。
	e.mustExec(t, `INSERT INTO chores (id, title, category, subject, points_value, created_by) VALUES (10, 'Dishes', 'core', '', 5, 1)`)
	e.mustExec(t, `INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES (10, 10, 2, 1)`)
	e.mustExec(t, `INSERT INTO chore_completions (id, chore_schedule_id, completed_by, status, completion_date) VALUES (10, 10, 2, 'pending', '2026-10-13')`)
	e.mustExec(t, `INSERT INTO completion_attachments
		(id, completion_id, file_path, display_name, mime_type, size_bytes, mime_class, sha256, sort_order)
		VALUES (10, 10, '/uploads/live.pdf', 'live.pdf', 'application/pdf', 50, 'pdf', 's10', 0)`)
	unclassified = listMedia(t, e, "?subject=__unclassified__")
	if len(unclassified.Items) != 1 || unclassified.Items[0].AttachmentID != 10 {
		t.Fatalf("after seeding, subject=__unclassified__: got %+v", unclassified.Items)
	}
	stats := listMedia(t, e, "?subject=math")
	emptyBucket := -1
	for _, s := range stats.Subjects {
		if s.Subject == "" {
			emptyBucket = s.Count
		}
	}
	if emptyBucket != 1 {
		t.Fatalf("stats must expose the unclassified bucket as subject=\"\" with count 1, got %d (%+v)", emptyBucket, stats.Subjects)
	}
	// 非管理员被拒（与既有 admin-gate 用例一致的 403）。
	e.expectStatus(t, "GET", "/api/admin/media?subject=__unclassified__", nil, childHeaders(2), http.StatusForbidden)
}
