package llm

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/liftedkilt/openchore/internal/model"
)

// Media review: video and audio proof goes to a separate model from the
// photo reviewer. The photo path stays llm/review.go (images only); this
// file is the timed-media half — a recording of the poem being read or a
// video of the piano practice can carry a verdict just like a photo can.
//
// The request is the OpenAI multimodal chat shape: the media rides as a
// "video_url" / "audio_url" part with a data: URL, the same convention the
// image half uses. Gateways that expose video models through other
// endpoints (e.g. /v1/videos) can be adapted here without touching the
// photo path.

// MediaURL is a video_url / audio_url part body (data: URL inline).
type MediaURL struct {
	URL string `json:"url"`
}

// Part gains the timed-media fields. Only one of ImageURL / VideoURL /
// AudioURL is set per part, keyed by Type ("image_url" / "video_url" /
// "audio_url").
type Part struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
	VideoURL *MediaURL `json:"video_url,omitempty"`
	AudioURL *MediaURL `json:"audio_url,omitempty"`
}

var mediaReviewSchema = &Schema{
	Name: "media_review",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"complete":   map[string]any{"type": "boolean"},
			"confidence": map[string]any{"type": "number"},
			"feedback":   map[string]any{"type": "string"},
		},
		"required":             []string{"complete", "confidence", "feedback"},
		"additionalProperties": false,
	},
}

// ReviewMedia asks a multimodal model whether a video or audio recording
// shows the chore done. mediaKind is model.MimeClassVideo or
// model.MimeClassAudio. The result shape matches ReviewPhoto so the
// caller stores it through the same per-attachment fields.
func (c *Client) ReviewMedia(ctx context.Context, mediaKind, choreTitle, choreDescription, filePath string) (*model.AIReviewResult, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading media: %w", err)
	}
	mimeType := http.DetectContentType(data)
	partType := "video_url"
	switch mediaKind {
	case model.MimeClassAudio:
		partType = "audio_url"
		if !strings.HasPrefix(mimeType, "audio/") {
			// Recordings arrive under odd container mimes (audio/mp4 for
			// m4a); sniffing said otherwise, so trust the recorded class.
			mimeType = "audio/mpeg"
		}
	default:
		if !strings.HasPrefix(mimeType, "video/") {
			mimeType = "video/mp4"
		}
	}
	dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)

	chore := choreTitle
	if choreDescription != "" {
		chore += ": " + choreDescription
	}
	prompt := fmt.Sprintf(`A child submitted this %s recording as proof that a task is done. A parent will make the final call; your note helps them decide quickly.

TASK: %s

Watch or listen to the whole recording. Judge whether the recording actually shows the task being performed (for a reading task, the text being read; for practice, the piece being played). A reasonable kid-level job counts; perfection does not.

Reply as JSON: {"complete": true or false, "confidence": 0.0 to 1.0, "feedback": "..."}
- feedback is one short sentence for the parent saying what you observed, e.g. "朗读了整首诗，中间停顿了一次。"
- If the recording is too short, too noisy or doesn't contain the expected content, set confidence low and say so.`,
		map[string]string{model.MimeClassVideo: "video", model.MimeClassAudio: "audio"}[mediaKind], chore)

	parts := []Part{
		{Type: "text", Text: prompt},
		{Type: partType},
	}
	switch partType {
	case "audio_url":
		parts[1].AudioURL = &MediaURL{URL: dataURL}
	default:
		parts[1].VideoURL = &MediaURL{URL: dataURL}
	}

	raw, err := c.Chat(ctx, []Message{{Role: "user", Content: parts}}, mediaReviewSchema)
	if err != nil {
		return nil, err
	}
	return parseReviewResponse(raw)
}
