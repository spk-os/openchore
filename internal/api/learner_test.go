package api_test

// L3 API tests: the student-config endpoints, the profile aggregation,
// the event-stream wiring on the existing action paths (answer/review),
// and the kid-side scaffold ladder. The store-level SQL (Laplace
// smoothing, json_each membership, UTC bounding) is pinned in
// store/learner_test.go; these pin the HTTP contract.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// learnerEnv is one admin + one kid behind the full router.
type learnerEnv struct {
	t     *testing.T
	s     *store.Store
	srv   *httptest.Server
	admin *model.User
	kid   *model.User
}

// setupLearnerEnv boots the full router through setupTest (migrated
// in-memory DB, sessions signed with the test secret) and adds one kid
// alongside setupTest's admin (user 1).
func setupLearnerEnv(t *testing.T) *learnerEnv {
	t.Helper()
	env := setupTest(t)
	// setupTest boots an empty DB — bootstrap the admin the same way
	// testEnv.createAdmin does (user 1, token sessionToken(1)), then add
	// the kid (user 2).
	if _, err := env.db.Exec(`INSERT INTO users (name, avatar_url, role) VALUES ('Admin', '', 'admin')`); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	admin := &model.User{ID: 1, Name: "Admin", Role: "admin"}
	kid := &model.User{Name: "小明", Role: "child"}
	if err := env.store.CreateUser(context.Background(), kid); err != nil {
		t.Fatalf("create kid: %v", err)
	}
	return &learnerEnv{t: t, s: env.store, srv: env.server, admin: admin, kid: kid}
}

// do runs one authenticated request (the named user's session token)
// and decodes the JSON body.
func (e *learnerEnv) do(method, path string, body any, u *model.User) (int, map[string]any) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatalf("request build: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sessionToken(int(u.ID)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// doRaw is do without JSON decoding (status-only assertions).
func (e *learnerEnv) doRaw(method, path string, body any, u *model.User) int {
	code, _ := e.do(method, path, body, u)
	return code
}

// TestStudentConfigRoundtrip pins the GET/PUT contract: the first GET
// materializes defaults, a PUT persists, an invalid enum is normalized
// (not rejected), and the kid scope holds.
func TestStudentConfigRoundtrip(t *testing.T) {
	e := setupLearnerEnv(t)
	path := fmt.Sprintf("/api/admin/student-config?user_id=%d", e.kid.ID)

	code, got := e.do("GET", path, nil, e.admin)
	if code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", code)
	}
	if got["depth"] != "小学" || got["tone"] != "Friendly" {
		t.Fatalf("defaults not materialized: %v", got)
	}

	code, _ = e.do("PUT", path, map[string]any{
		"user_id":        e.kid.ID,
		"depth":          "初中",
		"learning_style": "Verbal",
		"communication":  "Socratic",
		"tone":           "NotATone", // invalid: must normalize to Friendly
		"reasoning":      "Causal",
		"interests":      `["足球"]`,
		"strengths":      "[]",
		"weaknesses":     "[]",
	}, e.admin)
	if code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200", code)
	}
	code, got = e.do("GET", path, nil, e.admin)
	if got["depth"] != "初中" || got["learning_style"] != "Verbal" {
		t.Fatalf("update lost: %v", got)
	}
	if got["tone"] != "Friendly" {
		t.Fatalf("invalid tone must normalize to Friendly, got %v", got["tone"])
	}
	if got["interests"] != `["足球"]` {
		t.Fatalf("interests lost: %v", got["interests"])
	}

	// The kid cannot read the admin config route at all (403), the kid
	// self-read lands on the kid routes instead (见前端 api.ts).
	if code := e.doRaw("GET", "/api/admin/student-config", nil, e.kid); code != http.StatusForbidden {
		t.Fatalf("kid on admin route = %d, want 403", code)
	}
	// An admin without user_id gets a 400 (ambiguous target).
	if code := e.doRaw("GET", "/api/admin/student-config", nil, e.admin); code != http.StatusBadRequest {
		t.Fatalf("admin without user_id = %d, want 400", code)
	}
}

// TestLearnerProfileAggregation pins the profile payload: hero counts
// from the event stream, KC matrix weakest-first, the config, and the
// 14-bucket trend.
func TestLearnerProfileAggregation(t *testing.T) {
	e := setupLearnerEnv(t)
	ctx := context.Background()

	tagA, err := e.s.FindOrCreateKnowledgeTag(ctx, "判别式", "math", nil)
	if err != nil {
		t.Fatalf("tagA: %v", err)
	}
	tagB, err := e.s.FindOrCreateKnowledgeTag(ctx, "因式分解", "math", nil)
	if err != nil {
		t.Fatalf("tagB: %v", err)
	}
	rec := func(verb string, kc []int64, success bool) {
		ctxJSON, _ := json.Marshal(map[string]any{"kc_ids": kc})
		resultJSON, _ := json.Marshal(map[string]any{"success": success})
		if _, err := e.s.RecordLearnerEvent(ctx, store.LearnerEventInput{
			UserID: e.kid.ID, Verb: verb, ObjectType: model.EventObjectErrorItem,
			ObjectID: 1, ResultJSON: string(resultJSON), ContextJSON: string(ctxJSON),
		}); err != nil {
			t.Fatalf("event: %v", err)
		}
	}
	rec(model.EventVerbAnswered, []int64{tagA}, false)
	rec(model.EventVerbAnswered, []int64{tagA}, false)
	rec(model.EventVerbAnswered, []int64{tagA}, true)
	rec(model.EventVerbReviewed, []int64{tagB}, true)
	rec(model.EventVerbReviewed, []int64{tagB}, true)

	code, got := e.do("GET", fmt.Sprintf("/api/admin/learner-profile?user_id=%d", e.kid.ID), nil, e.admin)
	if code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", code)
	}
	hero, _ := got["hero"].(map[string]any)
	if hero == nil {
		t.Fatalf("hero missing: %v", got)
	}
	if hero["answers_total"].(float64) != 3 {
		t.Fatalf("answers_total = %v, want 3 (answered only, not reviewed)", hero["answers_total"])
	}
	if hero["weak_kc_count"].(float64) != 2 {
		t.Fatalf("weak_kc_count = %v, want 2 (0.4 and 0.75 both < 0.8)", hero["weak_kc_count"])
	}
	mastery, _ := got["kc_mastery"].([]any)
	if len(mastery) != 2 {
		t.Fatalf("kc_mastery rows = %d, want 2", len(mastery))
	}
	first, _ := mastery[0].(map[string]any)
	if first["name"] != "判别式" {
		t.Fatalf("weakest-first broken: first = %v", first["name"])
	}
	trend, _ := got["event_trend"].([]any)
	if len(trend) != 14 {
		t.Fatalf("trend buckets = %d, want 14", len(trend))
	}
	if _, ok := got["config"]; !ok {
		t.Fatal("config missing from profile payload")
	}
}

// TestKidAIFeedback pins the kid's deterministic feedback card: weak
// KCs surface by name; a fresh kid gets the no-data note.
func TestKidAIFeedback(t *testing.T) {
	e := setupLearnerEnv(t)
	code, got := e.do("GET", "/api/kid/ai-feedback", nil, e.kid)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	note, _ := got["generated_note"].(string)
	if !strings.Contains(note, "画像") {
		t.Fatalf("fresh-kid note should mention the profile: %q", note)
	}

	ctx := context.Background()
	tag, _ := e.s.FindOrCreateKnowledgeTag(ctx, "判别式", "math", nil)
	for i := 0; i < 6; i++ {
		success := i < 1 // 1/6 → weak
		ctxJSON, _ := json.Marshal(map[string]any{"kc_ids": []int64{tag}})
		resultJSON, _ := json.Marshal(map[string]any{"success": success})
		if _, err := e.s.RecordLearnerEvent(ctx, store.LearnerEventInput{
			UserID: e.kid.ID, Verb: model.EventVerbAnswered, ObjectType: model.EventObjectErrorItem,
			ObjectID: 1, ResultJSON: string(resultJSON), ContextJSON: string(ctxJSON),
		}); err != nil {
			t.Fatalf("event: %v", err)
		}
	}
	code, got = e.do("GET", "/api/kid/ai-feedback", nil, e.kid)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	weak, _ := got["weak_kcs"].([]any)
	if len(weak) != 1 {
		t.Fatalf("weak_kcs = %d, want 1", len(weak))
	}
	w, _ := weak[0].(map[string]any)
	if w["name"] != "判别式" {
		t.Fatalf("weak kc = %v, want 判别式", w["name"])
	}
}

// TestScaffoldLadder pins the Khanmigo ladder: four ascending stages,
// clamped at both ends, 404 on a foreign item.
func TestScaffoldLadder(t *testing.T) {
	e := setupLearnerEnv(t)
	ctx := context.Background()
	item := &model.ErrorItem{UserID: e.kid.ID, Subject: "math",
		QuestionText: "3x+6=18", Source: "期中卷", ErrorType: model.ErrorTypeConcept,
		MistakeStatus: model.MistakeWrongAttempt, MasteryLevel: model.MasteryNew}
	if err := e.s.CreateErrorItem(ctx, item); err != nil {
		t.Fatalf("CreateErrorItem: %v", err)
	}
	stageFor := func(attempt int) (float64, string) {
		code, got := e.do("POST", "/api/kid/scaffold", map[string]any{
			"error_item_id": item.ID, "attempt": attempt,
		}, e.kid)
		if code != http.StatusOK {
			t.Fatalf("scaffold status = %d, want 200", code)
		}
		return got["stage"].(float64), got["hint"].(string)
	}
	s1, h1 := stageFor(1)
	if s1 != 1 || h1 == "" {
		t.Fatalf("stage 1 = %v %q", s1, h1)
	}
	s2, _ := stageFor(2)
	s3, _ := stageFor(3)
	s4, h4 := stageFor(4)
	if s2 != 2 || s3 != 3 || s4 != 4 {
		t.Fatalf("ladder = %v %v %v, want 2 3 4", s2, s3, s4)
	}
	if h4 == h1 {
		t.Fatal("stage 4 hint must differ from stage 1")
	}
	if sLow, _ := stageFor(0); sLow != 1 {
		t.Fatalf("attempt 0 must clamp to 1, got %v", sLow)
	}
	if sHigh, _ := stageFor(99); sHigh != 4 {
		t.Fatalf("attempt 99 must clamp to 4, got %v", sHigh)
	}
	if code := e.doRaw("POST", "/api/kid/scaffold", map[string]any{
		"error_item_id": 99999, "attempt": 1,
	}, e.kid); code != http.StatusNotFound {
		t.Fatalf("missing item = %d, want 404", code)
	}
}

// TestAnsweredEventOnSubmitReviewResult pins the wiring: the kid's
// retry report appends an answered event carrying the item's KC ids,
// which rolls up into kc_mastery.
func TestAnsweredEventOnSubmitReviewResult(t *testing.T) {
	e := setupLearnerEnv(t)
	ctx := context.Background()
	tag, err := e.s.FindOrCreateKnowledgeTag(ctx, "一元一次方程", "math", nil)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	item := &model.ErrorItem{UserID: e.kid.ID, Subject: "math",
		QuestionText: "3x+6=18", Source: "期中卷", ErrorType: model.ErrorTypeConcept,
		MistakeStatus: model.MistakeWrongAttempt, MasteryLevel: model.MasteryNew}
	if err := e.s.CreateErrorItem(ctx, item); err != nil {
		t.Fatalf("CreateErrorItem: %v", err)
	}
	if err := e.s.AttachErrorItemTag(ctx, item.ID, tag); err != nil {
		t.Fatalf("AttachErrorItemTag: %v", err)
	}

	code, _ := e.do("POST", fmt.Sprintf("/api/error-items/%d/review-result", item.ID),
		map[string]any{"is_correct": true}, e.kid)
	if code != http.StatusOK {
		t.Fatalf("submit status = %d, want 200", code)
	}

	events, err := e.s.ListLearnerEvents(ctx, e.kid.ID, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("no learner events after retry: %v %d", err, len(events))
	}
	last := events[0]
	if last.Verb != model.EventVerbAnswered {
		t.Fatalf("verb = %s, want answered", last.Verb)
	}
	if !strings.Contains(last.ContextJSON, fmt.Sprintf("[%d]", tag)) {
		t.Fatalf("context missing kc id %d: %s", tag, last.ContextJSON)
	}
	mastery, err := e.s.ListKCMastery(ctx, e.kid.ID)
	if err != nil || len(mastery) != 1 {
		t.Fatalf("rollup after answered: %v %d", err, len(mastery))
	}
	if mastery[0].Mastery < 0.66 || mastery[0].Mastery > 0.67 {
		t.Fatalf("mastery = %.3f, want ~0.667 ((1+1)/(1+2))", mastery[0].Mastery)
	}
}

// TestReviewEventOnGrade pins the reviewed event on the FSRS grading
// path (Good → success=true, Again → success=false).
func TestReviewEventOnGrade(t *testing.T) {
	e := setupLearnerEnv(t)
	ctx := context.Background()
	item := &model.ErrorItem{UserID: e.kid.ID, Subject: "english",
		QuestionText: "necessary", Source: "听写", ErrorType: model.ErrorTypeConcept,
		MistakeStatus: model.MistakeWrongAttempt, MasteryLevel: model.MasteryNew}
	if err := e.s.CreateErrorItem(ctx, item); err != nil {
		t.Fatalf("CreateErrorItem: %v", err)
	}
	rev := &model.ReviewItem{UserID: e.kid.ID, SourceType: model.ReviewSourceErrorItem,
		SourceID: item.ID, FrontText: item.QuestionText, BackText: "necessary",
		Subject: "english", Due: time.Now(), State: model.ReviewStateNew}
	if err := e.s.CreateReviewItem(ctx, rev, false); err != nil {
		t.Fatalf("CreateReviewItem: %v", err)
	}

	code, _ := e.do("POST", fmt.Sprintf("/api/review/%d/grade", rev.ID),
		map[string]any{"rating": "Good"}, e.kid)
	if code != http.StatusOK {
		t.Fatalf("grade status = %d, want 200", code)
	}
	events, err := e.s.ListLearnerEvents(ctx, e.kid.ID, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("no events after grade: %v %d", err, len(events))
	}
	last := events[0]
	if last.Verb != model.EventVerbReviewed {
		t.Fatalf("verb = %s, want reviewed", last.Verb)
	}
	if !strings.Contains(last.ResultJSON, `"success":true`) {
		t.Fatalf("Good must be success=true: %s", last.ResultJSON)
	}
}
