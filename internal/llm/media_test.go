package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 036: ReviewMedia's wire shape — audio proof rides as an audio_url part,
// video as video_url, never crossed. A crossed part type is the failure
// the whole recording feature hangs on: the gateway would treat a poem
// reading as a video and reject or hallucinate on it.

// mediaTestFile writes bytes that sniff as the given container type.
func mediaTestFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// capturePartTypes runs ReviewMedia against a fake chat server and returns
// the part types of the one media part sent, plus the prompt text.
func capturePartTypes(t *testing.T, mediaKind, path string) (partType string, mediaURLPrefix string, prompt string) {
	t.Helper()
	var gotType, gotURL, gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dec := newDecoder(r)
		parts := dec.parts()
		for _, p := range parts {
			if p["type"] == "text" {
				gotPrompt, _ = p["text"].(string)
			}
			if p["type"] == "audio_url" || p["type"] == "video_url" {
				gotType, _ = p["type"].(string)
				inner := p[gotType].(map[string]any)
				url, _ := inner["url"].(string)
				gotURL = url
			}
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"complete\": true, \"confidence\": 0.9, \"feedback\": \"Read the poem.\"}"}}]}`))
	}))
	t.Cleanup(srv.Close)

	if _, err := New(srv.URL+"/v1", "", "media-model").ReviewMedia(context.Background(), mediaKind, "Read the poem", "Page 12", path); err != nil {
		t.Fatalf("ReviewMedia(%s): %v", mediaKind, err)
	}
	return gotType, gotURL, gotPrompt
}

func TestReviewMediaAudioSendsAudioURLPart(t *testing.T) {
	// An EBML webm: sniffs as video/webm, but the recorded class says audio.
	path := mediaTestFile(t, "rec.webm", append([]byte{0x1A, 0x45, 0xDF, 0xA3}, []byte("opus")...))
	partType, url, prompt := capturePartTypes(t, "audio", path)

	if partType != "audio_url" {
		t.Fatalf("audio proof sent as %q, want audio_url", partType)
	}
	if !strings.HasPrefix(url, "data:audio/") {
		t.Errorf("audio data URL should keep an audio/ prefix even from a video-sniffing container, got %.40s", url)
	}
	if !strings.Contains(prompt, "Read the poem: Page 12") {
		t.Errorf("expected the chore in the prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "audio") {
		t.Errorf("expected the prompt to say audio, got %q", prompt)
	}
}

func TestReviewMediaVideoSendsVideoURLPart(t *testing.T) {
	path := mediaTestFile(t, "take.mp4", append([]byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}, []byte("vp9")...))
	partType, url, prompt := capturePartTypes(t, "video", path)

	if partType != "video_url" {
		t.Fatalf("video proof sent as %q, want video_url", partType)
	}
	if !strings.HasPrefix(url, "data:video/") {
		t.Errorf("video data URL should keep a video/ prefix, got %.40s", url)
	}
	if !strings.Contains(prompt, "video") {
		t.Errorf("expected the prompt to say video, got %q", prompt)
	}
}

// ReviewMedia must fail cleanly when the file is gone (the parent may have
// removed proof while a review was queued).
func TestReviewMediaMissingFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should reach the server for a missing file")
	}))
	t.Cleanup(srv.Close)
	if _, err := New(srv.URL+"/v1", "", "m").ReviewMedia(context.Background(), "audio", "T", "D", filepath.Join(t.TempDir(), "gone.webm")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// The fake gateway must never see both media part types in one request.
func TestReviewMediaNeverCrossesPartTypes(t *testing.T) {
	for _, tc := range []struct{ kind, want, notWant string }{
		{"audio", "audio_url", "video_url"},
		{"video", "video_url", "audio_url"},
	} {
		path := mediaTestFile(t, "b.webm", []byte{0x1A, 0x45, 0xDF, 0xA3, 'x'})
		var seen []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range newDecoder(r).parts() {
				if ty, _ := p["type"].(string); ty == "audio_url" || ty == "video_url" {
					seen = append(seen, ty)
				}
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
		}))
		if _, err := New(srv.URL+"/v1", "", "m").ReviewMedia(context.Background(), tc.kind, "T", "", path); err == nil {
			if len(seen) != 1 || seen[0] != tc.want {
				t.Errorf("%s: media part types = %v, want exactly [%s]", tc.kind, seen, tc.want)
			}
			if len(seen) > 0 && seen[0] == tc.notWant {
				t.Errorf("%s: crossed part type %s", tc.kind, tc.notWant)
			}
		}
		srv.Close()
	}
}
