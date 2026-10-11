# MCP sidecar

OpenChore ships an optional [Model Context Protocol](https://modelcontextprotocol.io)
server — the **MCP sidecar** — that lets external agents talk to the
household: voice assistants (“What are my chores today?”, “I finished my
math homework”), SPK-OS family agents, scripts or anything else that speaks
MCP.

It is a *sidecar* on purpose: a separate process that only calls OpenChore's
public REST API. It links none of OpenChore's internals, so upstream
upgrades can never conflict with it, and it can be left out entirely without
affecting the app.

## Run it

```bash
# In the OpenChore checkout (Docker):
docker compose --profile mcp up -d
# → the sidecar answers on http://localhost:8090/mcp

# Or as a plain Go binary next to a running API:
OPENCHORE_URL=http://localhost:8080 PORT=8090 \
  go run cmd/mcp-server/main.go
```

| Variable | Default | Meaning |
|----------|---------|---------|
| `OPENCHORE_URL` | `http://api:8080` | Base URL of OpenChore's API |
| `PORT` | `8090` | Port the sidecar listens on |

The MCP endpoint is `POST /mcp` (JSON-RPC 2.0, the streamable-HTTP
transport: initialize, ping, tools/list, tools/call, plus request batches).
`GET /healthz` answers `ok` for container probes.

## Authentication

The sidecar holds **no credentials of its own**. Every call must carry an
`Authorization` header — an OpenChore API token (`Bearer <token>`, created
under *Settings → API tokens*, acts as an admin) or a session bearer —
which the sidecar forwards verbatim to the API. An agent can therefore do
exactly what the token it was given allows, and a family can revoke a
agent's access by revoking one token.

API-token auth in an MCP client looks like:

```json
{
  "mcpServers": {
    "openchore": {
      "type": "http",
      "url": "http://localhost:8090/mcp",
      "headers": { "Authorization": "Bearer ocs1.your-api-token" }
    }
  }
}
```

## Tools

Eight tools — the read-mostly minimum an agent needs, plus two controlled
writes. Everything else stays behind the app's own UI.

| Tool | Maps to | Writes? |
|------|---------|---------|
| `list_users` | `GET /api/users` | no |
| `list_chores` | `GET /api/users/{id}/chores?view=daily&date=` | no |
| `complete_chore` | `POST /api/schedules/{id}/complete` | **yes** |
| `query_balance` | `GET /api/users/{id}/points` | no |
| `list_rewards` | `GET /api/rewards` | no |
| `get_daily_report` | `GET /api/admin/reports[?period=&date=]` or `/ai-summary?user_id=&period=&date=` | no |
| `get_streak` | `GET /api/users/{id}/streak` | no |
| `create_chore_draft` | `POST /api/admin/copilot/draft` | **draft only** |

The two writes are deliberately narrow:

- **`complete_chore`** ticks off a chore on someone's behalf (the kid tells
  the speaker “I'm done”). It goes through the normal approval flow — AI
  review and parent sign-off apply exactly as if the kid had tapped the
  button in the app. Nothing can be bypassed.
- **`create_chore_draft`** is the parent Copilot, agent edition: it produces
  a *draft* (task cards) and nothing more. The parent applies the draft from
  the Quick Assign drawer in the admin UI. The tool refuses survival
  obligations (eating, dressing, hygiene) just like the in-app Copilot.

A typical voice round-trip: `list_users` (resolve “Emma” → id) →
`list_chores` → `complete_chore` → `query_balance` → speak the new balance.
That whole loop uses four read-only tools and one controlled write, and the
parent still approves the completion in the end.

Upstream errors come back as normal MCP tool results with `isError: true`
and the API's status and message in the text (e.g. `openchore 404: schedule
not found`), so an agent can react — a 401 means “my token was revoked”,
a 404 means “no such chore today”.

## Registering with SPK-OS

The sidecar speaks plain streamable HTTP, so any MCP-capable host can use
it. In SPK-OS's MCP infrastructure, register it with the URL and an
OpenChore API token as above; the family's voice agent then gets the tool
set immediately. Revoke the token to cut an agent off.
