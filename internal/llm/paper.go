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

// QuestionJudgement is one question's diagnosis: what was asked, what the
// student wrote, the correct answer, why the attempt went wrong, and the
// error taxonomy fields the report aggregates on.
type QuestionJudgement struct {
	QuestionText    string   `json:"question_text"`
	Attempted       bool     `json:"attempted"`
	IsWrong         bool     `json:"is_wrong"`
	WrongAnswerText string   `json:"wrong_answer_text"`
	AnswerText      string   `json:"answer_text"`
	Analysis        string   `json:"analysis"`
	MistakeAnalysis string   `json:"mistake_analysis"`
	ErrorType       string   `json:"error_type"`
	KnowledgePoints []string `json:"knowledge_points"`
	Difficulty      string   `json:"difficulty"`
	Confidence      float64  `json:"confidence"`
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

// imageDataURL loads a file and encodes it as a data: URL for the
// multimodal message parts.
func imageDataURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading image: %w", err)
	}
	return "data:" + http.DetectContentType(data) + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// SegmentPaper cuts one photographed page into its questions. Empty result
// is legitimate (the photo may not be an exam page); an error means the
// model could not be asked at all (after the built-in retry).
func (c *Client) SegmentPaper(ctx context.Context, subject, imagePath string) ([]PaperQuestion, error) {
	dataURL, err := imageDataURL(imagePath)
	if err != nil {
		return nil, err
	}
	prompt := fmt.Sprintf(`You are looking at one photographed page of a student's exam or homework paper (subject: %s). Segment the page into individual questions (题目).

Rules:
- IGNORE page furniture: headers, footers, page numbers, watermarks, school/exam titles, dates, class and student name fields. They are never questions. (Umi-OCR's header/footer removal, applied at the prompt level.)
- Each question starts at its question number. Emit sub-questions (题内小问) as separate entries with composite numbers like "3a", "3b".
- text: the question stem, transcribed. Keep the original language (Chinese stays Chinese). ALL math goes inline as LaTeX: $...$ for inline, $$...$$ for display. (Pix2Text's LaTeX convention.)
- handwritten: true when the stem area itself contains the student's handwriting (not just print).
- bbox: [x, y, width, height] of the question's area on the page, each value normalized 0.0-1.0 relative to the image dimensions.
- Only emit real, numbered questions. If the photo is not an exam/homework page, or shows no numbered questions, emit "questions": [].

Reply with only JSON matching the schema.`, subject)

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

// JudgeQuestion diagnoses one already-segmented question. The page image
// rides along so the model can read the student's handwriting around that
// question number; the prompt names the question to focus on.
func (c *Client) JudgeQuestion(ctx context.Context, subject, imagePath, number, questionText string) (*QuestionJudgement, error) {
	dataURL, err := imageDataURL(imagePath)
	if err != nil {
		return nil, err
	}
	prompt := fmt.Sprintf(`You are diagnosing ONE question on a photographed exam/homework page (subject: %s). The page shows the student's graded work: handwritten answers, cross-outs and marks (✗, red pen corrections) may be present.

The question to judge: #%s
%s

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

Reply with only JSON matching the schema.`, subject, number, questionText)

	parts := []Part{
		{Type: "text", Text: prompt},
		{Type: "image_url", ImageURL: &ImageURL{URL: dataURL}},
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := c.Chat(ctx, []Message{{Role: "user", Content: parts}}, judgeQuestionSchema)
		if err != nil {
			lastErr = err
			continue
		}
		j, err := parseQuestionJudgement(raw)
		if err == nil {
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
	return &j, nil
}
