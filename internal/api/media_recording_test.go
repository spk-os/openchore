package api_test

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"testing"
)

// 036: end-to-end recording classification. The recorder sends an
// audio/webm blob; the attachment must land in the audio bucket (not
// video, not image), respect the one-recording cap, and classify as audio
// in the parent's media library. Browsers set the part's Content-Type
// from the blob's real mime, which the server now honors — sniffing
// alone reads the EBML container as video/webm.

// postRecording uploads one recording with a browser-accurate part header
// (Content-Type set on the part, like FormData.append(File) does) and
// returns the response.
func (e *testEnv) postRecording(t *testing.T, userID int, filename, contentType string, content []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files[]"; filename="%s"`, filename))
	h.Set("Content-Type", contentType)
	fw, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(content)
	mw.Close()
	req, _ := http.NewRequest("POST", e.server.URL+"/api/completions/1/attachments", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+sessionToken(userID))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("recording upload failed: %v", err)
	}
	return resp
}

// makeWebmAudio returns EBML bytes — what MediaRecorder produces for an
// audio/webm (Opus) recording. Go sniffs these as video/webm.
func makeWebmAudio() []byte {
	return append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("\x01audio-recording-bytes")...)
}

// makeM4AAudio returns ftyp/M4A bytes — Safari's audio/mp4 recording
// container. Go sniffs these as application/octet-stream.
func makeM4AAudio() []byte {
	head := append([]byte{0x00, 0x00, 0x00, 0x20, 0x66, 0x74, 0x79, 0x70, 0x4d, 0x34, 0x41, 0x20},
		[]byte("\x00\x00\x00\x00M4A mp42isom")...)
	return append(head, []byte("aac frames")...)
}

func recordingFixture(t *testing.T, e *testEnv) {
	t.Helper()
	e.createAdmin(t)
	e.mustExec(t, `INSERT INTO users (id, name, role) VALUES (2, 'Mia', 'child')`)
	e.mustExec(t, `INSERT INTO chores (id, title, category, points_value, created_by) VALUES (1, 'Read the poem aloud', 'core', 10, 1)`)
	e.mustExec(t, `INSERT INTO chore_schedules (id, chore_id, assigned_to, day_of_week) VALUES (1, 1, 2, 1)`)
	e.mustExec(t, `INSERT INTO chore_completions (id, chore_schedule_id, completed_by, status, completion_date) VALUES (1, 1, 2, 'pending', '2026-10-14')`)
}

// TestUploadRecordingClassifiesAsAudio: the headline case — a webm Opus
// recording must be mime_class=audio and mime_type=audio/webm.
func TestUploadRecordingClassifiesAsAudio(t *testing.T) {
	e := setupTest(t)
	recordingFixture(t, e)

	resp := e.postRecording(t, 2, "rec.webm", "audio/webm;codecs=opus", makeWebmAudio())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(body))
	}
	var created struct {
		Added []struct {
			MimeClass string `json:"mime_class"`
			MimeType  string `json:"mime_type"`
			FilePath  string `json:"file_path"`
		} `json:"added"`
	}
	decodeBody(t, resp, &created)
	if len(created.Added) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(created.Added))
	}
	att := created.Added[0]
	if att.MimeClass != "audio" {
		t.Fatalf("audio/webm recording classified as %q, want audio (the sniffing-alone bug)", att.MimeClass)
	}
	if att.MimeType != "audio/webm" {
		t.Errorf("stored mime_type %q, want audio/webm", att.MimeType)
	}
	if !hasSuffix(att.FilePath, ".webm") {
		t.Errorf("expected a .webm file path, got %q", att.FilePath)
	}

	// And in the parent's media library: class=audio finds it.
	var lib struct {
		Items []struct {
			MimeClass string `json:"mime_class"`
		} `json:"items"`
	}
	decodeBody(t, e.request(t, "GET", "/api/admin/media?class=audio", nil, adminHeaders()), &lib)
	if len(lib.Items) != 1 || lib.Items[0].MimeClass != "audio" {
		t.Fatalf("expected the recording in the media library's audio class, got %+v", lib.Items)
	}
}

// TestUploadM4ARecordingClassifiesAsAudio: Safari's container.
func TestUploadM4ARecordingClassifiesAsAudio(t *testing.T) {
	e := setupTest(t)
	recordingFixture(t, e)

	resp := e.postRecording(t, 2, "rec.m4a", "audio/mp4", makeM4AAudio())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(body))
	}
	var created struct {
		Added []struct {
			MimeClass string `json:"mime_class"`
		} `json:"added"`
	}
	decodeBody(t, resp, &created)
	if len(created.Added) != 1 || created.Added[0].MimeClass != "audio" {
		t.Fatalf("m4a recording classified as %+v, want audio", created.Added)
	}
}

// TestUploadVideoRecordingStaysVideo: the video half of the pair — a
// video/webm blob still lands in the video bucket (the fix must not
// blanket-classify webm as audio).
func TestUploadVideoRecordingStaysVideo(t *testing.T) {
	e := setupTest(t)
	recordingFixture(t, e)

	resp := e.postRecording(t, 2, "take.webm", "video/webm", makeWebmAudio())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(body))
	}
	var created struct {
		Added []struct {
			MimeClass string `json:"mime_class"`
		} `json:"added"`
	}
	decodeBody(t, resp, &created)
	if len(created.Added) != 1 || created.Added[0].MimeClass != "video" {
		t.Fatalf("video/webm recording classified as %+v, want video", created.Added)
	}
}

// TestUploadRecordingCaps: the one-recording-per-completion rule now
// applies to actual recordings (before the fix, two "audio" recordings
// were charged against the video bucket instead).
func TestUploadRecordingCaps(t *testing.T) {
	e := setupTest(t)
	recordingFixture(t, e)

	resp := e.postRecording(t, 2, "rec.webm", "audio/webm", makeWebmAudio())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected first recording accepted, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp2 := e.postRecording(t, 2, "rec2.webm", "audio/webm", append(makeWebmAudio(), []byte("second take")...))
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("expected second recording rejected, got %d: %s", resp2.StatusCode, string(body))
	}
	var errMsg map[string]string
	decodeBody(t, resp2, &errMsg)
	if errMsg["error"] != "only one audio recording is allowed per completion" {
		t.Errorf("expected the audio cap message, got %q", errMsg["error"])
	}
}

// TestUploadRecordingThenVideoStillAllowed: an audio recording does not
// consume the single video slot (the pre-fix classification did).
func TestUploadRecordingThenVideoStillAllowed(t *testing.T) {
	e := setupTest(t)
	recordingFixture(t, e)

	if resp := e.postRecording(t, 2, "rec.webm", "audio/webm", makeWebmAudio()); resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected recording accepted, got %d", resp.StatusCode)
	} else {
		resp.Body.Close()
	}
	if status := e.postFiles(t, 2, "files[]", map[string][]byte{"v.mp4": makeMP4("practice")}); status != http.StatusCreated {
		t.Fatalf("expected a video after an audio recording still accepted, got %d", status)
	}
}

// TestUploadUnknownDeclaredTypeStillSniffed: an off-whitelist declared
// type must not bypass validation — a junk upload with declared
// audio/x-unknown is still rejected by the sniffer.
func TestUploadUnknownDeclaredTypeStillSniffed(t *testing.T) {
	e := setupTest(t)
	recordingFixture(t, e)

	resp := e.postRecording(t, 2, "x.bin", "audio/x-unknown", []byte("not media at all"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected junk with off-whitelist declared type rejected, got %d", resp.StatusCode)
	}
}

// hasSuffix avoids importing strings for one check.
func hasSuffix(s, suf string) bool {
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
}
