-- The scheduled-summary layer (问题③): a table for the monthly wrap-up
-- (the weekly one already lives in weekly_summaries), the configurable run
-- times, and the nightly coach moving from 21:00 to 22:30.

CREATE TABLE monthly_summaries (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    period_start DATE NOT NULL,
    summary TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, period_start)
);

-- Existing installs that kept the old default run at 21:00 migrate along;
-- an explicit non-default pick (say 20:00) is left untouched.
UPDATE app_settings SET value = '22:30' WHERE key = 'coach_time' AND value = '21:00';

-- The weekly/monthly workers read these (HH:MM, 24h). The monthly summary
-- is on by default so the household gets it without a settings visit.
INSERT INTO app_settings (key, value) VALUES
    ('weekly_time', '22:30'),
    ('monthly_time', '22:30'),
    ('ai_monthly_summary', 'true')
ON CONFLICT (key) DO NOTHING;
