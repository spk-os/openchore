-- 学习措施引擎（迁移 034）：分析结果沉淀为可执行的「措施」，家长可
-- 确认/暂停/完成，也可让 AI 基于最新错题数据加强某条措施。
-- 业界依据：作业帮「AI 老师一对一」四环节（摸底→制定计划→跟练→督促）
-- 与智学网「个性化学习手册」（错因分析+订正+相似题落成可执行的纸）——
-- AI 建议落地为每日任务清单，而非一段纯文字。OpenChore 的差异：措施
-- 是一行数据（可暂停/完成/加强），planner 与周报都消费它。

CREATE TABLE learning_measures (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subject TEXT NOT NULL DEFAULT '',           -- 针对科目（'' = 全科习惯类措施）
    kc_tag_id INTEGER REFERENCES knowledge_tags(id), -- 针对的知识点（可空）
    title TEXT NOT NULL,                        -- 措施标题（「判别式专项加练」）
    detail TEXT NOT NULL DEFAULT '',            -- 执行细节（怎么做、做多少）
    error_types TEXT NOT NULL DEFAULT '[]',     -- 针对错因 JSON 数组 ["concept"]
    origin TEXT NOT NULL DEFAULT 'analysis' CHECK (origin IN ('analysis','manual','ai_strengthen')),
                                               -- 从哪来：卷面分析提炼 / 家长手写 / AI 加强改写
    origin_id INTEGER,                          -- 来源对象 id（paper_analyses.id）
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','done')),
    priority INTEGER NOT NULL DEFAULT 1 CHECK (priority IN (1,2,3)), -- 1 高 2 中 3 低
    created_by INTEGER NOT NULL REFERENCES users(id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_learning_measures_user ON learning_measures(user_id, status);
CREATE INDEX idx_learning_measures_kc ON learning_measures(kc_tag_id);
