-- 035: error items outlive their completions.
--
-- error_items.completion_id was a plain REFERENCES ... (no action → RESTRICT
-- on delete of the referenced row inside a cascade), so deleting a chore
-- cascaded schedules → completions → error items: a whole error-book page
-- of learning history vanished with a task cleanup. The error book is a
-- learning asset — it must survive the chore that produced it. Switch the
-- reference to ON DELETE SET NULL so a completion delete keeps the item and
-- only drops the traceability link.
--
-- SQLite cannot ALTER a foreign key in place: rebuild the table (the 013
-- pattern), copying 028's columns verbatim except the one foreign key.

CREATE TABLE error_items_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    completion_id INTEGER REFERENCES chore_completions(id) ON DELETE SET NULL,  -- traceability only; the item survives
    analysis_id INTEGER REFERENCES paper_analyses(id) ON DELETE SET NULL,  -- the report may go; the item stays
    subject TEXT NOT NULL DEFAULT 'math',
    original_image_url TEXT NOT NULL,      -- the page the mistake was read from
    ocr_text TEXT,                         -- raw OCR text (LaTeX inline)
    -- AI verdict fields (aligned with wrong-notebook ErrorItem)
    question_text TEXT,                    -- 题干
    answer_text TEXT,                      -- AI-provided correct answer
    wrong_answer_text TEXT,                -- the student's wrong answer
    analysis TEXT,                         -- 解析
    mistake_analysis TEXT,                 -- 错因分析
    mistake_status TEXT NOT NULL DEFAULT 'unknown' CHECK (mistake_status IN ('not_attempted','wrong_attempt','unknown')),
                                          -- parse failures degrade to 'unknown' into the manual queue
    error_type TEXT CHECK (error_type IN ('calculation','concept','reading','method') OR error_type IS NULL),
    difficulty TEXT CHECK (difficulty IN ('easy','medium','hard') OR difficulty IS NULL),
    ai_confidence REAL,                    -- LLM scores must carry confidence (arXiv:2605.00200)
    paper_level TEXT,                      -- A/B/Other
    grade_semester TEXT,                   -- 年级学期
    source TEXT,                           -- e.g. "期中考试"
    -- mastery state machine (wrong-notebook masteryLevel: 0=New 1=Reviewing 2=Mastered)
    mastery_level INTEGER NOT NULL DEFAULT 0 CHECK (mastery_level IN (0,1,2)),
    next_review_at DATETIME,               -- denormalized (list ordering; L4 FSRS takes over scheduling)
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO error_items_new SELECT * FROM error_items;
DROP TABLE error_items;
ALTER TABLE error_items_new RENAME TO error_items;

-- Re-create 028's indexes (they died with the old table).
CREATE INDEX idx_error_items_user ON error_items(user_id, mastery_level);
CREATE INDEX idx_error_items_next_review ON error_items(next_review_at);
