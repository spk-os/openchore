package api

// L3 learning-data export/restore HTTP handlers (TX-LEARNING-HTTP-V2).
// These handlers serve the frozen model.LearningExport document — the
// portable, stable-ref-keyed counterpart to the legacy taxonomy.go
// ExportPayload handlers (now removed), whose review_items were always
// empty, events were capped at 1000, mastery carried only
// machine-local integer ids and the restore rebuilt nothing but
// diagnostic shells.
//
// Contract:
//   - ExportLearningDocument: auth via h.learningExportTarget (an admin
//     names any user with ?user_id=, a kid can only export their own
//     data). The document is served as a Content-Disposition attachment
//     with Cache-Control: no-store (a per-kid document must never come
//     back from a shared cache) and X-Content-Type-Options: nosniff.
//   - ImportLearningDocument: admin-only (RequireAdmin on the route, and
//     the handler re-checks the context role — defense in depth), body
//     capped at 20MB, strict JSON (unknown fields / trailing content /
//     bad JSON / oversized bodies are 400), document metadata validated
//     up front (schema_version must be exactly 1, pack_version non-blank,
//     exported_at non-zero), explicit mandatory target_user_id that must
//     name an existing user, and the restore result reports per-section
//     imported/skipped counts.
//
// The document's own User.ID is never a restore fallback: without an
// explicit target_user_id the import is refused, so one kid's data can
// never silently land on another profile.

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// learningImportMaxBytes caps the restore document at 20MB: a full
// mastery/event/diagnostic deck is nowhere near it, and a runaway body
// must not be buffered whole into memory.
const learningImportMaxBytes = 20 << 20

// learningExportSchemaVersion is the only schema_version this HTTP
// layer accepts — the same value the store restore enforces. Anything
// else (0, negative, or a newer schema) is refused with 400 before the
// store is touched, so a newer document can never be half-imported.
const learningExportSchemaVersion = 1

// learningExportTarget is listTarget for the export view: kids default
// to themselves, admins must name a user. Unlike listTarget it does NOT
// do the existence check itself — the export path maps a missing user to
// 404 (via the store's ErrLearningExportUserNotFound) rather than the
// list view's 400, so the existence lookup is left to the store and
// only the auth/shape rules run here.
//
// Non-admin rules, exhaustively: no user_id → self; an explicit user_id
// is allowed only when it parses to the caller's own positive id. Any
// successfully parsed-but-different value — including 0 and negatives,
// which can equal a synthesized session id — is a probe for someone
// else's data and gets 403, never a "missing self" that falls through
// to the store's 404. An unparseable value keeps the existing stable
// 403. API tokens are a synthesized admin (id 0) and belong to the
// admin branch, which requires an explicit positive user_id — there is
// no "token exports itself" path.
func (h *TaxonomyHandler) learningExportTarget(w http.ResponseWriter, r *http.Request) (int64, bool) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return 0, false
	}
	if user.Role == model.RoleAdmin {
		v := r.URL.Query().Get("user_id")
		if v == "" {
			writeError(w, http.StatusBadRequest, "user_id is required")
			return 0, false
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid user_id")
			return 0, false
		}
		return id, true
	}
	if v := r.URL.Query().Get("user_id"); v != "" {
		// Explicit ids must name the caller's own positive id exactly:
		// a parsed 0/negative could equal a synthesized session id and
		// must not slide through as "self" into the store's 404.
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 || id != user.ID {
			writeError(w, http.StatusForbidden, "cannot read another user's sessions")
			return 0, false
		}
	}
	return user.ID, true
}

// ExportLearningDocument serves one kid's learning data as a JSON
// download built from the store layer (mastery keyed by external_ref,
// the full event stream, diagnostics + attempts, and the review deck
// with stable source refs).
func (h *TaxonomyHandler) ExportLearningDocument(w http.ResponseWriter, r *http.Request) {
	target, ok := h.learningExportTarget(w, r)
	if !ok {
		return
	}
	doc, err := h.store.BuildLearningExport(r.Context(), target)
	if err != nil {
		if errors.Is(err, store.ErrLearningExportUserNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		// Anything else is a store/SQL failure: log the detail, never
		// leak it to the client.
		log.Printf("learning export: user %d: %v", target, err)
		writeError(w, http.StatusInternalServerError, "failed to build export")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="openchore-learning-export.json"`)
	// A per-kid document must not be served from a shared/proxy cache,
	// and a JSON download must not be MIME-sniffed into something else.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeJSON(w, http.StatusOK, doc)
}

// ImportLearningDocument restores a learning-data document onto an
// explicit target kid. Admin-only; the JSON is strict and size-capped,
// the document metadata (schema_version/pack_version/exported_at) is
// validated up front, and the target must already exist on this server
// (the restore never creates profiles).
func (h *TaxonomyHandler) ImportLearningDocument(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil || user.Role != model.RoleAdmin {
		writeError(w, http.StatusForbidden, "admin access required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, learningImportMaxBytes)
	defer r.Body.Close() // closed on every exit path, not just the happy one
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // a document with extra keys is a schema drift, refuse it whole
	var payload model.LearningExport
	if err := dec.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Document metadata gates: a drifted or unsigned document is refused
	// before any store access, so no section of it can half-land.
	if payload.SchemaVersion != learningExportSchemaVersion {
		writeError(w, http.StatusBadRequest, "unsupported export schema_version")
		return
	}
	if strings.TrimSpace(payload.PackVersion) == "" {
		writeError(w, http.StatusBadRequest, "pack_version is required")
		return
	}
	if payload.ExportedAt.IsZero() {
		writeError(w, http.StatusBadRequest, "exported_at is required")
		return
	}
	if payload.TargetUserID <= 0 {
		writeError(w, http.StatusBadRequest, "target_user_id is required")
		return
	}
	target, err := h.store.GetUser(r.Context(), payload.TargetUserID)
	if err != nil {
		// A broken lookup is a server fault, not a bad request.
		log.Printf("learning import: target lookup %d: %v", payload.TargetUserID, err)
		writeError(w, http.StatusInternalServerError, "failed to load target user")
		return
	}
	if target == nil {
		writeError(w, http.StatusBadRequest, "target user does not exist on this server")
		return
	}
	res, err := h.store.RestoreLearning(r.Context(), payload.TargetUserID, &payload)
	if err != nil {
		log.Printf("learning import: target %d: %v", payload.TargetUserID, err)
		// Map the store's own sentinel errors; never guess the cause
		// from the payload again.
		switch {
		case errors.Is(err, store.ErrLearningRestoreUnsupportedSchema):
			writeError(w, http.StatusBadRequest, "unsupported export schema_version")
		case errors.Is(err, store.ErrLearningRestoreTargetNotFound):
			writeError(w, http.StatusBadRequest, "target user does not exist on this server")
		default:
			writeError(w, http.StatusInternalServerError, "import failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, res)
}
