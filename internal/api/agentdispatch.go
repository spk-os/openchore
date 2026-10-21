package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/liftedkilt/openchore/internal/agent"
	"github.com/liftedkilt/openchore/internal/llm"
)

// 任务③ text-AI dispatcher（/spk-sdk-agent）：文本 AI 任务（规划/总结/
// 措施蒸馏/措施加强）优先走 spk-agent（真 agent，带工具），失败或未配置
// 时回落直连 llm.Client。图片类任务（照片审查、试卷切题/判读）保持直连
// 多模态客户端——spk-agent 的 instruction 是纯文本。
//
// 路由规则（每个入口一处）：
//
//	agent := h.aiSvc.Agent()   // nil = 未配置 spk-agent
//	if agent != nil { 走 agent 分支 } else { 原直连分支 }
//
// agent 分支超时比直连宽（agent 轮询本身要几秒起步），但最终仍受调用方
// context 约束；幂等键按「任务:实体:天」生成，重试不派生重复 run。

// agentTimeout is the agent path's overall budget (create + poll + items).
const agentTimeout = 3 * time.Minute

// askAgent runs one prompt through spk-agent. keySeed makes the
// idempotency key stable per (task, entity, day): a retried request
// returns the same run instead of forking a second one.
func askAgent(ctx context.Context, ag *agent.Client, prompt, keySeed string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, agentTimeout)
	defer cancel()
	key := fmt.Sprintf("openchore:%s:%s", keySeed, time.Now().UTC().Format("2006-01-02"))
	return ag.Ask(ctx, prompt, key)
}

// defaultPlannerPromptTmpl is the agent-path planner instruction. It
// mirrors buildPlannerPrompt's contract (cited refs, hard rules) but
// rides the agent's own reasoning: the schema is appended as the JSON
// contract instead of a structured-output response_format.
const defaultPlannerPromptTmpl = `You are a family learning planner for OpenChore. Read the kid's learner profile and propose next week's plan: a few new chores and review boosts, each justified by cited facts from the profile. The parent reviews and adopts; nothing is created automatically.

Hard rules:
- NEVER propose survival obligations (eating, dressing, brushing teeth, sleeping basics).
- Points fit the family's own economy: reference the existing chores and reward ladder below; 1-20 per task.
- category: "required" (school/homework must-dos), "core" (daily routine), "bonus" (extra credit).
- days uses "daily", "weekdays", "weekends", or "mon".."sun"; a one-day task sets "days": [] and "date": "YYYY-MM-DD".
- Every source_ref's fact must be a concrete, checkable statement drawn ONLY from the profile data below (a mastery rate, a count, a config value). Type it as "kc_mastery", "learner_events", "error_dist", "student_config" or "reward_ladder". At least one ref per plan; cite the weak KC a chore targets.
- Each new_chore ALSO carries its own "refs": 1-3 facts that justify THAT chore specifically.
- Low-confidence KCs (n < 5): propose only lightweight observation ("先观察"), never a heavy drill, and say so in the ref.
- Write titles, rationales, facts and notes in the family's language (default Chinese).

{{context}}

Reply with only JSON matching this schema:
{"source_refs": [{"type": "kc_mastery|learner_events|error_dist|student_config|reward_ladder", "id": 0, "fact": "..."}], "proposal": {"new_chores": [{"title": "...", "description": "...", "category": "required|core|bonus", "subject": "", "icon": "", "points_value": 1, "missed_penalty": 0, "estimated_minutes": 0, "days": ["daily"], "date": "", "due_by": "", "available_at": "", "confidence": 0.5, "rationale": "...", "refs": [{"type": "...", "fact": "..."}]}], "review_boost": [{"kc": "...", "reason": "..."}], "notes_to_parent": "..."}}`

// plannerPromptContext renders the profile facts block shared by both
// routes (the {{context}} placeholder of the agent template).
func plannerPromptContext(pc llm.PlannerContext) string {
	var b strings.Builder
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
	}
	if len(pc.WeakKCs) > 0 {
		b.WriteString("KC mastery (weakest first):\n")
		for _, kc := range pc.WeakKCs {
			flag := ""
			if kc.LowConfidence {
				flag = " [样本不足]"
			}
			fmt.Fprintf(&b, "- %s（%s）: %.0f%% correct, n=%d%s\n", kc.Name, kc.Subject, kc.Mastery*100, kc.TotalCount, flag)
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
	return b.String()
}

// draftLearnerPlan runs the planner: spk-agent first (任务③), the direct
// client as the fallback. Both parse through the same contract, so the
// degraded notes-only draft applies to both.
func (h *ChoreHandler) draftLearnerPlan(ctx context.Context, pc llm.PlannerContext) (*llm.PlannerDraft, error) {
	if ag := h.aiSvc.Agent(); ag != nil {
		prompt := renderPrompt(ctx, h.store, "prompt_planner", defaultPlannerPromptTmpl, map[string]string{
			"context": plannerPromptContext(pc),
		})
		raw, err := askAgent(ctx, ag, prompt, fmt.Sprintf("planner:%s", hashPlannerKey(pc)))
		if err == nil {
			if draft, perr := llm.ParsePlannerDraft(raw); perr == nil {
				return draft, nil
			}
		}
		// Fall through to the direct route — the agent path never
		// blocks the plan on its own failure.
	}
	ai := h.aiSvc.AI()
	if ai == nil {
		return nil, errAIOff
	}
	return ai.DraftLearnerPlan(ctx, pc)
}

// hashPlannerKey keeps the idempotency key short (a run key with the
// whole profile in it would be absurd).
func hashPlannerKey(pc llm.PlannerContext) string {
	s := pc.KidName + "|" + pc.RecentEventSummary + "|" + pc.Intent
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// defaultSummaryPromptTmpl is the agent-path weekly/monthly summary.
const defaultSummaryPromptTmpl = `Write a brief, encouraging 3-5 sentence summary of this child's chores and learning this week for their parent.

{{stats}}

Back each claim with this week's numbers as inline evidence (e.g. "corrected 4 of the 6 wrong questions"), and end with one short "本周沟通建议" tip for the parent — one concrete thing to say to the kid tomorrow (praise a specific improvement by name, or one gentle nudge), drawn only from these numbers. Be specific and suggest one small, practical next step. Reply with only the summary.`

// summaryPromptStats renders the stats block both summary routes share.
func summaryPromptStats(s llm.WeeklyStats) string {
	missed := "none"
	if len(s.MissedChores) > 0 {
		missed = strings.Join(s.MissedChores, ", ")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Kid: %s. Completed %d of %d (%.0f%%), %d missed, earned %d points, current streak %d days. Most missed: %s.",
		s.KidName, s.CompletedCount, s.TotalAssigned, s.CompletionRate, s.MissedCount, s.PointsEarned, s.CurrentStreak, missed)
	if s.ErrorNewCount > 0 || s.ErrorCorrectedCount > 0 || s.ReviewTotal > 0 {
		fmt.Fprintf(&b, " Learning this week: collected %d new wrong questions (corrected %d, %d still open), spaced-review streak %d days with %d cards graded.",
			s.ErrorNewCount, s.ErrorCorrectedCount, s.ErrorOpenCount, s.ReviewStreak, s.ReviewTotal)
		if len(s.WeakPoints) > 0 {
			fmt.Fprintf(&b, " Weakest knowledge points: %s.", strings.Join(s.WeakPoints, ", "))
		}
	}
	if s.LateCompletions > 0 {
		fmt.Fprintf(&b, " Attention signal: %d of the completed tasks were finished past their due-by time this week. Mention this in at most one neutral sentence, never as a scolding.", s.LateCompletions)
	}
	return b.String()
}

// writeSummary runs the weekly/monthly narrative: spk-agent first, the
// direct client as the fallback (任务③).
func (h *ReportsHandler) writeSummary(ctx context.Context, s llm.WeeklyStats, keySeed string) (string, error) {
	if ag := h.aiSvc.Agent(); ag != nil {
		prompt := renderPrompt(ctx, h.store, "prompt_summary", defaultSummaryPromptTmpl, map[string]string{
			"stats": summaryPromptStats(s),
		})
		if out, err := askAgent(ctx, ag, prompt, keySeed); err == nil {
			return out, nil
		}
	}
	ai := h.aiSvc.AI()
	if ai == nil {
		return "", errAIOff
	}
	return ai.WeeklySummary(ctx, s)
}

// defaultMeasuresPromptTmpl is the agent-path measure distill.
const defaultMeasuresPromptTmpl = `You distill a kid's paper-analysis report into concrete learning measures the parent will execute. Each measure must be one actionable habit or drill ("判别式专项加练：每天 2 题，连续 5 天"), never a vague wish ("提高数学").

Hard rules:
- At most 4 measures, the weakest knowledge point first.
- priority: 1 = start this week, 2 = next week, 3 = keep in mind.
- error_types only from calculation/concept/reading/method — the error mix the measure targets.
- Write title and detail in the family's language (default Chinese). Detail says HOW: what to do, how much, for how long.
- No measure may target survival obligations; these are study habits only.
- kc repeats the knowledge point name exactly as given in the facts below, or "" for a general study-habit measure.

{{context}}

Reply with only JSON: {"measures": [{"subject": "...", "kc": "...", "title": "...", "detail": "...", "error_types": ["..."], "priority": 1}]}`

// measuresPromptContext renders the distill facts block.
func measuresPromptContext(mc llm.MeasureContext) string {
	var b strings.Builder
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
	return b.String()
}

// draftMeasures runs the measure distill: spk-agent first, direct
// fallback (任务③).
func (h *ChoreHandler) draftMeasures(ctx context.Context, mc llm.MeasureContext, keySeed string) ([]llm.MeasureDraft, error) {
	if ag := h.aiSvc.Agent(); ag != nil {
		prompt := renderPrompt(ctx, h.store, "prompt_measures", defaultMeasuresPromptTmpl, map[string]string{
			"context": measuresPromptContext(mc),
		})
		if raw, err := askAgent(ctx, ag, prompt, keySeed); err == nil {
			if out, perr := llm.ParseMeasures(raw); perr == nil {
				return out, nil
			}
		}
	}
	ai := h.aiSvc.AI()
	if ai == nil {
		return nil, errAIOff
	}
	return ai.DraftMeasures(ctx, mc)
}

// defaultStrengthenPromptTmpl is the agent-path measure strengthen.
const defaultStrengthenPromptTmpl = `You strengthen one learning measure for a kid's parent. Keep what works, make it more specific and more executable against the fresh error data. Never lengthen for the sake of length — a sharper one-line detail beats a paragraph.

Kid: {{kid}}
Current measure:
- title: {{title}}
- detail: {{detail}}
Fresh error data:
{{facts}}

Reply with only JSON: {"title": "...", "detail": "..."}. Title and detail in the family's language (default Chinese). Detail must state HOW: what to do, how much, for how long, and what "done" looks like.`

// strengthenMeasure runs the measure strengthen: spk-agent first,
// direct fallback (任务③).
func (h *ChoreHandler) strengthenMeasure(ctx context.Context, kidName, title, detail, facts, keySeed string) (string, string, error) {
	if ag := h.aiSvc.Agent(); ag != nil {
		prompt := renderPrompt(ctx, h.store, "prompt_strengthen", defaultStrengthenPromptTmpl, map[string]string{
			"kid": kidName, "title": title, "detail": detail, "facts": facts,
		})
		if raw, err := askAgent(ctx, ag, prompt, keySeed); err == nil {
			if t, d, perr := llm.ParseStrengthen(raw); perr == nil {
				return t, d, nil
			}
		}
	}
	ai := h.aiSvc.AI()
	if ai == nil {
		return "", "", errAIOff
	}
	return ai.StrengthenMeasure(ctx, kidName, title, detail, facts)
}

// errAIOff is the dispatcher's "no AI configured" error — the handlers
// translate it to 503 the way they always have.
var errAIOff = fmt.Errorf("AI is not configured")
