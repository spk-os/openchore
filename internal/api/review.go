package api

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	fsrs "github.com/open-spaced-repetition/go-fsrs/v4"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// Review HTTP handlers (L4). Kid side: the live due queue and the
// two-button grading. Parent side: the stats views (heatmap, load
// forecast, retention), the FSRS params dial, and per-card suspend.
// The scheduler itself is review_fsrs.go.

// ReviewHandler serves the spaced-repetition endpoints.
type ReviewHandler struct {
	store *store.Store
	LearnerHook
}

func NewReviewHandler(s *store.Store) *ReviewHandler {
	return &ReviewHandler{store: s, LearnerHook: newLearnerHook(s)}
}

// featureFSRSSetting is the settings-l0l4 master switch for spaced
// review (flag-fsrs). Absent = on; only the literal "false" disables.
const featureFSRSSetting = "feature_fsrs"

// reviewTodayResponse is the kid's queue: the cards, the parent's
// preview list ("接下来还会遇到"), and the streak for the 🔥 line.
type reviewTodayResponse struct {
	Due      []reviewTodayCard `json:"due"`
	Upcoming []reviewTodayCard `json:"upcoming"`
	Streak   int               `json:"streak"`
}

// reviewTodayCard is one card as the review screen renders it: the raw
// ReviewItem plus the resolved provenance (subject badge, "来自：…"
// line) for error-item sources.
type reviewTodayCard struct {
	model.ReviewItem
	SourceLabel string `json:"source_label,omitempty"`
}

// GetReviewToday serves GET /api/review/today — the kid's live queue
// (due minus rated-today, under the new-card and total caps), the
// upcoming preview, and the streak.
func (h *ReviewHandler) GetReviewToday(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	// The L0-L4 master switch (settings-l0l4's flag-fsrs): off means the
	// kid sees an empty queue — the review screen's "nothing due" state —
	// but nothing is graded, suspended or deleted. Missing key = on.
	if off, _ := h.store.GetSetting(r.Context(), featureFSRSSetting); off == "false" {
		writeJSON(w, http.StatusOK, reviewTodayResponse{
			Due:      []reviewTodayCard{},
			Upcoming: []reviewTodayCard{},
		})
		return
	}
	now := time.Now()
	start := dayStart(now)

	due, err := h.store.ListDueReviewItems(r.Context(), user.ID, start,
		h.remainingNewCards(r.Context(), user.ID), fsrsTotalQueueCap)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load review queue")
		return
	}
	upcoming, err := h.store.ListUpcomingReviewItems(r.Context(), user.ID, 10)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load upcoming reviews")
		return
	}
	streak, _ := h.store.ReviewStreakDays(r.Context(), user.ID, now)

	writeJSON(w, http.StatusOK, reviewTodayResponse{
		Due:      h.withSourceLabels(r, due),
		Upcoming: h.withSourceLabels(r, upcoming),
		Streak:   streak,
	})
}

// remainingNewCards is today's remaining new-card allowance: the cap
// minus the new cards the kid already saw today (Anki resets the
// allowance daily; it never accumulates).
func (h *ReviewHandler) remainingNewCards(ctx context.Context, userID int64) int {
	dailyLimit := h.dailyNewCards(ctx)
	if dailyLimit <= 0 {
		return 0
	}
	seen, err := h.store.CountNewReviewCardsRatedToday(ctx, userID, dayStart(time.Now()))
	if err != nil {
		return dailyLimit
	}
	if seen >= dailyLimit {
		return 0
	}
	return dailyLimit - seen
}

// withSourceLabels resolves the provenance line for error-item cards
// ("来自：期中卷 · 题干…" — the review screen's source note).
func (h *ReviewHandler) withSourceLabels(r *http.Request, items []model.ReviewItem) []reviewTodayCard {
	out := make([]reviewTodayCard, 0, len(items))
	for _, it := range items {
		c := reviewTodayCard{ReviewItem: it}
		if it.SourceType == model.ReviewSourceErrorItem {
			if src, err := h.store.GetErrorItemLite(r.Context(), it.SourceID); err == nil && src != "" {
				c.SourceLabel = src
			}
		}
		out = append(out, c)
	}
	return out
}

// gradeRequest is one rating. The kid's UI sends "Again" or "Good";
// parents may send all four.
type gradeRequest struct {
	Rating string `json:"rating"`
}

// GradeReview serves POST /api/review/{id}/grade — run one rating
// through the FSRS scheduler and persist the result. Rating names are
// case-insensitive; anything unrecognized maps to Good (the scheduler
// still moves, a typo never orphans a card).
func (h *ReviewHandler) GradeReview(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid review item id")
		return
	}
	item, err := h.store.GetReviewItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get review item")
		return
	}
	if item == nil {
		writeError(w, http.StatusNotFound, "review item not found")
		return
	}
	user := UserFromContext(r.Context())
	if user.Role != model.RoleAdmin && item.UserID != user.ID {
		writeError(w, http.StatusForbidden, "this review item belongs to someone else")
		return
	}
	var req gradeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	item, err = h.GradeReviewCard(r.Context(), item, parseRating(req.Rating), time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to grade review")
		return
	}
	// Finishing the day's queue credits +6 points once. Checked on every
	// grade (a grade is the only way the queue shrinks); the idempotency
	// key in CreditReviewPoints makes repeat fires — a second session, a
	// retried request — a no-op.
	if item.UserID == user.ID || user.Role == model.RoleAdmin {
		h.maybeCreditDailyQueue(r.Context(), item.UserID)
	}

	// L3 事件流：一次评分即一次「复习」（xAPI reviewed）。Good/Easy 算掌握
	// 信号，Again/Hard 算未掌握——喂给 KC rollup 的 Laplace 平滑。复习卡
	// 的 KC 绑定在源错题上（source_type=error_item 时回查其标签）。
	rating := parseRating(req.Rating)
	success := rating == fsrs.Good || rating == fsrs.Easy
	kcIDs := []int64{}
	if item.SourceType == model.ReviewSourceErrorItem {
		if tags, err := h.store.TagsForItem(r.Context(), item.SourceID); err == nil {
			for _, t := range tags {
				kcIDs = append(kcIDs, t.ID)
			}
		}
	}
	h.LearnerHook.recordLearnerEventSafe(r.Context(), item.UserID, model.EventVerbReviewed,
		model.EventObjectReviewItem, item.ID,
		map[string]any{"success": success},
		map[string]any{"rating": ratingString(rating), "kc_ids": kcIDs})

	writeJSON(w, http.StatusOK, item)
}

// maybeCreditDailyQueue credits the queue-completion points when the
// kid's due queue is now empty and the kid actually reviewed something
// today (an empty queue on a fresh account must not mint points). A
// credit failure never fails the grade — the card is already saved.
func (h *ReviewHandler) maybeCreditDailyQueue(ctx context.Context, userID int64) {
	now := time.Now()
	due, err := h.store.ListDueReviewItems(ctx, userID, dayStart(now),
		h.remainingNewCards(ctx, userID), fsrsTotalQueueCap)
	if err != nil || len(due) > 0 {
		return
	}
	streak, err := h.store.ReviewStreakDays(ctx, userID, now)
	if err != nil || streak < 1 {
		return
	}
	if err := h.CreditReviewPoints(ctx, userID, now.Format(model.DateFormat)); err != nil {
		log.Printf("review: daily queue credit for user %d failed: %v", userID, err)
	}
}

// reviewStatsResponse feeds the parent's mastery views (Anki stats'
// three forms): the review heatmap (by day, good counts included), the
// 30-day load forecast, and the streak.
type reviewStatsResponse struct {
	Heatmap     []heatmapDay        `json:"heatmap"`
	Forecast    []forecastBucket    `json:"forecast"`
	Streak      int                 `json:"streak"`
	Params      *model.FSRSParams   `json:"fsrs_params"`
	TotalCards  int                 `json:"total_cards"`
	DueNow      int                 `json:"due_now"`
	PerKid      []kidReviewStats    `json:"per_kid"`
}

type heatmapDay struct {
	Date      string `json:"date"`
	Count     int    `json:"count"`
	GoodCount int    `json:"good_count"`
}

type forecastBucket struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

type kidReviewStats struct {
	UserID  int64  `json:"user_id"`
	Name    string `json:"name"`
	Streak  int    `json:"streak"`
	DueNow  int    `json:"due_now"`
	Total   int    `json:"total"`
}

// GetReviewStats serves GET /api/admin/review/stats — the parent's
// mastery dashboard data. ?user_id= focuses one kid (default: every
// child's numbers side by side).
func (h *ReviewHandler) GetReviewStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now()

	// The heatmap and forecast always answer for the requested kid (or
	// the household's first child); per_kid carries everyone's summary.
	// ?user_id= is a query param (route is /admin/review/stats).
	userID, err := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	focusID := userID
	if err != nil || focusID <= 0 {
		if kids, kerr := h.store.ListNonPausedChildren(ctx); kerr == nil && len(kids) > 0 {
			focusID = kids[0].ID
		}
	}

	resp := reviewStatsResponse{}
	if focusID > 0 {
		heat, err := h.store.ReviewHeatmap(ctx, focusID, now.AddDate(0, 0, -180))
		if err == nil {
			for _, d := range heat {
				resp.Heatmap = append(resp.Heatmap, heatmapDay{Date: d.Date, Count: d.Count, GoodCount: d.GoodCount})
			}
		}
		fc, err := h.store.ReviewLoadForecast(ctx, focusID, dayStart(now), 30)
		if err == nil {
			for _, b := range fc {
				resp.Forecast = append(resp.Forecast, forecastBucket{Date: b.Date, Count: b.Count})
			}
		}
		resp.Streak, _ = h.store.ReviewStreakDays(ctx, focusID, now)
		resp.Params, _ = h.store.GetFSRSParams(ctx, focusID)
		resp.TotalCards, _ = h.store.CountReviewItems(ctx, focusID)
		due, err := h.store.ListDueReviewItems(ctx, focusID, dayStart(now),
			h.remainingNewCards(ctx, focusID), fsrsTotalQueueCap)
		if err == nil {
			resp.DueNow = len(due)
		}
	}

	if kids, err := h.store.ListNonPausedChildren(ctx); err == nil {
		for _, k := range kids {
			ks := kidReviewStats{UserID: k.ID, Name: k.Name}
			ks.Streak, _ = h.store.ReviewStreakDays(ctx, k.ID, now)
			ks.Total, _ = h.store.CountReviewItems(ctx, k.ID)
			due, err := h.store.ListDueReviewItems(ctx, k.ID, dayStart(now),
				h.remainingNewCards(ctx, k.ID), fsrsTotalQueueCap)
			if err == nil {
				ks.DueNow = len(due)
			}
			resp.PerKid = append(resp.PerKid, ks)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetFSRSParams serves GET /api/admin/fsrs-params?user_id= — the
// parent's scheduler dial (defaults when never touched).
func (h *ReviewHandler) GetParams(w http.ResponseWriter, r *http.Request) {
	userID, err := h.resolveKid(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	p, err := h.store.GetFSRSParams(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load fsrs params")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// fsrsParamsRequest is the editable subset: the retention dial and the
// fuzz switch. Everything else keeps its stored (or default) value.
type fsrsParamsRequest struct {
	RequestRetention *float64 `json:"request_retention"`
	MaximumInterval  *int64   `json:"maximum_interval"`
	EnableFuzz       *bool    `json:"enable_fuzz"`
}

// UpdateParams serves PUT /api/admin/fsrs-params — the retention dial.
// The accepted band is 0.80–0.95 (Anki's docs: 0.90 is the recommended
// value, >0.97 overloads the kid's day — the slider never offers it).
func (h *ReviewHandler) UpdateParams(w http.ResponseWriter, r *http.Request) {
	userID, err := h.resolveKid(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	var req fsrsParamsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p, err := h.store.GetFSRSParams(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load fsrs params")
		return
	}
	if req.RequestRetention != nil {
		v := *req.RequestRetention
		if v < 0.80 || v > 0.95 {
			writeError(w, http.StatusBadRequest, "request_retention must be between 0.80 and 0.95")
			return
		}
		p.RequestRetention = v
	}
	if req.MaximumInterval != nil {
		if *req.MaximumInterval < 1 || *req.MaximumInterval > 36500 {
			writeError(w, http.StatusBadRequest, "maximum_interval must be between 1 and 36500")
			return
		}
		p.MaximumInterval = *req.MaximumInterval
	}
	if req.EnableFuzz != nil {
		p.EnableFuzz = *req.EnableFuzz
	}
	if err := h.store.SaveFSRSParams(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save fsrs params")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// suspendRequest flips the parent's pause switch.
type suspendRequest struct {
	Suspended *bool `json:"suspended"`
}

// SuspendReview serves POST /api/admin/review/{id}/suspend — pause or
// resume one card.
func (h *ReviewHandler) SuspendReview(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid review item id")
		return
	}
	var req suspendRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Suspended == nil {
		writeError(w, http.StatusBadRequest, "suspended is required")
		return
	}
	if err := h.store.SetReviewItemSuspended(r.Context(), id, *req.Suspended); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update review item")
		return
	}
	item, err := h.store.GetReviewItem(r.Context(), id)
	if err != nil || item == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// resolveKid reads ?user_id= (the admin dial is per-kid; a query
// string, not a route param).
func (h *ReviewHandler) resolveKid(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
}
