package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// overageAware wraps a tool handler so that a *PlanLimitError raised by the
// platform API client (HTTP 402 plan_limit_exceeded) is translated into a
// clean, SUCCESSFUL text result instead of a raw error or panic. Every tool
// is registered through this wrapper, so the over-limit behavior is defined
// in exactly one place and inherited uniformly — individual tools need no
// 402-specific code.
//
// Tools whose endpoints are never restricted by platform-api
// (account/organizations, billing, usage, health) simply never receive a
// 402, so wrapping them is a harmless no-op. Any non-402 error (and any 402
// that isn't the documented plan_limit_exceeded shape) is passed through
// unchanged, preserving existing behavior exactly.
func overageAware[In, Out any](h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		res, out, err := h(ctx, req, in)
		var pe *PlanLimitError
		if errors.As(err, &pe) {
			var zero Out
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: planLimitText(pe)}},
			}, zero, nil
		}
		return res, out, err
	}
}

// planLimitText renders the user-facing message shown when a BuildPulse
// organization is over its enforced monthly test-result limit. Wording
// follows the overage-enforcement spec: lead with platform-api's own
// explanation, reassure that ingestion continues uninterrupted, and point
// to the upgrade URL. Both the message and upgrade URL come from
// platform-api; sensible defaults cover the case where either is absent.
func planLimitText(e *PlanLimitError) string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = "Your BuildPulse organization is over its plan's monthly test-result limit."
	}
	var b strings.Builder
	b.WriteString(msg)
	b.WriteString("\n\nNothing has been lost: BuildPulse is still recording every test result for your organization. " +
		"Only read access to this data is paused until you upgrade.")
	if u := strings.TrimSpace(e.UpgradeURL); u != "" {
		b.WriteString("\n\nUpgrade your plan to restore access: ")
		b.WriteString(u)
	}
	return b.String()
}

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
		Name:        "list_my_organizations",
		Title:       "List my BuildPulse organizations",
		Description: "Return every BuildPulse organization the current MCP session can access. Multi-tenant users must call this first to discover the `id` (UUID) of the organization they want to scope subsequent tool calls to — pass that `id` as the `organization_id` argument on find_flaky_tests / get_test_history / list_recent_submissions / get_repo_flakiness / get_repo_coverage. Single-tenant callers will see exactly one entry and don't need to pass `organization_id`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, overageAware(listMyOrganizations(c)))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_repositories",
		Title:       "List repositories in a BuildPulse organization",
		Description: "Return every repository BuildPulse is monitoring for the given organization, sorted alphabetically. Call this whenever the user asks a repo-scoped question (\"do I have flaky tests?\", \"why is CI red?\") without naming a specific repo — the `name` field is what you pass to find_flaky_tests / list_recent_submissions / get_repo_flakiness / get_repo_coverage as their `repository` argument. For multi-tenant users, pass `organization_id` (call list_my_organizations first to enumerate); single-tenant tokens see exactly the bound org's repos.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, overageAware(listRepositories(c)))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "find_flaky_tests",
		Title:       "Find flaky tests",
		Description: "Search a repository's flaky / disruptive test inventory. Returns tests that have been intermittently failing in the last 14 days, sorted by disruptiveness (default) or recency. Filter by tags, free-text on test name / file / class, and a since-date. Use this as the entry point for any flaky-test investigation. For users in multiple organizations, pass `organization_id` (call list_my_organizations first to enumerate).",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: true,
			Title:        "Find flaky tests",
		},
	}, overageAware(findFlakyTests(c)))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_test_history",
		Title:       "Get test history",
		Description: "Return the most-recent disruption (failure / flake) events for a specific test, identified by its BuildPulse test_id. Up to 10 events from the last 14 days. Each event includes the CI build URL and commit SHA — useful for correlating regressions with code changes.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, overageAware(getTestHistory(c)))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_recent_submissions",
		Title:       "List recent CI runs",
		Description: "List the most-recent test submissions (CI runs) for a repository. Each entry corresponds to one CI run that uploaded test results to BuildPulse. Reach for this first when a user asks \"why is CI red?\" or \"what changed in the last hour\". Each entry includes an `id` you can pass to get_submission_test_results to drill into the individual test results for that run.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, overageAware(listRecentSubmissions(c)))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_submission_test_results",
		Title:       "Get per-test results for a submission",
		Description: "Return the individual test results recorded against one submission (one CI run). Use `status=\"failed\"` to filter to just the failures and errors — the typical \"red build\" set. Each result carries the test name / suite / file / class, the per-attempt duration in microseconds, the failure message, and the runner-recorded run count (1=first attempt, 2+=retries). Get a submission `id` from list_recent_submissions first.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, overageAware(getSubmissionTestResults(c)))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_recent_failures",
		Title:       "Get failures across recent CI runs",
		Description: "Return tests that failed in any of the most recent N submissions for a repository, aggregated by test identity (name+suite+file). For each test, returns failure_count (how many of the recent runs it failed in), most_recent_failure_at, most_recent_build_url, and the failure message from the most recent occurrence. Unlike find_flaky_tests, this is NOT filtered by the statistical flakiness threshold — it surfaces every test that failed in the recent window, which matches workflows where customers only upload to BuildPulse after their own CI has already passed (so any failure observed by BuildPulse is by definition unexpected). Default window is the last 10 submissions.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, overageAware(getRecentFailures(c)))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_repo_flakiness",
		Title:       "Get repo flakiness %",
		Description: "Return the current flakiness percentage for a repository over the last 14 days. Higher is worse. Use this for a quick health snapshot before drilling into individual tests with find_flaky_tests.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, overageAware(getRepoFlakiness(c)))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_repo_coverage",
		Title:       "Get repo coverage %",
		Description: "Return the current test coverage percentage for a repository (from the most-recent coverage report).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, overageAware(getRepoCoverage(c)))
}

// addOrgParam appends `organization_id=<uuid>` to the given query
// values iff the caller supplied one. The platform-api middleware
// silently ignores an empty value for single-org apiToken sessions
// but requires it for multi-org mcpSession callers, so it's safe to
// pass through verbatim.
func addOrgParam(params url.Values, orgID string) {
	if orgID = strings.TrimSpace(orgID); orgID != "" {
		params.Set("organization_id", orgID)
	}
}

// --- list_my_organizations --------------------------------------------------

type listOrgsInput struct{}

type orgOut struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
}

type listOrgsOutput struct {
	Organizations []orgOut `json:"organizations"`
}

func listMyOrganizations(c *Client) mcp.ToolHandlerFor[listOrgsInput, listOrgsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ listOrgsInput) (*mcp.CallToolResult, listOrgsOutput, error) {
		var resp struct {
			Organizations []orgOut `json:"organizations"`
		}
		if err := c.GetJSON(ctx, "/api/me/organizations", nil, &resp); err != nil {
			return nil, listOrgsOutput{}, err
		}
		if resp.Organizations == nil {
			resp.Organizations = []orgOut{}
		}
		return nil, listOrgsOutput{Organizations: resp.Organizations}, nil
	}
}

// --- list_repositories ------------------------------------------------------

type listReposInput struct {
	OrganizationID string `json:"organization_id,omitempty" jsonschema:"organization UUID (the id field from list_my_organizations). Required for multi-tenant users; ignored for single-org tokens."`
}

type repoOut struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
	WebURL      string `json:"web_url"`
}

type listReposOutput struct {
	Repositories []repoOut `json:"repositories"`
}

func listRepositories(c *Client) mcp.ToolHandlerFor[listReposInput, listReposOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listReposInput) (*mcp.CallToolResult, listReposOutput, error) {
		params := url.Values{}
		addOrgParam(params, in.OrganizationID)

		var resp struct {
			Repositories []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				FullName    string `json:"full_name"`
				URL         string `json:"url"`
				Description string `json:"description,omitempty"`
			} `json:"repositories"`
		}
		if err := c.GetJSON(ctx, "/api/me/repositories", params, &resp); err != nil {
			return nil, listReposOutput{}, err
		}

		out := listReposOutput{Repositories: make([]repoOut, 0, len(resp.Repositories))}
		for _, r := range resp.Repositories {
			out.Repositories = append(out.Repositories, repoOut{
				ID:          r.ID,
				Name:        r.Name,
				FullName:    r.FullName,
				URL:         r.URL,
				Description: r.Description,
				WebURL:      c.WebURL("/repos/" + url.PathEscape(r.Name)),
			})
		}
		return nil, out, nil
	}
}

// --- find_flaky_tests --------------------------------------------------------

type findFlakyInput struct {
	Repository         string   `json:"repository" jsonschema:"the repository name, case-insensitive (e.g. 'widgets')"`
	OrganizationID     string   `json:"organization_id,omitempty" jsonschema:"organization UUID (the id field from list_my_organizations). Required for multi-tenant users; ignored for single-org tokens."`
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
	TimeConsumed        *int64   `json:"time_consumed,omitempty" jsonschema:"total time spent on this test across recent runs, in microseconds — useful as a silent indicator of retries/slow specs"`
	PassRate            *float64 `json:"pass_rate,omitempty" jsonschema:"fraction of runs in which this test passed (0.0-1.0). Lower = worse. Sort by this to surface unreliable tests."`
	AvgDurationMs       *float64 `json:"avg_duration_ms,omitempty" jsonschema:"average per-attempt duration in milliseconds. Sort descending to find the slowest tests."`
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
		params.Set("include", "disruptiveness_ratio,nondeterministic_negative_result_count,nondeterminism_first_recorded_at,tags,time_consumed,pass_rate,avg_duration_ms")
		addOrgParam(params, in.OrganizationID)

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
				TimeConsumed        *int64   `json:"time_consumed,omitempty"`
				PassRate            *float64 `json:"pass_rate,omitempty"`
				AvgDurationMs       *float64 `json:"avg_duration_ms,omitempty"`
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
				TimeConsumed:        t.TimeConsumed,
				PassRate:            t.PassRate,
				AvgDurationMs:       t.AvgDurationMs,
				WebURL:              c.WebURL("/tests/" + url.PathEscape(t.ID)),
			})
		}
		return nil, out, nil
	}
}

// --- get_test_history --------------------------------------------------------

type testHistoryInput struct {
	TestID         string `json:"test_id" jsonschema:"the BuildPulse test (disruptor) ID — 24-char hex (from find_flaky_tests output)"`
	OrganizationID string `json:"organization_id,omitempty" jsonschema:"organization UUID (the id field from list_my_organizations). Required for multi-tenant users; ignored for single-org tokens."`
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
		params := url.Values{}
		addOrgParam(params, in.OrganizationID)
		if err := c.GetJSON(ctx, "/api/tests/"+url.PathEscape(in.TestID)+"/results", params, &r); err != nil {
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
	Owner          string `json:"owner" jsonschema:"the repository owner, case-insensitive (e.g. 'acme')"`
	Name           string `json:"name" jsonschema:"the repository name, case-insensitive (e.g. 'widgets')"`
	OrganizationID string `json:"organization_id,omitempty" jsonschema:"organization UUID (the id field from list_my_organizations). Required for multi-tenant users; ignored for single-org tokens."`
	Limit          int    `json:"limit,omitempty" jsonschema:"page size, 1-100 (default 10)"`
}

type submission struct {
	ID              string `json:"id" jsonschema:"submission identifier (24-char hex). Pass as submission_id to get_submission_test_results to fetch per-test results for this CI run."`
	Key             string `json:"key,omitempty"`
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
		addOrgParam(params, in.OrganizationID)

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

// --- get_submission_test_results --------------------------------------------

type submissionTestResultsInput struct {
	Owner          string `json:"owner" jsonschema:"the repository owner, case-insensitive (e.g. 'acme')"`
	Name           string `json:"name" jsonschema:"the repository name, case-insensitive (e.g. 'widgets')"`
	SubmissionID   string `json:"submission_id" jsonschema:"submission identifier (24-char hex). Get this from list_recent_submissions."`
	Status         string `json:"status,omitempty" jsonschema:"optional outcome filter: 'failed' (matches failure + error — typical red build), 'failure', 'error', 'skipped', 'passed', or 'all' (default)"`
	OrganizationID string `json:"organization_id,omitempty" jsonschema:"organization UUID (the id field from list_my_organizations). Required for multi-tenant users; ignored for single-org tokens."`
	Limit          int    `json:"limit,omitempty" jsonschema:"page size, 1-100 (default 25)"`
}

type submissionTestResultOut struct {
	ID         string  `json:"id"`
	TestCaseID string  `json:"test_case_id"`
	Name       string  `json:"name"`
	Suite      string  `json:"suite,omitempty"`
	Class      string  `json:"class,omitempty"`
	File       string  `json:"file,omitempty"`
	Conclusion string  `json:"conclusion" jsonschema:"passing | failure | error | skipped | unknown"`
	DurationUS int64   `json:"duration_us" jsonschema:"per-attempt duration in microseconds"`
	Message    *string `json:"message,omitempty" jsonschema:"one-line failure summary; for jest-junit runners this is synthesized from the first line of body"`
	Body       *string `json:"body,omitempty" jsonschema:"full failure detail — assertion message + stack trace — truncated at 2000 bytes (UTF-8 safe). Surface this verbatim to the user when they ask why a test failed; do not paraphrase the assertion text."`
	RanAt      string  `json:"ran_at"`
	RunCount   int     `json:"run_count" jsonschema:"retry attempt number (1=first attempt, 2+=retries)"`
}

type submissionTestResultsOutput struct {
	Owner        string                    `json:"owner"`
	Name         string                    `json:"name"`
	SubmissionID string                    `json:"submission_id"`
	Status       string                    `json:"status,omitempty"`
	Count        int64                     `json:"count"`
	Tests        []submissionTestResultOut `json:"tests"`
	NextCursor   *string                   `json:"next_cursor,omitempty"`
	WebURL       string                    `json:"web_url"`
}

func getSubmissionTestResults(c *Client) mcp.ToolHandlerFor[submissionTestResultsInput, submissionTestResultsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in submissionTestResultsInput) (*mcp.CallToolResult, submissionTestResultsOutput, error) {
		if strings.TrimSpace(in.Owner) == "" || strings.TrimSpace(in.Name) == "" {
			return nil, submissionTestResultsOutput{}, fmt.Errorf("owner and name are required")
		}
		subID := strings.TrimSpace(in.SubmissionID)
		if len(subID) != 24 {
			return nil, submissionTestResultsOutput{}, fmt.Errorf("submission_id must be a 24-char hex string, got %d chars", len(subID))
		}

		params := url.Values{}
		if in.Status != "" {
			params.Set("status", in.Status)
		}
		if in.Limit > 0 && in.Limit <= 100 {
			params.Set("limit", strconv.Itoa(in.Limit))
		}
		addOrgParam(params, in.OrganizationID)

		type resp struct {
			Count    int64                     `json:"count"`
			Tests    []submissionTestResultOut `json:"tests"`
			Metadata struct {
				After *string `json:"after"`
				Limit int     `json:"limit"`
			} `json:"metadata"`
		}
		var r resp
		path := fmt.Sprintf("/api/repos/%s/%s/submissions/%s/tests",
			url.PathEscape(in.Owner), url.PathEscape(in.Name), url.PathEscape(subID))
		if err := c.GetJSON(ctx, path, params, &r); err != nil {
			return nil, submissionTestResultsOutput{}, err
		}

		return nil, submissionTestResultsOutput{
			Owner:        in.Owner,
			Name:         in.Name,
			SubmissionID: subID,
			Status:       in.Status,
			Count:        r.Count,
			Tests:        r.Tests,
			NextCursor:   r.Metadata.After,
			WebURL: c.WebURL("/repos/" + url.PathEscape(in.Owner) + "/" +
				url.PathEscape(in.Name) + "/builds/" + url.PathEscape(subID)),
		}, nil
	}
}

// --- get_recent_failures ----------------------------------------------------

type recentFailuresInput struct {
	Owner          string `json:"owner" jsonschema:"the repository owner, case-insensitive (e.g. 'acme')"`
	Name           string `json:"name" jsonschema:"the repository name, case-insensitive (e.g. 'widgets')"`
	OrganizationID string `json:"organization_id,omitempty" jsonschema:"organization UUID (the id field from list_my_organizations). Required for multi-tenant users; ignored for single-org tokens."`
	Submissions    int    `json:"submissions,omitempty" jsonschema:"how many recent submissions to look back across, 1-50 (default 10)"`
}

type recentFailureOut struct {
	TestCaseID            string  `json:"test_case_id"`
	Name                  string  `json:"name"`
	Suite                 string  `json:"suite,omitempty"`
	Class                 string  `json:"class,omitempty"`
	File                  string  `json:"file,omitempty"`
	FailureCount          int     `json:"failure_count" jsonschema:"how many of the inspected submissions this test failed in"`
	MostRecentRanAt       string  `json:"most_recent_ran_at"`
	MostRecentBuildURL    string  `json:"most_recent_build_url,omitempty"`
	MostRecentMessage     *string `json:"most_recent_message,omitempty"`
	MostRecentBody        *string `json:"most_recent_body,omitempty"`
	MostRecentDurationUS  int64   `json:"most_recent_duration_us"`
}

type recentFailuresOutput struct {
	Owner               string             `json:"owner"`
	Name                string             `json:"name"`
	SubmissionsInspected int               `json:"submissions_inspected"`
	UniqueTestsFailed   int                `json:"unique_tests_failed"`
	Failures            []recentFailureOut `json:"failures"`
	WebURL              string             `json:"web_url"`
}

func getRecentFailures(c *Client) mcp.ToolHandlerFor[recentFailuresInput, recentFailuresOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in recentFailuresInput) (*mcp.CallToolResult, recentFailuresOutput, error) {
		if strings.TrimSpace(in.Owner) == "" || strings.TrimSpace(in.Name) == "" {
			return nil, recentFailuresOutput{}, fmt.Errorf("owner and name are required")
		}
		limit := 10
		if in.Submissions > 0 && in.Submissions <= 50 {
			limit = in.Submissions
		}

		// Step 1: list recent submissions to get the IDs to drill into.
		subParams := url.Values{}
		subParams.Set("limit", strconv.Itoa(limit))
		addOrgParam(subParams, in.OrganizationID)

		type submissionLite struct {
			ID       string `json:"id"`
			BuildURL string `json:"build_url"`
		}
		type subsResp struct {
			Submissions []submissionLite `json:"submissions"`
		}
		var sr subsResp
		subPath := fmt.Sprintf("/api/repos/%s/%s/submissions",
			url.PathEscape(in.Owner), url.PathEscape(in.Name))
		if err := c.GetJSON(ctx, subPath, subParams, &sr); err != nil {
			return nil, recentFailuresOutput{}, err
		}

		// Step 2: for each submission, fetch failed tests and aggregate.
		// Key by testCaseId so retries within one submission and the
		// same logical test across multiple submissions collapse.
		type testKey = string
		agg := map[testKey]*recentFailureOut{}

		type failedTest struct {
			TestCaseID string  `json:"test_case_id"`
			Name       string  `json:"name"`
			Suite      string  `json:"suite,omitempty"`
			Class      string  `json:"class,omitempty"`
			File       string  `json:"file,omitempty"`
			DurationUS int64   `json:"duration_us"`
			Message    *string `json:"message,omitempty"`
			Body       *string `json:"body,omitempty"`
			RanAt      string  `json:"ran_at"`
		}
		type stResp struct {
			Tests []failedTest `json:"tests"`
		}

		seenInThisSubmission := map[string]map[testKey]bool{}

		for _, s := range sr.Submissions {
			seenInThisSubmission[s.ID] = map[testKey]bool{}
			stParams := url.Values{}
			stParams.Set("status", "failed")
			stParams.Set("limit", "100")
			addOrgParam(stParams, in.OrganizationID)

			var r stResp
			stPath := fmt.Sprintf("/api/repos/%s/%s/submissions/%s/tests",
				url.PathEscape(in.Owner), url.PathEscape(in.Name), url.PathEscape(s.ID))
			if err := c.GetJSON(ctx, stPath, stParams, &r); err != nil {
				// best-effort: skip submissions that error so a flaky
				// one doesn't sink the whole response
				continue
			}

			for _, t := range r.Tests {
				if t.TestCaseID == "" {
					continue
				}
				// One submission, even with retries on the same test,
				// counts as a single failure for the per-submission tally.
				if seenInThisSubmission[s.ID][t.TestCaseID] {
					continue
				}
				seenInThisSubmission[s.ID][t.TestCaseID] = true

				row, ok := agg[t.TestCaseID]
				if !ok {
					row = &recentFailureOut{
						TestCaseID:           t.TestCaseID,
						Name:                 t.Name,
						Suite:                t.Suite,
						Class:                t.Class,
						File:                 t.File,
						MostRecentRanAt:      t.RanAt,
						MostRecentBuildURL:   s.BuildURL,
						MostRecentMessage:    t.Message,
						MostRecentBody:       t.Body,
						MostRecentDurationUS: t.DurationUS,
					}
					agg[t.TestCaseID] = row
				}
				row.FailureCount++
				// Submissions are returned newest-first, so the first
				// time we see a testCaseId is the most recent occurrence;
				// don't overwrite the "most_recent_*" fields after that.
			}
		}

		// Materialize, sort by failure_count desc then name asc.
		failures := make([]recentFailureOut, 0, len(agg))
		for _, row := range agg {
			failures = append(failures, *row)
		}
		// Insertion-sort-ish: simple stable sort using the stdlib.
		sortRecentFailures(failures)

		return nil, recentFailuresOutput{
			Owner:                in.Owner,
			Name:                 in.Name,
			SubmissionsInspected: len(sr.Submissions),
			UniqueTestsFailed:    len(failures),
			Failures:             failures,
			WebURL: c.WebURL("/repos/" + url.PathEscape(in.Owner) +
				"/" + url.PathEscape(in.Name) + "/builds"),
		}, nil
	}
}

// sortRecentFailures orders failures by FailureCount desc, then Name asc.
// Kept as a named helper so the slot is obvious if we ever swap in a
// different ranking.
func sortRecentFailures(rows []recentFailureOut) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].FailureCount != rows[j].FailureCount {
			return rows[i].FailureCount > rows[j].FailureCount
		}
		return rows[i].Name < rows[j].Name
	})
}

// --- get_repo_flakiness ------------------------------------------------------

type repoMetricInput struct {
	Repository     string `json:"repository" jsonschema:"the repository name (case-insensitive)"`
	OrganizationID string `json:"organization_id,omitempty" jsonschema:"organization UUID (the id field from list_my_organizations). Required for multi-tenant users; ignored for single-org tokens."`
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
		addOrgParam(params, in.OrganizationID)
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
		addOrgParam(params, in.OrganizationID)
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
