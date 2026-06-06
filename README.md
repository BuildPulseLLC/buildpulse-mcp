# BuildPulse MCP

> Model Context Protocol server for the [BuildPulse](https://buildpulse.io)
> Platform API. Surface flaky tests, CI run history, and coverage health
> in Claude Desktop, Cursor, ChatGPT, Cline, Windsurf, Continue, Zed,
> VS Code Copilot, and any other MCP-aware AI agent.

[![npm version](https://img.shields.io/npm/v/@buildpulse/mcp.svg)](https://www.npmjs.com/package/@buildpulse/mcp)
[![Install on Smithery](https://img.shields.io/badge/Install-Smithery-blueviolet)](https://smithery.ai)
[![Docs](https://img.shields.io/badge/Docs-platform.buildpulse.io%2Fdocs%2Fmcp-3e82f7)](https://platform.buildpulse.io/docs/mcp)

## Install

```bash
npx -y @buildpulse/mcp
```

Or pin globally:

```bash
npm install -g @buildpulse/mcp
```

The package downloads the matching native binary for your platform on
first install. Supported platforms: macOS arm64/x64, Linux arm64/x64,
Windows x64.

## Configure

Get a BuildPulse API token at <https://buildpulse.io> → Organization
Settings → API Tokens.

### Claude Desktop

`~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "buildpulse": {
      "command": "npx",
      "args": ["-y", "@buildpulse/mcp"],
      "env": { "BUILDPULSE_TOKEN": "your-buildpulse-api-token" }
    }
  }
}
```

### Cursor

`.cursor/mcp.json` (per-project) or `~/.cursor/mcp.json` (global):
same JSON shape.

### Other clients

Cline, Continue, Windsurf, Zed, and VS Code Copilot all read an
`mcpServers` block in their respective config files. See the
[install hub](https://platform.buildpulse.io/docs/mcp) for copy-paste
snippets per client.

## Tools

| Tool | Purpose |
|------|---------|
| `find_flaky_tests` | Search a repository's flaky test inventory; filter by tags, recency, free-text. |
| `get_test_history` | Recent disruption events for a specific test. |
| `list_recent_submissions` | Recent CI runs for a repository. |
| `get_repo_flakiness` | Current flakiness % over the last 14 days. |
| `get_repo_coverage` | Current coverage % from the latest report. |

Every output that names a test or repo includes a `web_url` deep-link
back to the BuildPulse web app — the same polish Sentry / Atlassian
use in their MCP responses.

## Prompts

The server also ships four guided prompts (slash-pickable in clients
that support them):

- `/triage_flaky_tests`
- `/ci_health_check`
- `/explain_test_failure`
- `/whats_red`

## Two transports

| Transport | Binary | Where it goes |
|---|---|---|
| **stdio** | [`cmd/mcp`](./cmd/mcp) | npm → `npx -y @buildpulse/mcp` |
| **Streamable HTTP** | [`cmd/mcp-remote`](./cmd/mcp-remote) | hosted at `https://mcp.buildpulse.io/mcp` |

Same tool surface; same prompts; same resources. Pick whichever your
client supports. The stdio path is universal; the hosted variant is
the path to Claude.ai web and ChatGPT.

## Resources

The server exposes two MCP resource templates so agents can pull
state into context without a tool call:

- `buildpulse://repos/{repo}/flaky-tests`
- `buildpulse://repos/{owner}/{name}/submissions`

## Environment variables

| Variable | Required | Default |
|---|---|---|
| `BUILDPULSE_TOKEN` | yes | — |
| `PLATFORM_API_URL` | no | `https://platform.buildpulse.io` |

## Build from source

```bash
git clone https://github.com/BuildPulseLLC/buildpulse-mcp
cd buildpulse-mcp
go build -o ./bin/buildpulse-mcp ./cmd/mcp
go build -o ./bin/buildpulse-mcp-remote ./cmd/mcp-remote
```

Requires Go 1.24+.

## Run tests

```bash
go test ./...
```

## License

MIT — see [LICENSE](./LICENSE).

## Related

- [BuildPulse Platform API](https://platform.buildpulse.io/docs) — the underlying public REST API
- [@buildpulse/mcp on npm](https://www.npmjs.com/package/@buildpulse/mcp)
- [Distribution strategy](./DISTRIBUTION.md) — Claude, OpenAI, Smithery, Cursor publishing details
- [`/docs/mcp`](https://platform.buildpulse.io/docs/mcp) — branded install hub with copy buttons
