package api

// Admin prerequisite-edge handlers (TX-PREREQ-API-V4-CODER,
// DEF-PREREQ-CONCURRENCY-R1). The parent edits the knowledge graph's
// prerequisite DAG through the store DAO (internal/store/
// prerequisites.go) while the engine's in-memory catalog is rebuilt
// copy-on-write through taxonomy.Catalog.WithEdges: the DB (042
// tables) is the durable source of truth for the effective edge set,
// h.catalog is its engine-side projection.
//
//	PutPrerequisite    — PUT: add one edge or rewrite an existing one
//	                     {topic_id, prerequisite_id, strength, reason}
//	ReviewPrerequisite — PATCH: flip one stored edge's review status
//	                     {topic_id, prerequisite_id, review_status}
//
// Routes are wired separately (router.go is not this file's scope).
// Error mapping, frozen by the task card:
//
//   - sql.ErrNoRows (unknown ref)        → 404
//   - missing params / strength / status → 400
//   - cycle / status conflict            → 409
//   - anything else                      → 500
//
// Concurrency (DEF-PREREQ-CONCURRENCY-R1): both mutations run under
// the package-level prereqWriteMu from before the first effective-edge
// read until the DB write and the catalog swap are done, so two
// concurrent admins can never interleave their read-modify-write
// cycles (A←B and B←A used to each pass their own gate and land a
// cyclic DB). Inside the lock the proposed effective set is built from
// the latest DB edges and validated into a candidate catalog first —
// every proposed edge's endpoints must resolve in the current catalog
// (a persisted row the pack doesn't know is version drift: a logged
// 500, never a fake cycle 409), a self-loop is a 400, and WithEdges'
// remaining refusal is the real DAG cycle — a 409 with nothing
// written. Only then does the DAO write run, and after it commits the
// already-validated candidate is swapped in directly: no post-write
// reload/rebuild, so a write can no longer commit while its rebuild
// fails and the handler answers 200 over a stale catalog. A failed
// validation never writes and never swaps.
//
// Locking order: prereqWriteMu → loadMu (catalogOnce and swapCatalog
// take loadMu themselves, so they are never called while loadMu is
// held). Nothing may take prereqWriteMu while holding loadMu.

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/liftedkilt/openchore/internal/taxonomy"
)

// prereqWriteMu serializes the admin prerequisite mutations (PUT and
// PATCH) process-wide: the read-effective → propose → validate →
// write → swap sequence is one critical section, so the proposed set
// each request validates is exactly the effective set its DB write
// produces. It lives at package level because TaxonomyHandler's field
// set is frozen in taxonomy.go (out of this file's scope).
var prereqWriteMu sync.Mutex

// Frozen vocabulary of the 042 edge table. The store DAO validates the
// same strings; local constants keep the handler's comparisons and its
// response payloads readable.
const (
	prereqStrengthHard   = "hard"
	prereqStrengthSoft   = "soft"
	prereqStatusReviewed = "reviewed"
	prereqStatusRejected = "rejected"
)

// prereqMsgPackDrift is the stable client copy for version drift: a
// persisted edge (or ref) the loaded pack cannot represent. The
// offending refs go to the log, never the response.
const prereqMsgPackDrift = "stored prerequisite edges reference topics missing from the taxonomy pack; re-import the pack"

// prerequisitePutRequest is the PUT body: add one edge, or rewrite the
// strength/reason of an existing one (same endpoints).
type prerequisitePutRequest struct {
	TopicID        string `json:"topic_id"`
	PrerequisiteID string `json:"prerequisite_id"`
	Strength       string `json:"strength"`
	Reason         string `json:"reason"`
}

// prerequisiteReviewRequest is the PATCH body: flip one stored edge
// between reviewed and rejected. The two refs identify the row;
// strength and reason are untouched.
type prerequisiteReviewRequest struct {
	TopicID        string `json:"topic_id"`
	PrerequisiteID string `json:"prerequisite_id"`
	ReviewStatus   string `json:"review_status"`
}

// prerequisiteEdgeResponse is the changed edge as served back. The PUT
// answers with the full written payload; the PATCH answers with the
// flip only (strength/reason stay empty). No question or answer data
// ever appears here.
type prerequisiteEdgeResponse struct {
	TopicID        string `json:"topic_id"`
	PrerequisiteID string `json:"prerequisite_id"`
	Strength       string `json:"strength,omitempty"`
	Reason         string `json:"reason,omitempty"`
	ReviewStatus   string `json:"review_status"`
}

// prerequisiteMutationResponse answers both handlers: the changed edge
// plus the size of the effective graph after the swap.
type prerequisiteMutationResponse struct {
	Edge           prerequisiteEdgeResponse `json:"edge"`
	EffectiveEdges int                      `json:"effective_edges"`
}

// PutPrerequisite serves the admin edge PUT — add one prerequisite
// edge or rewrite an existing one. The whole read-validate-write-swap
// sequence runs under prereqWriteMu: the proposed effective set (the
// DB's current edges with this edge replaced or appended) must rebuild
// into a valid DAG through WithEdges BEFORE the DAO runs, so an empty,
// unknown, self-referencing or cycle-forming edge never reaches the
// database. Only then does UpsertManualPrerequisite persist it
// (review_status='reviewed': a human adding an edge is the review) and
// the validated candidate catalog replaces the in-memory one — the
// lock guarantees the DB now holds exactly the proposed set, so no
// post-write reload is needed (or allowed: a reload that failed over a
// committed write used to answer 200 with a stale catalog).
func (h *TaxonomyHandler) PutPrerequisite(w http.ResponseWriter, r *http.Request) {
	var req prerequisitePutRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	topicRef := strings.TrimSpace(req.TopicID)
	prereqRef := strings.TrimSpace(req.PrerequisiteID)
	if topicRef == "" || prereqRef == "" {
		writeError(w, http.StatusBadRequest, "topic_id and prerequisite_id are required")
		return
	}
	if req.Strength != prereqStrengthHard && req.Strength != prereqStrengthSoft {
		writeError(w, http.StatusBadRequest, `strength must be "hard" or "soft"`)
		return
	}
	if topicRef == prereqRef {
		writeError(w, http.StatusBadRequest, "a topic cannot be its own prerequisite")
		return
	}
	prereqWriteMu.Lock()
	defer prereqWriteMu.Unlock()
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	// Unknown endpoints never reach the DB write: WithEdges would
	// reject them and the DAO would surface sql.ErrNoRows — answer the
	// clean 404 up front.
	if cat.Topic(topicRef) == nil {
		writeError(w, http.StatusNotFound, "unknown topic_id")
		return
	}
	if cat.Topic(prereqRef) == nil {
		writeError(w, http.StatusNotFound, "unknown prerequisite_id")
		return
	}
	edges, err := h.store.ListEffectivePrerequisiteEdges(r.Context())
	if err != nil {
		log.Printf("taxonomy: list prerequisite edges: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to load prerequisite edges")
		return
	}
	proposed := proposedPrerequisiteEdges(edges, &taxonomy.Edge{
		TopicID:        topicRef,
		PrerequisiteID: prereqRef,
		Strength:       req.Strength,
		Reason:         req.Reason,
		// What the DAO write will store — the validated set must be the
		// set the DB ends up with.
		ReviewStatus: prereqStatusReviewed,
	})
	// Version drift: a persisted edge the loaded pack can't represent
	// must refuse here (500) — with the request's own endpoints already
	// checked, letting it through to WithEdges would misreport it as a
	// cycle 409.
	if drift := firstEdgeMissingFromCatalog(cat, proposed); drift != nil {
		log.Printf("taxonomy: put prerequisite %s<-%s: persisted edge %s<-%s has an endpoint missing from the catalog",
			topicRef, prereqRef, drift.TopicID, drift.PrerequisiteID)
		writeError(w, http.StatusInternalServerError, prereqMsgPackDrift)
		return
	}
	// Gate: with both endpoints pre-checked, the self-loop rejected and
	// drifted rows refused above, a WithEdges failure here is the cycle
	// case — 409, nothing written, nothing swapped.
	candidate, err := cat.WithEdges(proposed)
	if err != nil {
		log.Printf("taxonomy: put prerequisite %s<-%s: %v", topicRef, prereqRef, err)
		writeError(w, http.StatusConflict, "prerequisite edge conflicts with the graph: "+err.Error())
		return
	}
	if err := h.store.UpsertManualPrerequisite(r.Context(), topicRef, prereqRef, req.Strength, req.Reason); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// The ref vanished from the DB between the pre-check and the
			// write (e.g. a concurrent re-import wiped the tag).
			writeError(w, http.StatusNotFound, "unknown topic_id or prerequisite_id")
		default:
			log.Printf("taxonomy: upsert prerequisite %s<-%s: %v", topicRef, prereqRef, err)
			writeError(w, http.StatusInternalServerError, "failed to save prerequisite edge")
		}
		return
	}
	// The lock held since the effective read means no other mutation
	// touched the edge set in between: the DB now holds exactly
	// `proposed`, so the candidate validated against it is swapped in
	// as-is. No reload — a reload/rebuild failing over this committed
	// write used to answer 200 with a stale catalog.
	h.swapCatalog(candidate)
	writeJSON(w, http.StatusOK, prerequisiteMutationResponse{
		Edge: prerequisiteEdgeResponse{
			TopicID:        topicRef,
			PrerequisiteID: prereqRef,
			Strength:       req.Strength,
			Reason:         req.Reason,
			ReviewStatus:   prereqStatusReviewed,
		},
		EffectiveEdges: len(candidate.Edges),
	})
}

// proposedPrerequisiteEdges splices one edge into an effective edge
// list: an entry with the same endpoints is replaced (the new
// strength/reason/review_status win), otherwise the edge is appended.
// The input list is never mutated; WithEdges value-copies every entry,
// so sharing pointers is safe.
func proposedPrerequisiteEdges(edges []*taxonomy.Edge, edge *taxonomy.Edge) []*taxonomy.Edge {
	out := make([]*taxonomy.Edge, 0, len(edges)+1)
	replaced := false
	for _, e := range edges {
		if e.TopicID == edge.TopicID && e.PrerequisiteID == edge.PrerequisiteID {
			out = append(out, edge)
			replaced = true
			continue
		}
		out = append(out, e)
	}
	if !replaced {
		out = append(out, edge)
	}
	return out
}

// ReviewPrerequisite serves the admin edge PATCH — flip one stored
// edge's review_status between reviewed and rejected. The row is never
// deleted: a rejected edge stays in the table for audit and simply
// drops out of the effective graph (both the DAO listing and WithEdges
// exclude it). Like the PUT, the whole sequence runs under
// prereqWriteMu and validates before it writes: the proposed effective
// set (the flip applied to the DB's current edges) must build into a
// candidate catalog first — both directions run the same gate — so
// restoring a rejected edge whose re-entry would now form a cycle is a
// 409 with the DB untouched (the old flip-first order committed the
// cycle to the DB and only then discovered it). A failed gate never
// writes and never swaps.
func (h *TaxonomyHandler) ReviewPrerequisite(w http.ResponseWriter, r *http.Request) {
	var req prerequisiteReviewRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	topicRef := strings.TrimSpace(req.TopicID)
	prereqRef := strings.TrimSpace(req.PrerequisiteID)
	if topicRef == "" || prereqRef == "" {
		writeError(w, http.StatusBadRequest, "topic_id and prerequisite_id are required")
		return
	}
	if req.ReviewStatus != prereqStatusReviewed && req.ReviewStatus != prereqStatusRejected {
		writeError(w, http.StatusBadRequest, `review_status must be "reviewed" or "rejected"`)
		return
	}
	if topicRef == prereqRef {
		writeError(w, http.StatusBadRequest, "a topic cannot be its own prerequisite")
		return
	}
	prereqWriteMu.Lock()
	defer prereqWriteMu.Unlock()
	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	edges, err := h.store.ListEffectivePrerequisiteEdges(r.Context())
	if err != nil {
		log.Printf("taxonomy: list prerequisite edges for review flip: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to load prerequisite edges")
		return
	}
	// Restoring a row that is not in the effective list (rejected so
	// far, or nothing stored yet) adds an edge the graph must be able
	// to represent: a ref that resolves nowhere is the plain 404, a ref
	// that persists in the DB but not in the pack is version drift —
	// 500, never a fake cycle 409.
	if req.ReviewStatus == prereqStatusReviewed && findPrerequisiteEdge(edges, topicRef, prereqRef) == nil {
		if cat.Topic(topicRef) == nil || cat.Topic(prereqRef) == nil {
			if !h.prereqRefsPersist(w, r, topicRef, prereqRef) {
				return
			}
			log.Printf("taxonomy: review prerequisite %s<-%s: persisted ref missing from the catalog", topicRef, prereqRef)
			writeError(w, http.StatusInternalServerError, prereqMsgPackDrift)
			return
		}
	}
	proposed := proposedReviewEdges(edges, topicRef, prereqRef, req.ReviewStatus)
	if drift := firstEdgeMissingFromCatalog(cat, proposed); drift != nil {
		log.Printf("taxonomy: review prerequisite %s<-%s: persisted edge %s<-%s has an endpoint missing from the catalog",
			topicRef, prereqRef, drift.TopicID, drift.PrerequisiteID)
		writeError(w, http.StatusInternalServerError, prereqMsgPackDrift)
		return
	}
	candidate, err := cat.WithEdges(proposed)
	if err != nil {
		// Both directions gate here before any write: a refused flip
		// (e.g. a restore whose re-entry would form a cycle) changes
		// nothing — not the DB, not the catalog.
		log.Printf("taxonomy: review prerequisite %s<-%s -> %s: %v", topicRef, prereqRef, req.ReviewStatus, err)
		writeError(w, http.StatusConflict, "review_status flip conflicts with the graph: "+err.Error())
		return
	}
	ok, err := h.store.SetPrerequisiteReviewStatus(r.Context(), topicRef, prereqRef, req.ReviewStatus)
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			writeError(w, http.StatusNotFound, "unknown topic_id or prerequisite_id")
		default:
			log.Printf("taxonomy: set prerequisite review status %s<-%s: %v", topicRef, prereqRef, err)
			writeError(w, http.StatusInternalServerError, "failed to update review status")
		}
		return
	}
	if !ok {
		// Both refs resolve but no edge is stored between them (the DAO
		// keeps unknown-row and unknown-ref distinct for exactly this
		// 404).
		writeError(w, http.StatusNotFound, "prerequisite edge not found")
		return
	}
	// Same lock guarantee as the PUT: the DB now holds exactly
	// `proposed`, so the validated candidate is swapped in as-is.
	h.swapCatalog(candidate)
	writeJSON(w, http.StatusOK, prerequisiteMutationResponse{
		Edge: prerequisiteEdgeResponse{
			TopicID:        topicRef,
			PrerequisiteID: prereqRef,
			ReviewStatus:   req.ReviewStatus,
		},
		EffectiveEdges: len(candidate.Edges),
	})
}

// proposedReviewEdges applies one review flip to an effective edge
// list without mutating it:
//
//   - rejected: the matching edge (if present) drops out of the
//     effective set — the stored row stays for audit, the graph loses
//     it. A row that is already rejected (or absent) changes nothing:
//     the set is returned as-is and the DAO write below sorts out the
//     404s.
//   - reviewed: the matching edge's status is rewritten in place,
//     payload kept (that is what the DAO write stores). A row that is
//     not in the list is a restore from rejected: the edge is appended
//     with review_status='reviewed'. Its stored strength/reason are
//     not readable through the effective listing (rejected rows are
//     excluded from it and no store read exposes them), so the
//     appended edge leaves them blank and WithEdges normalizes the
//     strength to hard — a restored soft edge is served as hard until
//     the next full catalog rebuild from the DB (restart or
//     re-import). The graph shape — the cycle decision this gate
//     exists for — is exact either way.
func proposedReviewEdges(edges []*taxonomy.Edge, topicRef, prereqRef, status string) []*taxonomy.Edge {
	if status == prereqStatusRejected {
		out := make([]*taxonomy.Edge, 0, len(edges))
		for _, e := range edges {
			if e.TopicID == topicRef && e.PrerequisiteID == prereqRef {
				continue
			}
			out = append(out, e)
		}
		return out
	}
	out := make([]*taxonomy.Edge, 0, len(edges)+1)
	restored := true
	for _, e := range edges {
		if e.TopicID == topicRef && e.PrerequisiteID == prereqRef {
			flipped := *e
			flipped.ReviewStatus = prereqStatusReviewed
			out = append(out, &flipped)
			restored = false
			continue
		}
		out = append(out, e)
	}
	if restored {
		out = append(out, &taxonomy.Edge{
			TopicID:        topicRef,
			PrerequisiteID: prereqRef,
			ReviewStatus:   prereqStatusReviewed,
		})
	}
	return out
}

// findPrerequisiteEdge locates one edge by its endpoints in an edge
// list (nil when absent).
func findPrerequisiteEdge(edges []*taxonomy.Edge, topicRef, prereqRef string) *taxonomy.Edge {
	for _, e := range edges {
		if e.TopicID == topicRef && e.PrerequisiteID == prereqRef {
			return e
		}
	}
	return nil
}

// firstEdgeMissingFromCatalog returns the first edge whose two
// endpoints don't both resolve in the catalog (nil when all do).
// Persisted rows failing this are version drift — the caller refuses
// with a 500 instead of letting WithEdges misreport them as a graph
// conflict.
func firstEdgeMissingFromCatalog(cat *taxonomy.Catalog, edges []*taxonomy.Edge) *taxonomy.Edge {
	for _, e := range edges {
		if cat.Topic(e.TopicID) == nil || cat.Topic(e.PrerequisiteID) == nil {
			return e
		}
	}
	return nil
}

// prereqRefsPersist resolves both refs against the DB to split the
// restore pre-check's failures: a ref that resolves nowhere gets the
// plain 404 (written here, false returns); a resolver error answers
// its own 500; both refs persisting returns true — the caller then
// answers the version-drift 500, since the pack cannot represent the
// edge the flip would restore.
func (h *TaxonomyHandler) prereqRefsPersist(w http.ResponseWriter, r *http.Request, topicRef, prereqRef string) bool {
	for _, ref := range []struct{ name, val string }{
		{"topic_id", topicRef},
		{"prerequisite_id", prereqRef},
	} {
		id, err := h.store.TagIDByExternalRef(r.Context(), ref.val)
		if err != nil {
			log.Printf("taxonomy: resolve %s %s: %v", ref.name, ref.val, err)
			writeError(w, http.StatusInternalServerError, "failed to resolve "+ref.name)
			return false
		}
		if id == 0 {
			writeError(w, http.StatusNotFound, "unknown topic_id or prerequisite_id")
			return false
		}
	}
	return true
}

// swapCatalog replaces the in-memory catalog pointer under a short
// loadMu hold. catalogOnce must already have returned (it takes loadMu
// itself — calling it under the lock self-deadlocks), and the
// replacement is always a fully validated WithEdges snapshot, so
// in-flight readers keep a consistent graph either side of the swap.
// Callers hold prereqWriteMu (the only lock order allowed is
// prereqWriteMu → loadMu).
func (h *TaxonomyHandler) swapCatalog(next *taxonomy.Catalog) {
	h.loadMu.Lock()
	h.catalog = next
	h.loadMu.Unlock()
}
