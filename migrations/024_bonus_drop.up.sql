-- P4-15: variable rewards — the "bonus drop". Completing a chore usually
-- pays a fixed amount; with the bonus_drop_enabled setting on (default
-- off), each approved completion has a small chance to drop 1-5 bonus
-- points on top, recorded as a second point_transactions row
-- (reason='bonus_drop') so the ledger stays auditable and the parent can
-- still explain every point. Chore-by-chore excitement with no per-chore
-- configuration: the drop rides the existing completion flow (EpicWin /
-- ClassDojo-style randomized celebration).
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
        'bonus_drop'
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
