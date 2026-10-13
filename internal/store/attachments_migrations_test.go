package store_test

import (
	"testing"

	"github.com/liftedkilt/openchore/migrations"
)

// 026/027 on a database from before L0 (025 applied, as on the released
// grandparent-boost version): the attachment table lands beside
// chore_completions, chores grows template_id/template_synced_at, and the
// whole chain still rolls back and forward.
func TestMigrations026027AttachmentsTemplates(t *testing.T) {
	m, db := migrator(t)
	if err := m.Migrate(25); err != nil {
		t.Fatalf("migrate to 25: %v", err)
	}

	// Pre-L0 shape: a chore and a completion with the single-photo column.
	mustExec(t, db, `INSERT INTO users (id, name, role, theme, color) VALUES (1, 'Mia', 'child', 'sunroom', 'mint')`)
	mustExec(t, db, `INSERT INTO chores (id, title, created_by) VALUES (1, 'Make bed', 1)`)
	mustExec(t, db, `INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES (1, 1, 1, 1)`)
	mustExec(t, db, `INSERT INTO chore_completions (id, chore_schedule_id, completed_by, status, photo_url, completion_date)
		VALUES (1, 1, 1, 'pending', '/uploads/100_upload.jpg', '2026-10-13')`)

	if err := m.Up(); err != nil {
		t.Fatalf("up through 027: %v", err)
	}

	// 026: attachments carry their own AI review columns and the sha256
	// dedup index exists.
	mustExec(t, db, `INSERT INTO completion_attachments (completion_id, file_path, display_name, mime_type, size_bytes, mime_class, sha256, sort_order)
		VALUES (1, '/uploads/1.jpg', 'page1.jpg', 'image/jpeg', 10, 'image', 'abc', 0)`)
	mustExec(t, db, `INSERT INTO completion_attachments (completion_id, file_path, display_name, mime_type, size_bytes, mime_class, sha256, ai_feedback, ai_confidence, ai_complete)
		VALUES (1, '/uploads/2.jpg', 'page2.jpg', 'image/jpeg', 10, 'image', 'def', 'looks done', 0.9, 1)`)
	if _, err := db.Exec(`INSERT INTO completion_attachments (completion_id, file_path, display_name, mime_type, size_bytes, mime_class, sha256, sort_order)
		VALUES (1, '/uploads/1_copy.jpg', 'page1 copy.jpg', 'image/jpeg', 10, 'image', 'abc', 1)`); err == nil {
		t.Fatal("expected duplicate sha256 within one completion to violate the unique index")
	}

	// 027: templates table + chores back-link columns.
	mustExec(t, db, `INSERT INTO task_templates (name, category, points, description, requires_photo, photo_source, ai_prompt, schedule_json, created_by)
		VALUES ('English reading', 'core', 10, 'Read 30 minutes', 1, 'both', 'check the reading log', '{"day_of_week":1}', 1)`)
	mustExec(t, db, `UPDATE chores SET template_id = 1, template_synced_at = '2026-10-13 10:00:00' WHERE id = 1`)

	// The chain still rolls all the way back and forward.
	if err := m.Down(); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("up again: %v", err)
	}
	mustExec(t, db, `SELECT ai_feedback, ai_complete FROM completion_attachments`)
	mustExec(t, db, `SELECT template_id, template_synced_at FROM chores`)
	if _, err := migrations.FS.Open("026_completion_attachments.up.sql"); err != nil {
		t.Fatalf("026 up embedded: %v", err)
	}
	if _, err := migrations.FS.Open("027_task_templates.down.sql"); err != nil {
		t.Fatalf("027 down embedded: %v", err)
	}
}
