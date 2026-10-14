-- L2: the course system — Canvas Modules mapped onto SQLite. courses is
-- the course shell (draft→published, edX's split: edits to a published
-- course never mutate what the kid is looking at until re-published);
-- course_units is the Canvas Module (prerequisite_unit_ids[] JSON +
-- requirement_type all|one + optional unlock_at), unit_resources is the
-- ModuleItem (resource_type matrix + the completion predicate — Canvas's
-- six completion_requirement enums cut to four for a household), and
-- resource_attempts is the fact table the unlock engine reads.
-- course_outcomes/outcome_assessments carry the Canvas Outcomes mastery
-- rollup (calculation_method latest|highest|average|decaying_average).
--
-- The unlock rule itself lives in code, not tables (Canvas semantics:
-- a unit unlocks when unlock_at has passed AND every prerequisite unit
-- is completed AND the unit is published). resource_attempts rows are
-- unique per (resource, user) and overwritten on retry — a child
-- re-does a practice, the attempt updates, the unit recomputes.

CREATE TABLE courses (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    syllabus_md TEXT NOT NULL DEFAULT '',      -- course outline Markdown (Canvas syllabus_body, MD-ified)
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published','archived')),
    is_template INTEGER NOT NULL DEFAULT 0,    -- §5.5: no separate template table; Canvas/edX clone instead
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE course_units (                    -- the Canvas Module
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    course_id INTEGER NOT NULL REFERENCES courses(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 1,       -- 1-based (Canvas position)
    prerequisite_unit_ids TEXT NOT NULL DEFAULT '[]',  -- JSON int array (Canvas prerequisite_module_ids)
    requirement_type TEXT NOT NULL DEFAULT 'all' CHECK (requirement_type IN ('all','one')),
    require_sequential_progress INTEGER NOT NULL DEFAULT 0,
    unlock_at DATETIME,                       -- optional timed unlock (NULL = immediately eligible)
    published INTEGER NOT NULL DEFAULT 1,
    UNIQUE(course_id, position)
);

CREATE TABLE unit_resources (                  -- the Canvas ModuleItem
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    unit_id INTEGER NOT NULL REFERENCES course_units(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 1,
    resource_type TEXT NOT NULL CHECK (resource_type IN ('page','file','audio','video','practice','link','task')),
    content_ref TEXT NOT NULL DEFAULT '',      -- file path / link URL / page markdown, by type
    -- completion predicate (Canvas completion_requirement, six enums cut
    -- to four for a household; NULL = display-only, never gates anything)
    completion_type TEXT CHECK (completion_type IN ('must_view','must_submit','min_score','min_minutes')),
    min_score INTEGER,                        -- min_score threshold (0 = disabled, Moodle integer encoding)
    min_minutes INTEGER,                      -- min_minutes threshold
    UNIQUE(unit_id, position)
);

CREATE TABLE resource_attempts (               -- the completion facts the engine reads
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource_id INTEGER NOT NULL REFERENCES unit_resources(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    viewed_seconds INTEGER NOT NULL DEFAULT 0,
    submitted_ref TEXT,
    score INTEGER,
    completed_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(resource_id, user_id)              -- retried attempts overwrite (child re-does, row updates)
);

CREATE TABLE course_outcomes (                 -- the Canvas Outcome (mastery rollup)
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    course_id INTEGER NOT NULL REFERENCES courses(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    mastery_points INTEGER NOT NULL DEFAULT 80,   -- pass line on the 100 scale
    calculation_method TEXT NOT NULL DEFAULT 'latest'
        CHECK (calculation_method IN ('latest','highest','average','decaying_average')),
    calc_int INTEGER,                         -- decaying_average weight percent (e.g. 65)
    ratings_json TEXT NOT NULL DEFAULT '[]'   -- rating bands [{description,points}]
);

CREATE TABLE outcome_assessments (             -- the Canvas RubricAssessment (score event stream)
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    outcome_id INTEGER NOT NULL REFERENCES course_outcomes(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    score INTEGER NOT NULL,
    assessment_type TEXT NOT NULL CHECK (assessment_type IN ('ai','parent','self')),
    artifact_type TEXT,                       -- linked object: practice/resource/error_item
    artifact_id INTEGER,
    comments TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_outcome_assess ON outcome_assessments(outcome_id, user_id, created_at);
CREATE INDEX idx_resource_attempts ON resource_attempts(user_id);
