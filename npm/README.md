# @buildpulse/mcp

Model Context Protocol server for the [BuildPulse](https://buildpulse.io)
Platform API. Lets Claude Desktop, Cursor, Cline, Windsurf, Continue,
ChatGPT (via MCP-enabled clients), and any other MCP-aware agent query
your CI test analytics directly.

## Install

```bash
npx -y @buildpulse/mcp
```

Or pin globally:

```bash
npm install -g @buildpulse/mcp
```

The package downloads the matching native binary for your platform on
first install. Supported platforms: macOS (arm64, x64), Linux (arm64,
x64), Windows (x64).

## Configure

You need a BuildPulse API token. Generate one at
<https://app.buildpulse.io> → Organization Settings → API Tokens.

### Claude Desktop

Edit `~/Library/Application Support/Claude/claude_desktop_config.json`
(macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "buildpulse": {
      "command": "npx",
      "args": ["-y", "@buildpulse/mcp"],
      "env": {
        "BUILDPULSE_TOKEN": "your-40-char-hex-token"
      }
    }
  }
}
```

### Cursor

Edit `.cursor/mcp.json` in your project (or `~/.cursor/mcp.json` for
all projects):

```json
{
  "mcpServers": {
    "buildpulse": {
      "command": "npx",
      "args": ["-y", "@buildpulse/mcp"],
      "env": {
        "BUILDPULSE_TOKEN": "your-40-char-hex-token"
      }
    }
  }
}
```

### Cline / Continue / Windsurf

Use the same JSON snippet — these clients all read `mcpServers` in
their respective config files.

## Tools

| Tool                       | Purpose |
|----------------------------|---------|
| `find_flaky_tests`         | Search a repo's flaky inventory. Filter by tags, recency, free-text. |
| `get_test_history`         | Recent disruption events for a specific test. |
| `list_recent_submissions`  | Recent CI runs for a repository. |
| `get_repo_flakiness`       | Current flakiness % (last 14 days). |
| `get_repo_coverage`        | Current coverage % (latest report). |

## Environment variables

| Variable           | Required | Default                              |
|--------------------|----------|--------------------------------------|
| `BUILDPULSE_TOKEN` | yes      | —                                    |
| `PLATFORM_API_URL` | no       | `https://platform.buildpulse.io`     |

## Troubleshooting

- **`authentication failed (401)`** — token is wrong, expired, or your
  org has no active plan. Confirm by running:
  ```bash
  curl -i -H "Authorization: token $BUILDPULSE_TOKEN" https://platform.buildpulse.io/api
  ```
  Expected: `HTTP/1.1 204 No Content`.

- **MCP client shows "server failed to start"** — open the integration's
  log panel. The stderr will tell you what's wrong (missing token,
  binary download failure, etc.).

- **Binary download failed during install** — set
  `BUILDPULSE_MCP_SKIP_INSTALL=1` and build from source:
  ```bash
  git clone https://github.com/BuildPulseLLC/buildpulse-mcp
  cd platform-api && go build -o /usr/local/bin/buildpulse-mcp ./cmd/mcp
  ```

## License

MIT. See [LICENSE](https://github.com/BuildPulseLLC/buildpulse-mcp/blob/main/LICENSE).
