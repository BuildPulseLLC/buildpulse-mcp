package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newPublicTestMux mirrors the route layout in main(): the public
// documentation routes plus a stand-in for every authenticated or
// discovery route the landing page must not shadow.
func newPublicTestMux() *http.ServeMux {
	mux := http.NewServeMux()
	registerPublicRoutes(mux)
	marker := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Handler", name)
			w.WriteHeader(http.StatusOK)
		}
	}
	mux.Handle("/mcp", marker("mcp"))
	mux.Handle("/mcp/", marker("mcp"))
	mux.HandleFunc("GET "+healthPath, marker("health"))
	mux.HandleFunc("GET "+wellKnownOAuth, marker("oauth-metadata"))
	mux.HandleFunc("GET "+wellKnownMCP, marker("mcp-discovery"))
	mux.HandleFunc("POST /oauth/register", marker("oauth-register"))
	return mux
}

func get(t *testing.T, mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestLandingPage(t *testing.T) {
	mux := newPublicTestMux()
	rec := get(t, mux, http.MethodGet, "/")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != publicCacheControl {
		t.Errorf("GET / Cache-Control = %q, want %q", cc, publicCacheControl)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"BuildPulse",
		"<title>BuildPulse MCP Server</title>",
		`<link rel="canonical" href="https://mcp.buildpulse.io/">`,
		`"@type": "SoftwareApplication"`,
		"https://mcp.buildpulse.io/mcp",
		"claude mcp add --transport http buildpulse https://mcp.buildpulse.io/mcp",
		"npx",
		"@buildpulse/mcp",
		"BUILDPULSE_TOKEN",
		"support@buildpulse.io",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET / body missing %q", want)
		}
	}
	// Every registered tool must be advertised on the landing page —
	// keep this list in sync with internal/mcpserver/tools.go.
	for _, tool := range []string{
		"list_my_organizations",
		"list_repositories",
		"find_flaky_tests",
		"get_test_history",
		"list_recent_submissions",
		"get_submission_test_results",
		"get_recent_failures",
		"get_repo_flakiness",
		"get_repo_coverage",
	} {
		if !strings.Contains(body, "<code>"+tool+"</code>") {
			t.Errorf("GET / body does not list tool %q", tool)
		}
	}
	// Legacy hostnames must not leak into anything new.
	for _, banned := range []string{"app.buildpulse.io", "app2.buildpulse.io"} {
		if strings.Contains(body, banned) {
			t.Errorf("GET / body references legacy host %q", banned)
		}
	}
}

func TestPublicTextRoutes(t *testing.T) {
	mux := newPublicTestMux()
	cases := []struct {
		path, wantCT, wantBody string
	}{
		{"/robots.txt", "text/plain", "Sitemap: https://mcp.buildpulse.io/sitemap.xml"},
		{"/robots.txt", "text/plain", "User-agent: ClaudeBot"},
		{"/robots.txt", "text/plain", "User-agent: GPTBot"},
		{"/sitemap.xml", "application/xml", "<loc>https://mcp.buildpulse.io/llms.txt</loc>"},
		{"/llms.txt", "text/plain", "# BuildPulse MCP Server"},
		{"/llms.txt", "text/plain", "https://platform.buildpulse.io/llms.txt"},
		{"/llms.txt", "text/plain", "io.github.BuildPulseLLC/buildpulse-mcp"},
	}
	for _, tc := range cases {
		rec := get(t, mux, http.MethodGet, tc.path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", tc.path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tc.wantCT) {
			t.Errorf("GET %s Content-Type = %q, want prefix %q", tc.path, ct, tc.wantCT)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != publicCacheControl {
			t.Errorf("GET %s Cache-Control = %q, want %q", tc.path, cc, publicCacheControl)
		}
		if !strings.Contains(rec.Body.String(), tc.wantBody) {
			t.Errorf("GET %s body missing %q", tc.path, tc.wantBody)
		}
	}
}

func TestLandingPageDoesNotShadowOtherRoutes(t *testing.T) {
	mux := newPublicTestMux()

	// Unknown paths still 404 — `GET /{$}` is exact-match, not a prefix.
	for _, path := range []string{"/nonexistent", "/index.html", "/public/index.html", "/oauth/nope"} {
		if rec := get(t, mux, http.MethodGet, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", path, rec.Code)
		}
	}

	// Existing routes are reached by their own handlers.
	for path, want := range map[string]string{
		"/mcp":         "mcp",
		"/mcp/":        "mcp",
		healthPath:     "health",
		wellKnownOAuth: "oauth-metadata",
		wellKnownMCP:   "mcp-discovery",
	} {
		rec := get(t, mux, http.MethodGet, path)
		if got := rec.Header().Get("X-Handler"); got != want {
			t.Errorf("GET %s routed to %q, want %q (status %d)", path, got, want, rec.Code)
		}
	}
	if rec := get(t, mux, http.MethodPost, "/oauth/register"); rec.Header().Get("X-Handler") != "oauth-register" {
		t.Errorf("POST /oauth/register not routed to its handler (status %d)", rec.Code)
	}

	// The landing page is GET-only; a POST to / must not be served as HTML.
	if rec := get(t, mux, http.MethodPost, "/"); rec.Code == http.StatusOK {
		t.Errorf("POST / status = %d, want non-200", rec.Code)
	}
}
