-- L4: FSRS spaced repetition — the review loop. review_items is the
-- go-fsrs Card field for field (due/stability/difficulty/scheduled_days/
-- reps/lapses/state/last_review/remaining_steps — the scheduler columns
-- are owned by the library, nothing else writes them), plus the
-- front/back text pair the kid sees. review_logs mirrors go-fsrs
-- ReviewLog for auditing and stats (Anki stats' three views: heatmap,
-- future load, retention curve). fsrs_params is the per-kid scheduler
-- config — Anki calls Desired Retention "the most important setting".
--
-- The daily queue is computed live (due <= now, minus cards already
-- rated today, under a new-cards-per-day cap and a total cap) rather
-- than materialized at 00:05: the spec's T+0 promise ("a corrected item
-- reviews the same night") requires evening corrections to surface
-- immediately, and the completion credit is anchored by the
-- idempotency-key pattern (see DebitMissedChore) instead of a snapshot
-- table. Anki computes its queue the same way: day cutoff + due +
-- new-card limit.

CREATE TABLE review_items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL CHECK (source_type IN ('error_item','kc','course_outcome')),
    source_id INTEGER NOT NULL,            -- the traced object (wrong question / KC / outcome)
    front_text TEXT NOT NULL,              -- card face (question or KC prompt, LaTeX-friendly)
    back_text TEXT NOT NULL DEFAULT '',    -- card back (answer)
    subject TEXT NOT NULL DEFAULT '',      -- subject badge on the kid's review card
    -- go-fsrs Card fields, verbatim (library-owned, don't hand-edit)
    due DATETIME NOT NULL,
    stability REAL NOT NULL DEFAULT 0,
    difficulty REAL NOT NULL DEFAULT 0,
    scheduled_days INTEGER NOT NULL DEFAULT 0,
    reps INTEGER NOT NULL DEFAULT 0,
    lapses INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL DEFAULT 'New' CHECK (state IN ('New','Learning','Review','Relearning')),
    last_review DATETIME,
    remaining_steps INTEGER NOT NULL DEFAULT 0,
    suspended INTEGER NOT NULL DEFAULT 0,  -- a parent can pause one card
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(source_type, source_id)         -- one active card per source object
);
CREATE INDEX idx_review_items_due ON review_items(user_id, state, due);

CREATE TABLE review_logs (                 -- go-fsrs ReviewLog (audit + stats source)
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    review_item_id INTEGER NOT NULL REFERENCES review_items(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    rating TEXT NOT NULL CHECK (rating IN ('Again','Hard','Good','Easy')),
    state TEXT NOT NULL,                   -- the card's state after the rating
    elapsed_days INTEGER,                  -- computed on our side (go-fsrs keeps no such Card field)
    scheduled_days INTEGER NOT NULL,
    reviewed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_review_logs_item ON review_logs(review_item_id, reviewed_at);
CREATE INDEX idx_review_logs_user ON review_logs(user_id, reviewed_at);

CREATE TABLE fsrs_params (                 -- per-kid scheduler config (the parent's one dial)
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    request_retention REAL NOT NULL DEFAULT 0.90,   -- Anki official: 0.90 default; >0.97 overloads
    maximum_interval INTEGER NOT NULL DEFAULT 36500,
    learning_steps_json TEXT NOT NULL DEFAULT '[1,10]',   -- minutes
    relearning_steps_json TEXT NOT NULL DEFAULT '[10]',
    enable_fuzz INTEGER NOT NULL DEFAULT 1,
    weights_json TEXT,                     -- NULL = library default W[21]; optimizer later
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Widen the point_transactions.reason CHECK for the review credit. Same
-- table-rebuild pattern as migrations 013/023/024/025.
PRAGMA foreign_keys = OFF;

CREATE TABLE point_transactions_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    amount INTEGER NOT NULL,
    reason TEXT NOT NULL CHECK (reason IN (
        'chore_complete', 'chore_uncomplete', 'reward_redeem',
        'streak_bonus', 'admin_adjust', 'expiry_penalty',
        'points_decay', 'missed_chore',
        'commit_to_goal', 'goal_break',
        'pet_feed',
        'bonus_drop',
        'grandparent_boost',
        'review_complete'
    )),
    reference_id INTEGER,
    note TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO point_transactions_new
    (id, user_id, amount, reason, reference_id, note, idempotency_key, created_at)
SELECT
    id, user_id, amount, reason, reference_id, note, idempotency_key, created_at
FROM point_transactions;

DROP TABLE point_transactions;
ALTER TABLE point_transactions_new RENAME TO point_transactions;

CREATE INDEX idx_point_tx_user ON point_transactions(user_id);
CREATE INDEX idx_point_tx_ref ON point_transactions(reason, reference_id);
CREATE UNIQUE INDEX idx_point_tx_idempotency_key
    ON point_transactions(idempotency_key)
    WHERE idempotency_key IS NOT NULL;

PRAGMA foreign_keys = ON;
