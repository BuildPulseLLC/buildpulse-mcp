# @buildpulse/mcp

[![npm version](https://img.shields.io/npm/v/@buildpulse/mcp.svg)](https://www.npmjs.com/package/@buildpulse/mcp)
[![npm downloads](https://img.shields.io/npm/dm/@buildpulse/mcp.svg)](https://www.npmjs.com/package/@buildpulse/mcp)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](https://github.com/BuildPulseLLC/buildpulse-mcp/blob/main/LICENSE)
[![MCP Registry](https://img.shields.io/badge/MCP%20Registry-io.github.BuildPulseLLC%2Fbuildpulse--mcp-3e82f7)](https://registry.modelcontextprotocol.io/v0/servers?search=buildpulse)

Model Context Protocol server for [BuildPulse](https://buildpulse.io) CI
test analytics. Nine read-only tools that surface flaky tests, recent CI
failures, per-run test results, flakiness % and code-coverage % for your
repositories — from Claude, ChatGPT, Cursor, VS Code, Windsurf, Cline,
Continue, Zed, and any other MCP-aware agent.

Source, threat model, and changelog:
<https://github.com/BuildPulseLLC/buildpulse-mcp>.

## Quickstart

You need a BuildPulse API token. Create one at <https://buildpulse.io> →
Organization Settings → API Tokens. Tokens look like `bp_<64 hex chars>`
(the older 40-character hex tokens still work).

```bash
BUILDPULSE_TOKEN=bp_... npx -y @buildpulse/mcp
```

Or pin globally:

```bash
npm install -g @buildpulse/mcp
```

The package downloads the matching native binary for your platform on
first install. Supported platforms: macOS (arm64, x64), Linux (arm64,
x64), Windows (x64). Node 18+.

Prefer not to run anything locally? The same server is hosted at
`https://mcp.buildpulse.io/mcp` (Streamable HTTP, Bearer token or OAuth)
— see [Hosted transport](#hosted-transport) below.

## Configure your client

Every client below reads the same JSON shape. Replace `bp_...` with your
token.

### Claude Code

```bash
# local stdio
claude mcp add buildpulse -e BUILDPULSE_TOKEN=bp_... -- npx -y @buildpulse/mcp

# or the hosted server (no local process; OAuth sign-in)
claude mcp add --transport http buildpulse https://mcp.buildpulse.io/mcp
```

### Claude Desktop

`~/Library/Application Support/Claude/claude_desktop_config.json` (macOS)
or `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "buildpulse": {
      "command": "npx",
      "args": ["-y", "@buildpulse/mcp"],
      "env": { "BUILDPULSE_TOKEN": "bp_..." }
    }
  }
}
```

### Cursor

`.cursor/mcp.json` in your project (or `~/.cursor/mcp.json` for all
projects):

```json
{
  "mcpServers": {
    "buildpulse": {
      "command": "npx",
      "args": ["-y", "@buildpulse/mcp"],
      "env": { "BUILDPULSE_TOKEN": "bp_..." }
    }
  }
}
```

### VS Code (GitHub Copilot agent mode)

`.vscode/mcp.json`:

```json
{
  "servers": {
    "buildpulse": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "@buildpulse/mcp"],
      "env": { "BUILDPULSE_TOKEN": "bp_..." }
    }
  }
}
```

### Windsurf

`~/.codeium/windsurf/mcp_config.json` — same `mcpServers` block as
Cursor.

### Cline

Cline → MCP Servers → Configure → paste the same `mcpServers` block into
`cline_mcp_settings.json`.

### Continue / Zed

Continue reads `mcpServers` under `experimental.modelContextProtocolServers`
in `~/.continue/config.json`; Zed reads `context_servers` in
`~/.config/zed/settings.json`. Both take the same `command` / `args` /
`env` fields.

### ChatGPT and Claude.ai (connectors)

Browser clients cannot spawn a local process, so use the hosted server:
add a custom connector and paste `https://mcp.buildpulse.io/mcp`. The
client completes the OAuth sign-in.

## Hosted transport

The same tools are served over Streamable HTTP at
`https://mcp.buildpulse.io/mcp`. Authenticate with
`Authorization: Bearer bp_...` or let the client run OAuth 2.1
(discovery at `/.well-known/oauth-authorization-server`). Cursor and
VS Code can point at the URL directly:

```json
{
  "mcpServers": {
    "buildpulse": {
      "url": "https://mcp.buildpulse.io/mcp",
      "headers": { "Authorization": "Bearer bp_..." }
    }
  }
}
```

## Tools

| Tool | What it returns |
|------|-----------------|
| `list_my_organizations` | Every organization the token can access, with the `id` UUID to pass as `organization_id`. |
| `list_repositories` | Repositories BuildPulse is monitoring for an organization. |
| `find_flaky_tests` | A repository's flaky-test inventory over the last 14 days, sorted by disruptiveness or recency; filter by tags, free text, or since-date. |
| `get_test_history` | Recent disruption events for one test, each with build URL and commit SHA. |
| `list_recent_submissions` | The most recent CI runs that uploaded results for a repository. |
| `get_submission_test_results` | Per-test results for one CI run; `status="failed"` narrows to the red set. |
| `get_recent_failures` | Every test that failed across the last N CI runs, aggregated by test identity. Not filtered by the flakiness threshold. |
| `get_repo_flakiness` | Current flakiness % for a repository (last 14 days). `-1` means no recent results. |
| `get_repo_coverage` | Current code-coverage % from the latest uploaded report. `-1` means no report. |

Every output that names a test or repository includes a `web_url`
deep-link into the BuildPulse web app.

## Organizations (multi-tenant)

A token may grant access to more than one organization. Every
repo-scoped tool takes an optional `organization_id` (the `id` from
`list_my_organizations`):

- **One organization** — omit it; the tool defaults to your only org.
- **Two or more** — pass it on every repo-scoped call. The server does
  not pick one for you: omitting it returns an error that lists each
  accessible organization and its UUID so the agent can retry with the
  right one.

## Prompts and resources

Four guided prompts, slash-pickable in clients that support them:
`triage_flaky_tests`, `ci_health_check`, `explain_test_failure`,
`whats_red`.

Two resource templates for pulling state into context without a tool
call: `buildpulse://repos/{repo}/flaky-tests` and
`buildpulse://repos/{owner}/{name}/submissions`.

## Try asking

- "Why is CI red on `web-client`? Show me the failing tests from the last run."
- "Which tests in `platform-api` have been flakiest this week, and where do they fail?"
- "How well tested is `agents`? Give me flakiness and coverage."
- "Which tests failed in more than one of the last 10 runs of `api`?"
- "Triage the flaky tests in `frontend` and tell me which to quarantine first."

## Environment variables

| Variable | Required | Default |
|----------|----------|---------|
| `BUILDPULSE_TOKEN` | yes | — |
| `PLATFORM_API_URL` | no | `https://platform.buildpulse.io` |

## Troubleshooting

- **`authentication failed (401)`** — the token is wrong, revoked, or
  your organization has no active plan. Confirm with:
  ```bash
  curl -i -H "Authorization: Bearer $BUILDPULSE_TOKEN" https://platform.buildpulse.io/api
  ```
  Expected: `HTTP/1.1 204 No Content`.

- **Empty results for a repo you know has data** — you are probably in
  more than one organization. Call `list_my_organizations` and pass the
  right `organization_id`.

- **Client shows "server failed to start"** — open the client's MCP log
  panel; stderr names the cause (missing token, binary download
  failure).

- **Binary download failed during install** — set
  `BUILDPULSE_MCP_SKIP_INSTALL=1` and build from source:
  ```bash
  git clone https://github.com/BuildPulseLLC/buildpulse-mcp
  cd buildpulse-mcp && go build -o /usr/local/bin/buildpulse-mcp ./cmd/mcp
  ```

## License

MIT. See [LICENSE](https://github.com/BuildPulseLLC/buildpulse-mcp/blob/main/LICENSE).
