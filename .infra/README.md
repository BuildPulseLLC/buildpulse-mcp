# `.infra/mcp/` — Hosted MCP service

Terraform module for the hosted BuildPulse MCP server at
`mcp.buildpulse.io`. Mirrors the structure of `.infra/` for
`platform-api`, with a few deltas:

| Aspect | platform-api | mcp-remote |
|--------|--------------|------------|
| Domain | `platform.buildpulse.io` | `mcp.buildpulse.io` |
| Binary | `cmd/api` | `cmd/mcp-remote` |
| Env  | `MONGODB_URI` | `PLATFORM_API_URL` (no DB access) |
| Auth | Per-request `apiTokens` lookup | Per-request token forwarded to `platform-api` |
| Image | `${ecr.platform_api}` | `${ecr.mcp_remote}` (NEW — see prerequisites) |

## Prerequisites (one-time, in `environment/`)

Before this module can be `terraform apply`'d, the shared
`environment/` repo must:

1. **Add an ECR repository** named `mcp-remote` and surface it as
   `outputs.ecr.mcp_remote`. Pattern: copy the existing
   `outputs.ecr.platform_api`.
2. **Register the DNS records** `mcp.buildpulse.io` and
   `mcp.dev.buildpulse.io` pointing at the same public ALB (host-based
   routing). Same Cloudflare CNAME pattern as `platform.*`.
3. **(Phase 2)** Provision a customer-facing Cognito user pool or
   OAuth authorization server for the Anthropic Connectors flow. Not
   needed for the Bearer-token path that's shipping first.

## Build + deploy

The CI workflow at `.github/workflows/build-and-push.yml` already
builds `cmd/api` to ECR. Add a sibling job that:

```yaml
- name: docker build mcp-remote
  run: |
    docker build -f cmd/mcp-remote/Dockerfile -t $ECR/mcp-remote:$SHA .
    docker push $ECR/mcp-remote:$SHA

- name: terraform apply mcp
  working-directory: .infra/mcp
  run: |
    terraform init -backend-config=backend.tf
    terraform apply -auto-approve -var "version_tag=$SHA"
```

## Region

`us-west-2`. (Same as everything else — never default to `us-east-1`.)
