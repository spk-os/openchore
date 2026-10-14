-- L3 · 学习者画像与 AI 规划（迁移 033）
-- xAPI Statement 语义骨架（不引入完整 LRS）+ KC 掌握度物化 + Mr.-Ranedeer 配置枚举。
-- 规格真源：design/insight/2026-10-12-OpenChore学习化L0-L4商用级规格.md §6。

CREATE TABLE learner_events (              -- xAPI Statement 语义骨架（actor/verb/object/result 四段）
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,   -- actor
    verb TEXT NOT NULL,                    -- completed/submitted/reviewed/answered/attempted（xAPI verb 精简）
    object_type TEXT NOT NULL,             -- chore/error_item/resource/review_item/course（xAPI object）
    object_id INTEGER NOT NULL,
    result_json TEXT,                      -- {score, success, completion, duration}（xAPI result 语义）
    context_json TEXT,                     -- {subject, kc_ids[]}（扩展维度）
    occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
                                           -- append-only：不 UPDATE 不 DELETE（xAPI Statement 不可变思想）
);
CREATE INDEX idx_learner_events_user ON learner_events(user_id, occurred_at);
CREATE INDEX idx_learner_events_object ON learner_events(object_type, object_id);

CREATE TABLE kc_mastery (                  -- KC 掌握度物化表（弱项识别底座）
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    correct_count INTEGER NOT NULL DEFAULT 0,
    total_count INTEGER NOT NULL DEFAULT 0,
    mastery REAL NOT NULL DEFAULT 0,       -- (correct+1)/(total+2) Laplace 平滑（样本<5 标 low_confidence）
    low_confidence INTEGER NOT NULL DEFAULT 1,  -- 样本量不足标记（前端灰显）
    last_event_at DATETIME,
    PRIMARY KEY (user_id, tag_id)          -- 复合主键即身份：Anki cards 表同款（一 kid 一 tag 一行 upsert）
);
CREATE INDEX idx_kc_mastery_user ON kc_mastery(user_id, mastery);

CREATE TABLE student_config (              -- Mr.-Ranedeer 配置枚举裁剪（家长可调的 AI 教学偏好）
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    depth TEXT NOT NULL DEFAULT '小学',     -- Ranedeer Depth 9 档取 K12 段（亲自核实为 9 档非 10 级）
    learning_style TEXT NOT NULL DEFAULT 'Visual'
        CHECK (learning_style IN ('Visual','Verbal','Active','Intuitive','Reflective','Global')),  -- Ranedeer 六值实证
    communication TEXT NOT NULL DEFAULT 'Textbook'
        CHECK (communication IN ('Formal','Textbook','Layman','StoryTelling','Socratic')),  -- Ranedeer 五值实证
    tone TEXT NOT NULL DEFAULT 'Friendly'
        CHECK (tone IN ('Encouraging','Neutral','Informative','Friendly','Humorous')),       -- Ranedeer 五值实证
    reasoning TEXT NOT NULL DEFAULT 'Deductive'
        CHECK (reasoning IN ('Deductive','Inductive','Abductive','Analogical','Causal')),    -- Ranedeer 五值实证
    interests TEXT NOT NULL DEFAULT '[]',  -- JSON 数组：兴趣标签（画像的「爱好」维度）
    strengths TEXT NOT NULL DEFAULT '[]',  -- JSON 数组：家长录入的优势科目
    weaknesses TEXT NOT NULL DEFAULT '[]', -- JSON 数组：家长录入的待提升项
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
