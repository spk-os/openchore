package api_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// 036: the parent-side media library query surface — subject / class /
// date / kid / chore filters, the chip counts, and the deleted-row and
// admin-gate invariants. Built on direct DB rows so the test is about the
// query, not the upload path.

// mediaFixture seeds two kids × three chores (math/english/no-subject)
// with one attachment each, plus a second attachment on the math chore
// (video) and a soft-deleted one that must never surface.
//
//	att 1  math     image   Mia   today
//	att 2  math     video   Mia   yesterday (created_at backdated)
//	att 3  english  audio   Ben   today
//	att 4  (none)   pdf     Mia   today   — soft-deleted, invisible
func mediaFixture(t *testing.T, e *testEnv) (miaID, benID int, mathChoreID, engChoreID int64) {
	t.Helper()
	e.createAdmin(t)
	// Kids 2 (Mia) and 3 (Ben), admin is 1.
	e.mustExec(t, `INSERT INTO users (id, name, role) VALUES (2, 'Mia', 'child'), (3, 'Ben', 'child')`)
	// Chores: 1 math, 2 english, 3 no subject.
	e.mustExec(t, `INSERT INTO chores (id, title, category, subject, points_value, created_by) VALUES
		(1, 'Math workbook', 'core', 'math', 10, 1),
		(2, 'English reading', 'core', 'english', 10, 1),
		(3, 'Dishes', 'core', '', 5, 1)`)
	// Schedules: math+english to Mia, dishes to Ben.
	e.mustExec(t, `INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES
		(1, 1, 2, 1), (2, 2, 2, 1), (3, 3, 3, 1)`)
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	// Completions carry the kids; created_at is set per attachment row.
	e.mustExec(t, fmt.Sprintf(`INSERT INTO chore_completions (id, chore_schedule_id, completed_by, status, completion_date) VALUES
		(1, 1, 2, 'pending', '%s'),
		(2, 2, 2, 'approved', '%s'),
		(3, 3, 3, 'pending', '%s')`, today, today, today))
	e.mustExec(t, fmt.Sprintf(`INSERT INTO completion_attachments
		(id, completion_id, file_path, display_name, mime_type, size_bytes, mime_class, sha256, sort_order, created_at) VALUES
		(1, 1, '/uploads/a1.jpg', 'page1.jpg', 'image/jpeg', 100, 'image', 's1', 0, '%s'),
		(2, 1, '/uploads/a2.mp4', 'demo.mp4', 'video/mp4', 200, 'video', 's2', 1, '%s'),
		(3, 2, '/uploads/a3.webm', 'reading.webm', 'audio/webm', 300, 'audio', 's3', 0, '%s'),
		(4, 3, '/uploads/a4.pdf', 'menu.pdf', 'application/pdf', 400, 'pdf', 's4', 0, '%s')`,
		today+" 10:00:00", yesterday+" 10:00:00", today+" 11:00:00", today+" 12:00:00"))
	// att 4 is soft-deleted: it must never appear anywhere.
	e.mustExec(t, `UPDATE completion_attachments SET deleted_at = '2026-10-17 00:00:00' WHERE id = 4`)
	return 2, 3, 1, 2
}

type mediaResponse struct {
	Items []struct {
		AttachmentID int64  `json:"attachment_id"`
		Subject      string `json:"subject"`
		MimeClass    string `json:"mime_class"`
		ChoreID      int64  `json:"chore_id"`
		ChoreTitle   string `json:"chore_title"`
		UserID       int64  `json:"user_id"`
		UserName     string `json:"user_name"`
		CreatedAt    string `json:"created_at"`
	} `json:"items"`
	Subjects []struct {
		Subject string `json:"subject"`
		Count   int    `json:"count"`
	} `json:"subjects"`
	Classes []struct {
		MimeClass string `json:"mime_class"`
		Count     int    `json:"count"`
	} `json:"classes"`
	Total int `json:"total"`
}

func listMedia(t *testing.T, e *testEnv, query string) mediaResponse {
	t.Helper()
	resp := e.expectStatus(t, "GET", "/api/admin/media"+query, nil, adminHeaders(), http.StatusOK)
	var out mediaResponse
	decodeBody(t, resp, &out)
	return out
}

func TestMediaLibraryFilters(t *testing.T) {
	e := setupTest(t)
	_, _, _, _ = mediaFixture(t, e)

	// No filter: three live items (the deleted pdf is invisible), newest first.
	all := listMedia(t, e, "")
	if len(all.Items) != 3 || all.Total != 3 {
		t.Fatalf("expected 3 live items, got %d (total %d)", len(all.Items), all.Total)
	}
	if all.Items[0].AttachmentID != 3 {
		t.Errorf("expected newest-first (att 3 at 11:00), got %d first", all.Items[0].AttachmentID)
	}
	// The joined columns ride along.
	if all.Items[0].Subject != "english" || all.Items[0].UserName != "Mia" || all.Items[0].ChoreTitle != "English reading" {
		t.Errorf("unexpected joined item: %+v", all.Items[0])
	}

	// Subject filter: math → the two math attachments.
	math := listMedia(t, e, "?subject=math")
	if len(math.Items) != 2 {
		t.Fatalf("subject=math: expected 2 items, got %d", len(math.Items))
	}

	// Class filter: audio → only the reading recording.
	audio := listMedia(t, e, "?class=audio")
	if len(audio.Items) != 1 || audio.Items[0].AttachmentID != 3 {
		t.Fatalf("class=audio: expected att 3, got %+v", audio.Items)
	}

	// Combined subject+class: math videos only.
	mv := listMedia(t, e, "?subject=math&class=video")
	if len(mv.Items) != 1 || mv.Items[0].AttachmentID != 2 {
		t.Fatalf("math+video: expected att 2, got %+v", mv.Items)
	}

	// Date window: only yesterday's video.
	yst := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	past := listMedia(t, e, fmt.Sprintf("?from=%s&to=%s", yst, yst))
	if len(past.Items) != 1 || past.Items[0].AttachmentID != 2 {
		t.Fatalf("yesterday window: expected att 2, got %+v", past.Items)
	}
	// From=today excludes yesterday.
	today := time.Now().Format("2006-01-02")
	sinceToday := listMedia(t, e, fmt.Sprintf("?from=%s", today))
	if len(sinceToday.Items) != 2 {
		t.Fatalf("from=today: expected 2 items, got %d", len(sinceToday.Items))
	}

	// Kid filter: Ben has none of the live items (his pdf is deleted).
	ben := listMedia(t, e, "?user_id=3")
	if len(ben.Items) != 0 || ben.Total != 0 {
		t.Fatalf("Ben should see his pdf deleted away, got %+v (total %d)", ben.Items, ben.Total)
	}

	// Chore filter.
	byChore := listMedia(t, e, "?chore_id=1")
	if len(byChore.Items) != 2 {
		t.Fatalf("chore_id=1: expected 2 items, got %d", len(byChore.Items))
	}

	// Chip stats: subjects math(2)/english(1), classes image/video/audio(1 each).
	if len(all.Subjects) != 2 {
		t.Fatalf("expected 2 subject stats, got %+v", all.Subjects)
	}
	if all.Subjects[0].Subject != "math" || all.Subjects[0].Count != 2 {
		t.Errorf("expected math first with 2, got %+v", all.Subjects[0])
	}
	if len(all.Classes) != 3 || all.Total != 3 {
		t.Errorf("expected 3 class stats totaling 3, got %+v (total %d)", all.Classes, all.Total)
	}
}

// TestMediaLibraryRequiresAdmin: the library spans every kid, so a child
// session gets 403 (not their own rows).
func TestMediaLibraryRequiresAdmin(t *testing.T) {
	e := setupTest(t)
	_, _, _, _ = mediaFixture(t, e)
	e.expectStatus(t, "GET", "/api/admin/media", nil, childHeaders(2), http.StatusForbidden)
}

// TestMediaLibraryDeletedChoreKeepsItems: the schema cascades a chore
// delete through schedules to completions and attachments (001), so the
// library keeps only the other chores' items — and crucially the query
// must not error on the dangling path. Deleting is rare (parents archive
// instead), but the LEFT JOIN chain must stay sound when it happens.
func TestMediaLibraryDeletedChoreKeepsItems(t *testing.T) {
	e := setupTest(t)
	_, _, _, _ = mediaFixture(t, e)
	e.mustExec(t, `DELETE FROM chores WHERE id = 1`)
	after := listMedia(t, e, "")
	if len(after.Items) != 1 {
		t.Fatalf("expected the english item to survive the math chore delete, got %d", len(after.Items))
	}
	if after.Items[0].AttachmentID != 3 || after.Total != 1 {
		t.Errorf("expected att 3 left, got %+v (total %d)", after.Items[0], after.Total)
	}
}
