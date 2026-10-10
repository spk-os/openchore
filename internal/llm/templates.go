package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// TemplateTuneInput is one task of a template pack as sent to the tuner.
type TemplateTuneInput struct {
	Index            int    `json:"index"`
	Title            string `json:"title"`
	Category         string `json:"category"`
	Points           int    `json:"points"`
	EstimatedMinutes int    `json:"estimated_minutes"`
}

// TemplateTuneTask is one adjustment the model proposes: which task (by
// index) and the fields to override. Empty fields keep the template value.
type TemplateTuneTask struct {
	Index            int    `json:"index"`
	Title            string `json:"title,omitempty"`
	Description      string `json:"description,omitempty"`
	Points           int    `json:"points,omitempty"`
	EstimatedMinutes int    `json:"estimated_minutes,omitempty"`
}

// TemplateTune is the tuner's answer: a one-line summary for the parent
// plus per-task overrides they confirm before importing.
type TemplateTune struct {
	Summary string             `json:"summary"`
	Changes []TemplateTuneTask `json:"changes"`
}

var templateTuneSchema = &Schema{
	Name: "template_tune",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
			"changes": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"index":             map[string]any{"type": "integer"},
						"title":             map[string]any{"type": "string"},
						"description":       map[string]any{"type": "string"},
						"points":            map[string]any{"type": "integer"},
						"estimated_minutes": map[string]any{"type": "integer"},
					},
					"required": []string{"index"},
				},
			},
		},
		"required":             []string{"summary", "changes"},
		"additionalProperties": false,
	},
}

// TuneTemplate applies a parent's one-line request ("他英语弱，英语加量") to a
// template pack: it may re-point values, durations, titles and descriptions
// of existing tasks, never add or remove tasks, never touch categories.
// The result is a suggestion the parent confirms task by task.
func (c *Client) TuneTemplate(ctx context.Context, packName string, tasks []TemplateTuneInput, tweak string) (*TemplateTune, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "You are adjusting a family chore template %q for one family.\n", packName)
	fmt.Fprintf(&b, "The parent's request: %q\n\nTasks (index. title [category, points, minutes]):\n", tweak)
	for _, t := range tasks {
		fmt.Fprintf(&b, "%d. %s [%s, %d points, %d min]\n", t.Index, t.Title, t.Category, t.Points, t.EstimatedMinutes)
	}
	b.WriteString(`
Adjust the tasks to fit the request. Rules:
- Only adjust EXISTING tasks, referenced by index. Never add or remove tasks.
- You may change points (1-20), estimated_minutes, title and description. Keep each task's category unchanged.
- When the request mentions a subject (e.g. 英语), adjust only the tasks about it; leave the rest untouched (omit them from changes).
- Write titles, descriptions and the summary in the same language as the parent's request (default Chinese).
- If the request does not apply to any task, return an empty changes list and explain why in the summary.

Reply with only JSON: {"summary": "...", "changes": [{"index": 2, "title": "...", "description": "...", "points": 6, "estimated_minutes": 25}]}`)

	raw, err := c.Chat(ctx, []Message{{Role: "user", Content: b.String()}}, templateTuneSchema)
	if err != nil {
		return nil, err
	}
	return parseTemplateTune(raw, len(tasks))
}

// parseTemplateTune accepts the model's JSON, tolerating servers without
// structured-output support that wrap it in prose or code fences.
func parseTemplateTune(raw string, taskCount int) (*TemplateTune, error) {
	raw = strings.TrimSpace(raw)
	var tune TemplateTune
	if err := json.Unmarshal([]byte(raw), &tune); err != nil {
		start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("could not parse AI response as JSON: %s", truncate(raw, 200))
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &tune); err != nil {
			return nil, fmt.Errorf("could not parse AI response as JSON: %s", truncate(raw, 200))
		}
	}
	tune.Summary = strings.TrimSpace(tune.Summary)
	valid := make([]TemplateTuneTask, 0, len(tune.Changes))
	for _, ch := range tune.Changes {
		// Out-of-range indexes are dropped, not fatal.
		if ch.Index < 0 || (taskCount > 0 && ch.Index >= taskCount) {
			continue
		}
		// Nonsense values reset to the "no override" zero, not an error.
		if ch.Points < 0 || ch.Points > 20 {
			ch.Points = 0
		}
		if ch.EstimatedMinutes < 0 {
			ch.EstimatedMinutes = 0
		}
		ch.Title = strings.TrimSpace(ch.Title)
		ch.Description = strings.TrimSpace(ch.Description)
		// A no-op entry (all fields zero/empty) is dropped too.
		if ch.Title == "" && ch.Description == "" && ch.Points == 0 && ch.EstimatedMinutes == 0 {
			continue
		}
		valid = append(valid, ch)
	}
	tune.Changes = valid
	return &tune, nil
}
