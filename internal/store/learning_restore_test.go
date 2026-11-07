package store_test

// TX-RESTORE-LEARNING-TESTER：用两台独立的真实 SQLite 库冻结
// Store.RestoreLearning 的跨机稳定引用与 FSRS 恢复语义。
//
// 源机使用小数值 id（用户 7 / 知识点 11 / 错题 31），目标机使用完全不同
// 的数值 id 但相同的 external_ref / error_items.source。导出文档手工构造，
// 不依赖尚未落地的 Export DAO；文档里刻意填入源机 id 与另一个已存在用户
// 的 id，用来证明恢复只认显式的 targetUserID 并且只写本机映射后的 id。
//
// 本卡只覆盖 V5A：mastery / learner_events / review_items。
// Diagnostics / Attempts 属 V5B，这里只断言其结果计数为零，不宣称完整往返。

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

const (
	lrSrcUserID   = int64(7)
	lrSrcTagID    = int64(11)
	lrSrcErrorID  = int64(31)
	lrSrcCardID   = int64(51)
	lrTgtUserID   = int64(201)
	lrTgtOtherID  = int64(202)
	lrTgtTagID    = int64(9001) // external_ref = lrRefAdd
	lrTgtDecoyTag = int64(11)   // 与源机 tag 同数值 id，但 external_ref 不同
	lrTgtQ17Tag   = int64(9002) // external_ref 恰好等于某道错题的 source
	lrTgtMissTag  = int64(9003) // external_ref 恰好等于缺失错题的 source
	lrTgtOutcTag  = int64(9004) // external_ref 恰好等于 course_outcome 卡的 ref
	lrTgtQ17Err   = int64(4101) // 目标用户 + lrRefQ17
	lrTgtOthQ17   = int64(4102) // 另一用户 + lrRefQ17
	lrTgtOthOnly  = int64(4103) // 另一用户 + lrRefQOther（目标用户没有）
	lrTgtBonusErr = int64(4104) // 目标用户 + 与 KC ref 同名的 source
	lrLocalCardID = int64(8801)
	lrLocalEvtID  = int64(7701)
)

const (
	lrRefAdd     = "cnm-math-add"
	lrRefDecoy   = "cnm-decoy"
	lrRefQ17     = "taxonomy:Q17"
	lrRefQOther  = "taxonomy:Q-OTHER"
	lrRefMissing = "taxonomy:Q-MISSING"
	lrRefOutcome = "outcome:xyz"
)

type lrEnv struct {
	srcStore *store.Store
	srcDB    *sql.DB
	tgtStore *store.Store
	tgtDB    *sql.DB
	srcTagID int64
	srcErrID int64
}

// newLREnv 建两台独立内存库：源机放小数值 id，目标机放不同数值 id 但相同
// 稳定引用，并预置若干诱饵行（同 ref 的 tag / 同数值 id 的 tag / 别人的错题）。
func newLREnv(t *testing.T) *lrEnv {
	t.Helper()
	srcStore, srcDB := setupStoreWithDB(t)
	tgtStore, tgtDB := setupStoreWithDB(t)

	// --- 源机：小数值 id，导出文档将手工模仿这些行 ---
	mustExec(t, srcDB, `INSERT INTO users (id, name, role) VALUES (?, '源机孩子', 'child')`, lrSrcUserID)
	mustExec(t, srcDB, `INSERT INTO knowledge_tags (id, name, subject, external_ref) VALUES (?, '源机加法', 'math', ?)`,
		lrSrcTagID, lrRefAdd)
	mustExec(t, srcDB, `INSERT INTO kc_mastery (user_id, tag_id, correct_count, total_count, mastery, low_confidence, last_event_at)
		VALUES (?, ?, 7, 9, 0.73, 0, '2026-03-01 09:00:00')`, lrSrcUserID, lrSrcTagID)
	mustExec(t, srcDB, `INSERT INTO error_items (id, user_id, original_image_url, source) VALUES (?, ?, 'src/q17.png', ?)`,
		lrSrcErrorID, lrSrcUserID, lrRefQ17)
	mustExec(t, srcDB, `INSERT INTO review_items (id, user_id, source_type, source_id, front_text, back_text, subject,
			due, stability, difficulty, scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended, created_at)
		VALUES (?, ?, 'error_item', ?, '源机3+4=?', '7', 'math',
			'2026-06-15 10:30:00', 18.25, 7.5, 12, 6, 2, 'Review', '2026-03-01 09:00:00', 2, 1, '2026-01-05 08:00:00')`,
		lrSrcCardID, lrSrcUserID, lrSrcErrorID)
	mustExec(t, srcDB, `INSERT INTO learner_events (user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at)
		VALUES (?, 'answered', 'review_item', ?, '{"success":false}', '{"subject":"math","kc_ids":[11]}',
			'2026-03-01 09:00:00', '2026-03-01 09:00:00')`, lrSrcUserID, lrSrcTagID)

	// --- 目标机：完全不同的数值 id，但相同稳定引用 ---
	mustExec(t, tgtDB, `INSERT INTO users (id, name, role) VALUES (?, '目标孩子', 'child'), (?, '另一个孩子', 'child')`,
		lrTgtUserID, lrTgtOtherID)
	mustExec(t, tgtDB, `INSERT INTO knowledge_tags (id, name, subject, external_ref) VALUES
		(?, '目标加法', 'math', ?), (?, '同数值诱饵', 'math', ?), (?, 'Q17标签', 'math', ?),
		(?, '缺失标签', 'math', ?), (?, '结果标签', 'math', ?)`,
		lrTgtTagID, lrRefAdd, lrTgtDecoyTag, lrRefDecoy, lrTgtQ17Tag, lrRefQ17,
		lrTgtMissTag, lrRefMissing, lrTgtOutcTag, lrRefOutcome)
	mustExec(t, tgtDB, `INSERT INTO error_items (id, user_id, original_image_url, source) VALUES
		(?, ?, 'tgt/q17.png', ?), (?, ?, 'other/q17.png', ?),
		(?, ?, 'other/only.png', ?), (?, ?, 'tgt/bonus.png', ?)`,
		lrTgtQ17Err, lrTgtUserID, lrRefQ17,
		lrTgtOthQ17, lrTgtOtherID, lrRefQ17,
		lrTgtOthOnly, lrTgtOtherID, lrRefQOther,
		lrTgtBonusErr, lrTgtUserID, lrRefAdd)

	return &lrEnv{
		srcStore: srcStore, srcDB: srcDB, tgtStore: tgtStore, tgtDB: tgtDB,
		srcTagID: lrSrcTagID, srcErrID: lrSrcErrorID,
	}
}

func lrTime(y int, m time.Month, d, h int) time.Time {
	return time.Date(y, m, d, h, 0, 0, 0, time.UTC)
}

func lrPtr(t time.Time) *time.Time { return &t }

func lrCount(t *testing.T, db *sql.DB, table, where string, args ...any) int64 {
	t.Helper()
	q := "SELECT COUNT(*) FROM " + table
	if where != "" {
		q += " WHERE " + where
	}
	var n int64
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count %s (%s): %v", table, where, err)
	}
	return n
}

// lrBaseExport 的所有切片显式非 nil 且为空；TargetUserID / User.ID 故意
// 指向目标库里另一个真实存在的用户，作为「不得隐式回退」的诱饵。
func lrBaseExport() *model.LearningExport {
	return &model.LearningExport{
		SchemaVersion: 1,
		PackVersion:   "cnm-2026",
		ExportedAt:    lrTime(2026, 4, 1, 12),
		TargetUserID:  lrTgtOtherID,
		User:          model.LearningExportUser{ID: lrTgtOtherID, Name: "另一个孩子", Role: model.RoleChild},
		Mastery:       []model.LearningMastery{},
		Events:        []model.LearningEvent{},
		Diagnostics:   []model.DiagnosticSession{},
		Attempts:      []model.DiagnosticAttempt{},
		ReviewItems:   []model.LearningReviewItem{},
	}
}

func lrMastery(ref string, at time.Time) model.LearningMastery {
	return model.LearningMastery{
		ExternalRef: ref, CorrectCount: 7, TotalCount: 9, Mastery: 0.73,
		LowConfidence: false, LastEventAt: lrPtr(at),
	}
}

// lrEvent 的 object_id 必须是 0（导出时已归零的本机 id 不随文档携带），
// 恢复后目标事件 object_id 仍为 0；ContextJSON 故意写源机 tag id 11，
// KCRefs 用稳定 ref，用来验证 kc_ids 重映射为本机 id，而 ObjectRef
// 只作 topic 元数据、绝不写回 object_id。
func lrEvent(ref, result string, occurred time.Time) model.LearningEvent {
	return model.LearningEvent{
		LearnerEvent: model.LearnerEvent{
			ID: 901, UserID: lrSrcUserID,
			Verb: model.EventVerbAnswered, ObjectType: model.EventObjectReviewItem,
			ObjectID: 0, ResultJSON: result,
			ContextJSON: `{"subject":"math","kc_ids":[11]}`,
			OccurredAt:  occurred, CreatedAt: occurred,
		},
		ObjectRef: ref, KCRefs: []string{ref},
	}
}

// lrFullCard 带完整的 go-fsrs 字段，并故意填入源机 ID/UserID/SourceID：
// 恢复必须丢弃这些本机 id，按 SourceRef 重新映射。
func lrFullCard(sourceType, ref string, lastReview time.Time, stability float64) model.LearningReviewItem {
	return model.LearningReviewItem{
		ReviewItem: model.ReviewItem{
			ID: lrSrcCardID, UserID: lrSrcUserID,
			SourceType: sourceType, SourceID: lrSrcErrorID,
			FrontText: "3+4=?", BackText: "7", Subject: "math",
			Due: lrTime(2026, 6, 15, 10), Stability: stability, Difficulty: 7.5,
			ScheduledDays: 12, Reps: 6, Lapses: 2, State: model.ReviewStateReview,
			LastReview: lrPtr(lastReview), RemainingSteps: 2, Suspended: true,
			CreatedAt: lrTime(2026, 1, 5, 8),
		},
		SourceRef: ref,
	}
}

// ---------------------------------------------------------------------------
// 1) target 必选，payload 内的任何 user id 都不能成为隐式目标
// ---------------------------------------------------------------------------

func TestRestoreLearning_RejectsInvalidTargetAndPayloadUserIDs(t *testing.T) {
	env := newLREnv(t)
	ctx := context.Background()

	if _, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, nil); err == nil || !strings.Contains(err.Error(), "payload is required") {
		t.Fatalf("nil payload: want 'payload is required', got %v", err)
	}

	cases := []struct {
		name   string
		target int64
		want   string
	}{
		{"zero", 0, "target user id is required"},
		{"negative", -5, "target user id is required"},
		{"nonexistent", 9999, "not found on this server"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := lrBaseExport()
			p.Mastery = []model.LearningMastery{lrMastery(lrRefAdd, lrTime(2026, 3, 1, 9))}
			// payload.TargetUserID / User.ID 都指向目标库中真实存在的 202，
			// 但显式 target 非法时绝不能隐式落到 202 上。
			res, err := env.tgtStore.RestoreLearning(ctx, tc.target, p)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("target=%d: want error containing %q, got res=%+v err=%v", tc.target, tc.want, res, err)
			}
			if res != nil {
				t.Fatalf("target=%d: result must be nil on validation error, got %+v", tc.target, res)
			}
		})
	}

	for _, table := range []string{"kc_mastery", "learner_events", "review_items"} {
		if n := lrCount(t, env.tgtDB, table, ""); n != 0 {
			t.Fatalf("invalid-target restore wrote %d rows into %s", n, table)
		}
		if n := lrCount(t, env.tgtDB, table, "user_id = ?", lrTgtOtherID); n != 0 {
			t.Fatalf("invalid-target restore leaked %d rows of %s onto payload user %d", n, table, lrTgtOtherID)
		}
	}
	if n := lrCount(t, env.tgtDB, "users", ""); n != 2 {
		t.Fatalf("restore must not create profiles: want 2 users, got %d", n)
	}
}

func TestRestoreLearning_OnlyExplicitTargetReceivesRows(t *testing.T) {
	env := newLREnv(t)
	ctx := context.Background()
	at := lrTime(2026, 3, 1, 9)

	// 两台库确实独立：源机小数值 id，目标机不同数值 id，稳定 ref 相同。
	var srcRef, tgtRef, srcSource string
	if err := env.srcDB.QueryRow(`SELECT external_ref FROM knowledge_tags WHERE id = ?`, env.srcTagID).Scan(&srcRef); err != nil {
		t.Fatalf("source tag %d: %v", env.srcTagID, err)
	}
	if err := env.tgtDB.QueryRow(`SELECT external_ref FROM knowledge_tags WHERE id = ?`, lrTgtTagID).Scan(&tgtRef); err != nil {
		t.Fatalf("target tag %d: %v", lrTgtTagID, err)
	}
	if srcRef != lrRefAdd || tgtRef != lrRefAdd {
		t.Fatalf("stable ref mismatch: src=%q tgt=%q want %q", srcRef, tgtRef, lrRefAdd)
	}
	if err := env.srcDB.QueryRow(`SELECT source FROM error_items WHERE id = ?`, env.srcErrID).Scan(&srcSource); err != nil {
		t.Fatalf("source error item %d: %v", env.srcErrID, err)
	}
	if srcSource != lrRefQ17 {
		t.Fatalf("source error item source: got %q want %q", srcSource, lrRefQ17)
	}

	p := lrBaseExport()
	p.Mastery = []model.LearningMastery{lrMastery(lrRefAdd, at)}
	p.Events = []model.LearningEvent{lrEvent(lrRefAdd, `{"success":false}`, at)}
	p.ReviewItems = []model.LearningReviewItem{lrFullCard(model.ReviewSourceErrorItem, lrRefQ17, at, 18.25)}

	res, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
	if err != nil {
		t.Fatalf("RestoreLearning(target=201): %v", err)
	}
	if res.Imported != 3 || res.Skipped != 0 {
		t.Fatalf("want 3 imported / 0 skipped, got %+v", res)
	}
	// 三段都只能属于显式目标 201；payload 里的 202 一行都不许有。
	for _, table := range []string{"kc_mastery", "learner_events", "review_items"} {
		if n := lrCount(t, env.tgtDB, table, "user_id = ?", lrTgtUserID); n != 1 {
			t.Fatalf("%s: want 1 row for target 201, got %d", table, n)
		}
		if n := lrCount(t, env.tgtDB, table, "user_id = ?", lrTgtOtherID); n != 0 {
			t.Fatalf("%s: payload user 202 must stay untouched, got %d rows", table, n)
		}
	}
	// 源机本机 id 不得被当成目标机 id 写入。
	if n := lrCount(t, env.tgtDB, "kc_mastery", "tag_id = ?", lrSrcTagID); n != 0 {
		t.Fatalf("source tag id %d leaked into target kc_mastery", lrSrcTagID)
	}
}

// ---------------------------------------------------------------------------
// 2) KC external_ref 跨机映射，mastery 数值原样，不按事件重算
// ---------------------------------------------------------------------------

func TestRestoreLearning_KCMasteryMapsExternalRefAndKeepsExportedValues(t *testing.T) {
	env := newLREnv(t)
	ctx := context.Background()
	at := lrTime(2026, 3, 1, 9)

	p := lrBaseExport()
	p.Mastery = []model.LearningMastery{
		lrMastery(lrRefAdd, at),
		lrMastery("cnm-unknown-on-target", at), // 目标机没有这个知识点 → skipped
	}
	// 一条带该 KC 且答错的事件：若恢复路径调用 bumpKCMastery 重算，
	// mastery 会从 0.73 变成 (0+1)/(1+2)=1/3 且 low_confidence=true。
	p.Events = []model.LearningEvent{lrEvent(lrRefAdd, `{"success":false}`, at)}

	res, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
	if err != nil {
		t.Fatalf("RestoreLearning: %v", err)
	}
	if res.Mastery.Imported != 1 || res.Mastery.Skipped != 1 || res.Events.Imported != 1 {
		t.Fatalf("unexpected section stats: %+v", res)
	}
	if res.Imported != 2 || res.Skipped != 1 {
		t.Fatalf("unexpected totals: %+v", res)
	}
	if res.Diagnostics != (model.LearningImportStat{}) || res.Attempts != (model.LearningImportStat{}) {
		t.Fatalf("V5B diagnostics/attempts must stay zero: %+v", res)
	}

	stats, err := env.tgtStore.ListKCMastery(ctx, lrTgtUserID)
	if err != nil {
		t.Fatalf("ListKCMastery: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("want exactly 1 mastery row, got %d: %+v", len(stats), stats)
	}
	m := stats[0]
	if m.TagID != lrTgtTagID {
		t.Fatalf("mastery must map external_ref %q to local tag %d, got %d (source id %d / decoy %d / Q17 tag %d are all wrong)",
			lrRefAdd, lrTgtTagID, m.TagID, lrSrcTagID, lrTgtDecoyTag, lrTgtQ17Tag)
	}
	if m.CorrectCount != 7 || m.TotalCount != 9 || m.Mastery != 0.73 || m.LowConfidence {
		t.Fatalf("mastery was recomputed instead of restored verbatim: %+v", m)
	}
	if m.LastEventAt == nil || !m.LastEventAt.UTC().Equal(at) {
		t.Fatalf("last_event_at: want %s, got %v", at.Format(time.RFC3339), m.LastEventAt)
	}
	if n := lrCount(t, env.tgtDB, "kc_mastery", "tag_id != ?", lrTgtTagID); n != 0 {
		t.Fatalf("mastery leaked onto %d other local tags", n)
	}

	events, err := env.tgtStore.ListAllLearnerEvents(ctx, lrTgtUserID)
	if err != nil {
		t.Fatalf("ListAllLearnerEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 restored event, got %d: %+v", len(events), events)
	}
	e := events[0]
	// 冻结契约：非零源 object_id 在恢复时被跳过；本事件 object_id=0，
	// 恢复后仍为 0。ObjectRef=lrRefAdd 只是 topic 元数据，不得被写回
	// 本机 tag id（既不是 9001 也不是源机 11）。
	if e.UserID != lrTgtUserID || e.ObjectID != 0 {
		t.Fatalf("event object_id must stay 0 (ObjectRef must not map to local tag %d / source %d): %+v",
			lrTgtTagID, lrSrcTagID, e)
	}
	var ctxData struct {
		KCIDs   []int64 `json:"kc_ids"`
		Subject string  `json:"subject"`
	}
	if err := json.Unmarshal([]byte(e.ContextJSON), &ctxData); err != nil {
		t.Fatalf("context_json not JSON after remap: %q: %v", e.ContextJSON, err)
	}
	if len(ctxData.KCIDs) != 1 || ctxData.KCIDs[0] != lrTgtTagID {
		t.Fatalf("context_json.kc_ids must be [%d], got %v", lrTgtTagID, ctxData.KCIDs)
	}
	if ctxData.Subject != "math" {
		t.Fatalf("context_json lost non-kc keys: %q", e.ContextJSON)
	}
	if got, want := e.OccurredAt.UTC().Format(time.DateTime), at.UTC().Format(time.DateTime); got != want {
		t.Fatalf("occurred_at: want %s got %s", want, got)
	}
}

// ---------------------------------------------------------------------------
// 3) ErrorItem FSRS 卡：目标用户 + source 映射，全字段保留
// ---------------------------------------------------------------------------

func TestRestoreLearning_ErrorItemCardMapsTargetSourceAndPreservesFSRS(t *testing.T) {
	env := newLREnv(t)
	ctx := context.Background()
	at := lrTime(2026, 3, 1, 9)
	before := lrCount(t, env.tgtDB, "error_items", "")

	p := lrBaseExport()
	p.ReviewItems = []model.LearningReviewItem{
		lrFullCard(model.ReviewSourceErrorItem, lrRefQ17, at, 18.25),    // 目标用户与另一用户都有，必须取目标用户的 4101
		lrFullCard(model.ReviewSourceErrorItem, lrRefQOther, at, 1.25),  // 只有另一用户有 → skipped
		lrFullCard(model.ReviewSourceErrorItem, lrRefMissing, at, 2.25), // 错题不存在（但有同名 external_ref 的 tag）→ skipped
		lrFullCard(model.ReviewSourceErrorItem, "", at, 3.25),           // 空 ref → skipped
		lrFullCard(model.ReviewSourceOutcome, lrRefOutcome, at, 4.25),   // 不支持的 source_type（也有同名 tag）→ skipped
	}

	res, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
	if err != nil {
		t.Fatalf("RestoreLearning: %v", err)
	}
	if res.ReviewItems.Imported != 1 || res.ReviewItems.Skipped != 4 {
		t.Fatalf("want 1 imported / 4 skipped cards, got %+v", res)
	}
	if res.Imported != 1 || res.Skipped != 4 {
		t.Fatalf("unexpected totals: %+v", res)
	}
	if after := lrCount(t, env.tgtDB, "error_items", ""); after != before {
		t.Fatalf("restore must never create error items: before=%d after=%d", before, after)
	}
	if n := lrCount(t, env.tgtDB, "review_items", ""); n != 1 {
		t.Fatalf("want exactly 1 restored card, got %d", n)
	}

	items, err := env.tgtStore.ListReviewItemsByUser(ctx, lrTgtUserID)
	if err != nil {
		t.Fatalf("ListReviewItemsByUser: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 card for target, got %d: %+v", len(items), items)
	}
	it := items[0]
	if it.SourceType != model.ReviewSourceErrorItem || it.SourceID != lrTgtQ17Err {
		t.Fatalf("card must attach to target's error item %d, not other user's %d / KC tag %d / source id %d: %+v",
			lrTgtQ17Err, lrTgtOthQ17, lrTgtQ17Tag, lrSrcErrorID, it)
	}
	if it.UserID != lrTgtUserID || it.ID == lrSrcCardID {
		t.Fatalf("card must be owned by target with a fresh local id, got %+v", it)
	}

	// 完整 go-fsrs 字段逐项冻结。
	if !it.Due.UTC().Equal(lrTime(2026, 6, 15, 10)) {
		t.Fatalf("due: got %s", it.Due.UTC().Format(time.RFC3339))
	}
	if it.Stability != 18.25 || it.Difficulty != 7.5 {
		t.Fatalf("stability/difficulty: %+v", it)
	}
	if it.ScheduledDays != 12 || it.Reps != 6 || it.Lapses != 2 {
		t.Fatalf("scheduled_days/reps/lapses: %+v", it)
	}
	if it.State != model.ReviewStateReview || it.RemainingSteps != 2 || !it.Suspended {
		t.Fatalf("state/remaining_steps/suspended: %+v", it)
	}
	if it.LastReview == nil || !it.LastReview.UTC().Equal(at) {
		t.Fatalf("last_review: want %s got %v", at.Format(time.RFC3339), it.LastReview)
	}
	if it.FrontText != "3+4=?" || it.BackText != "7" || it.Subject != "math" {
		t.Fatalf("card text columns: %+v", it)
	}
	if !it.CreatedAt.UTC().Equal(lrTime(2026, 1, 5, 8)) {
		t.Fatalf("created_at: got %s", it.CreatedAt.UTC().Format(time.RFC3339))
	}

	// 另一用户不能因为 source 相同而被写入卡片。
	other, err := env.tgtStore.ListReviewItemsByUser(ctx, lrTgtOtherID)
	if err != nil || other == nil || len(other) != 0 {
		t.Fatalf("other user must have no cards, got %+v err=%v", other, err)
	}
	if n := lrCount(t, env.tgtDB, "review_items", "source_id IN (?, ?, ?)", lrTgtOthQ17, lrTgtQ17Tag, lrTgtMissTag); n != 0 {
		t.Fatalf("%d cards landed on a foreign/decoy source id", n)
	}
}

func TestRestoreLearning_OtherUsersCardForSameSourceIsNotBorrowed(t *testing.T) {
	env := newLREnv(t)
	ctx := context.Background()
	at := lrTime(2026, 3, 1, 9)

	// 目标用户也有同 source 的错题，但该 source 的卡属于另一用户：
	// review_items 是表级 UNIQUE(source_type, source_id)，只能 skip。
	mustExec(t, env.tgtDB, `INSERT INTO error_items (id, user_id, original_image_url, source) VALUES (4105, ?, 'tgt/other-source.png', ?)`,
		lrTgtUserID, lrRefQOther)
	mustExec(t, env.tgtDB, `INSERT INTO review_items (id, user_id, source_type, source_id, front_text, back_text, subject,
			due, stability, difficulty, scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended, created_at)
		VALUES (8802, ?, 'error_item', 4105, '别人的卡', '', 'math',
			'2026-09-01 00:00:00', 3.5, 2.5, 1, 1, 0, 'New', NULL, 0, 0, '2026-01-01 00:00:00')`, lrTgtOtherID)
	before := lrCount(t, env.tgtDB, "review_items", "")

	p := lrBaseExport()
	p.ReviewItems = []model.LearningReviewItem{lrFullCard(model.ReviewSourceErrorItem, lrRefQOther, at, 18.25)}
	res, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
	if err != nil {
		t.Fatalf("RestoreLearning: %v", err)
	}
	if res.ReviewItems.Imported != 0 || res.ReviewItems.Skipped != 1 {
		t.Fatalf("card owned by another profile must be skipped: %+v", res)
	}
	if after := lrCount(t, env.tgtDB, "review_items", ""); after != before {
		t.Fatalf("no card may be inserted: before=%d after=%d", before, after)
	}
	if n := lrCount(t, env.tgtDB, "review_items", "user_id = ?", lrTgtUserID); n != 0 {
		t.Fatalf("target must not receive a borrowed card, got %d", n)
	}
}

// DEF-LEARNING-RESTORE-R2 P1：error_items 没有 UNIQUE(user_id, source)，
// 目标同用户出现重复 source 在 schema 上完全可能。旧实现
// ORDER BY id LIMIT 1 会在重复时静默猜第一条；冻结契约改为可判定语义：
// 恰好 1 行才绑卡，0 行或多行都 skip，绝不随机绑定。
func TestRestoreLearning_DuplicateTargetSourceRefusesToGuess(t *testing.T) {
	env := newLREnv(t)
	ctx := context.Background()
	at := lrTime(2026, 3, 1, 9)

	// 目标用户 201 出现第二条 source = taxonomy:Q17 的错题（与 4101 重复）。
	const dupErrID = int64(4106)
	mustExec(t, env.tgtDB, `INSERT INTO error_items (id, user_id, original_image_url, source) VALUES (?, ?, 'tgt/q17-dup.png', ?)`,
		dupErrID, lrTgtUserID, lrRefQ17)
	before := lrCount(t, env.tgtDB, "error_items", "")

	p := lrBaseExport()
	// 三张卡：重复 source（必须跳过）/ 目标唯一 source（正常导入）/ 目标 0 条（跳过）。
	p.ReviewItems = []model.LearningReviewItem{
		lrFullCard(model.ReviewSourceErrorItem, lrRefQ17, at, 18.25),
		lrFullCard(model.ReviewSourceErrorItem, lrRefAdd, at, 9.5),
		lrFullCard(model.ReviewSourceErrorItem, lrRefMissing, at, 2.25),
	}
	res, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
	if err != nil {
		t.Fatalf("RestoreLearning: %v", err)
	}
	if res.ReviewItems.Imported != 1 || res.ReviewItems.Skipped != 2 {
		t.Fatalf("want 1 imported / 2 skipped (duplicate + missing), got %+v", res)
	}
	if after := lrCount(t, env.tgtDB, "error_items", ""); after != before {
		t.Fatalf("restore must never create error items: before=%d after=%d", before, after)
	}

	items, err := env.tgtStore.ListReviewItemsByUser(ctx, lrTgtUserID)
	if err != nil {
		t.Fatalf("ListReviewItemsByUser: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want exactly 1 card (the unique-source one), got %+v", items)
	}
	if it := items[0]; it.SourceType != model.ReviewSourceErrorItem || it.SourceID != lrTgtBonusErr {
		t.Fatalf("unique source must bind to error item %d, got %+v", lrTgtBonusErr, it)
	}
	// 重复 source 的两条候选（4101 / 4106）一条都不能绑；别人的 4102、
	// 源机 31 也不许出现在 source_id 里。
	for _, id := range []int64{lrTgtQ17Err, dupErrID, lrTgtOthQ17, lrSrcErrorID} {
		if n := lrCount(t, env.tgtDB, "review_items", "source_id = ?", id); n != 0 {
			t.Fatalf("duplicate/foreign error item %d received a card: %d rows", id, n)
		}
	}
}

// ---------------------------------------------------------------------------
// 4) KC 卡与 ErrorItem 卡并存，两个 ref 空间互不误映射
// ---------------------------------------------------------------------------

func TestRestoreLearning_KCAndErrorItemCardsCoexistWithoutCrossMapping(t *testing.T) {
	env := newLREnv(t)
	ctx := context.Background()
	at := lrTime(2026, 3, 1, 9)

	p := lrBaseExport()
	p.ReviewItems = []model.LearningReviewItem{
		lrFullCard(model.ReviewSourceKC, lrRefAdd, at, 18.25),      // → (kc, 9001)
		lrFullCard(model.ReviewSourceErrorItem, lrRefQ17, at, 9.5), // → (error_item, 4101)
	}
	res, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
	if err != nil {
		t.Fatalf("RestoreLearning: %v", err)
	}
	if res.ReviewItems.Imported != 2 || res.ReviewItems.Skipped != 0 || res.Imported != 2 {
		t.Fatalf("both cards must import: %+v", res)
	}

	items, err := env.tgtStore.ListReviewItemsByUser(ctx, lrTgtUserID)
	if err != nil {
		t.Fatalf("ListReviewItemsByUser: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 coexisting cards, got %d: %+v", len(items), items)
	}
	byType := map[string]model.ReviewItem{}
	for _, it := range items {
		byType[it.SourceType] = it
	}
	kc, ok1 := byType[model.ReviewSourceKC]
	ei, ok2 := byType[model.ReviewSourceErrorItem]
	if !ok1 || !ok2 {
		t.Fatalf("one of the two source spaces was lost: %+v", items)
	}
	// KC 卡的 ref 恰好也是目标用户某道错题的 source（4104）；
	// ErrorItem 卡的 ref 恰好也是某个知识点的 external_ref（9002）。
	// 任一方向串用 ref 空间都会落到这些诱饵 id 上。
	if kc.SourceID != lrTgtTagID || kc.Stability != 18.25 {
		t.Fatalf("KC card must resolve via external_ref to tag %d, got %+v", lrTgtTagID, kc)
	}
	if ei.SourceID != lrTgtQ17Err || ei.Stability != 9.5 {
		t.Fatalf("ErrorItem card must resolve via source to error item %d, got %+v", lrTgtQ17Err, ei)
	}
	for _, decoy := range []int64{lrTgtBonusErr, lrTgtQ17Tag, lrSrcErrorID, lrTgtOthQ17, lrTgtDecoyTag} {
		if n := lrCount(t, env.tgtDB, "review_items", "source_id = ?", decoy); n != 0 {
			t.Fatalf("card cross-mapped onto decoy source id %d", decoy)
		}
	}
}

// ---------------------------------------------------------------------------
// 5) 内容级事件去重与重复 Restore 幂等
// ---------------------------------------------------------------------------

func TestRestoreLearning_RepeatRestoreIsIdempotentAndDedupesEventsByContent(t *testing.T) {
	env := newLREnv(t)
	ctx := context.Background()
	at1 := lrTime(2026, 3, 1, 9)
	at2 := lrTime(2026, 3, 2, 9)
	mappedCtx := fmt.Sprintf(`{"kc_ids":[%d]}`, lrTgtTagID)

	// 目标库预置一条与导出事件内容完全相同的本机事件。按冻结契约，
	// 恢复事件 object_id 恒为 0，dedupe key 也按 0 计算，所以本机行必须
	// 同样用 object_id=0 才构成"同内容"。learner_events 无唯一键，唯一
	// 能阻止重复的是内容级 dedupe key。
	mustExec(t, env.tgtDB, `INSERT INTO learner_events
			(id, user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at)
		VALUES (?, ?, 'answered', 'review_item', 0, ?, ?, ?, ?)`,
		lrLocalEvtID, lrTgtUserID, `{"success":true}`, mappedCtx,
		at1.UTC().Format(time.DateTime), at1.UTC().Format(time.DateTime))

	mk := func(result, ctxJSON string, refs []string, occurred time.Time, objRef string) model.LearningEvent {
		return model.LearningEvent{
			LearnerEvent: model.LearnerEvent{
				ID: 901, UserID: lrSrcUserID,
				Verb: model.EventVerbAnswered, ObjectType: model.EventObjectReviewItem,
				ObjectID: 0, ResultJSON: result, ContextJSON: ctxJSON,
				OccurredAt: occurred, CreatedAt: occurred,
			},
			ObjectRef: objRef, KCRefs: refs,
		}
	}
	nonZeroObj := mk(`{"success":true}`, "", []string{lrRefAdd}, at1, lrRefAdd)
	nonZeroObj.ObjectID = lrSrcTagID // 反向断言：非零源 object_id 必须被跳过
	p := lrBaseExport()
	p.Events = []model.LearningEvent{
		mk(`{"success":true}`, "", []string{lrRefAdd}, at1, lrRefAdd),                // 与预置行同内容 → dedupe
		mk(`{"success":false}`, "", []string{lrRefAdd}, at1, lrRefAdd),               // 只改 result_json → 新事件
		mk(`{"success":true}`, "", []string{lrRefAdd}, at2, lrRefAdd),                // 只改 occurred_at → 新事件
		mk(`{"success":true}`, `{"note":"extra"}`, nil, at1, lrRefAdd),               // 只改 context_json → 新事件
		mk(`{"success":true}`, "", []string{lrRefAdd}, at1, "cnm-unknown-on-target"), // ref 不存在 → skipped
		nonZeroObj, // 非零 object_id → 必须 skip，且不得被映射成本机 tag id
	}

	first, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
	if err != nil {
		t.Fatalf("first RestoreLearning: %v", err)
	}
	if first.Events.Imported != 3 || first.Events.Skipped != 3 {
		t.Fatalf("first restore: want 3 imported / 3 skipped, got %+v", first)
	}
	if n := lrCount(t, env.tgtDB, "learner_events", "user_id = ?", lrTgtUserID); n != 4 {
		t.Fatalf("first restore: want 1 preseeded + 3 imported events, got %d", n)
	}
	if n := lrCount(t, env.tgtDB, "learner_events",
		"result_json = ? AND context_json = ? AND occurred_at = ?", `{"success":true}`, mappedCtx, at1.UTC().Format(time.DateTime)); n != 1 {
		t.Fatalf("content-level dedupe failed: identical event count = %d", n)
	}

	second, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
	if err != nil {
		t.Fatalf("second RestoreLearning: %v", err)
	}
	if second.Imported != 0 || second.Events.Imported != 0 || second.Events.Skipped != 6 || second.Skipped != 6 {
		t.Fatalf("repeat restore must be a total no-op: %+v", second)
	}
	if n := lrCount(t, env.tgtDB, "learner_events", "user_id = ?", lrTgtUserID); n != 4 {
		t.Fatalf("repeat restore appended events: got %d want 4", n)
	}
	var preseededStill int64
	if err := env.tgtDB.QueryRow(`SELECT COUNT(*) FROM learner_events WHERE id = ? AND user_id = ?`,
		lrLocalEvtID, lrTgtUserID).Scan(&preseededStill); err != nil || preseededStill != 1 {
		t.Fatalf("preseeded event was modified: count=%d err=%v", preseededStill, err)
	}
	// 事件内容中不得残留源机 tag id，也不得被映射成本机 tag id：
	// 所有恢复事件的 object_id 必须为 0。
	if n := lrCount(t, env.tgtDB, "learner_events", "object_id = ?", lrSrcTagID); n != 0 {
		t.Fatalf("%d events still point at source tag id %d", n, lrSrcTagID)
	}
	if n := lrCount(t, env.tgtDB, "learner_events", "object_id = ?", lrTgtTagID); n != 0 {
		t.Fatalf("%d events wrongly mapped ObjectRef/KCRef onto local tag id %d", n, lrTgtTagID)
	}
	if n := lrCount(t, env.tgtDB, "learner_events", "user_id = ? AND object_id != 0", lrTgtUserID); n != 0 {
		t.Fatalf("%d restored events have non-zero object_id", n)
	}
}

// ---------------------------------------------------------------------------
// 5) 本机较新数据不被旧导出覆盖（严格时间戳语义）
// ---------------------------------------------------------------------------

func TestRestoreLearning_NewerLocalMasteryAndCardAreNotOverwritten(t *testing.T) {
	env := newLREnv(t)
	ctx := context.Background()
	stale := lrTime(2026, 3, 1, 9)
	local := lrTime(2026, 5, 1, 9)
	newer := lrTime(2026, 6, 1, 9)
	localStr := local.UTC().Format(time.DateTime)

	// 本机较新 mastery / 本机 NULL 时间戳 mastery / 本机较旧 mastery。
	mustExec(t, env.tgtDB, `INSERT INTO kc_mastery (user_id, tag_id, correct_count, total_count, mastery, low_confidence, last_event_at)
		VALUES (?, ?, 99, 100, 0.99, 1, ?)`, lrTgtUserID, lrTgtTagID, localStr)
	mustExec(t, env.tgtDB, `INSERT INTO kc_mastery (user_id, tag_id, correct_count, total_count, mastery, low_confidence, last_event_at)
		VALUES (?, ?, 0, 0, 0.1, 1, NULL)`, lrTgtUserID, lrTgtQ17Tag)
	mustExec(t, env.tgtDB, `INSERT INTO kc_mastery (user_id, tag_id, correct_count, total_count, mastery, low_confidence, last_event_at)
		VALUES (?, ?, 1, 1, 0.2, 1, '2026-02-01 09:00:00')`, lrTgtUserID, lrTgtMissTag)

	// 本机较新的 KC 卡，FSRS 全字段与导出值都不同；本机 NULL last_review 的错题卡。
	mustExec(t, env.tgtDB, `INSERT INTO review_items (id, user_id, source_type, source_id, front_text, back_text, subject,
			due, stability, difficulty, scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended, created_at)
		VALUES (?, ?, 'kc', ?, '本机题干', '本机答案', 'chinese',
			'2026-01-01 00:00:00', 99.5, 1.25, 1, 99, 9, 'Learning', ?, 4, 0, '2025-12-01 00:00:00')`,
		lrLocalCardID, lrTgtUserID, lrTgtTagID, localStr)
	mustExec(t, env.tgtDB, `INSERT INTO review_items (id, user_id, source_type, source_id, front_text, back_text, subject,
			due, stability, difficulty, scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended, created_at)
		VALUES (8803, ?, 'error_item', ?, '本机遇错题干', '本机答案2', 'chinese',
			'2026-01-02 00:00:00', 1.5, 2.5, 2, 1, 1, 'New', NULL, 0, 0, '2025-12-02 00:00:00')`,
		lrTgtUserID, lrTgtBonusErr)

	p := lrBaseExport()
	p.Mastery = []model.LearningMastery{
		lrMastery(lrRefAdd, stale),     // 旧导出 → skip
		lrMastery(lrRefQ17, stale),     // 本机 NULL → 允许覆盖
		lrMastery(lrRefMissing, newer), // 严格更新 → 允许覆盖
	}
	p.ReviewItems = []model.LearningReviewItem{
		lrFullCard(model.ReviewSourceKC, lrRefAdd, stale, 18.25),        // 旧导出 → skip
		lrFullCard(model.ReviewSourceKC, lrRefAdd, local, 18.25),        // 时间戳相等 → skip
		lrFullCard(model.ReviewSourceKC, lrRefQ17, stale, 18.25),        // 无本机卡 → insert
		lrFullCard(model.ReviewSourceErrorItem, lrRefAdd, stale, 18.25), // 本机 NULL last_review → update
	}

	res, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
	if err != nil {
		t.Fatalf("RestoreLearning: %v", err)
	}
	if res.Mastery.Imported != 2 || res.Mastery.Skipped != 1 || res.ReviewItems.Imported != 2 || res.ReviewItems.Skipped != 2 {
		t.Fatalf("stale guard stats: %+v", res)
	}

	stats, err := env.tgtStore.ListKCMastery(ctx, lrTgtUserID)
	if err != nil {
		t.Fatalf("ListKCMastery: %v", err)
	}
	byTag := map[int64]store.KCMasteryStat{}
	for _, m := range stats {
		byTag[m.TagID] = m
	}
	if len(byTag) != 3 {
		t.Fatalf("want 3 mastery rows, got %+v", stats)
	}
	if m := byTag[lrTgtTagID]; m.CorrectCount != 99 || m.TotalCount != 100 || m.Mastery != 0.99 || !m.LowConfidence ||
		m.LastEventAt == nil || !m.LastEventAt.UTC().Equal(local) {
		t.Fatalf("newer local mastery was overwritten: %+v", m)
	}
	if m := byTag[lrTgtQ17Tag]; m.CorrectCount != 7 || m.Mastery != 0.73 || m.LowConfidence {
		t.Fatalf("NULL local mastery should accept exported values: %+v", m)
	}
	if m := byTag[lrTgtMissTag]; m.CorrectCount != 7 || m.Mastery != 0.73 ||
		m.LastEventAt == nil || !m.LastEventAt.UTC().Equal(newer) {
		t.Fatalf("strictly newer export should win: %+v", m)
	}

	items, err := env.tgtStore.ListReviewItemsByUser(ctx, lrTgtUserID)
	if err != nil {
		t.Fatalf("ListReviewItemsByUser: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 cards, got %d: %+v", len(items), items)
	}
	bySource := map[string]model.ReviewItem{}
	for _, it := range items {
		bySource[fmt.Sprintf("%s/%d", it.SourceType, it.SourceID)] = it
	}
	localCard := bySource[fmt.Sprintf("kc/%d", lrTgtTagID)]
	if localCard.ID != lrLocalCardID || localCard.Stability != 99.5 || localCard.Difficulty != 1.25 ||
		localCard.ScheduledDays != 1 || localCard.Reps != 99 || localCard.Lapses != 9 ||
		localCard.State != model.ReviewStateLearning || localCard.RemainingSteps != 4 || localCard.Suspended ||
		localCard.LastReview == nil || !localCard.LastReview.UTC().Equal(local) {
		t.Fatalf("newer local FSRS card was overwritten: %+v", localCard)
	}
	updated := bySource[fmt.Sprintf("error_item/%d", lrTgtBonusErr)]
	if updated.Stability != 18.25 || updated.Difficulty != 7.5 || updated.ScheduledDays != 12 ||
		updated.Reps != 6 || updated.Lapses != 2 || updated.State != model.ReviewStateReview ||
		updated.RemainingSteps != 2 || !updated.Suspended ||
		updated.LastReview == nil || !updated.LastReview.UTC().Equal(stale) {
		t.Fatalf("NULL-local card must be updated with full FSRS state: %+v", updated)
	}
	// UPDATE 路径只改 scheduler 列，本机内容列保持。
	if updated.FrontText != "本机遇错题干" || updated.BackText != "本机答案2" || updated.Subject != "chinese" ||
		!updated.CreatedAt.UTC().Equal(lrTime(2025, 12, 2, 0)) {
		t.Fatalf("UPDATE path overwrote local content columns: %+v", updated)
	}

	// 同一份文档再恢复一次：所有时间戳相等，全部 skip，行数不变。
	again, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
	if err != nil {
		t.Fatalf("repeat RestoreLearning: %v", err)
	}
	if again.Imported != 0 || again.Mastery.Skipped != 3 || again.ReviewItems.Skipped != 4 {
		t.Fatalf("repeat restore must skip every row: %+v", again)
	}
	if n := lrCount(t, env.tgtDB, "review_items", ""); n != 3 {
		t.Fatalf("repeat restore appended cards: %d", n)
	}
}

// ---------------------------------------------------------------------------
// 6) 空 slices 契约 / unsupported schema
// ---------------------------------------------------------------------------

func TestRestoreLearning_EmptyDocumentKeepsNonNilSlicesAndV5BZeroes(t *testing.T) {
	env := newLREnv(t)
	ctx := context.Background()

	p := lrBaseExport() // 所有分节 slice 显式为空但非 nil
	res, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
	if err != nil {
		t.Fatalf("empty RestoreLearning: %v", err)
	}
	if res == nil {
		t.Fatal("empty restore must return a non-nil result")
	}
	if *res != (model.LearningRestoreResult{}) {
		t.Fatalf("empty restore must report all-zero stats, got %+v", res)
	}
	// V5B：Diagnostics / Attempts 在本卡恒为零，不代表完整往返。
	if res.Diagnostics.Imported != 0 || res.Diagnostics.Skipped != 0 ||
		res.Attempts.Imported != 0 || res.Attempts.Skipped != 0 {
		t.Fatalf("V5B sections must stay zero: %+v", res)
	}

	// LearningExport 的空分节在 JSON 里必须是 [] 而不是 null。
	blob, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(blob, &sections); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	for _, key := range []string{"mastery", "events", "diagnostics", "attempts", "review_items"} {
		if string(sections[key]) != "[]" {
			t.Fatalf("export section %q must marshal as [] (non-nil slice), got %s", key, sections[key])
		}
	}

	// 结果读取侧的空集合模型同样承诺 [] 非 nil。
	assertEmpty := func(name string, isNil bool, n int, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if isNil || n != 0 {
			t.Fatalf("%s: want non-nil empty slice, got isNil=%v len=%d", name, isNil, n)
		}
	}
	mastery, err := env.tgtStore.ListKCMastery(ctx, lrTgtUserID)
	assertEmpty("ListKCMastery", mastery == nil, len(mastery), err)
	events, err := env.tgtStore.ListLearnerEvents(ctx, lrTgtUserID, 0)
	assertEmpty("ListLearnerEvents", events == nil, len(events), err)
	allEvents, err := env.tgtStore.ListAllLearnerEvents(ctx, lrTgtUserID)
	assertEmpty("ListAllLearnerEvents", allEvents == nil, len(allEvents), err)
	cards, err := env.tgtStore.ListReviewItemsByUser(ctx, lrTgtUserID)
	assertEmpty("ListReviewItemsByUser", cards == nil, len(cards), err)
}

func TestRestoreLearning_RejectsUnsupportedSchemaWithoutWrites(t *testing.T) {
	env := newLREnv(t)
	ctx := context.Background()
	at := lrTime(2026, 3, 1, 9)

	for _, version := range []int{2, 99} {
		p := lrBaseExport()
		p.SchemaVersion = version
		p.Mastery = []model.LearningMastery{lrMastery(lrRefAdd, at)}
		p.Events = []model.LearningEvent{lrEvent(lrRefAdd, `{"success":true}`, at)}
		p.ReviewItems = []model.LearningReviewItem{lrFullCard(model.ReviewSourceErrorItem, lrRefQ17, at, 18.25)}
		res, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
		want := fmt.Sprintf("unsupported schema_version %d", version)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "understands 1") {
			t.Fatalf("schema_version=%d: want explicit error %q + supported version, got res=%+v err=%v",
				version, want, res, err)
		}
		if res != nil {
			t.Fatalf("schema_version=%d: result must be nil, got %+v", version, res)
		}
	}
	for _, table := range []string{"kc_mastery", "learner_events", "review_items"} {
		if n := lrCount(t, env.tgtDB, table, ""); n != 0 {
			t.Fatalf("unsupported schema must not write %s: %d rows", table, n)
		}
	}
}

// DEF-LEARNING-RESTORE-R2 P2：RestoreLearning 的 tag ref 映射必须按 400
// 一批分批（与导出侧 learningExportRefBatch 对齐），一份携带 >1000 个不同
// external refs 的文档不能撞 SQLite 参数上限，且跨批映射不得丢失或串批。
func TestRestoreLearning_ManyTagRefsResolveAcrossBatches(t *testing.T) {
	env := newLREnv(t)
	ctx := context.Background()
	at := lrTime(2026, 3, 1, 9)

	// 1003 = 400 + 400 + 203：正好压满两个整批再留一个不满的尾批。
	const nTags = 1003
	const baseTag = int64(20000)
	tx, err := env.tgtDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO knowledge_tags (id, name, subject, external_ref) VALUES (?, ?, 'math', ?)`)
	if err != nil {
		t.Fatalf("prepare tag insert: %v", err)
	}
	for i := 0; i < nTags; i++ {
		if _, err := stmt.ExecContext(ctx, baseTag+int64(i),
			fmt.Sprintf("批测点%04d", i), fmt.Sprintf("cnm-restore-%04d", i)); err != nil {
			t.Fatalf("insert tag %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatalf("close stmt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// mastery 每行一个不同 ref（1003 个不同 ref 直接进入 tag ref 集合），
	// 外加 1 个目标机不存在的 ref 验证未知 ref 仍走 skip。
	p := lrBaseExport()
	p.Mastery = make([]model.LearningMastery, 0, nTags+1)
	for i := 0; i < nTags; i++ {
		p.Mastery = append(p.Mastery, model.LearningMastery{
			ExternalRef: fmt.Sprintf("cnm-restore-%04d", i),
			LastEventAt: lrPtr(at),
		})
	}
	p.Mastery = append(p.Mastery, lrMastery("cnm-unknown-on-target", at))

	res, err := env.tgtStore.RestoreLearning(ctx, lrTgtUserID, p)
	if err != nil {
		t.Fatalf("RestoreLearning with %d distinct refs: %v", nTags, err)
	}
	if res.Mastery.Imported != nTags || res.Mastery.Skipped != 1 {
		t.Fatalf("want %d imported / 1 skipped, got %+v", nTags, res.Mastery)
	}
	// 每一行都必须映射到正确的本机 tag id（批间不丢、不串）。
	for i := 0; i < nTags; i += 97 { // 跨三批抽样
		var tagID int64
		if err := env.tgtDB.QueryRow(`SELECT tag_id FROM kc_mastery WHERE user_id = ? AND tag_id = ?`,
			lrTgtUserID, baseTag+int64(i)).Scan(&tagID); err != nil {
			t.Fatalf("mastery for ref %04d missing (batch boundary lost rows?): %v", i, err)
		}
	}
	// 尾批边界（第 1002 个 ref）与首批边界（第 0/399/400 个）逐点核对。
	for _, i := range []int{0, 399, 400, 799, 800, nTags - 1} {
		var n int64
		if err := env.tgtDB.QueryRow(`SELECT COUNT(*) FROM kc_mastery WHERE user_id = ? AND tag_id = ?`,
			lrTgtUserID, baseTag+int64(i)).Scan(&n); err != nil || n != 1 {
			t.Fatalf("batch-boundary ref %04d: rows=%d err=%v", i, n, err)
		}
	}
}
