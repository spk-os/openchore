package api

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 036: the recording channel's core correctness question — does an
// audio/webm upload land in the audio bucket? Content sniffing can't tell
// (Go sniffs every EBML stream as video/webm, M4A brands fall out of the
// mp4 sniffer entirely), so saveProofFile must honor the part's declared
// Content-Type whenever it is on the whitelist. These tests lock that in.

// makeWebm returns bytes with the EBML magic — what MediaRecorder produces
// for both audio and video webm. Go's sniffer reads them as video/webm.
func makeWebm(label string) []byte {
	return append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("\x01"+label)...)
}

// makeM4A returns bytes with an ftyp/M4A brand — Safari's audio recording
// container. Go's sniffer returns application/octet-stream for it (no
// video/mp4 brand match), so only the declared type can classify it.
func makeM4A(label string) []byte {
	head := append([]byte{0x00, 0x00, 0x00, 0x20, 0x66, 0x74, 0x79, 0x70, 0x4d, 0x34, 0x41, 0x20},
		[]byte("\x00\x00\x00\x00M4A mp42isom")...)
	return append(head, []byte(label)...)
}

// TestSniffersCannotSeeAudio proves the premise of the declared-type fix:
// with no declared type, both containers classify as non-audio today.
// If this ever flips (Go learns audio sniffing), the fix stays harmless.
func TestSniffersCannotSeeAudio(t *testing.T) {
	if got := http.DetectContentType(makeWebm("x")); got != "video/webm" {
		t.Errorf("EBML sniffs as %q, expected video/webm (the reason declared types must win)", got)
	}
	if got := http.DetectContentType(makeM4A("x")); got == "audio/mp4" {
		t.Errorf("M4A sniffs as audio/mp4 — Go learned audio sniffing, the declared-type fallback can be revisited")
	}
}

// TestSaveProofFilePrefersDeclaredType walks the classifier directly: a
// whitelisted declared type wins over sniffing, anything else falls back.
func TestSaveProofFilePrefersDeclaredType(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name         string
		content      []byte
		declared     string
		filename     string
		wantMime     string
		wantExt      string
		wantRejected bool
	}{
		// The two recordings the kid's client actually produces.
		{"audio webm", makeWebm("opus"), "audio/webm;codecs=opus", "rec.webm", "audio/webm", ".webm", false},
		{"audio m4a", makeM4A("aac"), "audio/mp4", "rec.m4a", "audio/mp4", ".m4a", false},
		// Video containers still classify as video.
		{"video webm", makeWebm("vp9"), "video/webm", "take.webm", "video/webm", ".webm", false},
		// File pickers send octet-stream or nothing: sniffing takes over.
		{"octet stream sniffs jpg", makeJPGBytes("x"), "application/octet-stream", "p.jpg", "image/jpeg", ".jpg", false},
		{"empty declared sniffs jpg", makeJPGBytes("x"), "", "p.jpg", "image/jpeg", ".jpg", false},
		// A declared type off the whitelist never smuggles bytes through:
		// audio/x-unknown falls back to sniffing, which rejects random data.
		{"off-whitelist declared falls back and rejects", []byte("junkjunk"), "audio/x-unknown", "x.bin", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url, mime, _, _, err := saveProofFile(bytes.NewReader(tc.content), tc.filename, tc.declared, "t")
			if tc.wantRejected {
				if err == nil {
					t.Fatalf("expected rejection, got url=%s mime=%s", url, mime)
				}
				return
			}
			if err != nil {
				t.Fatalf("saveProofFile: %v", err)
			}
			if mime != tc.wantMime {
				t.Errorf("mime = %q, want %q", mime, tc.wantMime)
			}
			if ext := filepath.Ext(url); ext != tc.wantExt {
				t.Errorf("stored ext = %q, want %q (url %s)", ext, tc.wantExt, url)
			}
			if !strings.HasPrefix(url, "/uploads/") {
				t.Errorf("url %q must live under /uploads/", url)
			}
			if _, statErr := os.Stat(filepath.Join(uploadDir, strings.TrimPrefix(url, "/uploads/"))); statErr != nil {
				t.Errorf("stored file missing: %v", statErr)
			}
		})
	}
}

// makeJPGBytes is the internal-package twin of the api_test makeJPG (the
// SOI magic makes the sniffer say image/jpeg).
func makeJPGBytes(label string) []byte {
	return append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte(label)...)
}

// multipartPartHeader is what CreateFormFile can't set: a real
// Content-Type on the part, which is exactly what browsers do for
// MediaRecorder blobs. Returns the part writer after writing the header.
func multipartPartHeader(mw *multipart.Writer, field, filename, contentType string) (io.Writer, error) {
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, filename))
	h.Set("Content-Type", contentType)
	return mw.CreatePart(h)
}
