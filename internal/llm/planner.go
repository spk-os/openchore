package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// The L3 planner protocol (spec §6.3): the existing copilot grows a
// profile-aware planning mode. The prompt injects the kid's learner
// profile (KC mastery weak spots, Ranedeer config, recent activity) and
// the output schema forces non-empty source_refs — DeepTutor's L2→L1
// auditability lesson: every suggestion must cite the fact that
// motivated it, so the parent can check "why this" before adopting.
// On schema failure the planner degrades to a notes-only suggestion
// (retry once first), never landing half-formed tasks.

// PlannerKC is one KC row injected into the prompt (the profile page's
// matrix, weakest first, capped by the caller).
type PlannerKC struct {
	Name          string
	Subject       string
	CorrectCount  int
	TotalCount    int
	Mastery       float64
	LowConfidence bool
}

// PlannerContext is the L3 profile slice the planner prompt needs, on
// top of the household anchors CopilotContext already carries.
type PlannerContext struct {
	CopilotContext
	// ConfigSummary is the Ranedeer config rendered as one line per
	// field ("深度: 初中 · 风格: Visual · …"), already normalized.
	ConfigSummary string
	// Interests are the kid's interest tags (题目情境替换的素材).
	Interests []string
	// WeakKCs are the weak knowledge points, weakest first.
	WeakKCs []PlannerKC
	// RecentEventSummary counts recent learning behavior, e.g.
	// "近 7 天：完成任务 6 次 · 订正 4 次 · 复习 9 次".
	RecentEventSummary string
	// LastPlanFollowup is how the previously adopted planner chores
	// went (作业帮 dynamic-adjust step), e.g.
	// "上周 AI 计划采纳 3 项，完成 2 项". Empty = no prior plan.
	LastPlanFollowup string
	// Intent is the parent's free-form planning intent ("" = let the
	// planner propose from the profile alone).
	Intent string
}

// PlannerRef is one cited fact (DeepTutor L2→L1 link). Type names the
// source table/object (kc_mastery / learner_events / error_dist);
// Fact is the human-readable line the plan card shows under "依据".
type PlannerRef struct {
	Type string `json:"type"`
	ID   int64  `json:"id,omitempty"`
	Fact string `json:"fact"`
}

// PlannerChore is one proposed chore — the same card shape the copilot
// drafts (so the apply path is unchanged), plus the refs that justify it.
type PlannerChore struct {
	Title            string       `json:"title"`
	Description      string       `json:"description"`
	Category         string       `json:"category"`
	// Subject: the school subject when the chore is schoolwork (math/
	// chinese/english/physics/chemistry), "" for household chores — so
	// adopted planner chores land in the media library's subject buckets.
	Subject          string       `json:"subject"`
	Icon             string       `json:"icon"`
	PointsValue      int          `json:"points_value"`
	MissedPenalty    int          `json:"missed_penalty"`
	EstimatedMinutes int          `json:"estimated_minutes"`
	Days             []string     `json:"days"`
	Date             string       `json:"date"`
	DueBy            string       `json:"due_by"`
	AvailableAt      string       `json:"available_at"`
	Confidence       float64      `json:"confidence"`
	Rationale        string       `json:"rationale"`
	Refs             []PlannerRef `json:"refs,omitempty"`
}

// PlannerReviewBoost proposes extra review load for one weak KC (not a
// new chore — the FSRS queue absorbs it).
type PlannerReviewBoost struct {
	KC     string `json:"kc"`
	Reason string `json:"reason"`
}

// PlannerProposal is the structured plan: new chores (the copilot
// card shape), review boosts and the parent-facing summary.
type PlannerProposal struct {
	NewChores     []PlannerChore       `json:"new_chores"`
	ReviewBoost   []PlannerReviewBoost `json:"review_boost"`
	NotesToParent string               `json:"notes_to_parent"`
}

// PlannerDraft is the planner's whole answer: the cited refs plus the
// proposal. Refs are required — a plan without evidence is rejected by
// the parser (the parent never sees an uncited suggestion).
type PlannerDraft struct {
	SourceRefs []PlannerRef    `json:"source_refs"`
	Proposal   PlannerProposal `json:"proposal"`
}

var plannerSchema = &Schema{
	Name: "learner_plan",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"source_refs": map[string]any{
				"type":     "array",
				"minItems": 1,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"type": map[string]any{"type": "string", "enum": []string{"kc_mastery", "learner_events", "error_dist", "student_config", "reward_ladder"}},
						"id":   map[string]any{"type": "integer"},
						"fact": map[string]any{"type": "string"},
					},
					"required": []string{"type", "fact"},
				},
			},
			"proposal": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"new_chores": map[string]any{
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
								"refs": map[string]any{
									"type":  "array",
									"items": map[string]any{
										"type": "object",
										"properties": map[string]any{
											"type": map[string]any{"type": "string"},
											"id":   map[string]any{"type": "integer"},
											"fact": map[string]any{"type": "string"},
										},
										"required": []string{"type", "fact"},
									},
								},
							},
							"required": []string{"title", "category", "points_value", "days"},
						},
					},
					"review_boost": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"kc":     map[string]any{"type": "string"},
								"reason": map[string]any{"type": "string"},
							},
							"required": []string{"kc"},
						},
					},
					"notes_to_parent": map[string]any{"type": "string"},
				},
				"required": []string{"new_chores", "review_boost", "notes_to_parent"},
			},
		},
		"required":             []string{"source_refs", "proposal"},
		"additionalProperties": false,
	},
}

// DraftLearnerPlan runs the planner: profile-in, cited-plan-out. One
// retry on parse failure, then the degraded notes-only draft (spec:
// "失败降级为仅 notes_to_parent 文本建议，不落半成品任务").
func (c *Client) DraftLearnerPlan(ctx context.Context, pc PlannerContext) (*PlannerDraft, error) {
	prompt := buildPlannerPrompt(pc)
	messages := []Message{{Role: "user", Content: prompt}}
	raw, err := c.Chat(ctx, messages, plannerSchema)
	if err != nil {
		return nil, err
	}
	draft, perr := parsePlannerDraft(raw)
	if perr != nil {
		// Retry once — a schema miss on the first shot is common with
		// free models (outlines/instructor's retry-then-degrade ladder).
		raw2, err2 := c.Chat(ctx, append(messages, Message{Role: "assistant", Content: raw},
			Message{Role: "user", Content: "That did not match the schema. Reply again with only the JSON: source_refs must be a non-empty array and proposal must contain new_chores, review_boost and notes_to_parent."}),
			plannerSchema)
		if err2 != nil {
			return nil, err
		}
		draft, perr = parsePlannerDraft(raw2)
		if perr != nil {
			return degradedPlan(perr), nil
		}
	}
	return draft, nil
}

// degradedPlan is the notes-only fallback: no chores, one ref saying the
// structured plan failed, the parse error as the note. The parent still
// gets something actionable (the raw advice), never a half-plan.
func degradedPlan(err error) *PlannerDraft {
	return &PlannerDraft{
		SourceRefs: []PlannerRef{{Type: "learner_events", Fact: "结构化规划解析失败，已降级为文本建议"}},
		Proposal: PlannerProposal{
			NotesToParent: "AI 规划的结构化输出未能解析（" + err.Error() + "）。请稍后重试，或用任务 Copilot 手动起草。",
		},
	}
}

// buildPlannerPrompt writes the planner instruction: profile facts first
// (the refs' raw material), then the hard rules shared with the copilot
// (survival-obligation ban, family-economy pricing), then the
// evidence-first output contract.
func buildPlannerPrompt(pc PlannerContext) string {
	var b strings.Builder
	b.WriteString(`You are a family learning planner. You read a kid's learner profile and propose next week's plan: a few new chores and review boosts, each justified by cited facts from the profile. The parent reviews and adopts; nothing is created automatically.

Hard rules:
- NEVER propose survival obligations (eating, dressing, brushing teeth, sleeping basics).
- Points fit the family's own economy: reference the existing chores and reward ladder below; 1-20 per task.
- category: "required" (school/homework must-dos), "core" (daily routine), "bonus" (extra credit).
- days uses "daily", "weekdays", "weekends", or "mon".."sun"; a one-day task sets "days": [] and "date": "YYYY-MM-DD".
- Every source_ref's fact must be a concrete, checkable statement drawn ONLY from the profile data below (a mastery rate, a count, a config value). Type it as "kc_mastery", "learner_events", "error_dist", "student_config" or "reward_ladder". At least one ref per plan; cite the weak KC a chore targets.
- Each new_chore ALSO carries its own "refs": 1-3 facts that justify THAT chore specifically (same typing rules). A chore without its own refs shows only the plan-level citations to the parent — give every chore its own.
- Low-confidence KCs (n < 5): propose only lightweight observation ("先观察"), never a heavy drill, and say so in the ref.
- Write titles, rationales, facts and notes in the family's language (default Chinese).

`)
	fmt.Fprintf(&b, "Kid: %s\n", pc.KidName)
	if pc.ConfigSummary != "" {
		fmt.Fprintf(&b, "Student config: %s\n", pc.ConfigSummary)
	}
	if len(pc.Interests) > 0 {
		fmt.Fprintf(&b, "Interests: %s\n", strings.Join(pc.Interests, "、"))
	}
	if pc.RecentEventSummary != "" {
		fmt.Fprintf(&b, "Recent activity: %s\n", pc.RecentEventSummary)
	}
	if pc.LastPlanFollowup != "" {
		fmt.Fprintf(&b, "Last week's AI plan follow-up: %s\n", pc.LastPlanFollowup)
		fmt.Fprintf(&b, "Compare against it: keep the tasks that went well (mark them 维持 in the rationale), adjust or drop the ones that were missed, and say which is which.\n")
	}
	if len(pc.WeakKCs) > 0 {
		b.WriteString("KC mastery (weakest first):\n")
		for _, kc := range pc.WeakKCs {
			flag := ""
			if kc.LowConfidence {
				flag = " [样本不足]"
			}
			fmt.Fprintf(&b, "- %s（%s）: %.0f%% correct, n=%d%s\n",
				kc.Name, kc.Subject, kc.Mastery*100, kc.TotalCount, flag)
		}
	} else {
		b.WriteString("KC mastery: no data yet — propose habit-building, not drills\n")
	}
	if len(pc.ExistingChores) > 0 {
		fmt.Fprintf(&b, "Existing chores (do not duplicate): %s\n", strings.Join(pc.ExistingChores, "、"))
	} else {
		b.WriteString("Existing chores: none yet\n")
	}
	if len(pc.RewardLadder) > 0 {
		fmt.Fprintf(&b, "Reward price ladder (points anchor): %s\n", strings.Join(pc.RewardLadder, "、"))
	}
	if pc.Intent != "" {
		fmt.Fprintf(&b, "\nThe parent's intent: %s\nWeave it in; the profile facts still gate the pricing and the refs.", pc.Intent)
	} else {
		b.WriteString("\nThe parent left the intent open: propose from the profile alone, weakest KCs first.")
	}
	b.WriteString(`

Reply with only JSON matching the schema.`)
	return b.String()
}

// parsePlannerDraft tolerates prose-wrapped JSON and rejects the two
// hard contract violations: empty source_refs, or a proposal whose
// notes are empty AND chores are empty (nothing to show the parent).
func parsePlannerDraft(raw string) (*PlannerDraft, error) {
	raw = strings.TrimSpace(raw)
	var draft PlannerDraft
	if err := json.Unmarshal([]byte(raw), &draft); err != nil {
		start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("could not parse planner response as JSON: %s", truncate(raw, 200))
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &draft); err != nil {
			return nil, fmt.Errorf("could not parse planner response as JSON: %s", truncate(raw, 200))
		}
	}

	refs := make([]PlannerRef, 0, len(draft.SourceRefs))
	for _, r := range draft.SourceRefs {
		r.Fact = strings.TrimSpace(r.Fact)
		if r.Fact == "" {
			continue
		}
		switch r.Type {
		case "kc_mastery", "learner_events", "error_dist", "student_config", "reward_ladder":
		default:
			r.Type = "learner_events" // an unknown source still cites a fact; keep it checkable
		}
		refs = append(refs, r)
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("planner returned no source refs")
	}
	draft.SourceRefs = refs

	validCats := map[string]bool{"required": true, "core": true, "bonus": true}
	validSubjects := map[string]bool{"": true, "math": true, "chinese": true, "english": true, "physics": true, "chemistry": true}
	chores := make([]PlannerChore, 0, len(draft.Proposal.NewChores))
	for _, t := range draft.Proposal.NewChores {
		t.Title = strings.TrimSpace(t.Title)
		if t.Title == "" {
			continue
		}
		if !validCats[t.Category] {
			t.Category = "core"
		}
		// Same fold as the copilot: a subject outside the five (+"")
		// would land in no media-library bucket, so it becomes "".
		if !validSubjects[t.Subject] {
			t.Subject = ""
		}
		if t.PointsValue < 1 {
			t.PointsValue = 1
		}
		if t.PointsValue > 20 {
			t.PointsValue = 20
		}
		if t.MissedPenalty < 0 {
			t.MissedPenalty = 0
		}
		if t.EstimatedMinutes < 0 {
			t.EstimatedMinutes = 0
		}
		if len(t.Days) == 0 && t.Date == "" {
			t.Days = []string{"daily"}
		}
		t.Confidence = min(max(t.Confidence, 0), 1)
		t.Description = strings.TrimSpace(t.Description)
		t.Rationale = strings.TrimSpace(t.Rationale)
		// Per-card refs get the same cleanup as the plan-level ones; a card
		// without its own citations simply shows the plan's (the UI falls
		// back), so per-card refs are an enhancement, never a requirement.
		cardRefs := make([]PlannerRef, 0, len(t.Refs))
		for _, r := range t.Refs {
			r.Fact = strings.TrimSpace(r.Fact)
			if r.Fact == "" {
				continue
			}
			switch r.Type {
			case "kc_mastery", "learner_events", "error_dist", "student_config", "reward_ladder":
			default:
				r.Type = "learner_events"
			}
			cardRefs = append(cardRefs, r)
		}
		t.Refs = cardRefs
		chores = append(chores, t)
	}
	draft.Proposal.NewChores = chores

	boosts := make([]PlannerReviewBoost, 0, len(draft.Proposal.ReviewBoost))
	for _, rb := range draft.Proposal.ReviewBoost {
		rb.KC = strings.TrimSpace(rb.KC)
		if rb.KC == "" {
			continue
		}
		boosts = append(boosts, rb)
	}
	draft.Proposal.ReviewBoost = boosts

	draft.Proposal.NotesToParent = strings.TrimSpace(draft.Proposal.NotesToParent)
	if draft.Proposal.NotesToParent == "" && len(chores) == 0 && len(boosts) == 0 {
		return nil, fmt.Errorf("planner proposal is empty")
	}
	return &draft, nil
}
