package api

import "net/http"

// DeleteErrorItem removes one wrong question from the book for good (the
// parent's cleanup of misparsed items — the kid's book stays theirs).
func (h *ChoreHandler) DeleteErrorItem(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid error item id")
		return
	}
	item, err := h.store.GetErrorItem(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get error item")
		return
	}
	if item == nil {
		writeError(w, http.StatusNotFound, "error item not found")
		return
	}
	if err := h.store.DeleteErrorItem(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete error item")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}
