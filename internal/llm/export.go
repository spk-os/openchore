package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 任务③: exports for the api-layer text-AI dispatcher (spk-agent first,
// direct fallback). The agent path needs the same schema contracts and
// parsers the direct path uses, so both routes validate identically —
// a parent sees the same plan quality whichever route served it.

// PlannerSchema exposes the planner's structured-output schema.
func PlannerSchema() *Schema { return plannerSchema }

// MeasuresSchema exposes the measure-distill schema.
func MeasuresSchema() *Schema { return measuresSchema }

// ParsePlannerDraft is the exported planner parser (agent path).
func ParsePlannerDraft(raw string) (*PlannerDraft, error) { return parsePlannerDraft(raw) }

// DegradedPlan is the exported notes-only fallback (agent path).
func DegradedPlan(err error) *PlannerDraft { return degradedPlan(err) }

// ParseMeasures is the exported measure-distill parser (agent path).
func ParseMeasures(raw string) ([]MeasureDraft, error) { return parseMeasures(raw) }

// ParseStrengthen parses the strengthen reply {"title","detail"}; the
// direct path (StrengthenMeasure) shares it.
func ParseStrengthen(raw string) (title, detail string, err error) {
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

// SchemaJSON renders a schema as compact JSON — the contract text the
// agent path appends to its instruction (the agent's instruction is
// plain text, so the schema rides inline).
func (s *Schema) SchemaJSON() string {
	b, err := json.Marshal(s.Schema)
	if err != nil {
		return "{}"
	}
	return string(b)
}
