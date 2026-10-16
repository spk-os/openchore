package api

import (
	"net/http"
	"strconv"
)

// The parent-side media library endpoints (036): one place to browse every
// photo / video / recording a kid uploaded, grouped by subject, media
// class, date and chore. Filters are all optional query parameters; the
// subject vocabulary is the error book's five.

// HandleListMedia serves GET /api/admin/media?subject=&class=&from=&to=&
// chore_id=&user_id=&limit=. Admin-only: the library spans every kid.
func (h *ChoreHandler) HandleListMedia(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	choreID, _ := strconv.ParseInt(q.Get("chore_id"), 10, 64)
	userID, _ := strconv.ParseInt(q.Get("user_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	items, err := h.store.ListMediaItems(r.Context(),
		q.Get("subject"), q.Get("class"), q.Get("from"), q.Get("to"),
		choreID, userID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list media")
		return
	}
	subjects, classes, total, err := h.store.MediaLibraryStats(r.Context(),
		q.Get("class"), q.Get("from"), q.Get("to"), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to summarize media")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    items,
		"subjects": subjects,
		"classes":  classes,
		"total":    total,
	})
}
