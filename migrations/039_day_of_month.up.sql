-- Monthly chores (问题④): a schedule can now land on the Nth of every
-- month (day_of_month 1-31). SQLite cannot ALTER a CHECK, so the table is
-- rebuilt: the "one of three cadences" constraint becomes "one of four".
--
-- The rebuild must not eat the child's history. chore_completions points
-- at chore_schedules with ON DELETE CASCADE, so a plain
-- DROP TABLE chore_schedules cascades: every completion dies, then
-- completion_attachments and paper_analyses die with their completions
-- (only error_items survives, SET NULL since 035). golang-migrate runs
-- the whole file inside one transaction, where PRAGMA foreign_keys is a
-- no-op (SQLite forbids changing FK inside a transaction), so the
-- foreign_keys OFF guards of 025/031 cannot help here — those rebuild
-- tables nothing cascades from. Instead: stash the casualties, do the
-- rebuild, then restore each table around the DROP so the rows come back
-- with the new table's foreign keys intact.

-- 1. Stash every table the cascade would eat.
CREATE TABLE _39_completions AS SELECT * FROM chore_completions;
CREATE TABLE _39_attachments AS SELECT * FROM completion_attachments;
CREATE TABLE _39_analyses AS SELECT * FROM paper_analyses;

-- 2. Rename the old table out of the way: every reference to
-- chore_schedules (the completions FK) now points at _39_schedules_old.
ALTER TABLE chore_schedules RENAME TO _39_schedules_old;

-- 3. Build the new table under the real name and copy the rows over.
CREATE TABLE chore_schedules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    chore_id INTEGER NOT NULL REFERENCES chores(id) ON DELETE CASCADE,
    assigned_to INTEGER NOT NULL REFERENCES users(id),
    assignment_type TEXT NOT NULL CHECK (assignment_type IN ('individual', 'family', 'fcfs')) DEFAULT 'individual',
    fcfs_group_id TEXT,
    day_of_week INTEGER CHECK (day_of_week BETWEEN 0 AND 6),
    day_of_month INTEGER CHECK (day_of_month BETWEEN 1 AND 31),
    specific_date DATE,
    available_at TEXT,
    due_by TEXT,
    expiry_penalty TEXT NOT NULL DEFAULT 'block',
    expiry_penalty_value INTEGER NOT NULL DEFAULT 0,
    points_multiplier REAL NOT NULL DEFAULT 1.0,
    start_date DATE,
    end_date DATE,
    recurrence_interval INTEGER,
    recurrence_start DATE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (day_of_week IS NOT NULL OR day_of_month IS NOT NULL OR specific_date IS NOT NULL OR recurrence_interval IS NOT NULL)
);

INSERT INTO chore_schedules (id, chore_id, assigned_to, assignment_type, fcfs_group_id, day_of_week, day_of_month, specific_date, available_at, due_by, expiry_penalty, expiry_penalty_value, points_multiplier, start_date, end_date, recurrence_interval, recurrence_start, created_at)
    SELECT id, chore_id, assigned_to, assignment_type, fcfs_group_id, day_of_week, NULL, specific_date, available_at, due_by, expiry_penalty, expiry_penalty_value, points_multiplier, start_date, end_date, recurrence_interval, recurrence_start, created_at
    FROM _39_schedules_old;

-- 4. Drop the old table. The CASCADE now empties chore_completions (its
-- FK points at the old table), and the attachments/analyses follow their
-- completions — all fine, every row is in a stash.
DROP TABLE _39_schedules_old;

-- 5. Rebuild chore_completions around the emptied shell so its FK points
-- at the new schedules table, then restore the rows (018's shape).
DROP TABLE chore_completions;
CREATE TABLE chore_completions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    chore_schedule_id INTEGER NOT NULL REFERENCES chore_schedules(id) ON DELETE CASCADE,
    completed_by INTEGER NOT NULL REFERENCES users(id),
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'excused')) DEFAULT 'approved',
    photo_url TEXT NOT NULL DEFAULT '',
    approved_by INTEGER REFERENCES users(id),
    approved_at DATETIME,
    completed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completion_date DATE NOT NULL,
    ai_feedback TEXT NOT NULL DEFAULT '',
    ai_confidence REAL NOT NULL DEFAULT 0,
    ai_complete INTEGER,
    uncompleted_at DATETIME
);
INSERT INTO chore_completions (id, chore_schedule_id, completed_by, status, photo_url, approved_by, approved_at, completed_at, completion_date, ai_feedback, ai_confidence, ai_complete, uncompleted_at)
    SELECT id, chore_schedule_id, completed_by, status, photo_url, approved_by, approved_at, completed_at, completion_date, ai_feedback, ai_confidence, ai_complete, uncompleted_at
    FROM _39_completions;
CREATE INDEX idx_completions_schedule_date ON chore_completions(chore_schedule_id, completion_date);
CREATE INDEX idx_completions_completed_by ON chore_completions(completed_by);
CREATE INDEX idx_completions_uncompleted_at_null
  ON chore_completions(chore_schedule_id, completion_date)
  WHERE uncompleted_at IS NULL;

-- 6. Rebuild completion_attachments (026's shape) and restore.
DROP TABLE completion_attachments;
CREATE TABLE completion_attachments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    completion_id INTEGER NOT NULL REFERENCES chore_completions(id) ON DELETE CASCADE,
    file_path TEXT NOT NULL,
    display_name TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    mime_class TEXT NOT NULL DEFAULT 'image',
    thumbnail_path TEXT,
    sha256 TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    deleted_at DATETIME,
    ai_feedback TEXT NOT NULL DEFAULT '',
    ai_confidence REAL NOT NULL DEFAULT 0,
    ai_complete INTEGER,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO completion_attachments (id, completion_id, file_path, display_name, mime_type, size_bytes, mime_class, thumbnail_path, sha256, sort_order, deleted_at, ai_feedback, ai_confidence, ai_complete, created_at)
    SELECT id, completion_id, file_path, display_name, mime_type, size_bytes, mime_class, thumbnail_path, sha256, sort_order, deleted_at, ai_feedback, ai_confidence, ai_complete, created_at
    FROM _39_attachments;
CREATE INDEX idx_comp_att_completion ON completion_attachments(completion_id);
CREATE UNIQUE INDEX idx_comp_att_sha ON completion_attachments(completion_id, sha256);

-- 7. Rebuild paper_analyses (028's shape plus 037's instructions column)
-- and restore.
DROP TABLE paper_analyses;
CREATE TABLE paper_analyses (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    completion_id INTEGER NOT NULL REFERENCES chore_completions(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_by INTEGER NOT NULL REFERENCES users(id),
    subject TEXT NOT NULL DEFAULT 'math',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','done','failed')),
    stage TEXT NOT NULL DEFAULT 'queued',
    progress INTEGER NOT NULL DEFAULT 0,
    total_questions INTEGER NOT NULL DEFAULT 0,
    wrong_count INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    summary TEXT,
    instructions TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO paper_analyses (id, completion_id, user_id, created_by, subject, status, stage, progress, total_questions, wrong_count, error, summary, instructions, created_at, updated_at)
    SELECT id, completion_id, user_id, created_by, subject, status, stage, progress, total_questions, wrong_count, error, summary, instructions, created_at, updated_at
    FROM _39_analyses;
CREATE INDEX idx_paper_analyses_completion ON paper_analyses(completion_id);

-- 8. The stashes go.
DROP TABLE _39_completions;
DROP TABLE _39_attachments;
DROP TABLE _39_analyses;

CREATE INDEX idx_schedules_assigned ON chore_schedules(assigned_to);
CREATE INDEX idx_schedules_chore ON chore_schedules(chore_id);
CREATE INDEX idx_schedules_fcfs_group ON chore_schedules(fcfs_group_id);
