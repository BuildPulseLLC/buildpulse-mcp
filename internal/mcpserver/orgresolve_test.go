package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// orgStub builds a platform-api stand-in that serves a fixed
// /api/me/organizations body and echoes back the organization_id it received
// on the flaky-tests endpoint (so tests can assert what MCP forwarded).
func orgStub(t *testing.T, orgsJSON string) (*httptest.Server, *string) {
	t.Helper()
	var lastFlakyOrg string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/me/organizations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(orgsJSON))
	})
	mux.HandleFunc("/api/v1/flaky/tests", func(w http.ResponseWriter, r *http.Request) {
		lastFlakyOrg = r.URL.Query().Get("organization_id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"count":0,"tests":[]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &lastFlakyOrg
}

func TestResolveOrgID_ExplicitOrg_NoLookup(t *testing.T) {
	// A supplied organization_id is returned verbatim and must NOT trigger a
	// /api/me/organizations lookup.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/me/organizations" {
			called = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	got, err := resolveOrgID(context.Background(), NewClient(srv.URL, "tok"), "  org-abc  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "org-abc" {
		t.Errorf("got %q, want trimmed %q", got, "org-abc")
	}
	if called {
		t.Error("must not call /api/me/organizations when an org is supplied")
	}
}

func TestResolveOrgID_SingleOrg_AutoDefaults(t *testing.T) {
	srv, _ := orgStub(t, `{"organizations":[{"id":"only-1","name":"Acme"}]}`)
	got, err := resolveOrgID(context.Background(), NewClient(srv.URL, "tok"), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("single-org must return \"\" to let platform-api auto-scope, got %q", got)
	}
}

func TestResolveOrgID_MultiOrg_ErrorsWithRoster(t *testing.T) {
	srv, _ := orgStub(t, `{"organizations":[`+
		`{"id":"uuid-alpha","name":"Alpha Inc","slug":"alpha"},`+
		`{"id":"uuid-beta","name":"Beta LLC"}]}`)
	_, err := resolveOrgID(context.Background(), NewClient(srv.URL, "tok"), "")
	if err == nil {
		t.Fatal("multi-org with no organization_id must error")
	}
	for _, want := range []string{
		"organization_id", "uuid-alpha", "Alpha Inc", "(alpha)", "uuid-beta", "Beta LLC",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q in:\n%s", want, err.Error())
		}
	}
}

// End-to-end: a multi-org session calling a repo-scoped tool WITHOUT
// organization_id gets a clear error naming the orgs — never a silent default.
func TestTool_MultiOrg_NoOrgID_ErrorsListingOrgs(t *testing.T) {
	srv, lastFlakyOrg := orgStub(t, `{"organizations":[`+
		`{"id":"uuid-alpha","name":"Alpha Inc"},`+
		`{"id":"uuid-beta","name":"Beta LLC"}]}`)

	cs := newTestServerSession(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "find_flaky_tests",
		Arguments: map[string]any{"repository": "widgets"},
	})
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("multi-org ambiguity must be an error result, got success: %s", resultText(res))
	}
	text := resultText(res)
	for _, want := range []string{"organization_id", "uuid-alpha", "uuid-beta"} {
		if !strings.Contains(text, want) {
			t.Errorf("error text missing %q in:\n%s", want, text)
		}
	}
	if *lastFlakyOrg != "" {
		t.Errorf("flaky endpoint must NOT be called for ambiguous multi-org; got org=%q", *lastFlakyOrg)
	}
}

// End-to-end: a multi-org session that DOES pass organization_id forwards it
// and succeeds — no roster error.
func TestTool_MultiOrg_WithOrgID_ForwardsAndSucceeds(t *testing.T) {
	srv, lastFlakyOrg := orgStub(t, `{"organizations":[`+
		`{"id":"uuid-alpha","name":"Alpha Inc"},`+
		`{"id":"uuid-beta","name":"Beta LLC"}]}`)

	cs := newTestServerSession(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "find_flaky_tests",
		Arguments: map[string]any{"repository": "widgets", "organization_id": "uuid-beta"},
	})
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if res.IsError {
		t.Fatalf("explicit org must succeed, got error: %s", resultText(res))
	}
	if *lastFlakyOrg != "uuid-beta" {
		t.Errorf("organization_id not forwarded to platform-api; got %q want uuid-beta", *lastFlakyOrg)
	}
}

// End-to-end: a single-org session omits organization_id and just works, with
// the flaky endpoint receiving no organization_id (platform-api auto-scopes).
func TestTool_SingleOrg_NoOrgID_Unaffected(t *testing.T) {
	srv, lastFlakyOrg := orgStub(t, `{"organizations":[{"id":"only-1","name":"Acme"}]}`)

	cs := newTestServerSession(t, srv)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "find_flaky_tests",
		Arguments: map[string]any{"repository": "widgets"},
	})
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if res.IsError {
		t.Fatalf("single-org path must not error: %s", resultText(res))
	}
	if *lastFlakyOrg != "" {
		t.Errorf("single-org should forward no organization_id, got %q", *lastFlakyOrg)
	}
}
