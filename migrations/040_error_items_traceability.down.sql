-- 040 down: drop the traceability columns (ALTER DROP COLUMN needs SQLite
-- 3.35+, and the table would have to be rebuilt anyway when below it — the
-- 035 rebuild pattern. Keep it simple and honest: rebuild without the three
-- columns, i.e. the 035 shape).

CREATE TABLE error_items_old (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    completion_id INTEGER REFERENCES chore_completions(id) ON DELETE SET NULL,
    analysis_id INTEGER REFERENCES paper_analyses(id) ON DELETE SET NULL,
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

INSERT INTO error_items_old
    SELECT id, user_id, completion_id, analysis_id, subject, original_image_url, ocr_text,
           question_text, answer_text, wrong_answer_text, analysis, mistake_analysis,
           mistake_status, error_type, difficulty, ai_confidence, paper_level, grade_semester,
           source, mastery_level, next_review_at, created_at, updated_at
      FROM error_items;

DROP TABLE error_items;
ALTER TABLE error_items_old RENAME TO error_items;

CREATE INDEX idx_error_items_user ON error_items(user_id, mastery_level);
CREATE INDEX idx_error_items_next_review ON error_items(next_review_at);
