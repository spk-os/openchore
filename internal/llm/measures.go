package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Learning-measure LLM passes (迁移 034). Two entry points:
//
//   - DraftMeasures: after a paper analysis finishes, distill the report
//     (weak tags, error mix, wrong questions) into a handful of concrete,
//     executable measures. 作业帮「AI 老师一对一」的摸底→计划环节, but
//     the parent confirms — nothing is created automatically.
//   - StrengthenMeasure: the parent picks one measure and asks the AI to
//     strengthen it against the kid's latest error data (改写得更具体、
//     更可执行, 带依据).

// MeasureInput is one fact the measure draft is written from.
type MeasureInput struct {
	Subject    string
	Tag        string // knowledge point name, "" for a general measure
	ErrorType  string // calculation/concept/reading/method
	WrongCount int
}

// MeasureDraft is one proposed measure — the schema-shaped unit the
// handler persists. ErrorTypes drives the parent-side filter chips.
type MeasureDraft struct {
	Subject    string   `json:"subject"`
	KC         string   `json:"kc"`
	Title      string   `json:"title"`
	Detail     string   `json:"detail"`
	ErrorTypes []string `json:"error_types"`
	Priority   int      `json:"priority"`
}

// measuresResult is the parsed reply wrapper.
type measuresResult struct {
	Measures []MeasureDraft `json:"measures"`
}

// MeasureContext is the distill pass's input: the analysis report facts.
type MeasureContext struct {
	KidName     string
	Subject     string
	WeakTags    []MeasureInput
	ErrorMix    []MeasureInput
	ParentNotes string // free-form extra intent, "" = none
}

var measuresSchema = &Schema{
	Name: "learning_measures",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"measures": map[string]any{
				"type": "array",
				"maxItems": 4,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"subject":    map[string]any{"type": "string"},
						"kc":         map[string]any{"type": "string"},
						"title":      map[string]any{"type": "string"},
						"detail":     map[string]any{"type": "string"},
						"error_types": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"calculation", "concept", "reading", "method"}}},
						"priority":   map[string]any{"type": "integer", "enum": []int{1, 2, 3}},
					},
					"required":             []string{"title", "detail", "priority"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"measures"},
		"additionalProperties": false,
	},
}

// DraftMeasures distills the analysis facts into at most 4 measures. One
// retry on parse failure, then an empty list (the caller treats "no
// measures" as "nothing new to add", never a half-formed set).
func (c *Client) DraftMeasures(ctx context.Context, mc MeasureContext) ([]MeasureDraft, error) {
	prompt := buildMeasuresPrompt(mc)
	messages := []Message{{Role: "user", Content: prompt}}
	raw, err := c.Chat(ctx, messages, measuresSchema)
	if err != nil {
		return nil, err
	}
	out, perr := parseMeasures(raw)
	if perr != nil {
		raw2, err2 := c.Chat(ctx, append(messages,
			Message{Role: "assistant", Content: raw},
			Message{Role: "user", Content: "That did not match the schema. Reply again with only the JSON: measures is an array of at most 4 objects with title, detail, priority (1-3)."}),
			measuresSchema)
		if err2 != nil {
			return nil, err
		}
		out, perr = parseMeasures(raw2)
		if perr != nil {
			return nil, perr
		}
	}
	return out, nil
}

// buildMeasuresPrompt renders the distill instruction.
func buildMeasuresPrompt(mc MeasureContext) string {
	var b strings.Builder
	b.WriteString(`You distill a kid's paper-analysis report into concrete learning measures the parent will execute. Each measure must be one actionable habit or drill ("判别式专项加练：每天 2 题，连续 5 天"), never a vague wish ("提高数学").

Hard rules:
- At most 4 measures, the weakest knowledge point first.
- priority: 1 = start this week, 2 = next week, 3 = keep in mind.
- error_types only from calculation/concept/reading/method — the error mix the measure targets.
- Write title and detail in the family's language (default Chinese). Detail says HOW: what to do, how much, for how long.
- No measure may target survival obligations; these are study habits only.
- kc repeats the knowledge point name exactly as given in the facts below, or "" for a general study-habit measure.

`)
	fmt.Fprintf(&b, "Kid: %s\nSubject: %s\n", mc.KidName, mc.Subject)
	if len(mc.WeakTags) > 0 {
		b.WriteString("Weak knowledge points (wrong count desc):\n")
		for _, t := range mc.WeakTags {
			fmt.Fprintf(&b, "- %s: 错 %d 题", t.Tag, t.WrongCount)
			if t.ErrorType != "" {
				fmt.Fprintf(&b, "（主要错因 %s）", t.ErrorType)
			}
			b.WriteString("\n")
		}
	}
	if len(mc.ErrorMix) > 0 {
		b.WriteString("Error-type mix:\n")
		for _, e := range mc.ErrorMix {
			fmt.Fprintf(&b, "- %s: %d 题\n", e.ErrorType, e.WrongCount)
		}
	}
	if mc.ParentNotes != "" {
		fmt.Fprintf(&b, "Parent's extra intent: %s\n", mc.ParentNotes)
	}
	b.WriteString("\nReply with only JSON matching the schema.")
	return b.String()
}

// parseMeasures tolerates prose-wrapped JSON and normalizes the drafts.
func parseMeasures(raw string) ([]MeasureDraft, error) {
	raw = strings.TrimSpace(raw)
	var res measuresResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("could not parse measures response: %s", truncate(raw, 200))
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &res); err != nil {
			return nil, fmt.Errorf("could not parse measures response: %s", truncate(raw, 200))
		}
	}
	out := make([]MeasureDraft, 0, len(res.Measures))
	for _, m := range res.Measures {
		m.Title = strings.TrimSpace(m.Title)
		m.Detail = strings.TrimSpace(m.Detail)
		if m.Title == "" || m.Detail == "" {
			continue
		}
		if m.Priority < 1 || m.Priority > 3 {
			m.Priority = 2
		}
		types := make([]string, 0, len(m.ErrorTypes))
		for _, t := range m.ErrorTypes {
			switch t {
			case "calculation", "concept", "reading", "method":
				types = append(types, t)
			}
		}
		m.ErrorTypes = types
		m.KC = strings.TrimSpace(m.KC)
		m.Subject = strings.TrimSpace(m.Subject)
		if m.Subject == "" {
			m.Subject = "math"
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("measures response carried no usable measures")
	}
	return out, nil
}

// StrengthenMeasure rewrites one existing measure against fresh error
// data. Returns the strengthened title + detail (the handler keeps the
// measure's id, status and origin; origin becomes ai_strengthen).
func (c *Client) StrengthenMeasure(ctx context.Context, kidName string, title, detail string, facts string) (string, string, error) {
	prompt := fmt.Sprintf(`You strengthen one learning measure for a kid's parent. Keep what works, make it more specific and more executable against the fresh error data. Never lengthen for the sake of length — a sharper one-line detail beats a paragraph.

Kid: %s
Current measure:
- title: %s
- detail: %s
Fresh error data:
%s

Reply with only JSON: {"title": "...", "detail": "..."}. Title and detail in the family's language (default Chinese). Detail must state HOW: what to do, how much, for how long, and what "done" looks like.`,
		kidName, title, detail, facts)
	raw, err := c.Chat(ctx, []Message{{Role: "user", Content: prompt}}, nil)
	if err != nil {
		return "", "", err
	}
	raw = strings.TrimSpace(raw)
	var out struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
		if start < 0 || end <= start {
			return "", "", fmt.Errorf("could not parse strengthen response: %s", truncate(raw, 200))
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &out); err != nil {
			return "", "", fmt.Errorf("could not parse strengthen response: %s", truncate(raw, 200))
		}
	}
	out.Title = strings.TrimSpace(out.Title)
	out.Detail = strings.TrimSpace(out.Detail)
	if out.Title == "" || out.Detail == "" {
		return "", "", fmt.Errorf("strengthen response was empty")
	}
	return out.Title, out.Detail, nil
}
