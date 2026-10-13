-- L0: task templates — a parent saves a tuned chore (points, AI review
-- prompt, reference photo, TTS) as a reusable template and instantiates it
-- in one tap. Habitica's challenge pattern, simplified: templates live in
-- their own table (OpenChore is greenfield) but keep the two ideas that
-- matter — archiving instead of deleting (is_archived, instances survive)
-- and instance back-links with a sync cursor (template_synced_at) so a
-- template update can be pushed to not-yet-done instances only.

CREATE TABLE task_templates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    category TEXT NOT NULL CHECK (category IN ('required','core','bonus')),
    points INTEGER NOT NULL DEFAULT 10,
    description TEXT NOT NULL DEFAULT '',
    requires_photo INTEGER NOT NULL DEFAULT 1,
    photo_source TEXT NOT NULL DEFAULT 'both' CHECK (photo_source IN ('child','external','both')),
    reference_photo_url TEXT,
    tts_audio_url TEXT,
    ai_prompt TEXT NOT NULL DEFAULT '',       -- AI review prompt travels with the template
    schedule_json TEXT NOT NULL DEFAULT '{}', -- day_of_week/specific_date/recurrence snapshot
    created_by INTEGER NOT NULL REFERENCES users(id),
    is_archived INTEGER NOT NULL DEFAULT 0,   -- archive, never delete (Habitica "broken" mark)
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- chores point back at their template (instances are full chores)
ALTER TABLE chores ADD COLUMN template_id INTEGER REFERENCES task_templates(id);
ALTER TABLE chores ADD COLUMN template_synced_at DATETIME;  -- cursor for template sync
