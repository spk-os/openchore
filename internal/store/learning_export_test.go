package store_test

// TX-LEARNING-EXPORT-TESTER：用 setupStoreWithDB 的真实 SQLite（042 全量
// 迁移、单写连接池）冻结 BuildLearningExport 的导出契约。所有行都用原生
// SQL 直接插入，不经过会顺带重写 rollup 的 RecordLearnerEvent，也不 mock
// store：断言的是 DAO 从真实表读出来的文档。
//
// 只覆盖导出侧。diagnostics/attempts 的恢复属 V5B，这里仅断言它们被完整
// 导出，不宣称往返恢复已通过。

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

const (
	leUserID   = int64(7)
	leOtherID  = int64(8)
	leTagAdd   = int64(9001)
	leTagSub   = int64(9002)
	leTagNoRef = int64(9003)
	leTagEmpty = int64(9004)
	leTagNilAt = int64(9005)
	leRefAdd   = "cnm-add"
	leRefSub   = "cnm-sub"
	leRefNilAt = "cnm-nil-at"
	leTopicRef = "cnm-topic-0001"
	leEventN   = 1007 // 故意超过旧 HTTP 导出的 1000 条上限
)

// ---------------------------------------------------------------------------
// 1) target 不存在
// ---------------------------------------------------------------------------

func TestLearningExport_MissingTarget(t *testing.T) {
	s, db := setupStoreWithDB(t)
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (?, 'Kid', 'child')`, leUserID)

	for _, tc := range []struct {
		name string
		id   int64
	}{
		{"not found", 999999},
		{"zero", 0},
		{"negative", -3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := s.BuildLearningExport(t.Context(), tc.id)
			if !errors.Is(err, store.ErrLearningExportUserNotFound) {
				t.Fatalf("target=%d: want ErrLearningExportUserNotFound, got %v", tc.id, err)
			}
			if doc != nil {
				t.Fatalf("target=%d: doc must be nil on error, got %+v", tc.id, doc)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2) 空文档：所有 slice 非 nil，JSON 不出现 null
// ---------------------------------------------------------------------------

func TestLearningExport_EmptyDocumentSlicesAreNonNil(t *testing.T) {
	s, db := setupStoreWithDB(t)
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (?, '小七', 'child')`, leUserID)

	doc, err := s.BuildLearningExport(t.Context(), leUserID)
	if err != nil {
		t.Fatalf("BuildLearningExport: %v", err)
	}
	if doc == nil {
		t.Fatal("doc is nil")
	}
	if doc.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", doc.SchemaVersion)
	}
	if doc.PackVersion != "cn-math-taxonomy@embedded-v1" {
		t.Fatalf("pack_version = %q, want cn-math-taxonomy@embedded-v1 (embedded pack marker)", doc.PackVersion)
	}
	if doc.ExportedAt.IsZero() {
		t.Fatal("exported_at is zero")
	}
	if doc.TargetUserID != 0 {
		t.Fatalf("target_user_id = %d, want 0 (export never names a restore target)", doc.TargetUserID)
	}
	if doc.User.ID != leUserID || doc.User.Name != "小七" || doc.User.Role != model.RoleChild {
		t.Fatalf("user section = %+v, want id=%d name=小七 role=child", doc.User, leUserID)
	}
	if doc.Mastery == nil || doc.Events == nil || doc.Diagnostics == nil ||
		doc.Attempts == nil || doc.ReviewItems == nil {
		t.Fatalf("a section is nil: mastery=%v events=%v diagnostics=%v attempts=%v review=%v",
			doc.Mastery == nil, doc.Events == nil, doc.Diagnostics == nil,
			doc.Attempts == nil, doc.ReviewItems == nil)
	}
	if len(doc.Mastery) != 0 || len(doc.Events) != 0 || len(doc.Diagnostics) != 0 ||
		len(doc.Attempts) != 0 || len(doc.ReviewItems) != 0 {
		t.Fatalf("empty user leaked rows: %+v", doc)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"mastery":[]`, `"events":[]`, `"diagnostics":[]`,
		`"attempts":[]`, `"review_items":[]`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("JSON missing %s: %s", want, b)
		}
	}
	if strings.Contains(string(b), `:null`) {
		t.Fatalf("JSON contains null: %s", b)
	}
}

// ---------------------------------------------------------------------------
// 3) mastery 只输出非空 external_ref
// ---------------------------------------------------------------------------

func TestLearningExport_MasteryOnlyStableExternalRefs(t *testing.T) {
	s, db := setupStoreWithDB(t)
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (?, 'Kid', 'child')`, leUserID)
	mustExec(t, db, `INSERT INTO knowledge_tags (id, name, subject, external_ref) VALUES
		(?, '加法', 'math', ?), (?, '减法', 'math', ?),
		(?, '自建点', 'math', NULL), (?, '空引用', 'math', ''), (?, '无时间', 'math', ?)`,
		leTagAdd, leRefAdd, leTagSub, leRefSub, leTagNoRef, leTagEmpty, leTagNilAt, leRefNilAt)
	mustExec(t, db, `INSERT INTO kc_mastery
		(user_id, tag_id, correct_count, total_count, mastery, low_confidence, last_event_at) VALUES
		(?, ?, 7, 9, 0.73, 0, '2026-03-01 09:00:00'),
		(?, ?, 3, 10, 0.31, 1, '2026-03-02 10:00:00'),
		(?, ?, 1, 1, 0.99, 0, '2026-03-03 11:00:00'),
		(?, ?, 2, 2, 0.88, 0, '2026-03-04 12:00:00'),
		(?, ?, 4, 6, 0.62, 1, NULL)`,
		leUserID, leTagAdd, leUserID, leTagSub, leUserID, leTagNoRef,
		leUserID, leTagEmpty, leUserID, leTagNilAt)

	doc, err := s.BuildLearningExport(t.Context(), leUserID)
	if err != nil {
		t.Fatalf("BuildLearningExport: %v", err)
	}
	if len(doc.Mastery) != 3 {
		t.Fatalf("mastery = %d rows, want 3 (only non-empty external_ref): %+v", len(doc.Mastery), doc.Mastery)
	}
	byRef := map[string]model.LearningMastery{}
	for _, m := range doc.Mastery {
		if m.ExternalRef == "" {
			t.Fatalf("empty external_ref leaked: %+v", m)
		}
		byRef[m.ExternalRef] = m
	}
	for _, ref := range []string{leRefAdd, leRefSub, leRefNilAt} {
		if _, ok := byRef[ref]; !ok {
			t.Fatalf("missing ref %q in %+v", ref, doc.Mastery)
		}
	}
	a := byRef[leRefAdd]
	if a.CorrectCount != 7 || a.TotalCount != 9 || a.Mastery != 0.73 || a.LowConfidence {
		t.Fatalf("add rollup = %+v", a)
	}
	if a.LastEventAt == nil || a.LastEventAt.UTC().Format(time.DateTime) != "2026-03-01 09:00:00" {
		t.Fatalf("add last_event_at = %v", a.LastEventAt)
	}
	b := byRef[leRefSub]
	if b.CorrectCount != 3 || b.TotalCount != 10 || b.Mastery != 0.31 || !b.LowConfidence {
		t.Fatalf("sub rollup = %+v", b)
	}
	if b.LastEventAt == nil || b.LastEventAt.UTC().Format(time.DateTime) != "2026-03-02 10:00:00" {
		t.Fatalf("sub last_event_at = %v", b.LastEventAt)
	}
	if c := byRef[leRefNilAt]; c.LastEventAt != nil {
		t.Fatalf("NULL last_event_at must stay nil, got %v", c.LastEventAt)
	}
}

// ---------------------------------------------------------------------------
// 4) >1000 事件全量 + kc_ids/topic_id 稳定引用 + 原始 JSON 保留
// ---------------------------------------------------------------------------

func TestLearningExport_AllEventsAndStableRefs(t *testing.T) {
	s, db := setupStoreWithDB(t)
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (?, 'Kid', 'child'), (?, 'Other', 'child')`,
		leUserID, leOtherID)
	mustExec(t, db, `INSERT INTO knowledge_tags (id, name, subject, external_ref) VALUES
		(?, '加法', 'math', ?), (?, '减法', 'math', ?), (?, '自建点', 'math', NULL)`,
		leTagAdd, leRefAdd, leTagSub, leRefSub, leTagNoRef)

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	stmt, err := tx.PrepareContext(t.Context(),
		`INSERT INTO learner_events (user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for i := 0; i < leEventN; i++ {
		// 三个 kc_id：两个有稳定 ref，一个自建无 ref；topic_id 是源对象
		// 的 pack 级引用。JSON 原文必须逐字保留（连空格与键顺序一起）。
		ctx := fmt.Sprintf(`{"subject": "math", "kc_ids":[%d,%d,%d], "topic_id":%q}`,
			leTagAdd, leTagSub, leTagNoRef, leTopicRef)
		result := fmt.Sprintf(`{"success":true,"seq":%d}`, i)
		at := fmt.Sprintf("2026-04-%02d 08:00:00", 1+i%28)
		if _, err := stmt.ExecContext(t.Context(), leUserID, "answered", "review_item",
			leTagAdd+int64(i), result, ctx, at, at); err != nil {
			t.Fatalf("insert event %d: %v", i, err)
		}
	}
	if _, err := stmt.ExecContext(t.Context(), leUserID, "answered", "error_item", 55,
		`{"success":false}`, `not-json`, "2026-05-01 08:00:00", "2026-05-01 08:00:00"); err != nil {
		t.Fatalf("insert malformed event: %v", err)
	}
	if _, err := stmt.ExecContext(t.Context(), leUserID, "completed", "chore", 66,
		nil, nil, "2026-05-02 08:00:00", "2026-05-02 08:00:00"); err != nil {
		t.Fatalf("insert NULL-json event: %v", err)
	}
	if _, err := stmt.ExecContext(t.Context(), leOtherID, "completed", "chore", 77,
		`{"owner":"other"}`, `{"kc_ids":[9001]}`, "2026-05-03 08:00:00", "2026-05-03 08:00:00"); err != nil {
		t.Fatalf("insert other-user event: %v", err)
	}
	if err := stmt.Close(); err != nil {
		t.Fatalf("close stmt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	doc, err := s.BuildLearningExport(t.Context(), leUserID)
	if err != nil {
		t.Fatalf("BuildLearningExport: %v", err)
	}
	if len(doc.Events) != leEventN+2 {
		t.Fatalf("events = %d, want %d (no 1000-row cap, no other-user leak)", len(doc.Events), leEventN+2)
	}
	if doc.Events == nil {
		t.Fatal("events slice is nil")
	}
	for i, e := range doc.Events[:leEventN] {
		wantCtx := fmt.Sprintf(`{"subject": "math", "kc_ids":[%d,%d,%d], "topic_id":%q}`,
			leTagAdd, leTagSub, leTagNoRef, leTopicRef)
		wantResult := fmt.Sprintf(`{"success":true,"seq":%d}`, i)
		if e.ContextJSON != wantCtx || e.ResultJSON != wantResult {
			t.Fatalf("event %d JSON rewritten: context=%q result=%q", i, e.ContextJSON, e.ResultJSON)
		}
		if e.ObjectRef != leTopicRef {
			t.Fatalf("event %d object_ref = %q, want %q", i, e.ObjectRef, leTopicRef)
		}
		if len(e.KCRefs) != 2 || e.KCRefs[0] != leRefAdd || e.KCRefs[1] != leRefSub {
			t.Fatalf("event %d kc_refs = %v, want [%s %s]", i, e.KCRefs, leRefAdd, leRefSub)
		}
		// ID/UserID 清零，但源 ObjectID 必须逐字保留（审计 + V5B 的
		// 「可安全恢复」标记，非目标机 id）。
		if e.ID != 0 || e.UserID != 0 {
			t.Fatalf("event %d kept machine-local row/user id: %+v", i, e.LearnerEvent)
		}
		if e.ObjectID != leTagAdd+int64(i) {
			t.Fatalf("event %d source object_id = %d, want %d preserved verbatim",
				i, e.ObjectID, leTagAdd+int64(i))
		}
		if e.Verb != "answered" || e.ObjectType != "review_item" {
			t.Fatalf("event %d statement skeleton lost: %+v", i, e.LearnerEvent)
		}
	}
	malformed := doc.Events[leEventN]
	if malformed.ContextJSON != "not-json" || malformed.ResultJSON != `{"success":false}` {
		t.Fatalf("malformed context event lost payload: %+v", malformed)
	}
	if malformed.ObjectRef != "" || len(malformed.KCRefs) != 0 || malformed.KCRefs == nil {
		t.Fatalf("malformed context event got refs: %q %v", malformed.ObjectRef, malformed.KCRefs)
	}
	nullJSON := doc.Events[leEventN+1]
	if nullJSON.ContextJSON != "" || nullJSON.ResultJSON != "" {
		t.Fatalf("NULL json columns must scan as empty strings: %+v", nullJSON)
	}
	if nullJSON.KCRefs == nil || len(nullJSON.KCRefs) != 0 || nullJSON.ObjectRef != "" {
		t.Fatalf("NULL json event refs = %v / %q", nullJSON.KCRefs, nullJSON.ObjectRef)
	}
	for _, e := range doc.Events {
		if strings.Contains(e.ResultJSON, `"other"`) {
			t.Fatalf("other user's event leaked: %+v", e)
		}
	}
}

// ---------------------------------------------------------------------------
// 4a-2) KC 外部引用批量解析：>1000 个不同 KC（跨 400 一批的 3 个批次边界），
//       每个事件都要完整映射、不漏、不串位；空输入仍是 []。
// ---------------------------------------------------------------------------

func TestLearningExport_ManyKCRefsResolveAcrossBatches(t *testing.T) {
	s, db := setupStoreWithDB(t)
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (?, 'Kid', 'child')`, leUserID)

	// 1003 = 400 + 400 + 203：正好压满两个批次并留一个不满的尾批，
	// 再加一条无 external_ref 的 tag，验证"无 ref 不冒充引用"在批间也成立。
	const nTags = 1003
	const baseTag = int64(20000)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	tagStmt, err := tx.PrepareContext(t.Context(),
		`INSERT INTO knowledge_tags (id, name, subject, external_ref) VALUES (?, ?, 'math', ?)`)
	if err != nil {
		t.Fatalf("prepare tag insert: %v", err)
	}
	for i := 0; i < nTags; i++ {
		if _, err := tagStmt.ExecContext(t.Context(), baseTag+int64(i),
			fmt.Sprintf("KC-%04d", i), fmt.Sprintf("cnm-batch-%04d", i)); err != nil {
			t.Fatalf("insert tag %d: %v", i, err)
		}
	}
	if err := tagStmt.Close(); err != nil {
		t.Fatalf("close tag stmt: %v", err)
	}
	noRefTag := baseTag + nTags
	if _, err := tx.ExecContext(t.Context(),
		`INSERT INTO knowledge_tags (id, name, subject, external_ref) VALUES (?, '自建点', 'math', NULL)`,
		noRefTag); err != nil {
		t.Fatalf("insert no-ref tag: %v", err)
	}

	stmt, err := tx.PrepareContext(t.Context(),
		`INSERT INTO learner_events (user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at)
		 VALUES (?, 'answered', 'review_item', 0, '{}', ?, '2026-06-01 08:00:00', '2026-06-01 08:00:00')`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// 每个事件引用一个 tag 外加无 ref 的 tag，验证无 ref 不进入 KCRefs。
	for i := 0; i < nTags; i++ {
		ids := fmt.Sprintf("%d,%d", baseTag+int64(i), noRefTag)
		if _, err := stmt.ExecContext(t.Context(), leUserID, fmt.Sprintf(`{"kc_ids":[%s]}`, ids)); err != nil {
			t.Fatalf("insert event %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatalf("close stmt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	doc, err := s.BuildLearningExport(t.Context(), leUserID)
	if err != nil {
		t.Fatalf("BuildLearningExport: %v", err)
	}
	if len(doc.Events) != nTags {
		t.Fatalf("events = %d, want %d", len(doc.Events), nTags)
	}
	seen := make(map[string]bool, nTags)
	for i, e := range doc.Events {
		want := fmt.Sprintf("cnm-batch-%04d", i)
		if len(e.KCRefs) != 1 || e.KCRefs[0] != want {
			t.Fatalf("event %d kc_refs = %v, want [%s] (batch boundary / order broken)", i, e.KCRefs, want)
		}
		if seen[want] {
			t.Fatalf("ref %s exported twice", want)
		}
		seen[want] = true
	}
	if len(seen) != nTags {
		t.Fatalf("distinct refs = %d, want %d (missing mappings silently dropped)", len(seen), nTags)
	}

	// 直接验证批量解析器：空输入返回非 nil 空 map；不存在的 id 不产生条目；
	// 无 ref 的 tag 映射为 ""。
	empty, err := s.ExternalRefsForTagIDs(t.Context(), nil)
	if err != nil {
		t.Fatalf("ExternalRefsForTagIDs(nil): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty input must give non-nil empty map, got %v", empty)
	}
	m, err := s.ExternalRefsForTagIDs(t.Context(), []int64{baseTag, baseTag + 500, baseTag + nTags, 999999})
	if err != nil {
		t.Fatalf("ExternalRefsForTagIDs: %v", err)
	}
	if m[baseTag] != "cnm-batch-0000" || m[baseTag+500] != "cnm-batch-0500" {
		t.Fatalf("id->ref mapping wrong: %v", m)
	}
	if ref, ok := m[baseTag+nTags]; !ok || ref != "" {
		t.Fatalf("no-external_ref tag must map to empty string, got %q ok=%v", ref, ok)
	}
	if _, ok := m[999999]; ok {
		t.Fatalf("unknown id must not appear in the map: %v", m)
	}
}

// ---------------------------------------------------------------------------
// 4b) context_json.topic_id 只进 ObjectRef：不改 object_type / object_id，
//     也不被当成目标机对象 id。
// ---------------------------------------------------------------------------

func TestLearningExport_TopicIDIsObjectRefOnly(t *testing.T) {
	s, db := setupStoreWithDB(t)
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (?, 'Kid', 'child')`, leUserID)
	mustExec(t, db, `INSERT INTO knowledge_tags (id, name, subject, external_ref) VALUES
		(?, '加法', 'math', ?)`, leTagAdd, leRefAdd)

	events := []struct {
		objectType string
		objectID   int64
		context    string
	}{
		{"chore", 4242, fmt.Sprintf(`{"topic_id":%q,"kc_ids":[%d]}`, leTopicRef, leTagAdd)},
		{"review_item", 0, fmt.Sprintf(`{"topic_id":%q}`, leTopicRef)},
		{"error_item", 55, fmt.Sprintf(`{"topic_id":%q}`, leTopicRef)},
	}
	for _, e := range events {
		mustExec(t, db, `INSERT INTO learner_events
			(user_id, verb, object_type, object_id, result_json, context_json, occurred_at, created_at)
			VALUES (?, 'answered', ?, ?, '{}', ?, '2026-05-01 08:00:00', '2026-05-01 08:00:00')`,
			leUserID, e.objectType, e.objectID, e.context)
	}

	doc, err := s.BuildLearningExport(t.Context(), leUserID)
	if err != nil {
		t.Fatalf("BuildLearningExport: %v", err)
	}
	if len(doc.Events) != len(events) {
		t.Fatalf("events = %d, want %d", len(doc.Events), len(events))
	}
	for i, e := range doc.Events {
		wantType, wantID := events[i].objectType, events[i].objectID
		if e.ObjectType != wantType {
			t.Fatalf("event %d object_type = %q, want %q (topic_id must not rewrite it)",
				i, e.ObjectType, wantType)
		}
		if e.ObjectID != wantID {
			t.Fatalf("event %d object_id = %d, want %d (topic_id must not be mapped onto it)",
				i, e.ObjectID, wantID)
		}
		if e.ObjectRef != leTopicRef {
			t.Fatalf("event %d object_ref = %q, want %q", i, e.ObjectRef, leTopicRef)
		}
		if e.ID != 0 || e.UserID != 0 {
			t.Fatalf("event %d local ids leaked: id=%d user=%d", i, e.ID, e.UserID)
		}
	}
	if len(doc.Events[0].KCRefs) != 1 || doc.Events[0].KCRefs[0] != leRefAdd {
		t.Fatalf("event 0 kc_refs = %v, want [%s]", doc.Events[0].KCRefs, leRefAdd)
	}
	for i := 1; i < len(doc.Events); i++ {
		if doc.Events[i].KCRefs == nil || len(doc.Events[i].KCRefs) != 0 {
			t.Fatalf("event %d kc_refs = %v, want non-nil empty", i, doc.Events[i].KCRefs)
		}
	}
}

// ---------------------------------------------------------------------------
// 5) review 卡的稳定 SourceRef：同用户 KC / 同用户错题通过，其余跳过
// ---------------------------------------------------------------------------

func TestLearningExport_ReviewItemsStableSourceRefs(t *testing.T) {
	s, db := setupStoreWithDB(t)
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (?, 'Kid', 'child'), (?, 'Other', 'child')`,
		leUserID, leOtherID)
	mustExec(t, db, `INSERT INTO knowledge_tags (id, name, subject, external_ref) VALUES
		(?, '加法', 'math', ?), (?, '自建点', 'math', NULL), (?, '空引用', 'math', '')`,
		leTagAdd, leRefAdd, leTagNoRef, leTagEmpty)
	mustExec(t, db, `INSERT INTO error_items (id, user_id, original_image_url, source) VALUES
		(4101, ?, 'q17.png', 'taxonomy:Q17'),
		(4102, ?, 'other.png', 'taxonomy:Q-OTHER'),
		(4103, ?, 'null.png', NULL),
		(4104, ?, 'empty.png', '')`,
		leUserID, leOtherID, leUserID, leUserID)

	card := `INSERT INTO review_items (id, user_id, source_type, source_id, front_text, back_text, subject,
			due, stability, difficulty, scheduled_days, reps, lapses, state, last_review, remaining_steps, suspended, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	rows := []struct {
		id         int64
		user       int64
		typ        string
		sourceID   int64
		lastReview any
	}{
		{501, leUserID, model.ReviewSourceKC, leTagAdd, "2026-03-01 09:00:00"},
		{502, leUserID, model.ReviewSourceKC, leTagNoRef, nil}, // NULL external_ref
		{503, leUserID, model.ReviewSourceKC, leTagEmpty, nil}, // '' external_ref
		{504, leUserID, model.ReviewSourceErrorItem, 4101, "2026-03-02 09:00:00"},
		{505, leUserID, model.ReviewSourceErrorItem, 4102, nil}, // 别人的错题
		{506, leUserID, model.ReviewSourceErrorItem, 4103, nil}, // source IS NULL
		{507, leUserID, model.ReviewSourceErrorItem, 4104, nil}, // source = ''
		{508, leUserID, model.ReviewSourceOutcome, 777, nil},    // 无稳定 ref 空间
		{509, leOtherID, model.ReviewSourceKC, 9101, nil},       // 别人的卡
	}
	for _, r := range rows {
		mustExec(t, db, card, r.id, r.user, r.typ, r.sourceID,
			fmt.Sprintf("front-%d", r.id), "back", "math",
			"2026-12-31 23:00:00", 12.5, 6.25, 9, 4, 1, model.ReviewStateReview,
			r.lastReview, 2, 1, "2026-01-10 09:00:00")
	}

	doc, err := s.BuildLearningExport(t.Context(), leUserID)
	if err != nil {
		t.Fatalf("BuildLearningExport: %v", err)
	}
	if doc.ReviewItems == nil {
		t.Fatal("review_items slice is nil")
	}
	if len(doc.ReviewItems) != 2 {
		t.Fatalf("review_items = %d, want 2 (stable refs only): %+v", len(doc.ReviewItems), doc.ReviewItems)
	}
	if doc.ReviewItems[0].ID != 501 || doc.ReviewItems[0].SourceRef != leRefAdd ||
		doc.ReviewItems[0].SourceType != model.ReviewSourceKC ||
		doc.ReviewItems[0].SourceID != leTagAdd {
		t.Fatalf("KC card = %+v", doc.ReviewItems[0])
	}
	if doc.ReviewItems[1].ID != 504 || doc.ReviewItems[1].SourceRef != "taxonomy:Q17" ||
		doc.ReviewItems[1].SourceType != model.ReviewSourceErrorItem ||
		doc.ReviewItems[1].SourceID != 4101 {
		t.Fatalf("error-item card = %+v", doc.ReviewItems[1])
	}
	for i, it := range doc.ReviewItems {
		if it.UserID != leUserID {
			t.Fatalf("card %d user_id = %d, want target %d", i, it.UserID, leUserID)
		}
		if it.Stability != 12.5 || it.Difficulty != 6.25 || it.ScheduledDays != 9 ||
			it.Reps != 4 || it.Lapses != 1 || it.State != model.ReviewStateReview ||
			it.RemainingSteps != 2 || !it.Suspended {
			t.Fatalf("card %d FSRS fields lost: %+v", i, it.ReviewItem)
		}
		if it.LastReview == nil || it.LastReview.UTC().Format(time.DateTime) == "" {
			t.Fatalf("card %d last_review lost: %+v", i, it.ReviewItem)
		}
	}
	// 绝不能把整数 id 当 ref 发出去。
	for _, it := range doc.ReviewItems {
		if it.SourceRef == "501" || it.SourceRef == "504" || it.SourceRef == "4101" {
			t.Fatalf("integer id used as source_ref: %+v", it)
		}
	}
}

// ---------------------------------------------------------------------------
// 5b) 错题卡的 taxonomy ref 安全边界：只有同用户、taxonomy: 前缀、非空后缀、
//     且同用户唯一 source 的卡才导出；空白/重复/manual/普通文本/他人 source
//     一律跳过，绝不猜一行 id。
// ---------------------------------------------------------------------------

func TestLearningExport_ErrorItemRefSafety(t *testing.T) {
	s, db := setupStoreWithDB(t)
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (?, 'Kid', 'child'), (?, 'Other', 'child')`,
		leUserID, leOtherID)

	// 每个 source 都配一张卡；期望只有 taxonomy:OK / taxonomy:SHARED 两张导出。
	items := []struct {
		id     int64
		user   int64
		source any
	}{
		{4201, leUserID, "taxonomy:"},       // 空后缀
		{4202, leUserID, "taxonomy:   "},    // 纯空格后缀
		{4203, leUserID, "taxonomy:DUP"},    // 同用户重复 source
		{4204, leUserID, "taxonomy:DUP"},    // 同用户重复 source
		{4205, leUserID, "期中考试"},            // manual / paper-title
		{4206, leUserID, "plain text"},      // 普通文本
		{4207, leUserID, "taxonomy:OK"},     // 唯一稳定 ref
		{4208, leUserID, "taxonomy:SHARED"}, // 唯一稳定 ref
		{4209, leUserID, "taxonomy"},        // 缺冒号
		{4210, leUserID, "Taxonomy:UPPER"},  // 大小写不符
		{4301, leOtherID, "taxonomy:OK"},    // 他人同 source：不得影响本用户唯一性
		{4302, leOtherID, "taxonomy:OTHER"}, // 他人卡源
	}
	for _, it := range items {
		mustExec(t, db, `INSERT INTO error_items (id, user_id, original_image_url, source) VALUES (?, ?, ?, ?)`,
			it.id, it.user, fmt.Sprintf("img-%d.png", it.id), it.source)
		mustExec(t, db, `INSERT INTO review_items (id, user_id, source_type, source_id, front_text, back_text,
			subject, due, state) VALUES (?, ?, 'error_item', ?, ?, '', 'math', '2026-12-31 23:00:00', 'Review')`,
			it.id+1000, it.user, it.id, fmt.Sprintf("front-%d", it.id))
	}

	doc, err := s.BuildLearningExport(t.Context(), leUserID)
	if err != nil {
		t.Fatalf("BuildLearningExport: %v", err)
	}
	if len(doc.ReviewItems) != 2 {
		t.Fatalf("review_items = %d, want 2 (taxonomy:OK + taxonomy:SHARED): %+v",
			len(doc.ReviewItems), doc.ReviewItems)
	}
	got := map[int64]string{}
	for _, it := range doc.ReviewItems {
		got[it.SourceID] = it.SourceRef
	}
	if got[4207] != "taxonomy:OK" {
		t.Fatalf("4207 ref = %q, want taxonomy:OK", got[4207])
	}
	if got[4208] != "taxonomy:SHARED" {
		t.Fatalf("4208 ref = %q, want taxonomy:SHARED (cross-user duplicate must not block)", got[4208])
	}
	for _, bad := range []int64{4201, 4202, 4203, 4204, 4205, 4206, 4209, 4210, 4301, 4302} {
		if ref, ok := got[bad]; ok {
			t.Fatalf("error item %d must be skipped, exported ref %q", bad, ref)
		}
	}
	for _, it := range doc.ReviewItems {
		if it.SourceRef == "" || !strings.HasPrefix(it.SourceRef, "taxonomy:") {
			t.Fatalf("non taxonomy ref leaked: %+v", it)
		}
		if it.SourceRef == fmt.Sprintf("%d", it.SourceID) {
			t.Fatalf("integer id used as source_ref: %+v", it)
		}
	}
}

// ---------------------------------------------------------------------------
// 5c) 缺陷记录：契约要求 taxonomy: 后缀「非空白」，实现用 SQL
//     TRIM(SUBSTR(ei.source, 10))，而 SQLite 的 TRIM 默认只去 ASCII 空格，
//     制表符/换行后缀会被当成有效 ref 导出。此用例应保持失败直到生产修复。
// ---------------------------------------------------------------------------

func TestLearningExport_ErrorItemBlankSuffixMustNotExport(t *testing.T) {
	s, db := setupStoreWithDB(t)
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (?, 'Kid', 'child')`, leUserID)

	for id, source := range map[int64]string{
		4601: "taxonomy:\t",
		4602: "taxonomy:\n",
		4603: "taxonomy:\r",
	} {
		mustExec(t, db, `INSERT INTO error_items (id, user_id, original_image_url, source) VALUES (?, ?, ?, ?)`,
			id, leUserID, fmt.Sprintf("img-%d.png", id), source)
		mustExec(t, db, `INSERT INTO review_items (id, user_id, source_type, source_id, front_text, back_text,
			subject, due, state) VALUES (?, ?, 'error_item', ?, ?, '', 'math', '2026-12-31 23:00:00', 'Review')`,
			id+1000, leUserID, id, fmt.Sprintf("front-%d", id))
	}

	doc, err := s.BuildLearningExport(t.Context(), leUserID)
	if err != nil {
		t.Fatalf("BuildLearningExport: %v", err)
	}
	if len(doc.ReviewItems) != 0 {
		t.Fatalf("blank taxonomy suffix exported %d card(s), want 0: %#v",
			len(doc.ReviewItems), doc.ReviewItems)
	}
}

// ---------------------------------------------------------------------------
// 6) diagnostics / attempts：按目标用户全量导出，字段保持
// ---------------------------------------------------------------------------

func TestLearningExport_DiagnosticsAndAttempts(t *testing.T) {
	s, db := setupStoreWithDB(t)
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (?, 'Kid', 'child'), (?, 'Other', 'child')`,
		leUserID, leOtherID)
	mustExec(t, db, `INSERT INTO knowledge_tags (id, name, subject, external_ref) VALUES
		(?, '加法', 'math', ?), (?, '减法', 'math', ?)`, leTagAdd, leRefAdd, leTagSub, leRefSub)
	mustExec(t, db, `INSERT INTO knowledge_questions (id, external_ref, topic_id, type, prompt) VALUES
		(5001, 'cnm-q1', ?, 'numeric', '1+1'), (5002, 'cnm-q2', ?, 'numeric', '2+2')`,
		leTagAdd, leTagSub)

	ctx := t.Context()
	mine := &model.DiagnosticSession{
		UserID: leUserID, Subject: "math", Mode: store.DiagnosticModeDiagnose,
		Status: store.DiagnosticStatusActive, ScopeJSON: `{"grade":1}`,
		StateJSON: `{"cursor":0}`, SummaryJSON: `{"score":0.8}`,
	}
	if err := s.CreateDiagnosticSession(ctx, mine); err != nil {
		t.Fatalf("CreateDiagnosticSession(mine): %v", err)
	}
	theirs := &model.DiagnosticSession{
		UserID: leOtherID, Subject: "chinese", Mode: store.DiagnosticModeDiagnose,
		Status: store.DiagnosticStatusActive, ScopeJSON: `{"grade":2}`,
		StateJSON: `{"cursor":9}`, SummaryJSON: `{"score":0.1}`,
	}
	if err := s.CreateDiagnosticSession(ctx, theirs); err != nil {
		t.Fatalf("CreateDiagnosticSession(theirs): %v", err)
	}
	yes, no := true, false
	mineAttempts := []*model.DiagnosticAttempt{
		{SessionID: mine.ID, Seq: 1, Role: "probe", AnswerJSON: `"7"`, Correct: &yes, Status: "answered"},
		{SessionID: mine.ID, Seq: 2, Role: "confirm", AnswerJSON: `"8"`, Correct: &no, Status: "corrected"},
	}
	for i, a := range mineAttempts {
		qID, tagID := int64(5001), leTagAdd
		if i == 1 {
			qID, tagID = 5002, leTagSub
		}
		if err := s.CreateDiagnosticAttempt(ctx, a, qID, tagID); err != nil {
			t.Fatalf("CreateDiagnosticAttempt(%d): %v", i, err)
		}
	}
	if err := s.CreateDiagnosticAttempt(ctx, &model.DiagnosticAttempt{
		SessionID: theirs.ID, Seq: 1, Role: "probe", AnswerJSON: `"other"`, Status: "answered",
	}, 5001, leTagAdd); err != nil {
		t.Fatalf("CreateDiagnosticAttempt(other): %v", err)
	}

	doc, err := s.BuildLearningExport(ctx, leUserID)
	if err != nil {
		t.Fatalf("BuildLearningExport: %v", err)
	}
	if doc.Diagnostics == nil || doc.Attempts == nil {
		t.Fatalf("diagnostics=%v attempts=%v must be non-nil", doc.Diagnostics == nil, doc.Attempts == nil)
	}
	if len(doc.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1 (target user only): %+v", len(doc.Diagnostics), doc.Diagnostics)
	}
	d := doc.Diagnostics[0]
	if d.ID != mine.ID || d.UserID != leUserID || d.Subject != "math" ||
		d.ScopeJSON != `{"grade":1}` || d.StateJSON != `{"cursor":0}` || d.SummaryJSON != `{"score":0.8}` {
		t.Fatalf("diagnostic session = %+v", d)
	}
	if len(doc.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2: %+v", len(doc.Attempts), doc.Attempts)
	}
	want := []struct {
		seq                             int
		question, topic, answer, status string
		correct                         bool
	}{
		{1, "cnm-q1", leRefAdd, `"7"`, "answered", true},
		{2, "cnm-q2", leRefSub, `"8"`, "corrected", false},
	}
	for i, w := range want {
		a := doc.Attempts[i]
		if a.SessionID != mine.ID || a.Seq != w.seq || a.QuestionID != w.question ||
			a.TopicID != w.topic || a.AnswerJSON != w.answer || a.Status != w.status {
			t.Fatalf("attempt %d = %+v, want %+v", i, a, w)
		}
		if a.Correct == nil || *a.Correct != w.correct {
			t.Fatalf("attempt %d correct = %v, want %v", i, a.Correct, w.correct)
		}
	}
	for _, a := range doc.Attempts {
		if a.AnswerJSON == `"other"` || a.SessionID == theirs.ID {
			t.Fatalf("other user's attempt leaked: %+v", a)
		}
	}
}

// ---------------------------------------------------------------------------
// 6b) diagnostics 边界：无 attempt 的会话不得伪造 attempt 行；attempt 的
//     question/topic/correct/voided 都可空，且审计字段完整导出。
// ---------------------------------------------------------------------------

func TestLearningExport_DiagnosticsEmptySessionAndNullables(t *testing.T) {
	s, db := setupStoreWithDB(t)
	mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (?, 'Kid', 'child')`, leUserID)

	// 会话 900：无任何 attempt，但撤销/恢复/完成时间都要保留。
	mustExec(t, db, `INSERT INTO diagnostic_sessions
		(id, user_id, subject, mode, status, scope_json, state_json, summary_json,
		 voided_at, voided_by, restored_at, completed_at)
		VALUES (900, ?, 'math', 'diagnose', 'voided', '{}', '{}', NULL,
		 '2026-03-01 09:00:00', ?, '2026-03-02 09:00:00', '2026-03-03 09:00:00')`,
		leUserID, leUserID)
	// 会话 901：一条内部题都不存在的 attempt（question_id/topic_id/correct 全 NULL）。
	mustExec(t, db, `INSERT INTO diagnostic_sessions
		(id, user_id, subject, mode, status, scope_json, state_json, summary_json)
		VALUES (901, ?, 'math', 'diagnose', 'active', '{}', '{}', '{"score":0.5}')`, leUserID)
	mustExec(t, db, `INSERT INTO diagnostic_attempts
		(session_id, question_id, topic_id, seq, role, answer_json, correct, status, voided_at)
		VALUES (901, NULL, NULL, 1, 'probe', '"?"', NULL, 'voided', '2026-03-04 09:00:00')`)

	doc, err := s.BuildLearningExport(t.Context(), leUserID)
	if err != nil {
		t.Fatalf("BuildLearningExport: %v", err)
	}
	if len(doc.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %d, want 2: %+v", len(doc.Diagnostics), doc.Diagnostics)
	}
	if len(doc.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1 (empty session must not fabricate one): %+v",
			len(doc.Attempts), doc.Attempts)
	}
	d := doc.Diagnostics[0]
	if d.ID != 900 || d.UserID != leUserID || d.Status != "voided" {
		t.Fatalf("session 900 = %+v", d)
	}
	if d.SummaryJSON != "" {
		t.Fatalf("NULL summary_json must stay empty, got %q", d.SummaryJSON)
	}
	if d.VoidedAt == nil || d.VoidedAt.UTC().Format(time.DateTime) != "2026-03-01 09:00:00" {
		t.Fatalf("session 900 voided_at = %v", d.VoidedAt)
	}
	if d.VoidedBy == nil || *d.VoidedBy != leUserID {
		t.Fatalf("session 900 voided_by = %v", d.VoidedBy)
	}
	if d.RestoredAt == nil || d.RestoredAt.UTC().Format(time.DateTime) != "2026-03-02 09:00:00" {
		t.Fatalf("session 900 restored_at = %v", d.RestoredAt)
	}
	if d.CompletedAt == nil || d.CompletedAt.UTC().Format(time.DateTime) != "2026-03-03 09:00:00" {
		t.Fatalf("session 900 completed_at = %v", d.CompletedAt)
	}
	if doc.Diagnostics[1].ID != 901 || doc.Diagnostics[1].SummaryJSON != `{"score":0.5}` {
		t.Fatalf("session 901 = %+v", doc.Diagnostics[1])
	}
	a := doc.Attempts[0]
	if a.SessionID != 901 || a.Seq != 1 || a.Role != "probe" || a.AnswerJSON != `"?"` {
		t.Fatalf("attempt = %+v", a)
	}
	if a.QuestionID != "" || a.TopicID != "" {
		t.Fatalf("NULL question/topic must scan as empty refs, got %q/%q", a.QuestionID, a.TopicID)
	}
	if a.Correct != nil {
		t.Fatalf("NULL correct must stay nil, got %v", *a.Correct)
	}
	if a.Status != "voided" || a.VoidedAt == nil ||
		a.VoidedAt.UTC().Format(time.DateTime) != "2026-03-04 09:00:00" || a.CorrectedByAt != nil {
		t.Fatalf("attempt audit fields = %+v", a)
	}
}

// ---------------------------------------------------------------------------
// 7) 任一 DAO 的 DB 错误必须上抛，不能被吞成「空导出」
// ---------------------------------------------------------------------------

func TestLearningExport_DBErrorsPropagate(t *testing.T) {
	for _, table := range []string{"kc_mastery", "learner_events", "diagnostic_sessions",
		"diagnostic_attempts", "review_items"} {
		t.Run(table, func(t *testing.T) {
			s, db := setupStoreWithDB(t)
			mustExec(t, db, `INSERT INTO users (id, name, role) VALUES (?, 'Kid', 'child')`, leUserID)
			// 让导出链上恰好一个查询失败；其余查询正常返回空集。
			mustExec(t, db, `DROP TABLE `+table)
			doc, err := s.BuildLearningExport(t.Context(), leUserID)
			if err == nil {
				t.Fatalf("DROP %s: error was swallowed into a successful export: %+v", table, doc)
			}
			if doc != nil {
				t.Fatalf("DROP %s: doc must be nil on error, got %+v", table, doc)
			}
			if !strings.Contains(err.Error(), table) {
				t.Fatalf("DROP %s: error does not name the failing stage: %v", table, err)
			}
		})
	}
}
