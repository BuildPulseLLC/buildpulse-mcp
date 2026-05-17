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
			"BuildPulse is a CI test-analytics platform. Use these tools to " +
			"investigate flaky tests, inspect CI run history, and surface " +
			"flakiness or coverage health for a repository.\n\n" +
			"Common workflows:\n" +
			"- \"Why is my CI red?\"  → list_recent_submissions, then get_test_history on any failing test.\n" +
			"- \"Triage flaky tests.\"  → find_flaky_tests (sort=recency), then for each interesting test, get_test_history.\n" +
			"- \"Repo health snapshot.\"  → get_repo_flakiness and get_repo_coverage.\n\n" +
			"Every tool output that names a test or repository includes a " +
			"`web_url` field — surface it back to the user as a clickable link.",
	})

	registerTools(s, client)
	registerPrompts(s)
	registerResources(s, client)

	return s
}
