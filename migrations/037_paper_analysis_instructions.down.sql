-- 037 down: drop the parent's free-text instructions column.

ALTER TABLE paper_analyses DROP COLUMN instructions;
