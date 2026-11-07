package api

import (
	"log"
	"net/http"

	"github.com/liftedkilt/openchore/internal/store"
	"github.com/liftedkilt/openchore/internal/taxonomy"
)

// Taxonomy status observability (card FR2-B): GET /api/admin/taxonomy/
// status. Read-only pack import reconciliation (embedded fingerprint vs
// the stored one, last sync time, row counts) plus the FR-11 binding
// ratio — in-system KC bindings vs fallback free-text tags. This file
// composes existing pieces (PackFingerprint, the two pack settings,
// CountTaxonomy, TaxonomyBindingStats); it never mutates anything.

// taxonomyStatusResponse is the frozen /admin/taxonomy/status payload.
// last_sync_at is nil (JSON null) when the pack has never synced; the
// binding block mirrors store.TaxonomyBindingStats verbatim.
type taxonomyStatusResponse struct {
	Fingerprint      string                      `json:"fingerprint"`
	FingerprintMatch bool                        `json:"fingerprint_match"`
	LastSyncAt       *string                     `json:"last_sync_at"`
	TopicCount       int64                       `json:"topic_count"`
	EdgeCount        int64                       `json:"edge_count"`
	QuestionCount    int64                       `json:"question_count"`
	StandardCount    int64                       `json:"standard_count"`
	Binding          *store.TaxonomyBindingStats `json:"binding"`
}

// AdminTaxonomyStatus serves GET /api/admin/taxonomy/status. The route
// is registered inside the RequireAdmin group, so no auth is repeated
// here. Any failure logs the cause server-side and answers with one
// stable generic message — store/embed details never echo back.
func (h *TaxonomyHandler) AdminTaxonomyStatus(w http.ResponseWriter, r *http.Request) {
	fail := func(err error) {
		log.Printf("taxonomy: status: %v", err)
		writeError(w, http.StatusInternalServerError, "taxonomy status failed")
	}

	fp, err := taxonomy.PackFingerprint()
	if err != nil {
		fail(err)
		return
	}
	stored, err := h.store.GetSetting(r.Context(), packFingerprintKey)
	if err != nil {
		fail(err)
		return
	}
	syncedAt, err := h.store.GetSetting(r.Context(), packSyncedAtKey)
	if err != nil {
		fail(err)
		return
	}
	counts, err := h.store.CountTaxonomy(r.Context())
	if err != nil {
		fail(err)
		return
	}
	binding, err := h.store.TaxonomyBindingStats(r.Context())
	if err != nil {
		fail(err)
		return
	}

	// A missing synced-at setting (never synced) stays null, not "".
	var lastSyncAt *string
	if syncedAt != "" {
		lastSyncAt = &syncedAt
	}
	writeJSON(w, http.StatusOK, taxonomyStatusResponse{
		Fingerprint:      fp,
		FingerprintMatch: stored == fp,
		LastSyncAt:       lastSyncAt,
		TopicCount:       counts.Topics,
		EdgeCount:        counts.Prerequisites,
		QuestionCount:    counts.Questions,
		StandardCount:    counts.Standards,
		Binding:          binding,
	})
}
