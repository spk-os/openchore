package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeOpenChore records what the sidecar asked for and answers canned JSON.
type fakeOpenChore struct {
	mu      chan string // every request line: "METHOD /path"
	auth    string
	handler func(w http.ResponseWriter, r *http.Request)
}

func (f *fakeOpenChore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu <- r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery
	f.auth = r.Header.Get("Authorization")
	if f.handler != nil {
		f.handler(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok": true}`))
}

// newSidecar spins up a fake upstream and the MCP handler in front of it.
func newSidecar(t *testing.T) (*httptest.Server, *fakeOpenChore) {
	t.Helper()
	upstream := &fakeOpenChore{mu: make(chan string, 16)}
	api := httptest.NewServer(upstream)
	t.Cleanup(api.Close)

	client := &client{baseURL: api.URL, http: api.Client()}
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		serveMCP(w, r, client)
	}))
	t.Cleanup(mcp.Close)
	return mcp, upstream
}

func rpc(t *testing.T, mcp *httptest.Server, auth string, payload string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, mcp.URL+"/mcp", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := mcp.Client().Do(req)
	if err != nil {
		t.Fatalf("post /mcp: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("response is not JSON (%s): %s", resp.Status, raw)
	}
	return resp.StatusCode, out
}

// rpcCall sends a tools/call and returns the text content.
func rpcCall(t *testing.T, mcp *httptest.Server, auth, name, args string) (string, bool) {
	t.Helper()
	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
	_, out := rpc(t, mcp, auth, payload)
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %v", out)
	}
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %v", content)
	}
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	isErr, _ := result["isError"].(bool)
	return text, isErr
}

func TestHealthz(t *testing.T) {
	mcp, _ := newSidecar(t)
	resp, err := mcp.Client().Get(mcp.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
}

func TestInitialize(t *testing.T) {
	mcp, _ := newSidecar(t)
	status, out := rpc(t, mcp, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if out["jsonrpc"] != "2.0" {
		t.Fatalf("jsonrpc = %v", out["jsonrpc"])
	}
	result, _ := out["result"].(map[string]any)
	if result == nil {
		t.Fatalf("missing result: %v", out)
	}
	if result["protocolVersion"] != "2025-03-26" {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "openchore" {
		t.Fatalf("serverInfo = %v", result["serverInfo"])
	}
}

func TestPing(t *testing.T) {
	mcp, _ := newSidecar(t)
	_, out := rpc(t, mcp, "", `{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	if out["id"] != float64(7) {
		t.Fatalf("id = %v", out["id"])
	}
	if _, ok := out["result"]; !ok {
		t.Fatalf("missing result: %v", out)
	}
}

// TestToolsList pins the design's tool set (10 册 12.3): read-mostly, two
// controlled writes, one draft-only creator.
func TestToolsList(t *testing.T) {
	mcp, _ := newSidecar(t)
	_, out := rpc(t, mcp, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	result, _ := out["result"].(map[string]any)
	toolsList, _ := result["tools"].([]any)
	want := []string{
		"list_users", "list_chores", "complete_chore", "query_balance",
		"list_rewards", "get_daily_report", "get_streak", "create_chore_draft",
	}
	if len(toolsList) != len(want) {
		t.Fatalf("expected %d tools, got %d (%v)", len(want), len(toolsList), toolsList)
	}
	for i, def := range toolsList {
		tool, _ := def.(map[string]any)
		if tool["name"] != want[i] {
			t.Fatalf("tool[%d] = %v, want %s", i, tool["name"], want[i])
		}
		if tool["description"] == "" {
			t.Fatalf("tool %s has no description", want[i])
		}
		schema, _ := tool["inputSchema"].(map[string]any)
		if schema == nil || schema["type"] != "object" {
			t.Fatalf("tool %s inputSchema = %v", want[i], tool["inputSchema"])
		}
	}
}

func TestCallListChoresForwardsPathAndToken(t *testing.T) {
	mcp, upstream := newSidecar(t)
	text, isErr := rpcCall(t, mcp, "Bearer ocs1.test", "list_chores", `{"user_id":3,"date":"2026-10-11"}`)
	if isErr {
		t.Fatalf("unexpected tool error: %s", text)
	}
	got := <-upstream.mu
	if got != "GET /api/users/3/chores?view=daily&date=2026-10-11" {
		t.Fatalf("upstream path = %q", got)
	}
	if upstream.auth != "Bearer ocs1.test" {
		t.Fatalf("Authorization not forwarded: %q", upstream.auth)
	}
}

func TestCallCompleteChorePostsAndDefaultsToday(t *testing.T) {
	mcp, upstream := newSidecar(t)
	text, isErr := rpcCall(t, mcp, "Bearer ocs1.test", "complete_chore", `{"schedule_id":9,"completion_date":"2026-10-10"}`)
	if isErr {
		t.Fatalf("unexpected tool error: %s", text)
	}
	got := <-upstream.mu
	if got != "POST /api/schedules/9/complete?" {
		t.Fatalf("upstream path = %q", got)
	}
}

func TestCallGetDailyReportBranches(t *testing.T) {
	mcp, upstream := newSidecar(t)
	// Per-kid narrative → ai-summary.
	if _, isErr := rpcCall(t, mcp, "", "get_daily_report", `{"user_id":5,"period":"month"}`); isErr {
		t.Fatal("unexpected error on user branch")
	}
	if got := <-upstream.mu; got != "GET /api/admin/reports/ai-summary?user_id=5&period=month&date=" {
		t.Fatalf("user branch = %q", got)
	}
	// Whole household → admin reports, default period week.
	if _, isErr := rpcCall(t, mcp, "", "get_daily_report", `{}`); isErr {
		t.Fatal("unexpected error on household branch")
	}
	if got := <-upstream.mu; got != "GET /api/admin/reports?period=week&date=" {
		t.Fatalf("household branch = %q", got)
	}
}

func TestCallCreateChoreDraftPassesTextMode(t *testing.T) {
	mcp, upstream := newSidecar(t)
	text, isErr := rpcCall(t, mcp, "Bearer ocs1.test", "create_chore_draft", `{"assigned_to":5,"request":"每天背20个单词"}`)
	if isErr {
		t.Fatalf("unexpected tool error: %s", text)
	}
	got := <-upstream.mu
	if got != "POST /api/admin/copilot/draft?" {
		t.Fatalf("upstream path = %q", got)
	}
}

func TestUpstreamErrorIsToolErrorNotProtocolError(t *testing.T) {
	upstream := &fakeOpenChore{mu: make(chan string, 16), handler: func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"schedule not found"}`))
	}}
	api := httptest.NewServer(upstream)
	defer api.Close()
	client := &client{baseURL: api.URL, http: api.Client()}
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveMCP(w, r, client)
	}))
	defer mcp.Close()

	text, isErr := rpcCall(t, mcp, "", "complete_chore", `{"schedule_id":999}`)
	if !isErr {
		t.Fatalf("expected isError result, got %q", text)
	}
	if !strings.Contains(text, "404") || !strings.Contains(text, "schedule not found") {
		t.Fatalf("error text should carry upstream status and body, got %q", text)
	}
}

func TestUnknownToolAndBadParams(t *testing.T) {
	mcp, _ := newSidecar(t)
	// Unknown tool → JSON-RPC error, not a tool result.
	status, out := rpc(t, mcp, "", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nuke_family","arguments":{}}}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	rpcErr, _ := out["error"].(map[string]any)
	if rpcErr == nil || rpcErr["code"] != float64(codeInvalidParams) {
		t.Fatalf("expected -32602 error, got %v", out)
	}
	// Malformed arguments → -32602.
	_, out = rpc(t, mcp, "", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_chores","arguments":42}}`)
	rpcErr, _ = out["error"].(map[string]any)
	if rpcErr == nil || rpcErr["code"] != float64(codeInvalidParams) {
		t.Fatalf("expected -32602 for bad arguments, got %v", out)
	}
}

func TestUnknownMethod(t *testing.T) {
	mcp, _ := newSidecar(t)
	_, out := rpc(t, mcp, "", `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	rpcErr, _ := out["error"].(map[string]any)
	if rpcErr == nil || rpcErr["code"] != float64(codeMethodNotFound) {
		t.Fatalf("expected -32601, got %v", out)
	}
}

func TestBatchRequests(t *testing.T) {
	mcp, _ := newSidecar(t)
	body := `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`
	resp, err := mcp.Client().Post(mcp.URL+"/mcp", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("post batch: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var resps []map[string]any
	if err := json.Unmarshal(raw, &resps); err != nil {
		t.Fatalf("batch response is not a JSON array: %s", raw)
	}
	if len(resps) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(resps))
	}
	if resps[0]["id"] != float64(1) || resps[1]["id"] != float64(2) {
		t.Fatalf("batch ids = %v, %v", resps[0]["id"], resps[1]["id"])
	}
}

func TestInvalidJSON(t *testing.T) {
	mcp, _ := newSidecar(t)
	resp, err := mcp.Client().Post(mcp.URL+"/mcp", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	rpcErr, _ := out["error"].(map[string]any)
	if rpcErr == nil || rpcErr["code"] != float64(codeParseError) {
		t.Fatalf("expected -32700, got %v", out)
	}
}

func TestClientPassesContext(t *testing.T) {
	// A cancelled context must abort the upstream call, not hang.
	api := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		// Never answers; the client must give up on context.
		select {}
	}))
	defer api.Close()
	c := &client{baseURL: api.URL, http: api.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx, "", "/api/users"); err == nil {
		t.Fatal("expected context error, got nil")
	}
}
