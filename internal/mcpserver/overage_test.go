package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- client-level 402 detection ---------------------------------------------

func TestGet_PlanLimit402_MappedToTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"code":"plan_limit_exceeded","message":"You've used 1,250,000 of 1,000,000 monthly test results.","upgradeUrl":"https://app.buildpulse.io/settings/billing"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	_, _, err := c.Get(context.Background(), "/api/v1/flaky/tests", nil)

	var pe *PlanLimitError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PlanLimitError, got %T: %v", err, err)
	}
	if pe.Message != "You've used 1,250,000 of 1,000,000 monthly test results." {
		t.Errorf("Message = %q", pe.Message)
	}
	if pe.UpgradeURL != "https://app.buildpulse.io/settings/billing" {
		t.Errorf("UpgradeURL = %q", pe.UpgradeURL)
	}
}

func TestGet_402WithoutPlanLimitCode_IsNormalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"code":"some_other_payment_thing","message":"nope"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	_, _, err := c.Get(context.Background(), "/api/v1/flaky/tests", nil)
	if err == nil {
		t.Fatal("expected an error for 402 without plan_limit_exceeded code")
	}
	var pe *PlanLimitError
	if errors.As(err, &pe) {
		t.Fatalf("a 402 without plan_limit_exceeded must NOT map to PlanLimitError, got %v", err)
	}
	if !strings.Contains(err.Error(), "402") {
		t.Errorf("expected generic 402 error text, got %q", err.Error())
	}
}

func TestGet_Non402_Unaffected(t *testing.T) {
	// A normal success and a 500 must behave exactly as before.
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer okSrv.Close()
	c := NewClient(okSrv.URL, "tok")
	body, _, err := c.Get(context.Background(), "/api/me/organizations", nil)
	if err != nil {
		t.Fatalf("unexpected error on 200: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q", body)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))
	defer errSrv.Close()
	c2 := NewClient(errSrv.URL, "tok")
	_, _, err = c2.Get(context.Background(), "/api/v1/flaky/tests", nil)
	if err == nil {
		t.Fatal("expected error on 500")
	}
	var pe *PlanLimitError
	if errors.As(err, &pe) {
		t.Fatalf("500 must not map to PlanLimitError, got %v", err)
	}
}

// --- planLimitText formatting -----------------------------------------------

func TestPlanLimitText(t *testing.T) {
	got := planLimitText(&PlanLimitError{
		Message:    "You are over your plan's monthly test-result limit.",
		UpgradeURL: "https://app.buildpulse.io/settings/billing",
	})
	for _, want := range []string{
		"over your plan's monthly test-result limit",
		"still recording every test result",
		"https://app.buildpulse.io/settings/billing",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing %q in:\n%s", want, got)
		}
	}

	// Empty message + URL still yields a sensible default with no dangling label.
	def := planLimitText(&PlanLimitError{})
	if !strings.Contains(def, "monthly test-result limit") {
		t.Errorf("default message missing limit phrasing: %q", def)
	}
	if strings.Contains(def, "Upgrade your plan to restore access:") {
		t.Errorf("must not emit upgrade label when no URL: %q", def)
	}
}

// --- end-to-end: 402 becomes a friendly, non-error tool result --------------

// newTestServerSession wires a real mcpserver (with all tools registered and
// overage-wrapped) against a platform-api stub, and returns a connected MCP
// client session for invoking tools end-to-end.
func newTestServerSession(t *testing.T, platform *httptest.Server) *mcp.ClientSession {
	t.Helper()
	srv := New(NewClient(platform.URL, "tok"))
	clientT, serverT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cli := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	cs, err := cli.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func TestTool_Over402_ReturnsFriendlyResult(t *testing.T) {
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"code":"plan_limit_exceeded","message":"You've reached your plan's monthly test-result limit.","upgradeUrl":"https://app.buildpulse.io/settings/billing"}`))
	}))
	defer platform.Close()

	cs := newTestServerSession(t, platform)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "find_flaky_tests",
		Arguments: map[string]any{"repository": "widgets"},
	})
	if err != nil {
		t.Fatalf("CallTool returned a protocol error: %v", err)
	}
	if res.IsError {
		t.Fatalf("over-limit must be a SUCCESSFUL result, got IsError=true: %s", resultText(res))
	}
	text := resultText(res)
	for _, want := range []string{
		"monthly test-result limit",
		"still recording every test result",
		"https://app.buildpulse.io/settings/billing",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("friendly text missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(strings.ToLower(text), "402") || strings.Contains(text, "platform API returned") {
		t.Errorf("must not leak raw HTTP/402 detail: %s", text)
	}
}

func TestTool_NormalError_StillErrors(t *testing.T) {
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))
	defer platform.Close()

	cs := newTestServerSession(t, platform)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "find_flaky_tests",
		Arguments: map[string]any{"repository": "widgets"},
	})
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("a non-402 failure must remain an error result, got success: %s", resultText(res))
	}
}

func TestTool_Success_Unaffected(t *testing.T) {
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":1,"tests":[{"id":"abc","name":"t"}]}`))
	}))
	defer platform.Close()

	cs := newTestServerSession(t, platform)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "find_flaky_tests",
		Arguments: map[string]any{"repository": "widgets"},
	})
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if res.IsError {
		t.Fatalf("success path must not be an error: %s", resultText(res))
	}
	if !strings.Contains(resultText(res), `"name":"t"`) {
		t.Errorf("expected normal structured output, got: %s", resultText(res))
	}
}
