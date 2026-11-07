-- 042 up: taxonomy 持久化结构（任务卡 TX-SCHEMA-CODER，规格 specs/taxonomy）。
-- 一条迁移承载全部结构（043-046 的分拆留在卡片范围外）：subjects 表、
-- knowledge_tags 扩列（复用 028 表，不建平行知识点表）、先修 DAG 边表、
-- 公共题库、课标条目、课标↔知识点关联、诊断会话/作答/结论三张表、
-- 课程↔知识点关联、以及诊断报告分享链接。
--
-- 兼容约束（与既有库的关系）：
--   * knowledge_tags 只 ALTER ADD COLUMN，UNIQUE(subject,name,parent_id)
--     （028）原样保留，既有 AI 自由文本 tag 行零改动；
--   * external_ref 为内容导入幂等键：唯一但可 NULL（家长/AI 自建 tag 无外部 id），
--     partial UNIQUE index 保证（API token 同款模式，api_tokens 可空列先例 002）；
--   * course_outcomes 只加可空 knowledge_tag_id（032 的行零改动）；
--   * 全部新表 FK 风格沿用 028/031/032（user 侧 CASCADE，内容侧默认）。

-- ── 1. 学科表驱动词表（FR-1/D4）───────────────────────────────────────────
-- 词表行由启动 seeder 幂等写入（Go 侧任务），本迁移只建结构。
CREATE TABLE subjects (
    key TEXT PRIMARY KEY,             -- math/chinese/... 与 chores.subject 等既有值同域
    label_zh TEXT NOT NULL DEFAULT '',
    sb_code TEXT,                     -- JY/T 0644-2022 六位学科编码，已核实的才填，其余 NULL
    sort_order INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1
);

-- ── 2. knowledge_tags 扩列（D2：扩列而非平行表）────────────────────────────
-- grade/semester/domain/chapter/topic_type 是十学科浏览与图谱过滤维度；
-- 复杂上游字段保留 JSON 原文（mastery_criteria 数组 / diagnostic_question 对象 /
-- standards 数组 / mappings 对象 / aliases 数组），避免丢功能（题目 12579 条的
-- 上游元数据全部可原样落库）；source 留导入溯源（NFR-4：source='cn-math-taxonomy@v'）。
ALTER TABLE knowledge_tags ADD COLUMN grade INTEGER;            -- 1-12，NULL=未结构化（既有 AI tag）
ALTER TABLE knowledge_tags ADD COLUMN semester INTEGER;         -- 1=上 2=下，NULL=不限/未结构化
ALTER TABLE knowledge_tags ADD COLUMN domain TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_tags ADD COLUMN chapter TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_tags ADD COLUMN topic_type TEXT NOT NULL DEFAULT '';   -- conceptual/procedural/...
ALTER TABLE knowledge_tags ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_tags ADD COLUMN mastery_criteria_json TEXT;
ALTER TABLE knowledge_tags ADD COLUMN diagnostic_question_json TEXT;
ALTER TABLE knowledge_tags ADD COLUMN source TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_tags ADD COLUMN external_ref TEXT;        -- 上游稳定 id（cnm_* 等），幂等键
ALTER TABLE knowledge_tags ADD COLUMN standards_json TEXT;      -- [{id,confidence,note}]
ALTER TABLE knowledge_tags ADD COLUMN mappings_json TEXT;       -- 教材版本对照 {edition:{grade,term,unit,...}}
ALTER TABLE knowledge_tags ADD COLUMN aliases_json TEXT;        -- [{id,reason}]（mathLinks 同结构复用）

-- 内容导入幂等：external_ref 唯一（可 NULL 多行——家长/AI 自建 tag 没有 external_ref）。
CREATE UNIQUE INDEX idx_kt_external_ref ON knowledge_tags(external_ref)
    WHERE external_ref IS NOT NULL;
-- 过滤索引：学科/年级浏览（3108 点量级的主查询路径）。
CREATE INDEX idx_kt_subject_grade ON knowledge_tags(subject, grade);
-- 先修 DAG 邻接查询（diagnose 回溯沿 hard 边向下走）。
CREATE INDEX idx_kt_parent ON knowledge_tags(parent_id);

-- ── 3. 先修 DAG 边表（D3）─────────────────────────────────────────────────
-- topic_id 依赖 prerequisite_id（学 topic 前需掌握 prerequisite，上游 dependencies.json 同向）。
-- review_status 治理分级：机器边 machine、家长确认 reviewed、垃圾边 rejected 保留行可审计。
CREATE TABLE knowledge_tag_prerequisites (
    topic_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    prerequisite_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    strength TEXT NOT NULL DEFAULT 'hard' CHECK (strength IN ('hard','soft')),
    reason TEXT NOT NULL DEFAULT '',
    review_status TEXT NOT NULL DEFAULT 'machine'
        CHECK (review_status IN ('machine','reviewed','rejected')),
    PRIMARY KEY (topic_id, prerequisite_id)
);
-- 回溯/被先修查询两个方向都走索引。
CREATE INDEX idx_ktp_prereq ON knowledge_tag_prerequisites(prerequisite_id, review_status);
CREATE INDEX idx_ktp_topic ON knowledge_tag_prerequisites(topic_id, review_status, strength);

-- ── 4. 公共题库（12579 题，FR-2/FR-10）────────────────────────────────────
-- 上游 questions.json 全字段保留：external_ref 幂等（可 NULL=自建题）、
-- choices/answer/accept 复杂结构 JSON 化（answer_json/accept_json），
-- verify 为受限核验表达式原文（Go 白名单求值器解释，不 eval），
-- figure 为内联 SVG 原文（不可信数据，导入时消毒，表不负责）。
CREATE TABLE knowledge_questions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    external_ref TEXT,                -- 上游题 id（cnm_p001_q1），幂等键
    topic_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    difficulty TEXT NOT NULL DEFAULT 'standard'
        CHECK (difficulty IN ('basic','standard','challenge','advanced')),
    type TEXT NOT NULL CHECK (type IN ('numeric','choice','short','self')),
    prompt TEXT NOT NULL,             -- LaTeX 原文
    choices_json TEXT NOT NULL DEFAULT '[]',   -- [{key,text,figure?}]
    answer_json TEXT NOT NULL DEFAULT '""',    -- 答案（answer/answer_text 同义，判分读此）
    accept_json TEXT NOT NULL DEFAULT '[]',    -- 额外可接受答案列表（short 题）
    explanation TEXT NOT NULL DEFAULT '',
    verify TEXT NOT NULL DEFAULT '',  -- 受限表达式，空=无确定性核验
    unordered INTEGER NOT NULL DEFAULT 0,      -- multi/short 答案顺序无关
    figure TEXT,                      -- 题干内联 SVG（题级 figure）
    hide_choice_text INTEGER NOT NULL DEFAULT 0, -- 选项只显示图形不显示文字
    source TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX idx_kq_external_ref ON knowledge_questions(external_ref)
    WHERE external_ref IS NOT NULL;
-- 出题主路径：某知识点按难度取题（诊断每点 4 题）。
CREATE INDEX idx_kq_topic ON knowledge_questions(topic_id, difficulty);

-- ── 5. 课标条目与关联（774 条，FR-2/FR-3）─────────────────────────────────
-- 上游 StandardItem 全字段保留：复杂/低频字段进 JSON，高频过滤字段独立成列。
CREATE TABLE curriculum_standards (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    external_ref TEXT,                -- 上游编码（cms-2022:1.NA.01），幂等键
    subject TEXT NOT NULL DEFAULT '',
    domain TEXT NOT NULL DEFAULT '',
    section TEXT NOT NULL DEFAULT '',
    clause TEXT NOT NULL DEFAULT '',
    text TEXT NOT NULL DEFAULT '',    -- 条文原文（课标内容要求）
    page_or_ref TEXT NOT NULL DEFAULT '',
    extra_json TEXT,                  -- {verbatim,text_status,note,wording_note,theme,elective,module}
    source TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX idx_cs_external_ref ON curriculum_standards(external_ref)
    WHERE external_ref IS NOT NULL;
CREATE INDEX idx_cs_subject ON curriculum_standards(subject, domain);

-- 课标 ↔ 知识点多对多关联（上游 standards[{id,confidence,note}]）。
-- confidence/note 放关联列：同一课标对不同知识点置信度不同，属关联属性。
CREATE TABLE knowledge_tag_standards (
    tag_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    standard_id INTEGER NOT NULL REFERENCES curriculum_standards(id) ON DELETE CASCADE,
    confidence TEXT NOT NULL DEFAULT 'exact' CHECK (confidence IN ('exact','inferred')),
    note TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (tag_id, standard_id)
);
CREATE INDEX idx_kts_standard ON knowledge_tag_standards(standard_id);

-- ── 6. 诊断会话三张表（D6：sessions/answers/findings）──────────────────────
-- diagnostic_sessions：会话壳。state_json 为引擎可序列化状态（续答），
-- summary_json 为终态四态汇总（mastered/shaky/weak/unknown 清单 + 根因 + 学习路径），
-- config_json 为发起配置（scope/seed 等）。void 不删数据（status 位 + voided_at/by）。
CREATE TABLE diagnostic_sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subject TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL DEFAULT 'diagnose'
        CHECK (mode IN ('diagnose','retest','confirm')),
    status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','completed','voided')),
    scope_json TEXT NOT NULL DEFAULT '{}',    -- {grade,domain,topic_ids,seed}
    state_json TEXT NOT NULL DEFAULT '{}',    -- 引擎状态（续答）
    summary_json TEXT,                        -- 终态汇总（NULL=未完成）
    voided_at DATETIME,
    voided_by INTEGER REFERENCES users(id),
    restored_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);
-- 孩子端历史列表：某孩子某学科按时间倒序。
CREATE INDEX idx_ds_user ON diagnostic_sessions(user_id, subject, status, created_at);
-- 管理端：撤销/恢复按 id 定位即可，无需额外索引。

-- diagnostic_attempts：作答事件流，append-only。撤销语义 = 状态位覆盖
-- （voided_at + corrected_by_at），不物理删除历史；序号 seq 保证同题多次作答可回放。
CREATE TABLE diagnostic_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL REFERENCES diagnostic_sessions(id) ON DELETE CASCADE,
    question_id INTEGER REFERENCES knowledge_questions(id),
    topic_id INTEGER REFERENCES knowledge_tags(id),
    seq INTEGER NOT NULL,                     -- 会话内作答序号（1 起）
    role TEXT NOT NULL DEFAULT 'probe' CHECK (role IN ('probe','confirm')),
    answer_json TEXT NOT NULL DEFAULT '""',   -- 孩子的答案（文本/选项 key）
    correct INTEGER,                          -- 引擎判定 1/0，NULL=未判定（short/self 自评前）
    status TEXT NOT NULL DEFAULT 'answered'
        CHECK (status IN ('answered','voided','corrected')),
    voided_at DATETIME,
    corrected_by_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_da_session ON diagnostic_attempts(session_id, seq);
CREATE INDEX idx_da_topic ON diagnostic_attempts(topic_id);

-- ── 7. 课程 ↔ 知识点关联（D7）─────────────────────────────────────────────
-- (a) course_units 覆盖哪些知识点（生成课程时拓扑分层写入；practice 组卷读它）。
CREATE TABLE unit_knowledge_tags (
    unit_id INTEGER NOT NULL REFERENCES course_units(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES knowledge_tags(id) ON DELETE CASCADE,
    PRIMARY KEY (unit_id, tag_id)
);
CREATE INDEX idx_ukt_tag ON unit_knowledge_tags(tag_id);

-- (b) course_outcomes 增加可空知识点关联（一个 Outcome 对齐一个 KC，主用法）。
ALTER TABLE course_outcomes ADD COLUMN knowledge_tag_id INTEGER REFERENCES knowledge_tags(id);

-- ── 8. 诊断报告分享（D9：boost-links token 模式，新建能力）─────────────────
-- token 存哈希（原始 token 只在创建响应里出现一次，不落库不落日志——
-- api_tokens.token_hash 先例 002）；快照语义：分享的是生成时结论，事后
-- override 不追溯改快照。revoke 不删行（revoked_at 留审计）。
CREATE TABLE diagnostic_shares (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash TEXT NOT NULL UNIQUE,          -- 原始 token 的哈希（原始值不落库）
    owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id INTEGER REFERENCES diagnostic_sessions(id) ON DELETE SET NULL,
    report_kind TEXT NOT NULL DEFAULT 'diagnosis'
        CHECK (report_kind IN ('diagnosis','path','blindspots')),
    snapshot_json TEXT NOT NULL,              -- 分享时的最小数据集快照（无 PIN/无其他孩子）
    expires_at DATETIME,
    revoked_at DATETIME,
    created_by INTEGER NOT NULL REFERENCES users(id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_dsh_owner ON diagnostic_shares(owner_id, created_at);
