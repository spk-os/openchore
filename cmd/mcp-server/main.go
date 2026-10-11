// Package main implements the OpenChore MCP sidecar (P3-12): a standalone
// Model Context Protocol server that exposes the household's chores,
// balances, rewards and reports to external agents (SPK-OS voice
// assistants, review agents, ...).
//
// It is deliberately a sidecar — it never links OpenChore's internals and
// only speaks the public REST API with a forwarded API token. Upstream
// upgrades can never conflict with it.
//
// Transport: streamable HTTP (POST /mcp) with JSON-RPC 2.0 envelopes, plus
// a plain GET /healthz for container probes. Auth: the client's
// Authorization header (API token or session bearer) is forwarded as-is to
// every OpenChore call, so agents hold exactly the permissions their token
// grants.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	baseURL := strings.TrimRight(os.Getenv("OPENCHORE_URL"), "/")
	if baseURL == "" {
		baseURL = "http://api:8080"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8090"
	}

	client := &client{baseURL: baseURL, http: &http.Client{Timeout: 30 * time.Second}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /mcp", func(w http.ResponseWriter, r *http.Request) {
		serveMCP(w, r, client)
	})

	log.Printf("openchore-mcp: listening on :%s, upstream %s", port, baseURL)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

// --- JSON-RPC 2.0 / MCP envelopes ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// toolDef is one entry of the MCP tools list.
type toolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

// jsonSchema builds a loose object schema: string/integer/number/boolean
// properties, optional with defaults.
type schemaProp struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Default     any      `json:"default,omitempty"`
}

type schemaArgs struct {
	Type       string                `json:"type"`
	Properties map[string]schemaProp `json:"properties"`
	Required   []string              `json:"required,omitempty"`
}

// serveMCP answers one JSON-RPC request. Batches (arrays) are also
// accepted — small agents send them and MCP 2025-03-26 allows it.
func serveMCP(w http.ResponseWriter, r *http.Request, c *client) {
	w.Header().Set("Content-Type", "application/json")

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeRPC(w, nil, &rpcError{Code: codeParseError, Message: "read body: " + err.Error()})
		return
	}

	// Batch?
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var reqs []rpcRequest
		if err := json.Unmarshal(body, &reqs); err != nil {
			writeRPC(w, nil, &rpcError{Code: codeParseError, Message: "invalid batch JSON"})
			return
		}
		resps := make([]rpcResponse, 0, len(reqs))
		for _, req := range reqs {
			resps = append(resps, dispatch(r.Context(), req, c, r.Header.Get("Authorization")))
		}
		_ = json.NewEncoder(w).Encode(resps)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, nil, &rpcError{Code: codeParseError, Message: "invalid JSON"})
		return
	}
	resp := dispatch(r.Context(), req, c, r.Header.Get("Authorization"))
	// Notifications (no id) are answered with nothing but 200.
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// dispatch routes one request to the MCP method handlers.
func dispatch(ctx context.Context, req rpcRequest, c *client, auth string) rpcResponse {
	switch req.Method {
	case "initialize":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2025-03-26",
				"serverInfo":      map[string]string{"name": "openchore", "version": "1.0.0"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			},
		}
	case "ping":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": tools()}}
	case "tools/call":
		return callTool(ctx, req, c, auth)
	default:
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeMethodNotFound, Message: "unknown method " + req.Method}}
	}
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, e *rpcError) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: id, Error: e})
}

// --- The tool set (design 12.3: Agent's minimal sufficient set) ---

func tools() []toolDef {
	return []toolDef{
		{
			Name:        "list_users",
			Description: "List the household's members (id, name, role, avatar) so the agent can resolve names to ids before calling other tools.",
			InputSchema: schemaArgs{Type: "object", Properties: map[string]schemaProp{}},
		},
		{
			Name:        "list_chores",
			Description: "One person's chores for a day (default today): title, category, points, done state and completion photo in a single aggregated answer.",
			InputSchema: schemaArgs{
				Type: "object",
				Properties: map[string]schemaProp{
					"user_id": {Type: "integer", Description: "The person whose chores to list."},
					"date":    {Type: "string", Description: "Day to look at, YYYY-MM-DD. Defaults to today."},
					"view":    {Type: "string", Description: "daily (default) or weekly.", Enum: []string{"daily", "weekly"}, Default: "daily"},
				},
				Required: []string{"user_id"},
			},
		},
		{
			Name:        "complete_chore",
			Description: "Tick off one chore on someone's behalf (e.g. the kid says \"I finished my math homework\" to a voice assistant). The completion still goes through OpenChore's normal approval flow — AI review and parent sign-off cannot be bypassed.",
			InputSchema: schemaArgs{
				Type: "object",
				Properties: map[string]schemaProp{
					"schedule_id":     {Type: "integer", Description: "The schedule to complete (from list_chores' schedule_id)."},
					"completion_date": {Type: "string", Description: "YYYY-MM-DD; defaults to today."},
				},
				Required: []string{"schedule_id"},
			},
		},
		{
			Name:        "query_balance",
			Description: "Someone's point balance, savings goals and today's earned points — answers \"how many points do I have?\".",
			InputSchema: schemaArgs{
				Type:       "object",
				Properties: map[string]schemaProp{"user_id": {Type: "integer", Description: "The person to ask about."}},
				Required:   []string{"user_id"},
			},
		},
		{
			Name:        "list_rewards",
			Description: "The reward store visible to a person: names, icons, costs — answers \"what can I redeem?\".",
			InputSchema: schemaArgs{Type: "object", Properties: map[string]schemaProp{}},
		},
		{
			Name:        "get_daily_report",
			Description: "The household's report for a period (day/week/month), or one person's narrative AI summary: totals, completion rates and per-kid rows for follow-up conversation.",
			InputSchema: schemaArgs{
				Type: "object",
				Properties: map[string]schemaProp{
					"user_id": {Type: "integer", Description: "A specific person's narrative summary. Omit for the whole-household report."},
					"period":  {Type: "string", Description: "week (default), month or year.", Enum: []string{"week", "month", "year"}, Default: "week"},
					"date":    {Type: "string", Description: "Reference day, YYYY-MM-DD; defaults to today."},
				},
			},
		},
		{
			Name:        "get_streak",
			Description: "Someone's current and longest streak plus the next milestone — answers \"how many days in a row?\".",
			InputSchema: schemaArgs{
				Type:       "object",
				Properties: map[string]schemaProp{"user_id": {Type: "integer", Description: "The person to ask about."}},
				Required:   []string{"user_id"},
			},
		},
		{
			Name:        "create_chore_draft",
			Description: "Draft new chores from a parent-style description (the Copilot, agent edition). Nothing is created: the household's parent must apply the draft from the admin Quick Assign drawer. Use this when asked to set up tasks; never for survival obligations (eating, dressing, hygiene).",
			InputSchema: schemaArgs{
				Type: "object",
				Properties: map[string]schemaProp{
					"assigned_to": {Type: "integer", Description: "The kid the draft is for."},
					"request":     {Type: "string", Description: "What the tasks should cover, e.g. \"20 English words every day next week\"."},
				},
				Required: []string{"assigned_to", "request"},
			},
		},
	}
}

// callTool runs tools/call: parse name+arguments, fetch from OpenChore,
// wrap the payload as MCP tool content.
func callTool(ctx context.Context, req rpcRequest, c *client, auth string) rpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params"}}
	}

	var (
		result any
		err    error
	)
	switch params.Name {
	case "list_users":
		result, err = c.get(ctx, auth, "/api/users")
	case "list_chores":
		var args struct {
			UserID int64  `json:"user_id"`
			Date   string `json:"date"`
			View   string `json:"view"`
		}
		if err := unmarshalArgs(params.Arguments, &args); err != nil {
			return badParams(req, err)
		}
		if args.View == "" {
			args.View = "daily"
		}
		result, err = c.get(ctx, auth, fmt.Sprintf("/api/users/%d/chores?view=%s&date=%s", args.UserID, args.View, args.Date))
	case "complete_chore":
		var args struct {
			ScheduleID     int64  `json:"schedule_id"`
			CompletionDate string `json:"completion_date"`
		}
		if err := unmarshalArgs(params.Arguments, &args); err != nil {
			return badParams(req, err)
		}
		payload := map[string]any{}
		if args.CompletionDate != "" {
			payload["completion_date"] = args.CompletionDate
		}
		result, err = c.post(ctx, auth, fmt.Sprintf("/api/schedules/%d/complete", args.ScheduleID), payload)
	case "query_balance":
		var args struct {
			UserID int64 `json:"user_id"`
		}
		if err := unmarshalArgs(params.Arguments, &args); err != nil {
			return badParams(req, err)
		}
		result, err = c.get(ctx, auth, fmt.Sprintf("/api/users/%d/points", args.UserID))
	case "list_rewards":
		result, err = c.get(ctx, auth, "/api/rewards")
	case "get_daily_report":
		var args struct {
			UserID int64  `json:"user_id"`
			Period string `json:"period"`
			Date   string `json:"date"`
		}
		if err := unmarshalArgs(params.Arguments, &args); err != nil {
			return badParams(req, err)
		}
		if args.Period == "" {
			args.Period = "week"
		}
		if args.UserID != 0 {
			result, err = c.get(ctx, auth, fmt.Sprintf("/api/admin/reports/ai-summary?user_id=%d&period=%s&date=%s", args.UserID, args.Period, args.Date))
		} else {
			result, err = c.get(ctx, auth, fmt.Sprintf("/api/admin/reports?period=%s&date=%s", args.Period, args.Date))
		}
	case "get_streak":
		var args struct {
			UserID int64 `json:"user_id"`
		}
		if err := unmarshalArgs(params.Arguments, &args); err != nil {
			return badParams(req, err)
		}
		result, err = c.get(ctx, auth, fmt.Sprintf("/api/users/%d/streak", args.UserID))
	case "create_chore_draft":
		var args struct {
			AssignedTo int64  `json:"assigned_to"`
			Request    string `json:"request"`
		}
		if err := unmarshalArgs(params.Arguments, &args); err != nil {
			return badParams(req, err)
		}
		result, err = c.post(ctx, auth, "/api/admin/copilot/draft", map[string]any{
			"assigned_to": args.AssignedTo,
			"mode":        "text",
			"request":     args.Request,
		})
	default:
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeInvalidParams, Message: "unknown tool " + params.Name}}
	}

	if err != nil {
		// Upstream errors surface as a normal MCP tool error result
		// (isError: true), not a JSON-RPC protocol error, so the agent
		// can read the message and react.
		return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: toolResult(err.Error(), true)}
	}
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: toolResult(result, false)}
}

func unmarshalArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

func badParams(req rpcRequest, err error) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: codeInvalidParams, Message: err.Error()}}
}

// toolResult wraps a payload (or an error message) as MCP content.
func toolResult(payload any, isError bool) map[string]any {
	var text string
	switch p := payload.(type) {
	case string:
		text = p
	default:
		b, err := json.Marshal(payload)
		if err != nil {
			text = fmt.Sprintf("%v", payload)
		} else {
			text = string(b)
		}
	}
	content := []map[string]any{{"type": "text", "text": text}}
	out := map[string]any{"content": content}
	if isError {
		out["isError"] = true
	}
	return out
}

// --- REST client with token pass-through ---

type client struct {
	baseURL string
	http    *http.Client
}

// apiError carries the upstream status so agents can distinguish "chore
// not found" (404) from "token invalid" (401) in the message text.
type apiError struct {
	status int
	body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("openchore %d: %s", e.status, e.body)
}

func (c *client) do(ctx context.Context, auth, method, path string, body any) (any, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// The caller's credential — an OpenChore API token or a session
	// bearer — decides what this call may do. The sidecar holds none of
	// its own.
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &apiError{status: resp.StatusCode, body: strings.TrimSpace(string(raw))}
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		// Some endpoints answer non-JSON (204 and friends); hand the
		// raw body through so the agent still sees something.
		return map[string]any{"raw": strings.TrimSpace(string(raw))}, nil
	}
	return out, nil
}

func (c *client) get(ctx context.Context, auth, path string) (any, error) {
	return c.do(ctx, auth, http.MethodGet, path, nil)
}

func (c *client) post(ctx context.Context, auth, path string, body any) (any, error) {
	return c.do(ctx, auth, http.MethodPost, path, body)
}
