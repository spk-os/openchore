package api

// Knowledge-point neighborhood graph endpoint (TX-GRAPH-API-V2-CODER).
//
//	GET /api/taxonomy/graph?root&depth&subject&domain&grade
//
// The frozen contract (route wiring lives in router.go, outside this
// file's scope):
//
//   - root is required (trimmed non-empty) and must name a known topic
//     (current catalog) — 404 otherwise.
//   - depth is 1 or 2, defaulting to 1; anything else is a 400.
//   - subject/domain/grade are optional filters: every neighbor must
//     match them, and expansion never traverses through an
//     invisible (filtered-out) node — only the root itself is exempt
//     (a root that contradicts the filters is a 400).
//   - the neighborhood expands undirected over the effective edge set
//     (DB: ListEffectivePrerequisiteEdges — rejected edges and edges
//     with an empty external_ref endpoint never appear), while every
//     served edge keeps its stored topic_id → prerequisite_id
//     direction.
//   - mastery is the resolved target's real kc_mastery rollup — null
//     when no record exists (never a fabricated 0.5).
//   - the graph is capped at 250 nodes (root always included; BFS
//     layer order, external_ref-sorted within a layer) with
//     truncated:true when the cap bites; only edges between kept
//     nodes are served.
//
// Locking: catalogOnce takes loadMu itself, so it is never called
// while loadMu is held.

import (
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/liftedkilt/openchore/internal/taxonomy"
)

// graphMaxNodes caps the served neighborhood (deterministic truncation
// keeps the response bounded and testable).
const graphMaxNodes = 250

// graphNode is one knowledge point of the neighborhood. Mastery is a
// *float64 so an absent kc_mastery record serializes as null instead
// of a made-up neutral prior.
type graphNode struct {
	ID      string   `json:"id"` // external_ref
	Name    string   `json:"name"`
	Subject string   `json:"subject"`
	Grade   *int     `json:"grade"`
	Domain  string   `json:"domain"`
	Chapter string   `json:"chapter"`
	Mastery *float64 `json:"mastery"`
}

// graphEdge is one effective prerequisite edge between two kept nodes,
// direction preserved (topic_id depends on prerequisite_id).
type graphEdge struct {
	TopicID        string `json:"topic_id"`
	PrerequisiteID string `json:"prerequisite_id"`
	Strength       string `json:"strength"`
	Reason         string `json:"reason"`
	ReviewStatus   string `json:"review_status"`
}

// graphResponse is the frozen response document. Nodes and edges are
// never null (empty slice → []).
type graphResponse struct {
	Nodes     []graphNode `json:"nodes"`
	Edges     []graphEdge `json:"edges"`
	Truncated bool        `json:"truncated"`
}

// GetGraph serves GET /api/taxonomy/graph — the undirected
// depth-limited neighborhood of one knowledge point over the effective
// prerequisite edges, with the resolved target's real mastery per
// node.
func (h *TaxonomyHandler) GetGraph(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	root := strings.TrimSpace(q.Get("root"))
	if root == "" {
		writeError(w, http.StatusBadRequest, "root is required")
		return
	}
	depth := 1
	if v := q.Get("depth"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || (n != 1 && n != 2) {
			writeError(w, http.StatusBadRequest, "depth must be 1 or 2")
			return
		}
		depth = n
	}
	var gradeFilter *int
	if v := q.Get("grade"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid grade")
			return
		}
		gradeFilter = &n
	}
	subjectFilter := strings.TrimSpace(q.Get("subject"))
	domainFilter := strings.TrimSpace(q.Get("domain"))

	cat, err := h.catalogOnce(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "taxonomy pack failed to load")
		return
	}
	// The requested user wins over X-User-ID, which is never trusted:
	// kids resolve to themselves, admins must name a user.
	target, ok := h.resolveTarget(w, r, parseGraphUserID(q.Get("user_id")))
	if !ok {
		return
	}
	rootTopic := cat.Topic(root)
	if rootTopic == nil {
		writeError(w, http.StatusNotFound, "topic not found")
		return
	}
	// The root is exempt from the neighbor filters, but a root that
	// openly contradicts them is a client mistake, not a graph fact.
	if !graphMatchesFilters(cat, rootTopic, subjectFilter, domainFilter, gradeFilter) {
		writeError(w, http.StatusBadRequest, "root does not match the requested filters")
		return
	}

	edges, err := h.store.ListEffectivePrerequisiteEdges(r.Context())
	if err != nil {
		log.Printf("taxonomy: graph edges for %s: %v", root, err)
		writeError(w, http.StatusInternalServerError, "failed to load prerequisite edges")
		return
	}

	// Adjacency over the effective edges, both directions, so the
	// neighborhood expands undirected while each edge object keeps its
	// stored direction for serving.
	adjacency := make(map[string][]string, len(edges)*2)
	for _, e := range edges {
		if e.TopicID == "" || e.PrerequisiteID == "" || e.TopicID == e.PrerequisiteID {
			continue
		}
		adjacency[e.TopicID] = append(adjacency[e.TopicID], e.PrerequisiteID)
		adjacency[e.PrerequisiteID] = append(adjacency[e.PrerequisiteID], e.TopicID)
	}

	// BFS from the root, layer by layer. A candidate neighbor must be a
	// current catalog topic and match every requested filter — a
	// filtered-out node is invisible, and expansion never passes
	// through an invisible node (it is not enqueued even when the edge
	// exists). Within a layer, candidates are visited in external_ref
	// order so the expansion (and the truncation cut) is stable.
	kept := map[string]bool{root: true}
	layer := []string{root}
	truncated := false
	for d := 0; d < depth && len(layer) > 0; d++ {
		candidates := map[string]bool{}
		for _, cur := range layer {
			for _, next := range adjacency[cur] {
				if kept[next] || candidates[next] {
					continue
				}
				tp := cat.Topic(next)
				if tp == nil || !graphMatchesFilters(cat, tp, subjectFilter, domainFilter, gradeFilter) {
					continue
				}
				candidates[next] = true
			}
		}
		nexts := make([]string, 0, len(candidates))
		for ref := range candidates {
			nexts = append(nexts, ref)
		}
		sort.Strings(nexts)
		if len(kept)+len(nexts) > graphMaxNodes {
			room := graphMaxNodes - len(kept)
			if room < 0 {
				room = 0
			}
			nexts = nexts[:room]
			truncated = true
		}
		for _, ref := range nexts {
			kept[ref] = true
		}
		layer = nexts
	}

	nodes := make([]graphNode, 0, len(kept))
	for _, tp := range cat.Topics {
		if !kept[tp.ID] {
			continue
		}
		nodes = append(nodes, graphNode{
			ID:      tp.ID,
			Name:    tp.Name,
			Subject: cat.SubjectOf(tp),
			Grade:   graphGradeOf(tp),
			Domain:  tp.Domain,
			Chapter: tp.Chapter,
		})
	}
	if err := h.fillGraphMastery(r, target, nodes); err != nil {
		log.Printf("taxonomy: graph mastery for user %d: %v", target, err)
		writeError(w, http.StatusInternalServerError, "failed to load mastery")
		return
	}
	out := graphResponse{Nodes: nodes, Edges: []graphEdge{}, Truncated: truncated}
	for _, e := range edges {
		if !kept[e.TopicID] || !kept[e.PrerequisiteID] {
			continue
		}
		out.Edges = append(out.Edges, graphEdge{
			TopicID:        e.TopicID,
			PrerequisiteID: e.PrerequisiteID,
			Strength:       e.Strength,
			Reason:         e.Reason,
			ReviewStatus:   e.ReviewStatus,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// fillGraphMastery fills each node's real mastery for the resolved
// target. One MapTagIDsByExternalRef + one ListKCMastery round covers
// the whole neighborhood (the cap is 250 nodes — no per-node query
// storm), and a tag with no kc_mastery row keeps its null.
func (h *TaxonomyHandler) fillGraphMastery(r *http.Request, userID int64, nodes []graphNode) error {
	if len(nodes) == 0 {
		return nil
	}
	refs := make([]string, 0, len(nodes))
	for _, n := range nodes {
		refs = append(refs, n.ID)
	}
	tagIDs, err := h.store.MapTagIDsByExternalRef(r.Context(), refs)
	if err != nil {
		return err
	}
	mastery, err := h.store.ListKCMastery(r.Context(), userID)
	if err != nil {
		return err
	}
	byTag := make(map[int64]float64, len(mastery))
	for _, m := range mastery {
		byTag[m.TagID] = m.Mastery
	}
	for i := range nodes {
		id, ok := tagIDs[nodes[i].ID]
		if !ok {
			continue
		}
		if m, ok := byTag[id]; ok {
			v := m
			nodes[i].Mastery = &v
		}
	}
	return nil
}

// graphGradeOf projects the catalog topic's grade onto the JSON node
// (0 = unset → null, matching the browse view's semantics).
func graphGradeOf(tp *taxonomy.Topic) *int {
	if tp.Grade <= 0 {
		return nil
	}
	g := tp.Grade
	return &g
}

// graphMatchesFilters reports whether one topic is visible under the
// requested subject/domain/grade filters (empty filter = no
// constraint).
func graphMatchesFilters(cat *taxonomy.Catalog, tp *taxonomy.Topic, subject, domain string, grade *int) bool {
	if subject != "" && cat.SubjectOf(tp) != subject {
		return false
	}
	if domain != "" && tp.Domain != domain {
		return false
	}
	if grade != nil && tp.Grade != *grade {
		return false
	}
	return true
}

// parseGraphUserID reads the optional user_id query parameter for the
// target resolution (nil = omitted; admins must name one). An
// unparseable value stays non-nil and negative — resolveTarget answers
// it with the same 400 as any unknown user id.
func parseGraphUserID(v string) *int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		neg := int64(-1)
		return &neg
	}
	return &id
}
