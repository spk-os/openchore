package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// paperCandidatesReplies is a fake OpenAI-compatible server that replies with
// the given contents in order (one per request) and records every request.
type paperCandidatesReplies struct {
	mu        sync.Mutex
	replies   []string
	requests  []decodedRequest
	rawBodies []map[string]any
}

// decodedRequest is a recorded chat request: both the decoded multimodal
// parts (via the package test helper) and the raw JSON body.
type decodedRequest struct {
	parts partList
	body  map[string]any
}

func (d decodedRequest) partsFn() []part { return d.parts.parts() }

// partList adapts a raw message content to the decoder helper's returns.
type partList struct{ p []part }

func (p partList) parts() []part { return p.p }

func newPaperJudgeFake(t *testing.T, replies ...string) (*httptest.Server, *paperCandidatesReplies) {
	t.Helper()
	rec := &paperCandidatesReplies{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rawBody map[string]any
		_ = json.NewDecoder(r.Body).Decode(&rawBody)
		rec.mu.Lock()
		idx := len(rec.requests)
		rec.requests = append(rec.requests, decodedRequest{parts: partList{replayParts(rawBody)}, body: rawBody})
		rec.mu.Unlock()
		reply := rec.replies[len(rec.replies)-1]
		if idx < len(rec.replies) {
			reply = rec.replies[idx]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func (r *paperCandidatesReplies) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// replayParts re-decodes the first message's content parts from the raw
// body (either a plain string or a list of multimodal parts).
func replayParts(body map[string]any) []part {
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		return nil
	}
	msg, _ := msgs[0].(map[string]any)
	switch c := msg["content"].(type) {
	case string:
		return []part{{"type": "text", "text": c}}
	case []any:
		out := make([]part, 0, len(c))
		for _, item := range c {
			if m, ok := item.(map[string]any); ok {
				out = append(out, part(m))
			}
		}
		return out
	}
	return nil
}

func (r *paperCandidatesReplies) prompt(i int) string {
	parts := r.requests[i].partsFn()
	if len(parts) == 0 {
		return ""
	}
	s, _ := parts[0]["text"].(string)
	return s
}

func (r *paperCandidatesReplies) imageURL(i int) string {
	parts := r.requests[i].partsFn()
	if len(parts) < 2 {
		return ""
	}
	img, _ := parts[1]["image_url"].(map[string]any)
	s, _ := img["url"].(string)
	return s
}

func writePaperJPEG(t *testing.T) string {
	t.Helper()
	photo := filepath.Join(t.TempDir(), "page.jpg")
	if err := os.WriteFile(photo, []byte("\xff\xd8\xff\xe0jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	return photo
}

func judgeMath(t *testing.T, srv *httptest.Server, candidates []KnowledgeCandidate) (*QuestionJudgement, error) {
	t.Helper()
	c := New(srv.URL+"/v1", "", "fake-model")
	return c.JudgeQuestionWithCandidates(context.Background(), "math", writePaperJPEG(t), "3", "解方程 $x^2=4$", "重点看第三题", candidates)
}

var mathCandidates = []KnowledgeCandidate{
	{ExternalRef: "M-101", Name: "一元二次方程", Subject: "math"},
	{ExternalRef: "M-102", Name: "因式分解", Subject: "math"},
	{ExternalRef: "P-201", Name: "欧姆定律", Subject: "physics"},
}

const judgementBody = `"question_text": "解方程 $x^2=4$", "attempted": true, "is_wrong": true, "wrong_answer_text": "2", "answer_text": "$x=\\pm 2$", "analysis": "开平方", "mistake_analysis": "漏解", "error_type": "concept", "knowledge_points": [" 一元二次方程 ", ""], "difficulty": "easy", "confidence": 1.5`

// frozenLegacyJudgePrompt 是 candidates 特性之前（HEAD:internal/llm/paper.go
// 的 JudgeQuestion）在 judgeMath 固定入参下发出的完整 prompt，逐字节冻结。
// 任何不注入候选白名单的路径都必须与它完全相同。
const frozenLegacyJudgePrompt = `You are diagnosing ONE question on a photographed exam/homework page (subject: math).
The parent's extra requirement for this analysis (家长的补充要求 — follow it when segmenting/judging, e.g. which question to prioritize):
重点看第三题
The page shows the student's graded work: handwritten answers, cross-outs and marks (✗, red pen corrections) may be present.

The question to judge: #3
解方程 $x^2=4$

Decide:
- attempted: did the student write an answer attempt for THIS question? Blank/no writing = false.
- is_wrong: is this question a mistake? true when the student's answer is wrong, partially wrong, or the question was required and left blank. false when the answer is correct, or the question is clearly optional and untouched with no marking against it.
- wrong_answer_text: what the student actually wrote (their wrong or partial answer). LaTeX inline ($...$). Empty when not attempted.
- answer_text: the correct answer, worked out by you. LaTeX inline ($...$).
- analysis: how to solve the question, step by step, concise. Chinese.
- mistake_analysis: WHY the student likely lost this question — one or two sentences pointing at the specific step or misconception (e.g. "移项时忘记变号"). Chinese.
- error_type: the dominant cause — "calculation" (会做但算错/粗心), "concept" (概念不清/定义混淆), "reading" (审题错/看错条件), "method" (没有思路/方法不会). Choose exactly one.
- knowledge_points: 1-4 short knowledge-point names this question tests, Chinese (e.g. "一元二次方程", "分式化简").
- difficulty: "easy", "medium" or "hard" for a student at this paper's level.
- confidence: 0.0-1.0 — how sure you are of this whole judgement. Low when the photo is blurry, cropped or the writing is illegible.

Reply with only JSON matching the schema.`

const (
	paperCandidatesIntro = "\nKnowledge-point candidates (taxonomy knowledge points this paper's subject may test), as compact JSON [external_ref, name] pairs:\n" +
		`[["M-101","一元二次方程"],["M-102","因式分解"]]` + "\n"
	paperRefsBullet = "\n- knowledge_point_refs: pick 1-4 external_ref values from the candidates above, copied VERBATIM. Do NOT invent, modify or combine refs; unknown refs are discarded. Choose the refs this question actually tests (most specific first)."
)

const (
	paperArbitraryRefs = `["P-201", "INVENTED/自由引用", "  ", "M-999", "P-201"]`
	paperFreePoints    = `["  自由知识点  ", "", "M-101"]`
)

func paperReply(refsJSON string) string {
	return `{"question_text":"q","attempted":true,"is_wrong":false,"confidence":0.5,` +
		`"knowledge_points":` + paperFreePoints + `,"knowledge_point_refs":` + refsJSON + `}`
}

func paperArbitraryReply() string { return paperReply(paperArbitraryRefs) }

// wantPaperCandidatePrompt 由冻结的旧 prompt 加上唯一的 intro/bullet 组合出
// 合法候选 prompt；空格、bullet 文案或插入位置变化都会导致断言失败。
func wantPaperCandidatePrompt(t *testing.T) string {
	t.Helper()
	const questionAnchor = "解方程 $x^2=4$\n\nDecide:"
	const kpLine = `- knowledge_points: 1-4 short knowledge-point names this question tests, Chinese (e.g. "一元二次方程", "分式化简").`
	if strings.Count(frozenLegacyJudgePrompt, questionAnchor) != 1 || strings.Count(frozenLegacyJudgePrompt, kpLine) != 1 {
		t.Fatal("frozen prompt anchors changed; update the frozen prompt deliberately")
	}
	p := strings.Replace(frozenLegacyJudgePrompt, questionAnchor,
		"解方程 $x^2=4$\n"+paperCandidatesIntro+"\nDecide:", 1)
	return strings.Replace(p, kpLine, kpLine+paperRefsBullet, 1)
}

// legacyResponseFormat is the HEAD-era judgeQuestionSchema as Chat serialises
// it into response_format. There is NO knowledge_point_refs property: the
// no-candidate path must send exactly this, byte-for-byte.
func legacyResponseFormat() map[string]any {
	props := map[string]any{
		"question_text":     map[string]any{"type": "string"},
		"attempted":         map[string]any{"type": "boolean"},
		"is_wrong":          map[string]any{"type": "boolean"},
		"wrong_answer_text": map[string]any{"type": "string"},
		"answer_text":       map[string]any{"type": "string"},
		"analysis":          map[string]any{"type": "string"},
		"mistake_analysis":  map[string]any{"type": "string"},
		"error_type":        map[string]any{"type": "string"},
		"knowledge_points":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"difficulty":        map[string]any{"type": "string"},
		"confidence":        map[string]any{"type": "number"},
	}
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "question_judgement",
			"strict": true,
			"schema": map[string]any{
				"type":                 "object",
				"properties":           props,
				"required":             []any{"question_text", "attempted", "is_wrong", "confidence"},
				"additionalProperties": false,
			},
		},
	}
}

func capturedResponseFormat(t *testing.T, rec *paperCandidatesReplies, i int) map[string]any {
	t.Helper()
	rf, ok := rec.requests[i].body["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("request %d has no response_format: %v", i, rec.requests[i].body)
	}
	return rf
}

func schemaProps(t *testing.T, rec *paperCandidatesReplies, i int) map[string]any {
	t.Helper()
	js, ok := capturedResponseFormat(t, rec, i)["json_schema"].(map[string]any)
	if !ok {
		t.Fatalf("request %d has no json_schema", i)
	}
	root, ok := js["schema"].(map[string]any)
	if !ok {
		t.Fatalf("request %d has no schema", i)
	}
	props, ok := root["properties"].(map[string]any)
	if !ok {
		t.Fatalf("request %d has no properties", i)
	}
	return props
}

// assertLegacySchema pins the no-candidate path to the HEAD schema: no
// knowledge_point_refs property anywhere, and every other byte identical.
func assertLegacySchema(t *testing.T, rec *paperCandidatesReplies, i int, label string) {
	t.Helper()
	got, err := json.Marshal(capturedResponseFormat(t, rec, i))
	if err != nil {
		t.Fatalf("%s: marshaling response_format: %v", label, err)
	}
	want, err := json.Marshal(legacyResponseFormat())
	if err != nil {
		t.Fatalf("%s: marshaling frozen response_format: %v", label, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s: response_format is not byte-identical to HEAD\ngot:  %s\nwant: %s", label, got, want)
	}
	if _, ok := schemaProps(t, rec, i)["knowledge_point_refs"]; ok {
		t.Errorf("%s: legacy schema must NOT declare knowledge_point_refs", label)
	}
}

// assertCandidatesSchema pins the non-empty-candidate path to the derived
// schema: the only difference from HEAD is the knowledge_point_refs property.
func assertCandidatesSchema(t *testing.T, rec *paperCandidatesReplies, i int, label string) {
	t.Helper()
	props := schemaProps(t, rec, i)
	refProp, ok := props["knowledge_point_refs"].(map[string]any)
	if !ok || refProp["type"] != "array" || refProp["items"].(map[string]any)["type"] != "string" {
		t.Fatalf("%s: schema knowledge_point_refs must be a string array, got %v", label, props["knowledge_point_refs"])
	}
	if got, want := len(props), len(legacyResponseFormat()["json_schema"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any))+1; got != want {
		t.Errorf("%s: candidates schema must add exactly one property, got %d want %d", label, got, want)
	}
	for k, v := range legacyResponseFormat()["json_schema"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any) {
		if fmt.Sprint(props[k]) != fmt.Sprint(v) {
			t.Errorf("%s: HEAD property %q changed: got %v want %v", label, k, props[k], v)
		}
	}
}

// assertFrozenLegacyPromptBytes asserts ONLY the prompt bytes: byte-identical
// to the frozen legacy prompt and free of any pseudo-whitelist injection.
// The response schema is deliberately NOT checked here: candidate paths
// (including ones whose candidates all normalize away) keep this exact prompt
// but send the derived candidates schema, so pinning the legacy schema here
// would contradict assertCandidatesSchema.
func assertFrozenLegacyPromptBytes(t *testing.T, rec *paperCandidatesReplies, i int, label string) {
	t.Helper()
	got := rec.prompt(i)
	if got != frozenLegacyJudgePrompt {
		t.Errorf("%s prompt is not byte-identical to the frozen legacy prompt (got %d bytes, want %d):\n%q", label, len(got), len(frozenLegacyJudgePrompt), got)
	}
	for _, banned := range []string{"Knowledge-point candidates", "knowledge_point_refs", "VERBATIM", "P-201", "欧姆定律"} {
		if strings.Contains(got, banned) {
			t.Errorf("%s prompt injected %q (no pseudo-whitelist is allowed)", label, banned)
		}
	}
}

// assertFrozenLegacyPrompt pins the FULL legacy contract and is reserved for
// the genuine legacy paths (nil/empty candidates, the JudgeQuestion wrapper):
// the frozen prompt bytes AND the byte-identical HEAD response schema.
func assertFrozenLegacyPrompt(t *testing.T, rec *paperCandidatesReplies, i int, label string) {
	t.Helper()
	assertFrozenLegacyPromptBytes(t, rec, i, label)
	assertLegacySchema(t, rec, i, label)
}

func assertPaperRefsEmptyWithEmptyAllowList(t *testing.T, j *QuestionJudgement, label string) {
	t.Helper()
	if j == nil {
		t.Fatalf("%s: nil judgement", label)
	}
	if len(j.KnowledgePointRefs) != 0 {
		t.Fatalf("%s: non-empty candidates but zero same-subject refs must return [], got %v", label, j.KnowledgePointRefs)
	}
	if got := strings.Join(j.KnowledgePoints, ","); got != "自由知识点,M-101" {
		t.Errorf("%s: only refs are restricted, free-text knowledge_points = %v", label, j.KnowledgePoints)
	}
}

// assertPaperLegacyNoBoundRefs: free-text knowledge_points stays advisory and
// unchanged on the legacy path, but refs are NEVER bound without a
// caller-supplied candidate set — even when the model emits arbitrary refs.
func assertPaperLegacyNoBoundRefs(t *testing.T, j *QuestionJudgement, label string) {
	t.Helper()
	if j == nil {
		t.Fatalf("%s: nil judgement", label)
	}
	if j.KnowledgePointRefs == nil || len(j.KnowledgePointRefs) != 0 {
		t.Errorf("%s: legacy path must NOT trust free-text refs, got %v, want []", label, j.KnowledgePointRefs)
	}
	if got := strings.Join(j.KnowledgePoints, ","); got != "自由知识点,M-101" {
		t.Errorf("%s: legacy free-text knowledge_points changed, got %v", label, j.KnowledgePoints)
	}
}

// legacyJudgePrompt builds the pre-candidate prompt through the public
// no-candidate entry point and returns it together with the result.
func TestPaperCandidatesLegacyPromptUnchanged(t *testing.T) {
	srv, rec := newPaperJudgeFake(t, `{`+judgementBody+`}`)
	j, err := judgeMath(t, srv, nil)
	if err != nil {
		t.Fatalf("JudgeQuestionWithCandidates(nil): %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("expected 1 request, got %d", rec.count())
	}
	prompt := rec.prompt(0)
	for _, banned := range []string{"external_ref", "candidates", "VERBATIM", "knowledge_point_refs:"} {
		if strings.Contains(prompt, banned) {
			t.Errorf("legacy prompt must not mention %q", banned)
		}
	}
	if !strings.Contains(prompt, "重点看第三题") || !strings.Contains(prompt, "knowledge_points: 1-4 short knowledge-point names") {
		t.Errorf("legacy prompt lost its instructions/knowledge_points line")
	}
	if len(j.KnowledgePointRefs) != 0 {
		t.Errorf("legacy path must leave refs empty, got %v", j.KnowledgePointRefs)
	}
	if j.Confidence != 1 || j.ErrorType != "concept" || len(j.KnowledgePoints) != 1 || j.KnowledgePoints[0] != "一元二次方程" {
		t.Errorf("legacy parse behaviour changed: %+v", j)
	}

	// JudgeQuestion (the wrapper) must be byte-identical to the nil-candidate path.
	srv2, rec2 := newPaperJudgeFake(t, `{`+judgementBody+`}`)
	c := New(srv2.URL+"/v1", "", "fake-model")
	if _, err := c.JudgeQuestion(context.Background(), "math", writePaperJPEG(t), "3", "解方程 $x^2=4$", "重点看第三题"); err != nil {
		t.Fatalf("JudgeQuestion: %v", err)
	}
	if got := rec2.prompt(0); got != prompt {
		t.Errorf("JudgeQuestion prompt diverged from nil-candidate path")
	}
	assertLegacySchema(t, rec, 0, "JudgeQuestionWithCandidates(nil)")
	assertLegacySchema(t, rec2, 0, "JudgeQuestion wrapper")
}

func TestPaperCandidatesPromptInjectsOnlySubjectRefsAndForbidsInvention(t *testing.T) {
	srv, rec := newPaperJudgeFake(t, `{`+judgementBody+`}`)
	// Empty candidates are treated exactly like nil.
	if _, err := judgeMath(t, srv, []KnowledgeCandidate{}); err != nil {
		t.Fatalf("empty candidates: %v", err)
	}
	if p := rec.prompt(0); strings.Contains(p, "Knowledge-point candidates") {
		t.Errorf("empty candidate slice must not inject a whitelist")
	}
	assertLegacySchema(t, rec, 0, "empty candidate slice")

	if _, err := judgeMath(t, srv, mathCandidates); err != nil {
		t.Fatalf("with candidates: %v", err)
	}
	p := rec.prompt(1)
	for _, want := range []string{"M-101", "M-102", "一元二次方程", "因式分解", "[[\"M-101\",\"一元二次方程\"]", "VERBATIM", "Do NOT invent", "knowledge_point_refs: pick 1-4"} {
		if !strings.Contains(p, want) {
			t.Errorf("candidate prompt missing %q", want)
		}
	}
	if strings.Contains(p, "P-201") || strings.Contains(p, "欧姆定律") {
		t.Errorf("prompt leaked other-subject candidates")
	}
	if !strings.Contains(p, "subject: math") || !strings.Contains(p, "重点看第三题") || !strings.Contains(p, "#3") {
		t.Errorf("candidate prompt lost subject/instructions/number")
	}
	// Image part still rides along with the text part.
	if img := rec.imageURL(1); !strings.HasPrefix(img, "data:image/jpeg;base64,") {
		t.Errorf("expected inline JPEG in candidate path, got %.40q", img)
	}
	if rec.requests[1].partsFn()[0]["type"] != "text" {
		t.Errorf("first part must be the text part")
	}

	// The candidate path sends the derived schema: HEAD properties plus
	// exactly one new knowledge_point_refs string-array property.
	assertCandidatesSchema(t, rec, 1, "with candidates")
}

func TestPaperCandidatesRefsTrimDedupeOrderAndSubjectFilter(t *testing.T) {
	reply := `{"question_text": "q", "attempted": true, "is_wrong": true, "confidence": 0.8,
		"knowledge_point_refs": ["  M-102 ", "M-999", "P-201", "", "M-101", "M-102", "   "]}`
	srv, _ := newPaperJudgeFake(t, reply)
	j, err := judgeMath(t, srv, mathCandidates)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	want := []string{"M-102", "M-101"}
	if strings.Join(j.KnowledgePointRefs, ",") != strings.Join(want, ",") {
		t.Fatalf("refs = %v, want %v (trim+dedupe+keep order+drop unknown/other-subject)", j.KnowledgePointRefs, want)
	}
}

func TestPaperCandidatesAllUnknownRefsYieldEmpty(t *testing.T) {
	srv, _ := newPaperJudgeFake(t, `{"question_text": "q", "attempted": false, "is_wrong": false, "confidence": 0.4,
		"knowledge_point_refs": ["FAKE-1", "physics:P-201", ""]}`)
	j, err := judgeMath(t, srv, mathCandidates)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if len(j.KnowledgePointRefs) != 0 {
		t.Fatalf("expected no refs, got %v", j.KnowledgePointRefs)
	}
}

func TestPaperCandidatesNoRefsFieldIsNil(t *testing.T) {
	srv, _ := newPaperJudgeFake(t, `{"question_text": "q", "attempted": true, "is_wrong": false, "confidence": 0.5}`)
	j, err := judgeMath(t, srv, mathCandidates)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if len(j.KnowledgePointRefs) != 0 {
		t.Fatalf("expected empty refs, got %v", j.KnowledgePointRefs)
	}
}

func TestPaperCandidatesAllInvalidCandidatesSkipWhitelistNotice(t *testing.T) {
	// 非空 candidates 但没有同学科有效项时，限制仍开启且 allow-list 为空：
	// 模型任意 refs（首答 / 重试）最终必须 []，prompt 不得注入伪白名单，
	// 必须与冻结的旧 prompt 逐字节一致；nil / [] 仍走自由文本路径。
	invalid := []KnowledgeCandidate{
		{ExternalRef: "P-201", Name: "欧姆定律", Subject: "physics"}, // 跨学科
		{ExternalRef: "", Name: "无编号", Subject: "math"},          // 空 ref
		{ExternalRef: "M-NO-NAME", Subject: "math"},              // 空 name
	}

	// 模型任意 refs（含空白、重复、跨学科、凭空捏造、嵌套重名）都必须被空
	// allow-list 清空；尾后 prose 与首个对象之外的伪字段也不得绕过解析。
	for _, tc := range []struct {
		name  string
		reply string
	}{
		{"arbitrary refs", paperArbitraryReply()},
		{"prose-wrapped arbitrary refs", "Sure! " + paperArbitraryReply() + " hope this helps"},
		{"duplicate suffix refs", paperReply(`["P-201", "  M-101  ", "P-201", "M-999", "", "INVENTED/自由引用"]`)},
		{"nested refs field ignored", `{"question_text":"q","attempted":true,"confidence":0.5,"knowledge_points":` + paperFreePoints + `,"knowledge_point_refs":["M-101"],"extra":{"knowledge_point_refs":["M-999"]}}`},
	} {
		t.Run(tc.name+" yields empty refs", func(t *testing.T) {
			srv, rec := newPaperJudgeFake(t, tc.reply)
			j, err := judgeMath(t, srv, invalid)
			if err != nil {
				t.Fatalf("judge: %v", err)
			}
			if rec.count() != 1 {
				t.Fatalf("expected 1 request, got %d", rec.count())
			}
			assertFrozenLegacyPromptBytes(t, rec, 0, tc.name)
			assertCandidatesSchema(t, rec, 0, tc.name+" (zero valid candidates)")
			assertPaperRefsEmptyWithEmptyAllowList(t, j, tc.name)
		})
	}

	t.Run("first reply arbitrary refs", func(t *testing.T) {
		srv, rec := newPaperJudgeFake(t, paperArbitraryReply())
		j, err := judgeMath(t, srv, invalid)
		if err != nil {
			t.Fatalf("judge: %v", err)
		}
		if rec.count() != 1 {
			t.Fatalf("expected 1 request, got %d", rec.count())
		}
		assertFrozenLegacyPromptBytes(t, rec, 0, "first reply")
		assertCandidatesSchema(t, rec, 0, "first reply (zero valid candidates)")
		assertPaperRefsEmptyWithEmptyAllowList(t, j, "first reply")
	})

	t.Run("retry arbitrary refs", func(t *testing.T) {
		srv, rec := newPaperJudgeFake(t, `not json at all`, paperArbitraryReply())
		j, err := judgeMath(t, srv, invalid)
		if err != nil {
			t.Fatalf("judge after retry: %v", err)
		}
		if rec.count() != 2 {
			t.Fatalf("expected 2 requests (one retry), got %d", rec.count())
		}
		for i, label := range []string{"retry request 1", "retry request 2"} {
			assertFrozenLegacyPromptBytes(t, rec, i, label)
			assertCandidatesSchema(t, rec, i, label+" (zero valid candidates)")
		}
		assertPaperRefsEmptyWithEmptyAllowList(t, j, "retry")
	})

	for _, tc := range []struct {
		name       string
		candidates []KnowledgeCandidate
	}{
		{"nil candidates", nil},
		{"empty candidate slice", []KnowledgeCandidate{}},
	} {
		t.Run(tc.name+" stays free text", func(t *testing.T) {
			srv, rec := newPaperJudgeFake(t, paperArbitraryReply())
			j, err := judgeMath(t, srv, tc.candidates)
			if err != nil {
				t.Fatalf("judge: %v", err)
			}
			if rec.count() != 1 {
				t.Fatalf("expected 1 request, got %d", rec.count())
			}
			assertFrozenLegacyPrompt(t, rec, 0, tc.name)
			assertPaperLegacyNoBoundRefs(t, j, tc.name)
		})
	}

	t.Run("JudgeQuestion wrapper prompt frozen", func(t *testing.T) {
		srv, rec := newPaperJudgeFake(t, paperArbitraryReply())
		c := New(srv.URL+"/v1", "", "fake-model")
		if _, err := c.JudgeQuestion(context.Background(), "math", writePaperJPEG(t), "3", "解方程 $x^2=4$", "重点看第三题"); err != nil {
			t.Fatalf("JudgeQuestion: %v", err)
		}
		if rec.count() != 1 {
			t.Fatalf("expected 1 request, got %d", rec.count())
		}
		assertFrozenLegacyPrompt(t, rec, 0, "JudgeQuestion wrapper")
	})

	t.Run("valid candidates prompt bullet format", func(t *testing.T) {
		srv, rec := newPaperJudgeFake(t, paperReply(`["  M-101 ", "M-999", "M-101"]`))
		j, err := judgeMath(t, srv, mathCandidates)
		if err != nil {
			t.Fatalf("judge: %v", err)
		}
		if rec.count() != 1 {
			t.Fatalf("expected 1 request, got %d", rec.count())
		}
		assertCandidatesSchema(t, rec, 0, "valid candidates")
		got := rec.prompt(0)
		if want := wantPaperCandidatePrompt(t); got != want {
			t.Errorf("candidate prompt differs from frozen legacy + intro + bullet\ngot:  %q\nwant: %q", got, want)
		}
		var bullet string
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, "- knowledge_point_refs:") {
				bullet = line
				break
			}
		}
		if wantBullet := strings.TrimPrefix(paperRefsBullet, "\n"); bullet != wantBullet {
			t.Errorf("knowledge_point_refs bullet = %q, want %q", bullet, wantBullet)
		}
		kpIdx := strings.Index(got, "- knowledge_points:")
		refIdx := strings.Index(got, "- knowledge_point_refs:")
		diffIdx := strings.Index(got, "- difficulty:")
		qIdx := strings.Index(got, "解方程 $x^2=4$")
		introIdx := strings.Index(got, "Knowledge-point candidates")
		decideIdx := strings.Index(got, "Decide:")
		if qIdx < 0 || introIdx <= qIdx || decideIdx <= introIdx {
			t.Errorf("candidate block must sit between the question text and Decide:, got q=%d intro=%d decide=%d", qIdx, introIdx, decideIdx)
		}
		if kpIdx < 0 || refIdx <= kpIdx || diffIdx <= refIdx {
			t.Errorf("refs bullet must sit between knowledge_points and difficulty, got kp=%d refs=%d difficulty=%d", kpIdx, refIdx, diffIdx)
		}
		if strings.Contains(got, "P-201") || strings.Contains(got, "欧姆定律") {
			t.Errorf("other-subject candidate leaked into the prompt")
		}
		if gotRefs := strings.Join(j.KnowledgePointRefs, ","); gotRefs != "M-101" {
			t.Errorf("valid candidates must filter arbitrary refs down to the allow-list, got %v", j.KnowledgePointRefs)
		}
	})
}

func TestPaperCandidatesRetryFiltersRefs(t *testing.T) {
	srv, rec := newPaperJudgeFake(
		t,
		`not json at all`,
		`{"question_text": "q", "attempted": true, "is_wrong": true, "confidence": 0.7,
		  "knowledge_point_refs": ["M-101", "UNKNOWN", "M-101"]}`,
	)
	j, err := judgeMath(t, srv, mathCandidates)
	if err != nil {
		t.Fatalf("judge after retry: %v", err)
	}
	if rec.count() != 2 {
		t.Fatalf("expected retry, got %d requests", rec.count())
	}
	if !strings.Contains(rec.prompt(1), "M-101") {
		t.Errorf("retry request must still carry the candidate block")
	}
	if strings.Join(j.KnowledgePointRefs, ",") != "M-101" {
		t.Fatalf("retry refs not filtered: %v", j.KnowledgePointRefs)
	}
}

func TestPaperCandidatesProseWrappedReplyParsed(t *testing.T) {
	srv, _ := newPaperJudgeFake(t, "Sure! {\"question_text\": \"q\", \"attempted\": true, \"is_wrong\": true, \"confidence\": 0.6, \"knowledge_point_refs\": [\" M-102 \"]} hope this helps")
	j, err := judgeMath(t, srv, mathCandidates)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if strings.Join(j.KnowledgePointRefs, ",") != "M-102" {
		t.Fatalf("prose-wrapped refs = %v", j.KnowledgePointRefs)
	}
}

func TestPaperCandidatesFilterHelper(t *testing.T) {
	// nil allowed => clean only (parseQuestionJudgement). A NON-NIL even
	// empty allowed slice is a strict allow-list that drops everything.
	if got := filterKnowledgePointRefs([]string{" a ", "a", "", "b"}, nil); strings.Join(got, ",") != "a,b" {
		t.Errorf("nil allowed cleans only: %v", got)
	}
	if got := filterKnowledgePointRefs(nil, []string{"a"}); got == nil || len(got) != 0 {
		t.Errorf("nil raw must yield a non-nil empty result: %v", got)
	}
	if got := filterKnowledgePointRefs([]string{"M-101", "INVENTED"}, []string{}); len(got) != 0 {
		t.Errorf("empty non-nil allow-list must drop every ref: %v", got)
	}
	if got := filterKnowledgePointRefs([]string{"  M-101 ", "M-999"}, []string{"M-101"}); strings.Join(got, ",") != "M-101" {
		t.Errorf("strict allow-list: %v", got)
	}
	// Case-sensitive, exact matching: only VERBATIM refs survive.
	if got := filterKnowledgePointRefs([]string{"m-101", "M-101", "M-101 "}, []string{"M-101"}); strings.Join(got, ",") != "M-101" {
		t.Errorf("case-insensitive/normalised matching must NOT be introduced: %v", got)
	}

	valid := normalizeCandidates("math", mathCandidates)
	if got := strings.Join(candidateRefs(valid), ","); got != "M-101,M-102" {
		t.Errorf("candidateRefs: %v", got)
	}
	if got := normalizeCandidates("math", []KnowledgeCandidate{{ExternalRef: "M-1", Subject: "math"}}); len(got) != 0 {
		t.Errorf("nameless candidate must not be allowed: %v", got)
	}
	if got := normalizeCandidates("math", []KnowledgeCandidate{
		{ExternalRef: "M-1", Name: "x", Subject: "math"},
		{ExternalRef: "M-1", Name: "x", Subject: "math"},
	}); len(got) != 1 {
		t.Errorf("duplicate candidates must dedupe: %v", got)
	}
	if got := candidateBlock(valid); strings.Contains(got, "P-201") || !strings.Contains(got, "M-101") {
		t.Errorf("candidateBlock: %s", got)
	}

	// 前 reviewer 的回归：第一条同 ref 候选 name 为空、后续 name 非空时，
	// 必须合并出完整候选（旧 keep-first 预消重会吞掉它并清空 allow-list）。
	merged := normalizeCandidates("math", []KnowledgeCandidate{
		{ExternalRef: "M-101", Name: "", Subject: "math"},
		{ExternalRef: "M-101", Name: "一元二次方程", Subject: "math"},
		{ExternalRef: "M-102", Name: "因式分解", Subject: "math"},
	})
	if len(merged) != 2 || merged[0].ExternalRef != "M-101" || merged[0].Name != "一元二次方程" || merged[1].ExternalRef != "M-102" {
		t.Fatalf("empty-name-first duplicate must merge the later name: %+v", merged)
	}
	if got := filterKnowledgePointRefs([]string{"M-101"}, candidateRefs(merged)); strings.Join(got, ",") != "M-101" {
		t.Errorf("merged candidate must be bindable: %v", got)
	}
	// 同一 ref 的所有行都无 name 时仍必须整条丢弃。
	if got := normalizeCandidates("math", []KnowledgeCandidate{
		{ExternalRef: "M-101", Subject: "math"},
		{ExternalRef: "M-101", Name: "  ", Subject: "math"},
	}); len(got) != 0 {
		t.Errorf("all-names-empty ref must be dropped: %+v", got)
	}
	// ref/name 空白 trim 后端到端仍可绑定。
	trimmed := normalizeCandidates("math", []KnowledgeCandidate{{ExternalRef: "  M-101  ", Name: " 一元二次方程 ", Subject: "math"}})
	if got := candidateBlock(trimmed); got != `[["M-101","一元二次方程"]]` {
		t.Errorf("normalized block must carry trimmed ref/name: %s", got)
	}
}

// normalizedRefNames extracts the [external_ref, name] pairs rendered in the
// candidate block of a captured prompt, for bounded/truncation assertions.
func normalizedRefNames(t *testing.T, prompt string) [][2]string {
	t.Helper()
	const marker = "as compact JSON [external_ref, name] pairs:\n"
	start := strings.Index(prompt, marker)
	if start < 0 {
		t.Fatalf("candidate block missing from prompt")
	}
	rest := prompt[start+len(marker):]
	end := strings.Index(rest, "\n")
	if end < 0 {
		t.Fatalf("candidate block has no terminating newline")
	}
	var rows [][2]string
	if err := json.Unmarshal([]byte(rest[:end]), &rows); err != nil {
		t.Fatalf("candidate block is not [ref, name] JSON: %v", err)
	}
	return rows
}

func TestPaperCandidatesCaseSensitiveExactMatch(t *testing.T) {
	srv, rec := newPaperJudgeFake(t, paperReply(`["m-101", "M-101", "  M-102  ", "M-102", "M-101"]`))
	j, err := judgeMath(t, srv, mathCandidates)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	assertCandidatesSchema(t, rec, 0, "case sensitive")
	if got := strings.Join(j.KnowledgePointRefs, ","); got != "M-101,M-102" {
		t.Errorf("only exact-case VERBATIM refs survive (deduped, in order), got %v", j.KnowledgePointRefs)
	}
}

// TestPaperCandidatesDeduplicateEmptyNameFirst is the end-to-end version of the
// former reviewer's question: an empty-name first duplicate must not discard
// the later complete candidate, so the prompt AND the allow-list both carry
// M-101 and the model's M-101 survives.
func TestPaperCandidatesDeduplicateEmptyNameFirst(t *testing.T) {
	candidates := []KnowledgeCandidate{
		{ExternalRef: "M-101", Name: "", Subject: "math"},
		{ExternalRef: "M-101", Name: "一元二次方程", Subject: "math"},
		{ExternalRef: "M-102", Name: "因式分解", Subject: "physics"}, // 跨学科拒绝
		{ExternalRef: "M-103", Name: "", Subject: "math"},        // 全空 name 拒绝
	}
	srv, rec := newPaperJudgeFake(t, paperReply(`["M-101", "M-102", "M-103", "INVENTED"]`))
	j, err := judgeMath(t, srv, candidates)
	if err != nil {
		t.Fatalf("judge: %v", err)
	}
	if got := strings.Join(j.KnowledgePointRefs, ","); got != "M-101" {
		t.Fatalf("merged M-101 must be bindable, got %v", j.KnowledgePointRefs)
	}
	rows := normalizedRefNames(t, rec.prompt(0))
	if len(rows) != 1 || rows[0][0] != "M-101" || rows[0][1] != "一元二次方程" {
		t.Errorf("prompt block must show the merged complete candidate, got %v", rows)
	}
	assertCandidatesSchema(t, rec, 0, "dedupe empty name first")
}

func TestPaperCandidatesBudgetTruncatesStableAndBounded(t *testing.T) {
	if maxKnowledgeCandidates != 80 {
		t.Fatalf("maxKnowledgeCandidates frozen at 80, got %d", maxKnowledgeCandidates)
	}
	// 3108 全量量级的候选：80 条跨学科排在前面，有效同学科条目全在后面。
	// 上限按调用方给出的顺序先截取前 80 再归一化，故有效候选被挤掉，
	// restrictRefs 仍为真、allow-list 为空、prompt 不含无界白名单。
	big := make([]KnowledgeCandidate, 0, 3108)
	for i := 0; i < 80; i++ {
		big = append(big, KnowledgeCandidate{ExternalRef: fmt.Sprintf("P-%04d", i), Name: "物理", Subject: "physics"})
	}
	for i := 0; i < 3028; i++ {
		big = append(big, KnowledgeCandidate{ExternalRef: fmt.Sprintf("M-%04d", i), Name: "数学知识点", Subject: "math"})
	}
	if len(big) != 3108 {
		t.Fatalf("test fixture size = %d, want 3108", len(big))
	}
	srv, rec := newPaperJudgeFake(t, paperReply(`["M-0000", "P-0000", "INVENTED"]`))
	j, err := judgeMath(t, srv, big)
	if err != nil {
		t.Fatalf("judge with 3108 candidates: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("expected 1 request, got %d", rec.count())
	}
	p := rec.prompt(0)
	if len(p) > len(frozenLegacyJudgePrompt)+1000 {
		t.Errorf("prompt is unbounded (%d bytes, legacy %d)", len(p), len(frozenLegacyJudgePrompt))
	}
	assertFrozenLegacyPromptBytes(t, rec, 0, "3108 truncated to 80 cross-subject")
	assertCandidatesSchema(t, rec, 0, "3108 truncated to 80 cross-subject")
	assertPaperRefsEmptyWithEmptyAllowList(t, j, "3108 truncated to 80 cross-subject")

	// 有效同学科候选排在前 80 时：prompt 恰含 80 条、不含第 81 条，
	// 边界外 ref 不得绑定；两次请求（含重试）结果稳定一致。
	validFirst := make([]KnowledgeCandidate, 0, 3108)
	for i := 0; i < 3108; i++ {
		validFirst = append(validFirst, KnowledgeCandidate{ExternalRef: fmt.Sprintf("M-%04d", i), Name: "数学知识点", Subject: "math"})
	}
	t.Run("first 80 kept and stable", func(t *testing.T) {
		srv, rec := newPaperJudgeFake(t, paperReply(`["M-0000", "M-0079", "M-0080", "M-9999"]`))
		j, err := judgeMath(t, srv, validFirst)
		if err != nil {
			t.Fatalf("judge: %v", err)
		}
		if rec.count() != 1 {
			t.Fatalf("expected 1 request, got %d", rec.count())
		}
		rows := normalizedRefNames(t, rec.prompt(0))
		if len(rows) != maxKnowledgeCandidates {
			t.Fatalf("prompt must carry exactly %d candidates, got %d", maxKnowledgeCandidates, len(rows))
		}
		if rows[0][0] != "M-0000" || rows[len(rows)-1][0] != "M-0079" {
			t.Errorf("truncation must be stable first-N order, got %v ... %v", rows[0], rows[len(rows)-1])
		}
		if strings.Contains(rec.prompt(0), "M-0080") || strings.Contains(rec.prompt(0), "M-9999") {
			t.Errorf("candidates beyond the cap leaked into the prompt")
		}
		if got := strings.Join(j.KnowledgePointRefs, ","); got != "M-0000,M-0079" {
			t.Errorf("only in-cap refs may bind, got %v", j.KnowledgePointRefs)
		}
		assertCandidatesSchema(t, rec, 0, "first 80")
	})

	t.Run("retry keeps the same bounded prompt and refs", func(t *testing.T) {
		srv, rec := newPaperJudgeFake(t, `not json at all`, paperReply(`["M-0000", "M-0080"]`))
		j, err := judgeMath(t, srv, validFirst)
		if err != nil {
			t.Fatalf("judge after retry: %v", err)
		}
		if rec.count() != 2 {
			t.Fatalf("expected 2 requests, got %d", rec.count())
		}
		if rec.prompt(0) != rec.prompt(1) {
			t.Errorf("retry prompt must be byte-identical (stable truncation)")
		}
		if got := strings.Join(j.KnowledgePointRefs, ","); got != "M-0000" {
			t.Errorf("retry must still enforce the cap, got %v", j.KnowledgePointRefs)
		}
		for i := range []int{0, 1} {
			assertCandidatesSchema(t, rec, i, "retry bounded")
		}
	})
}

func TestPaperCandidatesMissingImageErrorsBeforeRequest(t *testing.T) {
	srv, rec := newPaperJudgeFake(t, `{}`)
	c := New(srv.URL+"/v1", "", "fake-model")
	if _, err := c.JudgeQuestionWithCandidates(context.Background(), "math", filepath.Join(t.TempDir(), "nope.jpg"), "1", "q", "", mathCandidates); err == nil {
		t.Fatal("expected error for missing image")
	}
	if rec.count() != 0 {
		t.Fatalf("no request should be sent on missing image, got %d", rec.count())
	}
}
