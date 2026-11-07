package store_test

// TX-KC-REVIEW-SYNC-TESTER: Store.SyncKCReviewCard 的真实 SQLite 专项测试。
//
// 每个用例都跑在 setupStoreWithDB 之上：全量 042 迁移的内存 SQLite、
// MaxOpenConns=1 单写者连接池（与生产一致），不使用 mock。断言方式是
// review_items 全 18 列的快照比较 —— 任何一个 FSRS 调度列被重置、或
// 任何一张不该动的行被改写，都会立即暴露；单写者池也顺带验证了事务
// 内部只使用 tx（若改用 s.db 取第二个连接，测试会直接超时）。
//
// 语义依据：任务卡 TX-KC-REVIEW-SYNC-TESTER 的冻结契约 + 被测实现
// internal/store/taxonomy_suspend.go 的文件头注释。

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
	"github.com/liftedkilt/openchore/internal/taxonomy"
)

// kcCardRow 是 review_items 全部 18 列的快照。所有字段都可比较，
// 所以 "整行没动" 可以用 == 精确断言。
type kcCardRow struct {
	ID, UserID, SourceID                     int64
	SourceType, FrontText, BackText, Subject string
	Due                                      string
	Stability, Difficulty                    float64
	ScheduledDays, Reps, Lapses              int64
	State                                    string
	LastReview                               sql.NullString
	RemainingSteps, Suspended                int64
	CreatedAt                                string
}

const kcSyncColumns = `id, user_id, source_type, source_id, front_text, back_text, subject,
	CAST(due AS TEXT), stability, difficulty, scheduled_days, reps, lapses, state,
	CAST(last_review AS TEXT), remaining_steps, suspended, CAST(created_at AS TEXT)`

type kcSyncScanner interface {
	Scan(dest ...any) error
}

func kcSyncScan(sc kcSyncScanner) (kcCardRow, error) {
	var r kcCardRow
	err := sc.Scan(
		&r.ID, &r.UserID, &r.SourceType, &r.SourceID, &r.FrontText, &r.BackText, &r.Subject,
		&r.Due, &r.Stability, &r.Difficulty, &r.ScheduledDays, &r.Reps, &r.Lapses, &r.State,
		&r.LastReview, &r.RemainingSteps, &r.Suspended, &r.CreatedAt)
	return r, err
}

func kcSyncCard(t *testing.T, db *sql.DB, sourceType string, sourceID int64) kcCardRow {
	t.Helper()
	r, err := kcSyncScan(db.QueryRow(
		`SELECT `+kcSyncColumns+` FROM review_items WHERE source_type = ? AND source_id = ?`,
		sourceType, sourceID))
	if err != nil {
		t.Fatalf("读取 review_items(%s,%d): %v", sourceType, sourceID, err)
	}
	return r
}

func kcSyncCards(t *testing.T, db *sql.DB) []kcCardRow {
	t.Helper()
	rows, err := db.Query(`SELECT ` + kcSyncColumns + ` FROM review_items ORDER BY id`)
	if err != nil {
		t.Fatalf("查询全部 review_items: %v", err)
	}
	defer rows.Close()
	var out []kcCardRow
	for rows.Next() {
		r, err := kcSyncScan(rows)
		if err != nil {
			t.Fatalf("扫描 review_items 行: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 review_items: %v", err)
	}
	return out
}

func assertKCSyncCards(t *testing.T, db *sql.DB, want []kcCardRow) {
	t.Helper()
	got := kcSyncCards(t, db)
	if len(got) != len(want) {
		t.Fatalf("review_items 行数 = %d, 期望 %d\ngot:  %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 行被改写\ngot:  %+v\nwant: %+v", i, got[i], want[i])
		}
	}
}

// mastered 只应把 suspended 从 0 翻到 1；其余 17 列一律原样保留。
func assertKCSyncOnlySuspendedChanged(t *testing.T, before, after kcCardRow) {
	t.Helper()
	if before.Suspended != 0 || after.Suspended != 1 {
		t.Fatalf("suspended 应为 0→1，实际 %d→%d", before.Suspended, after.Suspended)
	}
	after.Suspended = before.Suspended
	if after != before {
		t.Fatalf("除 suspended 外还有列被改写\nbefore: %+v\nafter:  %+v", before, after)
	}
}

type kcSyncFixture struct {
	s                   *store.Store
	db                  *sql.DB
	userID, otherUserID int64
	tagID               int64
	tagName, tagSubject string
	tagDesc             string
}

func newKCSyncFixture(t *testing.T) *kcSyncFixture {
	t.Helper()
	s, db := setupStoreWithDB(t)
	kid := createTestUser(t, s, "kc-sync-target", "child")
	other := createTestUser(t, s, "kc-sync-other", "child")
	f := &kcSyncFixture{
		s: s, db: db, userID: kid.ID, otherUserID: other.ID,
		tagName: "分数的基本性质", tagSubject: "math",
		tagDesc: "分子分母同乘一个非零数，分数值不变",
	}
	f.tagID = f.insertTag(t, f.tagName, f.tagSubject, f.tagDesc, "kc-sync-frac")
	return f
}

func (f *kcSyncFixture) insertTag(t *testing.T, name, subject, desc, ref string) int64 {
	t.Helper()
	res, err := f.db.Exec(
		`INSERT INTO knowledge_tags (name, subject, description, external_ref, source)
		 VALUES (?, ?, ?, ?, 'test')`, name, subject, desc, ref)
	if err != nil {
		t.Fatalf("插入 knowledge_tags(%s): %v", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("knowledge_tags(%s) 的 id: %v", name, err)
	}
	return id
}

// seedFullCard 插入一张 18 列全显式赋值、FSRS 调度值全非默认的卡片
// （含 last_review、state=Review、remaining_steps）：任何
// "重置为 New/清零/调度列回默认" 的实现都会在快照比较中暴露。
func (f *kcSyncFixture) seedFullCard(t *testing.T, userID, sourceID int64, sourceType string, suspended int64) kcCardRow {
	t.Helper()
	_, err := f.db.Exec(
		`INSERT INTO review_items (user_id, source_type, source_id, front_text, back_text, subject,
			due, stability, difficulty, scheduled_days, reps, lapses, state, last_review,
			remaining_steps, suspended, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, sourceType, sourceID, "既有卡正面", "既有卡背面", "math",
		"2027-01-02T03:04:05Z", 12.5, 6.25, 9, 4, 1, "Review",
		"2026-11-01T10:20:30Z", 2, suspended, "2026-10-01T08:00:00Z",
	)
	if err != nil {
		t.Fatalf("插入完整 review_items(%s,%d): %v", sourceType, sourceID, err)
	}
	return kcSyncCard(t, f.db, sourceType, sourceID)
}

func kcSyncCall(t *testing.T, f *kcSyncFixture, ctx context.Context, userID, tagID int64, status string, wantChanged bool) {
	t.Helper()
	changed, err := f.s.SyncKCReviewCard(ctx, userID, tagID, status)
	if err != nil {
		t.Fatalf("SyncKCReviewCard(user=%d,tag=%d,status=%s) 返回错误: %v", userID, tagID, status, err)
	}
	if changed != wantChanged {
		t.Fatalf("changed = %v, 期望 %v (status=%s)", changed, wantChanged, status)
	}
}

// assertKCSyncNewCard 校验 weak/shaky 新建的卡片字段有效，且文本/学科
// 元数据确实来自 knowledge_tags，而不是硬编码或调用方传入。
func assertKCSyncNewCard(t *testing.T, f *kcSyncFixture, tagID int64, name, subject, desc string) kcCardRow {
	t.Helper()
	cards := kcSyncCards(t, f.db)
	if len(cards) != 1 {
		t.Fatalf("期望恰好 1 张卡，实际 %d: %+v", len(cards), cards)
	}
	c := cards[0]
	if c.ID <= 0 || c.UserID != f.userID {
		t.Fatalf("卡片 id/user_id 无效: %+v", c)
	}
	if c.SourceType != model.ReviewSourceKC || c.SourceID != tagID {
		t.Fatalf("卡片来源不是目标 KC: %+v", c)
	}
	if c.FrontText != name || c.BackText != desc || c.Subject != subject {
		t.Fatalf("卡片文本/学科未来自 knowledge_tags: %+v (want front=%q back=%q subject=%q)",
			c, name, desc, subject)
	}
	if c.FrontText == "" || c.BackText == "" || c.Subject == "" {
		t.Fatalf("卡片字段不得为空: %+v", c)
	}
	if c.State != model.ReviewStateNew || c.Suspended != 0 {
		t.Fatalf("新卡应为 state=New/suspended=0: %+v", c)
	}
	if c.Stability != 0 || c.Difficulty != 0 || c.ScheduledDays != 0 ||
		c.Reps != 0 || c.Lapses != 0 || c.RemainingSteps != 0 {
		t.Fatalf("新卡不应预置 FSRS 调度进度: %+v", c)
	}
	if c.LastReview.Valid {
		t.Fatalf("新卡不应有 last_review: %+v", c)
	}
	if c.CreatedAt == "" || c.Due == "" {
		t.Fatalf("新卡 due/created_at 不得为空: %+v", c)
	}
	due, err := time.Parse(time.RFC3339, c.Due)
	if err != nil {
		t.Fatalf("due %q 不是合法 RFC3339 时间: %v", c.Due, err)
	}
	if d := math.Abs(time.Since(due).Minutes()); d > 5 {
		t.Fatalf("due %q 不在当前时刻附近 (偏差 %.2f 分钟)", c.Due, d)
	}
	return c
}

// ── 1. mastered：只挂起目标用户 active KC 卡 ────────────────────────────

func TestSyncKCReviewCard_MasteredSuspendsOnlyTargetActiveCard(t *testing.T) {
	ctx := context.Background()

	t.Run("active_card_suspended", func(t *testing.T) {
		f := newKCSyncFixture(t)
		before := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceKC, 0)
		kcSyncCall(t, f, ctx, f.userID, f.tagID, taxonomy.StatusMastered, true)
		after := kcSyncCard(t, f.db, model.ReviewSourceKC, f.tagID)
		assertKCSyncOnlySuspendedChanged(t, before, after)
		assertKCSyncCards(t, f.db, []kcCardRow{after})
	})

	t.Run("repeat_is_idempotent", func(t *testing.T) {
		f := newKCSyncFixture(t)
		f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceKC, 0)
		kcSyncCall(t, f, ctx, f.userID, f.tagID, taxonomy.StatusMastered, true)
		afterFirst := kcSyncCard(t, f.db, model.ReviewSourceKC, f.tagID)
		for i := 2; i <= 4; i++ {
			kcSyncCall(t, f, ctx, f.userID, f.tagID, taxonomy.StatusMastered, false)
			if got := kcSyncCard(t, f.db, model.ReviewSourceKC, f.tagID); got != afterFirst {
				t.Fatalf("第 %d 次 mastered 改写了卡片: %+v (期望 %+v)", i, got, afterFirst)
			}
		}
		assertKCSyncCards(t, f.db, []kcCardRow{afterFirst})
	})

	t.Run("missing_card_not_created", func(t *testing.T) {
		f := newKCSyncFixture(t)
		kcSyncCall(t, f, ctx, f.userID, f.tagID, taxonomy.StatusMastered, false)
		assertKCSyncCards(t, f.db, nil)
	})

	t.Run("already_suspended_not_touched", func(t *testing.T) {
		f := newKCSyncFixture(t)
		before := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceKC, 1)
		kcSyncCall(t, f, ctx, f.userID, f.tagID, taxonomy.StatusMastered, false)
		assertKCSyncCards(t, f.db, []kcCardRow{before})
	})
}

// ── 2. weak/shaky：缺卡建卡，字段来自 knowledge_tags ──────────────────────

func TestSyncKCReviewCard_WeakShakyCreateCard(t *testing.T) {
	ctx := context.Background()

	for _, status := range []string{taxonomy.StatusWeak, taxonomy.StatusShaky} {
		t.Run(status+"_missing_card_created", func(t *testing.T) {
			f := newKCSyncFixture(t)
			kcSyncCall(t, f, ctx, f.userID, f.tagID, status, true)
			assertKCSyncNewCard(t, f, f.tagID, f.tagName, f.tagSubject, f.tagDesc)
		})

		t.Run(status+"_repeat_does_not_duplicate_or_reset", func(t *testing.T) {
			f := newKCSyncFixture(t)
			kcSyncCall(t, f, ctx, f.userID, f.tagID, status, true)
			first := assertKCSyncNewCard(t, f, f.tagID, f.tagName, f.tagSubject, f.tagDesc)

			// 模拟 go-fsrs 打分后写入调度列；再次同步不得重置它们。
			mustExecReviewFSRS(t, f, first.ID)
			afterGrade := kcSyncCard(t, f.db, model.ReviewSourceKC, f.tagID)
			kcSyncCall(t, f, ctx, f.userID, f.tagID, status, false)
			assertKCSyncCards(t, f.db, []kcCardRow{afterGrade})
		})
	}

	t.Run("metadata_comes_from_knowledge_tags_per_subject", func(t *testing.T) {
		f := newKCSyncFixture(t)
		chineseID := f.insertTag(t, "比喻", "chinese", "用相似事物打比方", "kc-sync-metaphor")

		kcSyncCall(t, f, ctx, f.userID, f.tagID, taxonomy.StatusWeak, true)
		kcSyncCall(t, f, ctx, f.userID, chineseID, taxonomy.StatusShaky, true)

		cards := kcSyncCards(t, f.db)
		if len(cards) != 2 {
			t.Fatalf("期望 2 张 KC 卡，实际 %d: %+v", len(cards), cards)
		}
		mathCard := kcSyncCard(t, f.db, model.ReviewSourceKC, f.tagID)
		chineseCard := kcSyncCard(t, f.db, model.ReviewSourceKC, chineseID)
		if mathCard.FrontText != f.tagName || mathCard.BackText != f.tagDesc || mathCard.Subject != "math" {
			t.Fatalf("math 卡元数据未来自 knowledge_tags: %+v", mathCard)
		}
		if chineseCard.FrontText != "比喻" || chineseCard.BackText != "用相似事物打比方" || chineseCard.Subject != "chinese" {
			t.Fatalf("chinese 卡元数据未来自 knowledge_tags: %+v", chineseCard)
		}
		if mathCard.ID == chineseCard.ID || mathCard.SourceID == chineseCard.SourceID {
			t.Fatalf("两张 KC 卡身份重复: %+v / %+v", mathCard, chineseCard)
		}
	})
}

func mustExecReviewFSRS(t *testing.T, f *kcSyncFixture, cardID int64) {
	t.Helper()
	mustExec(t, f.db,
		`UPDATE review_items SET due = '2028-03-04T05:06:07Z', stability = 7.5, difficulty = 8.25,
			scheduled_days = 21, reps = 9, lapses = 3, state = 'Relearning',
			last_review = '2026-11-05T09:00:00Z', remaining_steps = 1 WHERE id = ?`, cardID)
}

// ── 3. 已有 active/suspended 卡：FSRS/due/stability/reps/last_review 不重置 ──

func TestSyncKCReviewCard_ExistingCardsKeepFSRS(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		status     string
		suspended  int64
		wantChange bool
	}{
		{"weak_active", taxonomy.StatusWeak, 0, false},
		{"weak_suspended_no_resurrect", taxonomy.StatusWeak, 1, false},
		{"shaky_active", taxonomy.StatusShaky, 0, false},
		{"shaky_suspended_no_resurrect", taxonomy.StatusShaky, 1, false},
		{"mastered_suspended_no_resurrect", taxonomy.StatusMastered, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newKCSyncFixture(t)
			before := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceKC, tc.suspended)
			kcSyncCall(t, f, ctx, f.userID, f.tagID, tc.status, tc.wantChange)
			assertKCSyncCards(t, f.db, []kcCardRow{before})
		})
	}
}

// ── 4. unknown（及其它未识别状态）：完全 no-op ────────────────────────────

func TestSyncKCReviewCard_UnknownStatusNoop(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ name, status string }{
		{"unknown", taxonomy.StatusUnknown},
		{"empty_status", ""},
		{"unrecognized_status", "almost-mastered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newKCSyncFixture(t)
			// 缺卡：不得建卡。
			kcSyncCall(t, f, ctx, f.userID, f.tagID, tc.status, false)
			assertKCSyncCards(t, f.db, nil)

			// 已有 active / suspended 卡：任何列都不得变，也不得恢复挂起。
			active := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceKC, 0)
			secondTag := f.insertTag(t, "一元一次方程", "math", "只含一个未知数", "kc-sync-linear")
			suspended := f.seedFullCard(t, f.userID, secondTag, model.ReviewSourceKC, 1)
			kcSyncCall(t, f, ctx, f.userID, f.tagID, tc.status, false)
			kcSyncCall(t, f, ctx, f.userID, secondTag, tc.status, false)
			assertKCSyncCards(t, f.db, []kcCardRow{active, suspended})
		})
	}
}

// ── 5. 隔离：error_item 卡、其它用户的 KC 卡都绝不被改写 ──────────────────

func TestSyncKCReviewCard_Isolation(t *testing.T) {
	ctx := context.Background()

	t.Run("error_item_active_card_not_suspended", func(t *testing.T) {
		f := newKCSyncFixture(t)
		// 故意让 error_item 卡的 source_id 与 KC tag 相同：实现的所有语句
		// 必须按 source_type='kc' 限定，否则会误伤这张错题卡。
		before := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceErrorItem, 0)
		kcSyncCall(t, f, ctx, f.userID, f.tagID, taxonomy.StatusMastered, false)
		assertKCSyncCards(t, f.db, []kcCardRow{before})
	})

	t.Run("error_item_suspended_card_not_resumed", func(t *testing.T) {
		f := newKCSyncFixture(t)
		// 固定断言：suspended 的错题卡不得被任何状态恢复，且原行 18 列不变。
		before := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceErrorItem, 1)
		for _, status := range []string{taxonomy.StatusMastered, taxonomy.StatusWeak, taxonomy.StatusShaky} {
			// weak/shaky 会（且只应）为目标 KC 新建另一张 source_type='kc' 的卡；
			// 这里允许 changed=true，但错题卡必须逐列原样保留。
			_, err := f.s.SyncKCReviewCard(ctx, f.userID, f.tagID, status)
			if err != nil {
				t.Fatalf("SyncKCReviewCard(status=%s): %v", status, err)
			}
			if got := kcSyncCard(t, f.db, model.ReviewSourceErrorItem, f.tagID); got != before {
				t.Fatalf("status=%s 改写了错题卡\nbefore: %+v\nafter:  %+v", status, before, got)
			}
		}
		// 上述 weak/shaky 建出的 KC 卡与本用例无关，只需确认错题卡仍是挂起状态。
		if got := kcSyncCard(t, f.db, model.ReviewSourceErrorItem, f.tagID); got.Suspended != 1 {
			t.Fatalf("错题卡被自动恢复: %+v", got)
		}
	})

	t.Run("error_item_card_not_reused_for_kc_create", func(t *testing.T) {
		f := newKCSyncFixture(t)
		before := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceErrorItem, 0)
		kcSyncCall(t, f, ctx, f.userID, f.tagID, taxonomy.StatusWeak, true)
		created := kcSyncCard(t, f.db, model.ReviewSourceKC, f.tagID)
		if created.FrontText != f.tagName || created.BackText != f.tagDesc || created.Subject != f.tagSubject {
			t.Fatalf("新建的 KC 卡元数据错误: %+v", created)
		}
		assertKCSyncCards(t, f.db, []kcCardRow{before, created})
	})

	t.Run("other_user_mastered_does_not_suspend_foreign_card", func(t *testing.T) {
		f := newKCSyncFixture(t)
		before := f.seedFullCard(t, f.otherUserID, f.tagID, model.ReviewSourceKC, 0)
		kcSyncCall(t, f, ctx, f.userID, f.tagID, taxonomy.StatusMastered, false)
		assertKCSyncCards(t, f.db, []kcCardRow{before})
	})

	t.Run("other_user_weak_does_not_take_over_foreign_card", func(t *testing.T) {
		f := newKCSyncFixture(t)
		before := f.seedFullCard(t, f.otherUserID, f.tagID, model.ReviewSourceKC, 0)
		for _, status := range []string{taxonomy.StatusWeak, taxonomy.StatusShaky} {
			kcSyncCall(t, f, ctx, f.userID, f.tagID, status, false)
		}
		assertKCSyncCards(t, f.db, []kcCardRow{before})
	})

	t.Run("foreign_user_cannot_suspend_target_card", func(t *testing.T) {
		f := newKCSyncFixture(t)
		before := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceKC, 0)
		kcSyncCall(t, f, ctx, f.otherUserID, f.tagID, taxonomy.StatusMastered, false)
		assertKCSyncCards(t, f.db, []kcCardRow{before})
	})

	t.Run("foreign_user_weak_does_not_resurrect_suspended_card", func(t *testing.T) {
		f := newKCSyncFixture(t)
		before := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceKC, 1)
		for _, status := range []string{taxonomy.StatusWeak, taxonomy.StatusShaky, taxonomy.StatusUnknown} {
			kcSyncCall(t, f, ctx, f.otherUserID, f.tagID, status, false)
		}
		assertKCSyncCards(t, f.db, []kcCardRow{before})
	})
}

// ── 6. 非法参数：明确报错（且优先于 status 判定） ──────────────────────────

func TestSyncKCReviewCard_InvalidArgs(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		badUser  bool
		badValue int64
	}{
		{"zero_user_id", true, 0},
		{"negative_user_id", true, -1},
		{"zero_tag_id", false, 0},
		{"negative_tag_id", false, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newKCSyncFixture(t)
			userID, tagID := f.userID, f.tagID
			if tc.badUser {
				userID = tc.badValue
			} else {
				tagID = tc.badValue
			}
			changed, err := f.s.SyncKCReviewCard(ctx, userID, tagID, taxonomy.StatusMastered)
			if !errors.Is(err, store.ErrKCReviewSyncArgs) {
				t.Fatalf("期望 ErrKCReviewSyncArgs，实际 err=%v", err)
			}
			if changed {
				t.Fatalf("非法参数不得报告 changed=true")
			}
			assertKCSyncCards(t, f.db, nil)
		})
	}

	t.Run("invalid_args_win_over_unknown_status", func(t *testing.T) {
		f := newKCSyncFixture(t)
		changed, err := f.s.SyncKCReviewCard(ctx, 0, f.tagID, taxonomy.StatusUnknown)
		if !errors.Is(err, store.ErrKCReviewSyncArgs) || changed {
			t.Fatalf("非法参数应优先报错: changed=%v err=%v", changed, err)
		}
		assertKCSyncCards(t, f.db, nil)
	})
}

// ── 7. 未知 tag：语义冻结要求"明确报错"，且不得留下半张卡 ─────────────────

func TestSyncKCReviewCard_UnknownTagReportsError(t *testing.T) {
	ctx := context.Background()
	const missingTagID int64 = 987654321 // fixture 只插了一条 tag，此 id 不存在
	for _, status := range []string{taxonomy.StatusWeak, taxonomy.StatusShaky, taxonomy.StatusMastered} {
		t.Run(status, func(t *testing.T) {
			f := newKCSyncFixture(t)
			changed, err := f.s.SyncKCReviewCard(ctx, f.userID, missingTagID, status)
			if err == nil {
				t.Fatalf("未知 tag 必须明确报错（status=%s），实际 changed=%v err=nil", status, changed)
			}
			if !errors.Is(err, store.ErrKCReviewSyncTagNotFound) {
				t.Fatalf("未知 tag 应返回 ErrKCReviewSyncTagNotFound（errors.Is，status=%s），实际 err=%v", status, err)
			}
			if errors.Is(err, store.ErrKCReviewSyncArgs) {
				t.Fatalf("未知 tag 不得与参数错误混淆（status=%s）", status)
			}
			if changed {
				t.Fatalf("未知 tag 报错时不得报告 changed=true (status=%s)", status)
			}
			assertKCSyncCards(t, f.db, nil)
		})
	}
}

// ── 7b. 未知 tag + unknown：可识别的 no-op，且完全不触碰 DB ────────────────

func TestSyncKCReviewCard_MissingTagUnknownStatusNoop(t *testing.T) {
	ctx := context.Background()
	const missingTagID int64 = 987654321 // fixture 只插了一条 tag，此 id 不存在

	t.Run("silent_noop_with_no_row_changes", func(t *testing.T) {
		f := newKCSyncFixture(t)
		otherTagID := f.insertTag(t, "一元一次方程", "math", "只含一个未知数", "kc-sync-linear")
		// 铺三类行：本用户 active KC 卡、其它用户 suspended KC 卡、错题卡。
		// unknown 的 no-op 必须一列不动，也不得因为 tag 不存在而报错。
		active := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceKC, 0)
		suspended := f.seedFullCard(t, f.otherUserID, otherTagID, model.ReviewSourceKC, 1)
		errorItem := f.seedFullCard(t, f.userID, missingTagID, model.ReviewSourceErrorItem, 0)

		changed, err := f.s.SyncKCReviewCard(ctx, f.userID, missingTagID, taxonomy.StatusUnknown)
		if err != nil {
			t.Fatalf("unknown 状态对未知 tag 必须是 no-op，不得报错: %v", err)
		}
		if errors.Is(err, store.ErrKCReviewSyncTagNotFound) {
			t.Fatal("unknown 状态不得报 ErrKCReviewSyncTagNotFound")
		}
		if changed {
			t.Fatal("unknown 状态不得报告 changed=true")
		}
		assertKCSyncCards(t, f.db, []kcCardRow{active, suspended, errorItem})
	})

	t.Run("does_not_query_db_at_all", func(t *testing.T) {
		f := newKCSyncFixture(t)
		before := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceKC, 0)

		// 预取消的 ctx：任何 BeginTx / QueryRow 都会立刻返回 context.Canceled。
		// unknown 状态在参数校验后即返回，所以这里必须是 (false, nil)，
		// 这同时证明实现没有为“未知 tag”做任何查询。
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		changed, err := f.s.SyncKCReviewCard(ctx, f.userID, missingTagID, taxonomy.StatusUnknown)
		if err != nil {
			t.Fatalf("unknown 状态不应触碰 DB（取消 ctx 也不该报错）: %v", err)
		}
		if changed {
			t.Fatal("unknown 状态不得报告 changed=true")
		}
		assertKCSyncCards(t, f.db, []kcCardRow{before})

		// 对照组：同一取消 ctx 下 weak 确实进入 DB 路径并报 context.Canceled，
		// 证明上面的 nil 不是 ctx / fixture 假象。
		if _, err := f.s.SyncKCReviewCard(ctx, f.userID, f.tagID, taxonomy.StatusWeak); !errors.Is(err, context.Canceled) {
			t.Fatalf("对照 weak 应返回 context.Canceled，实际 %v", err)
		}
	})

	t.Run("recognized_status_with_existing_card_still_errors", func(t *testing.T) {
		for _, status := range []string{taxonomy.StatusWeak, taxonomy.StatusShaky, taxonomy.StatusMastered} {
			t.Run(status, func(t *testing.T) {
				f := newKCSyncFixture(t)
				// 即使目标 KC 已有一张 active 卡，mastered 也不能跳过 tag 校验；
				// weak/shaky 也不能因“已有卡”而跳过。
				before := f.seedFullCard(t, f.userID, missingTagID, model.ReviewSourceKC, 0)
				changed, err := f.s.SyncKCReviewCard(ctx, f.userID, missingTagID, status)
				if !errors.Is(err, store.ErrKCReviewSyncTagNotFound) {
					t.Fatalf("status=%s 应返回 ErrKCReviewSyncTagNotFound，实际 err=%v", status, err)
				}
				if changed {
					t.Fatalf("status=%s 报错时不得报告 changed=true", status)
				}
				assertKCSyncCards(t, f.db, []kcCardRow{before})
			})
		}
	})
}

// ── 8. context cancel：无半更新，单写者连接不卡死 ──────────────────────────

func TestSyncKCReviewCard_ContextCancel(t *testing.T) {
	assertCancelErr := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("已取消的 ctx 必须返回错误")
		}
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("期望 context.Canceled/DeadlineExceeded，实际 %v", err)
		}
	}

	t.Run("pre_canceled_mastered_no_partial_update", func(t *testing.T) {
		f := newKCSyncFixture(t)
		before := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceKC, 0)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		changed, err := f.s.SyncKCReviewCard(ctx, f.userID, f.tagID, taxonomy.StatusMastered)
		assertCancelErr(t, err)
		if changed {
			t.Fatal("取消后不得报告 changed=true")
		}
		assertKCSyncCards(t, f.db, []kcCardRow{before})

		// 连接未卡死：正常 ctx 的同一调用仍可成功提交。
		kcSyncCall(t, f, context.Background(), f.userID, f.tagID, taxonomy.StatusMastered, true)
		after := kcSyncCard(t, f.db, model.ReviewSourceKC, f.tagID)
		assertKCSyncOnlySuspendedChanged(t, before, after)
	})

	t.Run("pre_canceled_weak_creates_nothing", func(t *testing.T) {
		f := newKCSyncFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		changed, err := f.s.SyncKCReviewCard(ctx, f.userID, f.tagID, taxonomy.StatusWeak)
		assertCancelErr(t, err)
		if changed {
			t.Fatal("取消后不得报告 changed=true")
		}
		assertKCSyncCards(t, f.db, nil)

		kcSyncCall(t, f, context.Background(), f.userID, f.tagID, taxonomy.StatusWeak, true)
		assertKCSyncNewCard(t, f, f.tagID, f.tagName, f.tagSubject, f.tagDesc)
	})

	t.Run("cancel_while_waiting_for_single_writer", func(t *testing.T) {
		f := newKCSyncFixture(t)
		before := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceKC, 0)

		// 占住 MaxOpenConns=1 的唯一连接，使 BeginTx 等待；随后取消 ctx。
		conn, err := f.db.Conn(context.Background())
		if err != nil {
			t.Fatalf("占用唯一连接: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(20 * time.Millisecond)
			cancel()
		}()
		changed, err := f.s.SyncKCReviewCard(ctx, f.userID, f.tagID, taxonomy.StatusMastered)
		assertCancelErr(t, err)
		if changed {
			t.Fatal("取消后不得报告 changed=true")
		}
		_ = conn.Close()
		assertKCSyncCards(t, f.db, []kcCardRow{before})

		// 连接归还后单写者池仍可用（不是死等 / 连接泄漏）。
		kcSyncCall(t, f, context.Background(), f.userID, f.tagID, taxonomy.StatusMastered, true)
		after := kcSyncCard(t, f.db, model.ReviewSourceKC, f.tagID)
		assertKCSyncOnlySuspendedChanged(t, before, after)
	})
}

// ── 9. 事务内写失败：整笔回滚，无半更新，连接仍可用 ───────────────────────

func TestSyncKCReviewCard_WriteFailureRollsBack(t *testing.T) {
	f := newKCSyncFixture(t)
	before := f.seedFullCard(t, f.userID, f.tagID, model.ReviewSourceKC, 0)
	// 真实 SQLite 触发器注入写失败（不是 mock）：让 0→1 的挂起 UPDATE 中止，
	// 验证 defer tx.Rollback() 生效且事务内没有其它半写入。
	mustExec(t, f.db, `CREATE TRIGGER kc_sync_abort BEFORE UPDATE OF suspended ON review_items
		WHEN OLD.suspended = 0 AND NEW.suspended = 1
		BEGIN SELECT RAISE(ABORT, 'injected kc sync write failure'); END`)

	changed, err := f.s.SyncKCReviewCard(context.Background(), f.userID, f.tagID, taxonomy.StatusMastered)
	if err == nil {
		t.Fatal("写失败必须向上返回错误")
	}
	if changed {
		t.Fatal("写失败时不得报告 changed=true")
	}
	assertKCSyncCards(t, f.db, []kcCardRow{before})

	mustExec(t, f.db, `DROP TRIGGER kc_sync_abort`)
	kcSyncCall(t, f, context.Background(), f.userID, f.tagID, taxonomy.StatusMastered, true)
	after := kcSyncCard(t, f.db, model.ReviewSourceKC, f.tagID)
	assertKCSyncOnlySuspendedChanged(t, before, after)
}
