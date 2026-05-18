# MCP Distribution Strategy

This document covers how the BuildPulse MCP server reaches customers
running Claude Desktop, Claude.ai (web), ChatGPT, Cursor, Cline,
Windsurf, Continue, and other MCP-aware agents.

There are two distribution surfaces: **local stdio** (the user runs
the binary on their own machine) and **remote HTTP** (BuildPulse hosts
the server and the agent connects over the network). Each LLM /
client has different support, so we ship both.

---

## TL;DR

| Channel | Reaches | Effort | Status |
|---------|---------|--------|--------|
| npm — `@buildpulse/mcp` | Claude Desktop, Cursor, Cline, Windsurf, Continue, ChatGPT (via MCP-enabled clients), VS Code, anything that spawns stdio | Low — release workflow already wired | ✅ shipped |
| MCP servers registry (`modelcontextprotocol/servers`) | Discovery — official MCP server list | One PR | 📋 todo |
| Smithery.ai | Discovery + one-command install for Claude / Cursor / Cline | Submit `smithery.yaml` | 📋 todo |
| Cursor MCP marketplace | Cursor users (in-app discovery) | Submit PR to `getcursor/mcp` | 📋 todo |
| **Hosted remote MCP** (Streamable HTTP, Bearer-token auth) | Claude.ai web + mobile, Claude Desktop integrations panel, ChatGPT Connectors | Deploy `cmd/mcp-remote` (built) via `.infra/mcp/` Terraform; CNAME `mcp.buildpulse.io` | 🚧 server + infra ready, awaiting deploy |
| Anthropic Connectors directory listing | Claude.ai discovery surface | Add OAuth flow to the hosted server, submit to Anthropic Connectors program | 📋 phase 2 |
| ChatGPT custom-connector listing | ChatGPT Plus / Team / Enterprise discovery surface | OpenAI Connector partner program submission | 📋 phase 2 |
| Smithery.ai registry | One-line install for any MCP client | `smithery.yaml` (shipped) + submit | 🚧 manifest ready |
| Homebrew tap | Mac power users who prefer `brew` to `npx` | One-time tap repo | 📋 nice-to-have |
| Pulse MCP / Composio / Glama | Aggregator discovery | Submit listing | 📋 nice-to-have |

The npm channel is the workhorse — every modern MCP-aware client today
knows how to invoke `npx`, and the same install works across all of
them. The remote/hosted path unlocks the Claude.ai web app and
ChatGPT, where users can't install anything locally.

---

## Phase 1 — Local stdio (shipped today)

### How users install

```bash
npx -y @buildpulse/mcp
```

Or globally:

```bash
npm install -g @buildpulse/mcp
```

The npm package contains a Node shim (`bin/buildpulse-mcp.js`) and a
postinstall script (`lib/install.js`) that downloads the
platform-correct Go binary from the GitHub release tagged
`mcp-v<version>`. Five binaries are published per release:
darwin-amd64, darwin-arm64, linux-amd64, linux-arm64, windows-amd64.

### Cutting a release

```bash
git tag mcp-v0.1.0
git push --tags
```

That triggers `.github/workflows/release-mcp.yml`, which:

1. Cross-compiles `cmd/mcp` for all five targets.
2. Creates a GitHub Release with gzipped binaries + sha256 sums.
3. Publishes `@buildpulse/mcp@0.1.0` to npm with [provenance attestations](https://docs.npmjs.com/generating-provenance-statements).

Customers who already have it installed get the new version on the
next `npx -y` (or via `npm update -g @buildpulse/mcp`).

Required GitHub secrets:

- `NPM_TOKEN` — npm publish token (Automation type) for the `@buildpulse` scope.

### Where it works (clients that spawn stdio)

| Client | Config file | Notes |
|--------|-------------|-------|
| **Claude Desktop** | `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS), `%APPDATA%\Claude\claude_desktop_config.json` (Windows) | Anthropic. Most-deployed MCP client. |
| **Cursor** | `.cursor/mcp.json` per-project or `~/.cursor/mcp.json` globally | The IDE most customers use. |
| **Cline** | VS Code extension settings → MCP servers | Open-source coding agent. |
| **Continue** | `~/.continue/config.json` → `experimental.modelContextProtocolServers` | |
| **Windsurf** | `~/.codeium/windsurf/mcp_config.json` | Codeium's IDE. |
| **Zed** | `~/.config/zed/settings.json` → `assistant.mcp_servers` | |
| **ChatGPT (MCP-enabled clients)** | Various — see the Apps/Connectors UI on the desktop app | OpenAI shipped MCP support in 2025; configuration is via their Connectors panel. |
| **VS Code + GitHub Copilot Chat** | Settings → MCP servers | |

The JSON snippet is identical across clients. The
[`@buildpulse/mcp` README](./mcp-npm/README.md) has copy-paste examples
for the main three (Claude Desktop, Cursor, Cline).

### Listing on registries

These are submit-once-then-forget channels that drive discovery:

1. **`github.com/modelcontextprotocol/servers`** — the canonical
   community list. Add a line under "Community Servers" with a link to
   our repo + npm package. PR template + reviewer process is in the
   repo's CONTRIBUTING.md.

2. **Smithery.ai** — `smithery.yaml` in the repo root tells Smithery
   how to run the server. Once listed, users get a one-line install
   like:
   ```bash
   npx -y @smithery/cli install @buildpulse/mcp --client claude
   ```
   …which writes the JSON config for them. Submission: add
   `smithery.yaml`, push, then PR to `smithery-ai/registry`.

3. **`getcursor/mcp`** — Cursor's curated MCP list, surfaced in the
   IDE's MCP picker UI. PR with our entry.

4. **Pulse MCP / Glama / Composio** — aggregator marketplaces with
   their own listings. Lower priority but easy submissions.

5. **MCP Hub / Awesome MCP lists** — community-curated awesome-lists.
   Drive-by GitHub PRs.

---

## Phase 2 — Hosted remote MCP (server built, deploy pending)

The local-stdio path can't reach **Claude.ai (web app)** or
**ChatGPT.com** — those run in the browser and can't spawn a binary.
The fix is a **remote MCP** served over Streamable HTTP at, e.g.,
`https://mcp.buildpulse.io`.

The server itself is built ([`cmd/mcp-remote`](./cmd/mcp-remote)), with
infra Terraform in [`.infra/mcp/`](./.infra/mcp/). What remains is the
deploy + OAuth.

### Architecture

```
Claude.ai (browser) / ChatGPT
       ↓ Streamable HTTP + Bearer <token>   (phase 1)
       ↓ Streamable HTTP + OAuth 2.1        (phase 2)
mcp.buildpulse.io  (cmd/mcp-remote on ECS Fargate)
       ↓ Authorization: token <token>
platform.buildpulse.io  (cmd/api)
```

The remote binary `cmd/mcp-remote`:

- Speaks the [Streamable HTTP MCP transport](https://modelcontextprotocol.io/specification/draft/basic/transports#streamable-http)
- Accepts per-session Bearer-token auth today (`Authorization: Bearer <token>` or the legacy `token <token>` scheme); the token can be either the current `bp_<64-hex>` API token shape or the legacy 40-hex shape
- Advertises `/.well-known/mcp` for client discovery
- Has `/.well-known/oauth-authorization-server` scaffolded but stubbed (returns 501 + a hint pointing at Bearer auth) until OAuth lands
- Forwards the caller's token to `platform-api` on every tool call — no new credential to provision

Deployment: ECS Fargate alongside `platform-api` on the existing
public ALB (host-based routing on `mcp.buildpulse.io`), region
`us-west-2`. Image built from [`cmd/mcp-remote/Dockerfile`](./cmd/mcp-remote/Dockerfile) (distroless,
nonroot). Terraform in [`.infra/mcp/`](./.infra/mcp/) mirrors the
`platform-api` infra.

### Where it unlocks distribution

| Channel | What it gets us |
|---------|-----------------|
| **Anthropic Connectors Catalog** | Submit `mcp.buildpulse.io` as a Connector. Users add BuildPulse from the Claude.ai Connectors panel in one click. Required for Claude.ai mobile + iPad. |
| **Claude Desktop integrations panel** | Same Connector listing renders in the desktop app's Integrations view; no JSON-editing required for non-technical users. |
| **OpenAI Connectors (ChatGPT)** | ChatGPT Plus / Team / Enterprise can add remote MCP connectors. Submit through OpenAI's connector partner program. |
| **Custom GPTs** | A custom "BuildPulse Test Analyst" GPT can be authored against the hosted MCP and published to the GPT store. |
| **Enterprise tenants** | Many customers can't `npx` arbitrary binaries from corporate-locked dev machines but can authorize SaaS connectors. |

### Auth model

Two options, both worth supporting:

1. **OAuth (recommended)** — user clicks "Connect BuildPulse" in
   Claude.ai, redirects to `app.buildpulse.io` to authorize, lands back
   with a session token. Best UX, mandatory for Anthropic's Connectors
   program.

2. **Direct token** — for power users / CI, accept a Bearer token in
   the `Authorization` header on the Streamable HTTP transport. The
   token is the same BuildPulse API token used by `platform-api`
   (either the current `bp_<64-hex>` shape or the legacy 40-hex
   shape). No new secret to manage; useful for self-hosted MCP
   clients.

### Submission to the Anthropic Connectors program

When the hosted server is ready:

1. Get on Anthropic's MCP partner waitlist (currently `https://www.anthropic.com/mcp-partners`).
2. Submit our manifest: server URL, OAuth metadata URL, tool inventory, branding.
3. Pass Anthropic's security/UX review.
4. Listed in the Connectors directory.

---

## Versioning

| Surface | Versioning |
|---------|-----------|
| `cmd/mcp` Go binary | `--version` flag prints `serverVersion` from `main.go`. |
| `@buildpulse/mcp` npm | Matches the GitHub release tag (e.g. `mcp-v0.1.0` ↔ `0.1.0` on npm). |
| `platform-api` HTTP | Unversioned and stable (drop-in replacement for the legacy Rails API); see `openapi.yaml`. |
| MCP protocol | `protocolVersion` is negotiated per session; we accept anything ≥ `2024-11-05`. |

If we ever break the MCP tool surface (rename a tool, change required
inputs), bump to the next minor; consumers pinning `npx -y @buildpulse/mcp`
get the new minor on the next install.

---

## Telemetry & metrics

For both phases, we want to know:

- Install / startup counts (per platform).
- Per-tool call volume + p50/p99 latency.
- Authentication failures (bad token vs. expired plan vs. revoked).

Phase 1 (local): emit minimal anonymous telemetry on startup if
`BUILDPULSE_MCP_TELEMETRY=1` is set (opt-in, since this is running on
the user's machine).

Phase 2 (remote): full telemetry by default — the server is ours, the
sessions are server-side. Existing CloudWatch + the same platform-api
log pipeline.
