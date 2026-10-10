package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/liftedkilt/openchore/internal/llm"
	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
	"github.com/liftedkilt/openchore/internal/tts"
)

// The daily coach writes one two-sided note per kid per day: a short
// growth-minded line the kid sees the next morning (with read-aloud
// audio when TTS is on) and a longer observation for the parent.
// Generated nightly by a background worker; never a verdict, always a
// conversation starter.

// defaultCoachTime is when the nightly worker runs, "21:00" local by
// default (after the day's chores wrap, before bedtime).
const defaultCoachTime = "21:00"

// coachTimeSetting is the app_settings key for the nightly run time.
const coachTimeSetting = "coach_time"

// coachEnabledSetting gates the whole feature ("true"/"false").
const coachEnabledSetting = "ai_daily_coach"

// CoachHandler serves the stored notes and owns the nightly worker.
type CoachHandler struct {
	store  *store.Store
	aiSvc  *AIServices
	chores *ChoreHandler // for AI/audio services when set separately
}

func NewCoachHandler(s *store.Store, ai *AIServices, chores *ChoreHandler) *CoachHandler {
	return &CoachHandler{store: s, aiSvc: ai, chores: chores}
}

// SetAI replaces the AI client directly (tests), keeping read-aloud audio.
func (h *CoachHandler) SetAI(ai *llm.Client) {
	h.aiSvc.set(ai, h.aiSvc.Audio())
}

// services returns the AI client and TTS client to use. The TTS client
// may be nil (speech not configured); the AI client nil means the coach
// can't run.
func (h *CoachHandler) services(ctx context.Context) (*llm.Client, *tts.Client) {
	var aiSvc *AIServices
	if h.aiSvc != nil {
		aiSvc = h.aiSvc
	} else if h.chores != nil {
		aiSvc = h.chores.AIServices()
	}
	if aiSvc == nil {
		return nil, nil
	}
	ai := aiSvc.AI()
	var speech *tts.Client
	if audio := aiSvc.Audio(); audio != nil {
		speech = audio.Client()
	}
	return ai, speech
}

// GetNote returns the stored note for a user and date (?date= defaults
// to today). Kids may read their own note; grown-ups any note.
func (h *CoachHandler) GetNote(w http.ResponseWriter, r *http.Request) {
	userID, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().Format(model.DateFormat)
	} else if _, err := time.Parse(model.DateFormat, date); err != nil {
		writeError(w, http.StatusBadRequest, "invalid date, use YYYY-MM-DD")
		return
	}

	viewer := UserFromContext(r.Context())
	viewerID := int64(0)
	if viewer != nil {
		viewerID = viewer.ID
	}
	if viewerID != userID {
		if viewer == nil || viewer.Role != "admin" {
			writeError(w, http.StatusForbidden, "you can only read your own notes")
			return
		}
	}

	note, err := h.store.GetDailyCoachNote(r.Context(), userID, date)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get note")
		return
	}
	if note == nil {
		note = &model.DailyCoachNote{UserID: userID, Date: date}
	}
	// Grown-ups (admin, or a parent reading another profile) see the full
	// note; kids reading their own note never see the parent side.
	if viewer == nil || viewer.Role == model.RoleAdmin || viewerID != userID {
		writeJSON(w, http.StatusOK, note)
		return
	}
	writeJSON(w, http.StatusOK, kidNoteView{DailyCoachNote: *note})
}

// kidNoteView hides the parent side of a note from the kid it is about.
// The outer field shadows the embedded ParentText with an omitempty tag;
// encoding/json resolves the collision by preferring the shallower field,
// so an empty shadow drops the key entirely.
type kidNoteView struct {
	model.DailyCoachNote
	ParentText string `json:"parent_text,omitempty"`
}

// WriteDailyNotes generates notes for every kid for the given date when
// missing. The worker calls it once per kid per day; the public
// endpoint only exists for tests.
func (h *CoachHandler) WriteDailyNotes(ctx context.Context, date string) {
	if on, _ := h.store.GetSetting(ctx, coachEnabledSetting); on != "true" {
		return
	}
	ai, speech := h.services(ctx)
	if ai == nil {
		return
	}

	kids, err := h.store.ListUsers(ctx)
	if err != nil {
		log.Printf("coach: listing users: %v", err)
		return
	}
	voice := ""
	if v, _ := h.store.GetSetting(ctx, "tts_voice"); v != "" {
		voice = v
	}
	for _, kid := range kids {
		if ctx.Err() != nil {
			return
		}
		if kid.Role == "admin" {
			continue
		}
		if existing, _ := h.store.GetDailyCoachNote(ctx, kid.ID, date); existing != nil {
			continue
		}
		stats, err := h.dayStats(ctx, kid, date)
		if err != nil {
			log.Printf("coach: stats for %s: %v", kid.Name, err)
			continue
		}
		if stats == nil {
			continue // nothing scheduled that day; nothing to say
		}
		childText, parentText, err := ai.DailyNote(ctx, *stats)
		if err != nil {
			log.Printf("coach: note for %s failed: %v", kid.Name, err)
			continue
		}
		note := model.DailyCoachNote{
			UserID:     kid.ID,
			Date:       date,
			ChildText:  childText,
			ParentText: parentText,
		}
		if err := h.store.SaveDailyCoachNote(ctx, note); err != nil {
			log.Printf("coach: saving note for %s: %v", kid.Name, err)
			continue
		}
		// Read the kid's line aloud when speech is configured.
		if speech != nil && childText != "" {
			if audio, err := speech.Synthesize(ctx, childText, voice); err == nil {
				if url, err := saveCoachAudio(kid.ID, date, audio); err == nil {
					_ = h.store.UpdateDailyCoachNoteAudio(ctx, kid.ID, date, url)
				}
			} else {
				log.Printf("coach: audio for %s: %v", kid.Name, err)
			}
		}
		log.Printf("coach: wrote daily note for %s (%s)", kid.Name, date)
	}
}

// dayStats gathers what one kid's day looks like, or nil when nothing
// was scheduled.
func (h *CoachHandler) dayStats(ctx context.Context, kid model.User, date string) (*llm.DailyNoteStats, error) {
	scheduled, err := h.store.GetScheduledChoresForUser(ctx, kid.ID, []string{date}, time.Now())
	if err != nil {
		return nil, err
	}
	if len(scheduled) == 0 {
		return nil, nil
	}
	completed, missed, points := 0, 0, 0
	for _, c := range scheduled {
		if c.Completed {
			completed++
			points += c.PointsValue
		} else {
			missed++
		}
	}
	streak, err := h.store.GetUserStreak(ctx, kid.ID)
	if err != nil {
		return nil, err
	}
	return &llm.DailyNoteStats{
		KidName:        kid.Name,
		CompletedCount: completed,
		MissedCount:    missed,
		TotalAssigned:  len(scheduled),
		PointsEarned:   points,
		CurrentStreak:  streak.CurrentStreak,
	}, nil
}

// saveCoachAudio writes one note's MP3 under the TTS directory and
// returns its URL (served by the /tts/ file server).
func saveCoachAudio(userID int64, date string, audio []byte) (string, error) {
	if err := os.MkdirAll(tts.Dir, 0750); err != nil {
		return "", fmt.Errorf("creating TTS directory: %w", err)
	}
	name := fmt.Sprintf("coach_%d_%s.mp3", userID, date)
	if err := os.WriteFile(filepath.Join(tts.Dir, name), audio, 0640); err != nil {
		return "", fmt.Errorf("saving coach audio: %w", err)
	}
	return fmt.Sprintf("/tts/%s?v=%d", name, time.Now().UnixMilli()), nil
}

// parseCoachTime reads the coach_time setting ("HH:MM", 24h) and falls
// back to 21:00.
func parseCoachTime(v string) (hour, minute int) {
	hour, minute = 21, 0
	if v == "" {
		return
	}
	parts := strings.Split(v, ":")
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

// StartDailyCoach runs the nightly worker until ctx ends. Each tick it
// checks whether the configured time (coach_time, default 21:00) has
// passed for today and the day's notes are still missing.
func (h *CoachHandler) StartDailyCoach(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	// date → true once notes for that date have been attempted.
	done := map[string]bool{}
	for {
		now := time.Now()
		date := now.Format(model.DateFormat)
		hour, minute := parseCoachTime(mustGetSetting(ctx, h.store, coachTimeSetting))
		runAt := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		if !done[date] && !now.Before(runAt) {
			done[date] = true
			h.WriteDailyNotes(ctx, date)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func mustGetSetting(ctx context.Context, s *store.Store, key string) string {
	v, _ := s.GetSetting(ctx, key)
	return v
}
