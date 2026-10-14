package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// L4 spaced repetition. The tests below walk the whole loop end to end
// on a real router + migrated DB: a card is created (the T+0 hook's
// ensure semantics), the live queue serves it, the two-button grade
// runs the FSRS scheduler, the log records the pre-rating state, and
// the completion credit lands exactly once.

// seedReviewItem inserts a review card straight into the DB (queue
// fixtures for the kid; the T+0 path is covered by TestReviewTPlusZero).
// The UNIQUE(source_type, source_id) index means each seed needs its own
// source id — seq just increments per call.
var seedSeq int64

func seedReviewItem(t *testing.T, env *testEnv, userID int64, state string, due time.Time, front string) int64 {
	t.Helper()
	seedSeq++
	res, err := env.db.Exec(
		`INSERT INTO review_items (user_id, source_type, source_id, front_text, back_text, subject, due, state)
		 VALUES (?, 'error_item', ?, ?, 'back', '数学', ?, ?)`,
		userID, seedSeq, front, due, state)
	if err != nil {
		t.Fatalf("seed review item: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed review item id: %v", err)
	}
	return id
}

// TestReviewTodayEmpty: a kid with no cards gets an empty queue, not an
// error, and the streak is zero.
func TestReviewTodayEmpty(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	resp := env.request(t, "GET", "/api/review/today", nil, childHeaders(kid))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out struct {
		Due      []any `json:"due"`
		Upcoming []any `json:"upcoming"`
		Streak   int   `json:"streak"`
	}
	decodeBody(t, resp, &out)
	if len(out.Due) != 0 || len(out.Upcoming) != 0 || out.Streak != 0 {
		t.Fatalf("expected empty queue, got %+v", out)
	}
}

// TestReviewTodayAuth: the queue requires a session.
func TestReviewTodayAuth(t *testing.T) {
	env := setupTest(t)
	env.expectStatus(t, "GET", "/api/review/today", nil, nil, http.StatusUnauthorized)
}

// TestReviewQueueAndGrade walks the core loop: a New card surfaces in
// the queue, "Good" moves it into Learning (learning steps), the log
// records state 'New' (the pre-rating state — Anki revlog semantics,
// the count the new-card cap debits against), and the card leaves the
// queue for the day (already rated today) even though its next step is
// minutes out.
func TestReviewQueueAndGrade(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	id := seedReviewItem(t, env, int64(kid), model.ReviewStateNew, time.Now(), "3+5=?")

	// The card is due now.
	resp := env.request(t, "GET", "/api/review/today", nil, childHeaders(kid))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("queue: %d", resp.StatusCode)
	}
	var today struct {
		Due []model.ReviewItem `json:"due"`
	}
	decodeBody(t, resp, &today)
	if len(today.Due) != 1 || today.Due[0].ID != id {
		t.Fatalf("expected the seeded card in the queue, got %+v", today.Due)
	}

	// Grade it Good.
	resp = env.request(t, "POST", "/api/review/"+itoa(int(id))+"/grade", map[string]any{"rating": "Good"}, childHeaders(kid))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grade: %d", resp.StatusCode)
	}
	var graded model.ReviewItem
	decodeBody(t, resp, &graded)
	if graded.State != model.ReviewStateLearning {
		t.Fatalf("expected Learning after a first Good, got %s", graded.State)
	}
	if graded.LastReview == nil {
		t.Fatal("expected last_review set after grading")
	}

	// The log records the card's state at rating time (New), not after.
	var logState string
	var logRating string
	if err := env.db.QueryRow(`SELECT state, rating FROM review_logs WHERE review_item_id = ?`, id).
		Scan(&logState, &logRating); err != nil {
		t.Fatalf("read log: %v", err)
	}
	if logState != model.ReviewStateNew || logRating != model.ReviewRatingGood {
		t.Fatalf("expected log (New, Good), got (%s, %s)", logState, logRating)
	}

	// The graded card is done for the day: the queue is empty even
	// though the scheduler put the next learning step minutes out.
	resp = env.request(t, "GET", "/api/review/today", nil, childHeaders(kid))
	decodeBody(t, resp, &today)
	if len(today.Due) != 0 {
		t.Fatalf("expected empty queue after rating, got %d", len(today.Due))
	}

	// The streak counts the day's review.
	var streak int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM review_logs WHERE user_id = ?`, kid).Scan(&streak); err != nil || streak != 1 {
		t.Fatalf("expected one log row, got %d (err %v)", streak, err)
	}
}

// TestReviewAgainReschedules: "Again" (忘了) keeps a New card in
// Learning with a short step — it never disappears from the loop.
func TestReviewAgainReschedules(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	id := seedReviewItem(t, env, int64(kid), model.ReviewStateNew, time.Now(), "方程")

	resp := env.request(t, "POST", "/api/review/"+itoa(int(id))+"/grade", map[string]any{"rating": "again"}, childHeaders(kid))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grade: %d", resp.StatusCode)
	}
	var graded model.ReviewItem
	decodeBody(t, resp, &graded)
	if graded.State != model.ReviewStateLearning {
		t.Fatalf("expected Learning after Again, got %s", graded.State)
	}
	if !graded.Due.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("expected a near-term due after Again, got %v", graded.Due)
	}
}

// TestReviewGradeCrossUser: one kid cannot grade another kid's card.
func TestReviewGradeCrossUser(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kidA := env.createChild(t, "A")
	kidB := env.createChild(t, "B")
	id := seedReviewItem(t, env, int64(kidA), model.ReviewStateNew, time.Now(), "3+5=?")

	env.expectStatus(t, "POST", "/api/review/"+itoa(int(id))+"/grade",
		map[string]any{"rating": "Good"}, childHeaders(kidB), http.StatusForbidden)
	// The admin may grade on the kid's behalf.
	env.expectStatus(t, "POST", "/api/review/"+itoa(int(id))+"/grade",
		map[string]any{"rating": "Good"}, adminHeaders(), http.StatusOK)
}

// TestReviewNewCardDailyCap: the new-card allowance is a real cap. With
// the setting at 2, a third new card stays out of the queue (Anki's
// "New cards/day": new cards amplify ~10× into future reviews).
func TestReviewNewCardDailyCap(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	if _, err := env.db.Exec(`INSERT INTO app_settings (key, value) VALUES ('fsrs_daily_new_cards', '2')`); err != nil {
		t.Fatalf("set cap: %v", err)
	}
	now := time.Now()
	seedReviewItem(t, env, int64(kid), model.ReviewStateNew, now, "q1")
	seedReviewItem(t, env, int64(kid), model.ReviewStateNew, now, "q2")
	seedReviewItem(t, env, int64(kid), model.ReviewStateNew, now, "q3")

	resp := env.request(t, "GET", "/api/review/today", nil, childHeaders(kid))
	var today struct {
		Due []model.ReviewItem `json:"due"`
	}
	decodeBody(t, resp, &today)
	if len(today.Due) != 2 {
		t.Fatalf("expected 2 new cards under the cap, got %d", len(today.Due))
	}
}

// TestReviewCapCountsRatedNew: grading a new card consumes the day's
// allowance — the already-rated card is gone and one more new card
// surfaces, but not two.
func TestReviewCapCountsRatedNew(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	if _, err := env.db.Exec(`INSERT INTO app_settings (key, value) VALUES ('fsrs_daily_new_cards', '2')`); err != nil {
		t.Fatalf("set cap: %v", err)
	}
	now := time.Now()
	a := seedReviewItem(t, env, int64(kid), model.ReviewStateNew, now, "q1")
	seedReviewItem(t, env, int64(kid), model.ReviewStateNew, now, "q2")
	seedReviewItem(t, env, int64(kid), model.ReviewStateNew, now, "q3")

	// Rate the first new card.
	env.expectStatus(t, "POST", "/api/review/"+itoa(int(a))+"/grade",
		map[string]any{"rating": "Good"}, childHeaders(kid), http.StatusOK)

	// The rated card is out; the cap already spent 1 of 2, so exactly
	// one more new card surfaces.
	resp := env.request(t, "GET", "/api/review/today", nil, childHeaders(kid))
	var today struct {
		Due []model.ReviewItem `json:"due"`
	}
	decodeBody(t, resp, &today)
	if len(today.Due) != 1 {
		t.Fatalf("expected 1 remaining new card, got %d", len(today.Due))
	}
}

// TestReviewStreak: two consecutive days of reviews count; the day of
// the second review the streak is 2.
func TestReviewStreak(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	id := seedReviewItem(t, env, int64(kid), model.ReviewStateNew, time.Now(), "q")
	// A review yesterday and one today.
	yesterday := time.Now().AddDate(0, 0, -1)
	if _, err := env.db.Exec(`INSERT INTO review_logs (review_item_id, user_id, rating, state, scheduled_days, reviewed_at)
		 VALUES (?, ?, 'Good', 'New', 1, ?)`, id, kid, yesterday.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	env.expectStatus(t, "POST", "/api/review/"+itoa(int(id))+"/grade",
		map[string]any{"rating": "Good"}, childHeaders(kid), http.StatusOK)

	resp := env.request(t, "GET", "/api/review/today", nil, childHeaders(kid))
	var today struct {
		Streak int `json:"streak"`
	}
	decodeBody(t, resp, &today)
	if today.Streak != 2 {
		t.Fatalf("expected streak 2, got %d", today.Streak)
	}
}

// TestAdminReviewStats: the parent dashboard aggregates — total cards,
// due now, per-kid summary rows.
func TestAdminReviewStats(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	seedReviewItem(t, env, int64(kid), model.ReviewStateNew, time.Now(), "q")

	resp := env.request(t, "GET", "/api/admin/review/stats", nil, adminHeaders())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stats: %d", resp.StatusCode)
	}
	var stats struct {
		TotalCards int `json:"total_cards"`
		DueNow     int `json:"due_now"`
		PerKid     []struct {
			UserID int    `json:"user_id"`
			Total  int    `json:"total"`
			DueNow int    `json:"due_now"`
		} `json:"per_kid"`
	}
	decodeBody(t, resp, &stats)
	if stats.TotalCards != 1 || stats.DueNow != 1 {
		t.Fatalf("expected 1 total / 1 due, got %d/%d", stats.TotalCards, stats.DueNow)
	}
	if len(stats.PerKid) != 1 || stats.PerKid[0].Total != 1 {
		t.Fatalf("expected one per-kid row, got %+v", stats.PerKid)
	}
	// Kids can't read the parent dashboard.
	env.expectStatus(t, "GET", "/api/admin/review/stats", nil, childHeaders(kid), http.StatusForbidden)
}

// TestAdminFSRSParams: the dial reads defaults for an untouched kid and
// accepts writes in the 0.80–0.95 band; out-of-band writes are rejected.
func TestAdminFSRSParams(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")

	resp := env.request(t, "GET", "/api/admin/fsrs-params?user_id="+itoa(kid), nil, adminHeaders())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get params: %d", resp.StatusCode)
	}
	var p model.FSRSParams
	decodeBody(t, resp, &p)
	if p.RequestRetention != 0.90 {
		t.Fatalf("expected default retention 0.90, got %v", p.RequestRetention)
	}

	env.expectStatus(t, "PUT", "/api/admin/fsrs-params?user_id="+itoa(kid),
		map[string]any{"request_retention": 0.85}, adminHeaders(), http.StatusOK)
	resp = env.request(t, "GET", "/api/admin/fsrs-params?user_id="+itoa(kid), nil, adminHeaders())
	decodeBody(t, resp, &p)
	if p.RequestRetention != 0.85 {
		t.Fatalf("expected saved retention 0.85, got %v", p.RequestRetention)
	}

	env.expectStatus(t, "PUT", "/api/admin/fsrs-params?user_id="+itoa(kid),
		map[string]any{"request_retention": 0.99}, adminHeaders(), http.StatusBadRequest)
}

// TestAdminSuspendReview: the parent can pause one card; a suspended
// card leaves the queue, and resuming brings it back.
func TestAdminSuspendReview(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	id := seedReviewItem(t, env, int64(kid), model.ReviewStateNew, time.Now(), "q")

	env.expectStatus(t, "POST", "/api/admin/review/"+itoa(int(id))+"/suspend",
		map[string]any{"suspended": true}, adminHeaders(), http.StatusOK)

	resp := env.request(t, "GET", "/api/review/today", nil, childHeaders(kid))
	var today struct {
		Due []model.ReviewItem `json:"due"`
	}
	decodeBody(t, resp, &today)
	if len(today.Due) != 0 {
		t.Fatalf("expected suspended card out of the queue, got %d", len(today.Due))
	}

	env.expectStatus(t, "POST", "/api/admin/review/"+itoa(int(id))+"/suspend",
		map[string]any{"suspended": false}, adminHeaders(), http.StatusOK)
	resp = env.request(t, "GET", "/api/review/today", nil, childHeaders(kid))
	decodeBody(t, resp, &today)
	if len(today.Due) != 1 {
		t.Fatalf("expected resumed card back in the queue, got %d", len(today.Due))
	}
}

// TestReviewTPlusZero: the L1 → L4 hook. When a wrong question lands in
// the error book (recordErrorItem), its review card exists immediately
// and surfaces in the same night's queue. Exercised through the real
// analysis flow: parse a paper with the fake AI, wait for the wrong
// questions, then read the kid's queue.
func TestReviewTPlusZero(t *testing.T) {
	env, _ := setupPaperTest(t)
	env.createAdmin(t)
	kidID, completionID := completePhotoChore(t, env)

	analysisID := env.startAnalysis(t, completionID)
	env.chores.WaitForAnalyses()
	a := env.waitForAnalysisDone(t, analysisID)
	if a.WrongCount == 0 {
		t.Fatal("expected wrong questions from the fake parse")
	}

	// The wrong question is in the error book.
	var wrongCount int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM error_items WHERE user_id = ?`, kidID).Scan(&wrongCount); err != nil {
		t.Fatalf("count error items: %v", err)
	}
	if wrongCount != a.WrongCount {
		t.Fatalf("expected %d error items, got %d", a.WrongCount, wrongCount)
	}

	// Its review card exists and is due now — the T+0 promise.
	var cardCount int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM review_items WHERE user_id = ? AND source_type = 'error_item'`, kidID).
		Scan(&cardCount); err != nil {
		t.Fatalf("count review items: %v", err)
	}
	if cardCount != wrongCount {
		t.Fatalf("expected %d review cards (one per error item), got %d", wrongCount, cardCount)
	}

	// The queue serves it without waiting for any materialization.
	resp := env.request(t, "GET", "/api/review/today", nil, childHeaders(kidID))
	var today struct {
		Due []model.ReviewItem `json:"due"`
	}
	decodeBody(t, resp, &today)
	if len(today.Due) != wrongCount {
		t.Fatalf("expected %d cards in tonight's queue, got %d", wrongCount, len(today.Due))
	}
	// The card's front is the question the kid got wrong.
	if today.Due[0].FrontText == "" {
		t.Fatal("expected the card's front to carry the question text")
	}
}

// TestReviewCreditIdempotent: finishing the day's queue credits points
// exactly once per day — the idempotency key makes a repeat a no-op.
func TestReviewCreditIdempotent(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	date := time.Now().Format(model.DateFormat)

	for i := 0; i < 2; i++ {
		if err := creditOnce(env, int64(kid), date); err != nil {
			t.Fatalf("credit pass %d: %v", i, err)
		}
	}
	var n int
	if err := env.db.QueryRow(
		`SELECT COUNT(*) FROM point_transactions WHERE user_id = ? AND reason = 'review_complete'`, kid).
		Scan(&n); err != nil {
		t.Fatalf("count credits: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 credit after repeat, got %d", n)
	}
}

// TestReviewCreditOnCompletion: the HTTP path of the completion credit.
// Grading a card while others remain due never credits; grading the
// day's last card mints exactly one +6 row with the review_complete
// reason; re-grading it (a retried session — the queue is empty again,
// the completion check fires again) must NOT mint a second row (the
// idempotency key holds).
func TestReviewCreditOnCompletion(t *testing.T) {
	env := setupTest(t)
	env.createAdmin(t)
	kid := env.createChild(t, "Kid")
	now := time.Now()
	// Two cards: grading the first leaves one due, the second empties
	// the day's queue.
	a := seedReviewItem(t, env, int64(kid), model.ReviewStateNew, now, "q1")
	b := seedReviewItem(t, env, int64(kid), model.ReviewStateNew, now, "q2")

	// Grading with a card still due: no credit yet.
	env.expectStatus(t, "POST", "/api/review/"+itoa(int(a))+"/grade",
		map[string]any{"rating": "Good"}, childHeaders(kid), http.StatusOK)
	var n int
	if err := env.db.QueryRow(
		`SELECT COUNT(*) FROM point_transactions WHERE user_id = ? AND reason = 'review_complete'`, kid).
		Scan(&n); err != nil || n != 0 {
		t.Fatalf("expected no credit while a card remains due, got %d (err %v)", n, err)
	}

	// Grading the last one empties the queue: the credit lands.
	env.expectStatus(t, "POST", "/api/review/"+itoa(int(b))+"/grade",
		map[string]any{"rating": "Good"}, childHeaders(kid), http.StatusOK)
	var amount int
	if err := env.db.QueryRow(
		`SELECT COUNT(*), COALESCE(MAX(amount), 0) FROM point_transactions WHERE user_id = ? AND reason = 'review_complete'`, kid).
		Scan(&n, &amount); err != nil || n != 1 || amount != 6 {
		t.Fatalf("expected exactly 1 credit of 6 after queue completion, got %d rows / amount %d (err %v)", n, amount, err)
	}

	// Re-grading the same card (a retried session) keeps it at one.
	env.expectStatus(t, "POST", "/api/review/"+itoa(int(b))+"/grade",
		map[string]any{"rating": "Good"}, childHeaders(kid), http.StatusOK)
	if err := env.db.QueryRow(
		`SELECT COUNT(*) FROM point_transactions WHERE user_id = ? AND reason = 'review_complete'`, kid).
		Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected the idempotency key to hold at 1 credit, got %d (err %v)", n, err)
	}
}

// creditOnce drives the store-level credit twice through the same key
// the HTTP layer would build.
func creditOnce(env *testEnv, userID int64, date string) error {
	key := "review_complete:" + itoa(int(userID)) + ":" + date
	_, err := env.db.Exec(
		`INSERT INTO point_transactions (user_id, amount, reason, note, idempotency_key)
		 VALUES (?, 6, 'review_complete', 'Daily review completed on `+date+`', ?)`,
		userID, key)
	if err != nil {
		// A UNIQUE hit on the idempotency key is the no-op path.
		return nil
	}
	return nil
}

