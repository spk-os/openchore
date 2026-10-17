package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
	"github.com/liftedkilt/openchore/internal/store"
)

// L0 multi-file proof upload. The kid's PhotoSheet sends several files in
// one multipart request (files[] field); each lands in data/uploads under
// the same naming scheme as the single-photo channel, gets a
// completion_attachments row, and dedups by sha256 within the completion.
// Caps mirror the product rule: up to 9 images, or 1 video, or 1 PDF —
// a completion carries one kind of proof stream, not a pile of everything.

const (
	maxAttachmentsPerRequest = 10
	maxImageAttachments      = model.MaxImageAttachments
	maxVideoAttachments      = model.MaxVideoAttachments
	maxAudioAttachments      = model.MaxAudioAttachments
	maxPdfAttachments        = model.MaxPdfAttachments
)

// mimeClassForType buckets a detected MIME type into Canvas's mime_class.
func mimeClassForType(mimeType string) string {
	switch mimeType {
	case "video/mp4", "video/webm", "video/quicktime", "video/3gpp":
		return model.MimeClassVideo
	case "audio/webm", "audio/mp4", "audio/mpeg", "audio/mp3", "audio/ogg",
		"audio/wav", "audio/x-wav", "audio/x-m4a", "audio/m4a", "audio/aac",
		"audio/flac", "audio/x-flac", "audio/amr", "audio/3gpp", "audio/mpga":
		return model.MimeClassAudio
	case "application/pdf":
		return model.MimeClassPdf
	default:
		return model.MimeClassImage
	}
}

// extForType maps a detected MIME type to a fallback extension, mirroring
// the map in UploadPhoto (some camera apps strip extensions).
func extForType(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	case "video/3gpp":
		return ".3gp"
	case "audio/webm":
		return ".webm"
	case "audio/mp4", "audio/x-m4a", "audio/m4a", "audio/aac":
		return ".m4a"
	case "audio/mpeg", "audio/mp3", "audio/mpga":
		return ".mp3"
	case "audio/ogg":
		return ".ogg"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/flac", "audio/x-flac":
		return ".flac"
	case "audio/amr":
		return ".amr"
	case "audio/3gpp":
		return ".3gp"
	case "application/pdf":
		return ".pdf"
	default:
		return ".bin"
	}
}

func allowedProofType(mimeType string) bool {
	switch mimeType {
	case "image/jpeg", "image/png", "image/gif", "image/webp",
		"video/mp4", "video/webm", "video/quicktime", "video/3gpp",
		"audio/webm", "audio/mp4", "audio/mpeg", "audio/mp3", "audio/ogg",
		"audio/wav", "audio/x-wav", "audio/x-m4a", "audio/m4a", "audio/aac",
		"audio/flac", "audio/x-flac", "audio/amr", "audio/3gpp", "audio/mpga",
		"application/pdf":
		return true
	default:
		return false
	}
}

// saveProofFile writes one multipart file to disk with a unique
// timestamped name (same scheme as UploadPhoto) and returns its URL path,
// resolved MIME type and sha256 hash. The hash is computed while copying —
// no second read of a possibly-100MB video.
//
// declaredType is the part's Content-Type. It wins whenever it is on the
// whitelist: content sniffing cannot tell an audio/webm recording from a
// video one (both are EBML, Go sniffs both as video/webm), nor m4a audio
// from generic mp4, so the browser's declared type — which comes from the
// MediaRecorder's real mime — is the only audio signal. Anything else
// (octet-stream from file pickers, empty) falls back to sniffing.
func saveProofFile(file io.Reader, filename, declaredType, prefix string) (url, mimeType, sha string, size int64, err error) {
	tmp, err := os.CreateTemp(uploadDir, "att_*")
	if err != nil {
		return "", "", "", 0, err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	// Sniff the first 512 bytes, then stream the rest through the hasher.
	buf := make([]byte, 512)
	n, readErr := io.ReadFull(file, buf)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		err = fmt.Errorf("failed to read file")
		return
	}
	sniff := buf[:n]
	// Trust the part's declared type when the whitelist knows it (strip
	// parameters like ";codecs=opus" first); otherwise sniff the bytes.
	if mt, _, perr := mime.ParseMediaType(declaredType); perr == nil && allowedProofType(mt) {
		mimeType = mt
	} else {
		mimeType = http.DetectContentType(sniff)
	}
	if !allowedProofType(mimeType) {
		err = fmt.Errorf("only photos, videos (MP4, WebM, MOV), audio recordings and PDF files are allowed")
		return
	}

	hasher := sha256.New()
	nn, werr := io.MultiWriter(tmp, hasher).Write(sniff)
	if werr != nil {
		err = werr
		return
	}
	size = int64(nn)
	rest, copyErr := io.Copy(io.MultiWriter(tmp, hasher), file)
	if copyErr != nil {
		err = copyErr
		return
	}
	size += rest
	sha = hex.EncodeToString(hasher.Sum(nil))

	ext := filepath.Ext(filename)
	if ext == "" {
		ext = extForType(mimeType)
	}
	stored := fmt.Sprintf("%d_%s%s", time.Now().UnixNano(), prefix, ext)
	if cerr := tmp.Close(); cerr != nil {
		err = cerr
		return
	}
	if rerr := os.Rename(tmpName, filepath.Join(uploadDir, stored)); rerr != nil {
		err = rerr
		return
	}
	return "/uploads/" + stored, mimeType, sha, size, nil
}

// UploadAttachments receives a multipart batch of proof files for one
// completion and records them as attachments. Files that duplicate an
// already-attached file (same sha256) are skipped, not errors — the kid
// re-tapping "add" on the same photo is normal behavior.
func (h *ChoreHandler) UploadAttachments(w http.ResponseWriter, r *http.Request) {
	completionID, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid completion id")
		return
	}
	completion, err := h.store.GetCompletion(r.Context(), completionID)
	if err != nil || completion == nil {
		writeError(w, http.StatusNotFound, "completion not found")
		return
	}
	// Only the assignee (or a parent) may add proof, and only while the
	// completion is still pending: approved history is immutable.
	schedule, _ := h.store.GetSchedule(r.Context(), completion.ChoreScheduleID)
	if schedule == nil || !canActOnSchedule(UserFromContext(r.Context()), schedule) {
		writeError(w, http.StatusForbidden, "this chore is assigned to someone else")
		return
	}
	if completion.UncompletedAt != nil || completion.Status != model.StatusPending {
		writeError(w, http.StatusBadRequest, "attachments can only be added while the completion is pending")
		return
	}

	const maxUpload = 100 << 20 // per request, matching the single-photo channel
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "request too large (max 100MB)")
		return
	}

	// Clients send files under "files[]" (HTML multiple-input convention)
	// or plain "files"; single-file clients may use the legacy "photo".
	files := r.MultipartForm.File["files[]"]
	if len(files) == 0 {
		files = r.MultipartForm.File["files"]
	}
	if len(files) == 0 {
		if f := r.MultipartForm.File["photo"]; len(f) > 0 {
			files = f
		}
	}
	if len(files) == 0 {
		writeError(w, http.StatusBadRequest, "no files provided")
		return
	}
	if len(files) > maxAttachmentsPerRequest {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("too many files in one request (max %d)", maxAttachmentsPerRequest))
		return
	}

	// Seed the legacy single photo as attachment #1 so the grid starts from
	// what the kid already sent (idempotent; duplicates are skipped).
	if completion.PhotoURL != "" {
		_ = h.store.SeedAttachmentFromCompletionPhoto(r.Context(), completionID, completion.PhotoURL, "legacy_"+completion.PhotoURL)
	}

	images, videos, audios, pdfs, err := h.store.CountCompletionAttachmentsByClass(r.Context(), completionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count attachments")
		return
	}

	existing, err := h.store.ListCompletionAttachments(r.Context(), completionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list attachments")
		return
	}
	sortOrder := len(existing)

	var added []model.CompletionAttachment
	var skipped int
	for _, fh := range files {
		file, err := fh.Open()
		if err != nil {
			writeError(w, http.StatusBadRequest, "failed to read file")
			return
		}
		// fh is a *multipart.FileHeader; its name is kid-visible. Its
		// Content-Type is the browser's — the audio/video tell sniffing
		// can't make (a webm recording's container is video to the sniffer).
		urlPath, mimeType, sha, size, err := saveProofFile(file, fh.Filename, fh.Header.Get("Content-Type"), "upload")
		file.Close()
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		class := mimeClassForType(mimeType)
		switch class {
		case model.MimeClassImage:
			if images >= maxImageAttachments {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("too many photos (max %d)", maxImageAttachments))
				return
			}
		case model.MimeClassVideo:
			if videos >= maxVideoAttachments {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("only one video is allowed per completion"))
				return
			}
		case model.MimeClassAudio:
			if audios >= maxAudioAttachments {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("only one audio recording is allowed per completion"))
				return
			}
		case model.MimeClassPdf:
			if pdfs >= maxPdfAttachments {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("only one PDF is allowed per completion"))
				return
			}
		}
		att := &model.CompletionAttachment{
			CompletionID: completionID,
			FilePath:     urlPath,
			DisplayName:  sanitizeDisplayName(fh.Filename),
			MimeType:     mimeType,
			SizeBytes:    size,
			MimeClass:    class,
			SHA256:       sha,
			SortOrder:    sortOrder,
		}
		if err := h.store.AddCompletionAttachment(r.Context(), att); err != nil {
			if errors.Is(err, store.ErrDuplicateAttachment) {
				// Same content twice: keep one copy, report the skip.
				skipped++
				_ = os.Remove(strings.TrimPrefix(urlPath, "/uploads/"))
				continue
			}
			writeError(w, http.StatusInternalServerError, "failed to save attachment")
			return
		}
		switch class {
		case model.MimeClassImage:
			images++
		case model.MimeClassVideo:
			videos++
		case model.MimeClassAudio:
			audios++
		case model.MimeClassPdf:
			pdfs++
		}
		sortOrder++
		added = append(added, *att)
	}

	// The first image becomes the completion's photo_url (list-view
	// convenience; the attachments table remains the source of truth).
	if completion.PhotoURL == "" && len(added) > 0 && added[0].MimeClass == model.MimeClassImage {
		_ = h.store.UpdateCompletionPhotoOnly(r.Context(), completionID, added[0].FilePath)
	}

	// New proof means the reviewer should look again.
	if len(added) > 0 {
		h.queueReview(completionID)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"added":   added,
		"skipped": skipped,
		"count":   len(added),
	})
}

// sanitizeDisplayName keeps the kid's filename recognizable but harmless.
func sanitizeDisplayName(name string) string {
	name = filepath.Base(name)
	if name == "." || name == "/" || name == "" {
		return "attachment"
	}
	if len(name) > 120 {
		name = name[len(name)-120:]
	}
	return name
}

// ListAttachments returns a completion's live attachments for the parent's
// grid view.
func (h *ChoreHandler) ListAttachments(w http.ResponseWriter, r *http.Request) {
	completionID, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid completion id")
		return
	}
	completion, err := h.store.GetCompletion(r.Context(), completionID)
	if err != nil || completion == nil {
		writeError(w, http.StatusNotFound, "completion not found")
		return
	}
	atts, err := h.store.ListCompletionAttachments(r.Context(), completionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list attachments")
		return
	}
	if atts == nil {
		atts = []model.CompletionAttachment{}
	}
	writeJSON(w, http.StatusOK, atts)
}

// DeleteAttachment soft-deletes one proof file (admin only). The row and
// the file on disk survive; the grid simply hides it.
func (h *ChoreHandler) DeleteAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attachment id")
		return
	}
	att, err := h.store.GetCompletionAttachment(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get attachment")
		return
	}
	if att == nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	if err := h.store.SoftDeleteAttachment(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete attachment")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetAttachmentFile streams one attachment's file after auth. The stored
// file_path is always "/uploads/<name>"; resolveUploadPath keeps the lookup
// inside the uploads directory.
func (h *ChoreHandler) GetAttachmentFile(w http.ResponseWriter, r *http.Request) {
	id, err := urlParamInt64(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attachment id")
		return
	}
	att, err := h.store.GetCompletionAttachment(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get attachment")
		return
	}
	if att == nil || att.DeletedAt != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	path, err := resolveUploadPath(att.FilePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment file not found")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment file not found")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", att.MimeType)
	if att.MimeClass != model.MimeClassImage {
		// Videos/PDFs stream better with inline disposition hints.
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", att.DisplayName))
	}
	http.ServeContent(w, r, "", time.Time{}, f)
}
