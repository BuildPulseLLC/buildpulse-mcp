# Contributing

Thanks for helping improve the BuildPulse MCP server. Issues and pull
requests are welcome at
<https://github.com/BuildPulseLLC/buildpulse-mcp>.

## Development

Requires Go 1.26+ (see `go.mod`).

```bash
git clone https://github.com/BuildPulseLLC/buildpulse-mcp
cd buildpulse-mcp
go build ./...
go vet ./...
go test -race ./...
```

Run the stdio server against production with a real token:

```bash
BUILDPULSE_TOKEN=bp_... go run ./cmd/mcp
```

Run the hosted (Streamable HTTP) server locally. DocumentDB, DynamoDB,
and KMS are all optional at startup — without them OAuth falls back to
an in-memory store and Bearer-token auth on `/mcp` works as normal:

```bash
PORT=8080 go run ./cmd/mcp-remote
curl -s http://localhost:8080/health
curl -s http://localhost:8080/          # landing page
```

## Adding or changing a tool

All tools, prompts, and resources are registered in
`internal/mcpserver/` and are shared by both transports. When you add
a tool:

1. Register it in `tools.go` with a `Title`, a `Description` that says
   when an agent should reach for it, `Annotations.ReadOnlyHint: true`,
   and `Annotations.Title` (a test enforces these).
2. Give repo-scoped tools the `organization_id` argument and resolve it
   with `resolveOrgID` — multi-org sessions must never guess a tenant.
3. Update the tool lists in `README.md`, `npm/README.md`,
   `cmd/mcp-remote/public/index.html`, `cmd/mcp-remote/public/llms.txt`,
   `smithery.yaml`, and `SECURITY.md`. `cmd/mcp-remote/public_test.go`
   and `internal/mcpserver/annotations_test.go` fail if the counts drift.
4. Add an entry under "Unreleased" in `CHANGELOG.md`.

This server ships no write tools. If you want to add one, open an
issue first — the threat model in `SECURITY.md` assumes a read-only
surface.

## Pull requests

- Branch from `main`. Branches named `feat/**` and `fix/**` build and
  deploy to the development environment on push; `main` deploys to
  production.
- Keep `gofmt` clean and `go test -race ./...` green.
- Dependabot opens weekly grouped dependency PRs.

## Releasing

Releases are tag-driven and cut by maintainers:

```bash
git tag mcp-vX.Y.Z
git push --tags
```

`.github/workflows/release.yml` cross-compiles `cmd/mcp` for five
targets, creates the GitHub Release, publishes `@buildpulse/mcp` to npm
with provenance, and publishes `server.json` to the MCP Registry. Bump
`version` in `server.json` (and its `packages[0].version`) in the same
change. See `DISTRIBUTION.md` for the full channel list.

## Security

Report vulnerabilities to <security@buildpulse.io>, not in a public
issue. The threat model is in `SECURITY.md`.
