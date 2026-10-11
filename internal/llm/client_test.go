package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captured is what the fake server saw of the last request.
type captured struct {
	path string
	auth string
	body map[string]any
}

func fakeServer(t *testing.T, reply string, status int) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.path = r.URL.Path
		c.auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&c.body)
		if status != http.StatusOK {
			http.Error(w, reply, status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func TestReviewPhotoSendsImageAndSchema(t *testing.T) {
	srv, got := fakeServer(t, `{"complete": true, "confidence": 0.9, "feedback": "Bed is made."}`, http.StatusOK)
	photo := filepath.Join(t.TempDir(), "bed.jpg")
	if err := os.WriteFile(photo, []byte("\xff\xd8\xff\xe0jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := New(srv.URL+"/v1/", "sk-test", "gemma").ReviewPhoto(context.Background(), "Make bed", "Pull up the covers", photo, "")
	if err != nil {
		t.Fatalf("ReviewPhoto: %v", err)
	}
	if !res.Complete || res.Confidence != 0.9 || res.Feedback != "Bed is made." {
		t.Errorf("unexpected result %+v", res)
	}
	if got.path != "/v1/chat/completions" || got.auth != "Bearer sk-test" || got.body["model"] != "gemma" {
		t.Errorf("unexpected request: path=%s auth=%q model=%v", got.path, got.auth, got.body["model"])
	}

	rf := got.body["response_format"].(map[string]any)
	if rf["type"] != "json_schema" || rf["json_schema"].(map[string]any)["name"] != "chore_review" {
		t.Errorf("expected a JSON schema response format, got %v", rf)
	}
	parts := got.body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	text := parts[0].(map[string]any)["text"].(string)
	image := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.Contains(text, "Make bed: Pull up the covers") {
		t.Errorf("expected the chore in the prompt, got %q", text)
	}
	if !strings.HasPrefix(image, "data:image/jpeg;base64,") {
		t.Errorf("expected an inline JPEG, got %.40q", image)
	}
}

func TestReviewPhotoSendsReferenceImageWhenGiven(t *testing.T) {
	srv, got := fakeServer(t, `{"complete": true, "confidence": 0.9, "feedback": "Matches the reference."}`, http.StatusOK)
	dir := t.TempDir()
	photo := filepath.Join(dir, "bed.jpg")
	if err := os.WriteFile(photo, []byte("\xff\xd8\xff\xe0jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(dir, "ref.jpg")
	if err := os.WriteFile(ref, []byte("\xff\xd8\xff\xe0refjpeg"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := New(srv.URL+"/v1/", "sk-test", "gemma").ReviewPhoto(context.Background(), "Make bed", "", photo, ref)
	if err != nil {
		t.Fatalf("ReviewPhoto: %v", err)
	}
	if !res.Complete {
		t.Errorf("unexpected result %+v", res)
	}
	parts := got.body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 4 {
		t.Fatalf("expected prompt+photo+ref-label+ref-photo (4 parts), got %d", len(parts))
	}
	text := parts[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "reference photo from the parent") {
		t.Errorf("expected the comparison clause in the prompt, got %q", text)
	}
	image := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(image, "data:image/jpeg;base64,") {
		t.Errorf("expected the submission image inline, got %.40q", image)
	}
	refImage := parts[3].(map[string]any)["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(refImage, "data:image/jpeg;base64,") {
		t.Errorf("expected the reference image inline, got %.40q", refImage)
	}
	if !strings.Contains(refImage, base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0refjpeg"))) {
		t.Errorf("reference part does not carry the reference photo bytes")
	}
}

func TestDraftDescriptionHasNoKeyOrSchema(t *testing.T) {
	srv, got := fakeServer(t, `"Put your dishes in the sink."`, http.StatusOK)
	desc, err := New(srv.URL, "", "m").DraftDescription(context.Background(), "Dishes", "core")
	if err != nil {
		t.Fatalf("DraftDescription: %v", err)
	}
	if desc != "Put your dishes in the sink." {
		t.Errorf("expected quotes trimmed, got %q", desc)
	}
	if got.auth != "" || got.body["response_format"] != nil {
		t.Errorf("expected no auth and no schema, got auth=%q format=%v", got.auth, got.body["response_format"])
	}
}

func TestChatReportsServerErrors(t *testing.T) {
	srv, _ := fakeServer(t, "model not found", http.StatusNotFound)
	_, err := New(srv.URL, "", "m").DraftDescription(context.Background(), "Dishes", "core")
	if err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("expected the server's error, got %v", err)
	}
}

func TestParseReviewResponse(t *testing.T) {
	cases := map[string]struct {
		raw        string
		complete   bool
		confidence float64
		wantErr    bool
	}{
		"plain":        {raw: `{"complete": true, "confidence": 0.8, "feedback": "ok"}`, complete: true, confidence: 0.8},
		"code fence":   {raw: "```json\n{\"complete\": false, \"confidence\": 0.4, \"feedback\": \"no\"}\n```", confidence: 0.4},
		"prose around": {raw: `Sure! {"complete": true, "confidence": 0.7, "feedback": "ok"} Hope that helps.`, complete: true, confidence: 0.7},
		"clamped high": {raw: `{"complete": true, "confidence": 3, "feedback": ""}`, complete: true, confidence: 1},
		"clamped low":  {raw: `{"complete": false, "confidence": -1, "feedback": ""}`, confidence: 0},
		"not json":     {raw: "I can't tell.", wantErr: true},
		"empty":        {raw: "", wantErr: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := parseReviewResponse(c.raw)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Complete != c.complete || res.Confidence != c.confidence {
				t.Errorf("got %+v", res)
			}
		})
	}
}

// P4-13: the pet's line survives a model that wraps its JSON in fences —
// the defensive parse must keep both the coach's and the pet's sentences.
func TestDailyNotePetLineParsesWrappedJSON(t *testing.T) {
	srv, _ := fakeServer(t, "```json\n{\"child\": \"你做到了！\", \"pet\": \"🐕 豆豆说：谢谢你今天喂我\", \"parent\": \"很好。\"}\n```", http.StatusOK)
	child, parent, err := New(srv.URL, "", "m").DailyNote(context.Background(), DailyNoteStats{
		KidName: "Kid", CompletedCount: 2, TotalAssigned: 2, PetName: "豆豆", PetMood: "happy", PetFedToday: true,
	})
	if err != nil {
		t.Fatalf("DailyNote: %v", err)
	}
	wantChild := "你做到了！ 🐕 豆豆说：谢谢你今天喂我"
	if child != wantChild {
		t.Fatalf("child = %q, want %q", child, wantChild)
	}
	if parent != "很好。" {
		t.Fatalf("parent = %q", parent)
	}
}

// Without a pet, a stray "pet" key the model dreamt up is ignored — the
// kid's line stays just the coach's sentence.
func TestDailyNoteIgnoresStrayPetWithoutPetStats(t *testing.T) {
	srv, _ := fakeServer(t, `{"child": "很好", "pet": "🐕 Someone says hi", "parent": "不错"}`, http.StatusOK)
	child, parent, err := New(srv.URL, "", "m").DailyNote(context.Background(), DailyNoteStats{
		KidName: "Kid", CompletedCount: 1, TotalAssigned: 1,
	})
	if err != nil {
		t.Fatalf("DailyNote: %v", err)
	}
	if child != "很好" {
		t.Fatalf("child = %q, want just the coach line", child)
	}
	if parent != "不错" {
		t.Fatalf("parent = %q", parent)
	}
}

// The pet prompt asks for a third "pet" field in the reply JSON and
// carries the buddy's name, mood and fed state.
func TestDailyNotePromptCarriesPet(t *testing.T) {
	srv, got := fakeServer(t, `{"child": "好", "pet": "🐕 豆豆说：好", "parent": "好"}`, http.StatusOK)
	_, _, err := New(srv.URL, "", "m").DailyNote(context.Background(), DailyNoteStats{
		KidName: "Kid", CompletedCount: 1, TotalAssigned: 1, PetName: "豆豆", PetMood: "hungry", PetFedToday: false,
	})
	if err != nil {
		t.Fatalf("DailyNote: %v", err)
	}
	prompt := got.body["messages"].([]any)[0].(map[string]any)["content"].(string)
	for _, want := range []string{"豆豆", "hungry", "not fed today yet", `"pet": "..."`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q: %s", want, prompt)
		}
	}
}
