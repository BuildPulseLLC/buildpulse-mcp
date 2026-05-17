# Publishing BuildPulse MCP to community registries

The MCP code, npm package, and hosted server are shipped. This doc
covers the remaining community-listing work that needs human-loop
authentication.

## 1. Official MCP Registry (registry.modelcontextprotocol.io)

The canonical "app store for MCP servers." Listing here drives
discovery in every MCP client that integrates the official registry.

Prereqs already done in this repo:
- `server.json` at the repo root (schema 2025-12-11).
- `mcpName: "io.github.buildpulsellc/buildpulse-mcp"` field in
  `npm/package.json` — proves we own the namespace via npm ownership.

Steps for the human publisher:

```bash
# Install the official CLI.
brew install mcp-publisher       # or download from releases:
                                  # https://github.com/modelcontextprotocol/registry/releases

# Authenticate with GitHub (device flow — opens browser).
mcp-publisher login github

# Publish.
cd /path/to/buildpulse-mcp
mcp-publisher publish
```

The publisher verifies that the GitHub identity authorizing matches
the namespace in `server.json` (i.e. `BuildPulseLLC` org membership)
and that the npm package's `mcpName` field matches. No GitHub
Actions OIDC path is exposed yet, so this step is interactive
one-time per release.

When v1.0 of the registry ships, expect this to become automatable
via GitHub Actions; for now keep `server.json` updated and re-publish
manually on each release.

## 2. punkpeye/awesome-mcp-servers (community curated list)

This is the most popular community list and powers
[glama.ai/mcp/servers](https://glama.ai/mcp/servers).

Steps:

```bash
gh repo fork punkpeye/awesome-mcp-servers --clone
cd awesome-mcp-servers
# Find the section "## Developer Tools" or similar and add a line
# alphabetically; the format is:
#   - `[BuildPulse](https://github.com/BuildPulseLLC/buildpulse-mcp)` 🎖️ - Surface flaky tests, CI run history, and coverage from BuildPulse.
git checkout -b add-buildpulse
git commit -am "Add BuildPulse MCP"
gh pr create --base main --fill
```

## 3. Smithery.ai

Smithery indexes any public repo that contains a `smithery.yaml` —
which we have. To get listed in their searchable directory:

1. Visit <https://smithery.ai/new>
2. Sign in with GitHub
3. Authorize Smithery to read the BuildPulseLLC org
4. Select `buildpulse-mcp` from the dropdown
5. Smithery auto-detects the manifest and creates the listing

Once listed, users get:

```bash
npx -y @smithery/cli install @buildpulse/mcp --client claude
```

## 4. Cursor MCP marketplace

Cursor curates an in-app MCP picker. Submission is via the
`getcursor/mcp` repo or via direct outreach to the Cursor team.

```bash
gh repo fork getcursor/mcp --clone
cd mcp
# Edit the listing JSON / README per the contributing guide
gh pr create --base main --fill
```

Or submit via the in-app form at <https://cursor.sh/mcp>.

## 5. Anthropic Connectors directory

For Claude.ai web (browser) and Claude Desktop's Connectors panel.
Requires the hosted MCP at `mcp.buildpulse.io` to be running OAuth
2.1 (already scaffolded — see `cmd/mcp-remote/oauth.go`) and to pass
Anthropic's review.

1. Join the partner waitlist:
   <https://www.anthropic.com/mcp-partners>
2. Submit manifest:
   - `mcp_url`: `https://mcp.buildpulse.io/mcp`
   - `oauth_metadata_url`: `https://mcp.buildpulse.io/.well-known/oauth-authorization-server`
   - tool list (we have 5; see `internal/mcpserver/tools.go`)
   - branding assets (icon, color palette)
3. Pass Anthropic's UX + security review.
4. Listed in the Connectors directory.

## 6. OpenAI / ChatGPT Connectors

Same hosted endpoint, OpenAI's partner program:
<https://platform.openai.com/docs/guides/connectors>

OpenAI accepts MCP-protocol connectors via their connector partner
listing. Process is similar to Anthropic's — submit URL + OAuth
metadata, pass review.
