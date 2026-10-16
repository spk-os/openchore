-- 036 down: drop the chore subject column (SQLite 3.35+ supports DROP COLUMN;
-- modernc.org/sqlite bundles a newer engine, so no table rebuild is needed).

ALTER TABLE chores DROP COLUMN subject;
