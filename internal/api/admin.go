package api

import (
	"net/http"
	"strings"

	"github.com/liftedkilt/openchore/internal/config"
	"github.com/liftedkilt/openchore/internal/store"
	"github.com/liftedkilt/openchore/internal/webhook"
)

// writableSettings is the allowlist of setting keys that can be written via
// the admin API.  Keys not in this set are rejected with 400.
var writableSettings = map[string]bool{
	"ai_photo_review":           true,
	"ai_auto_approve":           true,
	"ai_auto_approve_threshold": true,
	"ai_weekly_summary":         true,
	"ai_monthly_summary":        true,
	"ai_daily_coach":            true,
	"ai_auto_paper_analysis":    true,
	"coach_time":                true,
	"weekly_time":               true,
	"monthly_time":              true,
	"tts_voice":                 true,
	"base_url":                  true,
	"discord_webhook_url":       true,
	"pet_enabled":               true,
	"bonus_drop_enabled":        true,
	"fsrs_daily_new_cards":      true,
	// The L0-L4 master switches (settings-l0l4): one per feature row.
	"feature_errorbook": true,
	"feature_fsrs":      true,
	"feature_profile":   true,
	// The video/audio proof reviewer connection (defaults to the main AI
	// gateway with agnes-video-2.5-flash; see aiconfig.go resolveMedia).
	"media_model":    true,
	"media_base_url": true,
	"media_api_key":  true,
	// 任务③ spk-agent 接入：agent 连接与各任务的 prompt 模板（界面可配）。
	"agent_base_url": true,
	"agent_token":    true,
	"agent_model":    true,
	"agent_ref":      true,
	// 任务④ 可解释性：文本 AI 任务的 prompt 模板（{{placeholders}} 由
	// 后端替换；空值用内置默认）。
	"prompt_planner":   true,
	"prompt_summary":   true,
	"prompt_measures":  true,
	"prompt_copilot":   true,
	"prompt_scaffold":  true,
	"prompt_strengthen": true,
}

type AdminHandler struct {
	store      *store.Store
	dispatcher *webhook.Dispatcher
}

func NewAdminHandler(s *store.Store, dispatcher *webhook.Dispatcher) *AdminHandler {
	return &AdminHandler{store: s, dispatcher: dispatcher}
}

// secretSettings are never readable through the settings API.
var secretSettings = map[string]bool{
	legacyPasscodeSetting: true,
	sessionSecretSetting:  true,
	settingAIAPIKey:       true,
	settingTTSAPIKey:      true,
	settingAgentToken:     true,
}

func (h *AdminHandler) GetSetting(w http.ResponseWriter, r *http.Request) {
	key := urlParam(r, "key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key required")
		return
	}
	if secretSettings[key] {
		writeError(w, http.StatusForbidden, "setting is not readable")
		return
	}
	val, err := h.store.GetSetting(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get setting")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": val})
}

func (h *AdminHandler) SetSetting(w http.ResponseWriter, r *http.Request) {
	key := urlParam(r, "key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key required")
		return
	}
	if !writableSettings[key] {
		writeError(w, http.StatusBadRequest, "unknown setting key")
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.store.SetSetting(r.Context(), key, req.Value); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update setting")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": req.Value})
}

func (h *AdminHandler) ExportConfig(w http.ResponseWriter, r *http.Request) {
	allSections := []string{"users", "chores", "rewards", "streak_rewards", "settings"}
	sections := allSections
	if q := r.URL.Query().Get("sections"); q != "" {
		sections = strings.Split(q, ",")
	}

	cfg, err := config.Export(r.Context(), h.store, sections)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to export config")
		return
	}

	data, err := config.Marshal(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to marshal config")
		return
	}

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="config.yaml"`)
	w.Write(data)
}
