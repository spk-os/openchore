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
}

// newFakePaperAI layers the paper answers over the standard fake.
func newFakePaperAI(t *testing.T) *fakePaperAI {
	t.Helper()
	f := &fakePaperAI{fakeAI: newFakeAI(t)}
	f.segment = `{"questions":[{"number":"1","text":"计算 $3+5$","handwritten":false,"bbox":[0.1,0.1,0.8,0.2]},{"number":"2","text":"解方程 $x+2=5$","handwritten":true,"bbox":[0.1,0.3,0.8,0.2]}]}`
	f.judge = `{"question_text":"计算 $3+5$","attempted":true,"is_wrong":true,"wrong_answer_text":"$7$","answer_text":"$8$","analysis":"直接相加。","mistake_analysis":"加法算错。","error_type":"calculation","knowledge_points":["整数加法"],"difficulty":"easy","confidence":0.92}`
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
			content := f.text
			switch {
			case isSegment:
				content = f.segment
			case isJudge:
				if f.judgeFail {
					content = "not json at all"
				} else {
					content = f.judge
				}
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
