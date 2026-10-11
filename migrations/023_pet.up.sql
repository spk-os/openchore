-- P4-13: the kid's pet — the "emotional balance" layer. One pet per kid
-- (Joon-style, deliberately minimal): a named companion whose mood is
-- computed from the kid's chore record, kept fed with points (a second
-- place for points to go) and grown through cumulative feeding. Feeding
-- debits go through the existing point_transactions ledger
-- (reason='pet_feed'), so no new balance math. Pets never die; the worst
-- mood is "hungry".
CREATE TABLE pets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL DEFAULT '',
    stage INTEGER NOT NULL DEFAULT 1 CHECK (stage IN (1, 2, 3)),
    fed_value INTEGER NOT NULL DEFAULT 0,
    last_fed_at DATETIME,
    hatched_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Widen the point_transactions.reason CHECK to accept the pet feeding
-- debit. SQLite has no ALTER CHECK, so rebuild the table (same pattern as
-- migration 013).
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
        'pet_feed'
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
