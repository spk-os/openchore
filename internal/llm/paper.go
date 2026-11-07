package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// L1 paper analysis: the two-stage protocol from the spec (学而思拍卷问诊
// compressed to segment → judge). Stage one cuts a photographed page into
// questions (headers/footers stripped — Umi-OCR's page-furniture removal);
// stage two judges each cut question against the student's handwriting.
// Both stages are multimodal and both carry a built-in one-shot retry:
// a flaky parse degrades the question to the manual "unknown" queue, it
// never blocks the photo flow (wrong-notebook's design intent).

// PaperQuestion is one question cut from a page: its number, the stem in
// LaTeX-inline text, whether the stem itself is handwritten, and a
// normalized [x, y, width, height] bounding box for thumbnails/cropping.
type PaperQuestion struct {
	Number      string    `json:"number"`
	Text        string    `json:"text"`
	Handwritten bool      `json:"handwritten"`
	BBox        []float64 `json:"bbox"` // normalized [x, y, w, h], 0.0-1.0
}

// KnowledgeCandidate is one taxonomy knowledge point a judgement may be
// pinned to: the taxonomy's stable external reference, its display name and
// its subject. The judge only ever selects from the refs it is given.
// Callers should pass a small, per-question top-K of same-subject candidates
// (relevance-ranked); JudgeQuestionWithCandidates caps the raw input at
// maxKnowledgeCandidates as a defensive bound against injecting the whole
// taxonomy. AI is advisory: a ref is only ever bound from this caller-supplied
// set, never from free-text knowledge-point names.
type KnowledgeCandidate struct {
	ExternalRef string `json:"external_ref"`
	Name        string `json:"name"`
	Subject     string `json:"subject"`
}

// QuestionJudgement is one question's diagnosis: what was asked, what the
// student wrote, the correct answer, why the attempt went wrong, and the
// error taxonomy fields the report aggregates on. KnowledgePoints keeps the
// free-text Chinese names; KnowledgePointRefs carries the candidate
// external_refs the model selected (empty on the legacy no-candidate path).
type QuestionJudgement struct {
	QuestionText       string   `json:"question_text"`
	Attempted          bool     `json:"attempted"`
	IsWrong            bool     `json:"is_wrong"`
	WrongAnswerText    string   `json:"wrong_answer_text"`
	AnswerText         string   `json:"answer_text"`
	Analysis           string   `json:"analysis"`
	MistakeAnalysis    string   `json:"mistake_analysis"`
	ErrorType          string   `json:"error_type"`
	KnowledgePoints    []string `json:"knowledge_points"`
	KnowledgePointRefs []string `json:"knowledge_point_refs,omitempty"`
	Difficulty         string   `json:"difficulty"`
	Confidence         float64  `json:"confidence"`
}

var segmentPaperSchema = &Schema{
	Name: "paper_questions",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"questions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"number":      map[string]any{"type": "string"},
						"text":        map[string]any{"type": "string"},
						"handwritten": map[string]any{"type": "boolean"},
						"bbox": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "number"},
						},
					},
					"required":             []string{"number", "text"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"questions"},
		"additionalProperties": false,
	},
}

// judgeQuestionSchema is the HEAD-compatible schema: no
// knowledge_point_refs property. The legacy no-candidate path sends exactly
// this, byte-for-byte (properties and all), so an AI server that only knows
// the old contract sees no change at all.
var judgeQuestionSchema = &Schema{
	Name: "question_judgement",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question_text":     map[string]any{"type": "string"},
			"attempted":         map[string]any{"type": "boolean"},
			"is_wrong":          map[string]any{"type": "boolean"},
			"wrong_answer_text": map[string]any{"type": "string"},
			"answer_text":       map[string]any{"type": "string"},
			"analysis":          map[string]any{"type": "string"},
			"mistake_analysis":  map[string]any{"type": "string"},
			"error_type":        map[string]any{"type": "string"},
			"knowledge_points": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"difficulty": map[string]any{"type": "string"},
			"confidence": map[string]any{"type": "number"},
		},
		"required":             []string{"question_text", "attempted", "is_wrong", "confidence"},
		"additionalProperties": false,
	},
}

// judgeQuestionCandidatesSchema adds knowledge_point_refs to the legacy
// schema. It is derived from judgeQuestionSchema (never maintained by hand)
// so the two paths differ in exactly that one property. Only the
// non-empty-candidate path sends it, including when normalisation yields no
// valid candidates at all.
var judgeQuestionCandidatesSchema = func() *Schema {
	baseProps := judgeQuestionSchema.Schema["properties"].(map[string]any)
	props := make(map[string]any, len(baseProps)+1)
	for k, v := range baseProps {
		props[k] = v
	}
	props["knowledge_point_refs"] = map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "string"},
	}
	root := make(map[string]any, len(judgeQuestionSchema.Schema))
	for k, v := range judgeQuestionSchema.Schema {
		root[k] = v
	}
	root["properties"] = props
	return &Schema{Name: judgeQuestionSchema.Name, Schema: root}
}()

// imageDataURL loads a file and encodes it as a data: URL for the
// multimodal message parts.
func imageDataURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading image: %w", err)
	}
	return "data:" + http.DetectContentType(data) + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// instructionBlock renders the parent's free-text focus for this parse
// into the prompt line both stages carry. Empty instructions (the auto
// path) render nothing — the prompt stays byte-identical to before, so
// prompt-marker routing in tests keeps working.
func instructionBlock(instructions string) string {
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return ""
	}
	return "\nThe parent's extra requirement for this analysis (家长的补充要求 — follow it when segmenting/judging, e.g. which question to prioritize):\n" + instructions + "\n"
}

// SegmentPaper cuts one photographed page into its questions. Empty result
// is legitimate (the photo may not be an exam page); an error means the
// model could not be asked at all (after the built-in retry).
func (c *Client) SegmentPaper(ctx context.Context, subject, imagePath, instructions string) ([]PaperQuestion, error) {
	dataURL, err := imageDataURL(imagePath)
	if err != nil {
		return nil, err
	}
	prompt := fmt.Sprintf(`You are looking at one photographed page of a student's exam or homework paper (subject: %s).%sSegment the page into individual questions (题目).

Rules:
- IGNORE page furniture: headers, footers, page numbers, watermarks, school/exam titles, dates, class and student name fields. They are never questions. (Umi-OCR's header/footer removal, applied at the prompt level.)
- Each question starts at its question number. Emit sub-questions (题内小问) as separate entries with composite numbers like "3a", "3b".
- text: the question stem, transcribed. Keep the original language (Chinese stays Chinese). ALL math goes inline as LaTeX: $...$ for inline, $$...$$ for display. (Pix2Text's LaTeX convention.)
- handwritten: true when the stem area itself contains the student's handwriting (not just print).
- bbox: [x, y, width, height] of the question's area on the page, each value normalized 0.0-1.0 relative to the image dimensions.
- Only emit real, numbered questions. If the photo is not an exam/homework page, or shows no numbered questions, emit "questions": [].

Reply with only JSON matching the schema.`, subject, instructionBlock(instructions))

	parts := []Part{
		{Type: "text", Text: prompt},
		{Type: "image_url", ImageURL: &ImageURL{URL: dataURL}},
	}

	var questions []PaperQuestion
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := c.Chat(ctx, []Message{{Role: "user", Content: parts}}, segmentPaperSchema)
		if err != nil {
			lastErr = err
			continue
		}
		questions, err = parseSegmentPaper(raw)
		if err == nil {
			return questions, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// parseSegmentPaper accepts the model's JSON, tolerating prose-wrapped
// replies from servers without structured-output support.
func parseSegmentPaper(raw string) ([]PaperQuestion, error) {
	raw = strings.TrimSpace(raw)
	var out struct {
		Questions []PaperQuestion `json:"questions"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("could not parse AI response as JSON: %s", truncate(raw, 200))
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &out); err != nil {
			return nil, fmt.Errorf("could not parse AI response as JSON: %s", truncate(raw, 200))
		}
	}
	questions := make([]PaperQuestion, 0, len(out.Questions))
	for _, q := range out.Questions {
		q.Number = strings.TrimSpace(q.Number)
		q.Text = strings.TrimSpace(q.Text)
		if q.Number == "" || q.Text == "" {
			continue
		}
		if len(q.BBox) != 4 {
			q.BBox = nil
		}
		questions = append(questions, q)
	}
	return questions, nil
}

// maxKnowledgeCandidates is the hard cap on raw candidates a single judge
// request may carry. Callers must pass a small, per-question top-K of
// same-subject, relevance-ranked knowledge points — never the full 3108-node
// taxonomy. The cap is defensive: the first maxKnowledgeCandidates entries
// in the caller's order are kept BEFORE normalisation, so cross-subject rows
// also consume a slot and are then dropped by normalisation (they can never
// become bound refs). Put the most relevant same-subject candidates first;
// anything past the cap is deterministically discarded.
const maxKnowledgeCandidates = 80

// normalizeCandidates is the single place that decides which candidates are
// judgement-ready. It caps the raw input, keeps same-subject rows with a
// trimmed non-empty external_ref, and deduplicates by subject+ref while
// MERGING names: a first row with an empty name does not poison the ref —
// the first later non-empty name wins. A ref whose every row has an empty
// name is dropped (a nameless point cannot be judged against). The prompt
// block and the allow-list are both built from this one result, so they can
// never diverge.
func normalizeCandidates(subject string, candidates []KnowledgeCandidate) []KnowledgeCandidate {
	if len(candidates) > maxKnowledgeCandidates {
		candidates = candidates[:maxKnowledgeCandidates]
	}
	// All kept rows share subject, so ref alone is the (subject, ref) key.
	names := make(map[string]string, len(candidates))
	order := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c.Subject != subject {
			continue
		}
		ref := strings.TrimSpace(c.ExternalRef)
		if ref == "" {
			continue
		}
		if _, ok := names[ref]; !ok {
			names[ref] = ""
			order = append(order, ref)
		}
		if names[ref] == "" {
			if name := strings.TrimSpace(c.Name); name != "" {
				names[ref] = name
			}
		}
	}
	out := make([]KnowledgeCandidate, 0, len(order))
	for _, ref := range order {
		if name := names[ref]; name != "" {
			out = append(out, KnowledgeCandidate{ExternalRef: ref, Name: name, Subject: subject})
		}
	}
	return out
}

// candidateBlock renders already-normalized candidates as a compact JSON
// array of [external_ref, name] pairs. Flat pairs keep a large (but capped)
// candidate set cheap in tokens.
func candidateBlock(candidates []KnowledgeCandidate) string {
	rows := make([][2]string, 0, len(candidates))
	for _, c := range candidates {
		rows = append(rows, [2]string{c.ExternalRef, c.Name})
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return ""
	}
	return string(b)
}

// candidateRefs is the allow-list, derived from the SAME normalized slice the
// prompt block uses. It is always non-nil (an empty slice means "no ref may
// be bound"), which filterKnowledgePointRefs treats as a strict allow-list.
func candidateRefs(candidates []KnowledgeCandidate) []string {
	refs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		refs = append(refs, c.ExternalRef)
	}
	return refs
}

// JudgeQuestion diagnoses one already-segmented question. The page image
// rides along so the model can read the student's handwriting around that
// question number; the prompt names the question to focus on.
func (c *Client) JudgeQuestion(ctx context.Context, subject, imagePath, number, questionText, instructions string) (*QuestionJudgement, error) {
	return c.JudgeQuestionWithCandidates(ctx, subject, imagePath, number, questionText, instructions, nil)
}

// JudgeQuestionWithCandidates is JudgeQuestion pinned to a taxonomy
// candidate set. Only a genuinely nil or empty candidate set keeps the
// legacy free-text behaviour: that path sends the byte-identical HEAD prompt
// and HEAD schema (no knowledge_point_refs property) and never binds any
// ref, whatever the model writes. Any non-empty set restricts — even when
// normalisation yields no valid candidates: refs come back empty, never
// leaked. When valid same-subject candidates exist the prompt carries the
// compact, capped candidate block and the model must pick
// knowledge_point_refs verbatim from it; inventing refs is forbidden and
// unknown ones are discarded after parsing. AI stays advisory: KnowledgePoints
// (free text) is unaffected and no completion is rejected.
// Callers should pass a per-question top-K (most relevant first); inputs past
// maxKnowledgeCandidates are defensively truncated before normalisation.
func (c *Client) JudgeQuestionWithCandidates(ctx context.Context, subject, imagePath, number, questionText, instructions string, candidates []KnowledgeCandidate) (*QuestionJudgement, error) {
	dataURL, err := imageDataURL(imagePath)
	if err != nil {
		return nil, err
	}

	// Only a genuinely nil or empty candidate set keeps the legacy path.
	// Any non-empty set restricts, decoupled from the allow-list's size: a
	// non-empty set with zero valid same-subject candidates still restricts
	// to nothing, so invented refs can't leak through.
	restrictRefs := len(candidates) > 0

	// candidatesIntro and refsBullet each absorb their own newlines and are
	// "" on the legacy path, so the no-candidate prompt stays byte-identical
	// to the pre-candidate one.
	var allowedRefs []string
	candidatesIntro := ""
	refsBullet := ""
	schema := judgeQuestionSchema // HEAD-compatible: no knowledge_point_refs
	if restrictRefs {
		schema = judgeQuestionCandidatesSchema
		valid := normalizeCandidates(subject, candidates)
		allowedRefs = candidateRefs(valid) // non-nil, possibly empty
		if len(valid) > 0 {
			candidatesIntro = fmt.Sprintf("\nKnowledge-point candidates (taxonomy knowledge points this paper's subject may test), as compact JSON [external_ref, name] pairs:\n%s\n", candidateBlock(valid))
			refsBullet = "\n- knowledge_point_refs: pick 1-4 external_ref values from the candidates above, copied VERBATIM. Do NOT invent, modify or combine refs; unknown refs are discarded. Choose the refs this question actually tests (most specific first)."
		}
	}

	prompt := fmt.Sprintf(`You are diagnosing ONE question on a photographed exam/homework page (subject: %s).%sThe page shows the student's graded work: handwritten answers, cross-outs and marks (✗, red pen corrections) may be present.

The question to judge: #%s
%s
%s
Decide:
- attempted: did the student write an answer attempt for THIS question? Blank/no writing = false.
- is_wrong: is this question a mistake? true when the student's answer is wrong, partially wrong, or the question was required and left blank. false when the answer is correct, or the question is clearly optional and untouched with no marking against it.
- wrong_answer_text: what the student actually wrote (their wrong or partial answer). LaTeX inline ($...$). Empty when not attempted.
- answer_text: the correct answer, worked out by you. LaTeX inline ($...$).
- analysis: how to solve the question, step by step, concise. Chinese.
- mistake_analysis: WHY the student likely lost this question — one or two sentences pointing at the specific step or misconception (e.g. "移项时忘记变号"). Chinese.
- error_type: the dominant cause — "calculation" (会做但算错/粗心), "concept" (概念不清/定义混淆), "reading" (审题错/看错条件), "method" (没有思路/方法不会). Choose exactly one.
- knowledge_points: 1-4 short knowledge-point names this question tests, Chinese (e.g. "一元二次方程", "分式化简").%s
- difficulty: "easy", "medium" or "hard" for a student at this paper's level.
- confidence: 0.0-1.0 — how sure you are of this whole judgement. Low when the photo is blurry, cropped or the writing is illegible.

Reply with only JSON matching the schema.`, subject, instructionBlock(instructions), number, questionText, candidatesIntro, refsBullet)

	parts := []Part{
		{Type: "text", Text: prompt},
		{Type: "image_url", ImageURL: &ImageURL{URL: dataURL}},
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := c.Chat(ctx, []Message{{Role: "user", Content: parts}}, schema)
		if err != nil {
			lastErr = err
			continue
		}
		j, err := parseQuestionJudgement(raw)
		if err == nil {
			if restrictRefs {
				// allowedRefs is non-nil whenever restriction is on, so
				// filterKnowledgePointRefs NEVER falls back to "clean only":
				// an empty allow-list returns an empty result.
				j.KnowledgePointRefs = filterKnowledgePointRefs(j.KnowledgePointRefs, allowedRefs)
			} else {
				// Legacy: free-text knowledge_points stays advisory, but
				// refs are never bound without a caller-supplied candidate
				// set — whatever the model wrote is discarded.
				j.KnowledgePointRefs = []string{}
			}
			return j, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// parseQuestionJudgement accepts the model's JSON defensively and clamps
// the enum-ish fields to their allowed values (an off-schema error_type
// would break the report's distribution chart).
func parseQuestionJudgement(raw string) (*QuestionJudgement, error) {
	raw = strings.TrimSpace(raw)
	var j QuestionJudgement
	if err := json.Unmarshal([]byte(raw), &j); err != nil {
		start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("could not parse AI response as JSON: %s", truncate(raw, 200))
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &j); err != nil {
			return nil, fmt.Errorf("could not parse AI response as JSON: %s", truncate(raw, 200))
		}
	}
	validErrorTypes := map[string]bool{"calculation": true, "concept": true, "reading": true, "method": true}
	validDifficulty := map[string]bool{"easy": true, "medium": true, "hard": true}
	if !validErrorTypes[j.ErrorType] {
		j.ErrorType = ""
	}
	if !validDifficulty[j.Difficulty] {
		j.Difficulty = ""
	}
	j.Confidence = min(max(j.Confidence, 0), 1)
	j.QuestionText = strings.TrimSpace(j.QuestionText)
	j.WrongAnswerText = strings.TrimSpace(j.WrongAnswerText)
	j.AnswerText = strings.TrimSpace(j.AnswerText)
	j.Analysis = strings.TrimSpace(j.Analysis)
	j.MistakeAnalysis = strings.TrimSpace(j.MistakeAnalysis)
	points := make([]string, 0, len(j.KnowledgePoints))
	for _, p := range j.KnowledgePoints {
		if p = strings.TrimSpace(p); p != "" {
			points = append(points, p)
		}
	}
	j.KnowledgePoints = points
	// Cleaning only — the caller decides whether any ref may be bound.
	j.KnowledgePointRefs = filterKnowledgePointRefs(j.KnowledgePointRefs, nil)
	return &j, nil
}

// filterKnowledgePointRefs cleans the model's raw knowledge_point_refs
// reply: trims each entry (whitespace only — matching is case-sensitive and
// exact, per "copied VERBATIM"), drops empties and duplicates (first-seen
// order). A non-nil allowed slice is a STRICT allow-list — including an
// empty one, which drops every ref. A nil allowed slice only cleans; it is
// reserved for parseQuestionJudgement and is never used to decide bindings.
func filterKnowledgePointRefs(raw, allowed []string) []string {
	allowedSet := make(map[string]bool, len(allowed))
	for _, r := range allowed {
		allowedSet[r] = true
	}
	refs := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		if allowed != nil && !allowedSet[r] {
			continue
		}
		seen[r] = true
		refs = append(refs, r)
	}
	return refs
}
