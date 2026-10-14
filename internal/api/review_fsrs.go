package api

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	fsrs "github.com/open-spaced-repetition/go-fsrs/v4"

	"github.com/liftedkilt/openchore/internal/model"
)

// The FSRS scheduler bridge (L4). This file is the only place the
// go-fsrs library is touched: Card ↔ ReviewItem mapping, Parameters ←
// fsrs_params, and the rating path. Everything else (queue math, caps,
// stats) lives in the store.
//
// Kid-facing grading maps the two buttons onto the four-rating scale
// (Anki's FAQ names the Hard-instead-of-Again habit as FSRS's one
// uncorrectable failure mode, so those buttons don't exist); parents
// may use the full four.

// fsrsDailyNewCardsSetting is the app_settings key for the daily
// new-card cap (Anki's "New cards/day"; default 20, its docs' own
// recommended ceiling — new cards amplify ~10× into future reviews).
const fsrsDailyNewCardsSetting = "fsrs_daily_new_cards"

// fsrsDefaultDailyNewCards is the default cap before any setting exists.
const fsrsDefaultDailyNewCards = 20

// fsrsTotalQueueCap is the whole-queue storm guard (spec §7.3).
const fsrsTotalQueueCap = 200

// parseState converts the stored TEXT state into the library's State.
func parseState(s string) fsrs.State {
	switch s {
	case model.ReviewStateLearning:
		return fsrs.Learning
	case model.ReviewStateReview:
		return fsrs.Review
	case model.ReviewStateRelearning:
		return fsrs.Relearning
	default:
		return fsrs.New
	}
}

// stateString converts the library's State into the stored TEXT form.
func stateString(st fsrs.State) string {
	switch st {
	case fsrs.Learning:
		return model.ReviewStateLearning
	case fsrs.Review:
		return model.ReviewStateReview
	case fsrs.Relearning:
		return model.ReviewStateRelearning
	default:
		return model.ReviewStateNew
	}
}

// parseRating converts a stored (or request-body) rating name into the
// library's Rating. The empty string maps to Good (a missing grade
// should never crash the scheduler).
func parseRating(s string) fsrs.Rating {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "again":
		return fsrs.Again
	case "hard":
		return fsrs.Hard
	case "easy":
		return fsrs.Easy
	default:
		return fsrs.Good
	}
}

// ratingString converts the library's Rating into the stored TEXT form.
func ratingString(r fsrs.Rating) string {
	switch r {
	case fsrs.Again:
		return model.ReviewRatingAgain
	case fsrs.Hard:
		return model.ReviewRatingHard
	case fsrs.Easy:
		return model.ReviewRatingEasy
	default:
		return model.ReviewRatingGood
	}
}

// itemToCard maps a stored ReviewItem onto the library's Card. The
// scheduler columns transfer one-for-one; LastReview's NULL becomes the
// zero time the library expects for never-rated cards.
func itemToCard(i *model.ReviewItem) fsrs.Card {
	card := fsrs.Card{
		Due:            i.Due,
		Stability:      i.Stability,
		Difficulty:     i.Difficulty,
		ScheduledDays:  uint64(i.ScheduledDays),
		Reps:           uint64(i.Reps),
		Lapses:         uint64(i.Lapses),
		State:          parseState(i.State),
		RemainingSteps: i.RemainingSteps,
	}
	if i.LastReview != nil {
		card.LastReview = *i.LastReview
	}
	return card
}

// cardToItem writes a rated Card's scheduler columns back onto the
// stored ReviewItem (the grade path's only scheduler-column update).
func cardToItem(card fsrs.Card, i *model.ReviewItem) {
	i.Due = card.Due
	i.Stability = card.Stability
	i.Difficulty = card.Difficulty
	i.ScheduledDays = int64(card.ScheduledDays)
	i.Reps = int64(card.Reps)
	i.Lapses = int64(card.Lapses)
	i.State = stateString(card.State)
	i.LastReview = &card.LastReview
	i.RemainingSteps = card.RemainingSteps
}

// parseStepsJSON decodes a "[1,10]" steps column. Bad JSON degrades to
// the library defaults rather than failing the scheduler.
func parseStepsJSON(raw string) []float64 {
	var steps []float64
	if err := json.Unmarshal([]byte(raw), &steps); err != nil || len(steps) == 0 {
		return fsrs.DefaultLearningSteps()
	}
	return steps
}

// schedulerFor builds the library scheduler from the kid's stored
// params (defaults when no row exists).
func schedulerFor(ctx context.Context, h *ReviewHandler, userID int64) (*fsrs.FSRS, *model.FSRSParams) {
	p, err := h.store.GetFSRSParams(ctx, userID)
	if err != nil {
		p = &model.FSRSParams{RequestRetention: 0.90, MaximumInterval: 36500,
			LearningStepsJSON: "[1,10]", RelearningStepsJSON: "[10]", EnableFuzz: true}
	}
	param := fsrs.Parameters{
		RequestRetention: p.RequestRetention,
		MaximumInterval:  float64(p.MaximumInterval),
		EnableShortTerm:  true,
		EnableFuzz:       p.EnableFuzz,
		LearningSteps:    parseStepsJSON(p.LearningStepsJSON),
		RelearningSteps:  parseStepsJSON(p.RelearningStepsJSON),
	}
	if p.WeightsJSON != "" {
		var w [21]float64
		if err := json.Unmarshal([]byte(p.WeightsJSON), &w); err == nil {
			param.W = w
		}
	}
	return fsrs.NewFSRS(param), p
}

// dailyNewCards reads the parent's new-card cap (default 20).
func (h *ReviewHandler) dailyNewCards(ctx context.Context) int {
	if v := mustGetSetting(ctx, h.store, fsrsDailyNewCardsSetting); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 200 {
			return n
		}
	}
	return fsrsDefaultDailyNewCards
}

// dayStart returns the local midnight of t (the queue's day cutoff).
func dayStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// EnsureReviewCard is the T+0 creation hook: when an L1 correction is
// reported (mastery New → Reviewing) or an L2 outcome is met, the
// source object immediately gets a review card — the item reviews the
// same night, not at tomorrow's materialization. Front/back carry the
// kid-facing text; subject feeds the card's badge. An existing card for
// the same source is left untouched (UNIQUE(source_type, source_id)).
func (h *ReviewHandler) EnsureReviewCard(ctx context.Context, userID int64, sourceType string, sourceID int64, front, back, subject string) {
	item := &model.ReviewItem{
		UserID:     userID,
		SourceType: sourceType,
		SourceID:   sourceID,
		FrontText:  front,
		BackText:   back,
		Subject:    subject,
		Due:        time.Now(),
		State:      model.ReviewStateNew,
	}
	if err := h.store.CreateReviewItem(ctx, item, true); err != nil {
		log.Printf("review: ensure card for %s/%d failed: %v", sourceType, sourceID, err)
	}
}

// GradeReviewCard runs one rating through the scheduler: Next() updates
// the Card, the ReviewLog lands in review_logs, and the caller gets the
// updated card back.
func (h *ReviewHandler) GradeReviewCard(ctx context.Context, item *model.ReviewItem, rating fsrs.Rating, now time.Time) (*model.ReviewItem, error) {
	s, _ := schedulerFor(ctx, h, item.UserID)
	// Capture the card's pre-rating state before the scheduler writes the
	// post-rating one back onto the item: the log's state is what the card
	// WAS (Anki's revlog "type" column records the state at review), and
	// elapsed_days counts from the previous review, not from now.
	oldState := item.State
	var oldLastReview *time.Time
	if item.LastReview != nil {
		t := *item.LastReview
		oldLastReview = &t
	}
	info, err := s.Next(itemToCard(item), now, rating)
	if err != nil {
		return nil, err
	}
	cardToItem(info.Card, item)
	if err := h.store.UpdateReviewItemCard(ctx, item); err != nil {
		return nil, err
	}
	var elapsed *int64
	if oldLastReview != nil {
		d := int64(now.Sub(*oldLastReview).Hours() / 24)
		elapsed = &d
	}
	lg := &model.ReviewLog{
		ReviewItemID:  item.ID,
		UserID:        item.UserID,
		Rating:        ratingString(rating),
		State:         oldState,
		ElapsedDays:   elapsed,
		ScheduledDays: int64(info.Card.ScheduledDays),
		ReviewedAt:    now,
	}
	if err := h.store.InsertReviewLog(ctx, lg); err != nil {
		return nil, err
	}
	return item, nil
}

// CreditReviewPoints anchors the day's queue-completion credit: one row
// per kid per date, keyed like the missed-chore penalty. A duplicate
// insert (reopened app, retry) is a no-op.
func (h *ReviewHandler) CreditReviewPoints(ctx context.Context, userID int64, date string) error {
	key := "review_complete:" + strconv.FormatInt(userID, 10) + ":" + date
	return h.store.CreditReviewPoints(ctx, userID, date, key)
}
