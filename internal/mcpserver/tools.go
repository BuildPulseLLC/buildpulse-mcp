package mcpserver

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerTools wires every BuildPulse MCP tool onto the supplied
// server. Tools are intent-shaped, not 1:1 mappings of HTTP endpoints:
// an agent looking for flaky tests should reach for `find_flaky_tests`,
// not have to choose between /api/repos/{}/tests and /api/v1/flaky/tests.
//
// Every tool output that names an entity includes a `web_url` field
// so the model can deep-link the user back into the BuildPulse web UI —
// matching the polish of Sentry's `sentry-issue-id` links and
// Atlassian's deep-linked Jira tool responses.
func registerTools(s *mcp.Server, c *Client) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "find_flaky_tests",
		Title:       "Find flaky tests",
		Description: "Search a repository's flaky / disruptive test inventory. Returns tests that have been intermittently failing in the last 14 days, sorted by disruptiveness (default) or recency. Filter by tags, free-text on test name / file / class, and a since-date. Use this as the entry point for any flaky-test investigation.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
			Title:        "Find flaky tests",
		},
	}, findFlakyTests(c))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_test_history",
		Title:       "Get test history",
		Description: "Return the most-recent disruption (failure / flake) events for a specific test, identified by its BuildPulse test_id. Up to 10 events from the last 14 days. Each event includes the CI build URL and commit SHA — useful for correlating regressions with code changes.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, getTestHistory(c))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_recent_submissions",
		Title:       "List recent CI runs",
		Description: "List the most-recent test submissions (CI runs) for a repository. Each entry corresponds to one CI run that uploaded test results to BuildPulse. Reach for this first when a user asks \"why is CI red?\" or \"what changed in the last hour\".",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, listRecentSubmissions(c))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_repo_flakiness",
		Title:       "Get repo flakiness %",
		Description: "Return the current flakiness percentage for a repository over the last 14 days. Higher is worse. Use this for a quick health snapshot before drilling into individual tests with find_flaky_tests.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, getRepoFlakiness(c))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_repo_coverage",
		Title:       "Get repo coverage %",
		Description: "Return the current test coverage percentage for a repository (from the most-recent coverage report).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, getRepoCoverage(c))
}

// --- find_flaky_tests --------------------------------------------------------

type findFlakyInput struct {
	Repository         string   `json:"repository" jsonschema:"the repository name, case-insensitive (e.g. 'widgets')"`
	Tags               []string `json:"tags,omitempty" jsonschema:"optional list of tags — matches any (OR)"`
	Search             string   `json:"search,omitempty" jsonschema:"optional free-text match on test name, suite, file, or classname"`
	SinceDate          string   `json:"since,omitempty" jsonschema:"only return tests last seen on or after this YYYY-MM-DD"`
	Sort               string   `json:"sort,omitempty" jsonschema:"'disruptivenessRatio' (default) or 'recency'"`
	IncludeQuarantined bool     `json:"include_quarantined,omitempty" jsonschema:"include tests under quarantine (default false)"`
	Limit              int      `json:"limit,omitempty" jsonschema:"page size, 1-100 (default 25)"`
}

type flakyTestOut struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Suite               string   `json:"suite,omitempty"`
	Class               string   `json:"class,omitempty"`
	File                string   `json:"file,omitempty"`
	DisruptorType       string   `json:"disruptor_type,omitempty"`
	DisruptivenessRatio *float64 `json:"disruptiveness_ratio,omitempty"`
	DisruptionCount     *int     `json:"disruption_count,omitempty"`
	FirstDisruptionAt   *string  `json:"first_disruption_at,omitempty"`
	Tags                []string `json:"tags,omitempty"`
	WebURL              string   `json:"web_url"`
}

type findFlakyOutput struct {
	Repository string         `json:"repository"`
	Count      int64          `json:"count"`
	Tests      []flakyTestOut `json:"tests"`
	WebURL     string         `json:"web_url"`
}

func findFlakyTests(c *Client) mcp.ToolHandlerFor[findFlakyInput, findFlakyOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in findFlakyInput) (*mcp.CallToolResult, findFlakyOutput, error) {
		if strings.TrimSpace(in.Repository) == "" {
			return nil, findFlakyOutput{}, fmt.Errorf("repository is required")
		}

		params := url.Values{}
		params.Set("repository", in.Repository)
		params.Set("include", "disruptiveness_ratio,nondeterministic_negative_result_count,nondeterminism_first_recorded_at,tags")

		if in.Limit > 0 && in.Limit <= 100 {
			params.Set("limit", strconv.Itoa(in.Limit))
		}
		if in.Sort == "recency" {
			params.Set("sort", "recency")
		}
		if in.IncludeQuarantined {
			params.Set("quarantine", "true")
		}

		var qParts []string
		if in.SinceDate != "" {
			if _, err := time.Parse("2006-01-02", in.SinceDate); err != nil {
				return nil, findFlakyOutput{}, fmt.Errorf("since must be YYYY-MM-DD, got %q", in.SinceDate)
			}
			qParts = append(qParts, "last-seen:>="+in.SinceDate)
		}
		if len(in.Tags) > 0 {
			qParts = append(qParts, "tags:"+strings.Join(in.Tags, ","))
		}
		if in.Search != "" {
			qParts = append(qParts, in.Search)
		}
		if len(qParts) > 0 {
			params.Set("q", strings.Join(qParts, " "))
		}

		type platformFlakyResp struct {
			Count int64 `json:"count"`
			Tests []struct {
				ID                  string   `json:"id"`
				Name                string   `json:"name"`
				Suite               string   `json:"suite"`
				Class               string   `json:"class"`
				File                string   `json:"file"`
				DisruptorType       string   `json:"disruptor_type"`
				DisruptivenessRatio *float64 `json:"disruptiveness_ratio,omitempty"`
				DisruptionCount     *int     `json:"disruption_count,omitempty"`
				FirstDisruptionAt   *string  `json:"first_disruption_at,omitempty"`
				Tags                []string `json:"tags,omitempty"`
			} `json:"tests"`
		}
		var resp platformFlakyResp
		if err := c.GetJSON(ctx, "/api/v1/flaky/tests", params, &resp); err != nil {
			return nil, findFlakyOutput{}, err
		}

		out := findFlakyOutput{
			Repository: in.Repository,
			Count:      resp.Count,
			Tests:      make([]flakyTestOut, 0, len(resp.Tests)),
			WebURL:     c.WebURL("/flaky-tests?repository=" + url.QueryEscape(in.Repository)),
		}
		for _, t := range resp.Tests {
			out.Tests = append(out.Tests, flakyTestOut{
				ID:                  t.ID,
				Name:                t.Name,
				Suite:               t.Suite,
				Class:               t.Class,
				File:                t.File,
				DisruptorType:       t.DisruptorType,
				DisruptivenessRatio: t.DisruptivenessRatio,
				DisruptionCount:     t.DisruptionCount,
				FirstDisruptionAt:   t.FirstDisruptionAt,
				Tags:                t.Tags,
				WebURL:              c.WebURL("/tests/" + url.PathEscape(t.ID)),
			})
		}
		return nil, out, nil
	}
}

// --- get_test_history --------------------------------------------------------

type testHistoryInput struct {
	TestID string `json:"test_id" jsonschema:"the BuildPulse test (disruptor) ID — 24-char hex (from find_flaky_tests output)"`
}

type disruptionEvent struct {
	ID         string  `json:"id"`
	BuildURL   string  `json:"build_url"`
	CommitOID  string  `json:"commit_oid"`
	Conclusion string  `json:"conclusion"`
	RecordedAt string  `json:"recorded_at"`
	Message    *string `json:"message,omitempty"`
}

type testHistoryOutput struct {
	TestID string            `json:"test_id"`
	Count  int               `json:"count"`
	Events []disruptionEvent `json:"events"`
	WebURL string            `json:"web_url"`
}

func getTestHistory(c *Client) mcp.ToolHandlerFor[testHistoryInput, testHistoryOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in testHistoryInput) (*mcp.CallToolResult, testHistoryOutput, error) {
		if strings.TrimSpace(in.TestID) == "" {
			return nil, testHistoryOutput{}, fmt.Errorf("test_id is required")
		}
		if len(in.TestID) != 24 {
			return nil, testHistoryOutput{}, fmt.Errorf("test_id must be a 24-char hex string, got %d chars", len(in.TestID))
		}

		type resp struct {
			Results []disruptionEvent `json:"results"`
		}
		var r resp
		if err := c.GetJSON(ctx, "/api/tests/"+url.PathEscape(in.TestID)+"/results", nil, &r); err != nil {
			return nil, testHistoryOutput{}, err
		}
		return nil, testHistoryOutput{
			TestID: in.TestID,
			Count:  len(r.Results),
			Events: r.Results,
			WebURL: c.WebURL("/tests/" + url.PathEscape(in.TestID)),
		}, nil
	}
}

// --- list_recent_submissions -------------------------------------------------

type submissionsInput struct {
	Owner string `json:"owner" jsonschema:"the repository owner, case-insensitive (e.g. 'acme')"`
	Name  string `json:"name" jsonschema:"the repository name, case-insensitive (e.g. 'widgets')"`
	Limit int    `json:"limit,omitempty" jsonschema:"page size, 1-100 (default 10)"`
}

type submission struct {
	Key             string `json:"key"`
	BuildURL        string `json:"build_url"`
	CommitOID       string `json:"commit_oid"`
	RecordedAt      string `json:"recorded_at"`
	Status          string `json:"status"`
	TestResultCount int    `json:"test_result_count"`
}

type submissionsOutput struct {
	Owner       string       `json:"owner"`
	Name        string       `json:"name"`
	Count       int64        `json:"count"`
	Submissions []submission `json:"submissions"`
	NextCursor  *string      `json:"next_cursor,omitempty"`
	WebURL      string       `json:"web_url"`
}

func listRecentSubmissions(c *Client) mcp.ToolHandlerFor[submissionsInput, submissionsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in submissionsInput) (*mcp.CallToolResult, submissionsOutput, error) {
		if strings.TrimSpace(in.Owner) == "" || strings.TrimSpace(in.Name) == "" {
			return nil, submissionsOutput{}, fmt.Errorf("owner and name are required")
		}

		params := url.Values{}
		if in.Limit > 0 && in.Limit <= 100 {
			params.Set("limit", strconv.Itoa(in.Limit))
		}

		type resp struct {
			Count       int64        `json:"count"`
			Submissions []submission `json:"submissions"`
			Metadata    struct {
				After *string `json:"after"`
				Limit int     `json:"limit"`
			} `json:"metadata"`
		}
		var r resp
		path := fmt.Sprintf("/api/repos/%s/%s/submissions", url.PathEscape(in.Owner), url.PathEscape(in.Name))
		if err := c.GetJSON(ctx, path, params, &r); err != nil {
			return nil, submissionsOutput{}, err
		}

		return nil, submissionsOutput{
			Owner:       in.Owner,
			Name:        in.Name,
			Count:       r.Count,
			Submissions: r.Submissions,
			NextCursor:  r.Metadata.After,
			WebURL:      c.WebURL("/repos/" + url.PathEscape(in.Owner) + "/" + url.PathEscape(in.Name)),
		}, nil
	}
}

// --- get_repo_flakiness ------------------------------------------------------

type repoMetricInput struct {
	Repository string `json:"repository" jsonschema:"the repository name (case-insensitive)"`
}

type flakinessOutput struct {
	Repository string  `json:"repository"`
	Percentage float64 `json:"percentage" jsonschema:"flakiness percentage over the last 14 days; -1 if no data"`
	Color      string  `json:"color" jsonschema:"green | yellow | red, matching the badge color thresholds"`
	BadgeURL   string  `json:"badge_url"`
	WebURL     string  `json:"web_url"`
}

func getRepoFlakiness(c *Client) mcp.ToolHandlerFor[repoMetricInput, flakinessOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in repoMetricInput) (*mcp.CallToolResult, flakinessOutput, error) {
		if strings.TrimSpace(in.Repository) == "" {
			return nil, flakinessOutput{}, fmt.Errorf("repository is required")
		}

		params := url.Values{}
		params.Set("repository", in.Repository)
		body, _, err := c.Get(ctx, "/api/v1/flaky/badges", params)
		if err != nil {
			return nil, flakinessOutput{}, err
		}
		pct := PercentFromBadgeSVG(body)
		return nil, flakinessOutput{
			Repository: in.Repository,
			Percentage: pct,
			Color:      FlakinessColor(pct),
			BadgeURL:   c.BaseURL() + "/api/v1/flaky/badges?repository=" + url.QueryEscape(in.Repository),
			WebURL:     c.WebURL("/flaky-tests?repository=" + url.QueryEscape(in.Repository)),
		}, nil
	}
}

// --- get_repo_coverage -------------------------------------------------------

type coverageOutput struct {
	Repository string  `json:"repository"`
	Percentage float64 `json:"percentage" jsonschema:"coverage percentage from the latest report; -1 if no report"`
	Color      string  `json:"color" jsonschema:"green | light_green | yellow | red"`
	BadgeURL   string  `json:"badge_url"`
	WebURL     string  `json:"web_url"`
}

func getRepoCoverage(c *Client) mcp.ToolHandlerFor[repoMetricInput, coverageOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in repoMetricInput) (*mcp.CallToolResult, coverageOutput, error) {
		if strings.TrimSpace(in.Repository) == "" {
			return nil, coverageOutput{}, fmt.Errorf("repository is required")
		}

		params := url.Values{}
		params.Set("repository", in.Repository)
		body, _, err := c.Get(ctx, "/api/v1/coverage/badges", params)
		if err != nil {
			return nil, coverageOutput{}, err
		}
		pct := PercentFromBadgeSVG(body)
		return nil, coverageOutput{
			Repository: in.Repository,
			Percentage: pct,
			Color:      CoverageColor(pct),
			BadgeURL:   c.BaseURL() + "/api/v1/coverage/badges?repository=" + url.QueryEscape(in.Repository),
			WebURL:     c.WebURL("/coverage?repository=" + url.QueryEscape(in.Repository)),
		}, nil
	}
}
