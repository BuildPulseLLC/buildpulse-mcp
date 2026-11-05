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
		Name:        "estimate_runner_savings",
		Title:       "Estimate BuildPulse runner savings",
		Description: "Work out what BuildPulse runners would save (or are saving) this organization. If runners are not activated yet, estimate CI minutes from recent runs and price them at BuildPulse versus GitHub list rate, then point at the setup page. If they are, summarise the last 30 days of usage, cost and savings.",
		Arguments: []*mcp.PromptArgument{
			{Name: "organization_id", Description: "Organization UUID from list_my_organizations (required for multi-org sessions)", Required: false},
			{Name: "repository", Description: "Optional: a repository name to sample CI runs from when estimating; otherwise the busiest repositories are used", Required: false},
		},
	}, estimateRunnerSavingsPrompt)

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

func estimateRunnerSavingsPrompt(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	orgID := req.Params.Arguments["organization_id"]
	repo := req.Params.Arguments["repository"]
	orgArg := ""
	if orgID != "" {
		orgArg = " with `organization_id=\"" + orgID + "\"`"
	}
	repoStep := "call `list_repositories`" + orgArg + " and pick the two or three repositories that look busiest (most recent activity)"
	if repo != "" {
		repoStep = "use the repository **" + repo + "**"
	}
	return &mcp.GetPromptResult{
		Description: "Estimate BuildPulse runner savings",
		Messages: []*mcp.PromptMessage{
			{
				Role: "user",
				Content: &mcp.TextContent{
					Text: "" +
						"Estimate what BuildPulse runners save this organization. Every number must come from a tool response; do not quote a price you were not given.\n\n" +
						"1. Call `get_runner_status`" + orgArg + ".\n\n" +
						"2. If `has_tenant` is true: call `get_runner_usage`" + orgArg + " with `days=30`. Report, from `totals`: jobs, billable_minutes, cost_usd, free_minutes_used and free_minutes_value_usd, net_cost_usd, github_list_price_usd and savings_percent, and p50/p90 job duration. " +
						"Then list the top entries of `breakdown` (runner size, provider, minutes, net cost, savings). Mention that `pricing.free_minutes_per_month` free minutes renew every month. Link `web_url`. Stop here.\n\n" +
						"3. If `has_tenant` is false, the organization is not on BuildPulse runners yet. Build an estimate:\n" +
						"   a. Call `get_runner_usage`" + orgArg + " anyway: its `pricing` block carries the published offer. Use `pricing.rate_card` for the per-minute prices (`buildpulse_usd_per_minute` and `github_usd_per_minute` for the matching size), `pricing.free_minutes_per_month` for the monthly grant and `pricing.speedup_vs_hosted` for the speed-up.\n" +
						"   b. If the user already knows their monthly CI minutes (the GitHub Actions usage page shows it), use that number and skip c.\n" +
						"   c. Otherwise estimate it: " + repoStep + ", call `list_recent_submissions` with `limit=50` for each to count how many CI runs land per day (use `recorded_at`), and call `get_submission_test_results` on two or three recent runs to add up `duration_us` per run as the test time of one job. Monthly minutes = runs per day x 30 x minutes per run. State plainly that this is a lower bound: it counts test time only, not checkout, install or build steps, and only runs that upload to BuildPulse.\n" +
						"   d. Assume the x64 2 vCPU size unless the user says otherwise. GitHub cost = minutes x `github_usd_per_minute`. BuildPulse cost = max(0, minutes / `speedup_vs_hosted` - `free_minutes_per_month`) x `buildpulse_usd_per_minute`: the same work finishes in fewer minutes and the first free minutes are not billed. Also show the figure without the speed-up so the saving from price alone is visible.\n" +
						"   e. Present both costs, the monthly and yearly saving, the assumptions in one short list, and finish with the activation link from `setup_url` (https://buildpulse.io/organizations/{orgId}/runners/setup): it is a one-line change to the workflow, 3,000 free minutes a month, no card required.",
				},
			},
		},
	}, nil
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
