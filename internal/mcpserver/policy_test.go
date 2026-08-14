package mcpserver

import (
	"testing"
	"time"
)

func TestTokenLimiterAllowsBurstThenBlocks(t *testing.T) {
	l := newTokenLimiter()
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < toolRateLimit; i++ {
		if !l.allow("tok", now) {
			t.Fatalf("call %d should be allowed", i+1)
		}
	}
	if l.allow("tok", now) {
		t.Fatal("expected rate limit after budget is spent")
	}
	if !l.allow("other", now) {
		t.Fatal("a different token must have its own budget")
	}
	if !l.allow("tok", now.Add(toolRateWindow+time.Second)) {
		t.Fatal("budget should refill after the window")
	}
}

func TestTokenLimiterEvictsIdleKeys(t *testing.T) {
	l := newTokenLimiter()
	now := time.Unix(1_700_000_000, 0)
	if !l.allow("idle", now) {
		t.Fatal("idle token should be allowed")
	}
	later := now.Add(toolRateWindow + time.Second)
	if !l.allow("fresh", later) {
		t.Fatal("fresh token should be allowed")
	}
	if _, ok := l.hits["idle"]; ok {
		t.Fatal("idle key should be deleted once its window has elapsed")
	}
	if _, ok := l.hits["fresh"]; !ok {
		t.Fatal("active key should remain")
	}
}

func TestOrganizationIDFrom(t *testing.T) {
	type in struct {
		OrganizationID string `json:"organization_id"`
		Repository     string `json:"repository"`
	}
	if got := organizationIDFrom(in{OrganizationID: "org-1", Repository: "widgets"}); got != "org-1" {
		t.Fatalf("got %q", got)
	}
	if got := organizationIDFrom(struct{ Name string }{Name: "x"}); got != "" {
		t.Fatalf("expected empty org, got %q", got)
	}
}

func TestClientTokenKeyIsStableAndNotRaw(t *testing.T) {
	c := NewClient("https://platform.buildpulse.io", "bp_secret_token_value")
	k := c.tokenKey()
	if k == "" || k == "bp_secret_token_value" {
		t.Fatalf("tokenKey leaked or empty: %q", k)
	}
	if c.tokenKey() != k {
		t.Fatal("tokenKey should be stable")
	}
}
