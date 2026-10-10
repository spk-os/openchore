package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/liftedkilt/openchore/internal/model"
)

var reviewSchema = &Schema{
	Name: "chore_review",
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

// ReviewPhoto asks a vision model whether a photo shows the chore done. The
// result is advice for the parent approving the chore, never a verdict.
// When refPhotoPath is non-empty the chore has a parent-provided reference
// photo ("this is what done looks like") and the model compares the two
// images; otherwise it judges the submission photo on its own.
func (c *Client) ReviewPhoto(ctx context.Context, choreTitle, choreDescription, photoPath, refPhotoPath string) (*model.AIReviewResult, error) {
	data, err := os.ReadFile(photoPath)
	if err != nil {
		return nil, fmt.Errorf("reading photo: %w", err)
	}
	dataURL := "data:" + http.DetectContentType(data) + ";base64," + base64.StdEncoding.EncodeToString(data)

	parts := []Part{
		{Type: "text", Text: buildReviewPrompt(choreTitle, choreDescription, refPhotoPath != "")},
		{Type: "image_url", ImageURL: &ImageURL{URL: dataURL}},
	}
	if refPhotoPath != "" {
		refData, err := os.ReadFile(refPhotoPath)
		if err == nil {
			refDataURL := "data:" + http.DetectContentType(refData) + ";base64," + base64.StdEncoding.EncodeToString(refData)
			parts = append(parts,
				Part{Type: "text", Text: "Reference photo provided by the parent (\"this is what done looks like\"):"},
				Part{Type: "image_url", ImageURL: &ImageURL{URL: refDataURL}})
		}
	}

	raw, err := c.Chat(ctx, []Message{{
		Role:    "user",
		Content: parts,
	}}, reviewSchema)
	if err != nil {
		return nil, err
	}
	return parseReviewResponse(raw)
}

func buildReviewPrompt(title, description string, hasRef bool) string {
	chore := title
	if description != "" {
		chore += ": " + description
	}
	refClause := ""
	if hasRef {
		refClause = `
A reference photo from the parent showing the expected finished state is attached after the submission photo. Compare the two: the child's photo should show a comparable end state, not a pixel-perfect match. Mention in your feedback what matches and what still differs.`
	}
	return fmt.Sprintf(`A child submitted this photo as proof that a household chore is done. A parent will make the final call; your note helps them decide quickly.

CHORE: %s
%s
Judge the current state of the area in the photo, not whether cleaning is visible in progress. Items put away on shelves, in bins or on hooks count as tidy; items scattered on the floor, couch or table do not. Give credit for a reasonable kid-level job, not perfection.

Reply as JSON: {"complete": true or false, "confidence": 0.0 to 1.0, "feedback": "..."}
- feedback is one short sentence for the parent saying what you see, e.g. "Bed is made, pillows are on the floor."
- If the photo is blurry, too dark, or doesn't show the area, set confidence low and say so.`, chore, refClause)
}

// parseReviewResponse accepts the model's JSON, tolerating servers without
// structured-output support that wrap it in prose or code fences.
func parseReviewResponse(raw string) (*model.AIReviewResult, error) {
	raw = strings.TrimSpace(raw)
	var result model.AIReviewResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("could not parse AI response as JSON: %s", truncate(raw, 200))
		}
		if err := json.Unmarshal([]byte(raw[start:end+1]), &result); err != nil {
			return nil, fmt.Errorf("could not parse AI response as JSON: %s", truncate(raw, 200))
		}
	}
	result.Confidence = min(max(result.Confidence, 0), 1)
	result.Feedback = strings.TrimSpace(result.Feedback)
	return &result, nil
}
