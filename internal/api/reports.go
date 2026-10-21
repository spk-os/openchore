package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/liftedkilt/openchore/internal/discord"
	"github.com/liftedkilt/openchore/internal/llm"
	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
	"github.com/liftedkilt/openchore/internal/webhook"
)

type ReportsHandler struct {
	store      *store.Store
	dispatcher *webhook.Dispatcher
	discord    *discord.Notifier
	aiSvc      *AIServices // optional AI used for narrative summaries
}

func NewReportsHandler(s *store.Store, d *webhook.Dispatcher, dn *discord.Notifier, ai *AIServices) *ReportsHandler {
	return &ReportsHandler{store: s, dispatcher: d, discord: dn, aiSvc: ai}
}

// SetAI replaces the AI client directly (tests), keeping read-aloud audio.
func (h *ReportsHandler) SetAI(ai *llm.Client) {
	h.aiSvc.set(ai, h.aiSvc.Media(), h.aiSvc.Audio())
}

// ReportsResponse is the full payload returned by GET /api/admin/reports.
type ReportsResponse struct {
	Period     string          `json:"period"`
	StartDate  string          `json:"start_date"`
	EndDate    string          `json:"end_date"`
	Kids       []KidSummary    `json:"kids"`
	MostMissed []MissedChore   `json:"most_missed"`
	Trend      []TrendDay      `json:"trend"`
	Categories []CategoryStat  `json:"categories"`
	Points     []PointsSummary `json:"points"`
	DayOfWeek  []DayOfWeekStat `json:"day_of_week"`
}

type KidSummary struct {
	UserID          int64   `json:"user_id"`
	Name            string  `json:"name"`
	AvatarURL       string  `json:"avatar_url"`
	TotalAssigned   int     `json:"total_assigned"`
	TotalCompleted  int     `json:"total_completed"`
	TotalMissed     int     `json:"total_missed"`
	CompletionRate  float64 `json:"completion_rate"`
	PointsEarned    int     `json:"points_earned"`
	CurrentStreak   int     `json:"current_streak"`
	LateCompletions int     `json:"late_completions"`
}

type MissedChore struct {
	ChoreID   int64    `json:"chore_id"`
	ChoreName string   `json:"chore_name"`
	MissCount int      `json:"miss_count"`
	Kids      []string `json:"kids"`
}

type TrendDay struct {
	Date      string `json:"date"`
	Completed int    `json:"completed"`
	Assigned  int    `json:"assigned"`
}

type CategoryStat struct {
	Category       string  `json:"category"`
	TotalAssigned  int     `json:"total_assigned"`
	TotalCompleted int     `json:"total_completed"`
	CompletionRate float64 `json:"completion_rate"`
}

type PointsSummary struct {
	UserID        int64  `json:"user_id"`
	Name          string `json:"name"`
	PointsEarned  int    `json:"points_earned"`
	PointsDecayed int    `json:"points_decayed"`
	PointsSpent   int    `json:"points_spent"`
}

type DayOfWeekStat struct {
	DayOfWeek      int     `json:"day_of_week"`
	DayName        string  `json:"day_name"`
	TotalAssigned  int     `json:"total_assigned"`
	TotalCompleted int     `json:"total_completed"`
	CompletionRate float64 `json:"completion_rate"`
}

var dayNames = [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

func (h *ReportsHandler) GetReports(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "week"
	}
	if period != "week" && period != "month" && period != "year" {
		writeError(w, http.StatusBadRequest, "period must be week, month, or year")
		return
	}

	dateStr := r.URL.Query().Get("date")
	var refDate time.Time
	if dateStr != "" {
		var err error
		refDate, err = time.Parse(model.DateFormat, dateStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid date format, use YYYY-MM-DD")
			return
		}
	} else {
		refDate = time.Now()
	}

	startDate, endDate := periodRange(period, refDate)
	startStr := startDate.Format(model.DateFormat)
	endStr := endDate.Format(model.DateFormat)

	kidRows, err := h.store.ReportKidSummaries(r.Context(), startStr, endStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get kid summaries")
		return
	}
	kids := make([]KidSummary, 0, len(kidRows))
	for _, k := range kidRows {
		missed := k.TotalAssigned - k.TotalCompleted
		if missed < 0 {
			missed = 0
		}
		rate := 0.0
		if k.TotalAssigned > 0 {
			rate = float64(k.TotalCompleted) / float64(k.TotalAssigned) * 100
		}
		kids = append(kids, KidSummary{
			UserID:          k.UserID,
			Name:            k.Name,
			AvatarURL:       k.AvatarURL,
			TotalAssigned:   k.TotalAssigned,
			TotalCompleted:  k.TotalCompleted,
			TotalMissed:     missed,
			CompletionRate:  rate,
			PointsEarned:    k.PointsEarned,
			CurrentStreak:   k.CurrentStreak,
			LateCompletions: k.LateCompletions,
		})
	}

	missedRows, err := h.store.ReportMostMissed(r.Context(), startStr, endStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get missed chores")
		return
	}
	mostMissed := make([]MissedChore, 0, len(missedRows))
	for _, m := range missedRows {
		kidList := strings.Split(m.Kids, ",")
		mostMissed = append(mostMissed, MissedChore{
			ChoreID:   m.ChoreID,
			ChoreName: m.ChoreName,
			MissCount: m.MissCount,
			Kids:      kidList,
		})
	}

	trendRows, err := h.store.ReportCompletionTrend(r.Context(), startStr, endStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get trend")
		return
	}
	trend := make([]TrendDay, 0, len(trendRows))
	for _, t := range trendRows {
		trend = append(trend, TrendDay{
			Date:      t.Date,
			Completed: t.Completed,
			Assigned:  t.Assigned,
		})
	}

	catRows, err := h.store.ReportCategoryBreakdown(r.Context(), startStr, endStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get category breakdown")
		return
	}
	categories := make([]CategoryStat, 0, len(catRows))
	for _, c := range catRows {
		rate := 0.0
		if c.TotalAssigned > 0 {
			rate = float64(c.TotalCompleted) / float64(c.TotalAssigned) * 100
		}
		categories = append(categories, CategoryStat{
			Category:       c.Category,
			TotalAssigned:  c.TotalAssigned,
			TotalCompleted: c.TotalCompleted,
			CompletionRate: rate,
		})
	}

	ptRows, err := h.store.ReportPointsSummary(r.Context(), startStr, endStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get points summary")
		return
	}
	points := make([]PointsSummary, 0, len(ptRows))
	for _, p := range ptRows {
		points = append(points, PointsSummary{
			UserID:        p.UserID,
			Name:          p.Name,
			PointsEarned:  p.PointsEarned,
			PointsDecayed: p.PointsDecayed,
			PointsSpent:   p.PointsSpent,
		})
	}

	dowRows, err := h.store.ReportDayOfWeek(r.Context(), startStr, endStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get day of week stats")
		return
	}
	dowStats := make([]DayOfWeekStat, 0, len(dowRows))
	for _, d := range dowRows {
		rate := 0.0
		if d.TotalAssigned > 0 {
			rate = float64(d.TotalCompleted) / float64(d.TotalAssigned) * 100
		}
		name := ""
		if d.DayOfWeek >= 0 && d.DayOfWeek < 7 {
			name = dayNames[d.DayOfWeek]
		}
		dowStats = append(dowStats, DayOfWeekStat{
			DayOfWeek:      d.DayOfWeek,
			DayName:        name,
			TotalAssigned:  d.TotalAssigned,
			TotalCompleted: d.TotalCompleted,
			CompletionRate: rate,
		})
	}

	resp := ReportsResponse{
		Period:     period,
		StartDate:  startStr,
		EndDate:    endStr,
		Kids:       kids,
		MostMissed: mostMissed,
		Trend:      trend,
		Categories: categories,
		Points:     points,
		DayOfWeek:  dowStats,
	}
	writeJSON(w, http.StatusOK, resp)
}

// periodRange computes the inclusive [start, end] dates for a given period.
func periodRange(period string, ref time.Time) (time.Time, time.Time) {
	switch period {
	case "week":
		// Monday through Sunday
		weekday := ref.Weekday()
		if weekday == time.Sunday {
			weekday = 7
		}
		start := ref.AddDate(0, 0, -int(weekday)+1)
		end := start.AddDate(0, 0, 6)
		return start, end
	case "month":
		start := time.Date(ref.Year(), ref.Month(), 1, 0, 0, 0, 0, ref.Location())
		end := start.AddDate(0, 1, -1)
		return start, end
	case "year":
		start := time.Date(ref.Year(), 1, 1, 0, 0, 0, 0, ref.Location())
		end := time.Date(ref.Year(), 12, 31, 0, 0, 0, 0, ref.Location())
		return start, end
	default:
		return ref, ref
	}
}

// The scheduled summaries' run times and switches (问题③). All are
// "HH:MM" 24h app_settings the admin can change; the daily coach keeps
// its own keys in coach.go.
const (
	weeklyTimeSetting     = "weekly_time"  // Sunday's weekly wrap-up
	monthlyTimeSetting    = "monthly_time" // the month-end wrap-up
	monthlyEnabledSetting = "ai_monthly_summary"
	defaultSummaryTime    = "22:30" // both wrap-ups' fallback run time
)

// parseSummaryTime reads a "HH:MM" setting and falls back to
// defaultSummaryTime.
func parseSummaryTime(v string) (hour, minute int) {
	parts := strings.Split(defaultSummaryTime, ":")
	hour, _ = strconv.Atoi(parts[0])
	minute, _ = strconv.Atoi(parts[1])
	if v == "" {
		return
	}
	parts = strings.Split(v, ":")
	if len(parts) != 2 {
		return
	}
	if h, err := strconv.Atoi(parts[0]); err == nil && h >= 0 && h < 24 {
		hour = h
	}
	if m, err := strconv.Atoi(parts[1]); err == nil && m >= 0 && m < 60 {
		minute = m
	}
	return
}

// GetAISummary returns a narrative summary of one person's period. Summaries
// of finished weeks are generated once and kept; anything else is written
// on demand.
func (h *ReportsHandler) GetAISummary(w http.ResponseWriter, r *http.Request) {
	if h.aiSvc.AI() == nil && h.aiSvc.Agent() == nil {
		writeError(w, http.StatusServiceUnavailable, "AI is not configured")
		return
	}

	userID, err := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	period := r.URL.Query().Get("period")
	if period == "" {
		period = "week"
	}
	if period != "week" && period != "month" && period != "year" {
		writeError(w, http.StatusBadRequest, "period must be week, month, or year")
		return
	}

	refDate := time.Now()
	if dateStr := r.URL.Query().Get("date"); dateStr != "" {
		refDate, err = time.ParseInLocation(model.DateFormat, dateStr, time.Local)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid date format, use YYYY-MM-DD")
			return
		}
	}
	start, end := periodRange(period, refDate)
	startStr, endStr := start.Format(model.DateFormat), end.Format(model.DateFormat)

	// A finished week's or month's summary never changes, so reuse it
	// (问题③: finished months now cache like finished weeks always did).
	finished := endStr < time.Now().Format(model.DateFormat)
	if finished && period == "week" {
		if cached, err := h.store.GetWeeklySummary(r.Context(), userID, startStr); err == nil && cached != "" {
			writeJSON(w, http.StatusOK, map[string]string{"summary": cached})
			return
		}
	}
	if finished && period == "month" {
		if cached, err := h.store.GetMonthlySummary(r.Context(), userID, startStr); err == nil && cached != "" {
			writeJSON(w, http.StatusOK, map[string]string{"summary": cached})
			return
		}
	}

	stats, err := h.summaryStats(r.Context(), userID, startStr, endStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get report data")
		return
	}
	if stats == nil {
		writeError(w, http.StatusNotFound, "no data for this user in the selected period")
		return
	}

	// 任务③：spk-agent 优先（真 agent 总结），直连 LLM 回退。
	summary, err := h.writeSummary(r.Context(), *stats, fmt.Sprintf("summary:%d:%s", userID, startStr))
	if err != nil {
		writeError(w, http.StatusBadGateway, "AI summary generation failed: "+err.Error())
		return
	}
	if finished {
		switch period {
		case "week":
			if err := h.store.SaveWeeklySummary(r.Context(), userID, startStr, summary); err != nil {
				log.Printf("ai: saving weekly summary for user %d: %v", userID, err)
			}
		case "month":
			if err := h.store.SaveMonthlySummary(r.Context(), userID, startStr, summary); err != nil {
				log.Printf("ai: saving monthly summary for user %d: %v", userID, err)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"summary": summary})
}

// GetMySummary serves a kid's own period summary (问题①): the finished
// week's/month's narrative the scheduled writer already kept, read-only.
// Kids read their own; a grown-up reading another profile is not this
// endpoint's caller (the admin one is GetAISummary). No AI here on
// purpose: the kid's screen shows what was written, it never triggers a
// generation (no surprise token spend from a child's pocket device).
func (h *ReportsHandler) GetMySummary(w http.ResponseWriter, r *http.Request) {
	userID, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	viewer := UserFromContext(r.Context())
	if viewer == nil || viewer.ID != userID {
		writeError(w, http.StatusForbidden, "you can only read your own summary")
		return
	}

	period := r.URL.Query().Get("period")
	if period != "week" && period != "month" {
		writeError(w, http.StatusBadRequest, "period must be week or month")
		return
	}
	refDate := time.Now()
	if dateStr := r.URL.Query().Get("date"); dateStr != "" {
		refDate, err = time.ParseInLocation(model.DateFormat, dateStr, time.Local)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid date format, use YYYY-MM-DD")
			return
		}
	}
	start, _ := periodRange(period, refDate)

	var summary string
	if period == "week" {
		summary, err = h.store.GetWeeklySummary(r.Context(), userID, start.Format(model.DateFormat))
	} else {
		summary, err = h.store.GetMonthlySummary(r.Context(), userID, start.Format(model.DateFormat))
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get summary")
		return
	}
	// "" (never generated) and an in-progress period both mean "not yet" —
	// the kid's card says so instead of an error.
	writeJSON(w, http.StatusOK, map[string]string{"summary": summary})
}

// summaryStats gathers what a summary is written from, or nil if the user
// has no report row for the range.
func (h *ReportsHandler) summaryStats(ctx context.Context, userID int64, startStr, endStr string) (*llm.WeeklyStats, error) {
	kidRows, err := h.store.ReportKidSummaries(ctx, startStr, endStr)
	if err != nil {
		return nil, err
	}
	var kid *store.KidSummaryRow
	for i := range kidRows {
		if kidRows[i].UserID == userID {
			kid = &kidRows[i]
			break
		}
	}
	if kid == nil {
		return nil, nil
	}
	rate := 0.0
	if kid.TotalAssigned > 0 {
		rate = float64(kid.TotalCompleted) / float64(kid.TotalAssigned) * 100
	}

	var missedChores []string
	missedRows, _ := h.store.ReportMostMissed(ctx, startStr, endStr)
	for _, m := range missedRows {
		for _, k := range strings.Split(m.Kids, ",") {
			if strings.TrimSpace(k) == kid.Name {
				missedChores = append(missedChores, m.ChoreName)
				break
			}
		}
		if len(missedChores) >= 3 {
			break
		}
	}

	// The L1-L4 half: error book, spaced review, weak knowledge points.
	// Best-effort — a failed lookup leaves zeros (the prompt omits the
	// learning lines then) and the chores-only summary still goes out.
	newErr, corrected, open, reviewStreak, reviewTotal, weak := h.learningStats(ctx, userID, startStr, endStr)

	return &llm.WeeklyStats{
		KidName:             kid.Name,
		CompletedCount:      kid.TotalCompleted,
		MissedCount:         max(kid.TotalAssigned-kid.TotalCompleted, 0),
		TotalAssigned:       kid.TotalAssigned,
		PointsEarned:        kid.PointsEarned,
		CurrentStreak:       kid.CurrentStreak,
		CompletionRate:      rate,
		MissedChores:        missedChores,
		LateCompletions:     kid.LateCompletions,
		ErrorNewCount:       newErr,
		ErrorCorrectedCount: corrected,
		ErrorOpenCount:      open,
		ReviewStreak:        reviewStreak,
		ReviewTotal:         reviewTotal,
		WeakPoints:          weak,
	}, nil
}

// learningStats gathers the L1-L4 half of the weekly summary: the error
// book's week shape, the spaced-review effort, and the weakest knowledge
// points. Every query is best-effort — a failed lookup degrades the
// summary to chores-only, it never blocks it.
func (h *ReportsHandler) learningStats(ctx context.Context, userID int64, startStr, endStr string) (newCount, corrected, open, reviewStreak, reviewTotal int, weak []string) {
	if st, err := h.store.ErrorBookWeekStats(ctx, userID, startStr, endStr); err == nil {
		newCount, corrected, open = int(st.NewCount), int(st.CorrectedCount), int(st.OpenCount)
	}
	reviewStreak, _ = h.store.ReviewStreakDays(ctx, userID, time.Now())
	reviewTotal, _ = h.store.CountReviewsBetween(ctx, userID, startStr, endStr)
	if tags, err := h.store.WeakKnowledgeTags(ctx, userID, 3); err == nil {
		for _, t := range tags {
			weak = append(weak, t.Name)
		}
	}
	return
}

// StartWeeklySummaries writes each person's summary of last week once it is
// over and shares it via webhooks and Discord. It only acts while AI is
// configured and the ai_weekly_summary setting is on. Blocks until ctx ends.
func (h *ReportsHandler) StartWeeklySummaries(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		h.WriteWeeklySummaries(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// WriteWeeklySummaries generates any missing summaries for the week before
// now. The week ends Sunday, and the wrap-up runs once the configured time
// (weekly_time, default 22:30) has passed that Sunday evening (问题③) —
// the household asked for the report on Sunday night, not Monday noon.
// StartWeeklySummaries calls it every minute; the finished-week cache
// keeps repeats cheap.
func (h *ReportsHandler) WriteWeeklySummaries(ctx context.Context, now time.Time) {
	if h.aiSvc.AI() == nil && h.aiSvc.Agent() == nil {
		return
	}
	if on, _ := h.store.GetSetting(ctx, "ai_weekly_summary"); on != "true" {
		return
	}
	// The week to wrap is the latest one whose Sunday's configured
	// weekly_time has passed. On Sunday evening that is the week containing
	// now — looking back 7 days from a Sunday lands a week early, since
	// Sunday is the week's last day. Any other time it is the finished
	// week before, so a server that was down on Sunday catches up on
	// Monday; the finished-week cache makes the re-check cheap.
	thisWeekStart, _ := periodRange("week", now)
	wrapStart := thisWeekStart
	weekEnd := wrapStart.AddDate(0, 0, 6)
	hour, minute := parseSummaryTime(mustGetSetting(ctx, h.store, weeklyTimeSetting))
	runAt := time.Date(weekEnd.Year(), weekEnd.Month(), weekEnd.Day(), hour, minute, 0, 0, now.Location())
	if now.Before(runAt) {
		wrapStart = wrapStart.AddDate(0, 0, -7)
		weekEnd = wrapStart.AddDate(0, 0, 6)
	}
	startStr, endStr := wrapStart.Format(model.DateFormat), weekEnd.Format(model.DateFormat)

	kids, err := h.store.ReportKidSummaries(ctx, startStr, endStr)
	if err != nil {
		log.Printf("ai: weekly summaries: %v", err)
		return
	}
	for _, kid := range kids {
		if ctx.Err() != nil {
			return
		}
		if kid.TotalAssigned == 0 {
			continue // nothing happened; nothing to summarize
		}
		if cached, err := h.store.GetWeeklySummary(ctx, kid.UserID, startStr); err != nil || cached != "" {
			continue
		}
		stats, err := h.summaryStats(ctx, kid.UserID, startStr, endStr)
		if err != nil || stats == nil {
			continue
		}
		summary, err := h.writeSummary(ctx, *stats, fmt.Sprintf("summary:%d:%s", kid.UserID, startStr))
		if err != nil {
			log.Printf("ai: weekly summary for %s failed: %v", kid.Name, err)
			continue
		}
		if err := h.store.SaveWeeklySummary(ctx, kid.UserID, startStr, summary); err != nil {
			log.Printf("ai: saving weekly summary for %s: %v", kid.Name, err)
			continue
		}
		log.Printf("ai: wrote weekly summary for %s (week of %s)", kid.Name, startStr)
		h.dispatcher.Fire(webhook.EventWeeklySummary, map[string]any{
			"user_id":    kid.UserID,
			"user_name":  kid.Name,
			"week_start": startStr,
			"week_end":   endStr,
			"summary":    summary,
		})
		h.discord.NotifyWeeklySummary(kid.Name, startStr, summary)
	}
}

// StartMonthlySummaries writes each person's month-end wrap-up on the last
// day of the month, once the configured time (monthly_time, default 22:30)
// has passed (问题③). Blocks until ctx ends.
func (h *ReportsHandler) StartMonthlySummaries(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		h.WriteMonthlySummaries(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// WriteMonthlySummaries generates any missing monthly summaries once the
// month's last day has reached the configured run time. The month is
// summarized as it ends, so late approvals on the last evening still count
// as long as they land before monthly_time.
func (h *ReportsHandler) WriteMonthlySummaries(ctx context.Context, now time.Time) {
	if h.aiSvc.AI() == nil && h.aiSvc.Agent() == nil {
		return
	}
	if on, _ := h.store.GetSetting(ctx, monthlyEnabledSetting); on != "true" {
		return
	}
	start, end := periodRange("month", now)
	// The wrap-up only exists on the month's last day, after monthly_time.
	hour, minute := parseSummaryTime(mustGetSetting(ctx, h.store, monthlyTimeSetting))
	runAt := time.Date(end.Year(), end.Month(), end.Day(), hour, minute, 0, 0, now.Location())
	if now.Format(model.DateFormat) != end.Format(model.DateFormat) || now.Before(runAt) {
		return
	}
	startStr, endStr := start.Format(model.DateFormat), end.Format(model.DateFormat)

	kids, err := h.store.ReportKidSummaries(ctx, startStr, endStr)
	if err != nil {
		log.Printf("ai: monthly summaries: %v", err)
		return
	}
	for _, kid := range kids {
		if ctx.Err() != nil {
			return
		}
		if kid.TotalAssigned == 0 {
			continue
		}
		if cached, err := h.store.GetMonthlySummary(ctx, kid.UserID, startStr); err != nil || cached != "" {
			continue
		}
		stats, err := h.summaryStats(ctx, kid.UserID, startStr, endStr)
		if err != nil || stats == nil {
			continue
		}
		summary, err := h.writeSummary(ctx, *stats, fmt.Sprintf("summary:%d:%s", kid.UserID, startStr))
		if err != nil {
			log.Printf("ai: monthly summary for %s failed: %v", kid.Name, err)
			continue
		}
		if err := h.store.SaveMonthlySummary(ctx, kid.UserID, startStr, summary); err != nil {
			log.Printf("ai: saving monthly summary for %s: %v", kid.Name, err)
			continue
		}
		log.Printf("ai: wrote monthly summary for %s (month of %s)", kid.Name, startStr)
		h.dispatcher.Fire(webhook.EventWeeklySummary, map[string]any{
			"user_id":      kid.UserID,
			"user_name":    kid.Name,
			"period_start": startStr,
			"period_end":   endStr,
			"summary":      summary,
		})
		h.discord.NotifyWeeklySummary(kid.Name, startStr, summary)
	}
}
