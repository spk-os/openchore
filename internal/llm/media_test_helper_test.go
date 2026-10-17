package llm

import (
	"encoding/json"
	"net/http"
)

// part is one decoded multimodal content part from a chat request.
type part map[string]any

// requestDecoder decodes one chat completion request.
type requestDecoder struct {
	body struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	partsCache []part
}

func newDecoder(r *http.Request) *requestDecoder {
	d := &requestDecoder{}
	_ = json.NewDecoder(r.Body).Decode(&d.body)
	return d
}

// parts flattens the first message's content: either a string (text) or a
// list of multimodal parts.
func (d *requestDecoder) parts() []part {
	if d.partsCache != nil {
		return d.partsCache
	}
	if len(d.body.Messages) == 0 {
		return nil
	}
	raw := d.body.Messages[0].Content
	var list []part
	if err := json.Unmarshal(raw, &list); err == nil {
		d.partsCache = list
		return list
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		d.partsCache = []part{{"type": "text", "text": text}}
	}
	return d.partsCache
}
