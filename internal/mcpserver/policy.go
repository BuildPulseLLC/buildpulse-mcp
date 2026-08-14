package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Generous per-token tool-call budget (DEV-189 / SECURITY.md P1).
// Sequential triage (`list_my_organizations` → `list_repositories` →
// `find_flaky_tests` → `get_test_history` × N) is the product workflow.
// We 429-as-a-tool-result when the budget is spent; we never kill the
// session or require HITL on these read tools.
const (
	toolRateLimit  = 120
	toolRateWindow = time.Minute
)

type tokenLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newTokenLimiter() *tokenLimiter {
	return &tokenLimiter{hits: make(map[string][]time.Time)}
}

func (l *tokenLimiter) allow(key string, now time.Time) bool {
	if key == "" {
		key = "anonymous"
	}
	cutoff := now.Add(-toolRateWindow)
	l.mu.Lock()
	defer l.mu.Unlock()

	// Drop keys whose hits have all aged out so a long-lived ECS task
	// does not retain one map entry per token that ever called a tool.
	for k, ts := range l.hits {
		j := 0
		for j < len(ts) && !ts[j].After(cutoff) {
			j++
		}
		ts = ts[j:]
		if len(ts) == 0 {
			delete(l.hits, k)
		} else {
			l.hits[k] = ts
		}
	}

	times := l.hits[key]
	if len(times) >= toolRateLimit {
		return false
	}
	l.hits[key] = append(times, now)
	return true
}

var defaultLimiter = newTokenLimiter()

// auditf is the structured tool audit line (tool / org / status).
// Tests may replace it. Never include the raw token.
var auditf = func(tool, org, status string) {
	if org == "" {
		org = "-"
	}
	log.Printf("mcp_audit tool=%q org=%q status=%q", tool, org, status)
}

func (c *Client) tokenKey() string {
	if c == nil || c.token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(c.token))
	return hex.EncodeToString(sum[:8])
}

func organizationIDFrom(in any) string {
	b, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	var w struct {
		OrganizationID string `json:"organization_id"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return ""
	}
	return w.OrganizationID
}

func rateLimitedResult[Out any]() (*mcp.CallToolResult, Out, error) {
	var zero Out
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "" +
			"This BuildPulse token has hit the per-minute tool-call limit " +
			fmt.Sprintf("(%d calls / %s). ", toolRateLimit, toolRateWindow) +
			"Wait a few seconds and retry. The session is still valid; nothing was revoked."}},
	}, zero, nil
}

// guarded applies per-token rate limits and the tool audit log, then
// overageAware. Rate-limit refusals are tool results (retryable), not
// transport errors and not session kills.
func guarded[In, Out any](c *Client, tool string, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	inner := overageAware(h)
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		org := organizationIDFrom(in)
		if !defaultLimiter.allow(c.tokenKey(), time.Now()) {
			auditf(tool, org, "rate_limited")
			return rateLimitedResult[Out]()
		}
		res, out, err := inner(ctx, req, in)
		status := "ok"
		switch {
		case err != nil:
			status = "error"
		case res != nil && res.IsError:
			status = "error"
		}
		auditf(tool, org, status)
		return res, out, err
	}
}
