-- P4-16: grandparent boost links — a share-out to elders outside the
-- household (BusyKid BusyPay, localized: the currency is points, never
-- money). A grown-up generates a token-addressed read-only page per kid;
-- the elder opens it by QR with zero registration, sees a curated
-- snapshot (streak, week's completions, the coach's line) and taps a
-- boost button. The boost lands in the existing ledger
-- (reason='grandparent_boost') with the link as its audit trail, under
-- caps the parent set: a monthly allowance (default 60 points) and a
-- per-day limit (default 3) so the boost stays a garnish, not a second
-- income.

CREATE TABLE boost_links (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token TEXT NOT NULL UNIQUE,
    label TEXT NOT NULL DEFAULT '',
    monthly_cap INTEGER NOT NULL DEFAULT 60,
    daily_limit INTEGER NOT NULL DEFAULT 3,
    granted INTEGER NOT NULL DEFAULT 0,
    granted_month TEXT NOT NULL DEFAULT '',
    revoked INTEGER NOT NULL DEFAULT 0,
    expires_at DATETIME NOT NULL,
    last_boost_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_boost_links_user ON boost_links(user_id);

-- Widen the point_transactions.reason CHECK for the boost. Same
-- table-rebuild pattern as migrations 013/023/024.
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
        'grandparent_boost'
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
