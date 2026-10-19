DROP TABLE IF EXISTS monthly_summaries;

DELETE FROM app_settings WHERE key IN ('weekly_time', 'monthly_time', 'ai_monthly_summary');

-- The coach time the upgrade moved forward goes back.
UPDATE app_settings SET value = '21:00' WHERE key = 'coach_time' AND value = '22:30';
