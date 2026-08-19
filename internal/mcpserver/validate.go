package mcpserver

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Hosted platform-api origins the remote MCP (mcp.buildpulse.io) may call.
// stdio NewClient does not use this list — local `npx` users and unit tests
// may point PLATFORM_API_URL at httptest or a custom host.
var hostedPlatformHosts = map[string]struct{}{
	"platform.buildpulse.io":     {},
	"platform.dev.buildpulse.io": {},
}

var (
	uuidRe     = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	objectIDRe = regexp.MustCompile(`(?i)^[0-9a-f]{24}$`)
)

// validateOrganizationID accepts empty (single-org auto-scope) or a UUID.
func validateOrganizationID(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	if !uuidRe.MatchString(s) {
		return fmt.Errorf("organization_id must be a UUID (the id from list_my_organizations), got %q", s)
	}
	return nil
}

func validateObjectID(raw, field string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if !objectIDRe.MatchString(s) {
		return "", fmt.Errorf("%s must be a 24-char hex string", field)
	}
	return strings.ToLower(s), nil
}

// validateRepoPart rejects values that can change an HTTP path or host
// (slash, backslash, "..", "@", control chars) while still allowing the
// dots, hyphens, and underscores GitHub-style names use.
func validateRepoPart(raw, field string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if len(s) > 200 {
		return "", fmt.Errorf("%s is too long", field)
	}
	if strings.Contains(s, "..") || strings.ContainsAny(s, "/\\@") {
		return "", fmt.Errorf("%s contains invalid characters", field)
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return "", fmt.Errorf("%s contains invalid characters", field)
		}
	}
	return s, nil
}

func validateOptionalLimit(n, max int) error {
	if n == 0 {
		return nil
	}
	if n < 1 || n > max {
		return fmt.Errorf("limit must be between 1 and %d, got %d", max, n)
	}
	return nil
}

func validateSubmissionsWindow(n int) error {
	if n == 0 {
		return nil
	}
	if n < 1 || n > 50 {
		return fmt.Errorf("submissions must be between 1 and 50, got %d", n)
	}
	return nil
}

// ValidateHostedPlatformURL is the mcp-remote startup gate: outbound
// calls may only target the production or development Platform API.
func ValidateHostedPlatformURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		raw = DefaultPlatformURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("PLATFORM_API_URL is not a valid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("PLATFORM_API_URL must use https, got %q", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("PLATFORM_API_URL must not include credentials")
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("PLATFORM_API_URL must not include a path")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("PLATFORM_API_URL must not include a query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("PLATFORM_API_URL is missing a host")
	}
	if ip := net.ParseIP(host); ip != nil {
		return fmt.Errorf("PLATFORM_API_URL must be a hostname, not an IP")
	}
	if _, ok := hostedPlatformHosts[host]; !ok {
		return fmt.Errorf("PLATFORM_API_URL host %q is not allowed", host)
	}
	return nil
}
