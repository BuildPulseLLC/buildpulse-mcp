# Changelog

All notable changes to the BuildPulse MCP server. Versions are the
`mcp-vX.Y.Z` git tags; the npm package `@buildpulse/mcp` and the MCP
Registry entry carry the same number.

## Unreleased (`main` since `mcp-v0.1.6`)

- Hosted server serves a public landing page at `/`, plus `robots.txt`,
  `sitemap.xml`, and `llms.txt`; unknown paths still 404.
- Richer `get_repo_flakiness` / `get_repo_coverage` descriptions;
  `Annotations.Title` set on every tool.
- Per-token rate limit (120 tool calls / minute), `mcp_audit` log line
  per tool call, and RFC 7009 `POST /oauth/revoke` (#38).
- Argument validation (repo parts, ObjectIds, `limit` maximums) and
  egress pinning to the configured Platform API host (#37).
- `organization_id` is required for sessions with 2+ organizations
  instead of silently defaulting to the first one (#28).
- `find_flaky_tests` tracks platform-api's quarantine semantics (#24).
- `get_recent_failures` fans out per-submission fetches and memoizes the
  org roster (#33).
- OAuth `refresh_token` grant, Cognito-backed (#7, #8).
- Graceful handling of HTTP 402 `plan_limit_exceeded` as a friendly
  tool result (#6).
- `web_url` deep-links point at `buildpulse.io` (#9).
- Go toolchain tracked via `go-version-file`; dependency bumps for
  Inspector findings; `mcp-remote` image built on the current Go.

## mcp-v0.1.6 — 2026-06-06

Tagged and built; the GitHub Release carries all five binaries. The
npm publish step of the release workflow failed (`E404` from the
registry), so **`0.1.6` was never published to npm or the MCP
Registry** — both still serve `0.1.5`. Included:

- New tools: `list_repositories`, `get_submission_test_results` (#2,
  #3), `get_recent_failures` (#4).
- `find_flaky_tests` gained `pass_rate`, `avg_duration_ms` (#4) and
  `time_consumed` (#1).
- Accept both `bp_<64-hex>` and legacy 40-hex API tokens.
- Hosted server: stateless Streamable HTTP so multiple replicas are
  safe; DynamoDB-backed OAuth store; Cognito identity bridged to
  `mcpSessions`; RFC 9728 protected-resource metadata; RDS CA bundle
  in the image.
- Docs point token creation at `buildpulse.io` (#5).

## mcp-v0.1.5 — 2026-05-17

First version published to npm and listed on the MCP Registry as
`io.github.BuildPulseLLC/buildpulse-mcp`. (`0.1.2`–`0.1.4` were
same-day release attempts that failed on the registry publish step and
were left as draft releases.)

- OAuth 2.1 scaffold: PKCE, dynamic client registration, Cognito Hosted
  UI env wiring.
- `server.json` and `mcpName` for the MCP Registry; `PUBLISH.md`.
- Release workflow publishes to the registry via GitHub OIDC with npm
  provenance.

## mcp-v0.1.1 — 2026-05-17

Initial public repository, split out from `BuildPulseLLC/platform-api`.
Five tools (`list_my_organizations`, `find_flaky_tests`,
`get_test_history`, `list_recent_submissions`, `get_repo_flakiness`,
`get_repo_coverage`), four prompts, two resource templates, stdio and
Streamable HTTP transports.
