package api

import "testing"

// 036: video and audio attachments must classify and pass the upload
// whitelist (previously audio fell into the image default, so a recording
// masqueraded as a photo through the whole chain).
func TestMimeClassAudio(t *testing.T) {
	for mime, want := range map[string]string{
		"audio/webm": "audio",
		"audio/mp4":  "audio",
		"audio/mpeg": "audio",
		"audio/wav":  "audio",
		"audio/x-m4a": "audio",
		"audio/ogg":  "audio",
		"video/mp4":  "video",
		"image/jpeg": "image",
		"application/pdf": "pdf",
	} {
		if got := mimeClassForType(mime); got != want {
			t.Errorf("mimeClassForType(%q) = %q, want %q", mime, got, want)
		}
		if !allowedProofType(mime) {
			t.Errorf("allowedProofType(%q) = false, want true", mime)
		}
	}
	if allowedProofType("audio/x-unknown") {
		t.Error("unknown audio container should be rejected")
	}
}
