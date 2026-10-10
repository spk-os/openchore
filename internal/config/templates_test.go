package config

import (
	"testing"
)

// The embedded template packs are a shipped feature: every pack must parse,
// carry a sane task list, and expand to valid schedules. These tests keep
// hand-edited JSON from quietly breaking the templates page.
func TestTemplatesLoadsEveryPack(t *testing.T) {
	packs, err := Templates()
	if err != nil {
		t.Fatalf("Templates() failed: %v", err)
	}
	if len(packs) < 4 {
		t.Fatalf("expected at least 4 template packs, got %d", len(packs))
	}
	ids := map[string]bool{}
	for _, p := range packs {
		if ids[p.ID] {
			t.Errorf("duplicate template id %q", p.ID)
		}
		ids[p.ID] = true
		if p.Name == "" || p.Scenario == "" || p.Description == "" {
			t.Errorf("template %q: name, scenario and description are required", p.ID)
		}
		if len(p.Tasks) == 0 {
			t.Errorf("template %q: has no tasks", p.ID)
		}
	}
	for _, want := range []string{"grade9-schoolday", "weekend-enrichment", "exam-week", "holiday-balance"} {
		if !ids[want] {
			t.Errorf("expected template %q to be embedded", want)
		}
	}
}

func TestTemplateTasksValidate(t *testing.T) {
	packs, err := Templates()
	if err != nil {
		t.Fatalf("Templates() failed: %v", err)
	}
	for _, p := range packs {
		for _, task := range p.Tasks {
			if task.Title == "" {
				t.Errorf("%s: task with empty title", p.ID)
			}
			switch task.Category {
			case "required", "core", "bonus":
			default:
				t.Errorf("%s: task %q has invalid category %q", p.ID, task.Title, task.Category)
			}
			if task.Points < 0 {
				t.Errorf("%s: task %q has negative points", p.ID, task.Title)
			}
			days := ExpandDays(task.Days)
			if len(days) == 0 {
				t.Errorf("%s: task %q has no expandable days %v", p.ID, task.Title, task.Days)
			}
			if task.DueBy != "" && !validClock(task.DueBy) {
				t.Errorf("%s: task %q has invalid due_by %q", p.ID, task.Title, task.DueBy)
			}
			if task.AvailableAt != "" && !validClock(task.AvailableAt) {
				t.Errorf("%s: task %q has invalid available_at %q", p.ID, task.Title, task.AvailableAt)
			}
		}
	}
}

// The exam-week pack is deliberately thin: no bonus chores and only a
// handful of tasks (Streaks' lesson — a short list gets done).
func TestExamWeekPackIsLean(t *testing.T) {
	packs, err := Templates()
	if err != nil {
		t.Fatalf("Templates() failed: %v", err)
	}
	for _, p := range packs {
		if p.ID != "exam-week" {
			continue
		}
		if !p.BonusPaused {
			t.Errorf("exam-week should set bonus_paused")
		}
		if len(p.Tasks) > 6 {
			t.Errorf("exam-week should stay under ~6 tasks, got %d", len(p.Tasks))
		}
		for _, task := range p.Tasks {
			if task.Category == "bonus" {
				t.Errorf("exam-week should not ship bonus tasks, found %q", task.Title)
			}
		}
		return
	}
	t.Fatalf("exam-week pack not found")
}

// Every pack must earn something each day it runs: the household contract
// is that effort is paid (ChoreMonster's rule — never pay for survival
// duties, always pay for effort).
func TestEveryPackHasRequiredAndPaidTasks(t *testing.T) {
	packs, err := Templates()
	if err != nil {
		t.Fatalf("Templates() failed: %v", err)
	}
	for _, p := range packs {
		hasRequired, hasCore := false, false
		for _, task := range p.Tasks {
			if task.Category == "required" {
				hasRequired = true
			}
			if task.Category == "core" {
				hasCore = true
			}
		}
		if !hasRequired {
			t.Errorf("%s: no required task — the pack must anchor on obligations", p.ID)
		}
		if !hasCore {
			t.Errorf("%s: no core task — the pack must carry daily fundamentals", p.ID)
		}
	}
}

func validClock(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	return h >= 0 && h <= 23 && m >= 0 && m <= 59
}
