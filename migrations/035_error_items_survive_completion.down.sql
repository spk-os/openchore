-- 035 down: restore the 028 error_items shape (completion_id without the
-- SET NULL escape hatch). Items whose completion is already gone cannot go
-- back (the column is a plain reference, not NOT NULL, so NULLs are fine).

CREATE TABLE error_items_old (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    completion_id INTEGER REFERENCES chore_completions(id),
    analysis_id INTEGER REFERENCES paper_analyses(id),
    subject TEXT NOT NULL DEFAULT 'math',
    original_image_url TEXT NOT NULL,
    ocr_text TEXT,
    question_text TEXT,
    answer_text TEXT,
    wrong_answer_text TEXT,
    analysis TEXT,
    mistake_analysis TEXT,
    mistake_status TEXT NOT NULL DEFAULT 'unknown' CHECK (mistake_status IN ('not_attempted','wrong_attempt','unknown')),
    error_type TEXT CHECK (error_type IN ('calculation','concept','reading','method') OR error_type IS NULL),
    difficulty TEXT CHECK (difficulty IN ('easy','medium','hard') OR difficulty IS NULL),
    ai_confidence REAL,
    paper_level TEXT,
    grade_semester TEXT,
    source TEXT,
    mastery_level INTEGER NOT NULL DEFAULT 0 CHECK (mastery_level IN (0,1,2)),
    next_review_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO error_items_old SELECT * FROM error_items;
DROP TABLE error_items;
ALTER TABLE error_items_old RENAME TO error_items;

CREATE INDEX idx_error_items_user ON error_items(user_id, mastery_level);
CREATE INDEX idx_error_items_next_review ON error_items(next_review_at);
