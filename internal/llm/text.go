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

// WeeklyStats holds what the weekly summary is written from. The chore
// half is the original contract; the learning half (L1-L4) joins the
// error book, the review streak and the weak knowledge points, so the
// parent's weekly report finally covers the学习 side too — zeros mean
// "no learning data this week", and the prompt omits those lines.
type WeeklyStats struct {
	KidName        string
	CompletedCount int
	MissedCount    int
	TotalAssigned  int
	PointsEarned   int
	CurrentStreak  int
	CompletionRate float64
	MissedChores   []string
	// Learning dimensions (zeros = none this week; the prompt skips them).
	ErrorNewCount       int      // wrong questions collected this week
	ErrorCorrectedCount int      // error-book items the kid worked
	ErrorOpenCount      int      // backlog still at "new"
	ReviewStreak        int      // consecutive days of spaced review
	ReviewTotal         int      // review cards graded this week
	WeakPoints           []string // weakest knowledge points, worst first
}

// WeeklySummary writes an encouraging 2-3 sentence summary for a parent.
func (c *Client) WeeklySummary(ctx context.Context, s WeeklyStats) (string, error) {
	missed := "none"
	if len(s.MissedChores) > 0 {
		missed = strings.Join(s.MissedChores, ", ")
	}
	// The learning half: only the lines with data. A kid with no error
	// book yet gets a chores-only summary, not a wall of zeros.
	learning := ""
	if s.ErrorNewCount > 0 || s.ErrorCorrectedCount > 0 || s.ReviewTotal > 0 {
		learning = fmt.Sprintf(` Learning this week: collected %d new wrong questions (corrected %d, %d still open), spaced-review streak %d days with %d cards graded.`,
			s.ErrorNewCount, s.ErrorCorrectedCount, s.ErrorOpenCount, s.ReviewStreak, s.ReviewTotal)
		if len(s.WeakPoints) > 0 {
			learning += fmt.Sprintf(` Weakest knowledge points: %s.`, strings.Join(s.WeakPoints, ", "))
		}
		learning += ` When learning numbers exist, weave one observation about them into the summary (practical: e.g. schedule a short re-do of the weak points next week).`
	}
	return c.ask(ctx, fmt.Sprintf(`Write a brief, encouraging 2-3 sentence summary of this child's chores and learning this week for their parent. Kid: %s. Completed %d of %d (%.0f%%), %d missed, earned %d points, current streak %d days. Most missed: %s.%s Be specific and suggest one small, practical next step. Reply with only the summary.`,
		s.KidName, s.CompletedCount, s.TotalAssigned, s.CompletionRate, s.MissedCount, s.PointsEarned, s.CurrentStreak, missed, learning))
}

// DailyNoteStats holds what a day's coaching note is written from. The
// pet fields are the P4-13 tie-in: an empty PetName means no pet line.
// The family-goal fields are the P4-14 tie-in: an empty FamilyGoalName
// means no progress sentence for the parent.
type DailyNoteStats struct {
	KidName          string
	CompletedCount   int
	MissedCount      int
	TotalAssigned    int
	PointsEarned     int
	CurrentStreak    int
	PetName          string // the kid's buddy, when they hatched one
	PetMood          string // happy / calm / hungry
	PetFedToday      bool
	FamilyGoalName   string // the shared family goal, when the kid joined one
	FamilyGoalSaved  int    // everyone's savings in the pool
	FamilyGoalTarget int
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
	family, familySuffix := "", ""
	if s.FamilyGoalName != "" && s.FamilyGoalTarget > 0 {
		pct := s.FamilyGoalSaved * 100 / s.FamilyGoalTarget
		family = fmt.Sprintf(`
The whole family is saving together toward "%s": %d of %d points pooled so far (%d%%). Weave the family goal's progress into the parent note ("parent"): when relevant, one clause tying the kid's effort to the shared goal (e.g. "家庭目标已完成 %d%%，照这个节奏本周能达成"). Never compare siblings' contributions.`,
			s.FamilyGoalName, s.FamilyGoalSaved, s.FamilyGoalTarget, pct, pct)
		// The parent's brief grows by one clause when there is a goal.
		familySuffix = " When relevant, also tie today's effort to the family's shared goal."
	}
	prompt := fmt.Sprintf(`A kid's day of chores just ended. Kid: %s. Completed %d of %d chores (%d missed), earned %d points, current streak %d days.%s%s

Write two short coaching notes about today, in the same language the family uses (default Chinese):
1. "child": ONE sentence, at most 120 characters, for the kid to read themselves. Warm, growth-minded, specific to today's numbers. Never scold; if chores were missed, gently note tomorrow is a fresh start. No emojis.
2. "parent": 2-3 sentences, at most 200 characters, for the parent. One concrete observation about today and one practical suggestion for tomorrow.%s

Reply with only JSON: %s`,
		s.KidName, s.CompletedCount, s.TotalAssigned, s.MissedCount, s.PointsEarned, s.CurrentStreak, pet, family, familySuffix, reply)
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
