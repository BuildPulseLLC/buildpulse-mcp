package mcpserver

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Runner tools: thin, typed views over platform-api's /api/v1/runners/*
// endpoints. BuildPulse runners are the primary product (about twice as fast
// at half the cost of GitHub-hosted, CircleCI and Semaphore runners, 3,000
// free minutes a month, Test Intelligence included on every minute), so an
// agent must be able to learn whether an organization is on them and read
// what they cost and saved. All three are organization-scoped: no repository
// argument. Prices and the free-minute grant come back in the API's
// `pricing` block; nothing here hardcodes a rate.

const (
	runnerUsageDefaultDays = 30
	runnerUsageMaxDays     = 90
)

func registerRunnerTools(s *mcp.Server, c *Client) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "get_runner_status",
		Title: "Get BuildPulse runner status",
		Description: "Report whether the organization has activated BuildPulse CI runners, and how to start if it has not. " +
			"BuildPulse runners are drop-in runners for GitHub Actions, CircleCI and Semaphore that finish jobs about 2x faster at half the cost of the provider's hosted runners, " +
			"with Test Intelligence (flaky-test detection, coverage and CI metrics) included with every minute. Every organization gets 3,000 free minutes a month, no card required. " +
			"With a tenant: `status`, `billing_mode` (free: cardless, served from the monthly grant; metered: card on file; exempt: BuildPulse's own) and `activated_at`. " +
			"Without one: `how_to_start` is a sentence you can relay, and `setup_url` (https://buildpulse.io/organizations/{orgId}/runners/setup) is where activation happens: " +
			"the customer picks a size and changes one line in the workflow (the runs-on label, resource class or agent type). " +
			"Call this before get_runner_usage when you do not know whether the organization uses runners. For users in multiple organizations, pass `organization_id`.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			OpenWorldHint:   closedWorld(),
			DestructiveHint: nonDestructive(),
			Title:           "Get BuildPulse runner status",
		},
	}, guarded(c, "get_runner_status", getRunnerStatus(c)))

	mcp.AddTool(s, &mcp.Tool{
		Name:  "get_runner_usage",
		Title: "Get runner usage, cost and savings",
		Description: "Return what the organization's BuildPulse runners did over the last `days` days (1-90, default 30): jobs, billable minutes, cost before free minutes, free minutes used and their value, net cost, " +
			"p50 and p90 job duration in seconds, p50 queue wait (only when jobs measured it), and the same minutes priced at GitHub's hosted larger-runner list rate with `savings_percent`. " +
			"Given as `totals` and as a `breakdown` per runner size, architecture and CI provider (highest cost first). " +
			"`pricing` carries the published offer and the full rate card (BuildPulse and GitHub list price per size, free minutes per month, speedup), and is returned even when the organization has no runners, so use it for any estimate instead of remembering prices. " +
			"Zeroed totals with an empty breakdown means nothing ran on BuildPulse runners in the window; check get_runner_status for whether runners are activated at all. For users in multiple organizations, pass `organization_id`.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			OpenWorldHint:   closedWorld(),
			DestructiveHint: nonDestructive(),
			Title:           "Get runner usage, cost and savings",
		},
	}, guarded(c, "get_runner_usage", getRunnerUsage(c)))

	mcp.AddTool(s, &mcp.Tool{
		Name:  "get_runner_configs",
		Title: "List configured runner sizes and labels",
		Description: "List the BuildPulse runner sizes the organization has configured: the `label` a workflow targets (the GitHub Actions `runs-on` value, e.g. bp-ubuntu-latest-x64-2x), CI provider, vCPU, memory, architecture, list price per minute and provisioning status. " +
			"For CircleCI and Semaphore, `provider_config` holds the resource class or agent type the pipeline points at (never a token). " +
			"Use this when editing a workflow onto BuildPulse runners or answering \"which runner labels can I use?\". An empty list with runners activated means no size has been added yet; `web_url` is the setup page. For users in multiple organizations, pass `organization_id`.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			OpenWorldHint:   closedWorld(),
			DestructiveHint: nonDestructive(),
			Title:           "List configured runner sizes and labels",
		},
	}, guarded(c, "get_runner_configs", getRunnerConfigs(c)))
}

// --- get_runner_status -------------------------------------------------------

type runnerStatusInput struct {
	OrganizationID string `json:"organization_id,omitempty" jsonschema:"organization UUID (the id field from list_my_organizations). Required for multi-tenant users; ignored for single-org tokens."`
}

type runnerStatusOutput struct {
	HasTenant   bool   `json:"has_tenant" jsonschema:"true when the organization has activated BuildPulse runners"`
	Status      string `json:"status,omitempty" jsonschema:"tenant provisioning status; only with has_tenant"`
	BillingMode string `json:"billing_mode,omitempty" jsonschema:"free | metered | exempt; only with has_tenant"`
	ActivatedAt string `json:"activated_at,omitempty"`
	HowToStart  string `json:"how_to_start,omitempty" jsonschema:"only without a tenant: the free-minute offer and the setup URL, ready to relay"`
	SetupURL    string `json:"setup_url" jsonschema:"where runners are activated: https://buildpulse.io/organizations/{orgId}/runners/setup"`
	PricingURL  string `json:"pricing_url"`
	WebURL      string `json:"web_url"`
}

// platformRunnerStatus is platform-api's wire shape (camelCase).
type platformRunnerStatus struct {
	HasTenant   bool   `json:"hasTenant"`
	Status      string `json:"status"`
	BillingMode string `json:"billingMode"`
	ActivatedAt string `json:"activatedAt"`
	HowToStart  string `json:"howToStart"`
	SetupURL    string `json:"setupUrl"`
	PricingURL  string `json:"pricingUrl"`
	WebURL      string `json:"web_url"`
}

func getRunnerStatus(c *Client) mcp.ToolHandlerFor[runnerStatusInput, runnerStatusOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in runnerStatusInput) (*mcp.CallToolResult, runnerStatusOutput, error) {
		orgID, err := resolveOrgID(ctx, c, in.OrganizationID)
		if err != nil {
			return nil, runnerStatusOutput{}, err
		}
		params := url.Values{}
		addOrgParam(params, orgID)

		var r platformRunnerStatus
		if err := c.GetJSON(ctx, "/api/v1/runners/status", params, &r); err != nil {
			return nil, runnerStatusOutput{}, err
		}
		return nil, runnerStatusOutput{
			HasTenant:   r.HasTenant,
			Status:      r.Status,
			BillingMode: r.BillingMode,
			ActivatedAt: r.ActivatedAt,
			HowToStart:  r.HowToStart,
			SetupURL:    r.SetupURL,
			PricingURL:  r.PricingURL,
			WebURL:      r.WebURL,
		}, nil
	}
}

// --- get_runner_usage --------------------------------------------------------

type runnerUsageInput struct {
	Days           int    `json:"days,omitempty" jsonschema:"how many days back to report, 1-90 (default 30)"`
	OrganizationID string `json:"organization_id,omitempty" jsonschema:"organization UUID (the id field from list_my_organizations). Required for multi-tenant users; ignored for single-org tokens."`
}

// runnerUsageFigures mirrors platform-api RunnerUsageFigures. Optional
// fields are pointers so "not measured" stays distinguishable from zero.
type runnerUsageFigures struct {
	Jobs                int64    `json:"jobs"`
	BillableMinutes     float64  `json:"billable_minutes"`
	CostUsd             float64  `json:"cost_usd" jsonschema:"the minutes at BuildPulse list price, before free minutes"`
	FreeMinutesUsed     float64  `json:"free_minutes_used"`
	FreeMinutesValueUsd float64  `json:"free_minutes_value_usd"`
	NetCostUsd          float64  `json:"net_cost_usd" jsonschema:"cost_usd minus free_minutes_value_usd: what the invoice charges before credits"`
	GithubListPriceUsd  *float64 `json:"github_list_price_usd,omitempty" jsonschema:"the same billable minutes at GitHub's hosted larger-runner list price; absent when no minute was on a priced size"`
	SavingsPercent      *int     `json:"savings_percent,omitempty" jsonschema:"100 x (1 - BuildPulse list cost / github_list_price_usd); absent with github_list_price_usd"`
	P50DurationSeconds  *int     `json:"p50_duration_seconds,omitempty"`
	P90DurationSeconds  *int     `json:"p90_duration_seconds,omitempty"`
	P50QueueSeconds     *int     `json:"p50_queue_seconds,omitempty" jsonschema:"median queued-to-running wait; absent when no job in the window measured it"`
}

// runnerUsageGroup is one breakdown row. Fields are spelled out rather than
// embedded so the SDK's output-schema inference sees a flat object.
type runnerUsageGroup struct {
	RunnerType          string   `json:"runner_type"`
	Arch                string   `json:"arch"`
	CiProviderType      string   `json:"ci_provider_type"`
	Jobs                int64    `json:"jobs"`
	BillableMinutes     float64  `json:"billable_minutes"`
	CostUsd             float64  `json:"cost_usd"`
	FreeMinutesUsed     float64  `json:"free_minutes_used"`
	FreeMinutesValueUsd float64  `json:"free_minutes_value_usd"`
	NetCostUsd          float64  `json:"net_cost_usd"`
	GithubListPriceUsd  *float64 `json:"github_list_price_usd,omitempty"`
	SavingsPercent      *int     `json:"savings_percent,omitempty"`
	P50DurationSeconds  *int     `json:"p50_duration_seconds,omitempty"`
	P90DurationSeconds  *int     `json:"p90_duration_seconds,omitempty"`
	P50QueueSeconds     *int     `json:"p50_queue_seconds,omitempty"`
}

type rateCardRate struct {
	RunnerType             string  `json:"runner_type"`
	Arch                   string  `json:"arch"`
	VCPU                   int     `json:"vcpu"`
	MemoryGi               int     `json:"memory_gi"`
	BuildpulseUsdPerMinute float64 `json:"buildpulse_usd_per_minute"`
	GithubUsdPerMinute     float64 `json:"github_usd_per_minute"`
}

type runnerPricing struct {
	FreeMinutesPerMonth  int            `json:"free_minutes_per_month"`
	BaseRateUsdPerMinute float64        `json:"base_rate_usd_per_minute"`
	BaseRunner           string         `json:"base_runner"`
	SpeedupVsHosted      int            `json:"speedup_vs_hosted"`
	RateCard             []rateCardRate `json:"rate_card"`
	PricingURL           string         `json:"pricing_url"`
}

type runnerUsageOutput struct {
	From      string             `json:"from"`
	To        string             `json:"to"`
	Days      int                `json:"days"`
	Totals    runnerUsageFigures `json:"totals"`
	Breakdown []runnerUsageGroup `json:"breakdown"`
	Pricing   runnerPricing      `json:"pricing"`
	WebURL    string             `json:"web_url"`
}

// platform-api wire shapes (camelCase) for the usage endpoint.
type platformUsageFigures struct {
	Jobs                int64    `json:"jobs"`
	BillableMinutes     float64  `json:"billableMinutes"`
	CostUsd             float64  `json:"costUsd"`
	FreeMinutesUsed     float64  `json:"freeMinutesUsed"`
	FreeMinutesValueUsd float64  `json:"freeMinutesValueUsd"`
	NetCostUsd          float64  `json:"netCostUsd"`
	GithubListPriceUsd  *float64 `json:"githubListPriceUsd"`
	SavingsPercent      *int     `json:"savingsPercent"`
	P50DurationSeconds  *int     `json:"p50DurationSeconds"`
	P90DurationSeconds  *int     `json:"p90DurationSeconds"`
	P50QueueSeconds     *int     `json:"p50QueueSeconds"`
}

func (p platformUsageFigures) out() runnerUsageFigures {
	return runnerUsageFigures{
		Jobs:                p.Jobs,
		BillableMinutes:     p.BillableMinutes,
		CostUsd:             p.CostUsd,
		FreeMinutesUsed:     p.FreeMinutesUsed,
		FreeMinutesValueUsd: p.FreeMinutesValueUsd,
		NetCostUsd:          p.NetCostUsd,
		GithubListPriceUsd:  p.GithubListPriceUsd,
		SavingsPercent:      p.SavingsPercent,
		P50DurationSeconds:  p.P50DurationSeconds,
		P90DurationSeconds:  p.P90DurationSeconds,
		P50QueueSeconds:     p.P50QueueSeconds,
	}
}

type platformRunnerUsage struct {
	From      string               `json:"from"`
	To        string               `json:"to"`
	Days      int                  `json:"days"`
	Totals    platformUsageFigures `json:"totals"`
	Breakdown []struct {
		RunnerType     string `json:"runnerType"`
		Arch           string `json:"arch"`
		CiProviderType string `json:"ciProviderType"`
		platformUsageFigures
	} `json:"breakdown"`
	Pricing struct {
		FreeMinutesPerMonth  int     `json:"freeMinutesPerMonth"`
		BaseRateUsdPerMinute float64 `json:"baseRateUsdPerMinute"`
		BaseRunner           string  `json:"baseRunner"`
		SpeedupVsHosted      int     `json:"speedupVsHosted"`
		RateCard             []struct {
			RunnerType             string  `json:"runnerType"`
			Arch                   string  `json:"arch"`
			VCPU                   int     `json:"vcpu"`
			MemoryGi               int     `json:"memoryGi"`
			BuildpulseUsdPerMinute float64 `json:"buildpulseUsdPerMinute"`
			GithubUsdPerMinute     float64 `json:"githubUsdPerMinute"`
		} `json:"rateCard"`
		PricingURL string `json:"pricingUrl"`
	} `json:"pricing"`
	WebURL string `json:"web_url"`
}

// validateRunnerDays accepts 0 (default) or 1..runnerUsageMaxDays.
func validateRunnerDays(n int) error {
	if n == 0 {
		return nil
	}
	if n < 1 || n > runnerUsageMaxDays {
		return fmt.Errorf("days must be between 1 and %d, got %d", runnerUsageMaxDays, n)
	}
	return nil
}

// runnerUsageWindow turns `days` into the from/to query platform-api takes:
// an RFC3339 `from` N days before now, `to` left to default to now.
func runnerUsageWindow(days int, now time.Time) string {
	if days <= 0 {
		days = runnerUsageDefaultDays
	}
	return now.UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
}

func getRunnerUsage(c *Client) mcp.ToolHandlerFor[runnerUsageInput, runnerUsageOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in runnerUsageInput) (*mcp.CallToolResult, runnerUsageOutput, error) {
		if err := validateRunnerDays(in.Days); err != nil {
			return nil, runnerUsageOutput{}, err
		}
		orgID, err := resolveOrgID(ctx, c, in.OrganizationID)
		if err != nil {
			return nil, runnerUsageOutput{}, err
		}
		params := url.Values{}
		params.Set("from", runnerUsageWindow(in.Days, time.Now()))
		addOrgParam(params, orgID)

		var r platformRunnerUsage
		if err := c.GetJSON(ctx, "/api/v1/runners/usage", params, &r); err != nil {
			return nil, runnerUsageOutput{}, err
		}

		out := runnerUsageOutput{
			From:      r.From,
			To:        r.To,
			Days:      r.Days,
			Totals:    r.Totals.out(),
			Breakdown: make([]runnerUsageGroup, 0, len(r.Breakdown)),
			Pricing: runnerPricing{
				FreeMinutesPerMonth:  r.Pricing.FreeMinutesPerMonth,
				BaseRateUsdPerMinute: r.Pricing.BaseRateUsdPerMinute,
				BaseRunner:           r.Pricing.BaseRunner,
				SpeedupVsHosted:      r.Pricing.SpeedupVsHosted,
				RateCard:             make([]rateCardRate, 0, len(r.Pricing.RateCard)),
				PricingURL:           r.Pricing.PricingURL,
			},
			WebURL: r.WebURL,
		}
		for _, g := range r.Breakdown {
			f := g.platformUsageFigures.out()
			out.Breakdown = append(out.Breakdown, runnerUsageGroup{
				RunnerType:          g.RunnerType,
				Arch:                g.Arch,
				CiProviderType:      g.CiProviderType,
				Jobs:                f.Jobs,
				BillableMinutes:     f.BillableMinutes,
				CostUsd:             f.CostUsd,
				FreeMinutesUsed:     f.FreeMinutesUsed,
				FreeMinutesValueUsd: f.FreeMinutesValueUsd,
				NetCostUsd:          f.NetCostUsd,
				GithubListPriceUsd:  f.GithubListPriceUsd,
				SavingsPercent:      f.SavingsPercent,
				P50DurationSeconds:  f.P50DurationSeconds,
				P90DurationSeconds:  f.P90DurationSeconds,
				P50QueueSeconds:     f.P50QueueSeconds,
			})
		}
		for _, rc := range r.Pricing.RateCard {
			out.Pricing.RateCard = append(out.Pricing.RateCard, rateCardRate{
				RunnerType:             rc.RunnerType,
				Arch:                   rc.Arch,
				VCPU:                   rc.VCPU,
				MemoryGi:               rc.MemoryGi,
				BuildpulseUsdPerMinute: rc.BuildpulseUsdPerMinute,
				GithubUsdPerMinute:     rc.GithubUsdPerMinute,
			})
		}
		return nil, out, nil
	}
}

// --- get_runner_configs ------------------------------------------------------

type runnerConfigsInput struct {
	OrganizationID string `json:"organization_id,omitempty" jsonschema:"organization UUID (the id field from list_my_organizations). Required for multi-tenant users; ignored for single-org tokens."`
}

// runnerProviderConfig is the non-secret half of a CircleCI / Semaphore
// config: what the pipeline points at. Tokens never reach platform-api's
// response, so there is nothing to redact here.
type runnerProviderConfig struct {
	ResourceClass string `json:"resource_class,omitempty" jsonschema:"CircleCI resource class, namespace/name"`
	Endpoint      string `json:"endpoint,omitempty" jsonschema:"Semaphore organisation host"`
	AgentType     string `json:"agent_type,omitempty" jsonschema:"Semaphore agent type"`
}

type runnerConfigOut struct {
	Label            string                `json:"label" jsonschema:"what a workflow targets, e.g. the GitHub Actions runs-on value bp-ubuntu-latest-x64-2x"`
	RunnerType       string                `json:"runner_type"`
	CiProviderType   string                `json:"ci_provider_type" jsonschema:"github_actions | circle_ci | semaphore_ci"`
	Arch             string                `json:"arch"`
	VCPU             int                   `json:"vcpu"`
	MemoryGi         float64               `json:"memory_gi"`
	CostPerMinuteUsd float64               `json:"cost_per_minute_usd"`
	Status           string                `json:"status" jsonschema:"creating | ready | error"`
	Variant          string                `json:"variant,omitempty"`
	ProviderConfig   *runnerProviderConfig `json:"provider_config,omitempty" jsonschema:"CircleCI / Semaphore only; never a token"`
}

type runnerConfigsOutput struct {
	Runners []runnerConfigOut `json:"runners"`
	Count   int               `json:"count"`
	WebURL  string            `json:"web_url" jsonschema:"the Runners setup page, where sizes are added"`
}

func getRunnerConfigs(c *Client) mcp.ToolHandlerFor[runnerConfigsInput, runnerConfigsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in runnerConfigsInput) (*mcp.CallToolResult, runnerConfigsOutput, error) {
		orgID, err := resolveOrgID(ctx, c, in.OrganizationID)
		if err != nil {
			return nil, runnerConfigsOutput{}, err
		}
		params := url.Values{}
		addOrgParam(params, orgID)

		var r struct {
			Runners []struct {
				Label            string  `json:"label"`
				RunnerType       string  `json:"runnerType"`
				CiProviderType   string  `json:"ciProviderType"`
				Arch             string  `json:"arch"`
				VCPU             int     `json:"vcpu"`
				MemoryGi         float64 `json:"memoryGi"`
				CostPerMinuteUsd float64 `json:"costPerMinuteUsd"`
				Status           string  `json:"status"`
				Variant          string  `json:"variant"`
				ProviderConfig   *struct {
					ResourceClass string `json:"resourceClass"`
					Endpoint      string `json:"endpoint"`
					AgentType     string `json:"agentType"`
				} `json:"providerConfig"`
			} `json:"runners"`
			WebURL string `json:"web_url"`
		}
		if err := c.GetJSON(ctx, "/api/v1/runners/configs", params, &r); err != nil {
			return nil, runnerConfigsOutput{}, err
		}
		out := runnerConfigsOutput{Runners: make([]runnerConfigOut, 0, len(r.Runners)), WebURL: r.WebURL}
		for _, x := range r.Runners {
			var pc *runnerProviderConfig
			if x.ProviderConfig != nil {
				pc = &runnerProviderConfig{ResourceClass: x.ProviderConfig.ResourceClass, Endpoint: x.ProviderConfig.Endpoint, AgentType: x.ProviderConfig.AgentType}
			}
			out.Runners = append(out.Runners, runnerConfigOut{
				Label:            x.Label,
				RunnerType:       x.RunnerType,
				CiProviderType:   x.CiProviderType,
				Arch:             x.Arch,
				VCPU:             x.VCPU,
				MemoryGi:         x.MemoryGi,
				CostPerMinuteUsd: x.CostPerMinuteUsd,
				Status:           x.Status,
				Variant:          x.Variant,
				ProviderConfig:   pc,
			})
		}
		out.Count = len(out.Runners)
		return nil, out, nil
	}
}
