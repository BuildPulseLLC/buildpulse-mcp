// Package mcpserver contains the BuildPulse MCP server implementation
// shared by the stdio (cmd/mcp) and remote / Streamable HTTP
// (cmd/mcp-remote) transports.
//
// The transports differ in how they're wired up — stdio reads
// BUILDPULSE_TOKEN from the process env at startup, the remote server
// extracts a per-session token from the inbound HTTP Authorization
// header — but the tool, prompt, and resource surface is identical.
// Keeping the surface in one place is what makes it possible for the
// stdio binary and the hosted server to evolve together without
// drifting.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// Version is the MCP server's semver. It is overridden at build time
// by the release workflow via `-ldflags "-X .../mcpserver.Version=..."`
// — note it MUST be a `var`, not a `const`, for the linker to overwrite.
// Defaults to "0.0.0-dev" for local `go run`.
var Version = "0.0.0-dev"

const (
	// DefaultPlatformURL is the production base URL.
	DefaultPlatformURL = "https://platform.buildpulse.io"
)

// UserAgent identifies the MCP server to the platform API in access
// logs. Stamped on every outbound request. Function (not constant) so
// it picks up the runtime-overridden Version.
func userAgent() string { return "buildpulse-mcp/" + Version }

// Client is a thin HTTP client over the BuildPulse Platform API,
// scoped to a single API token. Safe for concurrent use.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient builds a Client.
//
// `baseURL` may be empty, in which case DefaultPlatformURL is used.
// Any trailing slash on baseURL is trimmed.
func NewClient(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultPlatformURL
	}
	for len(baseURL) > 0 && baseURL[len(baseURL)-1] == '/' {
		baseURL = baseURL[:len(baseURL)-1]
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// BaseURL returns the configured platform API base URL.
func (c *Client) BaseURL() string { return c.baseURL }

// WebURL returns a best-effort link into the BuildPulse web UI for a
// repository. We map platform.* → app.* — both share the same root
// domain. Used by tool outputs to give the model a deep-link to send
// back to the user.
func (c *Client) WebURL(path string) string {
	web := c.baseURL
	// Replace the first "platform." with "app." — most environments
	// (production, dev) follow this naming.
	for i := 0; i < len(web)-len("platform."); i++ {
		if web[i:i+len("platform.")] == "platform." {
			web = web[:i] + "app." + web[i+len("platform."):]
			break
		}
	}
	return web + path
}

// Get performs an authenticated GET against the platform API. The
// path must start with "/api". Returns the body bytes and Content-Type.
func (c *Client) Get(ctx context.Context, path string, params url.Values) ([]byte, string, error) {
	full := c.baseURL + path
	if len(params) > 0 {
		full += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", full, nil)
	if err != nil {
		return nil, "", err
	}
	// Use the legacy `token <hex>` form — both forms work, but `token`
	// is what existing customer scripts use, so platform-api access
	// logs stay consistent.
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("platform API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read response: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, "", fmt.Errorf("authentication failed (401) — token is missing, invalid, or your BuildPulse organization has no active plan")
	case http.StatusNotFound:
		return nil, "", fmt.Errorf("not found (404) — the repository or test does not exist, or is not in your organization")
	}
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("platform API returned %d: %s", resp.StatusCode, truncate(body, 200))
	}

	return body, resp.Header.Get("Content-Type"), nil
}

// GetJSON performs Get and unmarshals into `out`.
func (c *Client) GetJSON(ctx context.Context, path string, params url.Values, out any) error {
	body, _, err := c.Get(ctx, path, params)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	return nil
}

// PercentFromBadgeSVG extracts the "12.3" percentage from a flakiness
// or coverage badge SVG. Returns -1 if the value can't be parsed
// (callers should treat that as "no data").
//
// The platform API's badge SVG embeds the percentage as a plain text
// node: `<text x="180" y="14" font-size="12">12.3%</text>`. Parsing
// the SVG is brittle in general, but the template is owned by the same
// repo as this MCP — see internal/handler/badges.go badgeSVGTemplate.
// Drift surfaces immediately in the unit tests below.
var badgePctRe = regexp.MustCompile(`>([0-9]+(?:\.[0-9]+)?)%<`)

func PercentFromBadgeSVG(svg []byte) float64 {
	m := badgePctRe.FindSubmatch(svg)
	if m == nil {
		return -1
	}
	v, err := strconv.ParseFloat(string(m[1]), 64)
	if err != nil {
		return -1
	}
	return v
}

// FlakinessColor maps a flakiness percentage to a human-readable color
// label, matching the badge color thresholds in
// internal/handler/badges.go.
func FlakinessColor(pct float64) string {
	switch {
	case pct < 0:
		return "unknown"
	case pct == 0:
		return "green"
	case pct <= 20:
		return "yellow"
	default:
		return "red"
	}
}

// CoverageColor mirrors badges.go coverage thresholds.
func CoverageColor(pct float64) string {
	switch {
	case pct < 0:
		return "unknown"
	case pct == 100:
		return "green"
	case pct >= 90:
		return "light_green"
	case pct >= 70:
		return "yellow"
	default:
		return "red"
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
