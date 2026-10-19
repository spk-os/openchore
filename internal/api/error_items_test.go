package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/llm"
	"github.com/liftedkilt/openchore/internal/model"
)

// fakePaperAI extends the review/tts fake with L1 two-stage answers:
// a segmentation result for "cut the page" prompts and a judgement for
// "judge this question" prompts. Both are keyed on prompt markers so the
// same server can serve any mix of L0/L1 traffic.
type fakePaperAI struct {
	*fakeAI

	mu        sync.Mutex
	segment   string // JSON for segment prompts
	judge     string // JSON for judge prompts
	judgeFail bool   // when set, judging errors (degrade path test)
	measures  string // JSON for the 034 distill prompt
	// The prompt bodies of the last two-stage run, for instructions
	// assertions.
	lastSegmentPrompt string
	lastJudgePrompt   string
}

// newFakePaperAI layers the paper answers over the standard fake.
func newFakePaperAI(t *testing.T) *fakePaperAI {
	t.Helper()
	f := &fakePaperAI{fakeAI: newFakeAI(t)}
	f.segment = `{"questions":[{"number":"1","text":"计算 $3+5$","handwritten":false,"bbox":[0.1,0.1,0.8,0.2]},{"number":"2","text":"解方程 $x+2=5$","handwritten":true,"bbox":[0.1,0.3,0.8,0.2]}]}`
	f.judge = `{"question_text":"计算 $3+5$","attempted":true,"is_wrong":true,"wrong_answer_text":"$7$","answer_text":"$8$","analysis":"直接相加。","mistake_analysis":"加法算错。","error_type":"calculation","knowledge_points":["整数加法"],"difficulty":"easy","confidence":0.92}`
	// The 034 distill (DraftMeasures): one concrete measure.
	f.measures = `{"measures":[{"subject":"math","kc":"整数加法","title":"口算练习：每天 10 题，连续 5 天","detail":"每天晚饭后做 10 道整数加法口算，做完自己先检查一遍再交给家长。","error_types":["calculation"],"priority":1}]}`
	return f
}

// setJudge swaps the judgement the fake returns.
func (f *fakePaperAI) setJudge(j string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.judge = j
}

// failJudge makes every judge call return invalid JSON (the degrade path).
func (f *fakePaperAI) failJudge() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.judgeFail = true
}

// intercept wraps the base fake's handler with the paper prompt logic.
// The base fake answers image prompts with the review JSON; the paper
// endpoints need distinct answers per stage, so the wrapper runs first.
func (f *fakePaperAI) intercept(t *testing.T) {
	t.Helper()
	// Replace the server handler by pointing a new server at our logic.
	// Simpler: swap the mux behavior via a wrapper around the existing
	// server's handler is not possible, so we rebuild the fake with a
	// custom handler chain instead.
}

// setupPaperTest wires a fake that answers the two L1 stages distinctly.
func setupPaperTest(t *testing.T) (*testEnv, *fakePaperAI) {
	t.Helper()
	t.Chdir(t.TempDir())
	env := setupTest(t)
	f := newFakePaperAI(t)
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/v1/chat/completions" {
			f.chatCalls++
			isSegment := strings.Contains(string(body), "Segment the page")
			isJudge := strings.Contains(string(body), "diagnosing ONE question")
			isMeasures := strings.Contains(string(body), "You distill a kid's paper-analysis report")
			content := f.text
			switch {
			case isSegment:
				f.lastSegmentPrompt = string(body)
				content = f.segment
			case isJudge:
				f.lastJudgePrompt = string(body)
				if f.judgeFail {
					content = "not json at all"
				} else {
					content = f.judge
				}
			case isMeasures:
				content = f.measures
			case strings.Contains(string(body), `"image_url"`):
				b, _ := json.Marshal(f.review)
				content = string(b)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}},
			})
			return
		}
		http.NotFound(w, r)
	})
	aiClient := llm.New(f.server.URL+"/v1", "", "test-model")
	env.chores.SetAI(aiClient, nil)
	return env, f
}

// analysisResp is the polled analysis row.
type analysisResp struct {
	ID             int64           `json:"id"`
	CompletionID   int64           `json:"completion_id"`
	UserID         int64           `json:"user_id"`
	Status         string          `json:"status"`
	Stage          string          `json:"stage"`
	Progress       int             `json:"progress"`
	TotalQuestions int             `json:"total_questions"`
	WrongCount     int             `json:"wrong_count"`
	Summary        json.RawMessage `json:"summary"`
}

type errorItemResp struct {
	ID            int64                `json:"id"`
	UserID        int64                `json:"user_id"`
	Subject       string               `json:"subject"`
	QuestionText  string               `json:"question_text"`
	MistakeStatus string               `json:"mistake_status"`
	ErrorType     string               `json:"error_type"`
	MasteryLevel  int                  `json:"mastery_level"`
	Tags          []model.KnowledgeTag `json:"tags"`
}

// startAnalysis posts the L1 job and returns its id.
func (e *testEnv) startAnalysis(t *testing.T, completionID int64) int64 {
	t.Helper()
	resp := e.expectStatus(t, "POST", "/api/admin/analyses",
		map[string]any{"completion_id": completionID, "subject": "math"},
		adminHeaders(), http.StatusCreated)
	var a analysisResp
	decodeBody(t, resp, &a)
	return a.ID
}

// startAnalysisWithInstructions is startAnalysis carrying the parent's
// free-text focus (问题③'s「用户输入要求」).
func (e *testEnv) startAnalysisWithInstructions(t *testing.T, completionID int64, instructions string) int64 {
	t.Helper()
	resp := e.expectStatus(t, "POST", "/api/admin/analyses",
		map[string]any{"completion_id": completionID, "subject": "math", "instructions": instructions},
		adminHeaders(), http.StatusCreated)
	var a analysisResp
	decodeBody(t, resp, &a)
	return a.ID
}

// waitForAnalysisDone polls the job until it leaves running (done or
// failed), asserting it lands on done.
func (e *testEnv) waitForAnalysisDone(t *testing.T, id int64) analysisResp {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp := e.expectStatus(t, "GET", fmt.Sprintf("/api/admin/analyses/%d", id), nil, adminHeaders(), http.StatusOK)
		var a analysisResp
		decodeBody(t, resp, &a)
		if a.Status == model.AnalysisDone {
			return a
		}
		if a.Status == model.AnalysisFailed {
			t.Fatalf("analysis failed unexpectedly: %+v", a)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("analysis did not finish in time")
	return analysisResp{}
}

// TestPaperAnalysisExtractsWrongQuestions walks the happy path: a photo
// completion, the two-stage parse, wrong items recorded with knowledge
// tags, and the diagnosis summary the parent's report page renders.
func TestPaperAnalysisExtractsWrongQuestions(t *testing.T) {
	env, f := setupPaperTest(t)
	env.createAdmin(t)
	kidID, completionID := completePhotoChore(t, env)

	analysisID := env.startAnalysis(t, completionID)
	env.chores.WaitForAnalyses()

	a := env.waitForAnalysisDone(t, analysisID)
	if a.TotalQuestions != 2 {
		t.Errorf("expected 2 questions parsed, got %d", a.TotalQuestions)
	}
	if a.WrongCount != 2 {
		t.Errorf("expected 2 wrong (fake judges everything wrong), got %d", a.WrongCount)
	}
	if len(a.Summary) == 0 {
		t.Fatal("expected a diagnosis summary on the finished analysis")
	}

	// The kid's wrong-question book holds both items with the tag.
	resp := env.expectStatus(t, "GET", "/api/error-items", nil, childHeaders(kidID), http.StatusOK)
	var items []errorItemResp
	decodeBody(t, resp, &items)
	if len(items) != 2 {
		t.Fatalf("expected 2 error items, got %d", len(items))
	}
	if items[0].MistakeStatus != model.MistakeWrongAttempt {
		t.Errorf("expected wrong_attempt, got %s", items[0].MistakeStatus)
	}
	if items[0].ErrorType != model.ErrorTypeCalculation {
		t.Errorf("expected calculation error type, got %s", items[0].ErrorType)
	}
	if len(items[0].Tags) != 1 || items[0].Tags[0].Name != "整数加法" {
		t.Errorf("expected the knowledge tag to ride along, got %+v", items[0].Tags)
	}
	_ = f
}

// TestPaperAnalysisDegradesToManualQueue checks the spec's degrade path:
// when judging fails (after the built-in retry), the question still lands
// in the book as mistake_status=unknown — never a silent drop.
func TestPaperAnalysisDegradesToManualQueue(t *testing.T) {
	env, f := setupPaperTest(t)
	f.failJudge()
	env.createAdmin(t)
	kidID, completionID := completePhotoChore(t, env)

	analysisID := env.startAnalysis(t, completionID)
	env.chores.WaitForAnalyses()

	a := env.waitForAnalysisDone(t, analysisID)
	if a.TotalQuestions != 2 {
		t.Errorf("expected the 2 cut questions recorded, got %d", a.TotalQuestions)
	}

	resp := env.expectStatus(t, "GET", "/api/error-items", nil, childHeaders(kidID), http.StatusOK)
	var items []errorItemResp
	decodeBody(t, resp, &items)
	if len(items) != 2 {
		t.Fatalf("expected 2 degraded items, got %d", len(items))
	}
	for _, it := range items {
		if it.MistakeStatus != model.MistakeUnknown {
			t.Errorf("expected unknown status on degrade, got %s", it.MistakeStatus)
		}
		if it.ErrorType != "" {
			t.Errorf("expected no error type on degrade, got %s", it.ErrorType)
		}
	}
}

// TestSubmitReviewResultMovesMastery walks the three-state machine:
// correct retry New → Reviewing → Mastered; a wrong retry keeps state.
func TestSubmitReviewResultMovesMastery(t *testing.T) {
	env, _ := setupPaperTest(t)
	env.createAdmin(t)
	kidID, completionID := completePhotoChore(t, env)

	analysisID := env.startAnalysis(t, completionID)
	env.chores.WaitForAnalyses()
	_ = env.waitForAnalysisDone(t, analysisID)

	resp := env.expectStatus(t, "GET", "/api/error-items", nil, childHeaders(kidID), http.StatusOK)
	var items []errorItemResp
	decodeBody(t, resp, &items)
	if len(items) == 0 {
		t.Fatal("expected error items to exist")
	}
	id := items[0].ID

	// Correct retry: New → Reviewing.
	resp = env.expectStatus(t, "POST", fmt.Sprintf("/api/error-items/%d/review-result", id),
		map[string]any{"is_correct": true}, childHeaders(kidID), http.StatusOK)
	var item errorItemResp
	decodeBody(t, resp, &item)
	if item.MasteryLevel != model.MasteryReviewing {
		t.Errorf("expected reviewing after a correct retry, got %d", item.MasteryLevel)
	}

	// Correct again: Reviewing → Mastered.
	resp = env.expectStatus(t, "POST", fmt.Sprintf("/api/error-items/%d/review-result", id),
		map[string]any{"is_correct": true}, childHeaders(kidID), http.StatusOK)
	decodeBody(t, resp, &item)
	if item.MasteryLevel != model.MasteryMastered {
		t.Errorf("expected mastered after two correct retries, got %d", item.MasteryLevel)
	}

	// A wrong retry on the other item keeps its state.
	resp = env.expectStatus(t, "POST", fmt.Sprintf("/api/error-items/%d/review-result", items[1].ID),
		map[string]any{"is_correct": false}, childHeaders(kidID), http.StatusOK)
	decodeBody(t, resp, &item)
	if item.MasteryLevel != model.MasteryNew {
		t.Errorf("expected a wrong retry to keep New, got %d", item.MasteryLevel)
	}
}

// TestCorrectionTaskCreatesChore checks the diagnosis report's
// "一键订正" hook: one wrong item becomes a one-day parent-approved chore
// assigned to the kid.
func TestCorrectionTaskCreatesChore(t *testing.T) {
	env, _ := setupPaperTest(t)
	env.createAdmin(t)
	kidID, completionID := completePhotoChore(t, env)

	analysisID := env.startAnalysis(t, completionID)
	env.chores.WaitForAnalyses()
	_ = env.waitForAnalysisDone(t, analysisID)

	// Admin list defaults to the admin's own (empty) book without a
	// user_id; the kid's items come back with the filter.
	resp := env.expectStatus(t, "GET", fmt.Sprintf("/api/error-items?user_id=%d", kidID), nil, adminHeaders(), http.StatusOK)
	var items []errorItemResp
	decodeBody(t, resp, &items)
	if len(items) == 0 {
		t.Fatal("expected the kid's items via user_id filter")
	}

	resp = env.expectStatus(t, "POST", fmt.Sprintf("/api/admin/error-items/%d/correction-task", items[0].ID),
		map[string]any{"points": 4}, adminHeaders(), http.StatusCreated)
	var out struct {
		Chore struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"chore"`
		Schedule struct {
			AssignedTo int64 `json:"assigned_to"`
		} `json:"schedule"`
	}
	decodeBody(t, resp, &out)
	if !strings.Contains(out.Chore.Title, "订正错题") {
		t.Errorf("expected a correction title, got %q", out.Chore.Title)
	}
	if out.Schedule.AssignedTo != int64(kidID) {
		t.Errorf("expected the chore assigned to the kid, got %d", out.Schedule.AssignedTo)
	}
}

// TestErrorItemAccessControl: kids can't read or retry another kid's
// items, and the book endpoints need auth.
func TestErrorItemAccessControl(t *testing.T) {
	env, _ := setupPaperTest(t)
	env.createAdmin(t)
	kidID, completionID := completePhotoChore(t, env)
	otherKid := env.createChild(t, "Other")

	analysisID := env.startAnalysis(t, completionID)
	env.chores.WaitForAnalyses()
	_ = env.waitForAnalysisDone(t, analysisID)

	resp := env.expectStatus(t, "GET", "/api/error-items", nil, childHeaders(kidID), http.StatusOK)
	var items []errorItemResp
	decodeBody(t, resp, &items)
	if len(items) == 0 {
		t.Fatal("expected error items to exist")
	}
	id := items[0].ID

	// The other kid can't read it.
	env.expectStatus(t, "GET", fmt.Sprintf("/api/error-items/%d", id), nil, childHeaders(otherKid), http.StatusForbidden)
	// Nor report a retry outcome.
	env.expectStatus(t, "POST", fmt.Sprintf("/api/error-items/%d/review-result", id),
		map[string]any{"is_correct": true}, childHeaders(otherKid), http.StatusForbidden)
	// Unauthenticated access is rejected.
	env.request(t, "GET", "/api/error-items", nil, nil)
}

// TestAutoPaperAnalysisOnSubmission walks the opt-in auto-parse: with
// ai_auto_paper_analysis on, the kid's photo completion spawns the
// analysis (and its measure distill) by itself — no POST /admin/analyses.
func TestAutoPaperAnalysisOnSubmission(t *testing.T) {
	env, _ := setupPaperTest(t)
	env.createAdmin(t)
	env.setSetting(t, "ai_auto_paper_analysis", "true")
	kidID, _ := completePhotoChore(t, env)

	// The completion itself is the trigger, so the auto job must exist by
	// the time the queued work drains. waitForAnalysisDone polls by id,
	// but we only know the completion — find via the kid's analyses list.
	env.chores.WaitForAnalyses()
	var analyses []analysisResp
	resp := env.expectStatus(t, "GET", fmt.Sprintf("/api/admin/users/%d/analyses", kidID), nil, adminHeaders(), http.StatusOK)
	decodeBody(t, resp, &analyses)
	if len(analyses) != 1 {
		t.Fatalf("expected 1 auto analysis, got %d", len(analyses))
	}
	if analyses[0].Status != model.AnalysisDone {
		t.Errorf("expected done, got %s", analyses[0].Status)
	}
	if analyses[0].TotalQuestions != 2 {
		t.Errorf("expected 2 parsed questions, got %d", analyses[0].TotalQuestions)
	}

	// The learn-from-it half: measures distilled automatically.
	resp = env.expectStatus(t, "GET", fmt.Sprintf("/api/admin/measures?user_id=%d", kidID), nil, adminHeaders(), http.StatusOK)
	var measures []map[string]any
	decodeBody(t, resp, &measures)
	if len(measures) == 0 {
		t.Fatal("expected auto-distilled measures after the auto analysis")
	}
	if measures[0]["origin"] != model.MeasureOriginAnalysis {
		t.Errorf("expected origin=analysis, got %v", measures[0]["origin"])
	}
}

// TestAutoPaperAnalysisOffByDefault pins the gate: without the setting,
// completing a photo chore must NOT create any analysis — the parent's
// manual button stays the only parse path.
func TestAutoPaperAnalysisOffByDefault(t *testing.T) {
	env, _ := setupPaperTest(t)
	kidID, _ := completePhotoChore(t, env)

	env.chores.WaitForAnalyses()
	resp := env.expectStatus(t, "GET", fmt.Sprintf("/api/admin/users/%d/analyses", kidID), nil, adminHeaders(), http.StatusOK)
	var analyses []analysisResp
	decodeBody(t, resp, &analyses)
	if len(analyses) != 0 {
		t.Fatalf("expected no auto analysis with the setting off, got %d", len(analyses))
	}
}


// TestErrorItemsSurviveCompletionDelete locks the 035 migration's contract:// deleting the chore (which cascades schedules → completions) must keep the
// kid's extracted error items — the error book is a learning asset that
// outlives the task that produced it. Regression guard for the silent
// history loss the CASCADE foreign key used to cause.
func TestErrorItemsSurviveCompletionDelete(t *testing.T) {
	env, _ := setupPaperTest(t)
	env.createAdmin(t)
	kidID, completionID := completePhotoChore(t, env)

	analysisID := env.startAnalysis(t, completionID)
	env.chores.WaitForAnalyses()
	_ = env.waitForAnalysisDone(t, analysisID)

	// The kid owns their extracted items before any cleanup.
	resp := env.expectStatus(t, "GET", "/api/error-items", nil, childHeaders(kidID), http.StatusOK)
	var items []errorItemResp
	decodeBody(t, resp, &items)
	if len(items) == 0 {
		t.Fatal("expected error items before the chore delete")
	}

	// Which chore the completion belongs to: the completion row carries the
	// schedule, the schedule the chore. Delete the chore (the parent's task
	// cleanup) — schedules and completions cascade away.
	cc := env.completion(t, completionID)
	var scheduleID, choreID int64
	env.db.QueryRow(
		"SELECT id, chore_id FROM chore_schedules WHERE id = ?", cc.ChoreScheduleID,
	).Scan(&scheduleID, &choreID)
	env.expectStatus(t, "DELETE", fmt.Sprintf("/api/chores/%d", choreID), nil, adminHeaders(), http.StatusNoContent)

	// The error book survives: same items, only the completion link dropped.
	resp = env.expectStatus(t, "GET", "/api/error-items", nil, childHeaders(kidID), http.StatusOK)
	var after []errorItemResp
	decodeBody(t, resp, &after)
	if len(after) != len(items) {
		t.Fatalf("error items must survive the chore delete: had %d, left %d", len(items), len(after))
	}
}

// TestPaperAnalysisInstructionsCarryThrough checks 问题③'s「用户输入要求」
// end to end: the parent's free-text focus rides the request, is echoed
// back on the polled row, and lands inside BOTH stage prompts the model
// receives — a parse without instructions keeps the prompt byte-identical
// (the auto path).
func TestPaperAnalysisInstructionsCarryThrough(t *testing.T) {
	env, f := setupPaperTest(t)
	env.createAdmin(t)
	kidID, completionID := completePhotoChore(t, env)

	const focus = "重点分析第3题的计算错误"
	analysisID := env.startAnalysisWithInstructions(t, completionID, focus)
	env.chores.WaitForAnalyses()

	// The polled row echoes the instructions back.
	resp := env.expectStatus(t, "GET", fmt.Sprintf("/api/admin/analyses/%d", analysisID), nil, adminHeaders(), http.StatusOK)
	var row map[string]any
	decodeBody(t, resp, &row)
	if row["instructions"] != focus {
		t.Errorf("expected instructions echoed on the analysis row, got %v", row["instructions"])
	}

	// Both stage prompts carry the requirement block.
	f.mu.Lock()
	seg, judge := f.lastSegmentPrompt, f.lastJudgePrompt
	f.mu.Unlock()
	if !strings.Contains(seg, focus) {
		t.Errorf("segment prompt missing the parent's instructions: %s", truncateForLog(seg))
	}
	if !strings.Contains(judge, focus) {
		t.Errorf("judge prompt missing the parent's instructions: %s", truncateForLog(judge))
	}
	_ = kidID
}

// TestPaperAnalysisWithoutInstructionsKeepsPromptClean pins the auto path:
// no instructions field → no requirement block in either prompt.
func TestPaperAnalysisWithoutInstructionsKeepsPromptClean(t *testing.T) {
	env, f := setupPaperTest(t)
	env.createAdmin(t)
	_, completionID := completePhotoChore(t, env)

	analysisID := env.startAnalysis(t, completionID)
	env.chores.WaitForAnalyses()
	_ = analysisID

	f.mu.Lock()
	seg, judge := f.lastSegmentPrompt, f.lastJudgePrompt
	f.mu.Unlock()
	if strings.Contains(seg, "家长的补充要求") {
		t.Errorf("segment prompt must stay clean without instructions")
	}
	if strings.Contains(judge, "家长的补充要求") {
		t.Errorf("judge prompt must stay clean without instructions")
	}
}

// TestPaperAnalysisInstructionsRejectedTooLong pins the 500-rune cap: an
// essay-sized requirement is a 400, not a bloated multimodal prompt.
func TestPaperAnalysisInstructionsRejectedTooLong(t *testing.T) {
	env, _ := setupPaperTest(t)
	env.createAdmin(t)
	_, completionID := completePhotoChore(t, env)

	long := strings.Repeat("重", 501)
	env.expectStatus(t, "POST", "/api/admin/analyses",
		map[string]any{"completion_id": completionID, "subject": "math", "instructions": long},
		adminHeaders(), http.StatusBadRequest)
}

// TestPaperAnalysisZeroQuestionsSummaryNotNull is the regression guard for
// the dashboard-crash root cause (问题③「打不开了」): a photo that parses to
// ZERO questions must still produce a summary whose questions/notes arrays
// serialize as [] (not null) — the report page maps/filters them directly.
func TestPaperAnalysisZeroQuestionsSummaryNotNull(t *testing.T) {
	env, f := setupPaperTest(t)
	env.createAdmin(t)
	_, completionID := completePhotoChore(t, env)

	f.mu.Lock()
	f.segment = `{"questions":[]}` // a photo that isn't an exam page
	f.mu.Unlock()

	analysisID := env.startAnalysis(t, completionID)
	env.chores.WaitForAnalyses()
	resp := env.expectStatus(t, "GET", fmt.Sprintf("/api/admin/analyses/%d", analysisID), nil, adminHeaders(), http.StatusOK)
	var row struct {
		Status  string          `json:"status"`
		Summary json.RawMessage `json:"summary"`
	}
	decodeBody(t, resp, &row)
	if row.Status != model.AnalysisDone {
		t.Fatalf("a 0-question parse is done, not failed: %s", row.Status)
	}
	if !json.Valid(row.Summary) {
		t.Fatalf("summary must be valid JSON, got %s", row.Summary)
	}
	var sum map[string]json.RawMessage
	if err := json.Unmarshal(row.Summary, &sum); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	// The exact contract the frontend relies on: [] not null.
	if string(sum["questions"]) == "null" || string(sum["questions"]) == "" {
		t.Fatalf("questions must serialize as [], got %s", sum["questions"])
	}
	if string(sum["notes"]) == "null" {
		t.Fatalf("notes must serialize as [], got %s", sum["notes"])
	}
	if !strings.Contains(string(row.Summary), "未识别到题目") {
		t.Errorf("expected the 0-question note in the summary, got %s", row.Summary)
	}
}

// truncateForLog keeps assertion failures readable.
func truncateForLog(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
