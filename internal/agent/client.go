// Package agent talks to spk-agent (SPK-OS's single agent dispatch
// entry, ADR-MA-005): create a run, poll it to a terminal state, then
// pull the assistant's full text back through the items snapshot. This
// is the task-③ upgrade path — the household's text AI tasks (planning,
// summaries, measures) ride a real agent with tools instead of a bare
// chat completion, while photo review stays on the direct multimodal
// client (an agent instruction is text-only).
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config is one spk-agent connection. BaseURL is the service root
// (e.g. "http://192.168.96.1:8097"); Token is the optional bearer
// (SPK_AGENT_AUTH_TOKEN, empty when the household's spk-agent runs
// without auth); Model optionally overrides the agent's model;
// AgentRef is the omnigent agent name (default "spk-tester" — never a
// heavy spk-ipd-* name, per the dispatch safety boundary).
type Config struct {
	BaseURL  string
	Token    string
	Model    string
	AgentRef string
}

// Client is a configured spk-agent caller. Safe for concurrent use.
type Client struct {
	cfg Config
	http *http.Client
	// pollInterval is the await loop's tick (2s per the spk-sdk-agent
	// guidance); tests shrink it to keep the suite fast.
	pollInterval time.Duration
}

// New builds a client. The HTTP timeout only bounds single requests
// (create, each poll); the overall wait is the caller's context — an
// agent run takes seconds to minutes, so no request needs to hang.
func New(cfg Config) *Client {
	return &Client{
		cfg:          cfg,
		http:         &http.Client{Timeout: 30 * time.Second},
		pollInterval: 2 * time.Second,
	}
}

// Config returns the connection the client was built from.
func (c *Client) Config() Config { return c.cfg }

// terminal states (spk-sdk-agent §2: irreversible, don't guess).
var terminal = map[string]bool{
	"succeeded": true, "failed": true, "cancelled": true, "timed_out": true, "orphaned": true,
}

// Ask runs one instruction through the agent and returns the
// assistant's full reply text. It blocks: create → poll every
// pollInterval until a terminal state (or ctx ends) → fetch the items
// snapshot and join the assistant messages' text parts. The
// idempotency key makes the internal create retry safe: the same key
// returns the original run instead of forking a second one.
func (c *Client) Ask(ctx context.Context, instruction, idempotencyKey string) (string, error) {
	runID, err := c.create(ctx, instruction, idempotencyKey)
	if err != nil {
		return "", err
	}
	status, err := c.await(ctx, runID)
	if err != nil {
		return "", err
	}
	if status != "succeeded" {
		return "", fmt.Errorf("agent run %s: %s", runID, status)
	}
	return c.reply(ctx, runID)
}

// create posts /agent-runs (the three source fields plus the optional
// model) and retries once on a transport error with the same key.
func (c *Client) create(ctx context.Context, instruction, key string) (string, error) {
	body := map[string]any{
		"agent_ref":   c.agentRef(),
		"source_kind": "system",
		"source_name": "openchore",
		"title":       "openchore-AI任务-" + time.Now().Format("01-02"),
		"instruction": instruction,
	}
	if c.cfg.Model != "" {
		body["model"] = c.cfg.Model
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	var runID string
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+"/agent-runs", bytes.NewReader(payload))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil || attempt == 1 {
				return "", fmt.Errorf("agent create: %w", err)
			}
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode != http.StatusAccepted && res.StatusCode != http.StatusOK {
			return "", fmt.Errorf("agent create returned %d: %s", res.StatusCode, trunc(string(raw), 200))
		}
		var created struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(raw, &created); err != nil || created.RunID == "" {
			return "", fmt.Errorf("agent create response carried no run_id")
		}
		runID = created.RunID
		break
	}
	return runID, nil
}

// await polls GET /agent-runs/{id} every 2s (spk-sdk-agent: 2-5s) until
// a terminal state; the context bounds the overall wait.
func (c *Client) await(ctx context.Context, runID string) (string, error) {
	tick := time.NewTicker(c.pollInterval)
	defer tick.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.cfg.BaseURL, "/")+"/agent-runs/"+runID, nil)
		if err != nil {
			return "", err
		}
		if c.cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		}
		res, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			// A poll hiccup is not a failed run: try the next tick.
			if !sleep(ctx, tick) {
				return "", ctx.Err()
			}
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return "", fmt.Errorf("agent poll returned %d: %s", res.StatusCode, trunc(string(raw), 200))
		}
		var run struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &run); err != nil {
			return "", fmt.Errorf("agent poll response: %w", err)
		}
		if terminal[run.Status] {
			return run.Status, nil
		}
		if !sleep(ctx, tick) {
			return "", ctx.Err()
		}
	}
}

// sleep waits for the next tick or the context's end; false = ctx done.
func sleep(ctx context.Context, tick *time.Ticker) bool {
	select {
	case <-ctx.Done():
		return false
	case <-tick.C:
		return true
	}
}

// reply fetches the items snapshot and joins every assistant message's
// text parts (the omnigent PaginatedList the proxy passes through —
// role and content sit at the item's top level). Result summaries only
// carry a 200-char preview, so this snapshot is the one full-text
// channel (metadata-only red line: spk-agent never stores the body).
func (c *Client) reply(ctx context.Context, runID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.cfg.BaseURL, "/")+"/agent-runs/"+runID+"/items?limit=1000", nil)
	if err != nil {
		return "", err
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("agent items: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("agent items: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("agent items returned %d", res.StatusCode)
	}
	var page struct {
		Data []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return "", fmt.Errorf("agent items response: %w", err)
	}
	var texts []string
	for _, item := range page.Data {
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		for _, part := range item.Content {
			if part.Text != "" && (part.Type == "output_text" || part.Type == "text") {
				texts = append(texts, part.Text)
			}
		}
	}
	if len(texts) == 0 {
		return "", fmt.Errorf("agent run %s produced no assistant text", runID)
	}
	return strings.TrimSpace(strings.Join(texts, "\n")), nil
}

// agentRef defaults to the light tester agent (never a dot in the
// name; the IPD heavies are off-limits for third-party dispatch).
func (c *Client) agentRef() string {
	if c.cfg.AgentRef != "" {
		return c.cfg.AgentRef
	}
	return "spk-tester"
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
