package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A fake spk-agent: create → a few running polls → succeeded → items.
// It asserts the three source fields and the idempotency key on create.
func newFake(t *testing.T, polls int) *httptest.Server {
	t.Helper()
	var runID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/agent-runs"):
			if r.Header.Get("Idempotency-Key") == "" {
				t.Errorf("create without Idempotency-Key")
			}
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			for _, k := range []string{"source_kind", "source_name", "instruction"} {
				if body[k] == "" {
					t.Errorf("create missing %s", k)
				}
			}
			runID = "run_test123"
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"run_id": runID, "status": "accepted"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/agent-runs/"+runID):
			polls--
			status := "running"
			if polls < 0 {
				status = "succeeded"
			}
			json.NewEncoder(w).Encode(map[string]string{"status": status})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/items"):
			json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{
				map[string]any{"type": "message", "role": "user", "content": []any{
					map[string]string{"type": "input_text", "text": "hi"},
				}},
				map[string]any{"type": "message", "role": "assistant", "content": []any{
					map[string]string{"type": "output_text", "text": "方案A"},
					map[string]string{"type": "output_text", "text": "收到"},
				}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAskRunsAndExtractsReply(t *testing.T) {
	srv := newFake(t, 2)
	c := New(Config{BaseURL: srv.URL})
	c.pollInterval = time.Millisecond // fast polls in tests
	out, err := c.Ask(context.Background(), "做点什么", "test:key:1")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if out != "方案A\n收到" {
		t.Fatalf("reply = %q, want both assistant parts joined", out)
	}
}

func TestAskFailsNonSucceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(map[string]string{"run_id": "run_x"})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "failed"})
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL})
	c.pollInterval = time.Millisecond
	if _, err := c.Ask(context.Background(), "x", ""); err == nil {
		t.Fatal("a failed run must surface an error")
	}
}

// The model override rides the create body when set.
func TestModelOverrideSent(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			gotModel, _ = body["model"].(string)
			w.WriteHeader(202)
			json.NewEncoder(w).Encode(map[string]string{"run_id": "run_y"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/items") {
			json.NewEncoder(w).Encode(map[string]any{"data": []any{
				map[string]any{"type": "message", "role": "assistant", "content": []any{
					map[string]string{"type": "output_text", "text": "ok"},
				}},
			}})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "succeeded"})
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL, Model: "deepseek-v4-flash"})
	c.pollInterval = time.Millisecond
	if _, err := c.Ask(context.Background(), "x", ""); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if gotModel != "deepseek-v4-flash" {
		t.Fatalf("model = %q, want the override", gotModel)
	}
}

func TestUnreachable(t *testing.T) {
	c := New(Config{BaseURL: "http://127.0.0.1:1"})
	if _, err := c.Ask(context.Background(), "x", ""); err == nil {
		t.Fatal("an unreachable agent must error, not hang")
	}
	_ = fmt.Sprint()
}
