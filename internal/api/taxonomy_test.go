package api_test

// TX-BE-CODER: taxonomy REST contract coverage. Real SQLite + the
// embedded pack (13MB, loaded once per process) + httptest — no mocks.
// The frozen contract lives in the route comments; the data anchors
// are real pack ids (cnm_p001/cnm_p002 …) so a data change that breaks
// a test is a data regression worth seeing.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/liftedkilt/openchore/internal/api"
	"github.com/liftedkilt/openchore/internal/store"
	"github.com/liftedkilt/openchore/internal/taxonomy"
)

// importPack runs the idempotent import once for the test env (the
// production startup hook; tests need it because a fresh :memory: DB
// starts empty).
func importPack(t *testing.T, e *testEnv) *store.TaxonomyCounts {
	t.Helper()
	h := api.NewTaxonomyHandler(e.store, nil)
	counts, err := h.ImportPack(t.Context())
	if err != nil {
		t.Fatalf("pack import failed: %v", err)
	}
	return counts
}

// TestTaxonomyImportReconciliation locks the T2 baseline: re-running
// the import is a no-op (idempotent upsert by external_ref) and the
// counts match the frozen pack numbers.
func TestTaxonomyImportReconciliation(t *testing.T) {
	e := setupTest(t)
	counts := importPack(t, e)
	if counts.Topics != 3108 {
		t.Errorf("topics = %d, want 3108", counts.Topics)
	}
	if counts.Prerequisites != 4625 || counts.HardEdges != 2478 || counts.SoftEdges != 2147 {
		t.Errorf("edges = %d (hard %d soft %d), want 4625 (2478/2147)",
			counts.Prerequisites, counts.HardEdges, counts.SoftEdges)
	}
	if counts.Questions != 12579 {
		t.Errorf("questions = %d, want 12579", counts.Questions)
	}
	if counts.Standards != 774 {
		t.Errorf("standards = %d, want 774", counts.Standards)
	}
	if counts.TagStandards != 3372 {
		t.Errorf("tag_standards = %d, want 3372", counts.TagStandards)
	}

	// Re-import: same numbers (idempotency is the restart contract).
	again := importPack(t, e)
	if again.Topics != counts.Topics || again.Questions != counts.Questions {
		t.Fatalf("re-import changed counts: %+v vs %+v", again, counts)
	}
}

// TestTaxonomySubjectsAndBrowse covers GET /subjects (the ten-word
// list with counts) and GET /topics (filter + pagination contract).
func TestTaxonomySubjectsAndBrowse(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小小")
	importPack(t, e)

	// Subjects: auth required.
	e.expectStatus(t, "GET", "/api/taxonomy/subjects", nil, nil, http.StatusUnauthorized)

	resp := e.expectStatus(t, "GET", "/api/taxonomy/subjects", nil, childHeaders(kid), http.StatusOK)
	var subs struct {
		Subjects []store.SubjectStat `json:"subjects"`
	}
	decodeBody(t, resp, &subs)
	if len(subs.Subjects) != 10 {
		t.Fatalf("subjects = %d, want 10", len(subs.Subjects))
	}
	var mathStat *store.SubjectStat
	for i := range subs.Subjects {
		if subs.Subjects[i].Key == taxonomy.SubjectMath {
			mathStat = &subs.Subjects[i]
		}
	}
	if mathStat == nil {
		t.Fatalf("math subject missing from the word list")
	}
	if mathStat.TopicCount == 0 || mathStat.QuestionCount == 0 {
		t.Errorf("math counts empty: %+v", mathStat)
	}

	// Browse: subject filter + keyword + pagination.
	resp = e.expectStatus(t, "GET", "/api/taxonomy/topics?subject=math&q=1到5&limit=10", nil, childHeaders(kid), http.StatusOK)
	var browse struct {
		Topics     []map[string]any `json:"topics"`
		NextCursor string           `json:"next_cursor"`
	}
	decodeBody(t, resp, &browse)
	if len(browse.Topics) == 0 {
		t.Fatalf("keyword browse returned nothing")
	}
	if browse.Topics[0]["external_ref"] != "cnm_p001" {
		t.Errorf("first hit = %v, want cnm_p001", browse.Topics[0]["external_ref"])
	}
	if browse.Topics[0]["answer"] != nil {
		t.Errorf("browse row leaked an answer field")
	}

	// Grade filter rejects junk.
	e.expectStatus(t, "GET", "/api/taxonomy/topics?grade=99", nil, childHeaders(kid), http.StatusBadRequest)
	e.expectStatus(t, "GET", "/api/taxonomy/topics?grade=abc", nil, childHeaders(kid), http.StatusBadRequest)
}

// TestTaxonomyTopicDetailAnswerLeak covers GET /topics/{ref}: the
// admin sees answers, the kid never does (leak guard), 404 for junk.
func TestTaxonomyTopicDetailAnswerLeak(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小明明细")
	importPack(t, e)

	// Kid view: questions carry no answer/accept/explanation.
	resp := e.expectStatus(t, "GET", "/api/taxonomy/topics/cnm_p002", nil, childHeaders(kid), http.StatusOK)
	var detail struct {
		Topic         map[string]any   `json:"topic"`
		Questions     []map[string]any `json:"questions"`
		Prerequisites []map[string]any `json:"prerequisites"`
		Dependents    []map[string]any `json:"dependents"`
		Standards     []map[string]any `json:"standards"`
	}
	decodeBody(t, resp, &detail)
	if detail.Topic["external_ref"] != "cnm_p002" {
		t.Fatalf("topic ref = %v", detail.Topic["external_ref"])
	}
	if len(detail.Questions) == 0 {
		t.Fatalf("no questions on cnm_p002")
	}
	for _, q := range detail.Questions {
		for _, leak := range []string{"answer", "accept", "explanation"} {
			if q[leak] != nil {
				t.Fatalf("kid question leaked %q: %v", leak, q[leak])
			}
		}
	}
	if len(detail.Prerequisites) == 0 {
		t.Errorf("cnm_p002 prerequisites empty (want cnm_p001)")
	}

	// Admin view: the bank stays answerless for admins too. The admin's
	// pre-submit view used to carry answer/accept/explanation, a second
	// answer face the kid-side guard never covered; the frozen security
	// contract is one answerless projection for every role (answers only
	// arrive as post-submit reference_answer on the practice/diagnostic
	// feedback paths). Never re-grant admin a privileged answer.
	resp = e.expectStatus(t, "GET", "/api/taxonomy/topics/cnm_p002", nil, adminHeaders(), http.StatusOK)
	var adminDetail struct {
		Questions []map[string]any `json:"questions"`
	}
	decodeBody(t, resp, &adminDetail)
	if len(adminDetail.Questions) == 0 {
		t.Fatalf("admin view lost the question bank: %+v", adminDetail.Questions)
	}
	for _, q := range adminDetail.Questions {
		for _, leak := range []string{"answer", "accept", "explanation"} {
			if q[leak] != nil {
				t.Fatalf("admin question leaked %q pre-submit: %v", leak, q[leak])
			}
		}
	}

	e.expectStatus(t, "GET", "/api/taxonomy/topics/nope_nope", nil, childHeaders(kid), http.StatusNotFound)
}

// TestTaxonomyTopicNeighborDirection locks the neighbor-side fix: a
// prereq list resolves the PrerequisiteID end of its edges, a dependent
// list the TopicID end — a dependent list echoing the topic itself is
// the old wrong-side bug.
func TestTaxonomyTopicNeighborDirection(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小向")
	importPack(t, e)

	// cnm_p001 is a root: no prerequisites, and its direct dependents
	// are the seven topics that list it as a prerequisite.
	resp := e.expectStatus(t, "GET", "/api/taxonomy/topics/cnm_p001", nil, childHeaders(kid), http.StatusOK)
	var root struct {
		Prerequisites []map[string]any `json:"prerequisites"`
		Dependents    []map[string]any `json:"dependents"`
	}
	decodeBody(t, resp, &root)
	if len(root.Prerequisites) != 0 {
		t.Errorf("cnm_p001 prerequisites = %v, want none (root)", root.Prerequisites)
	}
	if len(root.Dependents) == 0 {
		t.Fatalf("cnm_p001 dependents empty")
	}
	seen := map[string]bool{}
	for _, d := range root.Dependents {
		ref, _ := d["external_ref"].(string)
		if ref == "cnm_p001" {
			t.Errorf("dependent list echoes the topic itself (wrong edge side): %v", d)
		}
		seen[ref] = true
	}
	if !seen["cnm_p002"] {
		t.Errorf("cnm_p001 dependents missing cnm_p002: %v", seen)
	}

	// The reverse view agrees: cnm_p002's prerequisite is cnm_p001 and
	// its only dependent is cnm_p009.
	resp = e.expectStatus(t, "GET", "/api/taxonomy/topics/cnm_p002", nil, childHeaders(kid), http.StatusOK)
	var next struct {
		Prerequisites []map[string]any `json:"prerequisites"`
		Dependents    []map[string]any `json:"dependents"`
	}
	decodeBody(t, resp, &next)
	if len(next.Prerequisites) != 1 || next.Prerequisites[0]["external_ref"] != "cnm_p001" {
		t.Errorf("cnm_p002 prerequisites = %v, want [cnm_p001]", next.Prerequisites)
	}
	if len(next.Dependents) != 1 || next.Dependents[0]["external_ref"] != "cnm_p009" {
		t.Errorf("cnm_p002 dependents = %v, want [cnm_p009]", next.Dependents)
	}
}

// practiceAnswered runs one practice request and returns the decoded
// response (shared by the practice tests).
func practiceAnswered(t *testing.T, e *testEnv, headers map[string]string, body map[string]any) map[string]any {
	t.Helper()
	resp := e.expectStatus(t, "POST", "/api/taxonomy/practice", body, headers, http.StatusOK)
	var out map[string]any
	decodeBody(t, resp, &out)
	return out
}

// TestTaxonomyPracticeJudgesAndProfiles covers POST /practice: the
// frozen response shape (correct/explanation/mastery/review_item_id),
// the learner-event + kc_mastery write, and the weak-KC review card.
func TestTaxonomyPracticeJudgesAndProfiles(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小明")
	importPack(t, e)

	// Kid answers correctly without naming a user (self).
	out := practiceAnswered(t, e, childHeaders(kid), map[string]any{
		"topic_id": "cnm_p001", "question_id": "cnm_p001_q1", "answer": "4",
	})
	if out["correct"] != true {
		t.Fatalf("correct = %v, want true (%v)", out["correct"], out)
	}
	if out["verdict"] != "correct" {
		t.Fatalf("verdict = %v", out["verdict"])
	}
	q := out["question"].(map[string]any)
	if q["answer"] != nil {
		t.Errorf("practice response leaked the answer to the kid")
	}

	// A wrong answer on the same KC drops mastery below 0.6 → card.
	out = practiceAnswered(t, e, childHeaders(kid), map[string]any{
		"topic_id": "cnm_p001", "question_id": "cnm_p001_q2", "answer": "9",
	})
	if out["correct"] != false {
		t.Fatalf("wrong answer judged correct: %v", out)
	}
	if out["review_item_id"] == nil {
		t.Fatalf("weak KC created no review card: %v", out)
	}

	// The learner event + mastery rollup landed.
	var events int
	e.db.QueryRow(`SELECT COUNT(*) FROM learner_events WHERE user_id = ? AND verb = 'answered'`, kid).Scan(&events)
	if events != 2 {
		t.Errorf("learner events = %d, want 2", events)
	}
	var mastery float64
	var total int
	e.db.QueryRow(`SELECT mastery, total_count FROM kc_mastery WHERE user_id = ?`, kid).Scan(&mastery, &total)
	if total != 2 {
		t.Errorf("kc_mastery total = %d, want 2", total)
	}
	// Laplace (1+1)/(2+2) = 0.5 for one right one wrong.
	if m, _ := out["mastery"].(float64); m < 0.4 || m > 0.6 {
		t.Errorf("mastery = %v, want ~0.5", out["mastery"])
	}

	// The review card exists with source kc.
	var cards int
	e.db.QueryRow(`SELECT COUNT(*) FROM review_items WHERE user_id = ? AND source_type = 'kc'`, kid).Scan(&cards)
	if cards != 1 {
		t.Errorf("kc review cards = %d, want 1", cards)
	}

	// A manual (self-assessed) round: never a rejection — verdict
	// manual, the reference answer shipped for self-check (the
	// embedded question view still carries no answer), and the
	// attempt recorded as a skip event (success=false, skipped=true).
	out = practiceAnswered(t, e, childHeaders(kid), map[string]any{
		"topic_id": "cnm_003", "question_id": "cnm_003_q1", "answer": "正数：3.5，7",
	})
	if out["verdict"] != "manual" {
		t.Fatalf("self-question verdict = %v, want manual (%v)", out["verdict"], out)
	}
	if ref, _ := out["reference_answer"].(string); ref == "" {
		t.Errorf("manual round missing reference_answer: %v", out)
	}
	if q := out["question"].(map[string]any); q["answer"] != nil {
		t.Errorf("manual round leaked the answer inside the question view")
	}
	var skipResult string
	e.db.QueryRow(`SELECT result_json FROM learner_events
		WHERE user_id = ? AND context_json LIKE '%cnm_003_q1%'`, kid).Scan(&skipResult)
	if !strings.Contains(skipResult, `"success":false`) || !strings.Contains(skipResult, `"skipped":true`) {
		t.Errorf("skip event result = %s, want success=false + skipped=true", skipResult)
	}
}

// TestTaxonomyPracticeBlankManualNoLeak covers the blank-manual leak
// guard: a whitespace-only answer on a self question gets the manual
// verdict recorded (skip event) but never the reference answer or the
// explanation — nothing was attempted, nothing is revealed.
func TestTaxonomyPracticeBlankManualNoLeak(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小空")
	importPack(t, e)

	out := practiceAnswered(t, e, childHeaders(kid), map[string]any{
		"topic_id": "cnm_003", "question_id": "cnm_003_q1", "answer": "   ",
	})
	if out["verdict"] != "manual" {
		t.Fatalf("blank self verdict = %v, want manual (%v)", out["verdict"], out)
	}
	if ref, _ := out["reference_answer"].(string); ref != "" {
		t.Errorf("blank manual leaked reference_answer: %q", ref)
	}
	if expl, _ := out["explanation"].(string); expl != "" {
		t.Errorf("blank manual leaked explanation: %q", expl)
	}
	q := out["question"].(map[string]any)
	for _, leak := range []string{"answer", "accept", "explanation"} {
		if q[leak] != nil {
			t.Fatalf("blank manual question view leaked %q: %v", leak, q[leak])
		}
	}

	// The blank round is still a recorded skip event.
	var skipResult string
	e.db.QueryRow(`SELECT result_json FROM learner_events
		WHERE user_id = ? AND context_json LIKE '%cnm_003_q1%'`, kid).Scan(&skipResult)
	if !strings.Contains(skipResult, `"skipped":true`) {
		t.Errorf("blank round not recorded as a skip event: %s", skipResult)
	}

	// A real (non-blank) manual round on the same question still ships
	// the reference answer for the self-check.
	out = practiceAnswered(t, e, childHeaders(kid), map[string]any{
		"topic_id": "cnm_003", "question_id": "cnm_003_q1", "answer": "正数：3.5，7",
	})
	if out["verdict"] != "manual" {
		t.Fatalf("self verdict = %v, want manual", out["verdict"])
	}
	if ref, _ := out["reference_answer"].(string); ref == "" {
		t.Errorf("non-blank manual round missing reference_answer: %v", out)
	}
	if expl, _ := out["explanation"].(string); expl == "" {
		t.Errorf("non-blank manual round missing explanation: %v", out)
	}
}

// TestTaxonomyPracticeAuth covers the access rules: admin must name a
// user, a kid cannot act as someone else, unknown ids are 400.
func TestTaxonomyPracticeAuth(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小红")
	other := e.createChild(t, "小刚")
	importPack(t, e)

	body := map[string]any{"topic_id": "cnm_p001", "question_id": "cnm_p001_q1", "answer": "4"}

	// Unauthenticated.
	e.expectStatus(t, "POST", "/api/taxonomy/practice", body, nil, http.StatusUnauthorized)
	// Admin without user_id.
	e.expectStatus(t, "POST", "/api/taxonomy/practice", map[string]any{
		"topic_id": "cnm_p001", "question_id": "cnm_p001_q1", "answer": "4",
	}, adminHeaders(), http.StatusBadRequest)
	// Kid naming another kid (access probe).
	e.expectStatus(t, "POST", "/api/taxonomy/practice", map[string]any{
		"user_id": other, "topic_id": "cnm_p001", "question_id": "cnm_p001_q1", "answer": "4",
	}, childHeaders(kid), http.StatusForbidden)
	// Kid naming self is fine.
	practiceAnswered(t, e, childHeaders(kid), map[string]any{
		"user_id": kid, "topic_id": "cnm_p001", "question_id": "cnm_p001_q1", "answer": "4",
	})
	// Admin naming a real kid is fine.
	practiceAnswered(t, e, adminHeaders(), map[string]any{
		"user_id": kid, "topic_id": "cnm_p001", "question_id": "cnm_p001_q1", "answer": "4",
	})
	// Unknown ids.
	e.expectStatus(t, "POST", "/api/taxonomy/practice", map[string]any{
		"topic_id": "nope", "question_id": "cnm_p001_q1", "answer": "4",
	}, childHeaders(kid), http.StatusBadRequest)
	e.expectStatus(t, "POST", "/api/taxonomy/practice", map[string]any{
		"topic_id": "cnm_p001", "question_id": "cnm_p999_q1", "answer": "4",
	}, childHeaders(kid), http.StatusBadRequest)
}

// createDiagnosticSession starts a target-mode session and returns the
// detail payload (the shared setup of the diagnostic tests).
func createDiagnosticSession(t *testing.T, e *testEnv, headers map[string]string, body map[string]any) map[string]any {
	t.Helper()
	resp := e.expectStatus(t, "POST", "/api/taxonomy/diagnostics", body, headers, http.StatusCreated)
	var out map[string]any
	decodeBody(t, resp, &out)
	return out
}

// answerNext answers the session's current question via the contract
// endpoint; returns the decoded detail.
func answerNext(t *testing.T, e *testEnv, headers map[string]string, sessionID int64, answer string) map[string]any {
	t.Helper()
	resp := e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/answers", sessionID),
		map[string]any{"answer": answer}, headers, http.StatusOK)
	var out map[string]any
	decodeBody(t, resp, &out)
	return out
}

// nextQuestionOf pulls the pending question block out of a detail
// payload (nil when the session is done).
func nextQuestionOf(detail map[string]any) map[string]any {
	q, _ := detail["question"].(map[string]any)
	return q
}

// TestTaxonomyDiagnosticLifecycle walks the whole flow: create → answer
// → finish → summary → attempts, with the weak-KC card hook at the end.
func TestTaxonomyDiagnosticLifecycle(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小华")
	importPack(t, e)

	// Screen mode over two adjacent KCs (cnm_p001, cnm_p002): scope is
	// the target set itself, so a couple of answers finish it.
	detail := createDiagnosticSession(t, e, childHeaders(kid), map[string]any{
		"mode":    "screen",
		"subject": "math",
		"targets": []string{"cnm_p001", "cnm_p002"},
		"limit":   4,
		"confirm": 2,
		"seed":    "test-seed",
	})
	session := detail["session"].(map[string]any)
	sessionID := int64(session["id"].(float64))
	if session["status"] != "active" {
		t.Fatalf("session status = %v, want active", session["status"])
	}

	// Answer everything right until it finishes (bounded loop).
	answers := 0
	for {
		q := nextQuestionOf(detail)
		if q == nil {
			break
		}
		question := q["question"].(map[string]any)
		// The engine asks numeric questions here; the right answer for
		// cnm_p001/cnm_p002 basics is derived from the prompt's number
		// pattern — instead of parsing, answer with each topic's known
		// correct values.
		topicID, _ := q["topic_id"].(string)
		ans := correctAnswerFor(topicID, question["id"].(string))
		detail = answerNext(t, e, childHeaders(kid), sessionID, ans)
		answers++
		if answers > 10 {
			t.Fatalf("session never finished after %d answers", answers)
		}
	}

	// All-mastered: finished, no weak cards.
	summary, _ := detail["summary"].(map[string]any)
	if summary == nil || summary["finished"] != true {
		t.Fatalf("summary not finished: %v", summary)
	}
	var cards int
	e.db.QueryRow(`SELECT COUNT(*) FROM review_items WHERE user_id = ? AND source_type = 'kc'`, kid).Scan(&cards)
	if cards != 0 {
		t.Errorf("mastered session created %d review cards, want 0", cards)
	}

	// The attempts persisted (one per applied answer).
	var attempts int
	e.db.QueryRow(`SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ?`, sessionID).Scan(&attempts)
	if attempts != answers {
		t.Errorf("attempts = %d, want %d", attempts, answers)
	}

	// The list view shows it.
	resp := e.expectStatus(t, "GET", "/api/taxonomy/diagnostics", nil, childHeaders(kid), http.StatusOK)
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	decodeBody(t, resp, &list)
	if len(list.Sessions) != 1 || int64(list.Sessions[0]["id"].(float64)) != sessionID {
		t.Fatalf("list view = %+v", list.Sessions)
	}
}

// correctAnswerFor maps the pack's known basic answers (the numeric
// facts the seed questions test). Only the ids the lifecycle test hits.
func correctAnswerFor(topicID, questionID string) string {
	switch questionID {
	case "cnm_p001_q1", "cnm_p001_q2":
		return map[string]string{"cnm_p001_q1": "4", "cnm_p001_q2": "3"}[questionID]
	case "cnm_p002_q1", "cnm_p002_q3":
		return "A"
	case "cnm_p002_q2":
		return "2"
	case "cnm_p002_q4":
		return "3"
	}
	// Unknown id (a later question in the same topic): the pack's
	// topics all answer 4-or-3 style; wrong is fine — the test only
	// needs the flow to advance.
	_ = topicID
	return "1"
}

// TestTaxonomyDiagnosticUndoCorrect covers undo (void overlay, engine
// replay) and the admin correction path.
func TestTaxonomyDiagnosticUndoCorrect(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小军")
	importPack(t, e)

	detail := createDiagnosticSession(t, e, childHeaders(kid), map[string]any{
		"mode":    "screen",
		"subject": "math",
		"targets": []string{"cnm_p001", "cnm_p002"},
		"limit":   4,
		"confirm": 2,
		"seed":    "undo-seed",
	})
	sessionID := int64(detail["session"].(map[string]any)["id"].(float64))

	// Answer once (wrong on purpose).
	q := nextQuestionOf(detail)
	if q == nil {
		t.Fatalf("no first question")
	}
	detail = answerNext(t, e, childHeaders(kid), sessionID, "999")

	// One attempt row, answered.
	var status string
	var cnt int
	e.db.QueryRow(`SELECT status, COUNT(*) FROM diagnostic_attempts WHERE session_id = ? GROUP BY status`, sessionID).Scan(&status, &cnt)
	if cnt != 1 || status != "answered" {
		t.Fatalf("attempt row = %s x%d, want answered x1", status, cnt)
	}

	// Undo: the row is voided (never deleted), the engine state loses
	// the answer, and the next question is back.
	resp := e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/undo", sessionID),
		map[string]any{"count": 1}, childHeaders(kid), http.StatusOK)
	var undone map[string]any
	decodeBody(t, resp, &undone)
	e.db.QueryRow(`SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ? AND status = 'voided'`, sessionID).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("voided attempts = %d, want 1", cnt)
	}
	e.db.QueryRow(`SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ?`, sessionID).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("undo deleted the attempt row (append-only violated): %d", cnt)
	}

	// Answer again (right), then the admin corrects nothing yet — but
	// correct requires a real attempt row and admin rights.
	detail = answerNext(t, e, childHeaders(kid), sessionID, correctAnswerFor("", ""))
	var attempts []map[string]any
	attemptsRaw, _ := json.Marshal(detail["attempts"])
	json.Unmarshal(attemptsRaw, &attempts)
	var liveAttemptID int64
	for _, a := range attempts {
		if a["status"] == "answered" {
			liveAttemptID = int64(a["id"].(float64))
		}
	}
	if liveAttemptID == 0 {
		t.Fatalf("no answered attempt to correct")
	}

	// Kid cannot correct (the admin gate runs before body validation).
	e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/correct", sessionID),
		map[string]any{"attempt_id": liveAttemptID, "verdict": "correct"}, childHeaders(kid), http.StatusForbidden)

	// Admin correction: verdict override on the kid's own answer text
	// (never a re-judged parent answer) + overlay + state replay.
	resp = e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/correct", sessionID),
		map[string]any{"attempt_id": liveAttemptID, "verdict": "correct"}, adminHeaders(), http.StatusOK)
	var corrected map[string]any
	decodeBody(t, resp, &corrected)
	e.db.QueryRow(`SELECT COUNT(*) FROM diagnostic_attempts WHERE session_id = ? AND status = 'corrected'`, sessionID).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("corrected attempts = %d, want 1", cnt)
	}
}

// TestTaxonomyDiagnosticCrossUser covers the ownership rules: a kid
// cannot read, answer or list another kid's sessions (404 — existence
// is not confirmed), and admins can.
func TestTaxonomyDiagnosticCrossUser(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小丽")
	other := e.createChild(t, "小强")
	importPack(t, e)

	detail := createDiagnosticSession(t, e, childHeaders(kid), map[string]any{
		"mode":    "screen",
		"subject": "math",
		"targets": []string{"cnm_p001"},
		"limit":   2,
	})
	sessionID := int64(detail["session"].(map[string]any)["id"].(float64))

	// Another kid: 404 everywhere (no existence oracle).
	e.expectStatus(t, "GET", fmt.Sprintf("/api/taxonomy/diagnostics/%d", sessionID), nil, childHeaders(other), http.StatusNotFound)
	e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/answers", sessionID),
		map[string]any{"answer": "4"}, childHeaders(other), http.StatusNotFound)
	e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/undo", sessionID),
		map[string]any{}, childHeaders(other), http.StatusNotFound)
	// Listing with someone else's user_id: 403.
	e.expectStatus(t, "GET", fmt.Sprintf("/api/taxonomy/diagnostics?user_id=%d", kid), nil, childHeaders(other), http.StatusForbidden)
	// Admin reads it fine.
	e.expectStatus(t, "GET", fmt.Sprintf("/api/taxonomy/diagnostics/%d", sessionID), nil, adminHeaders(), http.StatusOK)
	// Admin list requires user_id.
	e.expectStatus(t, "GET", "/api/taxonomy/diagnostics", nil, adminHeaders(), http.StatusBadRequest)
}

// TestTaxonomyShareLifecycle covers the share contract: mint (raw token
// returned once), public read with no auth, revoke → 404, unknown → 404.
func TestTaxonomyShareLifecycle(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小霞")
	importPack(t, e)

	detail := createDiagnosticSession(t, e, childHeaders(kid), map[string]any{
		"mode":    "screen",
		"subject": "math",
		"targets": []string{"cnm_p001"},
		"limit":   2,
	})
	sessionID := int64(detail["session"].(map[string]any)["id"].(float64))

	// The kid shares their own session.
	resp := e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/share", sessionID),
		map[string]any{"expires_in_hours": 1}, childHeaders(kid), http.StatusCreated)
	var share struct {
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at"`
	}
	decodeBody(t, resp, &share)
	if share.URL == "" {
		t.Fatalf("share URL empty")
	}
	// Token is long (256-bit hex) and only the hash is stored.
	var tokenHash string
	e.db.QueryRow(`SELECT token_hash FROM diagnostic_shares WHERE session_id = ?`, sessionID).Scan(&tokenHash)
	if tokenHash == "" {
		t.Fatalf("share row missing token hash")
	}
	// The share URL points at the public page route, so the raw
	// token is extracted from it for the API read.
	token := strings.TrimPrefix(share.URL, "/share/taxonomy/")
	if token == share.URL || token == "" {
		t.Fatalf("share URL = %q, want /share/taxonomy/{token}", share.URL)
	}

	// Public read works with no auth at all.
	resp = e.expectStatus(t, "GET", "/api/taxonomy/shared/"+token, nil, nil, http.StatusOK)
	var report map[string]any
	decodeBody(t, resp, &report)
	if report["report_kind"] != "diagnosis" {
		t.Fatalf("report kind = %v", report["report_kind"])
	}
	snapshot, _ := report["snapshot"].(map[string]any)
	if snapshot == nil || snapshot["session_id"] == nil {
		t.Fatalf("snapshot missing session data: %v", report)
	}
	// The snapshot never carries the raw answers.
	if _, hasAttempts := snapshot["attempts"]; hasAttempts {
		t.Errorf("share snapshot leaked attempts")
	}

	// Unknown token: 404.
	e.expectStatus(t, "GET", "/api/taxonomy/shared/"+strings.Repeat("0", 64), nil, nil, http.StatusNotFound)

	// Revoke (admin) → public read 404.
	var shareID int64
	e.db.QueryRow(`SELECT id FROM diagnostic_shares WHERE session_id = ?`, sessionID).Scan(&shareID)
	e.expectStatus(t, "DELETE", fmt.Sprintf("/api/admin/taxonomy/shares/%d", shareID), nil, adminHeaders(), http.StatusOK)
	e.expectStatus(t, "GET", "/api/taxonomy/shared/"+token, nil, nil, http.StatusNotFound)
}

// TestTaxonomyShare is the TX-BE-SHARE-FINISH-DIRECT contract lock: a
// real kid creates a diagnostic, shares it, and the create answer
// carries the four fields (share_id / url / token / expires_at) with
// the URL on the public page route. The public GET resolves the
// snapshot with no auth, revocation kills it, and the snapshot never
// carries attempts or answers.
func TestTaxonomyShare(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小娜")
	importPack(t, e)

	// The kid answers one question first so the snapshot carries a
	// real summary with data in it.
	detail := createDiagnosticSession(t, e, childHeaders(kid), map[string]any{
		"mode":    "screen",
		"subject": "math",
		"targets": []string{"cnm_p001"},
		"limit":   2,
		"confirm": 2,
		"seed":    "share-seed",
	})
	sessionID := int64(detail["session"].(map[string]any)["id"].(float64))
	detail = answerNext(t, e, childHeaders(kid), sessionID, "999")

	// Share: the four-field answer.
	resp := e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/share", sessionID),
		map[string]any{"expires_in_hours": 2}, childHeaders(kid), http.StatusCreated)
	var share struct {
		ShareID   int64  `json:"share_id"`
		URL       string `json:"url"`
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	decodeBody(t, resp, &share)
	if share.ShareID <= 0 {
		t.Fatalf("share_id = %d, want > 0", share.ShareID)
	}
	if share.Token == "" || len(share.Token) < 32 {
		t.Fatalf("token = %q, want a long raw token", share.Token)
	}
	if share.URL != "/share/taxonomy/"+share.Token {
		t.Fatalf("url = %q, want /share/taxonomy/%s", share.URL, share.Token)
	}
	if share.ExpiresAt == "" {
		t.Fatalf("expires_at missing")
	}
	// share_id names the stored row (the revoke path's id).
	var storedID int64
	var tokenHash string
	e.db.QueryRow(`SELECT id, token_hash FROM diagnostic_shares WHERE session_id = ?`, sessionID).
		Scan(&storedID, &tokenHash)
	if storedID != share.ShareID {
		t.Fatalf("share_id = %d, want the stored row id %d", share.ShareID, storedID)
	}
	if tokenHash == share.Token {
		t.Fatalf("the raw token was stored, not its hash")
	}
	if tokenHash == "" {
		t.Fatalf("share row missing token hash")
	}

	// Public read with no auth resolves the snapshot through the API
	// endpoint (the page route only points at it).
	resp = e.expectStatus(t, "GET", "/api/taxonomy/shared/"+share.Token, nil, nil, http.StatusOK)
	var report struct {
		ReportKind string          `json:"report_kind"`
		ExpiresAt  string          `json:"expires_at"`
		Snapshot   json.RawMessage `json:"snapshot"`
	}
	decodeBody(t, resp, &report)
	if report.ReportKind != "diagnosis" {
		t.Fatalf("report_kind = %q, want diagnosis", report.ReportKind)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(report.Snapshot, &snapshot); err != nil {
		t.Fatalf("snapshot is not an object: %v", err)
	}
	if int64(snapshot["session_id"].(float64)) != sessionID {
		t.Fatalf("snapshot session_id = %v, want %d", snapshot["session_id"], sessionID)
	}
	// The snapshot is summary-only: no attempts, no answers.
	if _, has := snapshot["attempts"]; has {
		t.Errorf("share snapshot leaked attempts")
	}
	if _, has := snapshot["answer"]; has {
		t.Errorf("share snapshot leaked an answer")
	}
	// The summary inside the snapshot is the diagnosis itself.
	if snapshot["summary"] == nil {
		t.Errorf("share snapshot missing the summary: %v", snapshot)
	}

	// Admin revoke → the public read is gone (404, never a reason).
	e.expectStatus(t, "DELETE", fmt.Sprintf("/api/admin/taxonomy/shares/%d", share.ShareID), nil, adminHeaders(), http.StatusOK)
	e.expectStatus(t, "GET", "/api/taxonomy/shared/"+share.Token, nil, nil, http.StatusNotFound)
}

// TestTaxonomyDiagnosticFinish is the TX-BE-SHARE-FINISH-DIRECT finish
// lock: finishing an unfinished session answers with
// summary.finished=true and no next question, and a fresh GET says the
// same thing.
func TestTaxonomyDiagnosticFinish(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小馨")
	importPack(t, e)

	// An unfinished session: one answer in, more to go.
	detail := createDiagnosticSession(t, e, childHeaders(kid), map[string]any{
		"mode":    "screen",
		"subject": "math",
		"targets": []string{"cnm_p001", "cnm_p002"},
		"limit":   4,
		"confirm": 2,
		"seed":    "finish-seed",
	})
	sessionID := int64(detail["session"].(map[string]any)["id"].(float64))
	detail = answerNext(t, e, childHeaders(kid), sessionID, "999")
	if q := nextQuestionOf(detail); q == nil {
		t.Fatalf("session should still have a pending question after one answer")
	}

	// Finish: the response carries summary.finished=true and no question.
	resp := e.expectStatus(t, "POST", fmt.Sprintf("/api/taxonomy/diagnostics/%d/finish", sessionID),
		nil, childHeaders(kid), http.StatusOK)
	var finished map[string]any
	decodeBody(t, resp, &finished)
	if s, _ := finished["summary"].(map[string]any); s == nil || s["finished"] != true {
		t.Fatalf("finish summary.finished = %v, want true (%v)", finished["summary"], finished)
	}
	if finished["question"] != nil {
		t.Fatalf("finished session still serves a question: %v", finished["question"])
	}
	if s, _ := finished["session"].(map[string]any); s["status"] != "completed" {
		t.Fatalf("session status = %v, want completed", finished["session"])
	}

	// A fresh GET agrees: summary.finished stays true, no question.
	resp = e.expectStatus(t, "GET", fmt.Sprintf("/api/taxonomy/diagnostics/%d", sessionID), nil, childHeaders(kid), http.StatusOK)
	var again map[string]any
	decodeBody(t, resp, &again)
	if s, _ := again["summary"].(map[string]any); s == nil || s["finished"] != true {
		t.Fatalf("re-read summary.finished = %v, want true", again["summary"])
	}
	if again["question"] != nil {
		t.Fatalf("re-read of a finished session still serves a question: %v", again["question"])
	}
}

// TestTaxonomyBlindspotsAndExport covers GET /blindspots (buckets from
// kc_mastery) and GET /export (the JSON download, learning data only).
func TestTaxonomyBlindspotsAndExport(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小涛")
	importPack(t, e)

	// Seed two practice outcomes: one strong, one weak.
	practiceAnswered(t, e, childHeaders(kid), map[string]any{
		"topic_id": "cnm_p001", "question_id": "cnm_p001_q1", "answer": "4",
	})
	practiceAnswered(t, e, childHeaders(kid), map[string]any{
		"topic_id": "cnm_p001", "question_id": "cnm_p001_q2", "answer": "9",
	})

	resp := e.expectStatus(t, "GET", "/api/taxonomy/blindspots", nil, childHeaders(kid), http.StatusOK)
	var spots struct {
		Weak      []map[string]any `json:"weak"`
		Shaky     []map[string]any `json:"shaky"`
		Mastered  []map[string]any `json:"mastered"`
		ByChapter []map[string]any `json:"by_chapter"`
	}
	decodeBody(t, resp, &spots)
	if len(spots.Weak)+len(spots.Shaky)+len(spots.Mastered) == 0 {
		t.Fatalf("blindspots all empty after practice")
	}

	// Export: kid downloads their own.
	resp = e.expectStatus(t, "GET", "/api/taxonomy/export", nil, childHeaders(kid), http.StatusOK)
	var payload struct {
		SchemaVersion int              `json:"schema_version"`
		User          map[string]any   `json:"user"`
		Mastery       []map[string]any `json:"mastery"`
		Events        []map[string]any `json:"events"`
	}
	decodeBody(t, resp, &payload)
	if payload.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d", payload.SchemaVersion)
	}
	if payload.User["pin"] != nil || payload.User["session_secret"] != nil {
		t.Errorf("export leaked a secret: %v", payload.User)
	}
	if len(payload.Mastery) == 0 || len(payload.Events) == 0 {
		t.Errorf("export missing learning data: mastery=%d events=%d", len(payload.Mastery), len(payload.Events))
	}

	// Cross-kid export is 403: create a second real child so the request
	// clears session auth (a made-up id would 401 before reaching the
	// ownership check, hiding the 403 contract).
	other := e.createChild(t, "小海")
	e.expectStatus(t, "GET", fmt.Sprintf("/api/taxonomy/export?user_id=%d", kid), nil, childHeaders(other), http.StatusForbidden)
}

// TestTaxonomyCourseGeneration covers POST /admin/taxonomy/courses/
// generate from an explicit topic list: units grouped by chapter, the
// KC links (unit_knowledge_tags), practice resources and knowledge-
// linked outcomes.
func TestTaxonomyCourseGeneration(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小澜")
	importPack(t, e)

	// Kid cannot generate.
	e.expectStatus(t, "POST", "/api/admin/taxonomy/courses/generate", map[string]any{
		"user_id": kid, "title": "补差课", "topic_ids": []string{"cnm_p001"},
	}, childHeaders(kid), http.StatusForbidden)

	resp := e.expectStatus(t, "POST", "/api/admin/taxonomy/courses/generate", map[string]any{
		"user_id":   kid,
		"title":     "数与代数补差",
		"subject":   "math",
		"topic_ids": []string{"cnm_p001", "cnm_p002"},
	}, adminHeaders(), http.StatusCreated)
	var out struct {
		CourseID int64 `json:"course_id"`
	}
	decodeBody(t, resp, &out)
	if out.CourseID == 0 {
		t.Fatalf("course_id = 0")
	}

	// One unit per chapter: cnm_p001 and cnm_p002 sit in the same chapter
	// ("一年级上册 第1单元 5以内数的认识和加、减法"), so the chapter grouping
	// yields a single unit covering both KCs. Each KC keeps its own
	// unit_knowledge_tag link, practice resource and knowledge-linked outcome.
	var units int
	e.db.QueryRow(`SELECT COUNT(*) FROM course_units WHERE course_id = ?`, out.CourseID).Scan(&units)
	if units != 1 {
		t.Errorf("units = %d, want 1 (both topics share one chapter)", units)
	}
	var ukt int
	e.db.QueryRow(`SELECT COUNT(*) FROM unit_knowledge_tags ukt
		JOIN course_units cu ON cu.id = ukt.unit_id WHERE cu.course_id = ?`, out.CourseID).Scan(&ukt)
	if ukt != 2 {
		t.Errorf("unit_knowledge_tags = %d, want 2", ukt)
	}
	var resources int
	e.db.QueryRow(`SELECT COUNT(*) FROM unit_resources ur
		JOIN course_units cu ON cu.id = ur.unit_id WHERE cu.course_id = ? AND ur.resource_type = 'practice'`, out.CourseID).Scan(&resources)
	if resources != 2 {
		t.Errorf("practice resources = %d, want 2", resources)
	}
	var outcomes int
	e.db.QueryRow(`SELECT COUNT(*) FROM course_outcomes WHERE course_id = ? AND knowledge_tag_id IS NOT NULL`, out.CourseID).Scan(&outcomes)
	if outcomes != 2 {
		t.Errorf("knowledge-linked outcomes = %d, want 2", outcomes)
	}
}

// TestTaxonomyAdminImportAuth covers the admin-only import endpoints.
func TestTaxonomyAdminImportAuth(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	kid := e.createChild(t, "小小管理")
	importPack(t, e)

	// Both admin endpoints are admin-only: anonymous gets 401, a kid 403.
	// The pack path (/sync) and the learning-restore path (/import) must
	// never alias, so the permission gate is asserted on both.
	for _, path := range []string{"/api/admin/taxonomy/sync", "/api/admin/taxonomy/import"} {
		e.expectStatus(t, "POST", path, nil, nil, http.StatusUnauthorized)
		e.expectStatus(t, "POST", path, nil, childHeaders(kid), http.StatusForbidden)
	}

	// Admin can re-run the pack import on /sync (idempotent).
	resp := e.expectStatus(t, "POST", "/api/admin/taxonomy/sync", nil, adminHeaders(), http.StatusOK)
	var out struct {
		Imported int `json:"imported"`
	}
	decodeBody(t, resp, &out)
	if out.Imported != 3108 {
		t.Errorf("imported = %d, want 3108", out.Imported)
	}

	// /import is the learning-document restore, not the pack path: an
	// empty or malformed body is a strict-JSON refusal (400), never a
	// silent pack re-run and never a relaxed 200.
	e.expectStatus(t, "POST", "/api/admin/taxonomy/import", nil, adminHeaders(), http.StatusBadRequest)
	e.expectStatus(t, "POST", "/api/admin/taxonomy/import", map[string]any{
		"schema_version": "not-a-number",
		"pack_version":   "p",
		"exported_at":    "2026-11-06T00:00:00Z",
		"target_user_id": kid,
	}, adminHeaders(), http.StatusBadRequest)
	// Unknown fields are schema drift and refused whole (DisallowUnknownFields),
	// which also proves a well-typed body is not enough to get a 200.
	e.expectStatus(t, "POST", "/api/admin/taxonomy/import", map[string]any{
		"schema_version": 1,
		"pack_version":   "p",
		"exported_at":    "2026-11-06T00:00:00Z",
		"target_user_id": kid,
		"unknown_field":  true,
	}, adminHeaders(), http.StatusBadRequest)
}
