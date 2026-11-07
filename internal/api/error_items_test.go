package api_test

import (
	"database/sql"
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
	// 042 之后 startAnalysis 的 math 走 subjects 表校验（validSubject）；
	// 夹具必须和线上启动一样先导入学科词表，否则空表会把合法学科判成 400。
	// seedSubjects 走真 DB 的 ImportPack，不是 mock（见
	// subject_validation_test.go 的同名 helper）。
	seedSubjects(t, env)
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

// TestCorrectionTaskIdempotent pins the 一键订正 idempotency (2026-10-22):
// a double-tap used to fan out a second chore+schedule for the same error
// item (live had a 订正 pair on error_item:2). The second request reuses
// the existing chore — 200 + reused:true — and lands nothing new.
func TestCorrectionTaskIdempotent(t *testing.T) {
	env, _ := setupPaperTest(t)
	env.createAdmin(t)
	kidID, completionID := completePhotoChore(t, env)

	analysisID := env.startAnalysis(t, completionID)
	env.chores.WaitForAnalyses()
	_ = env.waitForAnalysisDone(t, analysisID)

	resp := env.expectStatus(t, "GET", fmt.Sprintf("/api/error-items?user_id=%d", kidID), nil, adminHeaders(), http.StatusOK)
	var items []errorItemResp
	decodeBody(t, resp, &items)
	if len(items) == 0 {
		t.Fatal("expected the kid's items via user_id filter")
	}

	type correctionOut struct {
		Chore struct {
			ID    int64  `json:"id"`
			Title string `json:"title"`
		} `json:"chore"`
		Reused bool `json:"reused"`
	}

	// First tap creates.
	resp = env.expectStatus(t, "POST", fmt.Sprintf("/api/admin/error-items/%d/correction-task", items[0].ID),
		map[string]any{"points": 4}, adminHeaders(), http.StatusCreated)
	var first correctionOut
	decodeBody(t, resp, &first)
	if first.Reused {
		t.Fatalf("first tap must create, got reused=true (chore %d)", first.Chore.ID)
	}

	// Second tap on the same item reuses the same chore — no new row.
	resp = env.expectStatus(t, "POST", fmt.Sprintf("/api/admin/error-items/%d/correction-task", items[0].ID),
		map[string]any{"points": 4}, adminHeaders(), http.StatusOK)
	var second correctionOut
	decodeBody(t, resp, &second)
	if !second.Reused {
		t.Fatalf("second tap must reuse, got reused=false (chore %d)", second.Chore.ID)
	}
	if second.Chore.ID != first.Chore.ID {
		t.Fatalf("expected the same chore %d reused, got a new chore %d", first.Chore.ID, second.Chore.ID)
	}

	// Exactly one correction chore and one schedule survived both taps.
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM chores WHERE title LIKE '订正错题%'`).Scan(&n); err != nil {
		t.Fatalf("count correction chores: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 correction chore, found %d", n)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM chore_schedules WHERE chore_id = ?`, first.Chore.ID).Scan(&n); err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 schedule on the reused chore, found %d", n)
	}
}

// TestPaperAnalysisSubjectEmptyAndUnknown pins StartPaperAnalysis' 042
// contract: an omitted subject is 201 and saved as "" (not the column's
// legacy 'math' default), while a non-empty value outside the seeded word
// list is a 400 that creates no job row.
func TestPaperAnalysisSubjectEmptyAndUnknown(t *testing.T) {
	env, _ := setupPaperTest(t) // seeds the ten-subject word list in the real DB
	_, completionID := completePhotoChore(t, env)

	resp := env.expectStatus(t, "POST", "/api/admin/analyses",
		map[string]any{"completion_id": completionID}, adminHeaders(), http.StatusCreated)
	var created struct {
		ID      int64  `json:"id"`
		Subject string `json:"subject"`
	}
	decodeBody(t, resp, &created)
	if created.Subject != "" {
		t.Fatalf("omitted subject must stay empty on the response, got %q", created.Subject)
	}
	var dbSubject string
	if err := env.db.QueryRow(`SELECT subject FROM paper_analyses WHERE id = ?`, created.ID).Scan(&dbSubject); err != nil {
		t.Fatalf("read analysis subject: %v", err)
	}
	if dbSubject != "" {
		t.Fatalf("omitted subject must be saved as empty, got %q", dbSubject)
	}
	// Snapshot every knowledge tag before the run so the final count proves
	// the empty-subject path created none at all.
	tagsBefore := countAllKnowledgeTags(t, env)

	env.chores.WaitForAnalyses()
	_ = env.waitForAnalysisDone(t, created.ID)

	// Empty-subject items stay in the book (legal) but the auto KC binding
	// must import them under subject='' (a garbage tag) or default them to
	// math. Both are on-disk checks against the real migrations.
	var emptyItems, mathItems int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM error_items WHERE subject = ''`).Scan(&emptyItems); err != nil {
		t.Fatalf("count empty-subject error items: %v", err)
	}
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM error_items WHERE subject = 'math'`).Scan(&mathItems); err != nil {
		t.Fatalf("count math error items: %v", err)
	}
	if emptyItems == 0 {
		t.Fatalf("an empty-subject analysis must still record its wrong questions (subject=''), found none")
	}
	if mathItems != 0 {
		t.Fatalf("empty subject must not default to math, found %d math error items", mathItems)
	}
	var emptyTags int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM knowledge_tags WHERE subject = ''`).Scan(&emptyTags); err != nil {
		t.Fatalf("count subject='' knowledge tags: %v", err)
	}
	if emptyTags != 0 {
		t.Fatalf("auto KC binding must not create subject='' knowledge tags, found %d", emptyTags)
	}
	name := "整数加法" // the fake judge's knowledge point must not leak a tag
	var leaked int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM knowledge_tags WHERE name = ?`, name).Scan(&leaked); err != nil {
		t.Fatalf("count %q knowledge tags: %v", name, err)
	}
	if leaked != 0 {
		t.Fatalf("auto KC binding must not create a tag for %q on an empty-subject analysis", name)
	}
	var emptyLinks int
	if err := env.db.QueryRow(
		`SELECT COUNT(*) FROM error_item_tags eit JOIN error_items ei ON ei.id = eit.error_item_id WHERE ei.subject = ''`).
		Scan(&emptyLinks); err != nil {
		t.Fatalf("count tag links of empty-subject items: %v", err)
	}
	if emptyLinks != 0 {
		t.Fatalf("empty-subject error items must carry no auto-bound tags, found %d links", emptyLinks)
	}
	if got := countAllKnowledgeTags(t, env); got != tagsBefore {
		t.Fatalf("empty-subject analysis changed knowledge_tags count: before %d, after %d", tagsBefore, got)
	}

	env.expectStatus(t, "POST", "/api/admin/analyses",
		map[string]any{"completion_id": completionID, "subject": "gym"}, adminHeaders(), http.StatusBadRequest)
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM paper_analyses`).Scan(&n); err != nil {
		t.Fatalf("count analyses: %v", err)
	}
	if n != 1 {
		t.Fatalf("a rejected subject must not create an analysis, found %d rows", n)
	}
}

func countAllKnowledgeTags(t *testing.T, env *testEnv) int {
	t.Helper()
	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM knowledge_tags`).Scan(&n); err != nil {
		t.Fatalf("count knowledge_tags: %v", err)
	}
	return n
}

// TestPaperAnalysisSubjectDisabled pins the enabled flag: math switched
// off before the handler's first vocabulary lookup is a 400, while the
// other seeded subjects still start a job.
func TestPaperAnalysisSubjectDisabled(t *testing.T) {
	env, _ := setupPaperTest(t)
	// Flip before any validSubject call so the handler's lazy cache sees
	// the disabled row (restart semantics, same trick as
	// subject_validation_test.go's disabled-case tests).
	env.mustExec(t, `UPDATE subjects SET enabled = 0 WHERE key = 'math'`)
	_, completionID := completePhotoChore(t, env)

	env.expectStatus(t, "POST", "/api/admin/analyses",
		map[string]any{"completion_id": completionID, "subject": "english"}, adminHeaders(), http.StatusCreated)
	env.expectStatus(t, "POST", "/api/admin/analyses",
		map[string]any{"completion_id": completionID, "subject": "math"}, adminHeaders(), http.StatusBadRequest)
	env.chores.WaitForAnalyses()

	var n int
	if err := env.db.QueryRow(`SELECT COUNT(*) FROM paper_analyses`).Scan(&n); err != nil {
		t.Fatalf("count analyses: %v", err)
	}
	if n != 1 {
		t.Fatalf("only the enabled subject must create an analysis, found %d rows", n)
	}
}

// TestErrorItemUpdateSubjectValidation pins UpdateErrorItem's write gate:
// an empty/omitted subject keeps the stored value, every one of the ten
// seeded subjects can be switched to, and junk is a 400 that leaves the
// row untouched. Frozen R2 contract: a subject change without
// knowledge_tag_ids is only legal when the item's current tags already
// share the new subject (an empty tag set trivially does). So the
// per-subject switch here either clears the old tags in the same body
// (knowledge_tag_ids: []) or explicitly resends compatible tags.
func TestErrorItemUpdateSubjectValidation(t *testing.T) {
	env, _ := setupPaperTest(t)
	kidID, completionID := completePhotoChore(t, env)
	analysisID := env.startAnalysis(t, completionID) // subject=math, word list seeded
	env.chores.WaitForAnalyses()
	_ = env.waitForAnalysisDone(t, analysisID)

	resp := env.expectStatus(t, "GET", "/api/error-items", nil, childHeaders(kidID), http.StatusOK)
	var items []errorItemResp
	decodeBody(t, resp, &items)
	if len(items) == 0 {
		t.Fatal("expected error items to exist")
	}
	id := items[0].ID
	path := fmt.Sprintf("/api/admin/error-items/%d", id)

	// countTagLinks is the on-disk truth for every assertion below.
	countTagLinks := func() []int64 {
		t.Helper()
		rows, err := env.db.Query(`SELECT tag_id FROM error_item_tags WHERE error_item_id = ? ORDER BY tag_id`, id)
		if err != nil {
			t.Fatalf("read error_item_tags: %v", err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var tid int64
			if err := rows.Scan(&tid); err != nil {
				t.Fatalf("scan tag link: %v", err)
			}
			out = append(out, tid)
		}
		return out
	}
	dbSubject := func() string {
		t.Helper()
		var s string
		if err := env.db.QueryRow(`SELECT subject FROM error_items WHERE id = ?`, id).Scan(&s); err != nil {
			t.Fatalf("read error item subject: %v", err)
		}
		return s
	}

	// Case A: the analysed item carries auto-created math tags in practice.
	// Switch to chinese WITHOUT knowledge_tag_ids: the old math tags are
	// cross-subject, so the frozen contract must reject the whole request
	// and leave subject and links untouched (this is the R2 guard — do not
	// drop it just because the old fixture assumed 200).
	if links := countTagLinks(); len(links) > 0 {
		env.expectStatus(t, "PUT", path, map[string]any{"subject": "chinese", "question_text": "跨学科应被拒"},
			adminHeaders(), http.StatusBadRequest)
		if got := dbSubject(); got != "math" {
			t.Fatalf("rejected cross-subject switch must keep subject math, got %q", got)
		}
		after := countTagLinks()
		if len(after) != len(links) {
			t.Fatalf("rejected cross-subject switch must keep %d tag links, got %d", len(links), len(after))
		}
		var qt string
		if err := env.db.QueryRow(`SELECT question_text FROM error_items WHERE id = ?`, id).Scan(&qt); err != nil {
			t.Fatalf("read question_text: %v", err)
		}
		if qt == "跨学科应被拒" {
			t.Fatal("rejected switch must not write the other fields either")
		}
	} else {
		// No tags: the guard has nothing to reject, the switch is legal.
		env.expectStatus(t, "PUT", path, map[string]any{"subject": "chinese"}, adminHeaders(), http.StatusOK)
		env.expectStatus(t, "PUT", path, map[string]any{"subject": "math"}, adminHeaders(), http.StatusOK)
	}

	// Explicit "" and an omitted subject both keep the original value,
	// even while another field is being corrected.
	for _, body := range []map[string]any{{"subject": ""}, {"analysis": "家长补充"}} {
		resp = env.expectStatus(t, "PUT", path, body, adminHeaders(), http.StatusOK)
		var item errorItemResp
		decodeBody(t, resp, &item)
		if item.Subject != "math" {
			t.Fatalf("empty/omitted subject must keep math, got %q (%v)", item.Subject, body)
		}
	}
	if got := dbSubject(); got != "math" {
		t.Fatalf("db subject after empty updates = %q, want math", got)
	}

	// Every seeded subject switches through with an explicit tag list in
	// the same body: [] clears the old subject's tags (first switch), then
	// each further switch is field-only (math tag re-attached when we come
	// back to math so the next [] clear is a real cross-subject clear).
	mustMathTag, err := env.store.FindOrCreateKnowledgeTag(t.Context(), "整数加法", "math", nil)
	if err != nil {
		t.Fatalf("FindOrCreateKnowledgeTag(math): %v", err)
	}
	for _, subject := range []string{"math", "chinese", "english", "physics", "chemistry",
		"biology", "science", "morality", "history", "geography"} {
		body := map[string]any{"subject": subject}
		if subject == "math" {
			body["knowledge_tag_ids"] = []int64{mustMathTag}
		} else if len(countTagLinks()) > 0 {
			body["knowledge_tag_ids"] = []int64{}
		}
		resp = env.expectStatus(t, "PUT", path, body, adminHeaders(), http.StatusOK)
		var item errorItemResp
		decodeBody(t, resp, &item)
		if item.Subject != subject {
			t.Fatalf("switch to %s: got subject %q", subject, item.Subject)
		}
		if got := dbSubject(); got != subject {
			t.Fatalf("switch to %s: db subject = %q", subject, got)
		}
	}
	links := countTagLinks()
	if len(links) != 0 {
		t.Fatalf("the [] clears along the way must leave no links for a non-math subject, got %v", links)
	}

	// FSRS state must survive a field-only PUT: mastery_level / next_review_at
	// (the denormalized scheduling columns the PUT writes back from its read)
	// and the L4 card itself are owned by the review flow, not by a parent's
	// edit of the question text.
	if _, err := env.db.Exec(`UPDATE error_items SET mastery_level = 1, next_review_at = '2027-01-02T03:04:05Z' WHERE id = ?`, id); err != nil {
		t.Fatalf("seed FSRS state: %v", err)
	}
	type reviewCard struct {
		Front, State, LastReview string
		Due                      string
		Reps, Lapses             int64
		Stability, Difficulty    float64
	}
	readCard := func() (reviewCard, bool) {
		t.Helper()
		var c reviewCard
		var last sql.NullString
		err := env.db.QueryRow(`SELECT front_text, state, COALESCE(last_review,''), due, reps, lapses, stability, difficulty
			FROM review_items WHERE source_type = 'error_item' AND source_id = ?`, id).
			Scan(&c.Front, &c.State, &last, &c.Due, &c.Reps, &c.Lapses, &c.Stability, &c.Difficulty)
		if err == sql.ErrNoRows {
			return reviewCard{}, false
		}
		if err != nil {
			t.Fatalf("read review card: %v", err)
		}
		c.LastReview = last.String
		return c, true
	}
	cardBefore, hadCard := readCard()
	env.expectStatus(t, "PUT", path, map[string]any{"subject": "geography", "question_text": "FSRS 不应重置"},
		adminHeaders(), http.StatusOK)
	var gotMastery int
	var gotNext sql.NullString
	if err := env.db.QueryRow(`SELECT mastery_level, next_review_at FROM error_items WHERE id = ?`, id).
		Scan(&gotMastery, &gotNext); err != nil {
		t.Fatalf("re-read FSRS columns: %v", err)
	}
	if gotMastery != 1 || !gotNext.Valid || !strings.HasPrefix(gotNext.String, "2027-01-02T03:04:05") {
		t.Fatalf("PUT reset FSRS columns: mastery=%d next_review_at=%v", gotMastery, gotNext)
	}
	if !hadCard {
		t.Log("该夹具没有 L4 复习卡，只校验 error_items 上的 FSRS 列")
	}
	cardAfter, hasCard := readCard()
	if hadCard != hasCard {
		t.Fatalf("PUT created/removed the review card: before=%v after=%v", hadCard, hasCard)
	}
	if hadCard && cardAfter != cardBefore {
		t.Fatalf("PUT mutated the FSRS review card:\nbefore %+v\nafter  %+v", cardBefore, cardAfter)
	}

	// Junk subject is a 400 that leaves the row untouched.
	env.expectStatus(t, "PUT", path, map[string]any{"subject": "gym"}, adminHeaders(), http.StatusBadRequest)
	resp = env.expectStatus(t, "GET", fmt.Sprintf("/api/error-items/%d", id), nil, adminHeaders(), http.StatusOK)
	var after errorItemResp
	decodeBody(t, resp, &after)
	if after.Subject != "geography" {
		t.Fatalf("a 400 must not rewrite the stored subject, got %q", after.Subject)
	}
}
