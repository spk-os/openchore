package api

// syncKCReviewCard — the TaxonomyHandler-side hook of the KC → FSRS
// card sync (TX-KC-REVIEW-SYNC-V2-CODER). The practice/diagnostic
// mastery paths call it after a KC's mastery status is settled; it
// forwards to store.SyncKCReviewCard (internal/store/
// taxonomy_suspend.go) with the semantics frozen there:
//
//   - mastered  → suspend the KC's active card (never un-suspend)
//   - weak/shaky → create the card if missing (never touch an
//     existing card's suspended flag or FSRS scheduler columns)
//   - unknown   → no-op
//
// The card sync is best-effort by design: a failure here must never
// overturn an already-committed learning event (the practice answer,
// the diagnostic attempt), so errors are logged, not propagated.
// Routes are wired elsewhere; this file registers nothing.

import (
	"context"
	"log"
)

// syncKCReviewCard mirrors one KC's mastery status into the target
// user's review card for it, best-effort: a nil handler or store is a
// silent no-op (the taxonomy pack's tests run without one), and a
// store error is only logged — the committed learning event stands.
func (h *TaxonomyHandler) syncKCReviewCard(ctx context.Context, userID, tagID int64, status string) {
	if h == nil || h.store == nil {
		return
	}
	if _, err := h.store.SyncKCReviewCard(ctx, userID, tagID, status); err != nil {
		log.Printf("taxonomy: sync review card for kc %d (user %d, status %s): %v",
			tagID, userID, status, err)
	}
}
