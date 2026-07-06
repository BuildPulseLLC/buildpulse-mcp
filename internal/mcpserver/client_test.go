package mcpserver

import "testing"

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
