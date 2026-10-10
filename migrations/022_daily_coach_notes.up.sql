-- P2: the daily coach's two-sided note. Each kid gets one row per day:
-- a short growth-minded note for the kid (text + optional read-aloud
-- audio) and a longer observation for the parent. Rows are append-only
-- in spirit (regenerated only by the nightly worker when still empty).
CREATE TABLE daily_coach_notes (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    date DATE NOT NULL,
    child_text TEXT NOT NULL DEFAULT '',
    child_audio_url TEXT NOT NULL DEFAULT '',
    parent_text TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, date)
);
