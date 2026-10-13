-- L1: error items — the exam/homework mistake loop. The error_items and
-- knowledge_tags schema follows wttwins/wrong-notebook (747★) field for
-- field: the three-state mastery machine (mastery_level), the orthogonal
-- mistake_status (did the parse succeed?) vs error_type (why was it wrong?)
-- pair, and the knowledge-tag adjacency list (parent_id + code + is_system
-- — never JSON, the upstream project's own [DEPRECATED] marker on its JSON
-- column is the lesson). OpenChore additions: completion_id traces each
-- item back to the photographed submission (the L0 attachment channel),
-- and paper_analyses persists the two-stage async parse so the parent can
-- poll progress (POST /api/admin/analyses → GET /api/admin/analyses/{id}).

CREATE TABLE paper_analyses (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    completion_id INTEGER NOT NULL REFERENCES chore_completions(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,  -- whose paper it is (completed_by)
    created_by INTEGER NOT NULL REFERENCES users(id),                 -- the parent who started the analysis
    subject TEXT NOT NULL DEFAULT 'math',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','done','failed')),
    stage TEXT NOT NULL DEFAULT 'queued',                             -- queued / segmenting / judging / done
    progress INTEGER NOT NULL DEFAULT 0,                              -- 0-100 for the parent's progress bar
    total_questions INTEGER NOT NULL DEFAULT 0,
    wrong_count INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    summary TEXT,                                                     -- diagnosis report JSON (error-type mix, weak tags)
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_paper_analyses_completion ON paper_analyses(completion_id);

CREATE TABLE error_items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    completion_id INTEGER REFERENCES chore_completions(id),  -- traceability to the photographed submission
    analysis_id INTEGER REFERENCES paper_analyses(id),
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
CREATE INDEX idx_error_items_user ON error_items(user_id, mastery_level);
CREATE INDEX idx_error_items_next_review ON error_items(next_review_at);

-- Knowledge points: adjacency list, never JSON (wrong-notebook marked its
-- knowledgePoints JSON [DEPRECATED] and migrated to this shape — the most
-- direct evidence available).
CREATE TABLE knowledge_tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    subject TEXT NOT NULL,
    parent_id INTEGER REFERENCES knowledge_tags(id),
    code TEXT,                             -- textbook code, e.g. "1.2.1"
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_system INTEGER NOT NULL DEFAULT 0,  -- curated presets vs parent/AI-created
    UNIQUE(subject, name, parent_id)
);

CREATE TABLE error_item_tags (
    error_item_id INTEGER NOT NULL REFERENCES error_items(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    PRIMARY KEY (error_item_id, tag_id)
);
