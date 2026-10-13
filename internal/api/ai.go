package api

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/tts"
)

// defaultAutoApproveThreshold is the confidence the reviewer must reach
// before a photo is auto-approved (when auto-approval is switched on).
const defaultAutoApproveThreshold = 0.85

// isImageProof reports whether a stored proof URL points at an image the
// AI reviewer can look at. Videos and PDFs are parent-review only.
func isImageProof(photoURL string) bool {
	switch strings.ToLower(filepath.Ext(photoURL)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	default:
		return false
	}
}

// queueReview runs the AI photo review for a pending completion in the
// background. The kid is never kept waiting on it and it never rejects:
// it leaves a note for the parent and, if enabled, approves clear passes.
func (h *ChoreHandler) queueReview(completionID int64) {
	if h.aiSvc.AI() == nil {
		return
	}
	h.reviews.Add(1)
	go func() {
		defer h.reviews.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		h.reviewCompletion(ctx, completionID)
	}()
}

func (h *ChoreHandler) reviewCompletion(ctx context.Context, completionID int64) {
	if on, _ := h.store.GetSetting(ctx, "ai_photo_review"); on != "true" {
		return
	}
	c, err := h.store.GetCompletion(ctx, completionID)
	if err != nil || c == nil || c.Status != model.StatusPending {
		return
	}

	// L0: the attachments table is the source of truth for the proof set;
	// the legacy photo_url column is the first-image convenience. Review
	// every image attachment (AI verdict per attachment), then roll the
	// verdicts up into the completion-level note. Videos and PDFs are
	// parent-review only, as before.
	attachments, err := h.store.ListCompletionAttachments(ctx, completionID)
	if err != nil {
		log.Printf("ai: listing attachments of completion %d: %v", completionID, err)
		return
	}
	var images []model.CompletionAttachment
	for _, a := range attachments {
		if a.MimeClass == model.MimeClassImage {
			images = append(images, a)
		}
	}
	if len(images) == 0 && c.PhotoURL == "" {
		return
	}
	// A legacy single-photo completion (no attachments yet): seed the
	// column photo as attachment #1 so the per-attachment path handles
	// both worlds with one code path.
	if len(images) == 0 && c.PhotoURL != "" && isImageProof(c.PhotoURL) {
		_ = h.store.SeedAttachmentFromCompletionPhoto(ctx, completionID, c.PhotoURL, "legacy_"+c.PhotoURL)
		attachments, err = h.store.ListCompletionAttachments(ctx, completionID)
		if err != nil {
			return
		}
		for _, a := range attachments {
			if a.MimeClass == model.MimeClassImage {
				images = append(images, a)
			}
		}
	}
	if len(images) == 0 {
		return
	}

	var title, description, refPhotoPath string
	if schedule, _ := h.store.GetSchedule(ctx, c.ChoreScheduleID); schedule != nil {
		if chore, _ := h.store.GetChore(ctx, schedule.ChoreID); chore != nil {
			title, description = chore.Title, chore.Description
			if chore.ReferencePhotoURL != "" {
				if p, err := resolveUploadPath(chore.ReferencePhotoURL); err == nil {
					refPhotoPath = p
				}
			}
		}
	}

	ai := h.aiSvc.AI()
	if ai == nil {
		return
	}

	// Per-attachment review (Canvas's per-submission-comment model): each
	// image gets its own verdict, stored on the attachment row, and the
	// completion-level rollup (worst confidence, joint feedback) drives
	// auto-approve exactly as the single-photo path did.
	start := time.Now()
	var results []model.AttachmentReviewResult
	for _, att := range images {
		photoPath, err := resolveUploadPath(att.FilePath)
		if err != nil {
			log.Printf("ai: skipping review of attachment %d: %v", att.ID, err)
			continue
		}
		result, err := ai.ReviewPhoto(ctx, title, description, photoPath, refPhotoPath)
		if err != nil {
			log.Printf("ai: review of attachment %d (completion %d) failed: %v", att.ID, completionID, err)
			continue
		}
		_ = h.store.SetAttachmentAIReview(ctx, att.ID, *result)
		results = append(results, model.AttachmentReviewResult{AttachmentID: att.ID, Review: *result})
	}
	if len(results) == 0 {
		return
	}
	result := rollupAttachmentReviews(results)
	log.Printf("ai: reviewed completion %d (%d image%s) in %s — complete=%v confidence=%.2f",
		completionID, len(results), plural(len(results)), time.Since(start).Round(time.Millisecond), result.Complete, result.Confidence)

	// The photo may have been replaced, or the chore approved or
	// unchecked, while the model was thinking.
	cur, err := h.store.GetCompletion(ctx, completionID)
	if err != nil || cur == nil || cur.UncompletedAt != nil || cur.Status != model.StatusPending {
		return
	}
	if err := h.store.SetCompletionAIReview(ctx, completionID, result); err != nil {
		log.Printf("ai: saving review of completion %d: %v", completionID, err)
		return
	}
	if !h.shouldAutoApprove(ctx, &result) {
		return
	}
	if err := h.approveCompletion(ctx, cur, nil); err != nil {
		log.Printf("ai: auto-approving completion %d: %v", completionID, err)
		return
	}
	log.Printf("ai: auto-approved completion %d", completionID)
}

// rollupAttachmentReviews folds per-image verdicts into one completion-
// level verdict: complete only if every reviewed image passed, confidence
// is the weakest link, feedback stitches the notes in order.
func rollupAttachmentReviews(results []model.AttachmentReviewResult) model.AIReviewResult {
	rollup := model.AIReviewResult{Complete: true, Confidence: 1.0}
	var parts []string
	for i, r := range results {
		if !r.Review.Complete {
			rollup.Complete = false
		}
		if r.Review.Confidence < rollup.Confidence {
			rollup.Confidence = r.Review.Confidence
		}
		if r.Review.Feedback != "" {
			if i > 0 {
				parts = append(parts, "；")
			}
			parts = append(parts, r.Review.Feedback)
		}
	}
	rollup.Feedback = strings.Join(parts, "")
	if rollup.Feedback == "" {
		if rollup.Complete {
			rollup.Feedback = "all proof images passed review"
		} else {
			rollup.Feedback = "one or more proof images did not pass review"
		}
	}
	return rollup
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (h *ChoreHandler) shouldAutoApprove(ctx context.Context, r *model.AIReviewResult) bool {
	if on, _ := h.store.GetSetting(ctx, "ai_auto_approve"); on != "true" || !r.Complete {
		return false
	}
	threshold := defaultAutoApproveThreshold
	if v, _ := h.store.GetSetting(ctx, "ai_auto_approve_threshold"); v != "" {
		if t, err := strconv.ParseFloat(v, 64); err == nil && t > 0 && t <= 1 {
			threshold = t
		}
	}
	return r.Confidence >= threshold
}

// AIStatus reports which optional AI services are configured, so the admin
// UI can hide what isn't available.
func (h *ChoreHandler) AIStatus(w http.ResponseWriter, r *http.Request) {
	ai, audio := h.aiSvc.AI(), h.aiSvc.Audio()
	resp := map[string]any{
		"ai":  map[string]any{"configured": false},
		"tts": map[string]any{"configured": false},
	}
	if ai != nil {
		resp["ai"] = map[string]any{"configured": true, "model": ai.Model()}
	}
	if audio != nil {
		resp["tts"] = map[string]any{"configured": true, "model": audio.Client().Model()}
	}
	writeJSON(w, http.StatusOK, resp)
}

// TestAIReview runs the photo reviewer on an uploaded photo so a parent can
// see how the configured model judges it. Nothing is saved.
func (h *ChoreHandler) TestAIReview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChoreTitle string `json:"chore_title"`
		PhotoURL   string `json:"photo_url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ChoreTitle == "" || req.PhotoURL == "" {
		writeError(w, http.StatusBadRequest, "chore_title and photo_url are required")
		return
	}
	ai := h.aiSvc.AI()
	if ai == nil {
		writeError(w, http.StatusServiceUnavailable, "AI is not configured")
		return
	}
	photoPath, err := resolveUploadPath(req.PhotoURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid photo_url")
		return
	}

	start := time.Now()
	result, err := ai.ReviewPhoto(r.Context(), req.ChoreTitle, "", photoPath, "")
	if err != nil {
		writeError(w, http.StatusBadGateway, "AI review failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"complete":      result.Complete,
		"confidence":    result.Confidence,
		"feedback":      result.Feedback,
		"would_approve": h.shouldAutoApprove(r.Context(), result),
		"elapsed_ms":    time.Since(start).Milliseconds(),
	})
}

// GenerateDescription drafts a kid-friendly chore description.
func (h *ChoreHandler) GenerateDescription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title    string `json:"title"`
		Category string `json:"category"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	ai := h.aiSvc.AI()
	if ai == nil {
		writeError(w, http.StatusServiceUnavailable, "AI is not configured")
		return
	}
	desc, err := ai.DraftDescription(r.Context(), req.Title, req.Category)
	if err != nil {
		writeError(w, http.StatusBadGateway, "AI generation failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"description": desc})
}

// RegenerateChoreTTS re-synthesizes one chore's read-aloud audio.
func (h *ChoreHandler) RegenerateChoreTTS(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid chore id")
		return
	}
	audio := h.aiSvc.Audio()
	if audio == nil {
		writeError(w, http.StatusServiceUnavailable, "text-to-speech is not configured")
		return
	}
	chore, err := h.store.GetChore(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get chore")
		return
	}
	if chore == nil {
		writeError(w, http.StatusNotFound, "chore not found")
		return
	}
	audioURL, err := audio.Generate(r.Context(), chore)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to synthesize audio: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"tts_audio_url": audioURL})
}

// RegenerateAllTTS re-synthesizes every chore's audio in the background,
// e.g. after the voice changes.
func (h *ChoreHandler) RegenerateAllTTS(w http.ResponseWriter, r *http.Request) {
	audio := h.aiSvc.Audio()
	if audio == nil {
		writeError(w, http.StatusServiceUnavailable, "text-to-speech is not configured")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		audio.Sync(ctx, true)
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "regenerating"})
}

// ttsFileServer serves chore audio from tts.Dir.
func ttsFileServer() http.Handler {
	_ = os.MkdirAll(tts.Dir, 0750)
	return http.StripPrefix("/tts/", http.FileServer(http.Dir(tts.Dir)))
}
