package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// The kid's pet (P4-13): the "emotional balance" layer. One named
// companion per kid, fed with points (a second place for points to go),
// grown through cumulative feeding, never dying — the worst mood is
// hungry. The whole feature is gated by the pet_enabled app setting
// (default off): parents opt in per kid's age and temperament.

// petEnabledSetting gates the whole feature ("true"/"false").
const petEnabledSetting = "pet_enabled"

// PetHandler serves the kid's pet: state (with the computed mood), naming
// and feeding.
type PetHandler struct {
	store *store.Store
}

func NewPetHandler(s *store.Store) *PetHandler {
	return &PetHandler{store: s}
}

// petEnabled reports whether the pet layer is turned on for this
// household.
func (h *PetHandler) petEnabled(r *http.Request) bool {
	on, _ := h.store.GetSetting(r.Context(), petEnabledSetting)
	return on == "true"
}

// petState is the pet plus its server-computed mood — the single payload
// the kid's client renders.
type petState struct {
	model.Pet
	Mood          string `json:"mood"`
	NextStageAt   int    `json:"next_stage_at"`
	StageProgress int    `json:"stage_progress"` // 0-100 toward the next stage
}

// GetPet returns the caller's pet with its computed mood, or
// {"hatched": false} when they haven't hatched one yet. Feature off →
// 404 so the client can hide the whole layer.
func (h *PetHandler) GetPet(w http.ResponseWriter, r *http.Request) {
	viewer := UserFromContext(r.Context())
	if !h.petEnabled(r) {
		writeError(w, http.StatusNotFound, "pet feature is not enabled")
		return
	}
	pet, err := h.store.GetPet(r.Context(), viewer.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load pet")
		return
	}
	if pet == nil {
		writeJSON(w, http.StatusOK, map[string]any{"hatched": false})
		return
	}
	writeJSON(w, http.StatusOK, h.state(r, pet))
}

// HatchPet names and hatches the caller's pet.
func (h *PetHandler) HatchPet(w http.ResponseWriter, r *http.Request) {
	viewer := UserFromContext(r.Context())
	if !h.petEnabled(r) {
		writeError(w, http.StatusNotFound, "pet feature is not enabled")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len([]rune(req.Name)) > 24 || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name must be 1-24 characters")
		return
	}
	pet, err := h.store.SavePetName(r.Context(), viewer.ID, req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hatch pet")
		return
	}
	writeJSON(w, http.StatusCreated, h.state(r, pet))
}

// FeedPet spends points on pet food: one ledger debit (reason='pet_feed')
// plus the fed_value bump and possible stage-up, all in one transaction.
func (h *PetHandler) FeedPet(w http.ResponseWriter, r *http.Request) {
	viewer := UserFromContext(r.Context())
	if !h.petEnabled(r) {
		writeError(w, http.StatusNotFound, "pet feature is not enabled")
		return
	}
	pet, err := h.store.GetPet(r.Context(), viewer.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load pet")
		return
	}
	if pet == nil {
		writeError(w, http.StatusBadRequest, "no pet to feed")
		return
	}
	pet, err = h.store.FeedPet(r.Context(), viewer.ID, model.FeedPetCost)
	if err != nil {
		if errors.Is(err, store.ErrInsufficientPoints) {
			writeError(w, http.StatusUnprocessableEntity, "not enough points")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to feed pet")
		return
	}
	writeJSON(w, http.StatusOK, h.state(r, pet))
}

// GetPetForAdmin returns any kid's pet for the parent dashboard's status
// card (the prototype's "stat-pet" line). Same shape as GetPet: 404 when
// the feature is off, {"hatched": false} when the kid hasn't hatched one.
func (h *PetHandler) GetPetForAdmin(w http.ResponseWriter, r *http.Request) {
	userID, err := urlParamInt64(r, "userID")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	if !h.petEnabled(r) {
		writeError(w, http.StatusNotFound, "pet feature is not enabled")
		return
	}
	pet, err := h.store.GetPet(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load pet")
		return
	}
	if pet == nil {
		writeJSON(w, http.StatusOK, map[string]any{"hatched": false})
		return
	}
	writeJSON(w, http.StatusOK, h.state(r, pet))
}

// state computes the mood and stage progress for a stored pet.
func (h *PetHandler) state(r *http.Request, pet *model.Pet) petState {
	mood, err := h.store.PetMood(r.Context(), pet.UserID, time.Now().Format(model.DateFormat))
	if err != nil || mood == "" {
		mood = model.PetMoodCalm
	}
	nextAt := 0
	progress := 100
	switch {
	case pet.FedValue < model.PetStage2FedValue:
		nextAt = model.PetStage2FedValue
		progress = pet.FedValue * 100 / model.PetStage2FedValue
	case pet.FedValue < model.PetStage3FedValue:
		nextAt = model.PetStage3FedValue
		progress = (pet.FedValue - model.PetStage2FedValue) * 100 / (model.PetStage3FedValue - model.PetStage2FedValue)
	}
	return petState{Pet: *pet, Mood: mood, NextStageAt: nextAt, StageProgress: progress}
}
