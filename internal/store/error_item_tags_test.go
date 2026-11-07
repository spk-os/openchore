package store_test

// DEF-ERROR-TAGS-STORE-TESTER：ReplaceErrorItemTags 的真实 SQLite 复验。
// 全部走 setupStore（042 迁移 + MaxOpenConns=1 单写者），不 mock；
// 每个子测试自带 5s context 超时，失败路径后仍读一次，验证事务没有占住连接池。

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

const errorItemTagsTestTimeout = 5 * time.Second

func errorItemTagsCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), errorItemTagsTestTimeout)
	t.Cleanup(cancel)
	return ctx
}

func newErrorItemTagsItem(t *testing.T, s *store.Store, ctx context.Context, subject string) int64 {
	t.Helper()
	kid := createTestUser(t, s, "tags-kid", "child")
	item := &model.ErrorItem{
		UserID:           kid.ID,
		Subject:          subject,
		OriginalImageURL: "https://example.test/error-item-tags.png",
		MistakeStatus:    model.MistakeUnknown,
	}
	if err := s.CreateErrorItem(ctx, item); err != nil {
		t.Fatalf("CreateErrorItem(subject=%q): %v", subject, err)
	}
	return item.ID
}

func newErrorItemTags(t *testing.T, s *store.Store, ctx context.Context, subject string, names ...string) []int64 {
	t.Helper()
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		id, err := s.FindOrCreateKnowledgeTag(ctx, name, subject, nil)
		if err != nil {
			t.Fatalf("FindOrCreateKnowledgeTag(%q,%q): %v", name, subject, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func errorItemTagIDs(tags []model.KnowledgeTag) []int64 {
	ids := make([]int64, 0, len(tags))
	for _, tag := range tags {
		ids = append(ids, tag.ID)
	}
	slices.Sort(ids)
	return ids
}

func assertErrorItemTagIDs(t *testing.T, got []model.KnowledgeTag, want ...int64) {
	t.Helper()
	wantIDs := append([]int64(nil), want...)
	slices.Sort(wantIDs)
	if !slices.Equal(errorItemTagIDs(got), wantIDs) {
		t.Fatalf("tag ids = %v, want %v", errorItemTagIDs(got), wantIDs)
	}
}

// assertBothReadPaths 同时验证 GetErrorItem().Tags 与 TagsForItem 可见；顺序按
// SQL 的 sort_order/name 归一为 ID 集合比较，因此与输入顺序无关。
func assertBothReadPaths(t *testing.T, s *store.Store, ctx context.Context, itemID int64, want ...int64) {
	t.Helper()
	item, err := s.GetErrorItem(ctx, itemID)
	if err != nil || item == nil {
		t.Fatalf("GetErrorItem(%d) = %v, %v", itemID, item, err)
	}
	assertErrorItemTagIDs(t, item.Tags, want...)
	tags, err := s.TagsForItem(ctx, itemID)
	if err != nil {
		t.Fatalf("TagsForItem(%d): %v", itemID, err)
	}
	assertErrorItemTagIDs(t, tags, want...)
}

func visibleErrorItemTagIDs(t *testing.T, s *store.Store, ctx context.Context, itemID int64) (fromGet, fromTags []int64) {
	t.Helper()
	item, err := s.GetErrorItem(ctx, itemID)
	if err != nil || item == nil {
		t.Fatalf("GetErrorItem(%d) = %v, %v", itemID, item, err)
	}
	tags, err := s.TagsForItem(ctx, itemID)
	if err != nil {
		t.Fatalf("TagsForItem(%d): %v", itemID, err)
	}
	return errorItemTagIDs(item.Tags), errorItemTagIDs(tags)
}

func TestReplaceErrorItemTags(t *testing.T) {
	t.Run("SameSubjectMultiTagReplaceOrderIndependent", func(t *testing.T) {
		ctx := errorItemTagsCtx(t)
		s := setupStore(t)
		itemID := newErrorItemTagsItem(t, s, ctx, "math")
		tags := newErrorItemTags(t, s, ctx, "math", "方程", "因式分解", "判别式", "旧知识点")
		a, b, c, old := tags[0], tags[1], tags[2], tags[3]

		// 先挂一个旧 tag，证明后续是「替换」而不是追加。
		if err := s.ReplaceErrorItemTags(ctx, itemID, []int64{old}); err != nil {
			t.Fatalf("seed old link: %v", err)
		}
		if err := s.ReplaceErrorItemTags(ctx, itemID, []int64{a, b, c}); err != nil {
			t.Fatalf("replace with 3 same-subject tags: %v", err)
		}
		firstGet, firstList := visibleErrorItemTagIDs(t, s, ctx, itemID)
		assertBothReadPaths(t, s, ctx, itemID, a, b, c)

		// 换一个输入顺序，读到的集合必须完全一致（顺序无关）。
		if err := s.ReplaceErrorItemTags(ctx, itemID, []int64{c, a, b}); err != nil {
			t.Fatalf("replace with reordered tags: %v", err)
		}
		secondGet, secondList := visibleErrorItemTagIDs(t, s, ctx, itemID)
		assertBothReadPaths(t, s, ctx, itemID, a, b, c)
		if !slices.Equal(firstGet, secondGet) || !slices.Equal(firstList, secondList) || !slices.Equal(firstGet, firstList) {
			t.Fatalf("read paths disagree: get1=%v list1=%v get2=%v list2=%v", firstGet, firstList, secondGet, secondList)
		}
	})

	t.Run("NilClearsLinks", func(t *testing.T) {
		ctx := errorItemTagsCtx(t)
		s := setupStore(t)
		itemID := newErrorItemTagsItem(t, s, ctx, "math")
		tags := newErrorItemTags(t, s, ctx, "math", "清空A", "清空B")
		if err := s.ReplaceErrorItemTags(ctx, itemID, tags); err != nil {
			t.Fatalf("seed: %v", err)
		}
		assertBothReadPaths(t, s, ctx, itemID, tags...)
		if err := s.ReplaceErrorItemTags(ctx, itemID, nil); err != nil {
			t.Fatalf("replace(nil): %v", err)
		}
		assertBothReadPaths(t, s, ctx, itemID)
	})

	t.Run("EmptySliceClearsLinks", func(t *testing.T) {
		ctx := errorItemTagsCtx(t)
		s := setupStore(t)
		itemID := newErrorItemTagsItem(t, s, ctx, "math")
		tags := newErrorItemTags(t, s, ctx, "math", "清空C", "清空D")
		if err := s.ReplaceErrorItemTags(ctx, itemID, tags); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := s.ReplaceErrorItemTags(ctx, itemID, []int64{}); err != nil {
			t.Fatalf("replace([]): %v", err)
		}
		assertBothReadPaths(t, s, ctx, itemID)
	})

	t.Run("DuplicateIDsCollapse", func(t *testing.T) {
		ctx := errorItemTagsCtx(t)
		s := setupStore(t)
		itemID := newErrorItemTagsItem(t, s, ctx, "math")
		tags := newErrorItemTags(t, s, ctx, "math", "去重A", "去重B")
		a, b := tags[0], tags[1]
		if err := s.ReplaceErrorItemTags(ctx, itemID, []int64{a, b, a, b, a}); err != nil {
			t.Fatalf("replace with duplicates: %v", err)
		}
		assertBothReadPaths(t, s, ctx, itemID, a, b)
	})

	t.Run("UnknownTagRejectedAndRollsBack", func(t *testing.T) {
		ctx := errorItemTagsCtx(t)
		s := setupStore(t)
		itemID := newErrorItemTagsItem(t, s, ctx, "math")
		tags := newErrorItemTags(t, s, ctx, "math", "旧A", "旧B", "新C")
		oldA, oldB, newC := tags[0], tags[1], tags[2]
		if err := s.ReplaceErrorItemTags(ctx, itemID, []int64{oldA, oldB}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		err := s.ReplaceErrorItemTags(ctx, itemID, []int64{newC, oldA, 999999})
		if !errors.Is(err, store.ErrKnowledgeTagMismatch) {
			t.Fatalf("unknown tag: err = %v, want ErrKnowledgeTagMismatch", err)
		}
		// 旧关联完整保留，且输入里的合法新 tag 没有被写入（整体回滚）。
		assertBothReadPaths(t, s, ctx, itemID, oldA, oldB)
	})

	t.Run("NonPositiveTagRejectedAndRollsBack", func(t *testing.T) {
		ctx := errorItemTagsCtx(t)
		s := setupStore(t)
		itemID := newErrorItemTagsItem(t, s, ctx, "math")
		tags := newErrorItemTags(t, s, ctx, "math", "旧A", "旧B", "新C")
		oldA, oldB, newC := tags[0], tags[1], tags[2]
		if err := s.ReplaceErrorItemTags(ctx, itemID, []int64{oldA, oldB}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		for _, bad := range []int64{0, -1} {
			err := s.ReplaceErrorItemTags(ctx, itemID, []int64{newC, oldA, bad})
			if !errors.Is(err, store.ErrKnowledgeTagMismatch) {
				t.Fatalf("tag id %d: err = %v, want ErrKnowledgeTagMismatch", bad, err)
			}
			assertBothReadPaths(t, s, ctx, itemID, oldA, oldB)
		}
	})

	t.Run("CrossSubjectTagRejectedAndRollsBack", func(t *testing.T) {
		ctx := errorItemTagsCtx(t)
		s := setupStore(t)
		itemID := newErrorItemTagsItem(t, s, ctx, "math")
		tags := newErrorItemTags(t, s, ctx, "math", "旧A", "旧B", "新C")
		oldA, oldB, newC := tags[0], tags[1], tags[2]
		chinese := newErrorItemTags(t, s, ctx, "chinese", "语文知识点")[0]
		if err := s.ReplaceErrorItemTags(ctx, itemID, []int64{oldA, oldB}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		err := s.ReplaceErrorItemTags(ctx, itemID, []int64{newC, oldA, chinese})
		if !errors.Is(err, store.ErrKnowledgeTagMismatch) {
			t.Fatalf("cross-subject tag: err = %v, want ErrKnowledgeTagMismatch", err)
		}
		assertBothReadPaths(t, s, ctx, itemID, oldA, oldB)
	})

	t.Run("MissingItemReturnsErrNoRows", func(t *testing.T) {
		ctx := errorItemTagsCtx(t)
		s := setupStore(t)
		itemID := newErrorItemTagsItem(t, s, ctx, "math")
		tags := newErrorItemTags(t, s, ctx, "math", "存在tag")
		missingID := itemID + 9999
		err := s.ReplaceErrorItemTags(ctx, missingID, tags)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("missing item: err = %v, want sql.ErrNoRows", err)
		}
		// 不存在的 item 不应留下任何幽灵关联。
		orphan, err := s.TagsForItem(ctx, missingID)
		if err != nil {
			t.Fatalf("TagsForItem(missing): %v", err)
		}
		if len(orphan) != 0 {
			t.Fatalf("missing item has %d links, want 0", len(orphan))
		}
	})

	t.Run("NoSubjectRejectsIllegalAndBindsEmptySubjectTag", func(t *testing.T) {
		ctx := errorItemTagsCtx(t)
		s := setupStore(t)
		// error_items.subject 是 NOT NULL，schema 允许空串表达「无学科」。
		itemID := newErrorItemTagsItem(t, s, ctx, "")
		emptyTags := newErrorItemTags(t, s, ctx, "", "空学科A", "空学科B")
		chinese := newErrorItemTags(t, s, ctx, "chinese", "语文tag")[0]

		// 合法空学科 tag 可绑定（按实现契约，不虚构 NULL subject 需求）。
		if err := s.ReplaceErrorItemTags(ctx, itemID, []int64{emptyTags[0]}); err != nil {
			t.Fatalf("bind empty-subject tag: %v", err)
		}
		assertBothReadPaths(t, s, ctx, itemID, emptyTags[0])

		// 无学科时跨学科仍拒绝，旧关联完整保留。
		err := s.ReplaceErrorItemTags(ctx, itemID, []int64{emptyTags[1], chinese})
		if !errors.Is(err, store.ErrKnowledgeTagMismatch) {
			t.Fatalf("cross-subject with empty-subject item: err = %v, want ErrKnowledgeTagMismatch", err)
		}
		assertBothReadPaths(t, s, ctx, itemID, emptyTags[0])

		// 无学科时未知 tag 仍拒绝，旧关联完整保留。
		err = s.ReplaceErrorItemTags(ctx, itemID, []int64{emptyTags[1], 999999})
		if !errors.Is(err, store.ErrKnowledgeTagMismatch) {
			t.Fatalf("unknown tag with empty-subject item: err = %v, want ErrKnowledgeTagMismatch", err)
		}
		assertBothReadPaths(t, s, ctx, itemID, emptyTags[0])
	})

	t.Run("CanceledContextLeavesNoHalfReplace", func(t *testing.T) {
		ctx := errorItemTagsCtx(t)
		s := setupStore(t)
		itemID := newErrorItemTagsItem(t, s, ctx, "math")
		tags := newErrorItemTags(t, s, ctx, "math", "旧A", "新B", "新C")
		if err := s.ReplaceErrorItemTags(ctx, itemID, []int64{tags[0]}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		err := s.ReplaceErrorItemTags(canceled, itemID, []int64{tags[1], tags[2]})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled ctx: err = %v, want context.Canceled", err)
		}
		// 旧关联未被半替换（既没丢旧 tag，也没混入新 tag）。
		assertBothReadPaths(t, s, ctx, itemID, tags[0])
		// 单写者连接池未被残留事务占死：再成功替换一次。
		if err := s.ReplaceErrorItemTags(ctx, itemID, []int64{tags[1]}); err != nil {
			t.Fatalf("follow-up replace after cancellation: %v", err)
		}
		assertBothReadPaths(t, s, ctx, itemID, tags[1])
	})
}
