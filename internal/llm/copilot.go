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

// CopilotContext is the household state injected into every Copilot prompt:
// the kid's existing chores and the reward price ladder anchor the model's
// point suggestions to the family's own economy (design: "AI 的定价必须参照
// 现有体系", never "数学卷 100 分").
type CopilotContext struct {
	// KidName grounds titles/descriptions language ("给小明的一周任务").
	KidName string
	// ExistingChores are the kid's chore titles, for dedup awareness.
	ExistingChores []string
	// RewardLadder is "name cost" per reward, the pricing anchor.
	RewardLadder []string
}

// CopilotTask is one drafted task of the Copilot's answer: what the parent
// reviews card by card before anything is created. Confidence and Rationale
// are shown on the card so the parent knows where each suggestion came from.
type CopilotTask struct {
	Title            string   `json:"title"`
	Description      string   `json:"description"`
	Category         string   `json:"category"`
	// Subject is the school subject when the task is schoolwork (math/
	// chinese/english/physics/chemistry — the media library and the error
	// book vocabulary); "" for household chores. The apply path validates
	// it, so the media library's subject chips light up for AI-drafted
	// tasks too.
	Subject          string   `json:"subject"`
	Icon             string   `json:"icon"`
	PointsValue      int      `json:"points_value"`
	MissedPenalty    int      `json:"missed_penalty"`
	EstimatedMinutes int      `json:"estimated_minutes"`
	// Days: daily / weekdays / weekends / mon..sun (same tokens the
	// template packs use, expanded by config.ExpandDays).
	Days []string `json:"days"`
	// Date is set when the task is one specific day (flyer deadlines,
	// "春游 11月2日 准备零食") instead of a repeating pattern.
	Date string `json:"date"`
	// DueBy / AvailableAt are optional "HH:MM" windows.
	DueBy       string `json:"due_by"`
	AvailableAt string `json:"available_at"`
	Confidence  float64 `json:"confidence"`
	Rationale   string  `json:"rationale"`
}

// CopilotEvent is a dated fact pulled out of a flyer (考试 11月3-5日): not a
// chore, but surfaced so the parent can act — e.g. switch template packs.
type CopilotEvent struct {
	Title string `json:"title"`
	Date  string `json:"date"`
	// TemplateSuggestion names a built-in pack the event implies
	// (exam-week for 期中考试), wired to the templates page.
	TemplateSuggestion string `json:"template_suggestion"`
}

// CopilotMemo is a parent-side to-do (缴费、家长会): explicitly NOT a kid
// chore — the design keeps OpenChore a kid-incentive system, the parent's
// own GTD belongs elsewhere. Shown read-only with a copyable line.
type CopilotMemo struct {
	Title string `json:"title"`
	Date  string `json:"date"`
}

// CopilotDraft is the whole answer: task cards plus the advisory notes
// (dedup skips, questions) the drawer shows under the cards.
type CopilotDraft struct {
	Tasks  []CopilotTask  `json:"tasks"`
	Events []CopilotEvent `json:"events"`
	Memos  []CopilotMemo  `json:"memos"`
	Notes  []string       `json:"notes"`
}

var copilotSchema = &Schema{
	Name: "copilot_draft",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"tasks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":             map[string]any{"type": "string"},
						"description":       map[string]any{"type": "string"},
						"category":          map[string]any{"type": "string"},
						"subject":           map[string]any{"type": "string", "enum": []string{"", "math", "chinese", "english", "physics", "chemistry"}},
						"icon":              map[string]any{"type": "string"},
						"points_value":      map[string]any{"type": "integer"},
						"missed_penalty":    map[string]any{"type": "integer"},
						"estimated_minutes": map[string]any{"type": "integer"},
						"days":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						"date":              map[string]any{"type": "string"},
						"due_by":            map[string]any{"type": "string"},
						"available_at":      map[string]any{"type": "string"},
						"confidence":        map[string]any{"type": "number"},
						"rationale":         map[string]any{"type": "string"},
					},
					"required": []string{"title", "category", "points_value", "days"},
				},
			},
			"events": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":               map[string]any{"type": "string"},
						"date":                map[string]any{"type": "string"},
						"template_suggestion": map[string]any{"type": "string"},
					},
					"required": []string{"title"},
				},
			},
			"memos": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title": map[string]any{"type": "string"},
						"date":  map[string]any{"type": "string"},
					},
					"required": []string{"title"},
				},
			},
			"notes": map[string]any{
				"type": "array",
				"items": map[string]any{"type": "string"},
			},
		},
		"required":             []string{"tasks", "events", "memos", "notes"},
		"additionalProperties": false,
	},
}

// DraftFromText turns a parent's one-liner ("下周每天背 20 个英语单词，周六
// 做一张数学卷") into a task plan draft. Existing chores and the reward
// ladder are injected so points stay in-family; survival-obligation tasks
// (吃饭穿衣洗漱) are explicitly forbidden — the ChoreMonster golden rule,
// AI-era edition.
func (c *Client) DraftFromText(ctx context.Context, ctxInfo CopilotContext, request string) (*CopilotDraft, error) {
	return c.DraftFromTextPrompt(ctx, buildCopilotPrompt(ctxInfo, request, false))
}

// DraftFromTextPrompt is DraftFromText with a caller-supplied prompt (任务④:
// the api layer renders the prompt_copilot setting first; the default path
// above keeps the built-in template).
func (c *Client) DraftFromTextPrompt(ctx context.Context, prompt string) (*CopilotDraft, error) {
	raw, err := c.Chat(ctx, []Message{{Role: "user", Content: prompt}}, copilotSchema)
	if err != nil {
		return nil, err
	}
	return parseCopilotDraft(raw)
}

// CopilotPromptBody renders the default text-mode copilot prompt — the
// fallback template of the prompt_copilot setting (任务④). The api layer
// substitutes {{placeholders}} on top of what this returns.
func CopilotPromptBody(ctxInfo CopilotContext, request string) string {
	return buildCopilotPrompt(ctxInfo, request, false)
}

// CopilotPromptFromPhoto renders the default flyer-mode instruction — the
// fallback template of prompt_copilot's photo branch (任务④).
func CopilotPromptFromPhoto(ctxInfo CopilotContext) string {
	return buildCopilotPrompt(ctxInfo, "", true)
}

// DraftFromFlyer extracts the same draft from a photographed school notice
// (multimodal: image + instructions). Kid-executable items become tasks;
// parent-side items (缴费) land in memos; dated facts (期中考试) land in
// events with an optional template-pack suggestion.
func (c *Client) DraftFromFlyer(ctx context.Context, ctxInfo CopilotContext, imagePath string) (*CopilotDraft, error) {
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return nil, fmt.Errorf("reading flyer image: %w", err)
	}
	dataURL := "data:" + http.DetectContentType(data) + ";base64," + base64.StdEncoding.EncodeToString(data)

	parts := []Part{
		{Type: "text", Text: buildCopilotPrompt(ctxInfo, "", true)},
		{Type: "image_url", ImageURL: &ImageURL{URL: dataURL}},
	}
	raw, err := c.Chat(ctx, []Message{{Role: "user", Content: parts}}, copilotSchema)
	if err != nil {
		return nil, err
	}
	return parseCopilotDraft(raw)
}

// buildCopilotPrompt writes the shared instruction block. The mode flips
// one paragraph: free-form request vs. photo extraction.
func buildCopilotPrompt(ctxInfo CopilotContext, request string, fromPhoto bool) string {
	var b strings.Builder
	b.WriteString(`You draft a kid's chore/task plan for a family chore app. The parent reviews every card before anything is created; your job is a complete, well-priced draft they can trust at a glance.

Hard rules:
- NEVER draft survival obligations (eating, dressing, brushing teeth, sleeping basics). Those are family life, not point tasks.
- Points must fit the family's own economy: reference the existing chores and reward ladder below. A 90-minute exam paper is worth a bit more than a 15-minute daily task, never 100 points.
- category: "required" (school/homework must-dos), "core" (daily routine), "bonus" (extra credit). Default "core".
- subject: "math", "chinese", "english", "physics", or "chemistry" when the task is schoolwork in that subject, "" for household chores.
- days uses: "daily", "weekdays", "weekends", or day names "mon".."sun". For a one-day task set "days": [] and "date": "YYYY-MM-DD".
- Every task needs: title (short, kid-readable), description (1 sentence, what "done" looks like), points_value (1-20), estimated_minutes, icon (one emoji), confidence (0.0-1.0), rationale (one short line: why this price/schedule, referencing the anchors when they apply).
- Dedup: if a task duplicates an existing chore listed below, DO NOT emit it as a task — mention it in notes instead ("与现有任务 X 重复，已跳过").
- Write titles, descriptions, notes and rationale in the same language as the parent's request (default Chinese).
- If the parent's request mentions a subject's difficulty or the kid's weak spot, reflect it in points/minutes, and say so in the rationale.

`)
	fmt.Fprintf(&b, "Kid: %s\n", ctxInfo.KidName)
	if len(ctxInfo.ExistingChores) > 0 {
		fmt.Fprintf(&b, "Existing chores (do not duplicate): %s\n", strings.Join(ctxInfo.ExistingChores, "、"))
	} else {
		b.WriteString("Existing chores: none yet\n")
	}
	if len(ctxInfo.RewardLadder) > 0 {
		fmt.Fprintf(&b, "Reward price ladder (points anchor): %s\n", strings.Join(ctxInfo.RewardLadder, "、"))
	}

	if fromPhoto {
		b.WriteString(`
The attached photo is a school notice / flyer / timetable from the kid's school. Extract:
- tasks: only what the KID must do (pack snacks, bring a water bottle, prepare materials) — each with date/days and a fair price.
- events: dated facts worth surfacing (期中考试 11月3-5日, 春游 11月2日). If an event implies a built-in template pack, set template_suggestion to its id: "exam-week" for exams, "holiday-balance" for holidays.
- memos: PARENT-side obligations (缴费、家长会、签字回执) — these must NOT become kid tasks. Put each here with its deadline.
- notes: anything ambiguous or cut off in the photo.
Dates in the photo may lack a year: use the current year unless that would put the date in the past, then use next year. Emit "events": [], "memos": [] when the photo has none.

Reply with only JSON matching the schema.`)
	} else {
		fmt.Fprintf(&b, `
The parent's request: %q
Turn it into the task plan. Add nothing the parent didn't ask for beyond obvious scheduling details; if the request is too vague to schedule, emit your best guess per task with lower confidence and explain in notes.

Reply with only JSON matching the schema.`, request)
	}
	return b.String()
}

// parseCopilotDraft accepts the model's JSON, tolerating servers without
// structured-output support that wrap it in prose or code fences.
func parseCopilotDraft(raw string) (*CopilotDraft, error) {
	raw = strings.TrimSpace(raw)
	var draft CopilotDraft
	if err := json.Unmarshal([]byte(raw), &draft); err != nil {
		start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("could not parse AI response as JSON: %s", truncate(raw, 200))
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &draft); err != nil {
			return nil, fmt.Errorf("could not parse AI response as JSON: %s", truncate(raw, 200))
		}
	}

	validCats := map[string]bool{"required": true, "core": true, "bonus": true}
	validSubjects := map[string]bool{"": true, "math": true, "chinese": true, "english": true, "physics": true, "chemistry": true}
	tasks := make([]CopilotTask, 0, len(draft.Tasks))
	for _, task := range draft.Tasks {
		task.Title = strings.TrimSpace(task.Title)
		if task.Title == "" {
			continue
		}
		if !validCats[task.Category] {
			task.Category = "core"
		}
		// Junk subjects would fall into no media-library bucket at all;
		// fold anything outside the five (plus "") back to unclassified.
		if !validSubjects[task.Subject] {
			task.Subject = ""
		}
		// Keep points in the family band; a wild 100-point exam paper
		// would wreck the economy the ladder anchors.
		if task.PointsValue < 1 {
			task.PointsValue = 1
		}
		if task.PointsValue > 20 {
			task.PointsValue = 20
		}
		if task.MissedPenalty < 0 {
			task.MissedPenalty = 0
		}
		if task.EstimatedMinutes < 0 {
			task.EstimatedMinutes = 0
		}
		if len(task.Days) == 0 && task.Date == "" {
			// A task with neither pattern nor date falls back to daily —
			// the parent sees it on the card and can drop it.
			task.Days = []string{"daily"}
		}
		task.Confidence = min(max(task.Confidence, 0), 1)
		task.Description = strings.TrimSpace(task.Description)
		task.Rationale = strings.TrimSpace(task.Rationale)
		task.Date = strings.TrimSpace(task.Date)
		task.DueBy = strings.TrimSpace(task.DueBy)
		task.AvailableAt = strings.TrimSpace(task.AvailableAt)
		tasks = append(tasks, task)
	}
	draft.Tasks = tasks

	events := make([]CopilotEvent, 0, len(draft.Events))
	for _, e := range draft.Events {
		e.Title = strings.TrimSpace(e.Title)
		if e.Title == "" {
			continue
		}
		events = append(events, e)
	}
	draft.Events = events

	memos := make([]CopilotMemo, 0, len(draft.Memos))
	for _, m := range draft.Memos {
		m.Title = strings.TrimSpace(m.Title)
		if m.Title == "" {
			continue
		}
		memos = append(memos, m)
	}
	draft.Memos = memos

	notes := make([]string, 0, len(draft.Notes))
	for _, n := range draft.Notes {
		if n = strings.TrimSpace(n); n != "" {
			notes = append(notes, n)
		}
	}
	draft.Notes = notes
	return &draft, nil
}
