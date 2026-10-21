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

	"github.com/liftedkilt/openchore/internal/agent"
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
	// 任务③ spk-agent 接入（/spk-sdk-agent）：文本 AI 任务（规划/总结/
	// 措施）可切到 spk-agent（真 agent + 工具），与直连 LLM 并存。base
	// URL 指向 spk-agent 服务根（无 /v1）；token 是 SPK_AGENT_AUTH_TOKEN，
	// 可空（家用 spk-agent 常不设鉴权）；model 覆盖 agent 内模型。
	settingAgentBaseURL = "agent_base_url"
	settingAgentToken   = "agent_token"
	settingAgentModel   = "agent_model"
	settingAgentRef     = "agent_ref"
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

	mu         sync.RWMutex
	ai         *llm.Client
	media      *llm.Client // reviews video/audio proof (may equal ai)
	audio      *tts.ChoreAudio
	agent      *agent.Client // 任务③: spk-agent for text tasks, may be nil
	aiConn     aiConnection  // what ai was built from
	mediaConn  aiConnection  // what media was built from
	ttsConn    aiConnection  // what audio was built from
	agentConn  agent.Config  // what agent was built from
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

// Agent returns the spk-agent client for text AI tasks (任务③), or nil
// when no agent_base_url is configured. Text callers prefer the agent
// and fall back to the direct llm.Client.
func (a *AIServices) Agent() *agent.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.agent
}

func (a *AIServices) set(ai, media *llm.Client, audio *tts.ChoreAudio) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ai, a.media, a.audio = ai, media, audio
}

// resolve reads the current connection settings, environment first.
func (a *AIServices) resolve(ctx context.Context) (ai, media, speech aiConnection, ag agent.Config) {
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
	// 任务③：AGENT_BASE_URL env 优先，否则读 agent_base_url 设置。
	if base := os.Getenv("AGENT_BASE_URL"); base != "" {
		ag = agent.Config{
			BaseURL:  base,
			Token:    os.Getenv("AGENT_TOKEN"),
			Model:    os.Getenv("AGENT_MODEL"),
			AgentRef: os.Getenv("AGENT_REF"),
		}
	} else {
		ag.BaseURL, _ = a.store.GetSetting(ctx, settingAgentBaseURL)
		ag.Token, _ = a.store.GetSetting(ctx, settingAgentToken)
		ag.Model, _ = a.store.GetSetting(ctx, settingAgentModel)
		ag.AgentRef, _ = a.store.GetSetting(ctx, settingAgentRef)
	}
	return ai, media, speech, ag
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

	aiConn, mediaConn, ttsConn, agentConn := a.resolve(ctx)

	a.mu.RLock()
	ai, media, audio := a.ai, a.media, a.audio
	ag := a.agent
	aiSame, mediaSame, ttsSame := aiConn == a.aiConn, mediaConn == a.mediaConn, ttsConn == a.ttsConn
	agentSame := agentConn == a.agentConn
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
	if !agentSame {
		ag = nil
		if agentConn.BaseURL != "" {
			ag = agent.New(agentConn)
			ref := agentConn.AgentRef
			if ref == "" {
				ref = "spk-tester"
			}
			log.Printf("ai: spk-agent at %s (ref %s)", agentConn.BaseURL, ref)
		} else {
			log.Printf("ai: spk-agent off")
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
	a.ai, a.media, a.audio, a.agent = ai, media, audio, ag
	a.aiConn, a.mediaConn, a.ttsConn, a.agentConn = aiConn, mediaConn, ttsConn, agentConn
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

// agentConnectionView is the agent section of GET /api/admin/ai/config.
// The token is reported only as set or not (it is a secret setting).
type agentConnectionView struct {
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	AgentRef  string `json:"agent_ref"`
	TokenSet  bool   `json:"token_set"`
	FromEnv   bool   `json:"from_env"`
}

// GetConfig returns where the AI and speech services live. API keys are
// reported only as set or not. The media section is reported with its
// resolved defaults (model falling back to the main connection's gateway).
func (a *AIServices) GetConfig(w http.ResponseWriter, r *http.Request) {
	ai, media, speech, ag := a.resolve(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"ai":     ai.view(),
		"media":  media.view(),
		"tts":    speech.view(),
		"agent":  agentConnectionView{BaseURL: ag.BaseURL, Model: ag.Model, AgentRef: ag.AgentRef, TokenSet: ag.Token != "", FromEnv: os.Getenv("AGENT_BASE_URL") != ""},
	})
}

// agentConnectionUpdate is the agent section of PUT /api/admin/ai/config
// (任务③). A nil Token keeps the stored one; "" clears it.
type agentConnectionUpdate struct {
	BaseURL  string  `json:"base_url"`
	Model    string  `json:"model"`
	AgentRef string  `json:"agent_ref"`
	Token    *string `json:"token"`
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
		AI    *aiConnectionUpdate   `json:"ai"`
		Media *aiConnectionUpdate   `json:"media"`
		TTS   *aiConnectionUpdate   `json:"tts"`
		Agent *agentConnectionUpdate `json:"agent"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := r.Context()
	current, currentMedia, currentTTS, _ := a.resolve(ctx)

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

	// 任务③: the spk-agent section. AGENT_BASE_URL pins it read-only.
	if req.Agent != nil {
		if os.Getenv("AGENT_BASE_URL") != "" {
			writeError(w, http.StatusConflict, "agent is configured by environment variables")
			return
		}
		ag := *req.Agent
		ag.BaseURL = strings.TrimSpace(ag.BaseURL)
		ag.Model = strings.TrimSpace(ag.Model)
		ag.AgentRef = strings.TrimSpace(ag.AgentRef)
		if ag.BaseURL != "" && !isHTTPURL(ag.BaseURL) {
			writeError(w, http.StatusBadRequest, "agent base URL must be an http(s) URL")
			return
		}
		// The agent name must match spk-agent's [a-zA-Z0-9_-]+ rule (no
		// dots); the IPD heavies (spk-ipd-*/spk-architect/spk-reporter)
		// are off-limits for third-party dispatch.
		if ag.AgentRef != "" && (len(ag.AgentRef) > 64 || !agentRefOK(ag.AgentRef) || isForbiddenAgentRef(ag.AgentRef)) {
			writeError(w, http.StatusBadRequest, "invalid agent ref")
			return
		}
		values := map[string]string{settingAgentBaseURL: ag.BaseURL, settingAgentModel: ag.Model, settingAgentRef: ag.AgentRef}
		if ag.Token != nil {
			values[settingAgentToken] = strings.TrimSpace(*ag.Token)
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

// agentRefOK checks spk-agent's agent-name rule: [a-zA-Z0-9_-]+.
func agentRefOK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return s != ""
}

// isForbiddenAgentRef keeps the Cortex-only heavy IPD agents out of
// OpenChore dispatch (the dispatch safety boundary).
func isForbiddenAgentRef(s string) bool {
	switch {
	case strings.HasPrefix(s, "spk-ipd-"):
		return true
	case s == "spk-architect", s == "spk-reporter":
		return true
	}
	return false
}

func isHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
