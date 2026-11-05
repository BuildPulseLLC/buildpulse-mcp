package mcpserver

import "github.com/modelcontextprotocol/go-sdk/mcp"

// ServerImplementation is the MCP `Implementation` block advertised to
// clients on initialize. Same metadata regardless of transport so the
// stdio binary and the hosted server feel like one product to the
// model.
var ServerImplementation = &mcp.Implementation{
	Name:    "buildpulse",
	Title:   "BuildPulse",
	Version: Version,
}

// New constructs a fully-registered MCP server bound to the supplied
// platform API client. Used by both cmd/mcp (stdio) and
// cmd/mcp-remote (Streamable HTTP).
//
// Adding a new tool, prompt, or resource means editing one of the
// register* functions here — both transports pick it up for free.
func New(client *Client) *mcp.Server {
	s := mcp.NewServer(ServerImplementation, &mcp.ServerOptions{
		Instructions: "" +
			"BuildPulse provides faster, cheaper CI runners for GitHub Actions, CircleCI and Semaphore, with Test Intelligence built in: " +
			"flaky-test detection, coverage and CI metrics. Runners finish jobs about 2x faster at half the cost of the provider's hosted runners; " +
			"every organization gets 3,000 free minutes a month with no card required, and Test Intelligence is included with every runner minute " +
			"(it also works on any CI without switching runners). Use these tools to read an organization's runner usage, cost and savings, " +
			"to find out whether it is on BuildPulse runners yet, and to investigate flaky tests, CI run history, failures and coverage for a repository.\n\n" +
			"Common workflows:\n" +
			"- \"Are we on BuildPulse runners?\" / \"What do our runners cost?\" / \"What did we save?\"  -> get_runner_status, then get_runner_usage (days=30 by default). Report net_cost_usd, free_minutes_used, savings_percent and github_list_price_usd from the response; never invent a price.\n" +
			"- An organization with has_tenant=false has no runners yet. Relay how_to_start and point the user at the setup page: https://buildpulse.io/organizations/{orgId}/runners/setup (the orgId is the `id` from list_my_organizations). The estimate_runner_savings prompt walks through estimating their bill from recent CI activity.\n" +
			"- \"Which runner label do I use?\" / moving a workflow onto BuildPulse  -> get_runner_configs; the `label` is the runs-on value (or the CircleCI resource class / Semaphore agent type in provider_config).\n" +
			"- If the user asks a repo-scoped question without naming a repo, call list_repositories first (passing organization_id for multi-tenant users) and use the returned `name` values as the `repository` argument on later tools. Do NOT ask the user to type a repo name.\n" +
			"- \"Why is my CI red?\"  -> list_recent_submissions, then get_test_history on any failing test.\n" +
			"- \"Triage flaky tests.\"  -> find_flaky_tests (sort=recency), then for each interesting test, get_test_history.\n" +
			"- \"Repo health snapshot.\"  -> get_repo_flakiness and get_repo_coverage.\n\n" +
			"Organizations: multi-tenant users (list_my_organizations returns 2+ orgs) MUST pass organization_id (the org's `id` UUID) on EVERY org- or repo-scoped call. The org is NOT auto-selected: if you omit organization_id when the session has 2+ orgs, the call returns an error listing the accessible orgs and their UUIDs. Read it, pick the right org, and retry with organization_id set. Single-tenant users (exactly one org) can omit organization_id; it auto-defaults to their one org.\n\n" +
			"Every tool output that names a test, repository or runner page includes a `web_url` field. Surface it back to the user as a clickable link. " +
			"If a call fails with plan_required, the organization's plan does not include API access: a paid Test Intelligence plan or a paid runner subscription unlocks it (https://buildpulse.io/pricing).",
	})

	registerRunnerTools(s, client)
	registerTools(s, client)
	registerPrompts(s)
	registerResources(s, client)

	return s
}
