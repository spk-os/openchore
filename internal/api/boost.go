package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// Grandparent boost links (P4-16): a BusyKid BusyPay idea, localized —
// the currency is points, never money. A grown-up generates a
// token-addressed read-only page per kid; the elder opens it by QR with
// zero registration, sees a curated snapshot (streak, this week's
// completions, the coach's line) and taps a boost button. Caps keep the
// boost a garnish: a monthly allowance (default 60) and at most 3 boosts
// a day per link. The boost lands in the ledger
// (reason='grandparent_boost') with the link as its audit trail.

// boostAmounts are the amounts a boost button may carry — three tiers,
// all in points.
var boostAmounts = []int{5, 10, 20}

// BoostHandler serves both ends of a boost link: the elder's token
// page (public — the token is the auth) and the grown-up's link
// management (admin).
type BoostHandler struct {
	store *store.Store
}

func NewBoostHandler(s *store.Store) *BoostHandler {
	return &BoostHandler{store: s}
}

// boostSnapshot is the curated read-only view the elder's page renders.
// Deliberately small: a greeting, three stats, the coach's line and the
// boost buttons with what's left of the allowance. No chore details, no
// balance, no photos — only what a family would show over the phone.
type boostSnapshot struct {
	KidName          string `json:"kid_name"`
	Label            string `json:"label"`
	CurrentStreak    int    `json:"current_streak"`
	WeekCompleted    int    `json:"week_completed"`
	CoachLine        string `json:"coach_line"`
	MonthlyCap       int    `json:"monthly_cap"`
	GrantedThisMonth int    `json:"granted_this_month"`
	DailyLimit       int    `json:"daily_limit"`
	Amounts          []int  `json:"amounts"`
}

// GetBoostPage resolves a token to the elder's read-only snapshot.
// The token is the only credential; unknown, revoked or expired links
// all answer 404 so the page says "this link is no longer available"
// without leaking which.
func (h *BoostHandler) GetBoostPage(w http.ResponseWriter, r *http.Request) {
	token := urlParam(r, "token")
	link, err := h.store.GetBoostLinkByToken(r.Context(), token)
	if errors.Is(err, store.ErrBoostLinkNotFound) {
		writeError(w, http.StatusNotFound, "boost link not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load boost link")
		return
	}
	snap, err := h.snapshot(r, link)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load snapshot")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// PostBoost applies the elder's gift. 422 with a phrase the page can
// show ("the monthly allowance is used up", "come back tomorrow")
// rather than a bare error.
func (h *BoostHandler) PostBoost(w http.ResponseWriter, r *http.Request) {
	token := urlParam(r, "token")
	link, err := h.store.GetBoostLinkByToken(r.Context(), token)
	if errors.Is(err, store.ErrBoostLinkNotFound) {
		writeError(w, http.StatusNotFound, "boost link not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load boost link")
		return
	}
	var req struct {
		Amount int `json:"amount"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validBoostAmount(req.Amount) {
		writeError(w, http.StatusBadRequest, "amount must be one of 5, 10 or 20")
		return
	}
	now := time.Now()
	if err := h.store.ApplyBoost(r.Context(), link, req.Amount, now); err != nil {
		switch {
		case errors.Is(err, store.ErrBoostCapReached):
			writeError(w, http.StatusUnprocessableEntity, "monthly boost cap reached")
		case errors.Is(err, store.ErrBoostDailyLimit):
			writeError(w, http.StatusUnprocessableEntity, "daily boost limit reached")
		default:
			writeError(w, http.StatusInternalServerError, "failed to apply boost")
		}
		return
	}
	// Reload: the caller's copy went stale the moment the boost moved
	// the granted tally.
	fresh, err := h.store.GetBoostLink(r.Context(), link.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load snapshot")
		return
	}
	snap, err := h.snapshot(r, fresh)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load snapshot")
		return
	}
	writeJSON(w, http.StatusCreated, snap)
}

// ListLinks returns a kid's links for the grown-up's management view.
func (h *BoostHandler) ListLinks(w http.ResponseWriter, r *http.Request) {
	userID, err := urlParamInt64(r, "userID")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	links, err := h.store.ListBoostLinks(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list boost links")
		return
	}
	writeJSON(w, http.StatusOK, links)
}

// CreateLink mints a fresh token link for a kid. The body is optional —
// it may carry a label ("Grandma") for the management list.
func (h *BoostHandler) CreateLink(w http.ResponseWriter, r *http.Request) {
	userID, err := urlParamInt64(r, "userID")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if u, err := h.store.GetUser(r.Context(), userID); err != nil || u == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	var req struct {
		Label string `json:"label"`
	}
	// An empty body is fine — the label is optional.
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	if len([]rune(req.Label)) > 60 {
		writeError(w, http.StatusBadRequest, "label must be at most 60 characters")
		return
	}
	link, err := h.store.CreateBoostLink(r.Context(), userID, req.Label,
		model.BoostDefaultMonthlyCap, model.BoostDefaultDailyLimit, model.BoostDefaultDays)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create boost link")
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

// RevokeLink kills a link's token — the elder's page stops working.
func (h *BoostHandler) RevokeLink(w http.ResponseWriter, r *http.Request) {
	linkID, err := urlParamInt64(r, "linkID")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid link id")
		return
	}
	userID, err := urlParamInt64(r, "userID")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if err := h.store.RevokeBoostLink(r.Context(), linkID, userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke boost link")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

// snapshot gathers the elder's curated view. The month's tally rolls
// over lazily: a granted count from a past month reads as zero.
func (h *BoostHandler) snapshot(r *http.Request, link *model.BoostLink) (*boostSnapshot, error) {
	user, err := h.store.GetUser(r.Context(), link.UserID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, store.ErrBoostLinkNotFound
	}
	streak, err := h.store.GetUserStreak(r.Context(), link.UserID)
	if err != nil {
		return nil, err
	}
	weekStart, weekEnd := periodRange("week", time.Now())
	completed, err := h.store.CountApprovedCompletions(r.Context(), link.UserID,
		weekStart.Format(model.DateFormat), weekEnd.Format(model.DateFormat))
	if err != nil {
		return nil, err
	}
	// The coach's line for today, when the nightly note has run. A
	// growth-minded sentence beats any stat — but its absence is fine.
	coachLine := ""
	if note, err := h.store.GetDailyCoachNote(r.Context(), link.UserID,
		time.Now().Format(model.DateFormat)); err == nil && note != nil {
		coachLine = note.ChildText
	}
	return &boostSnapshot{
		KidName:          user.Name,
		Label:            link.Label,
		CurrentStreak:    streak.CurrentStreak,
		WeekCompleted:    completed,
		CoachLine:        coachLine,
		MonthlyCap:       link.MonthlyCap,
		GrantedThisMonth: grantedThisMonth(link, time.Now()),
		DailyLimit:       link.DailyLimit,
		Amounts:          boostAmounts,
	}, nil
}

// grantedThisMonth reports the points granted in the current calendar
// month — a stale month's tally reads as zero.
func grantedThisMonth(link *model.BoostLink, now time.Time) int {
	if link.GrantedMonth != now.Format("2006-01") {
		return 0
	}
	return link.Granted
}

// validBoostAmount reports whether the amount is one of the tiers.
func validBoostAmount(amount int) bool {
	for _, a := range boostAmounts {
		if amount == a {
			return true
		}
	}
	return false
}
