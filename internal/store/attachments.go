package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/liftedkilt/openchore/internal/model"
)

// Completion attachments (L0). A completion's proof files live here; the
// completion's photo_url column stays as the first-image convenience.
// Duplicate files within one completion are rejected by the (completion_id,
// sha256) unique index — the kid re-tapping "add" on the same photo is the
// common case, and silently keeping one copy beats an error in their face.

// ErrDuplicateAttachment means the exact same file (same sha256) is already
// attached to this completion.
var ErrDuplicateAttachment = errors.New("attachment already exists on this completion")

const attachmentColumns = `id, completion_id, file_path, display_name, mime_type, size_bytes, mime_class, thumbnail_path, sha256, sort_order, deleted_at, ai_feedback, ai_confidence, ai_complete, created_at`

func scanAttachment(row interface{ Scan(...any) error }) (*model.CompletionAttachment, error) {
	a := &model.CompletionAttachment{}
	var thumb sql.NullString
	if err := row.Scan(&a.ID, &a.CompletionID, &a.FilePath, &a.DisplayName, &a.MimeType,
		&a.SizeBytes, &a.MimeClass, &thumb, &a.SHA256, &a.SortOrder, &a.DeletedAt,
		&a.AIFeedback, &a.AIConfidence, &a.AIComplete, &a.CreatedAt); err != nil {
		return nil, err
	}
	if thumb.Valid {
		a.ThumbnailPath = thumb.String
	}
	return a, nil
}

// AddCompletionAttachment stores one proof file row. sort_order positions
// it in the kid's chosen order (photos first, then video, then PDF).
func (s *Store) AddCompletionAttachment(ctx context.Context, a *model.CompletionAttachment) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO completion_attachments (completion_id, file_path, display_name, mime_type, size_bytes, mime_class, thumbnail_path, sha256, sort_order)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.CompletionID, a.FilePath, a.DisplayName, a.MimeType, a.SizeBytes, a.MimeClass, nullString(a.ThumbnailPath), a.SHA256, a.SortOrder)
	if err != nil {
		// Same sha256 already on this completion — the (completion_id,
		// sha256) unique index keeps it to one copy.
		if isUniqueConstraintErr(err) {
			return ErrDuplicateAttachment
		}
		return err
	}
	a.ID, _ = res.LastInsertId()
	return nil
}

// ListCompletionAttachments returns a completion's live attachments in
// display order (soft-deleted rows hidden). Including deleted ones is an
// admin-only concern; the default view is what the kid handed in now.
func (s *Store) ListCompletionAttachments(ctx context.Context, completionID int64) ([]model.CompletionAttachment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+attachmentColumns+`
		 FROM completion_attachments
		 WHERE completion_id = ? AND deleted_at IS NULL
		 ORDER BY sort_order, id`, completionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.CompletionAttachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// GetCompletionAttachment fetches one attachment row regardless of soft
// delete, so an admin can still see what they removed.
func (s *Store) GetCompletionAttachment(ctx context.Context, id int64) (*model.CompletionAttachment, error) {
	a, err := scanAttachment(s.db.QueryRowContext(ctx,
		`SELECT `+attachmentColumns+` FROM completion_attachments WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return a, err
}

// SoftDeleteAttachment hides an attachment without destroying it — a
// parent's removal must be reversible (Canvas keeps submissions too).
func (s *Store) SoftDeleteAttachment(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE completion_attachments SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
		time.Now().UTC(), id)
	return err
}

// CountCompletionAttachmentsByClass returns how many live attachments of
// each mime class a completion holds, for the upload caps (9 images / 1
// video / 1 audio / 1 PDF).
func (s *Store) CountCompletionAttachmentsByClass(ctx context.Context, completionID int64) (images, videos, audios, pdfs int, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT mime_class, COUNT(*) FROM completion_attachments
		 WHERE completion_id = ? AND deleted_at IS NULL
		 GROUP BY mime_class`, completionID)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var class string
		var n int
		if err := rows.Scan(&class, &n); err != nil {
			return 0, 0, 0, 0, err
		}
		switch class {
		case model.MimeClassImage:
			images = n
		case model.MimeClassVideo:
			videos = n
		case model.MimeClassAudio:
			audios = n
		case model.MimeClassPdf:
			pdfs = n
		}
	}
	return images, videos, audios, pdfs, rows.Err()
}

// ReplaceCompletionAttachmentsFromPhoto is the migration shim for legacy
// single-photo completions: when the first multi-file upload arrives for a
// completion that only ever had its photo_url, that photo becomes
// attachment #1 so the grid starts from what the kid already sent.
func (s *Store) SeedAttachmentFromCompletionPhoto(ctx context.Context, completionID int64, photoURL, sha string) error {
	if photoURL == "" {
		return nil
	}
	var existing int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM completion_attachments WHERE completion_id = ? AND file_path = ?`,
		completionID, photoURL).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO completion_attachments (completion_id, file_path, display_name, mime_type, size_bytes, mime_class, sha256, sort_order)
		 VALUES (?, ?, ?, ?, 0, ?, ?, 0)`,
		completionID, photoURL, displayNameFromPath(photoURL), mimeTypeFromPath(photoURL), mimeClassFromPath(photoURL), sha)
	return err
}

// SetAttachmentAIReview records the reviewer's per-attachment verdict.
func (s *Store) SetAttachmentAIReview(ctx context.Context, attachmentID int64, r model.AIReviewResult) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE completion_attachments SET ai_feedback = ?, ai_confidence = ?, ai_complete = ? WHERE id = ?`,
		r.Feedback, r.Confidence, r.Complete, attachmentID)
	return err
}

// nullString maps "" to SQL NULL for the optional thumbnail_path column.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// mimeTypeFromPath infers a MIME type from a stored upload path's
// extension — mirrors the whitelist extension map in api/uploads.go.
func mimeTypeFromPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".3gp":
		return "video/3gpp"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

// mimeClassFromPath buckets a stored upload path for rendering.
func mimeClassFromPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return model.MimeClassImage
	case ".mp4", ".webm", ".mov", ".3gp":
		return model.MimeClassVideo
	case ".pdf":
		return model.MimeClassPdf
	default:
		return model.MimeClassImage
	}
}

// displayNameFromPath derives a kid-friendly name from a stored path —
// just the filename, since uploads are content-addressed by timestamp.
func displayNameFromPath(path string) string {
	return filepath.Base(path)
}
