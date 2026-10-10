package config

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
)

// The built-in chore templates (P2-9): one JSON file per scene × stage pack
// under templates/, embedded so they ship with the binary and survive
// upgrades without touching the database.
//
//go:embed templates/*.json
var templateFS embed.FS

// TemplateTask is one chore in a template: the fields map 1:1 onto what
// POST /api/chores + POST /api/chores/{id}/schedules accept.
type TemplateTask struct {
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	Icon             string   `json:"icon,omitempty"`
	Category         string   `json:"category"`              // required | core | bonus
	Points           int      `json:"points"`
	MissedPenalty    int      `json:"missed_penalty,omitempty"`
	EstimatedMinutes int      `json:"estimated_minutes,omitempty"`
	Days             []string `json:"days,omitempty"`        // daily / weekdays / weekends / mon..sun
	AvailableAt      string   `json:"available_at,omitempty"`
	DueBy            string   `json:"due_by,omitempty"`
	Expiry           string   `json:"expiry,omitempty"`      // block | no_points | penalty
}

// Template is one importable task pack (e.g. 初三学习日, 考试周冲刺包).
type Template struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Icon        string        `json:"icon"`
	Scenario    string        `json:"scenario"`
	Description string        `json:"description"`
	Tasks       []TemplateTask `json:"tasks"`
	// BonusPaused marks packs that deliberately drop every bonus chore
	// (exam week: a short list is the point).
	BonusPaused bool `json:"bonus_paused,omitempty"`
}

// Templates loads every embedded template, sorted by id so the UI list is
// stable across rebuilds.
func Templates() ([]Template, error) {
	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		return nil, fmt.Errorf("reading embedded templates: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	out := make([]Template, 0, len(names))
	for _, name := range names {
		data, err := templateFS.ReadFile("templates/" + name)
		if err != nil {
			return nil, fmt.Errorf("reading template %s: %w", name, err)
		}
		var t Template
		if err := json.Unmarshal(data, &t); err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", name, err)
		}
		if t.ID == "" {
			return nil, fmt.Errorf("template %s: missing id", name)
		}
		out = append(out, t)
	}
	return out, nil
}
