ALTER TABLE chores DROP COLUMN template_synced_at;
ALTER TABLE chores DROP COLUMN template_id;
DROP TABLE IF EXISTS task_templates;
