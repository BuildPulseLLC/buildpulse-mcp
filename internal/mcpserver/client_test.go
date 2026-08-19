package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPercentFromBadgeSVG(t *testing.T) {
	cases := []struct {
		name string
		svg  string
		want float64
	}{
		{"zero percent", `<text>0%</text>`, 0},
		{"integer percent", `<text x="180" y="14">42%</text>`, 42},
		{"decimal percent", `<text x="180" y="14" font-size="12">12.5%</text>`, 12.5},
		{"hundred percent", `<svg>...<text>100%</text></svg>`, 100},
		{"no percent in svg", `<svg></svg>`, -1},
		{"non-numeric", `<text>N/A%</text>`, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PercentFromBadgeSVG([]byte(tc.svg))
			if got != tc.want {
				t.Errorf("PercentFromBadgeSVG(%q) = %v, want %v", tc.svg, got, tc.want)
			}
		})
	}
}

func TestFlakinessColor(t *testing.T) {
	cases := []struct {
		pct  float64
		want string
	}{
		{-1, "unknown"},
		{0, "green"},
		{1, "yellow"},
		{20, "yellow"},
		{20.5, "red"},
		{75, "red"},
	}
	for _, tc := range cases {
		if got := FlakinessColor(tc.pct); got != tc.want {
			t.Errorf("FlakinessColor(%v) = %q, want %q", tc.pct, got, tc.want)
		}
	}
}

func TestCoverageColor(t *testing.T) {
	cases := []struct {
		pct  float64
		want string
	}{
		{-1, "unknown"},
		{0, "red"},
		{69.9, "red"},
		{70, "yellow"},
		{89, "yellow"},
		{90, "light_green"},
		{99.9, "light_green"},
		{100, "green"},
	}
	for _, tc := range cases {
		if got := CoverageColor(tc.pct); got != tc.want {
			t.Errorf("CoverageColor(%v) = %q, want %q", tc.pct, got, tc.want)
		}
	}
}

func TestNewClientNormalizesURL(t *testing.T) {
	c := NewClient("https://example.com/", "abc")
	if c.BaseURL() != "https://example.com" {
		t.Errorf("BaseURL trailing slash not trimmed: %q", c.BaseURL())
	}
	c2 := NewClient("", "abc")
	if c2.BaseURL() != DefaultPlatformURL {
		t.Errorf("empty baseURL not defaulted: %q", c2.BaseURL())
	}
}

func TestWebURL(t *testing.T) {
	c := NewClient("https://platform.buildpulse.io", "abc")
	if got, want := c.WebURL("/repos/acme/widgets"), "https://buildpulse.io/repos/acme/widgets"; got != want {
		t.Errorf("WebURL = %q, want %q", got, want)
	}
	c2 := NewClient("https://platform.dev.buildpulse.io", "abc")
	if got, want := c2.WebURL("/repos/x/y"), "https://dev.buildpulse.io/repos/x/y"; got != want {
		t.Errorf("WebURL = %q, want %q", got, want)
	}
	// A custom host without a "platform." label falls through unchanged.
	c3 := NewClient("https://example.com", "abc")
	if got, want := c3.WebURL("/repos/x/y"), "https://example.com/repos/x/y"; got != want {
		t.Errorf("WebURL = %q, want %q", got, want)
	}
}

func TestResolveAPIURL(t *testing.T) {
	c := NewClient("https://platform.buildpulse.io", "tok")
	got, err := c.resolveAPIURL("/api/me/organizations", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://platform.buildpulse.io/api/me/organizations" {
		t.Errorf("got %q", got)
	}

	params := url.Values{}
	params.Set("organization_id", "11111111-1111-1111-1111-111111111111")
	got, err = c.resolveAPIURL("/api/v1/flaky/tests", params)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "organization_id=") {
		t.Errorf("query not attached: %q", got)
	}

	bads := []string{
		"/health",
		"https://evil.example/api",
		"//evil.example/api",
		"/api/../secret",
		"/api/foo@evil",
	}
	for _, p := range bads {
		if _, err := c.resolveAPIURL(p, nil); err == nil {
			t.Errorf("path %q should be rejected", p)
		}
	}
}

func TestClientGet_BlocksOffHostRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/me/organizations", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example/steal", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, _, err := NewClient(srv.URL, "tok").Get(context.Background(), "/api/me/organizations", nil)
	if err == nil {
		t.Fatal("off-host redirect must fail")
	}
	if !strings.Contains(err.Error(), "blocked redirect") && !strings.Contains(err.Error(), "redirect") {
		t.Errorf("unexpected error: %v", err)
	}
}
