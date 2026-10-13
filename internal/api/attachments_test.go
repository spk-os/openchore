package api_test

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"testing"
)

// L0: multi-file proof upload. The kid hands in three photos in one
// request; the parent's grid sees all three (in order), the first image
// becomes the completion's photo_url, and re-uploading the same bytes is
// skipped, not duplicated.
func TestUploadAttachmentsMultiFile(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	// A kid with a chore, completed pending with no photo yet.
	e.mustExec(t, `INSERT INTO users (id, name, role) VALUES (2, 'Mia', 'child')`)
	e.mustExec(t, `INSERT INTO chores (id, title, category, points_value, requires_photo, photo_source, created_by) VALUES (1, 'Homework', 'core', 10, 1, 'child', 1)`)
	e.mustExec(t, `INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES (1, 1, 2, 1)`)
	e.mustExec(t, `INSERT INTO chore_completions (id, chore_schedule_id, completed_by, status, completion_date) VALUES (1, 1, 2, 'pending', '2026-10-14')`)

	// Build a multipart body: files[] with three distinct images.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for i, content := range [][]byte{
		makeJPG("page-one"),
		makeJPG("page-two"),
		makeJPG("page-three"),
	} {
		fw, _ := mw.CreateFormFile("files[]", "page"+string(rune('0'+i+1))+".jpg")
		fw.Write(content)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", e.server.URL+"/api/completions/1/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+sessionToken(2))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(body))
	}
	var created struct {
		Added []map[string]any `json:"added"`
	}
	decodeBody(t, resp, &created)
	if len(created.Added) != 3 {
		t.Fatalf("expected 3 attachments, got %d", len(created.Added))
	}
	if created.Added[0]["mime_class"] != "image" {
		t.Fatalf("expected image mime_class, got %v", created.Added[0]["mime_class"])
	}

	// The parent's view lists all three in order.
	var listed []map[string]any
	decodeBody(t, e.request(t, "GET", "/api/completions/1/attachments", nil, adminHeaders()), &listed)
	if len(listed) != 3 {
		t.Fatalf("expected 3 listed attachments, got %d", len(listed))
	}

	// First image became the completion's photo_url.
	var photoURL string
	e.db.QueryRow(`SELECT photo_url FROM chore_completions WHERE id = 1`).Scan(&photoURL)
	if photoURL == "" {
		t.Fatal("expected photo_url promoted from first attachment")
	}

	// Re-uploading identical bytes is skipped, not stored twice.
	buf2 := &bytes.Buffer{}
	mw2 := multipart.NewWriter(buf2)
	fw2, _ := mw2.CreateFormFile("files[]", "page1.jpg")
	fw2.Write(makeJPG("page-one"))
	mw2.Close()
	req2, _ := http.NewRequest("POST", e.server.URL+"/api/completions/1/attachments", buf2)
	req2.Header.Set("Content-Type", mw2.FormDataContentType())
	req2.Header.Set("Authorization", "Bearer "+sessionToken(2))
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("second upload failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 on duplicate upload, got %d", resp2.StatusCode)
	}
	var dup struct {
		Skipped int `json:"skipped"`
		Count   int `json:"count"`
	}
	decodeBody(t, resp2, &dup)
	if dup.Skipped != 1 || dup.Count != 0 {
		t.Fatalf("expected duplicate skipped, got skipped=%d count=%d", dup.Skipped, dup.Count)
	}
	var n int
	e.db.QueryRow(`SELECT COUNT(*) FROM completion_attachments WHERE completion_id = 1`).Scan(&n)
	if n != 3 {
		t.Fatalf("expected still 3 attachments after duplicate upload, got %d", n)
	}
}

// L0: caps — a second video is rejected once one video is attached.
func TestUploadAttachmentCaps(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	e.mustExec(t, `INSERT INTO users (id, name, role) VALUES (2, 'Mia', 'child')`)
	e.mustExec(t, `INSERT INTO chores (id, title, category, points_value, created_by) VALUES (1, 'Homework', 'core', 10, 1)`)
	e.mustExec(t, `INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES (1, 1, 2, 1)`)
	e.mustExec(t, `INSERT INTO chore_completions (id, chore_schedule_id, completed_by, status, completion_date) VALUES (1, 1, 2, 'pending', '2026-10-14')`)

	if status := e.postFiles(t, 2, "files[]", map[string][]byte{
		"v.mp4": makeMP4("fake video data"),
	}); status != http.StatusCreated {
		t.Fatalf("expected first video accepted, got %d", status)
	}
	if status := e.postFiles(t, 2, "files[]", map[string][]byte{
		"v2.mp4": makeMP4("another video"),
	}); status != http.StatusBadRequest {
		t.Fatalf("expected second video rejected, got %d", status)
	}
}

// L0: task templates — save from a chore, list, instantiate, edit, sync
// into open instances (completed instances untouched), archive.
func TestTaskTemplatesLifecycle(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	e.mustExec(t, `INSERT INTO users (id, name, role) VALUES (2, 'Mia', 'child')`)
	// The source chore the parent tuned and wants to save.
	e.mustExec(t, `INSERT INTO chores (id, title, description, category, points_value, requires_photo, photo_source, created_by) VALUES (1, 'English reading', 'Read 30 minutes', 'core', 10, 1, 'child', 1)`)
	e.mustExec(t, `INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES (1, 1, 2, 1)`)

	// Save as template (from chore 1) — the schedule snapshot rides along.
	var tmpl map[string]any
	decodeBody(t, e.request(t, "POST", "/api/admin/task-templates", map[string]any{
		"from_chore_id": 1,
		"name":          "English reading",
	}, adminHeaders()), &tmpl)
	if tmpl["id"] == nil {
		t.Fatalf("expected template id, got %v", tmpl)
	}
	if sj, _ := tmpl["schedule_json"].(string); sj == "" {
		t.Fatalf("expected schedule snapshot on template, got %v", tmpl)
	}

	// Instantiate for Mia.
	var inst map[string]any
	decodeBody(t, e.request(t, "POST", "/api/admin/task-templates/1/instantiate", map[string]any{
		"assigned_to": 2,
	}, adminHeaders()), &inst)
	if inst["chore"] == nil || inst["schedule"] == nil {
		t.Fatalf("expected chore+schedule from instantiation, got %v", inst)
	}
	var count int
	e.db.QueryRow(`SELECT COUNT(*) FROM chore_schedules WHERE chore_id = (SELECT id FROM chores WHERE template_id = 1)`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 schedule on instantiated chore, got %d", count)
	}

	// Bump the template's points, then sync: the open instance follows.
	decodeBody(t, e.request(t, "PUT", "/api/admin/task-templates/1", map[string]any{
		"points": 15,
	}, adminHeaders()), &tmpl)
	var synced map[string]any
	decodeBody(t, e.request(t, "POST", "/api/admin/task-templates/1/sync", map[string]any{}, adminHeaders()), &synced)
	if synced["updated"].(float64) != 1 {
		t.Fatalf("expected 1 instance synced, got %v", synced)
	}
	var pts int
	e.db.QueryRow(`SELECT points_value FROM chores WHERE template_id = 1`).Scan(&pts)
	if pts != 15 {
		t.Fatalf("expected synced points 15, got %d", pts)
	}

	// Complete the instance, then sync again: history is not the
	// template's — zero updates.
	e.mustExec(t, `INSERT INTO chore_completions (chore_schedule_id, completed_by, status, completion_date)
		SELECT id, 2, 'approved', '2026-10-14' FROM chore_schedules WHERE chore_id = (SELECT id FROM chores WHERE template_id = 1)`)
	decodeBody(t, e.request(t, "PUT", "/api/admin/task-templates/1", map[string]any{
		"points": 20,
	}, adminHeaders()), &tmpl)
	var synced2 map[string]any
	decodeBody(t, e.request(t, "POST", "/api/admin/task-templates/1/sync", map[string]any{}, adminHeaders()), &synced2)
	if synced2["updated"].(float64) != 0 {
		t.Fatalf("expected 0 synced for completed instance, got %v", synced2)
	}

	// Archive: instances survive.
	resp := e.request(t, "DELETE", "/api/admin/task-templates/1", nil, adminHeaders())
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 archive, got %d", resp.StatusCode)
	}
	var still int
	e.db.QueryRow(`SELECT COUNT(*) FROM chores WHERE template_id = 1`).Scan(&still)
	if still != 1 {
		t.Fatalf("expected instance to survive archive, got %d", still)
	}
	var archived int
	e.db.QueryRow(`SELECT is_archived FROM task_templates WHERE id = 1`).Scan(&archived)
	if archived != 1 {
		t.Fatal("expected template archived flag")
	}
}

// L0: attachments are only addable while pending, and only by the
// assignee (or a parent).
func TestUploadAttachmentPermissions(t *testing.T) {
	e := setupTest(t)
	e.createAdmin(t)
	e.mustExec(t, `INSERT INTO users (id, name, role) VALUES (2, 'Mia', 'child'), (3, 'Sam', 'child')`)
	e.mustExec(t, `INSERT INTO chores (id, title, category, points_value, created_by) VALUES (1, 'Homework', 'core', 10, 1)`)
	e.mustExec(t, `INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES (1, 1, 2, 1)`)
	e.mustExec(t, `INSERT INTO chore_completions (id, chore_schedule_id, completed_by, status, completion_date) VALUES (1, 1, 2, 'pending', '2026-10-14')`)

	// Another kid can't add proof to Mia's completion.
	if status := e.postFiles(t, 3, "files[]", map[string][]byte{"x.jpg": makeJPG("nope")}); status != http.StatusForbidden {
		t.Fatalf("expected sibling forbidden, got %d", status)
	}
	// Mia can.
	if status := e.postFiles(t, 2, "files[]", map[string][]byte{"x.jpg": makeJPG("mine")}); status != http.StatusCreated {
		t.Fatalf("expected assignee accepted, got %d", status)
	}
	// Admin can (acting on behalf).
	if status := e.postFiles(t, 1, "files[]", map[string][]byte{"y.jpg": makeJPG("admin")}); status != http.StatusCreated {
		t.Fatalf("expected admin accepted, got %d", status)
	}
	// Once approved, no more attachments.
	e.mustExec(t, `UPDATE chore_completions SET status = 'approved' WHERE id = 1`)
	if status := e.postFiles(t, 2, "files[]", map[string][]byte{"z.jpg": makeJPG("late")}); status != http.StatusBadRequest {
		t.Fatalf("expected approved completion rejected, got %d", status)
	}
}

// --- helpers ---

// makeJPG returns bytes that sniff as image/jpeg (the SOI magic prefix).
func makeJPG(label string) []byte {
	return append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte(label)...)
}

// makeMP4 returns bytes that sniff as video/mp4 (ftyp box signature).
func makeMP4(content string) []byte {
	head := append([]byte{0x00, 0x00, 0x00, 0x18, 0x66, 0x74, 0x79, 0x70, 0x69, 0x73, 0x6f, 0x6d},
		[]byte{0x00, 0x00, 0x02, 0x00, 0x69, 0x73, 0x6f, 0x6d, 0x6d, 0x70, 0x34, 0x32}...)
	return append(head, []byte(content)...)
}

// postFiles sends one multipart request to /completions/1/attachments with
// the given files and returns the status code.
func (e *testEnv) postFiles(t *testing.T, userID int, field string, files map[string][]byte) int {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, content := range files {
		fw, _ := mw.CreateFormFile(field, name)
		fw.Write(content)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", e.server.URL+"/api/completions/1/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+sessionToken(userID))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post files failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		t.Logf("post files error body: %s", string(body))
	}
	return resp.StatusCode
}

// mustExec runs a raw SQL statement on the test database.
func (e *testEnv) mustExec(t *testing.T, q string) {
	t.Helper()
	if _, err := e.db.Exec(q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
