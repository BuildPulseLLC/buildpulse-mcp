package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerResources exposes server-side resources. Resources are
// addressable read-only blobs the model can pull into context — think
// of them as durable handles to BuildPulse state.
//
// We currently expose one resource template:
//   buildpulse://repos/{repo}/flaky-tests
//
// Clients can read it directly to seed a session with the current
// flaky inventory, without an extra tool round trip.
func registerResources(s *mcp.Server, c *Client) {
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "Flaky tests by repo",
		Title:       "Flaky tests for a repository",
		Description: "The current flaky-test inventory for a single repository (last 14 days). Read directly to seed an investigation without a tool call.",
		URITemplate: "buildpulse://repos/{repo}/flaky-tests",
		MIMEType:    "application/json",
	}, flakyTestsResource(c))

	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "Recent CI runs",
		Title:       "Recent CI submissions for a repository",
		Description: "The most-recent test submissions (CI runs) for a repository. Read directly to seed a CI-status investigation.",
		URITemplate: "buildpulse://repos/{owner}/{name}/submissions",
		MIMEType:    "application/json",
	}, submissionsResource(c))
}

func flakyTestsResource(c *Client) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		// Parse buildpulse://repos/{repo}/flaky-tests
		u, err := url.Parse(req.Params.URI)
		if err != nil {
			return nil, fmt.Errorf("invalid URI: %w", err)
		}
		// Path: /{repo}/flaky-tests (host is "repos" — url.Parse puts the
		// part after the scheme into Host for opaque schemes; the
		// MCP spec just gives us a URI string back, so we tolerate both.)
		var repo string
		if u.Path != "" {
			parts := splitNonEmpty(u.Path, '/')
			if len(parts) >= 1 {
				repo = parts[0]
			}
		}
		if repo == "" {
			return nil, fmt.Errorf("expected URI of the form buildpulse://repos/{repo}/flaky-tests")
		}

		params := url.Values{}
		params.Set("repository", repo)
		params.Set("include", "disruptiveness_ratio,tags,nondeterminism_first_recorded_at")
		params.Set("limit", "50")
		// This resource is "the repo's flaky tests", i.e. the ACTIVE ones.
		// /api/v1/flaky/tests defaults to quarantined-only (legacy Rails
		// parity), so the param must be explicit — omitting it would silently
		// turn this resource into a quarantine roster.
		params.Set("quarantine", "false")
		body, _, err := c.Get(ctx, "/api/v1/flaky/tests", params)
		if err != nil {
			return nil, err
		}

		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{
					URI:      req.Params.URI,
					MIMEType: "application/json",
					Text:     string(body),
				},
			},
		}, nil
	}
}

func submissionsResource(c *Client) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		u, err := url.Parse(req.Params.URI)
		if err != nil {
			return nil, fmt.Errorf("invalid URI: %w", err)
		}
		parts := splitNonEmpty(u.Path, '/')
		if len(parts) < 2 {
			return nil, fmt.Errorf("expected URI of the form buildpulse://repos/{owner}/{name}/submissions")
		}
		owner, name := parts[0], parts[1]

		params := url.Values{}
		params.Set("limit", "25")
		body, _, err := c.Get(ctx, "/api/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/submissions", params)
		if err != nil {
			return nil, err
		}
		// Round-trip through json.RawMessage to ensure we return a single
		// canonical JSON blob even if the upstream changes representation.
		var raw json.RawMessage = body
		canonical, _ := json.Marshal(raw)
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{
					URI:      req.Params.URI,
					MIMEType: "application/json",
					Text:     string(canonical),
				},
			},
		}, nil
	}
}

func splitNonEmpty(s string, sep byte) []string {
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == sep {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}
