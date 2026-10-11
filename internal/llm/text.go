package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DraftDescription writes a short chore description that works both on
// screen and read aloud to a young child.
func (c *Client) DraftDescription(ctx context.Context, title, category string) (string, error) {
	return c.ask(ctx, fmt.Sprintf(`Write a 1-2 sentence description of the household chore %q (category: %s) for a child aged 5-12. Use simple words, say what "done" looks like, and keep it friendly. It will be shown on screen and read aloud. Reply with only the description.`, title, category))
}

// WeeklyStats holds what the weekly summary is written from.
type WeeklyStats struct {
	KidName        string
	CompletedCount int
	MissedCount    int
	TotalAssigned  int
	PointsEarned   int
	CurrentStreak  int
	CompletionRate float64
	MissedChores   []string
}

// WeeklySummary writes an encouraging 2-3 sentence summary for a parent.
func (c *Client) WeeklySummary(ctx context.Context, s WeeklyStats) (string, error) {
	missed := "none"
	if len(s.MissedChores) > 0 {
		missed = strings.Join(s.MissedChores, ", ")
	}
	return c.ask(ctx, fmt.Sprintf(`Write a brief, encouraging 2-3 sentence summary of this child's chores this week for their parent. Kid: %s. Completed %d of %d (%.0f%%), %d missed, earned %d points, current streak %d days. Most missed: %s. Be specific and suggest one small, practical next step. Reply with only the summary.`,
		s.KidName, s.CompletedCount, s.TotalAssigned, s.CompletionRate, s.MissedCount, s.PointsEarned, s.CurrentStreak, missed))
}

// DailyNoteStats holds what a day's coaching note is written from. The
// pet fields are the P4-13 tie-in: an empty PetName means no pet line.
type DailyNoteStats struct {
	KidName        string
	CompletedCount int
	MissedCount    int
	TotalAssigned  int
	PointsEarned   int
	CurrentStreak  int
	PetName        string // the kid's buddy, when they hatched one
	PetMood        string // happy / calm / hungry
	PetFedToday    bool
}

// DailyNote writes the two sides of one day's coaching note in one pass:
// a growth-minded line for the kid and a longer observation for the
// parent. Both come back as JSON; callers parse defensively. When the
// stats carry a pet, the kid's side gains one extra line in the pet's
// voice — the buddy becomes the note's personality (P4-13).
func (c *Client) DailyNote(ctx context.Context, s DailyNoteStats) (childText, parentText string, err error) {
	pet, reply := "", `{"child": "...", "parent": "..."}`
	if s.PetName != "" {
		fed := "not fed today yet"
		if s.PetFedToday {
			fed = "fed today"
		}
		pet = fmt.Sprintf(`
The kid has a pet companion, %s (mood: %s; %s). Moods: happy = today's chores went well, calm = some chores were still open, hungry = chores were missed two days running. Add "pet": ONE extra sentence, at most 60 characters, in the pet's own voice — start with the pet's name, optionally a pet emoji (e.g. "🐕 %s说：…" in Chinese, "%s says: …" in English). Match the pet's mood and whether it was fed; playful and warm, never scold.`,
			s.PetName, s.PetMood, fed, s.PetName, s.PetName)
		reply = `{"child": "...", "pet": "...", "parent": "..."}`
	}
	prompt := fmt.Sprintf(`A kid's day of chores just ended. Kid: %s. Completed %d of %d chores (%d missed), earned %d points, current streak %d days.%s

Write two short coaching notes about today, in the same language the family uses (default Chinese):
1. "child": ONE sentence, at most 120 characters, for the kid to read themselves. Warm, growth-minded, specific to today's numbers. Never scold; if chores were missed, gently note tomorrow is a fresh start. No emojis.
2. "parent": 2-3 sentences, at most 200 characters, for the parent. One concrete observation about today and one practical suggestion for tomorrow.

Reply with only JSON: %s`,
		s.KidName, s.CompletedCount, s.TotalAssigned, s.MissedCount, s.PointsEarned, s.CurrentStreak, pet, reply)
	text, err := c.ask(ctx, prompt)
	if err != nil {
		return "", "", err
	}
	// The model was asked for JSON but may wrap it in fences or prose.
	text = strings.TrimSpace(text)
	if i := strings.Index(text, "{"); i >= 0 {
		if j := strings.LastIndex(text, "}"); j > i {
			text = text[i : j+1]
		}
	}
	var parsed struct {
		Child  string `json:"child"`
		Pet    string `json:"pet"`
		Parent string `json:"parent"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return "", "", fmt.Errorf("parsing daily note: %w", err)
	}
	child := strings.TrimSpace(parsed.Child)
	// Append the pet line only when we asked for one — a stray "pet" key
	// the model dreamt up without a pet is ignored.
	if s.PetName != "" {
		if p := strings.TrimSpace(parsed.Pet); p != "" {
			child = strings.TrimSpace(child + " " + p)
		}
	}
	return child, strings.TrimSpace(parsed.Parent), nil
}
