package store

import (
	"context"
	"time"
)

// The parent-side media library: every proof file a kid has uploaded,
// browsable by subject / media class / date / chore — the volume answer to
// "photos, videos and recordings are piling up, how do I find Friday's
// math recording". One SQL view over completion_attachments joined to the
// chore (its subject) and the kid; the filters are query parameters on the
// admin endpoint, not separate tables.

// MediaItem is one proof file in the media library.
type MediaItem struct {
	AttachmentID int64     `json:"attachment_id"`
	CompletionID int64     `json:"completion_id"`
	FilePath     string    `json:"file_path"`
	DisplayName  string    `json:"display_name"`
	MimeType     string    `json:"mime_type"`
	MimeClass    string    `json:"mime_class"`
	SizeBytes    int64     `json:"size_bytes"`
	CreatedAt    time.Time `json:"created_at"`
	// The chore the proof belongs to (its subject drives the subject
	// filter; a NULL join row means the chore was deleted since).
	ChoreID    int64   `json:"chore_id"`
	ChoreTitle string  `json:"chore_title"`
	Subject    string  `json:"subject"`
	Category   string  `json:"category"`
	// The kid who handed it in.
	UserID   int64  `json:"user_id"`
	UserName string `json:"user_name"`
	// Completion state, so the library can also answer "still unreviewed".
	Status string `json:"status"`
	// The AI verdict when a review ran (per-attachment fields).
	AIFeedback   string   `json:"ai_feedback,omitempty"`
	AIConfidence float64  `json:"ai_confidence,omitempty"`
	AIComplete   *bool    `json:"ai_complete,omitempty"`
}

const mediaColumns = `ca.id, ca.completion_id, ca.file_path, ca.display_name, ca.mime_type, ca.mime_class, ca.size_bytes, ca.created_at,
	c.id, c.title, COALESCE(c.subject, ''), c.category,
	u.id, u.name, cc.status,
	ca.ai_feedback, ca.ai_confidence, ca.ai_complete`

func scanMediaItem(row interface{ Scan(...any) error }) (*MediaItem, error) {
	m := &MediaItem{}
	if err := row.Scan(&m.AttachmentID, &m.CompletionID, &m.FilePath, &m.DisplayName, &m.MimeType, &m.MimeClass, &m.SizeBytes, &m.CreatedAt,
		&m.ChoreID, &m.ChoreTitle, &m.Subject, &m.Category,
		&m.UserID, &m.UserName, &m.Status,
		&m.AIFeedback, &m.AIConfidence, &m.AIComplete); err != nil {
		return nil, err
	}
	return m, nil
}

// ListMediaItems is the media library query. Every filter is optional
// (empty/zero means "no constraint"); results are newest first, capped at
// limit (default/ceiling applied by the caller).
func (s *Store) ListMediaItems(ctx context.Context, subject, mimeClass, dateFrom, dateTo string, choreID, userID int64, limit int) ([]MediaItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+mediaColumns+`
		FROM completion_attachments ca
		JOIN chore_completions cc ON cc.id = ca.completion_id
		JOIN users u ON u.id = cc.completed_by
		LEFT JOIN chore_schedules cs ON cs.id = cc.chore_schedule_id
		LEFT JOIN chores c ON c.id = cs.chore_id
		WHERE ca.deleted_at IS NULL
		  AND (? = '' OR COALESCE(c.subject, '') = ?)
		  AND (? = '' OR ca.mime_class = ?)
		  AND (date(ca.created_at) >= ? OR ? = '')
		  AND (date(ca.created_at) <= ? OR ? = '')
		  AND (? = 0 OR c.id = ?)
		  AND (? = 0 OR u.id = ?)
		ORDER BY ca.created_at DESC
		LIMIT ?`,
		subject, subject, mimeClass, mimeClass,
		dateFrom, dateFrom, dateTo, dateTo,
		choreID, choreID, userID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]MediaItem, 0, 32)
	for rows.Next() {
		m, err := scanMediaItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *m)
	}
	return items, rows.Err()
}

// MediaSubjectStat is one subject's file count in the library.
type MediaSubjectStat struct {
	Subject string `json:"subject"` // "" groups the unclassified
	Count   int    `json:"count"`
}

// MediaClassStat is one media class's file count in the library.
type MediaClassStat struct {
	MimeClass string `json:"mime_class"`
	Count     int    `json:"count"`
}

// MediaLibraryStats summarizes the whole library (subject / class
// breakdowns for the filter chips) under the same filters except the
// dimension being reported.
func (s *Store) MediaLibraryStats(ctx context.Context, mimeClass, dateFrom, dateTo string, userID int64) (subjects []MediaSubjectStat, classes []MediaClassStat, total int, err error) {
	filter := `
		WHERE ca.deleted_at IS NULL
		  AND (? = '' OR ca.mime_class = ?)
		  AND (date(ca.created_at) >= ? OR ? = '')
		  AND (date(ca.created_at) <= ? OR ? = '')
		  AND (? = 0 OR u.id = ?)`
	args := []any{mimeClass, mimeClass, dateFrom, dateFrom, dateTo, dateTo, userID, userID}

	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(c.subject, '') AS subject, COUNT(*)
		FROM completion_attachments ca
		JOIN chore_completions cc ON cc.id = ca.completion_id
		JOIN users u ON u.id = cc.completed_by
		LEFT JOIN chore_schedules cs ON cs.id = cc.chore_schedule_id
		LEFT JOIN chores c ON c.id = cs.chore_id`+filter+`
		GROUP BY subject ORDER BY COUNT(*) DESC, subject ASC`, args...)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var st MediaSubjectStat
		if err := rows.Scan(&st.Subject, &st.Count); err != nil {
			return nil, nil, 0, err
		}
		subjects = append(subjects, st)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, 0, err
	}

	rows2, err := s.db.QueryContext(ctx, `
		SELECT ca.mime_class, COUNT(*)
		FROM completion_attachments ca
		JOIN chore_completions cc ON cc.id = ca.completion_id
		JOIN users u ON u.id = cc.completed_by
		LEFT JOIN chore_schedules cs ON cs.id = cc.chore_schedule_id
		LEFT JOIN chores c ON c.id = cs.chore_id`+filter+`
		GROUP BY ca.mime_class ORDER BY COUNT(*) DESC`, args...)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var st MediaClassStat
		if err := rows2.Scan(&st.MimeClass, &st.Count); err != nil {
			return nil, nil, 0, err
		}
		classes = append(classes, st)
		total += st.Count
	}
	return subjects, classes, total, rows2.Err()
}
