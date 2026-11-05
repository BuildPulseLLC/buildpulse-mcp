package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeRunnerPlatform is an httptest platform-api serving the three runner
// endpoints with fixed bodies, recording the query each one received.
func fakeRunnerPlatform(t *testing.T, hasTenant bool) (*httptest.Server, map[string]string) {
	t.Helper()
	seen := map[string]string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/me/organizations", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"organizations":[{"id":"11111111-1111-1111-1111-111111111111","name":"Acme"}]}`))
	})
	mux.HandleFunc("/api/v1/runners/status", func(w http.ResponseWriter, r *http.Request) {
		seen["status"] = r.URL.RawQuery
		if hasTenant {
			_, _ = w.Write([]byte(`{"hasTenant":true,"status":"active","billingMode":"metered","activatedAt":"2026-07-14T18:02:11Z","setupUrl":"https://buildpulse.io/organizations/o/runners/setup","pricingUrl":"https://buildpulse.io/pricing","web_url":"https://buildpulse.io/organizations/o/runners"}`))
			return
		}
		_, _ = w.Write([]byte(`{"hasTenant":false,"howToStart":"This organization has not activated BuildPulse runners yet. Every organization gets 3,000 free minutes a month. Activate at https://buildpulse.io/organizations/o/runners/setup","setupUrl":"https://buildpulse.io/organizations/o/runners/setup","pricingUrl":"https://buildpulse.io/pricing","web_url":"https://buildpulse.io/organizations/o/runners"}`))
	})
	mux.HandleFunc("/api/v1/runners/usage", func(w http.ResponseWriter, r *http.Request) {
		seen["usage"] = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"from":"2026-10-06T00:00:00Z","to":"2026-11-05T00:00:00Z","days":30,
		  "totals":{"jobs":1842,"billableMinutes":9120,"costUsd":41.28,"freeMinutesUsed":3000,"freeMinutesValueUsd":12,"netCostUsd":29.28,"githubListPriceUsd":62.4,"savingsPercent":34,"p50DurationSeconds":214,"p90DurationSeconds":1130,"p50QueueSeconds":4},
		  "breakdown":[{"runnerType":"x86-4x","arch":"x86-64","ciProviderType":"github_actions","jobs":1200,"billableMinutes":4800,"costUsd":38.4,"freeMinutesUsed":1500,"freeMinutesValueUsd":12,"netCostUsd":26.4,"githubListPriceUsd":57.6,"savingsPercent":33,"p50DurationSeconds":240,"p90DurationSeconds":1210},
		               {"runnerType":"gpu-1x","arch":"x86-64","ciProviderType":"circle_ci","jobs":2,"billableMinutes":10,"costUsd":0.5,"freeMinutesUsed":0,"freeMinutesValueUsd":0,"netCostUsd":0.5}],
		  "pricing":{"freeMinutesPerMonth":3000,"baseRateUsdPerMinute":0.004,"baseRunner":"x64 2 vCPU","speedupVsHosted":2,"rateCard":[{"runnerType":"x86-2x","arch":"x86-64","vcpu":2,"memoryGi":8,"buildpulseUsdPerMinute":0.004,"githubUsdPerMinute":0.006}],"pricingUrl":"https://buildpulse.io/pricing"},
		  "web_url":"https://buildpulse.io/organizations/o/runners/usage"}`))
	})
	mux.HandleFunc("/api/v1/runners/configs", func(w http.ResponseWriter, r *http.Request) {
		seen["configs"] = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"runners":[
		  {"label":"bp-ubuntu-latest-x64-2x","runnerType":"x86-2x","ciProviderType":"github_actions","arch":"x86-64","vcpu":2,"memoryGi":8,"costPerMinuteUsd":0.004,"status":"ready"},
		  {"label":"bp-ubuntu-latest-arm64-8x","runnerType":"arm64-8x","ciProviderType":"circle_ci","arch":"arm64","vcpu":8,"memoryGi":32,"costPerMinuteUsd":0.01,"status":"ready","providerConfig":{"resourceClass":"acme/bp-arm64-8x"}}],
		  "web_url":"https://buildpulse.io/organizations/o/runners/setup"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, seen
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func TestGetRunnerStatus_NoTenantRelaysOffer(t *testing.T) {
	platform, _ := fakeRunnerPlatform(t, false)
	cs := newTestServerSession(t, platform)
	res := callTool(t, cs, "get_runner_status", nil)
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(res))
	}
	var out runnerStatusOutput
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("decode: %v: %s", err, resultText(res))
	}
	if out.HasTenant {
		t.Error("has_tenant must be false")
	}
	if !strings.Contains(out.HowToStart, "3,000 free minutes") || !strings.Contains(out.HowToStart, "/runners/setup") {
		t.Errorf("how_to_start must carry the offer and the setup link: %q", out.HowToStart)
	}
	if out.SetupURL != "https://buildpulse.io/organizations/o/runners/setup" {
		t.Errorf("setup_url = %q", out.SetupURL)
	}
	if strings.Contains(strings.ToLower(resultText(res)), "stripe") {
		t.Error("status must never mention Stripe identifiers")
	}
}

func TestGetRunnerStatus_TenantFields(t *testing.T) {
	platform, seen := fakeRunnerPlatform(t, true)
	cs := newTestServerSession(t, platform)
	res := callTool(t, cs, "get_runner_status", map[string]any{"organization_id": "11111111-1111-1111-1111-111111111111"})
	var out runnerStatusOutput
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.HasTenant || out.Status != "active" || out.BillingMode != "metered" || out.ActivatedAt != "2026-07-14T18:02:11Z" {
		t.Errorf("status = %+v", out)
	}
	if out.HowToStart != "" {
		t.Errorf("a tenant must not get how_to_start, got %q", out.HowToStart)
	}
	if !strings.Contains(seen["status"], "organization_id=11111111-1111-1111-1111-111111111111") {
		t.Errorf("organization_id must be forwarded, query was %q", seen["status"])
	}
}

func TestGetRunnerUsage_DefaultsAndMapping(t *testing.T) {
	platform, seen := fakeRunnerPlatform(t, true)
	cs := newTestServerSession(t, platform)
	res := callTool(t, cs, "get_runner_usage", nil)
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(res))
	}
	// Default window: from = now - 30 days, to omitted (platform-api defaults to now).
	q := seen["usage"]
	if !strings.Contains(q, "from=") || strings.Contains(q, "to=") {
		t.Errorf("default call must send from and not to, got %q", q)
	}
	var out runnerUsageOutput
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("decode: %v: %s", err, resultText(res))
	}
	if out.Totals.Jobs != 1842 || out.Totals.NetCostUsd != 29.28 || out.Totals.SavingsPercent == nil || *out.Totals.SavingsPercent != 34 {
		t.Errorf("totals = %+v", out.Totals)
	}
	if out.Totals.P50QueueSeconds == nil || *out.Totals.P50QueueSeconds != 4 {
		t.Errorf("p50_queue_seconds = %v, want 4", out.Totals.P50QueueSeconds)
	}
	if len(out.Breakdown) != 2 || out.Breakdown[0].RunnerType != "x86-4x" || out.Breakdown[0].CiProviderType != "github_actions" {
		t.Fatalf("breakdown = %+v", out.Breakdown)
	}
	if out.Breakdown[0].GithubListPriceUsd == nil || *out.Breakdown[0].GithubListPriceUsd != 57.6 {
		t.Errorf("breakdown[0].github_list_price_usd = %v", out.Breakdown[0].GithubListPriceUsd)
	}
	// Unpriced size: the comparison and queue fields stay absent, not zero.
	raw := resultText(res)
	if gpu := strings.Index(raw, `"gpu-1x"`); gpu < 0 || strings.Contains(raw[gpu:], `"savings_percent":0`) {
		t.Errorf("unpriced size must omit savings_percent rather than report 0: %s", raw)
	}
	if out.Pricing.FreeMinutesPerMonth != 3000 || out.Pricing.BaseRateUsdPerMinute != 0.004 || out.Pricing.SpeedupVsHosted != 2 {
		t.Errorf("pricing = %+v", out.Pricing)
	}
	if len(out.Pricing.RateCard) != 1 || out.Pricing.RateCard[0].GithubUsdPerMinute != 0.006 {
		t.Errorf("rate_card = %+v", out.Pricing.RateCard)
	}
	if out.WebURL != "https://buildpulse.io/organizations/o/runners/usage" {
		t.Errorf("web_url = %q", out.WebURL)
	}
}

func TestGetRunnerUsage_DaysValidation(t *testing.T) {
	platform, seen := fakeRunnerPlatform(t, true)
	cs := newTestServerSession(t, platform)

	for _, bad := range []int{-1, 91, 400} {
		res := callTool(t, cs, "get_runner_usage", map[string]any{"days": bad})
		if !res.IsError || !strings.Contains(resultText(res), "between 1 and 90") {
			t.Errorf("days=%d must be rejected client-side, got isError=%v %q", bad, res.IsError, resultText(res))
		}
	}
	if _, called := seen["usage"]; called {
		t.Error("invalid days must not reach platform-api")
	}

	before := time.Now().UTC()
	res := callTool(t, cs, "get_runner_usage", map[string]any{"days": 7})
	if res.IsError {
		t.Fatalf("days=7: %s", resultText(res))
	}
	// from must be about 7 days before now.
	q := seen["usage"]
	i := strings.Index(q, "from=")
	if i < 0 {
		t.Fatalf("no from in %q", q)
	}
	fromStr := q[i+len("from="):]
	if j := strings.Index(fromStr, "&"); j >= 0 {
		fromStr = fromStr[:j]
	}
	fromStr = strings.ReplaceAll(fromStr, "%3A", ":")
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		t.Fatalf("from %q not RFC3339: %v", fromStr, err)
	}
	if d := before.Sub(from); d < 7*24*time.Hour-time.Minute || d > 7*24*time.Hour+time.Minute {
		t.Errorf("from is %v before now, want 7 days", d)
	}
}

func TestRunnerUsageWindow(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if got := runnerUsageWindow(0, now); got != "2026-10-06T12:00:00Z" {
		t.Errorf("default window from = %q, want 30 days back", got)
	}
	if got := runnerUsageWindow(1, now); got != "2026-11-04T12:00:00Z" {
		t.Errorf("1-day window from = %q", got)
	}
}

func TestGetRunnerConfigs_MapsLabelsAndProviderConfig(t *testing.T) {
	platform, _ := fakeRunnerPlatform(t, true)
	cs := newTestServerSession(t, platform)
	res := callTool(t, cs, "get_runner_configs", nil)
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(res))
	}
	var out runnerConfigsOutput
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("decode: %v: %s", err, resultText(res))
	}
	if out.Count != 2 || len(out.Runners) != 2 {
		t.Fatalf("count = %d runners = %d", out.Count, len(out.Runners))
	}
	if out.Runners[0].Label != "bp-ubuntu-latest-x64-2x" || out.Runners[0].CostPerMinuteUsd != 0.004 || out.Runners[0].ProviderConfig != nil {
		t.Errorf("runner[0] = %+v", out.Runners[0])
	}
	if out.Runners[1].ProviderConfig == nil || out.Runners[1].ProviderConfig.ResourceClass != "acme/bp-arm64-8x" {
		t.Errorf("runner[1] provider_config = %+v", out.Runners[1].ProviderConfig)
	}
	if out.WebURL != "https://buildpulse.io/organizations/o/runners/setup" {
		t.Errorf("web_url = %q", out.WebURL)
	}
}

// TestRunnerTools_PlanRequired403IsFriendly: a valid token on an org whose
// plan excludes the API gets platform-api's 403 plan_required; every tool
// turns it into a readable, non-error result naming what unlocks access.
func TestRunnerTools_PlanRequired403IsFriendly(t *testing.T) {
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/me/organizations" {
			_, _ = w.Write([]byte(`{"organizations":[{"id":"11111111-1111-1111-1111-111111111111","name":"Acme"}]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"plan_required","message":"This BuildPulse organization's plan does not include API access. A paid Test Intelligence plan or a paid BuildPulse runner subscription unlocks the REST API and the MCP server.","upgradeUrl":"https://buildpulse.io/pricing"}`))
	}))
	t.Cleanup(platform.Close)
	cs := newTestServerSession(t, platform)
	for _, tool := range []string{"get_runner_status", "get_runner_usage", "get_runner_configs"} {
		res := callTool(t, cs, tool, nil)
		if res.IsError {
			t.Errorf("%s: plan_required must be a friendly non-error result, got error %q", tool, resultText(res))
		}
		txt := resultText(res)
		if !strings.Contains(txt, "runner subscription") || !strings.Contains(txt, "https://buildpulse.io/pricing") {
			t.Errorf("%s: message must name the unlock paths and pricing URL, got %q", tool, txt)
		}
	}
}

// TestGet_403WithoutPlanRequiredCode_IsNormalError keeps any other 403 on the
// generic error path.
func TestGet_403WithoutPlanRequiredCode_IsNormalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`forbidden`))
	}))
	defer srv.Close()
	_, _, err := NewClient(srv.URL, "tok").Get(context.Background(), "/api/v1/runners/status", nil)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected generic 403 error, got %v", err)
	}
}

// TestEstimateRunnerSavingsPrompt pins the prompt's contract: it is
// registered, it walks status -> usage (tenant) or status -> estimate (no
// tenant), it never hardcodes a per-minute price, and it sources prices from
// the usage response's pricing block.
func TestEstimateRunnerSavingsPrompt(t *testing.T) {
	platform, _ := fakeRunnerPlatform(t, false)
	cs := newTestServerSession(t, platform)
	list, err := cs.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	names := map[string]bool{}
	for _, p := range list.Prompts {
		names[p.Name] = true
	}
	for _, want := range []string{"estimate_runner_savings", "triage_flaky_tests", "ci_health_check", "explain_test_failure", "whats_red"} {
		if !names[want] {
			t.Errorf("prompt %q missing; registered: %v", want, names)
		}
	}
	if len(list.Prompts) != 5 {
		t.Errorf("registered %d prompts, want 5: update the READMEs, llms.txt and the landing page", len(list.Prompts))
	}

	res, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "estimate_runner_savings", Arguments: map[string]string{"organization_id": "11111111-1111-1111-1111-111111111111"}})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	txt := res.Messages[0].Content.(*mcp.TextContent).Text
	for _, want := range []string{"get_runner_status", "get_runner_usage", "pricing.rate_card", "free_minutes_per_month", "speedup_vs_hosted", "list_recent_submissions", "get_submission_test_results", "setup_url", "/runners/setup", "3,000 free minutes", `organization_id="11111111-1111-1111-1111-111111111111"`} {
		if !strings.Contains(txt, want) {
			t.Errorf("prompt text missing %q", want)
		}
	}
	for _, banned := range []string{"$0.004", "$0.006", "0.004", "0.006"} {
		if strings.Contains(txt, banned) {
			t.Errorf("prompt must not hardcode a per-minute price, found %q", banned)
		}
	}
	if strings.ContainsAny(txt, "—―") {
		t.Error("prompt text contains an em-dash")
	}
	// Optional repository argument steers the sampling step.
	res, err = cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "estimate_runner_savings", Arguments: map[string]string{"repository": "widgets"}})
	if err != nil {
		t.Fatalf("GetPrompt with repository: %v", err)
	}
	if txt := res.Messages[0].Content.(*mcp.TextContent).Text; !strings.Contains(txt, "**widgets**") || strings.Contains(txt, "organization_id=") {
		t.Errorf("repository argument not honoured / org arg leaked: %s", txt)
	}
}

// TestServerInstructionsLeadWithRunners guards the positioning: the
// instructions an agent reads on initialize open with runners and Test
// Intelligence, name the setup URL, and contain no em-dashes.
func TestServerInstructionsLeadWithRunners(t *testing.T) {
	platform, _ := fakeRunnerPlatform(t, true)
	cs := newTestServerSession(t, platform)
	instr := cs.InitializeResult().Instructions
	if !strings.HasPrefix(instr, "BuildPulse provides faster, cheaper CI runners") {
		t.Errorf("instructions must lead with runners, got: %.120s", instr)
	}
	for _, want := range []string{"Test Intelligence", "3,000 free minutes", "https://buildpulse.io/organizations/{orgId}/runners/setup", "get_runner_status", "get_runner_usage", "get_runner_configs", "plan_required", "find_flaky_tests", "list_repositories"} {
		if !strings.Contains(instr, want) {
			t.Errorf("instructions missing %q", want)
		}
	}
	if strings.ContainsAny(instr, "—―") {
		t.Error("instructions contain an em-dash")
	}
}
