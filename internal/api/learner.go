package api

// L3 learner profile: the xAPI-shaped event stream writers shared by the
// feature handlers (chores/error book/review/courses) and the profile /
// planner / scaffold endpoints. Handlers embed LearnerHook so a
// best-effort append is one method call — an analytics row must never
// fail the business action that produced it (Khanmigo RCT lesson: the
// learning loop lives or dies on the primary flow staying intact).

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// LearnerHook carries the append-only learner event writer. Embed it in
// any handler that produces observable learning behavior.
type LearnerHook struct {
	store *store.Store
}

// newLearnerHook wires the shared store.
func newLearnerHook(s *store.Store) LearnerHook { return LearnerHook{store: s} }

// marshalOrEmpty keeps the append call one-liner friendly: a nil marshal
// target means "no payload for this field".
func marshalOrEmpty(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// recordLearnerEventSafe appends one learner event; errors are logged
// and swallowed. The verb/object constants come from model so the stream
// stays queryable ("what did this kid actually do") without handler-level
// string drift.
func (h *LearnerHook) recordLearnerEventSafe(ctx context.Context, userID int64, verb, objectType string, objectID int64, result, contextData any) {
	if h == nil || h.store == nil || userID == 0 {
		return
	}
	_, err := h.store.RecordLearnerEvent(ctx, store.LearnerEventInput{
		UserID:      userID,
		Verb:        verb,
		ObjectType:  objectType,
		ObjectID:    objectID,
		ResultJSON:  marshalOrEmpty(result),
		ContextJSON: marshalOrEmpty(contextData),
	})
	if err != nil {
		log.Printf("learner event append failed (user %d %s %s#%d): %v", userID, verb, objectType, objectID, err)
	}
}

// --- LearnerHandler: the admin profile & config endpoints ---

// LearnerHandler serves the parent-side learner profile endpoints: the
// Ranedeer-style student config (GET/PUT) and the aggregated profile
// page payload (GET). The kid-side feedback and scaffold endpoints live
// here too — they read the same tables and share nothing else.
type LearnerHandler struct {
	store   *store.Store
	reviews *ReviewHandler
	LearnerHook
}

// NewLearnerHandler wires the shared store and the review handler (the
// profile header reads the review streak from it).
func NewLearnerHandler(s *store.Store, reviews *ReviewHandler) *LearnerHandler {
	return &LearnerHandler{store: s, reviews: reviews, LearnerHook: newLearnerHook(s)}
}

// handleGetStudentConfig serves GET /api/admin/student-config?user_id= —
// always returns a full config (defaults created on first read), so the
// profile page can render and edit without a create-first dance.
func (h *LearnerHandler) HandleGetStudentConfig(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.kidIDOrSelf(w, r)
	if !ok {
		return
	}
	c, err := h.store.GetStudentConfig(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load student config")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// handlePutStudentConfig serves PUT /api/admin/student-config. The body
// is the full config; unknown enum values are normalized (not rejected)
// — a stale client must not brick the save.
func (h *LearnerHandler) HandlePutStudentConfig(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.kidIDOrSelf(w, r)
	if !ok {
		return
	}
	var c model.StudentConfig
	if err := decodeJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	c.UserID = userID
	store.ValidateStudentConfig(&c)
	if err := h.store.PutStudentConfig(r.Context(), &c); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save student config")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// kidIDOrSelf resolves the target kid: the user_id query param for
// admins, self for kids (a kid can only read/write their own config).
func (h *LearnerHandler) kidIDOrSelf(w http.ResponseWriter, r *http.Request) (int64, bool) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return 0, false
	}
	if user.Role == model.RoleAdmin {
		id, err := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
		if err != nil || id == 0 {
			writeError(w, http.StatusBadRequest, "user_id is required")
			return 0, false
		}
		return id, true
	}
	return user.ID, true
}

// learnerProfileResponse is the profile page payload: the OD prototype's
// parent-profile.html five zones — hero stats, KC matrix, event trend,
// recent events and the config — in one fetch.
type learnerProfileResponse struct {
	KidID      int64                  `json:"kid_id"`
	KidName    string                 `json:"kid_name"`
	Config     *model.StudentConfig   `json:"config"`
	KCMastery  []store.KCMasteryStat  `json:"kc_mastery"`
	Events     []model.LearnerEvent   `json:"events"`
	EventTrend []learnerEventDayCount `json:"event_trend"`
	Hero       learnerHeroStats       `json:"hero"`
}

// learnerHeroStats is the four-stat header (tasks this week / fix rate /
// review streak / weak KC count).
type learnerHeroStats struct {
	ChoresThisWeek  int64   `json:"chores_this_week"`
	AnswersTotal    int64   `json:"answers_total"`
	FixRate         float64 `json:"fix_rate"`
	ReviewStreak    int     `json:"review_streak"`
	WeakKCCount     int     `json:"weak_kc_count"`
	MasteredItems   int64   `json:"mastered_items"`
	ReviewedToday   int64   `json:"reviewed_today"`
}

// learnerEventDayCount is one day of the trend sparkline.
type learnerEventDayCount struct {
	Day    string `json:"day"`
	Events int64  `json:"events"`
}

// handleGetLearnerProfile serves GET /api/admin/learner-profile?user_id=.
// The aggregation mirrors the OD prototype's parent-profile.html: hero
// counts from the event stream, the KC matrix weakest-first, and a
// 14-day activity trend.
func (h *LearnerHandler) HandleGetLearnerProfile(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.kidIDOrSelf(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	kidName := ""
	if u, _ := h.store.GetUser(ctx, userID); u != nil {
		kidName = u.Name
	}
	cfg, err := h.store.GetStudentConfig(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load student config")
		return
	}
	mastery, err := h.store.ListKCMastery(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load kc mastery")
		return
	}
	events, err := h.store.ListLearnerEvents(ctx, userID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load events")
		return
	}

	weekAgo := time.Now().AddDate(0, 0, -7)
	choresThisWeek, _ := h.store.CountLearnerEventsSince(ctx, userID, weekAgo, model.EventVerbCompleted)
	answersTotal, _ := h.store.CountLearnerEventsSince(ctx, userID, time.Now().AddDate(0, 0, -3650), model.EventVerbAnswered)
	reviewedToday, _ := h.store.CountLearnerEventsSince(ctx, userID, dayStart(time.Now()), model.EventVerbReviewed)

	// Fix rate: mastered error items / total error items (the prototype's
	// 订正率 hero stat), from the kid's own error book.
	fixRate := 0.0
	masteredItems := int64(0)
	if items, err := h.store.ListErrorItems(ctx, userID, 0, "", 0); err == nil {
		if len(items) > 0 {
			mastered := 0
			for _, it := range items {
				if it.MasteryLevel == model.MasteryMastered {
					mastered++
				}
			}
			fixRate = float64(mastered) / float64(len(items))
			masteredItems = int64(mastered)
		}
	}

	reviewStreak := 0
	if days, err := h.store.ReviewStreakDays(ctx, userID, time.Now()); err == nil {
		reviewStreak = days
	}

	weakKC := 0
	for _, m := range mastery {
		if m.Mastery < 0.8 {
			weakKC++
		}
	}

	// 14-day trend from the event stream (days with zero events included
	// — a flat line is information too). Events are read back in UTC
	// (CURRENT_TIMESTAMP storage); bucket them in local time so they land
	// on the same calendar day the kid experienced.
	trend := make([]learnerEventDayCount, 0, 14)
	dayCounts := map[string]int64{}
	for _, e := range events {
		d := e.OccurredAt.Local().Format("2006-01-02")
		dayCounts[d]++
	}
	for i := 13; i >= 0; i-- {
		d := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		trend = append(trend, learnerEventDayCount{Day: d, Events: dayCounts[d]})
	}

	writeJSON(w, http.StatusOK, learnerProfileResponse{
		KidID:     userID,
		KidName:   kidName,
		Config:    cfg,
		KCMastery: mastery,
		Events:    events,
		EventTrend: trend,
		Hero: learnerHeroStats{
			ChoresThisWeek: choresThisWeek,
			AnswersTotal:   answersTotal,
			FixRate:        fixRate,
			ReviewStreak:   reviewStreak,
			WeakKCCount:    weakKC,
			MasteredItems:  masteredItems,
			ReviewedToday:  reviewedToday,
		},
	})
}

// --- kid-facing endpoints ---

// handleKidAIFeedback powers the kid-side persistent AI study feedback
// card (GET /api/kid/ai-feedback): weakest KCs, review streak and a
// deterministic note. Not an LLM call — the personalized LLM layer lives
// in the coach notes and the error-book scaffolding; this endpoint is
// the always-on, free, offline-safe baseline.
func (h *LearnerHandler) HandleKidAIFeedback(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ctx := r.Context()
	stats, err := h.store.ListKCMastery(ctx, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load mastery")
		return
	}
	weak := make([]store.KCMasteryStat, 0, 4)
	for _, s := range stats {
		if s.Mastery < 0.8 && !s.LowConfidence {
			weak = append(weak, s)
		}
		if len(weak) == 4 {
			break
		}
	}
	reviewStreak := 0
	if days, err := h.store.ReviewStreakDays(ctx, user.ID, time.Now()); err == nil {
		reviewStreak = days
	}
	// The kid cannot read /admin/student-config (403 by design), so the
	// profile one-liner the error book's AI card title shows — 「视觉型 +
	// 鼓励语气」— is folded in here (OD kid-errorbook contract).
	configSummary := ""
	if cfg, err := h.store.GetStudentConfig(ctx, user.ID); err == nil && cfg != nil {
		configSummary = learnerConfigSummary(cfg)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"weak_kcs":       weak,
		"review_streak":  reviewStreak,
		"generated_note": kidFeedbackNote(weak, reviewStreak),
		"config_summary": configSummary,
	})
}

// learnerConfigSummary renders the kid-facing config one-liner: the
// localized learning style plus tone labels (Ranedeer enums → 中文口径).
func learnerConfigSummary(c *model.StudentConfig) string {
	style := map[string]string{
		"Visual": "视觉型", "Verbal": "语言型", "Active": "主动型",
		"Intuitive": "直觉型", "Reflective": "反思型", "Global": "全局型",
	}[c.LearningStyle]
	tone := map[string]string{
		"Encouraging": "鼓励语气", "Neutral": "中性语气", "Informative": "信息型语气",
		"Friendly": "友好语气", "Humorous": "幽默语气",
	}[c.Tone]
	if style == "" {
		style = c.LearningStyle
	}
	if tone == "" {
		tone = c.Tone
	}
	return style + " + " + tone
}

// kidFeedbackNote renders the deterministic encouragement line.
func kidFeedbackNote(weak []store.KCMasteryStat, reviewStreak int) string {
	if len(weak) == 0 {
		if reviewStreak > 0 {
			return "最近正确率很不错，继续保持每天复习！"
		}
		return "还没有薄弱知识点，开始做几道题建立你的学习画像吧！"
	}
	return "重点关注这些知识点：" + joinKCNames(weak) + "，错题本里再练几道就稳了。"
}

func joinKCNames(weak []store.KCMasteryStat) string {
	out := ""
	for i, s := range weak {
		if i > 0 {
			out += "、"
		}
		out += s.Name
	}
	return out
}

// scaffoldStepAt maps an attempt count (1-based) to the Khanmigo-style
// scaffolding ladder: hint → specific hint → decomposition → worked
// answer. The ladder is server-side so a refresh (or a sibling clicking
// around) can't skip to the answer.
func scaffoldStepAt(attempts int) (stage int, prompt string) {
	switch {
	case attempts <= 1:
		return 1, "想一想这一步用哪个知识点？先说说你的思路。"
	case attempts == 2:
		return 2, "再具体一点：把题目里的已知条件列出来，看看能推出什么。"
	case attempts == 3:
		return 3, "我们把题目拆成小步骤：①读题找已知 ②套用公式 ③算出结果。先做第①步。"
	default:
		return 4, "完整示范：按 ①②③ 的步骤做一遍，然后自己换一道类似的题试试。"
	}
}

// handleKidScaffold is the error-book AI hint ladder (POST /api/kid/scaffold).
// Body: {error_item_id, attempt}. The attempt counter is client-supplied
// but clamped server-side (1..20) — a misbehaving client walks the same
// ladder, just no further than step 4.
func (h *LearnerHandler) HandleKidScaffold(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		ErrorItemID int64 `json:"error_item_id"`
		Attempt     int   `json:"attempt"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ErrorItemID == 0 {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if req.Attempt < 1 {
		req.Attempt = 1
	}
	if req.Attempt > 20 {
		req.Attempt = 20
	}
	ctx := r.Context()
	item, err := h.store.GetErrorItem(ctx, req.ErrorItemID)
	if err != nil || item == nil || item.UserID != user.ID {
		writeError(w, http.StatusNotFound, "error item not found")
		return
	}
	stage, prompt := scaffoldStepAt(req.Attempt)
	writeJSON(w, http.StatusOK, map[string]any{
		"stage": stage,
		"hint":  prompt,
	})
}
