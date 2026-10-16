package api

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/liftedkilt/openchore/internal/llm"
	"github.com/liftedkilt/openchore/internal/store"
	"github.com/liftedkilt/openchore/internal/tts"
)

// Where the AI and speech servers live. The environment (AI_BASE_URL,
// TTS_BASE_URL, ...) wins when set; otherwise these settings, edited under
// Manage → Settings → AI, are used. The API keys are never readable through
// the settings API.
const (
	settingAIBaseURL  = "ai_base_url"
	settingAIModel    = "ai_model"
	settingAIAPIKey   = "ai_api_key"
	settingTTSBaseURL = "tts_base_url"
	settingTTSModel   = "tts_model"
	settingTTSAPIKey  = "tts_api_key"
	// The video/audio proof reviewer. Falls back to the main AI
	// connection (same gateway, the model named below) when unset — the
	// common single-gateway setup needs zero extra configuration.
	settingMediaBaseURL = "media_base_url"
	settingMediaModel   = "media_model"
	settingMediaAPIKey  = "media_api_key"
)

// defaultMediaModel is the video/audio proof reviewer when no media_model
// setting overrides it (the household's gateway serves it; new-api's
// agnes-video-2.5-flash by default).
const defaultMediaModel = "agnes-video-2.5-flash"

// aiConnection is where one OpenAI-compatible service lives.
type aiConnection struct {
	BaseURL string
	Model   string
	APIKey  string
	FromEnv bool
}

// AIServices holds the optional AI client and read-aloud audio. Both can be
// swapped at runtime when a parent changes the connection settings, so
// callers fetch them per use and must handle nil (not configured).
type AIServices struct {
	store *store.Store

	reloadMu sync.Mutex // serializes Reload

	mu        sync.RWMutex
	ai        *llm.Client
	media     *llm.Client // reviews video/audio proof (may equal ai)
	audio     *tts.ChoreAudio
	aiConn    aiConnection // what ai was built from
	mediaConn aiConnection // what media was built from
	ttsConn   aiConnection // what audio was built from
}

func NewAIServices(s *store.Store) *AIServices {
	return &AIServices{store: s}
}

// AI returns the chat client, or nil when AI is off.
func (a *AIServices) AI() *llm.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ai
}

// Audio returns the read-aloud audio generator, or nil when TTS is off.
func (a *AIServices) Audio() *tts.ChoreAudio {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.audio
}

// Media returns the video/audio proof reviewer, or nil when it is off.
// Defaults to the main AI client when no separate media connection is
// configured (single-gateway households).
func (a *AIServices) Media() *llm.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.media
}

func (a *AIServices) set(ai, media *llm.Client, audio *tts.ChoreAudio) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ai, a.media, a.audio = ai, media, audio
}

// resolve reads the current connection settings, environment first.
func (a *AIServices) resolve(ctx context.Context) (ai, media, speech aiConnection) {
	if base := os.Getenv("AI_BASE_URL"); base != "" {
		ai = aiConnection{BaseURL: base, Model: os.Getenv("AI_MODEL"), APIKey: os.Getenv("AI_API_KEY"), FromEnv: true}
	} else {
		ai.BaseURL, _ = a.store.GetSetting(ctx, settingAIBaseURL)
		ai.Model, _ = a.store.GetSetting(ctx, settingAIModel)
		ai.APIKey, _ = a.store.GetSetting(ctx, settingAIAPIKey)
	}
	media = a.resolveMedia(ctx, ai)
	if base := os.Getenv("TTS_BASE_URL"); base != "" {
		speech = aiConnection{BaseURL: base, Model: os.Getenv("TTS_MODEL"), APIKey: os.Getenv("TTS_API_KEY"), FromEnv: true}
	} else {
		speech.BaseURL, _ = a.store.GetSetting(ctx, settingTTSBaseURL)
		speech.Model, _ = a.store.GetSetting(ctx, settingTTSModel)
		speech.APIKey, _ = a.store.GetSetting(ctx, settingTTSAPIKey)
	}
	return ai, media, speech
}

// resolveMedia layers the video/audio reviewer connection on top of the
// main AI connection: an explicit media_* setting overrides; the
// MEDIA_* environment variables override that; otherwise the main
// connection's gateway and key carry the default proof-review model
// (agnes-video-2.5-flash) so a single-gateway household needs no extra
// configuration.
func (a *AIServices) resolveMedia(ctx context.Context, ai aiConnection) aiConnection {
	if ai.BaseURL == "" {
		return aiConnection{}
	}
	media := aiConnection{
		BaseURL: ai.BaseURL,
		APIKey:  ai.APIKey,
		Model:   defaultMediaModel,
		FromEnv: ai.FromEnv,
	}
	if m, _ := a.store.GetSetting(ctx, settingMediaModel); m != "" {
		media.Model = m
	}
	if b, _ := a.store.GetSetting(ctx, settingMediaBaseURL); b != "" {
		media.BaseURL = b
		media.FromEnv = false
	}
	if k, _ := a.store.GetSetting(ctx, settingMediaAPIKey); k != "" {
		media.APIKey = k
	}
	if base := os.Getenv("MEDIA_BASE_URL"); base != "" {
		media.BaseURL = base
		media.FromEnv = true
	}
	if model := os.Getenv("MEDIA_MODEL"); model != "" {
		media.Model = model
		media.FromEnv = true
	}
	if key := os.Getenv("MEDIA_API_KEY"); key != "" {
		media.APIKey = key
	}
	return media
}

// Reload rebuilds whichever client's connection settings changed. It
// reports whether read-aloud audio changed, so the caller can fill in
// recordings with the new service.
func (a *AIServices) Reload(ctx context.Context) (ttsChanged bool) {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()

	aiConn, mediaConn, ttsConn := a.resolve(ctx)

	a.mu.RLock()
	ai, media, audio := a.ai, a.media, a.audio
	aiSame, mediaSame, ttsSame := aiConn == a.aiConn, mediaConn == a.mediaConn, ttsConn == a.ttsConn
	a.mu.RUnlock()

	if !aiSame {
		ai = nil
		switch {
		case aiConn.BaseURL == "":
			log.Printf("ai: off")
		case aiConn.Model == "":
			log.Printf("WARNING: an AI base URL is set but no model; AI features stay off")
		default:
			ai = llm.New(aiConn.BaseURL, aiConn.APIKey, aiConn.Model)
			log.Printf("ai: using model %s at %s", aiConn.Model, aiConn.BaseURL)
		}
	}
	if !mediaSame {
		media = nil
		switch {
		case mediaConn.BaseURL == "":
			log.Printf("ai: media review off")
		default:
			media = llm.New(mediaConn.BaseURL, mediaConn.APIKey, mediaConn.Model)
			log.Printf("ai: media review using model %s at %s", mediaConn.Model, mediaConn.BaseURL)
		}
	}
	if !ttsSame {
		audio = nil
		if ttsConn.BaseURL != "" {
			audio = tts.NewChoreAudio(tts.NewClient(ttsConn.BaseURL, ttsConn.APIKey, ttsConn.Model), a.store)
			log.Printf("tts: using %s", ttsConn.BaseURL)
		} else {
			log.Printf("tts: off")
		}
	}

	a.mu.Lock()
	a.ai, a.media, a.audio = ai, media, audio
	a.aiConn, a.mediaConn, a.ttsConn = aiConn, mediaConn, ttsConn
	a.mu.Unlock()
	return !ttsSame
}

// syncAudioAsync records any chores still missing audio.
func (a *AIServices) syncAudioAsync() {
	audio := a.Audio()
	if audio == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		audio.Sync(ctx, false)
	}()
}

type aiConnectionView struct {
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	APIKeySet bool   `json:"api_key_set"`
	FromEnv   bool   `json:"from_env"`
}

func (c aiConnection) view() aiConnectionView {
	return aiConnectionView{BaseURL: c.BaseURL, Model: c.Model, APIKeySet: c.APIKey != "", FromEnv: c.FromEnv}
}

// GetConfig returns where the AI and speech services live. API keys are
// reported only as set or not. The media section is reported with its
// resolved defaults (model falling back to the main connection's gateway).
func (a *AIServices) GetConfig(w http.ResponseWriter, r *http.Request) {
	ai, media, speech := a.resolve(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ai": ai.view(), "media": media.view(), "tts": speech.view()})
}

// aiConnectionUpdate is one section of PUT /api/admin/ai/config. A nil
// APIKey keeps the stored key; "" clears it.
type aiConnectionUpdate struct {
	BaseURL string  `json:"base_url"`
	Model   string  `json:"model"`
	APIKey  *string `json:"api_key"`
}

// UpdateConfig saves the AI and/or speech connection and switches to it
// without a restart. Sections set by environment variables can't be changed.
func (a *AIServices) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AI    *aiConnectionUpdate `json:"ai"`
		Media *aiConnectionUpdate `json:"media"`
		TTS   *aiConnectionUpdate `json:"tts"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := r.Context()
	current, currentMedia, currentTTS := a.resolve(ctx)

	type section struct {
		name                      string
		upd                       *aiConnectionUpdate
		fromEnv                   bool
		baseKey, modelKey, apiKey string
	}
	sections := []section{
		{"AI", req.AI, current.FromEnv, settingAIBaseURL, settingAIModel, settingAIAPIKey},
		{"media", req.Media, currentMedia.FromEnv, settingMediaBaseURL, settingMediaModel, settingMediaAPIKey},
		{"speech", req.TTS, currentTTS.FromEnv, settingTTSBaseURL, settingTTSModel, settingTTSAPIKey},
	}
	for _, s := range sections {
		if s.upd == nil {
			continue
		}
		if s.fromEnv {
			writeError(w, http.StatusConflict, s.name+" is configured by environment variables")
			return
		}
		s.upd.BaseURL = strings.TrimSpace(s.upd.BaseURL)
		s.upd.Model = strings.TrimSpace(s.upd.Model)
		if s.upd.BaseURL != "" && !isHTTPURL(s.upd.BaseURL) {
			writeError(w, http.StatusBadRequest, s.name+" base URL must be an http(s) URL")
			return
		}
	}
	if req.AI != nil && req.AI.BaseURL != "" && req.AI.Model == "" {
		writeError(w, http.StatusBadRequest, "a model is required with the AI base URL")
		return
	}

	for _, s := range sections {
		if s.upd == nil {
			continue
		}
		values := map[string]string{s.baseKey: s.upd.BaseURL, s.modelKey: s.upd.Model}
		if s.upd.APIKey != nil {
			values[s.apiKey] = strings.TrimSpace(*s.upd.APIKey)
		}
		for k, v := range values {
			if err := a.store.SetSetting(ctx, k, v); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to save setting")
				return
			}
		}
	}

	if a.Reload(ctx) {
		a.syncAudioAsync()
	}
	a.GetConfig(w, r)
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
