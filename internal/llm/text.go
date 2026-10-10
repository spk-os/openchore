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

// DailyNoteStats holds what a day's coaching note is written from.
type DailyNoteStats struct {
	KidName        string
	CompletedCount int
	MissedCount    int
	TotalAssigned  int
	PointsEarned   int
	CurrentStreak  int
}

// DailyNote writes the two sides of one day's coaching note in one pass:
// a growth-minded line for the kid and a longer observation for the
// parent. Both come back as JSON; callers parse defensively.
func (c *Client) DailyNote(ctx context.Context, s DailyNoteStats) (childText, parentText string, err error) {
	prompt := fmt.Sprintf(`A kid's day of chores just ended. Kid: %s. Completed %d of %d chores (%d missed), earned %d points, current streak %d days.

Write two short coaching notes about today, in the same language the family uses (default Chinese):
1. "child": ONE sentence, at most 120 characters, for the kid to read themselves. Warm, growth-minded, specific to today's numbers. Never scold; if chores were missed, gently note tomorrow is a fresh start. No emojis.
2. "parent": 2-3 sentences, at most 200 characters, for the parent. One concrete observation about today and one practical suggestion for tomorrow.

Reply with only JSON: {"child": "...", "parent": "..."}`,
		s.KidName, s.CompletedCount, s.TotalAssigned, s.MissedCount, s.PointsEarned, s.CurrentStreak)
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
		Parent string `json:"parent"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return "", "", fmt.Errorf("parsing daily note: %w", err)
	}
	return strings.TrimSpace(parsed.Child), strings.TrimSpace(parsed.Parent), nil
}
