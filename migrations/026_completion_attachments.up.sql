-- L0: completion attachments — a completion can carry more than one proof
-- file (multi-page homework is the common case: 3 photos of one workbook).
-- Modeled on Canvas Submissions: attachments are first-class rows with
-- their own metadata and soft delete, never a JSON blob. photo_url on
-- chore_completions stays as the first-image convenience for list views;
-- this table is the source of truth.

CREATE TABLE completion_attachments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    completion_id INTEGER NOT NULL REFERENCES chore_completions(id) ON DELETE CASCADE,
    file_path TEXT NOT NULL,            -- stored path under data/uploads
    display_name TEXT NOT NULL,         -- kid/parent visible name (Canvas display_name)
    mime_type TEXT NOT NULL,            -- from the uploads.go whitelist (9 types)
    size_bytes INTEGER NOT NULL,
    mime_class TEXT NOT NULL DEFAULT 'image',  -- Canvas mime_class: image/video/pdf
    thumbnail_path TEXT,                -- first-frame thumbnail for video/PDF
    sha256 TEXT NOT NULL,               -- dedup and integrity
    sort_order INTEGER NOT NULL DEFAULT 0,
    deleted_at DATETIME,                -- soft delete (parent removal is reversible)
    ai_feedback TEXT NOT NULL DEFAULT '',     -- per-attachment AI review note (L0)
    ai_confidence REAL NOT NULL DEFAULT 0,
    ai_complete INTEGER,                -- NULL until reviewed
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_comp_att_completion ON completion_attachments(completion_id);
CREATE UNIQUE INDEX idx_comp_att_sha ON completion_attachments(completion_id, sha256);
