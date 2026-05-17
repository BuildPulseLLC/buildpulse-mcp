package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPrompts exposes guided workflows that surface in MCP clients
// as slash-pickable prompts. Where tools are atomic capabilities,
// prompts are reusable starting points — "triage flaky tests for repo
// X" is one click, then the model orchestrates the right tools.
//
// Modeled on the Sentry MCP's prompt pattern (where issue triage and
// release diagnosis are first-class prompts) and Atlassian's Jira
// prompts.
func registerPrompts(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "triage_flaky_tests",
		Title:       "Triage flaky tests",
		Description: "Walk through the flakiest tests in a repository and propose next actions (quarantine, owner assignment, root-cause hypotheses).",
		Arguments: []*mcp.PromptArgument{
			{Name: "repository", Description: "Repository name", Required: true},
		},
	}, triageFlakyTestsPrompt)

	s.AddPrompt(&mcp.Prompt{
		Name:        "ci_health_check",
		Title:       "CI health snapshot",
		Description: "One-shot health snapshot for a repository: flakiness %, coverage %, and the top three flaky tests of the week.",
		Arguments: []*mcp.PromptArgument{
			{Name: "repository", Description: "Repository name", Required: true},
		},
	}, ciHealthCheckPrompt)

	s.AddPrompt(&mcp.Prompt{
		Name:        "explain_test_failure",
		Title:       "Explain a test failure",
		Description: "Given a specific BuildPulse test_id, fetch its recent disruption history and propose a hypothesis for the root cause.",
		Arguments: []*mcp.PromptArgument{
			{Name: "test_id", Description: "The BuildPulse test ID (24-char hex) from find_flaky_tests", Required: true},
		},
	}, explainTestFailurePrompt)

	s.AddPrompt(&mcp.Prompt{
		Name:        "whats_red",
		Title:       "What broke my CI?",
		Description: "Given an owner + repo, list the most-recent CI runs and identify what's currently red.",
		Arguments: []*mcp.PromptArgument{
			{Name: "owner", Description: "Repository owner", Required: true},
			{Name: "name", Description: "Repository name", Required: true},
		},
	}, whatsRedPrompt)
}

func triageFlakyTestsPrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	repo := req.Params.Arguments["repository"]
	if repo == "" {
		return nil, fmt.Errorf("repository argument is required")
	}
	return &mcp.GetPromptResult{
		Description: "Triage flaky tests for " + repo,
		Messages: []*mcp.PromptMessage{
			{
				Role: "user",
				Content: &mcp.TextContent{
					Text: "" +
						"Triage flaky tests for the BuildPulse repository **" + repo + "**.\n\n" +
						"1. Call `find_flaky_tests` with `repository=\"" + repo + "\"`, " +
						"`sort=\"recency\"`, `limit=10`. Include `tags` and " +
						"`disruptiveness_ratio` in the response.\n" +
						"2. For the top three tests by disruptiveness ratio, call " +
						"`get_test_history` to inspect recent failures.\n" +
						"3. Summarize each with: name, file, disruptiveness, recent " +
						"failure message, and a suggested next action " +
						"(quarantine / assign / investigate). Include the `web_url` " +
						"as a clickable link for each test.\n" +
						"4. End with a one-line health verdict for the repository.",
				},
			},
		},
	}, nil
}

func ciHealthCheckPrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	repo := req.Params.Arguments["repository"]
	if repo == "" {
		return nil, fmt.Errorf("repository argument is required")
	}
	return &mcp.GetPromptResult{
		Description: "Health snapshot for " + repo,
		Messages: []*mcp.PromptMessage{
			{
				Role: "user",
				Content: &mcp.TextContent{
					Text: "" +
						"Produce a one-screen CI health snapshot for the BuildPulse " +
						"repository **" + repo + "**.\n\n" +
						"1. Call `get_repo_flakiness` and `get_repo_coverage`.\n" +
						"2. Call `find_flaky_tests` with `limit=3`, " +
						"`sort=\"disruptivenessRatio\"`.\n" +
						"3. Output: \n" +
						"   - Flakiness: X%  (color)\n" +
						"   - Coverage:  Y%  (color)\n" +
						"   - Top three flaky tests, each with disruptiveness and a `web_url` link.\n" +
						"   - One-sentence overall verdict (\"healthy\" / \"watch list\" / \"action needed\").",
				},
			},
		},
	}, nil
}

func explainTestFailurePrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	testID := req.Params.Arguments["test_id"]
	if testID == "" {
		return nil, fmt.Errorf("test_id argument is required")
	}
	return &mcp.GetPromptResult{
		Description: "Explain test failure for " + testID,
		Messages: []*mcp.PromptMessage{
			{
				Role: "user",
				Content: &mcp.TextContent{
					Text: "" +
						"Investigate BuildPulse test `" + testID + "`.\n\n" +
						"1. Call `get_test_history` with `test_id=\"" + testID + "\"`.\n" +
						"2. Group the disruption events by `conclusion` and by similarity " +
						"of `message`. Highlight any recurring failure patterns.\n" +
						"3. Cross-reference the `build_url` and `commit_oid` for the most " +
						"recent failure to identify what changed.\n" +
						"4. Propose one or two root-cause hypotheses (timing, environment, " +
						"flaky dependency, etc.) and a concrete next step.",
				},
			},
		},
	}, nil
}

func whatsRedPrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	owner := req.Params.Arguments["owner"]
	name := req.Params.Arguments["name"]
	if owner == "" || name == "" {
		return nil, fmt.Errorf("owner and name are required")
	}
	return &mcp.GetPromptResult{
		Description: "What broke " + owner + "/" + name,
		Messages: []*mcp.PromptMessage{
			{
				Role: "user",
				Content: &mcp.TextContent{
					Text: "" +
						"Identify what's currently red in " + owner + "/" + name + " on BuildPulse.\n\n" +
						"1. Call `list_recent_submissions` with `owner=\"" + owner + "\"`, " +
						"`name=\"" + name + "\"`, `limit=10`.\n" +
						"2. For each submission with a non-success status, surface the " +
						"`build_url`, `recorded_at`, and `test_result_count`.\n" +
						"3. If the recent runs are mostly green, call `find_flaky_tests` to " +
						"check whether intermittent flakes are masking real failures.\n" +
						"4. Conclude with a one-line statement: \"CI is green\" / " +
						"\"CI is red on N of last M runs\" / \"CI is mostly green but " +
						"flaky tests on the watch list\".",
				},
			},
		},
	}, nil
}
