package api

// Parent-side share list (TX-SHARE-LIST-API-CODER). The kid's share
// links are invisible to the parent once minted (the raw token is
// returned exactly once, to the kid), so this list is the revoke UI's
// data source: every share row for one kid, with a derived status and
// none of the sensitive columns (no token_hash, no raw token, no
// snapshot, no answers).
//
//	GET /api/admin/taxonomy/shares?user_id=<positive int>
//
// Rules frozen by the task card:
//   - admin-only (the RequireAdmin route group)
//   - missing/invalid user_id → 400 JSON error
//   - status: revoked (RevokedAt set) > expired (ExpiresAt set and
//     now not before it) > active
//   - empty list must serialize as [], never null

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// Share statuses (frozen contract).
const (
	shareStatusActive  = "active"
	shareStatusExpired = "expired"
	shareStatusRevoked = "revoked"
)

// shareListItem is one row of the admin list. Only the columns the
// revoke UI needs — the model's DiagnosticShare JSON tags would leak
// nothing (token_hash is json:"-"), but re-declaring here keeps the
// response stable even if the model grows a sensitive field.
type shareListItem struct {
	ID         int64      `json:"id"`
	OwnerID    int64      `json:"owner_id"`
	SessionID  *int64     `json:"session_id"`
	ReportKind string     `json:"report_kind"`
	ExpiresAt  *time.Time `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	CreatedAt  time.Time  `json:"created_at"`
	Status     string     `json:"status"`
}

// shareStatus derives the frozen status order: revoked wins over
// expired (a revoked share stays revoked even after it also expires).
func shareStatus(d *model.DiagnosticShare, now time.Time) string {
	if d.RevokedAt != nil {
		return shareStatusRevoked
	}
	if d.ExpiresAt != nil && !now.Before(*d.ExpiresAt) {
		return shareStatusExpired
	}
	return shareStatusActive
}

// ListShares serves GET /api/admin/taxonomy/shares?user_id= — every
// share link minted for one kid, newest first, with the derived status
// the revoke UI shows. The response never contains the token hash, the
// raw token, the snapshot or any child answer data.
func (h *TaxonomyHandler) ListShares(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query().Get("user_id")
	if v == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	userID, err := strconv.ParseInt(v, 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid user_id")
		return
	}
	shares, err := h.store.ListDiagnosticShares(r.Context(), userID)
	if err != nil {
		log.Printf("api: list diagnostic shares for user %d failed: %v", userID, err)
		writeError(w, http.StatusInternalServerError, "failed to list shares")
		return
	}
	now := time.Now()
	items := make([]shareListItem, 0, len(shares))
	for i := range shares {
		d := &shares[i]
		items = append(items, shareListItem{
			ID:         d.ID,
			OwnerID:    d.OwnerID,
			SessionID:  d.SessionID,
			ReportKind: d.ReportKind,
			ExpiresAt:  d.ExpiresAt,
			RevokedAt:  d.RevokedAt,
			CreatedAt:  d.CreatedAt,
			Status:     shareStatus(d, now),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": items})
}
