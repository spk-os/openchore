package store_test

// TX-PRACTICE-ERRORBOOK-STORE-TESTER: real-SQLite tests for
// EnsureTaxonomyErrorItem — the practice loop's "same wrong question lands
// in the error book exactly once" guarantee keyed on (user_id, source).
// The atomicity claim lives in a single INSERT ... SELECT ... WHERE NOT
// EXISTS; these tests pin it through the Store API only (no mocks, real
// migrations, single-writer pool as in production).

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// ensureFixture builds the fully-populated ErrorItem used to compare the
// ensure path against CreateErrorItem column for column.
func ensureFixture(userID int64, source string) *model.ErrorItem {
	// FK targets are NULL by default (valid); tests that need real link
	// values set them once the referenced rows exist.
	nextReview := time.Date(2026, 11, 20, 8, 30, 0, 0, time.UTC)
	return &model.ErrorItem{
		UserID:           userID,
		ChoreTitle:       "每日口算",
		ErroredDate:      "2026-11-06",
		Subject:          "math",
		OriginalImageURL: "https://example.test/page1.jpg",
		OCRText:          "x^2 - 5x + 6 = 0",
		QuestionText:     "解方程 x^2 - 5x + 6 = 0",
		AnswerText:       "x=2 或 x=3",
		WrongAnswerText:  "x=1 或 x=3",
		Analysis:         "因式分解出错",
		MistakeAnalysis:  "符号处理错误",
		MistakeStatus:    model.MistakeWrongAttempt,
		ErrorType:        "calculation",
		Difficulty:       "easy",
		AIConfidence:     0.82,
		PaperLevel:       "A",
		GradeSemester:    "七年级上",
		Source:           source,
		MasteryLevel:     1,
		NextReviewAt:     &nextReview,
	}
}

func TestTaxonomyErrorItemCreateMatchesCreateErrorItem(t *testing.T) {
	ctx := context.Background()
	s := setupStore(t)
	kid := createTestUser(t, s, "create-kid", "child")
	parent := createTestUser(t, s, "create-parent", "admin")
	chore := createTestChore(t, s, "口算练习", 1, parent.ID)
	schedule := createTestSchedule(t, s, chore.ID, kid.ID, 1)
	completion := &model.ChoreCompletion{
		ChoreScheduleID: schedule.ID,
		CompletedBy:     kid.ID,
		Status:          "approved",
		CompletionDate:  "2026-11-06",
	}
	if err := s.CompleteChore(ctx, completion); err != nil {
		t.Fatalf("CompleteChore: %v", err)
	}

	viaEnsure := ensureFixture(kid.ID, "practice:item:1")
	viaEnsure.ChoreScheduleID = &schedule.ID
	viaEnsure.ChoreTitle = chore.Title
	viaEnsure.CompletionID = &completion.ID
	id, created, err := s.EnsureTaxonomyErrorItem(ctx, viaEnsure, 0)
	if err != nil {
		t.Fatalf("EnsureTaxonomyErrorItem: %v", err)
	}
	if !created {
		t.Fatalf("first call created=false, want true")
	}
	if id <= 0 || viaEnsure.ID != id {
		t.Fatalf("id=%d item.ID=%d, want same positive id", id, viaEnsure.ID)
	}

	viaCreate := ensureFixture(kid.ID, "practice:item:1-baseline")
	viaCreate.ChoreScheduleID = &schedule.ID
	viaCreate.ChoreTitle = chore.Title
	viaCreate.CompletionID = &completion.ID
	if err := s.CreateErrorItem(ctx, viaCreate); err != nil {
		t.Fatalf("CreateErrorItem: %v", err)
	}
	if viaCreate.ID == id {
		t.Fatalf("baseline row reused id %d", id)
	}

	gotEnsure, err := s.GetErrorItem(ctx, id)
	if err != nil || gotEnsure == nil {
		t.Fatalf("GetErrorItem(ensure)=%v,%v", gotEnsure, err)
	}
	gotCreate, err := s.GetErrorItem(ctx, viaCreate.ID)
	if err != nil || gotCreate == nil {
		t.Fatalf("GetErrorItem(create)=%v,%v", gotCreate, err)
	}

	// Same shape; only identity/timestamps/source differ.
	type view struct {
		UserID                                                      int64
		CompletionID, AnalysisID, ChoreScheduleID                   int64
		ChoreTitle, ErroredDate, Subject, OriginalImageURL, OCRText string
		QuestionText, AnswerText, WrongAnswerText                   string
		Analysis, MistakeAnalysis, MistakeStatus                    string
		ErrorType, Difficulty, PaperLevel, GradeSemester            string
		AIConfidence                                                float64
		MasteryLevel                                                int
		NextReviewAt                                                string
	}
	flatten := func(i *model.ErrorItem) view {
		v := view{
			UserID: i.UserID, ChoreTitle: i.ChoreTitle, ErroredDate: i.ErroredDate,
			Subject: i.Subject, OriginalImageURL: i.OriginalImageURL, OCRText: i.OCRText,
			QuestionText: i.QuestionText, AnswerText: i.AnswerText, WrongAnswerText: i.WrongAnswerText,
			Analysis: i.Analysis, MistakeAnalysis: i.MistakeAnalysis, MistakeStatus: i.MistakeStatus,
			ErrorType: i.ErrorType, Difficulty: i.Difficulty, PaperLevel: i.PaperLevel,
			GradeSemester: i.GradeSemester, AIConfidence: i.AIConfidence, MasteryLevel: i.MasteryLevel,
		}
		if i.CompletionID != nil {
			v.CompletionID = *i.CompletionID
		}
		if i.AnalysisID != nil {
			v.AnalysisID = *i.AnalysisID
		}
		if i.ChoreScheduleID != nil {
			v.ChoreScheduleID = *i.ChoreScheduleID
		}
		if i.NextReviewAt != nil {
			v.NextReviewAt = i.NextReviewAt.UTC().Format(time.RFC3339)
		}
		return v
	}
	if a, b := flatten(gotEnsure), flatten(gotCreate); a != b {
		t.Fatalf("ensure row differs from CreateErrorItem row:\n ensure=%+v\n create=%+v", a, b)
	}

	// Nil pointers and empty optional strings degrade exactly like the
	// CreateErrorItem path (NULL, schema defaults).
	sparse := &model.ErrorItem{UserID: kid.ID, Subject: "math", Source: "practice:sparse"}
	sparseID, created, err := s.EnsureTaxonomyErrorItem(ctx, sparse, 0)
	if err != nil || !created {
		t.Fatalf("sparse ensure: id=%d created=%v err=%v", sparseID, created, err)
	}
	gotSparse, err := s.GetErrorItem(ctx, sparseID)
	if err != nil || gotSparse == nil {
		t.Fatalf("GetErrorItem(sparse)=%v,%v", gotSparse, err)
	}
	if gotSparse.CompletionID != nil || gotSparse.AnalysisID != nil || gotSparse.ChoreScheduleID != nil {
		t.Fatalf("nil FKs stored non-NULL: %+v", gotSparse)
	}
	if gotSparse.MistakeStatus != model.MistakeUnknown {
		t.Fatalf("empty mistake_status=%q, want %q", gotSparse.MistakeStatus, model.MistakeUnknown)
	}
	if gotSparse.MasteryLevel != 0 || gotSparse.NextReviewAt != nil {
		t.Fatalf("sparse mastery=%d next=%v, want 0/nil", gotSparse.MasteryLevel, gotSparse.NextReviewAt)
	}
}

func TestTaxonomyErrorItemIdempotentSameUserSource(t *testing.T) {
	ctx := context.Background()
	s := setupStore(t)
	kid := createTestUser(t, s, "idem-kid", "child")

	first := ensureFixture(kid.ID, "practice:item:7")
	id1, created, err := s.EnsureTaxonomyErrorItem(ctx, first, 0)
	if err != nil || !created {
		t.Fatalf("first: id=%d created=%v err=%v", id1, created, err)
	}
	before, err := s.GetErrorItem(ctx, id1)
	if err != nil || before == nil {
		t.Fatalf("GetErrorItem: %v", err)
	}

	// Simulate the learner having progressed: new call carries different
	// question/answer/mastery/FSRS values. None may overwrite the row.
	retry := ensureFixture(kid.ID, "practice:item:7")
	retry.QuestionText = "溢出题干"
	retry.AnswerText = "溢出答案"
	retry.WrongAnswerText = "溢出错答"
	retry.MasteryLevel = 2
	newNext := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	retry.NextReviewAt = &newNext
	retry.OCRText = "溢出 OCR"
	retry.MistakeStatus = model.MistakeNotAttempted
	retry.AIConfidence = 0.01
	retry.MistakeAnalysis = "溢出错因"

	id2, created2, err := s.EnsureTaxonomyErrorItem(ctx, retry, 0)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if created2 {
		t.Fatalf("retry created=true, want false")
	}
	if id2 != id1 {
		t.Fatalf("retry id=%d, want %d", id2, id1)
	}
	if retry.ID != id1 {
		t.Fatalf("retry item.ID=%d, want %d", retry.ID, id1)
	}

	after, err := s.GetErrorItem(ctx, id1)
	if err != nil || after == nil {
		t.Fatalf("GetErrorItem after: %v", err)
	}
	if after.QuestionText != before.QuestionText || after.AnswerText != before.AnswerText ||
		after.WrongAnswerText != before.WrongAnswerText || after.OCRText != before.OCRText {
		t.Fatalf("content overwritten: before=%q/%q after=%q/%q",
			before.QuestionText, before.AnswerText, after.QuestionText, after.AnswerText)
	}
	if after.MasteryLevel != before.MasteryLevel || after.MasteryLevel != 1 {
		t.Fatalf("mastery overwritten: %d -> %d", before.MasteryLevel, after.MasteryLevel)
	}
	if after.NextReviewAt == nil || before.NextReviewAt == nil ||
		!after.NextReviewAt.Equal(*before.NextReviewAt) {
		t.Fatalf("FSRS next_review_at overwritten: before=%v after=%v", before.NextReviewAt, after.NextReviewAt)
	}
	if after.MistakeStatus != before.MistakeStatus || after.MistakeAnalysis != before.MistakeAnalysis ||
		after.AIConfidence != before.AIConfidence {
		t.Fatalf("verdict fields overwritten: status %q->%q analysis %q->%q conf %v->%v",
			before.MistakeStatus, after.MistakeStatus, before.MistakeAnalysis, after.MistakeAnalysis,
			before.AIConfidence, after.AIConfidence)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("created_at changed: %v -> %v", before.CreatedAt, after.CreatedAt)
	}
	if n := countItems(t, s, ctx, kid.ID); n != 1 {
		t.Fatalf("row count=%d err=%v, want 1", n, err)
	}
}

func TestTaxonomyErrorItemDistinctUserAndSource(t *testing.T) {
	ctx := context.Background()
	s := setupStore(t)
	kidA := createTestUser(t, s, "dist-a", "child")
	kidB := createTestUser(t, s, "dist-b", "child")

	sharedSource := "practice:item:shared"
	a1, created, err := s.EnsureTaxonomyErrorItem(ctx, ensureFixture(kidA.ID, sharedSource), 0)
	if err != nil || !created {
		t.Fatalf("A/%s: id=%d created=%v err=%v", sharedSource, a1, created, err)
	}
	b1, created, err := s.EnsureTaxonomyErrorItem(ctx, ensureFixture(kidB.ID, sharedSource), 0)
	if err != nil || !created {
		t.Fatalf("B/%s: id=%d created=%v err=%v", sharedSource, b1, created, err)
	}
	if a1 == b1 {
		t.Fatalf("same source across users shared row %d", a1)
	}
	a2, created, err := s.EnsureTaxonomyErrorItem(ctx, ensureFixture(kidA.ID, "practice:item:other"), 0)
	if err != nil || !created {
		t.Fatalf("A/other: id=%d created=%v err=%v", a2, created, err)
	}
	if a2 == a1 {
		t.Fatalf("different source reused row %d", a1)
	}

	// Same source text on two users: dedup is per user, not global.
	if n := countItems(t, s, ctx, kidA.ID); n != 2 {
		t.Fatalf("A rows=%d, want 2", n)
	}
	if n := countItems(t, s, ctx, kidB.ID); n != 1 {
		t.Fatalf("B rows=%d, want 1", n)
	}
}

func TestTaxonomyErrorItemTagLinkIdempotent(t *testing.T) {
	ctx := context.Background()
	s := setupStore(t)
	kid := createTestUser(t, s, "tag-kid", "child")
	tag, err := s.FindOrCreateKnowledgeTag(ctx, "二元一次方程", "math", nil)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	otherTag, err := s.FindOrCreateKnowledgeTag(ctx, "不等式", "math", nil)
	if err != nil {
		t.Fatalf("tag2: %v", err)
	}

	id, created, err := s.EnsureTaxonomyErrorItem(ctx, ensureFixture(kid.ID, "practice:tag"), tag)
	if err != nil || !created {
		t.Fatalf("first: id=%d created=%v err=%v", id, created, err)
	}
	for i := 0; i < 3; i++ {
		got, created, err := s.EnsureTaxonomyErrorItem(ctx, ensureFixture(kid.ID, "practice:tag"), tag)
		if err != nil || created || got != id {
			t.Fatalf("repeat %d: id=%d created=%v err=%v", i, got, created, err)
		}
	}
	// A second knowledge point on the same item adds one row, not two.
	if _, _, err := s.EnsureTaxonomyErrorItem(ctx, ensureFixture(kid.ID, "practice:tag"), otherTag); err != nil {
		t.Fatalf("second tag: %v", err)
	}

	got, err := s.GetErrorItem(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("GetErrorItem: %v", err)
	}
	tagCount := map[int64]int{}
	for _, kt := range got.Tags {
		tagCount[kt.ID]++
	}
	if tagCount[tag] != 1 || tagCount[otherTag] != 1 || len(got.Tags) != 2 {
		t.Fatalf("tags=%v, want exactly one each of %d,%d", tagCount, tag, otherTag)
	}

	// tagID <= 0 links nothing.
	bare, _, err := s.EnsureTaxonomyErrorItem(ctx, ensureFixture(kid.ID, "practice:notag"), -5)
	if err != nil {
		t.Fatalf("negative tag: %v", err)
	}
	bareItem, err := s.GetErrorItem(ctx, bare)
	if err != nil || bareItem == nil {
		t.Fatalf("GetErrorItem(bare): %v", err)
	}
	if len(bareItem.Tags) != 0 {
		t.Fatalf("tagID<=0 linked %d tags", len(bareItem.Tags))
	}
}

func TestTaxonomyErrorItemPicksOldestDuplicate(t *testing.T) {
	ctx := context.Background()
	s := setupStore(t)
	kid := createTestUser(t, s, "dup-kid", "child")
	tag, err := s.FindOrCreateKnowledgeTag(ctx, "旧题知识点", "math", nil)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}

	// Legacy duplicates: the paper-analysis path wrote two rows for the
	// same (user, source) before this bridge existed.
	oldest := ensureFixture(kid.ID, "legacy:dup")
	oldest.QuestionText = "最旧"
	if err := s.CreateErrorItem(ctx, oldest); err != nil {
		t.Fatalf("create oldest: %v", err)
	}
	newer := ensureFixture(kid.ID, "legacy:dup")
	newer.QuestionText = "较新"
	if err := s.CreateErrorItem(ctx, newer); err != nil {
		t.Fatalf("create newer: %v", err)
	}

	id, created, err := s.EnsureTaxonomyErrorItem(ctx, ensureFixture(kid.ID, "legacy:dup"), tag)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if created {
		t.Fatalf("ensure created a third row")
	}
	if id != oldest.ID {
		t.Fatalf("resolved id=%d, want oldest %d (newer=%d)", id, oldest.ID, newer.ID)
	}
	if n := countItems(t, s, ctx, kid.ID); n != 2 {
		t.Fatalf("rows=%d, want 2 (no third row)", n)
	}
	oldestItem, err := s.GetErrorItem(ctx, oldest.ID)
	if err != nil || oldestItem == nil {
		t.Fatalf("GetErrorItem(oldest): %v", err)
	}
	if len(oldestItem.Tags) != 1 || oldestItem.Tags[0].ID != tag {
		t.Fatalf("oldest tags=%+v, want [%d]", oldestItem.Tags, tag)
	}
	if n, _ := countTagsViaList(ctx, t, s, kid.ID, tag); n != 1 {
		t.Fatalf("tag links=%d, want 1", n)
	}
}

// countItems counts a kid's error items through the public listing API.
func countItems(t *testing.T, s interface {
	ListErrorItems(ctx context.Context, userID int64, masteryLevel int, subject string, limit int) ([]model.ErrorItem, error)
}, ctx context.Context, userID int64) int {
	t.Helper()
	items, err := s.ListErrorItems(ctx, userID, -1, "", 0)
	if err != nil {
		t.Fatalf("ListErrorItems(%d): %v", userID, err)
	}
	return len(items)
}

// countTagsViaList counts how many of the kid's items carry tagID.
func countTagsViaList(ctx context.Context, t *testing.T, s interface {
	ListErrorItems(ctx context.Context, userID int64, masteryLevel int, subject string, limit int) ([]model.ErrorItem, error)
}, userID, tagID int64) (int, error) {
	t.Helper()
	items, err := s.ListErrorItems(ctx, userID, -1, "", 0)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, it := range items {
		for _, kt := range it.Tags {
			if kt.ID == tagID {
				n++
			}
		}
	}
	return n, nil
}

func TestTaxonomyErrorItemRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	s := setupStore(t)
	kid := createTestUser(t, s, "reject-kid", "child")

	cases := []struct {
		name string
		item *model.ErrorItem
	}{
		{"nil", nil},
		{"zero user", &model.ErrorItem{UserID: 0, Source: "practice:x"}},
		{"negative user", &model.ErrorItem{UserID: -3, Source: "practice:x"}},
		{"empty source", &model.ErrorItem{UserID: kid.ID}},
	}
	for _, tc := range cases {
		id, created, err := s.EnsureTaxonomyErrorItem(ctx, tc.item, 0)
		if err == nil {
			t.Fatalf("%s: expected error, got id=%d created=%v", tc.name, id, created)
		}
		if id != 0 || created {
			t.Fatalf("%s: id=%d created=%v, want 0/false", tc.name, id, created)
		}
	}
	if n := countItems(t, s, ctx, kid.ID); n != 0 {
		t.Fatalf("rejected calls wrote %d rows", n)
	}

	// Valid after the rejects still works (no poisoned state).
	if id, created, err := s.EnsureTaxonomyErrorItem(ctx, ensureFixture(kid.ID, "practice:ok"), 0); err != nil || !created || id <= 0 {
		t.Fatalf("valid after rejects: id=%d created=%v err=%v", id, created, err)
	}
}

func TestTaxonomyErrorItemConcurrentSameKey(t *testing.T) {
	ctx := context.Background()
	s := setupStore(t)
	kid := createTestUser(t, s, "conc-kid", "child")
	tag, err := s.FindOrCreateKnowledgeTag(ctx, "并发知识点", "math", nil)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}

	const n = 16
	ids := make([]int64, n)
	createds := make([]bool, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ids[i], createds[i], errs[i] = s.EnsureTaxonomyErrorItem(
				ctx, ensureFixture(kid.ID, "practice:concurrent"), tag)
		}(i)
	}
	close(start)
	wg.Wait()

	createdCount := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v (possible deadlock on single conn)", i, errs[i])
		}
		if ids[i] != ids[0] {
			t.Fatalf("goroutine %d id=%d, want %d", i, ids[i], ids[0])
		}
		if createds[i] {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created=true %d times, want exactly 1", createdCount)
	}
	if rows := countItems(t, s, ctx, kid.ID); rows != 1 {
		t.Fatalf("rows=%d, want 1", rows)
	}
	if links, err := countTagsViaList(ctx, t, s, kid.ID, tag); err != nil || links != 1 {
		t.Fatalf("tag links=%d err=%v, want 1", links, err)
	}
}
