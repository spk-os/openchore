package store_test

// 任务卡 TX-AI-KC-CANDIDATE-DAO-TESTER：paper_knowledge_candidates.go 的真实
// SQLite 行为验收。覆盖 SearchKnowledgeCandidates / ResolveRefTagIDs /
// MergeErrorItemKnowledgeRefs 三个 DAO。
//
// 全部走本包既有 helper setupStoreWithDB（真实迁移 + MaxOpenConns=1 单写者），
// 不 mock；每个子测试用独立 :memory: 库，fixture 直接写真实 knowledge_tags /
// error_items / error_item_tags / review_items / kc_mastery 表。
//
// 已知仓库中间态：internal/store/learning_restore.go 在当前工作树有未定义符号，
// 会阻塞整包编译；该阻塞不属于本 DAO，测试结果需在阻塞解除后复跑（见任务报告）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

const kcCandTimeout = 5 * time.Second

func kcCandCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), kcCandTimeout)
	t.Cleanup(cancel)
	return ctx
}

func kcCandStrPtr(s string) *string { return &s }

// kcCandTagSpec 控制 knowledge_tags 的全部行为相关列；ExternalRef 为 nil 表示
// SQL NULL，为 "" 表示空串，为 "   " 表示仅空白的非空串（DAO 必须在 Go 侧 trim
// 后剔除）。
type kcCandTagSpec struct {
	ID          int64 // 0 = 交给 AUTOINCREMENT
	Name        string
	Subject     string
	IsSystem    int
	ExternalRef *string
	SortOrder   int
}

func kcCandInsertTag(t *testing.T, db *sql.DB, spec kcCandTagSpec) int64 {
	t.Helper()
	var ref any
	if spec.ExternalRef != nil {
		ref = *spec.ExternalRef
	}
	var res sql.Result
	var err error
	if spec.ID > 0 {
		res, err = db.ExecContext(context.Background(),
			`INSERT INTO knowledge_tags (id, name, subject, sort_order, is_system, external_ref)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			spec.ID, spec.Name, spec.Subject, spec.SortOrder, spec.IsSystem, ref)
	} else {
		res, err = db.ExecContext(context.Background(),
			`INSERT INTO knowledge_tags (name, subject, sort_order, is_system, external_ref)
			 VALUES (?, ?, ?, ?, ?)`,
			spec.Name, spec.Subject, spec.SortOrder, spec.IsSystem, ref)
	}
	if err != nil {
		t.Fatalf("insert knowledge_tags %+v: %v", spec, err)
	}
	if spec.ID > 0 {
		return spec.ID
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("knowledge_tags LastInsertId: %v", err)
	}
	return id
}

func kcCandExec(t *testing.T, db *sql.DB, query string, args ...any) sql.Result {
	t.Helper()
	res, err := db.ExecContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
	return res
}

func kcCandCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

// kcCandSnapshot 把一行或多行拍平成 "列名=值" 字符串，用于断言"零写入"：
// 两次同一查询的快照必须逐项相等。%v 对 []byte/time.Time 都是稳定输出。
func kcCandSnapshot(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("snapshot %q: %v", query, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("snapshot columns %q: %v", query, err)
	}
	out := make([]string, 0)
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("snapshot scan %q: %v", query, err)
		}
		for i, v := range vals {
			out = append(out, fmt.Sprintf("%s=%v", cols[i], v))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("snapshot rows %q: %v", query, err)
	}
	return out
}

func kcCandLinkIDs(t *testing.T, db *sql.DB, itemID int64) []int64 {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT tag_id FROM error_item_tags WHERE error_item_id = ? ORDER BY tag_id`, itemID)
	if err != nil {
		t.Fatalf("link ids item %d: %v", itemID, err)
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("link id scan: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("link ids rows: %v", err)
	}
	return ids
}

func kcCandAssertLinks(t *testing.T, db *sql.DB, itemID int64, want ...int64) {
	t.Helper()
	wantSorted := append([]int64(nil), want...)
	slices.Sort(wantSorted)
	if got := kcCandLinkIDs(t, db, itemID); !slices.Equal(got, wantSorted) {
		t.Fatalf("item %d links = %v, want %v", itemID, got, wantSorted)
	}
}

func kcCandCandidateRefs(rows []store.KnowledgeCandidateRow) []string {
	refs := make([]string, 0, len(rows))
	for _, r := range rows {
		refs = append(refs, r.ExternalRef)
	}
	return refs
}

func kcCandAssertCandidates(t *testing.T, got []store.KnowledgeCandidateRow, subject string, wantRefs ...string) {
	t.Helper()
	if got == nil {
		t.Fatalf("candidates = nil, want non-nil slice")
	}
	if refs := kcCandCandidateRefs(got); !slices.Equal(refs, wantRefs) {
		t.Fatalf("candidate refs = %v, want %v", refs, wantRefs)
	}
	for _, r := range got {
		if r.ID <= 0 {
			t.Fatalf("candidate %+v has non-positive id", r)
		}
		if r.Subject != subject {
			t.Fatalf("candidate %+v subject = %q, want %q", r, r.Subject, subject)
		}
		if strings.TrimSpace(r.ExternalRef) == "" {
			t.Fatalf("candidate %+v has empty/blank external_ref", r)
		}
		if r.ExternalRef != strings.TrimSpace(r.ExternalRef) {
			t.Fatalf("candidate %+v external_ref is not trimmed", r)
		}
	}
}

// kcCandNewItem 用真实 DAO 建一条错题，并写入可被 Merge 误改的字段
// （mastery_level / next_review_at / question_text / updated_at 等）。
func kcCandNewItem(t *testing.T, s *store.Store, ctx context.Context, subject string) (itemID, userID int64) {
	t.Helper()
	u := createTestUser(t, s, "kc-cand-kid", "child")
	next := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)
	item := &model.ErrorItem{
		UserID:           u.ID,
		Subject:          subject,
		OriginalImageURL: "https://example.test/kc-cand.png",
		OCRText:          "ocr-original",
		QuestionText:     "题干原文",
		AnswerText:       "答案原文",
		WrongAnswerText:  "错答原文",
		Analysis:         "解析原文",
		MistakeAnalysis:  "错因原文",
		MistakeStatus:    model.MistakeUnknown,
		ErrorType:        "concept",
		Difficulty:       "hard",
		AIConfidence:     0.91,
		PaperLevel:       "A",
		GradeSemester:    "七上",
		Source:           "期中考试",
		MasteryLevel:     2,
		NextReviewAt:     &next,
	}
	if err := s.CreateErrorItem(ctx, item); err != nil {
		t.Fatalf("CreateErrorItem(subject=%q): %v", subject, err)
	}
	return item.ID, u.ID
}

func kcCandSeedReviewCard(t *testing.T, db *sql.DB, userID, itemID int64) {
	t.Helper()
	kcCandExec(t, db,
		`INSERT INTO review_items
		 (user_id, source_type, source_id, front_text, back_text, subject, due, stability, difficulty,
		  scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended)
		 VALUES (?, 'error_item', ?, '正面原文', '背面原文', 'math', '2032-01-02 03:04:05',
		         12.5, 7.25, 30, 4, 1, 'Review', '2031-12-01 00:00:00', 0, 0)`,
		userID, itemID)
}

func kcCandSeedMastery(t *testing.T, db *sql.DB, userID, tagID int64) {
	t.Helper()
	kcCandExec(t, db,
		`INSERT INTO kc_mastery (user_id, tag_id, correct_count, total_count, mastery, low_confidence, last_event_at)
		 VALUES (?, ?, 9, 12, 0.75, 0, '2031-01-02 03:04:05')`,
		userID, tagID)
}

func kcCandItemAndStateSnapshot(t *testing.T, db *sql.DB, itemID int64) (item, cards, mastery []string) {
	t.Helper()
	return kcCandSnapshot(t, db, `SELECT * FROM error_items WHERE id = ?`, itemID),
		kcCandSnapshot(t, db,
			`SELECT * FROM review_items WHERE source_type = 'error_item' AND source_id = ?`, itemID),
		kcCandSnapshot(t, db,
			`SELECT * FROM kc_mastery ORDER BY user_id, tag_id`)
}

func kcCandTagTableSnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return kcCandSnapshot(t, db,
		`SELECT id, name, subject, parent_id, sort_order, is_system, external_ref
		 FROM knowledge_tags ORDER BY id`)
}

func kcCandAssertUnchanged(t *testing.T, before, after []string, what string) {
	t.Helper()
	if !slices.Equal(before, after) {
		t.Fatalf("%s changed: before=%v after=%v", what, before, after)
	}
}

// ===== SearchKnowledgeCandidates =====

func TestSearchKnowledgeCandidates(t *testing.T) {
	t.Run("EmptySubjectReturnsNonNilEmptySlice", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "库内行", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("REF_X"), SortOrder: 1})

		for _, subject := range []string{"", "   ", "\t\n", " \t "} {
			got, err := s.SearchKnowledgeCandidates(ctx, subject, "REF_X", []string{"REF_X", "REF_X"}, 0)
			if err != nil {
				t.Fatalf("subject=%q: err = %v, want nil", subject, err)
			}
			if got == nil {
				t.Fatalf("subject=%q: candidates = nil, want non-nil empty slice", subject)
			}
			if len(got) != 0 {
				t.Fatalf("subject=%q: len = %d, want 0", subject, len(got))
			}
		}

		// 合法 subject 但无候选：同样必须非 nil。
		got, err := s.SearchKnowledgeCandidates(ctx, "chinese", "", nil, 0)
		if err != nil {
			t.Fatalf("chinese no-candidate: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("chinese no-candidate = %#v, want non-nil empty", got)
		}
	})

	t.Run("OnlySameSubjectSystemNonEmptyRef", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)

		ok1 := kcCandInsertTag(t, db, kcCandTagSpec{Name: "甲", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("REF_M_OK1"), SortOrder: 10})
		ok2 := kcCandInsertTag(t, db, kcCandTagSpec{Name: "乙", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("REF_M_OK2"), SortOrder: 20})
		emptyName := kcCandInsertTag(t, db, kcCandTagSpec{Name: "", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("REF_M_EMPTYNAME"), SortOrder: 30})
		_ = ok1
		_ = ok2
		_ = emptyName
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "家长自建", Subject: "math", IsSystem: 0,
			ExternalRef: kcCandStrPtr("REF_M_USER"), SortOrder: 5})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "NULL引用", Subject: "math", IsSystem: 1,
			ExternalRef: nil, SortOrder: 1})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "空引用", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr(""), SortOrder: 2})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "空白引用", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("   "), SortOrder: 3})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "语文行", Subject: "chinese", IsSystem: 1,
			ExternalRef: kcCandStrPtr("REF_ZH"), SortOrder: 6})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "大写学科行", Subject: "Math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("REF_CAP"), SortOrder: 7})

		got, err := s.SearchKnowledgeCandidates(ctx, "math", "", nil, 0)
		if err != nil {
			t.Fatalf("search math: %v", err)
		}
		kcCandAssertCandidates(t, got, "math", "REF_M_OK1", "REF_M_OK2", "REF_M_EMPTYNAME")
		// 结果行字段完整：无 NULL/空 ref、名字字段即使是空串也能安全 Scan。
		for _, r := range got {
			if r.Name != "" && strings.TrimSpace(r.Name) == "" {
				t.Fatalf("candidate %+v name is blank", r)
			}
		}

		// subject trim 后等价。
		got, err = s.SearchKnowledgeCandidates(ctx, " math \t", "", nil, 0)
		if err != nil {
			t.Fatalf("search trimmed math: %v", err)
		}
		kcCandAssertCandidates(t, got, "math", "REF_M_OK1", "REF_M_OK2", "REF_M_EMPTYNAME")

		// 同学科大小写精确：Math 只能看到自己的行；MATH 无行。
		got, err = s.SearchKnowledgeCandidates(ctx, "Math", "", nil, 0)
		if err != nil {
			t.Fatalf("search Math: %v", err)
		}
		kcCandAssertCandidates(t, got, "Math", "REF_CAP")

		got, err = s.SearchKnowledgeCandidates(ctx, "MATH", "", nil, 0)
		if err != nil {
			t.Fatalf("search MATH: %v", err)
		}
		if got == nil || len(got) != 0 {
			t.Fatalf("search MATH = %#v, want non-nil empty", got)
		}

		got, err = s.SearchKnowledgeCandidates(ctx, "chinese", "", nil, 0)
		if err != nil {
			t.Fatalf("search chinese: %v", err)
		}
		kcCandAssertCandidates(t, got, "chinese", "REF_ZH")

		// Search 是只读：knowledge_tags 内容前后不变。
		before := kcCandTagTableSnapshot(t, db)
		if _, err := s.SearchKnowledgeCandidates(ctx, "math", "REF_M_OK1", []string{"REF_M_OK1"}, 0); err != nil {
			t.Fatalf("second search: %v", err)
		}
		kcCandAssertUnchanged(t, before, kcCandTagTableSnapshot(t, db), "knowledge_tags")
	})

	t.Run("SeedRefsOrderDedupTiersAndDeterminism", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)

		// 档位 0：seed 精确命中（顺序/去重/trim），sort_order 故意很靠后。
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "Seed A", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_A"), SortOrder: 90})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "Seed B", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_B"), SortOrder: 80})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "Seed C", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_C"), SortOrder: 70})
		// 档位 1：题干精确包含 external_ref。
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "RefInText", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_D"), SortOrder: 60})
		// 档位 2：题干精确包含名称。
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "ExactName", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_E"), SortOrder: 50})
		// 档位 3：关键词相关度，score 2 的两行按 sort_order，score 1 的一行在后。
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "Equation Quadratic", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_K"), SortOrder: 20})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "Quadratic Equation", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_F"), SortOrder: 40})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "Quadratic Function", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_G"), SortOrder: 30})
		// 档位 4：按 (sort_order, id) 收尾；REF_I/REF_J 同 sort_order 用 id 决定。
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "Filler Late", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_H"), SortOrder: 100})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "Filler Early", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_I"), SortOrder: 5})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "Filler Same Sort", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_J"), SortOrder: 5})

		questionText := "REF_D ExactName quadratic equation"
		seedRefs := []string{"REF_C", "REF_A", "REF_C", "  REF_B  ", "", "ref_a"}
		want := []string{"REF_C", "REF_A", "REF_B", "REF_D", "REF_E", "REF_K", "REF_F", "REF_G", "REF_I", "REF_J", "REF_H"}

		for i := 0; i < 3; i++ {
			got, err := s.SearchKnowledgeCandidates(ctx, "math", questionText, seedRefs, 0)
			if err != nil {
				t.Fatalf("run %d: %v", i, err)
			}
			kcCandAssertCandidates(t, got, "math", want...)
		}

		// seed 大小写精确：小写 "ref_a" 不能把 REF_A 抬进第 0 档；无文本命中时
		// 全部落第 4 档，按 (sort_order, id)：REF_I/REF_J(5) → REF_K(20) → ...
		got, err := s.SearchKnowledgeCandidates(ctx, "math", "", []string{"ref_a"}, 0)
		if err != nil {
			t.Fatalf("lowercase seed: %v", err)
		}
		kcCandAssertCandidates(t, got, "math",
			"REF_I", "REF_J", "REF_K", "REF_G", "REF_F", "REF_E", "REF_D", "REF_C", "REF_B", "REF_A", "REF_H")
	})

	t.Run("RefHitBeatsNameHitRegardlessOfSortOrder", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)
		// R1 的 ref 在题干里（档位 1），R2 的名称在题干里（档位 2）；
		// 即使 R1 的 sort_order 远大于 R2，档位优先仍让 R1 置前。
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "RefInText", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_D"), SortOrder: 99})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "ExactName", Subject: "math", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_OTHER"), SortOrder: 1})

		got, err := s.SearchKnowledgeCandidates(ctx, "math", "REF_D ExactName", nil, 0)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		kcCandAssertCandidates(t, got, "math", "REF_D", "REF_OTHER")
	})

	t.Run("LimitDefaultsAndHardCapsAt80", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)

		// 干扰行：非系统 / 异学科 / NULL ref，不应计入 95 个候选。
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "干扰-非系统", Subject: "math", IsSystem: 0, ExternalRef: kcCandStrPtr("REF_NOISE_USER"), SortOrder: -5})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "干扰-异学科", Subject: "chinese", IsSystem: 1, ExternalRef: kcCandStrPtr("REF_NOISE_ZH"), SortOrder: -4})
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "干扰-NULL", Subject: "math", IsSystem: 1, ExternalRef: nil, SortOrder: -3})

		wantAll := make([]string, 0, 95)
		for i := 0; i < 95; i++ {
			ref := fmt.Sprintf("L-%03d", i)
			kcCandInsertTag(t, db, kcCandTagSpec{Name: fmt.Sprintf("Liner %03d", i), Subject: "math", IsSystem: 1,
				ExternalRef: kcCandStrPtr(ref), SortOrder: i})
			wantAll = append(wantAll, ref)
		}
		want80 := wantAll[:80]

		for _, limit := range []int{0, -1, -999, 81, 100, 1000} {
			got, err := s.SearchKnowledgeCandidates(ctx, "math", "", nil, limit)
			if err != nil {
				t.Fatalf("limit=%d: %v", limit, err)
			}
			if len(got) != 80 {
				t.Fatalf("limit=%d: len = %d, want hard cap 80", limit, len(got))
			}
			kcCandAssertCandidates(t, got, "math", want80...)
		}

		for _, tc := range []struct {
			limit int
			want  int
		}{{1, 1}, {10, 10}, {79, 79}, {80, 80}} {
			got, err := s.SearchKnowledgeCandidates(ctx, "math", "", nil, tc.limit)
			if err != nil {
				t.Fatalf("limit=%d: %v", tc.limit, err)
			}
			if len(got) != tc.want {
				t.Fatalf("limit=%d: len = %d, want %d", tc.limit, len(got), tc.want)
			}
			kcCandAssertCandidates(t, got, "math", wantAll[:tc.want]...)
		}
	})
}

// ===== ResolveRefTagIDs =====

func TestResolveRefTagIDs(t *testing.T) {
	t.Run("EmptyInputsReturnNonNilEmptyMap", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "存在行", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("RES_A"), SortOrder: 1})

		cases := []struct {
			name    string
			subject string
			refs    []string
		}{
			{"empty subject", "", []string{"RES_A"}},
			{"blank subject", "   ", []string{"RES_A"}},
			{"nil refs", "math", nil},
			{"empty refs", "math", []string{}},
			{"blank refs", "math", []string{"", "   ", "\t", "  "}},
			{"case-mismatch subject", "Math", []string{"RES_A"}},
		}
		for _, tc := range cases {
			m, err := s.ResolveRefTagIDs(ctx, tc.subject, tc.refs)
			if err != nil {
				t.Fatalf("%s: err = %v, want nil", tc.name, err)
			}
			if m == nil {
				t.Fatalf("%s: map = nil, want non-nil empty map", tc.name)
			}
			if len(m) != 0 {
				t.Fatalf("%s: map = %v, want empty", tc.name, m)
			}
		}

		before := kcCandTagTableSnapshot(t, db)
		if _, err := s.ResolveRefTagIDs(ctx, "math", []string{"", "RES_A"}); err != nil {
			t.Fatalf("prod call: %v", err)
		}
		kcCandAssertUnchanged(t, before, kcCandTagTableSnapshot(t, db), "knowledge_tags")
	})

	t.Run("ExactCaseSameSubjectUnknownAndNoInsert", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)

		up := kcCandInsertTag(t, db, kcCandTagSpec{Name: "大写", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("RES_UP"), SortOrder: 1})
		low := kcCandInsertTag(t, db, kcCandTagSpec{Name: "小写", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("res_low"), SortOrder: 2})
		zh := kcCandInsertTag(t, db, kcCandTagSpec{Name: "语文", Subject: "chinese", IsSystem: 1,
			ExternalRef: kcCandStrPtr("RES_ZH"), SortOrder: 3})

		before := kcCandTagTableSnapshot(t, db)

		// 输入含重复/空白/大小写变体：只解析逐字节精确、同学科的两个 ref。
		// 键是 trim 后的输入 ref（DAO 契约）。
		m, err := s.ResolveRefTagIDs(ctx, "math",
			[]string{"RES_UP", "res_low", "RES_UP", "  RES_UP  ", "res_low", "", "   "})
		if err != nil {
			t.Fatalf("resolve mixed: %v", err)
		}
		if len(m) != 2 {
			t.Fatalf("resolve mixed map = %v, want exactly 2 entries", m)
		}
		if m["RES_UP"] != up || m["res_low"] != low {
			t.Fatalf("resolve mixed map = %v, want RES_UP=%d res_low=%d", m, up, low)
		}

		// 严格大小写：只有逐字节相同的 ref 命中。
		m, err = s.ResolveRefTagIDs(ctx, "math", []string{"Res_Up", "RES_UP"})
		if err != nil {
			t.Fatalf("resolve case variant: %v", err)
		}
		if len(m) != 1 || m["RES_UP"] != up {
			t.Fatalf("resolve case variant = %v, want {RES_UP:%d}", m, up)
		}

		// 异学科：ref 存在于 chinese，用 math 解析必须为空；用 chinese 才命中。
		m, err = s.ResolveRefTagIDs(ctx, "math", []string{"RES_ZH"})
		if err != nil {
			t.Fatalf("resolve cross-subject: %v", err)
		}
		if len(m) != 0 {
			t.Fatalf("cross-subject math resolve = %v, want empty", m)
		}
		m, err = s.ResolveRefTagIDs(ctx, "chinese", []string{"RES_ZH"})
		if err != nil {
			t.Fatalf("resolve chinese: %v", err)
		}
		if len(m) != 1 || m["RES_ZH"] != zh {
			t.Fatalf("chinese resolve = %v, want {RES_ZH:%d}", m, zh)
		}

		// 学科大小写精确 + trim。
		if m, err = s.ResolveRefTagIDs(ctx, "Math", []string{"RES_UP"}); err != nil {
			t.Fatalf("resolve Math: %v", err)
		}
		if len(m) != 0 {
			t.Fatalf("resolve Math = %v, want empty", m)
		}
		if m, err = s.ResolveRefTagIDs(ctx, " math\t", []string{"RES_UP"}); err != nil {
			t.Fatalf("resolve trimmed math: %v", err)
		}
		if len(m) != 1 || m["RES_UP"] != up {
			t.Fatalf("resolve trimmed math = %v, want {RES_UP:%d}", m, up)
		}

		// 未知 ref 不解析、绝不 INSERT。
		if m, err = s.ResolveRefTagIDs(ctx, "math", []string{"RES_MISSING", "RES_UP"}); err != nil {
			t.Fatalf("resolve unknown: %v", err)
		}
		if len(m) != 1 || m["RES_UP"] != up {
			t.Fatalf("resolve unknown = %v, want only RES_UP", m)
		}
		if missing := kcCandCount(t, db, `SELECT COUNT(*) FROM knowledge_tags WHERE external_ref = 'RES_MISSING'`); missing != 0 {
			t.Fatalf("unknown ref created %d knowledge_tags rows", missing)
		}
		kcCandAssertUnchanged(t, before, kcCandTagTableSnapshot(t, db), "knowledge_tags")
	})

	t.Run("NumericRefNeverMeansLocalRowID", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)

		// 先造出 id=123 的本机知识点行；它的 external_ref 不是 "123"。
		for i := 0; i < 122; i++ {
			kcCandInsertTag(t, db, kcCandTagSpec{
				Name: fmt.Sprintf("Fill %03d", i), Subject: "math", IsSystem: 1,
				ExternalRef: kcCandStrPtr(fmt.Sprintf("RES_F%03d", i)), SortOrder: i,
			})
		}
		at123 := kcCandInsertTag(t, db, kcCandTagSpec{Name: "At123", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("RES_AT123"), SortOrder: 200})
		if at123 != 123 {
			t.Fatalf("fixture tag id = %d, want 123", at123)
		}

		// 没有任何知识点 external_ref 恰为 "123"：数字串绝不能按主键解释。
		m, err := s.ResolveRefTagIDs(ctx, "math", []string{"123", "122", "1", "123"})
		if err != nil {
			t.Fatalf("resolve numeric: %v", err)
		}
		if len(m) != 0 {
			t.Fatalf("numeric refs resolved to %v, want empty (ids are not refs)", m)
		}

		// 契约边界：只有当某行 external_ref 恰为 "123" 时才命中该行，且值是
		// 该行真实 id（124），绝不是本机 id=123。
		literal := kcCandInsertTag(t, db, kcCandTagSpec{Name: "Literal123", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("123"), SortOrder: 201})
		if literal != 124 {
			t.Fatalf("literal tag id = %d, want 124", literal)
		}
		m, err = s.ResolveRefTagIDs(ctx, "math", []string{"123"})
		if err != nil {
			t.Fatalf("resolve literal numeric ref: %v", err)
		}
		if len(m) != 1 || m["123"] != 124 {
			t.Fatalf("literal numeric ref = %v, want {123:124}", m)
		}
		if _, ok := m["123"]; !ok || m["123"] == 123 {
			t.Fatalf("literal numeric ref mapped to local row id 123: %v", m)
		}

		// 前导零不是数字等价："00123" 不命中。
		m, err = s.ResolveRefTagIDs(ctx, "math", []string{"00123"})
		if err != nil {
			t.Fatalf("resolve padded numeric: %v", err)
		}
		if len(m) != 0 {
			t.Fatalf("padded numeric ref = %v, want empty", m)
		}

		// 真实 ref 与真 id 的映射独立正确。
		m, err = s.ResolveRefTagIDs(ctx, "math", []string{"RES_AT123"})
		if err != nil {
			t.Fatalf("resolve real ref: %v", err)
		}
		if len(m) != 1 || m["RES_AT123"] != 123 {
			t.Fatalf("real ref = %v, want {RES_AT123:123}", m)
		}

		// Resolve 读多写零：调用前后行数不变。
		if n := kcCandCount(t, db, `SELECT COUNT(*) FROM knowledge_tags`); n != 124 {
			t.Fatalf("knowledge_tags rows = %d, want 124 (no INSERT by resolve)", n)
		}
	})
}

// ===== MergeErrorItemKnowledgeRefs =====

func TestMergeErrorItemKnowledgeRefs(t *testing.T) {
	t.Run("AdditiveUnionPreservesParentTagsAndItemState", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)
		itemID, userID := kcCandNewItem(t, s, ctx, "math")

		// 家长已有标签（非系统、无 external_ref）与一个已挂的系统标签，必须保留。
		parentTag, err := s.FindOrCreateKnowledgeTag(ctx, "家长已有标签", "math", nil)
		if err != nil {
			t.Fatalf("FindOrCreateKnowledgeTag: %v", err)
		}
		if err := s.AttachErrorItemTag(ctx, itemID, parentTag); err != nil {
			t.Fatalf("attach parent tag: %v", err)
		}

		tagA := kcCandInsertTag(t, db, kcCandTagSpec{Name: "KC A", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_A"), SortOrder: 10})
		tagB := kcCandInsertTag(t, db, kcCandTagSpec{Name: "KC B", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_B"), SortOrder: 20})
		tagC := kcCandInsertTag(t, db, kcCandTagSpec{Name: "KC C", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_C"), SortOrder: 30})
		if err := s.AttachErrorItemTag(ctx, itemID, tagA); err != nil {
			t.Fatalf("attach tag A: %v", err)
		}

		// 写一条真实 FSRS 卡与一条掌握度物化行，Merge 不得重置它们。
		kcCandSeedReviewCard(t, db, userID, itemID)
		kcCandSeedMastery(t, db, userID, parentTag)

		itemBefore, cardsBefore, masteryBefore := kcCandItemAndStateSnapshot(t, db, itemID)
		tagsBefore := kcCandTagTableSnapshot(t, db)

		resolved, attached, err := s.MergeErrorItemKnowledgeRefs(ctx, itemID, "math",
			[]string{"MR_B", "MR_C", "MR_A", "  MR_B  "})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if resolved != 3 || attached != 2 {
			t.Fatalf("merge resolved=%d attached=%d, want 3/2", resolved, attached)
		}

		// additive union：家长标签 + 原有链接 + 新增链接全部在，没有被替换。
		assertBothReadPaths(t, s, ctx, itemID, parentTag, tagA, tagB, tagC)
		kcCandAssertLinks(t, db, itemID, parentTag, tagA, tagB, tagC)

		// 错题字段、FSRS 卡、掌握度物化、知识点表全部零改动。
		itemAfter, cardsAfter, masteryAfter := kcCandItemAndStateSnapshot(t, db, itemID)
		kcCandAssertUnchanged(t, itemBefore, itemAfter, "error_items")
		kcCandAssertUnchanged(t, cardsBefore, cardsAfter, "review_items (FSRS)")
		kcCandAssertUnchanged(t, masteryBefore, masteryAfter, "kc_mastery")
		kcCandAssertUnchanged(t, tagsBefore, kcCandTagTableSnapshot(t, db), "knowledge_tags")

		// 完全重复的调用：resolved 仍计已解析的不同 ref，attached 必须为 0。
		resolved, attached, err = s.MergeErrorItemKnowledgeRefs(ctx, itemID, "math",
			[]string{"MR_A", "MR_B", "MR_C", "MR_A"})
		if err != nil {
			t.Fatalf("second merge: %v", err)
		}
		if resolved != 3 || attached != 0 {
			t.Fatalf("second merge resolved=%d attached=%d, want 3/0", resolved, attached)
		}
		assertBothReadPaths(t, s, ctx, itemID, parentTag, tagA, tagB, tagC)
	})

	t.Run("DuplicateRefsAndExistingLinksIdempotent", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)
		itemID, _ := kcCandNewItem(t, s, ctx, "math")
		tagA := kcCandInsertTag(t, db, kcCandTagSpec{Name: "幂等 A", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_A"), SortOrder: 1})
		tagB := kcCandInsertTag(t, db, kcCandTagSpec{Name: "幂等 B", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_B"), SortOrder: 2})
		tagC := kcCandInsertTag(t, db, kcCandTagSpec{Name: "幂等 C", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_C"), SortOrder: 3})
		if err := s.AttachErrorItemTag(ctx, itemID, tagA); err != nil {
			t.Fatalf("attach A: %v", err)
		}
		tagsBefore := kcCandTagTableSnapshot(t, db)

		steps := []struct {
			refs         []string
			wantResolved int
			wantAttached int
		}{
			{[]string{"MR_A", "MR_B", "MR_A", "MR_A", "  MR_B  "}, 2, 1},
			{[]string{"MR_A", "MR_B", "MR_A", "  MR_B  "}, 2, 0},
			{[]string{"MR_C"}, 1, 1},
			{[]string{"MR_A", "MR_B", "MR_C", "MR_A", "MR_B"}, 3, 0},
		}
		for i, step := range steps {
			resolved, attached, err := s.MergeErrorItemKnowledgeRefs(ctx, itemID, "math", step.refs)
			if err != nil {
				t.Fatalf("step %d: %v", i, err)
			}
			if resolved != step.wantResolved || attached != step.wantAttached {
				t.Fatalf("step %d resolved=%d attached=%d, want %d/%d",
					i, resolved, attached, step.wantResolved, step.wantAttached)
			}
		}

		assertBothReadPaths(t, s, ctx, itemID, tagA, tagB, tagC)
		kcCandAssertLinks(t, db, itemID, tagA, tagB, tagC)
		kcCandAssertUnchanged(t, tagsBefore, kcCandTagTableSnapshot(t, db), "knowledge_tags")
	})

	t.Run("IgnoresUnknownCrossSubjectCaseMismatchWithoutCreatingKC", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)
		itemID, _ := kcCandNewItem(t, s, ctx, "math")
		tagA := kcCandInsertTag(t, db, kcCandTagSpec{Name: "有效 A", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_A"), SortOrder: 1})
		tagB := kcCandInsertTag(t, db, kcCandTagSpec{Name: "有效 B", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_B"), SortOrder: 2})
		zhTag := kcCandInsertTag(t, db, kcCandTagSpec{Name: "语文知识点", Subject: "chinese", IsSystem: 1,
			ExternalRef: kcCandStrPtr("RES_ZH"), SortOrder: 3})

		itemBefore, cardsBefore, masteryBefore := kcCandItemAndStateSnapshot(t, db, itemID)
		tagsBefore := kcCandTagTableSnapshot(t, db)
		linksBefore := kcCandSnapshot(t, db,
			`SELECT error_item_id, tag_id FROM error_item_tags ORDER BY error_item_id, tag_id`)

		resolved, attached, err := s.MergeErrorItemKnowledgeRefs(ctx, itemID, "math",
			[]string{"MR_A", "MR_B", "cnm:missing", "RES_ZH", "mr_a", "  MR_B  ", "", "   "})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if resolved != 2 || attached != 2 {
			t.Fatalf("resolved=%d attached=%d, want 2/2 (invalid refs ignored)", resolved, attached)
		}

		assertBothReadPaths(t, s, ctx, itemID, tagA, tagB)
		if ids := kcCandLinkIDs(t, db, itemID); slices.Contains(ids, zhTag) {
			t.Fatalf("cross-subject tag %d was linked: %v", zhTag, ids)
		}

		// 绝不创建知识点：表内容与行数均不变，被忽略的 ref 不存在。
		kcCandAssertUnchanged(t, tagsBefore, kcCandTagTableSnapshot(t, db), "knowledge_tags")
		if n := kcCandCount(t, db, `SELECT COUNT(*) FROM knowledge_tags WHERE external_ref IN ('cnm:missing','mr_a')`); n != 0 {
			t.Fatalf("ignored refs created %d knowledge_tags rows", n)
		}

		itemAfter, cardsAfter, masteryAfter := kcCandItemAndStateSnapshot(t, db, itemID)
		kcCandAssertUnchanged(t, itemBefore, itemAfter, "error_items")
		kcCandAssertUnchanged(t, cardsBefore, cardsAfter, "review_items")
		kcCandAssertUnchanged(t, masteryBefore, masteryAfter, "kc_mastery")
		_ = linksBefore
	})

	t.Run("MissingItemReturnsNotFoundAndZeroWrites", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)
		itemID, _ := kcCandNewItem(t, s, ctx, "math")
		tagA := kcCandInsertTag(t, db, kcCandTagSpec{Name: "缺失测试 A", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_A"), SortOrder: 1})

		itemBefore, cardsBefore, masteryBefore := kcCandItemAndStateSnapshot(t, db, itemID)
		tagsBefore := kcCandTagTableSnapshot(t, db)
		linksBefore := kcCandSnapshot(t, db,
			`SELECT error_item_id, tag_id FROM error_item_tags ORDER BY error_item_id, tag_id`)

		missingID := itemID + 9999
		resolved, attached, err := s.MergeErrorItemKnowledgeRefs(ctx, missingID, "math", []string{"MR_A"})
		if !errors.Is(err, store.ErrErrorItemNotFound) {
			t.Fatalf("missing item err = %v, want ErrErrorItemNotFound", err)
		}
		if resolved != 0 || attached != 0 {
			t.Fatalf("missing item resolved=%d attached=%d, want 0/0", resolved, attached)
		}

		itemAfter, cardsAfter, masteryAfter := kcCandItemAndStateSnapshot(t, db, itemID)
		kcCandAssertUnchanged(t, itemBefore, itemAfter, "error_items")
		kcCandAssertUnchanged(t, cardsBefore, cardsAfter, "review_items")
		kcCandAssertUnchanged(t, masteryBefore, masteryAfter, "kc_mastery")
		kcCandAssertUnchanged(t, tagsBefore, kcCandTagTableSnapshot(t, db), "knowledge_tags")
		kcCandAssertUnchanged(t, linksBefore, kcCandSnapshot(t, db,
			`SELECT error_item_id, tag_id FROM error_item_tags ORDER BY error_item_id, tag_id`), "error_item_tags")

		if tags, err := s.TagsForItem(ctx, missingID); err != nil || len(tags) != 0 {
			t.Fatalf("TagsForItem(missing) = %v, %v; want empty/nil", tags, err)
		}

		// 失败路径后连接池必须可用（deferred Rollback 生效）。
		resolved, attached, err = s.MergeErrorItemKnowledgeRefs(ctx, itemID, "math", []string{"MR_A"})
		if err != nil || resolved != 1 || attached != 1 {
			t.Fatalf("follow-up merge resolved=%d attached=%d err=%v, want 1/1 nil", resolved, attached, err)
		}
		kcCandAssertLinks(t, db, itemID, tagA)
	})

	t.Run("SubjectMismatchReturnsSentinelAndZeroWrites", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)
		itemID, _ := kcCandNewItem(t, s, ctx, "math")
		parentTag, err := s.FindOrCreateKnowledgeTag(ctx, "家长标签", "math", nil)
		if err != nil {
			t.Fatalf("FindOrCreateKnowledgeTag: %v", err)
		}
		if err := s.AttachErrorItemTag(ctx, itemID, parentTag); err != nil {
			t.Fatalf("attach parent tag: %v", err)
		}
		tagA := kcCandInsertTag(t, db, kcCandTagSpec{Name: "学科 A", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_A"), SortOrder: 1})
		zhTag := kcCandInsertTag(t, db, kcCandTagSpec{Name: "学科 语文", Subject: "chinese", IsSystem: 1,
			ExternalRef: kcCandStrPtr("RES_ZH"), SortOrder: 2})

		tagsBefore := kcCandTagTableSnapshot(t, db)
		itemBefore, cardsBefore, masteryBefore := kcCandItemAndStateSnapshot(t, db, itemID)

		for _, tc := range []struct {
			name    string
			subject string
			refs    []string
		}{
			{"cross-subject arg", "chinese", []string{"RES_ZH"}},
			{"empty arg", "", []string{"MR_A"}},
			{"case-variant arg", "Math", []string{"MR_A"}},
		} {
			resolved, attached, err := s.MergeErrorItemKnowledgeRefs(ctx, itemID, tc.subject, tc.refs)
			if !errors.Is(err, store.ErrErrorItemSubjectMismatch) {
				t.Fatalf("%s: err = %v, want ErrErrorItemSubjectMismatch", tc.name, err)
			}
			if resolved != 0 || attached != 0 {
				t.Fatalf("%s: resolved=%d attached=%d, want 0/0", tc.name, resolved, attached)
			}
			itemAfter, cardsAfter, masteryAfter := kcCandItemAndStateSnapshot(t, db, itemID)
			kcCandAssertUnchanged(t, itemBefore, itemAfter, "error_items")
			kcCandAssertUnchanged(t, cardsBefore, cardsAfter, "review_items")
			kcCandAssertUnchanged(t, masteryBefore, masteryAfter, "kc_mastery")
			kcCandAssertUnchanged(t, tagsBefore, kcCandTagTableSnapshot(t, db), "knowledge_tags")
			kcCandAssertLinks(t, db, itemID, parentTag)
			_ = tagA
			_ = zhTag
		}

		// 最终 subject 冲突：家长并发把库内 subject 改成 chinese，调用方仍传 math。
		kcCandExec(t, db, `UPDATE error_items SET subject = 'chinese' WHERE id = ?`, itemID)
		itemAfterEdit, cardsAfterEdit, masteryAfterEdit := kcCandItemAndStateSnapshot(t, db, itemID)

		resolved, attached, err := s.MergeErrorItemKnowledgeRefs(ctx, itemID, "math", []string{"MR_A"})
		if !errors.Is(err, store.ErrErrorItemSubjectMismatch) {
			t.Fatalf("final subject conflict err = %v, want ErrErrorItemSubjectMismatch", err)
		}
		if resolved != 0 || attached != 0 {
			t.Fatalf("final subject conflict resolved=%d attached=%d, want 0/0", resolved, attached)
		}
		itemAfter, cardsAfter, masteryAfter := kcCandItemAndStateSnapshot(t, db, itemID)
		kcCandAssertUnchanged(t, itemAfterEdit, itemAfter, "error_items after subject conflict")
		kcCandAssertUnchanged(t, cardsAfterEdit, cardsAfter, "review_items after subject conflict")
		kcCandAssertUnchanged(t, masteryAfterEdit, masteryAfter, "kc_mastery after subject conflict")
		kcCandAssertLinks(t, db, itemID, parentTag)

		// 恢复学科后必须能继续写，证明失败事务没有占死单写者连接。
		kcCandExec(t, db, `UPDATE error_items SET subject = 'math' WHERE id = ?`, itemID)
		resolved, attached, err = s.MergeErrorItemKnowledgeRefs(ctx, itemID, "math", []string{"MR_A"})
		if err != nil || resolved != 1 || attached != 1 {
			t.Fatalf("follow-up after mismatch resolved=%d attached=%d err=%v, want 1/1 nil", resolved, attached, err)
		}
		kcCandAssertLinks(t, db, itemID, parentTag, tagA)
	})

	t.Run("EmptyRefsZeroWrites", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)
		itemID, _ := kcCandNewItem(t, s, ctx, "math")
		parentTag, err := s.FindOrCreateKnowledgeTag(ctx, "空输入家长标签", "math", nil)
		if err != nil {
			t.Fatalf("FindOrCreateKnowledgeTag: %v", err)
		}
		if err := s.AttachErrorItemTag(ctx, itemID, parentTag); err != nil {
			t.Fatalf("attach parent tag: %v", err)
		}
		tagA := kcCandInsertTag(t, db, kcCandTagSpec{Name: "空输入 A", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_A"), SortOrder: 1})

		itemBefore, cardsBefore, masteryBefore := kcCandItemAndStateSnapshot(t, db, itemID)
		tagsBefore := kcCandTagTableSnapshot(t, db)

		for _, refs := range [][]string{nil, {}, {"", "   ", "\t"}} {
			resolved, attached, err := s.MergeErrorItemKnowledgeRefs(ctx, itemID, "math", refs)
			if err != nil {
				t.Fatalf("empty refs %#v: %v", refs, err)
			}
			if resolved != 0 || attached != 0 {
				t.Fatalf("empty refs %#v resolved=%d attached=%d, want 0/0", refs, resolved, attached)
			}
			itemAfter, cardsAfter, masteryAfter := kcCandItemAndStateSnapshot(t, db, itemID)
			kcCandAssertUnchanged(t, itemBefore, itemAfter, "error_items")
			kcCandAssertUnchanged(t, cardsBefore, cardsAfter, "review_items")
			kcCandAssertUnchanged(t, masteryBefore, masteryAfter, "kc_mastery")
			kcCandAssertUnchanged(t, tagsBefore, kcCandTagTableSnapshot(t, db), "knowledge_tags")
			kcCandAssertLinks(t, db, itemID, parentTag)
		}

		// 空输入后连接池仍可用。
		resolved, attached, err := s.MergeErrorItemKnowledgeRefs(ctx, itemID, "math", []string{"MR_A"})
		if err != nil || resolved != 1 || attached != 1 {
			t.Fatalf("follow-up after empty refs resolved=%d attached=%d err=%v, want 1/1 nil", resolved, attached, err)
		}
		kcCandAssertLinks(t, db, itemID, parentTag, tagA)
	})

	t.Run("ConcurrentMergeSingleWriterNoDeadlock", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		itemID, _ := kcCandNewItem(t, s, ctx, "math")

		const n = 8
		tagIDs := make([]int64, 0, n)
		refs := make([]string, 0, n)
		for i := 0; i < n; i++ {
			ref := fmt.Sprintf("MR_P%d", i)
			refs = append(refs, ref)
			tagIDs = append(tagIDs, kcCandInsertTag(t, db, kcCandTagSpec{
				Name: fmt.Sprintf("并发点 %d", i), Subject: "math", IsSystem: 1,
				ExternalRef: kcCandStrPtr(ref), SortOrder: i,
			}))
		}
		tagsBefore := kcCandTagTableSnapshot(t, db)

		type mergeResult struct {
			idx      int
			resolved int
			attached int
			err      error
		}
		run := func() []mergeResult {
			ch := make(chan mergeResult, n)
			var wg sync.WaitGroup
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					resolved, attached, err := s.MergeErrorItemKnowledgeRefs(ctx, itemID, "math", []string{refs[i]})
					ch <- mergeResult{idx: i, resolved: resolved, attached: attached, err: err}
				}(i)
			}
			wg.Wait()
			close(ch)
			out := make([]mergeResult, 0, n)
			for r := range ch {
				out = append(out, r)
			}
			return out
		}

		first := run()
		attachedTotal := 0
		for _, r := range first {
			if r.err != nil {
				t.Fatalf("concurrent merge idx=%d: err=%v", r.idx, r.err)
			}
			if r.resolved != 1 || r.attached < 0 || r.attached > 1 {
				t.Fatalf("concurrent merge idx=%d resolved=%d attached=%d, want resolved=1 attached in [0,1]",
					r.idx, r.resolved, r.attached)
			}
			attachedTotal += r.attached
		}
		if attachedTotal != n {
			t.Fatalf("first concurrent round attached total = %d, want %d", attachedTotal, n)
		}
		kcCandAssertLinks(t, db, itemID, tagIDs...)

		second := run()
		attachedTotal = 0
		for _, r := range second {
			if r.err != nil {
				t.Fatalf("second concurrent merge idx=%d: err=%v", r.idx, r.err)
			}
			if r.resolved != 1 {
				t.Fatalf("second concurrent merge idx=%d resolved=%d, want 1", r.idx, r.resolved)
			}
			attachedTotal += r.attached
		}
		if attachedTotal != 0 {
			t.Fatalf("second concurrent round attached total = %d, want 0", attachedTotal)
		}
		kcCandAssertLinks(t, db, itemID, tagIDs...)
		kcCandAssertUnchanged(t, tagsBefore, kcCandTagTableSnapshot(t, db), "knowledge_tags")
	})

	t.Run("CanceledContextLeavesNoHalfMerge", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)
		itemID, _ := kcCandNewItem(t, s, ctx, "math")
		parentTag, err := s.FindOrCreateKnowledgeTag(ctx, "取消家长标签", "math", nil)
		if err != nil {
			t.Fatalf("FindOrCreateKnowledgeTag: %v", err)
		}
		if err := s.AttachErrorItemTag(ctx, itemID, parentTag); err != nil {
			t.Fatalf("attach parent tag: %v", err)
		}
		kcCandInsertTag(t, db, kcCandTagSpec{Name: "取消 A", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_A"), SortOrder: 1})

		itemBefore, cardsBefore, masteryBefore := kcCandItemAndStateSnapshot(t, db, itemID)

		canceled, cancel := context.WithCancel(ctx)
		cancel()
		resolved, attached, err := s.MergeErrorItemKnowledgeRefs(canceled, itemID, "math", []string{"MR_A"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled ctx err = %v, want context.Canceled", err)
		}
		if resolved != 0 || attached != 0 {
			t.Fatalf("canceled ctx resolved=%d attached=%d, want 0/0", resolved, attached)
		}
		kcCandAssertLinks(t, db, itemID, parentTag)
		itemAfter, cardsAfter, masteryAfter := kcCandItemAndStateSnapshot(t, db, itemID)
		kcCandAssertUnchanged(t, itemBefore, itemAfter, "error_items")
		kcCandAssertUnchanged(t, cardsBefore, cardsAfter, "review_items")
		kcCandAssertUnchanged(t, masteryBefore, masteryAfter, "kc_mastery")

		// 取消路径之后单写者连接池仍可用。
		resolved, attached, err = s.MergeErrorItemKnowledgeRefs(ctx, itemID, "math", []string{"MR_A"})
		if err != nil || resolved != 1 || attached != 1 {
			t.Fatalf("follow-up after cancel resolved=%d attached=%d err=%v, want 1/1 nil", resolved, attached, err)
		}
	})

	t.Run("ParentCreatedSameRefRowIsNotResolved", func(t *testing.T) {
		s, db := setupStoreWithDB(t)
		ctx := kcCandCtx(t)
		itemID, _ := kcCandNewItem(t, s, ctx, "math")

		// 家长/AI 自建行（is_system=0）占用同一 external_ref：resolve 与 merge
		// 都不得命中它——external_ref 的 UNIQUE 部分索引下家长行确实可以占用
		// 该 ref，但候选来自系统体系（is_system=1），绑定也只认系统行。
		parentTag := kcCandInsertTag(t, db, kcCandTagSpec{Name: "家长占位行", Subject: "math", IsSystem: 0,
			ExternalRef: kcCandStrPtr("MR_SHARED"), SortOrder: 1})
		sysTag := kcCandInsertTag(t, db, kcCandTagSpec{Name: "系统行", Subject: "math", IsSystem: 1,
			ExternalRef: kcCandStrPtr("MR_SYS"), SortOrder: 2})

		m, err := s.ResolveRefTagIDs(ctx, "math", []string{"MR_SHARED", "MR_SYS"})
		if err != nil {
			t.Fatalf("resolve mixed system/parent: %v", err)
		}
		if len(m) != 1 || m["MR_SYS"] != sysTag {
			t.Fatalf("resolve = %v, want only MR_SYS=%d (parent row must not resolve)", m, sysTag)
		}

		tagsBefore := kcCandTagTableSnapshot(t, db)
		resolved, attached, err := s.MergeErrorItemKnowledgeRefs(ctx, itemID, "math", []string{"MR_SHARED", "MR_SYS"})
		if err != nil {
			t.Fatalf("merge mixed system/parent: %v", err)
		}
		if resolved != 1 || attached != 1 {
			t.Fatalf("merge resolved=%d attached=%d, want 1/1 (only the system row)", resolved, attached)
		}
		kcCandAssertLinks(t, db, itemID, sysTag)
		if ids := kcCandLinkIDs(t, db, itemID); slices.Contains(ids, parentTag) {
			t.Fatalf("parent-created row %d was linked: %v", parentTag, ids)
		}
		kcCandAssertUnchanged(t, tagsBefore, kcCandTagTableSnapshot(t, db), "knowledge_tags")

		// 全部 ref 都被家长行占用：resolved=0（API 层据此保留自由文本兜底），
		// 零写入。
		itemID2, _ := kcCandNewItem(t, s, ctx, "math")
		linksBefore := kcCandSnapshot(t, db,
			`SELECT error_item_id, tag_id FROM error_item_tags ORDER BY error_item_id, tag_id`)
		resolved, attached, err = s.MergeErrorItemKnowledgeRefs(ctx, itemID2, "math", []string{"MR_SHARED"})
		if err != nil {
			t.Fatalf("merge parent-only ref: %v", err)
		}
		if resolved != 0 || attached != 0 {
			t.Fatalf("parent-only ref resolved=%d attached=%d, want 0/0", resolved, attached)
		}
		kcCandAssertUnchanged(t, linksBefore, kcCandSnapshot(t, db,
			`SELECT error_item_id, tag_id FROM error_item_tags ORDER BY error_item_id, tag_id`), "error_item_tags")
	})
}

// 说明：TestMergeErrorItemKnowledgeRefs 的每个子测试都验证 resolved/attached
// 计数、additive union、零副作用与单写者事务可继续使用。
