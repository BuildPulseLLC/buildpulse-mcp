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
	"strings"
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

	// planLimitCode is the stable machine-readable code platform-api sends
	// in the body of an HTTP 402 response when an organization is over its
	// enforced monthly test-result limit and READ access to data is being
	// restricted. The 402 contract is owned by the platform-api enforcement
	// PR — `{"code":"plan_limit_exceeded","message":...,"upgradeUrl":...}` —
	// so this constant must not change without coordinating there.
	planLimitCode = "plan_limit_exceeded"
)

// PlanLimitError is returned by the client when a data-read endpoint
// responds with HTTP 402 and the documented plan_limit_exceeded body. It
// signals that the caller's BuildPulse organization is over its enforced
// monthly test-result limit, so platform-api is restricting READ access to
// data. Ingestion is never affected — every test result is still recorded.
//
// MCP does NOT compute entitlement itself: the decision to enforce is made
// entirely upstream in platform-api. This error is purely a typed carrier
// for the 402 so that tool handlers can translate it into a friendly,
// non-error tool result (see overageAware in tools.go) instead of leaking a
// raw HTTP 402 or a stack trace to the model.
type PlanLimitError struct {
	// Message is the human-readable explanation supplied by platform-api.
	Message string
	// UpgradeURL is a deep link to upgrade the plan, supplied by platform-api.
	UpgradeURL string
}

func (e *PlanLimitError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "plan limit exceeded"
}

// parsePlanLimit interprets the body of an HTTP 402 response. It returns a
// non-nil *PlanLimitError only when the body is the documented
// plan_limit_exceeded JSON; any other 402 body (or unparseable body) yields
// nil so the caller falls back to ordinary error handling.
func parsePlanLimit(body []byte) *PlanLimitError {
	var b struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		UpgradeURL string `json:"upgradeUrl"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return nil
	}
	if b.Code != planLimitCode {
		return nil
	}
	return &PlanLimitError{Message: b.Message, UpgradeURL: b.UpgradeURL}
}

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
// given path. It derives the web host from the platform API host by
// dropping the leading "platform." label, so it tracks whatever
// PLATFORM_API_URL points at:
//
//	https://platform.buildpulse.io      -> https://buildpulse.io
//	https://platform.dev.buildpulse.io  -> https://dev.buildpulse.io
//
// Previously this mapped platform.* -> app.*, but app.buildpulse.io is
// the legacy Rails app being retired — it never served these new-app
// routes (/repos/<uuid>, /flaky-tests, ...). The new web app is served
// at the apex (prod) / dev host, which is also the app2.* deprecation
// target. Used by tool outputs to give the model a deep-link to send
// back to the user.
func (c *Client) WebURL(path string) string {
	// Strip the "platform." label immediately after the scheme so a
	// "platform." appearing elsewhere is left alone. No match (a custom
	// host) falls through unchanged — still best-effort.
	web := strings.Replace(c.baseURL, "://platform.", "://", 1)
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
	// Use the legacy `token <…>` scheme — both `token` and `Bearer`
	// work, but `token` is what existing customer scripts use, so
	// platform-api access logs stay consistent. The token itself can
	// be either current (`bp_<64-hex>`) or legacy (`<40-hex>`) shape;
	// platform-api accepts both.
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
	case http.StatusPaymentRequired:
		// platform-api returns 402 with a plan_limit_exceeded body on
		// data-read endpoints when an org is over an enforced limit. Surface
		// it as a typed error so tool handlers can render a friendly result;
		// any other 402 shape falls through to the generic error path below.
		if pe := parsePlanLimit(body); pe != nil {
			return nil, "", pe
		}
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
