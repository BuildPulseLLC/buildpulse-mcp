# Security — BuildPulse MCP

This is the threat model for [DEV-86](https://buildpulse.atlassian.net/browse/DEV-86)
(P0: [DEV-182](https://buildpulse.atlassian.net/browse/DEV-182)). It describes
what the server actually is, what we defend, and what we will not do because it
would break real users.

## What this MCP is

A **read-only, domain-scoped proxy** to the BuildPulse Platform API. Nine
tools (`list_my_organizations`, `list_repositories`, `find_flaky_tests`,
`get_test_history`, `list_recent_submissions`, `get_submission_test_results`,
`get_recent_failures`, `get_repo_flakiness`, `get_repo_coverage`) plus two
resource templates. There is no fetch-URL, shell, filesystem, or write tool.

Two transports share that surface:

| Transport | Binary | Credential | Who it talks to |
|-----------|--------|------------|-----------------|
| stdio | `cmd/mcp` (`npx @buildpulse/mcp`) | `BUILDPULSE_TOKEN` on the user's machine | `PLATFORM_API_URL` (default production) |
| Streamable HTTP | `cmd/mcp-remote` (`mcp.buildpulse.io`) | Bearer API token or OAuth `mcpSession` | Host-pinned Platform API only |

**platform-api is the authorization authority.** MCP forwards the session
token; platform-api resolves it to an organization (or a membership set) and
scopes every analytics query. MCP must not invent a second policy that
contradicts that, but it *does* validate arguments and pin egress so a
compromised agent cannot aim HTTP somewhere else.

## Trust boundaries

1. **The model is untrusted.** Tool schemas are advertised on purpose (MCP).
   Sequential "recon" (`list_my_organizations` → `list_repositories` →
   `find_flaky_tests`) is the supported product workflow, not an anomaly.
2. **A valid token is the blast radius.** Prompt injection can make the agent
   exfiltrate *that tenant's* data through chat or another client tool. We
   cannot stop a user-authorized read of the user's own org. We **can** stop
   that session from reading another customer's org.
3. **stdio vs hosted.** stdio runs as the user; a stolen laptop token is an
   org-admin problem, same as a stolen API token in CI. Hosted MCP adds
   OAuth, short-lived `mcpSessions`, and a public HTTP endpoint — pin egress
   there so the task cannot be turned into an open proxy.

## Attacks we take seriously

- **Cross-tenant `organization_id`.** An mcpSession lists every org the user
  belongs to. A prompt-injected agent might pass a *foreign* UUID. platform-api
  must 401 unless that UUID is in `mcpSessions.organizationIds`. Single-org
  API tokens must 401 on a mismatched `organization_id` rather than ignore it.
  Locked by tests in platform-api (`apiTokenOrgMatches`,
  `mcpSessionTargetOrg`) and by UUID validation at the MCP edge.
- **Path / host tricks in tool args.** `owner`, `name`, `repository`, and IDs
  are interpolated into URLs. Reject `..`, `/`, `@`, and non-hex ObjectIds so
  arguments cannot change host or escape `/api`.
- **Oversized windows.** `limit` / `submissions` above the documented max are
  errors, not silent clamps — agents get a retryable message; we do not open
  a bulk-dump path by accident.
- **Off-host HTTP.** `Client.Get` only requests `/api` on the configured base
  URL and refuses redirects to another host. `mcp-remote` will not start
  unless `PLATFORM_API_URL` is `https://platform.buildpulse.io` or
  `https://platform.dev.buildpulse.io` (Terraform enforces the same).

Fargate does not expose EC2 IMDS (`169.254.169.254`) the way EC2 does; the
application pin is still the control that matters if `PLATFORM_API_URL` were
ever mis-set.

## What we will not do (on purpose)

These ideas show up in generic "secure your MCP" write-ups. They assume
write/fetch/code-exec tools we do not ship, and they would kneecap triage:

- **Human-in-the-loop on read tools.** Confirming every `find_flaky_tests`
  call breaks Cursor/Claude. HITL belongs on a future `buildpulse.write`
  scope only.
- **Hiding tool lists or blocking multi-step sessions.** That *is* how
  customers use the product.
- **Session-kill on "intent change."** Power users chain many reads after a
  quiet start. Prefer generous rate limits (P1, implemented: 120 tool calls
  per token per minute, retryable tool result, session stays up) over killing
  the session.

## P1 controls (DEV-189)

- **Per-token rate limits.** `internal/mcpserver/policy.go`: 120 tool calls
  per hashed token per rolling minute. Excess calls return a retryable tool
  error (`IsError`), not a transport drop and not a revoked session.
- **Audit log.** Each tool invocation logs
  `mcp_audit tool="…" org="…" status="ok|error|rate_limited"`. Org is the
  `organization_id` argument when present (`-` otherwise). The raw token is
  never logged (a SHA-256 prefix is used only as the rate-limit key).
- **OAuth revoke.** Hosted `POST /oauth/revoke` (RFC 7009). Access tokens are
  deleted from `mcpSessions` (platform-api then 401s). Refresh tokens are
  popped from the OAuth store. Discovery advertises `revocation_endpoint`.
  Public clients; unknown tokens still return 200.

No HITL on read tools.
- **Aggressive output redaction.** Failure messages and stack traces are the
  product. Secret-shaped scrubbing is P2 and must be proven against real
  fixtures first.
- **A generic egress proxy / DNS-rebinding stack.** There is no user-supplied
  URL fetch. Do not add one.

## How to verify

```bash
go test ./...
```

stdio still accepts httptest / custom `PLATFORM_API_URL` for local work.
Hosted `mcp-remote` does not.
