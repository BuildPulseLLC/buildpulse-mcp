package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/BuildPulseLLC/buildpulse-mcp/internal/mcpserver"
)

// passthrough records whether the wrapped handler was reached.
func passthrough(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

// TestUnauthenticatedMCPRequestGetsChallenge is the regression test for the
// broken connect flow. The property is not "a 401 happens" — it is that an
// MCP client with no credentials is told, in the one way the spec defines,
// where to go and authenticate. Returning the SDK's bare 400 fails this.
func TestUnauthenticatedMCPRequestGetsChallenge(t *testing.T) {
	var reached bool
	h := requireBearer(passthrough(&reached))

	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (a 400 gives the client nothing to act on)", w.Code)
	}
	if reached {
		t.Error("unauthenticated request reached the MCP handler")
	}
	chal := w.Header().Get("WWW-Authenticate")
	if chal == "" {
		t.Fatal("no WWW-Authenticate header; the client cannot discover it must authenticate")
	}
	if !strings.HasPrefix(chal, "Bearer ") {
		t.Errorf("challenge = %q, want a Bearer challenge", chal)
	}
	if !strings.Contains(chal, "resource_metadata=") {
		t.Errorf("challenge = %q, want resource_metadata (RFC 9728)", chal)
	}
}

// The URL advertised in the challenge must be the same path the server
// actually serves its protected-resource metadata on. This is what catches
// drift between the challenge and the route if either is edited later.
func TestChallengePointsAtServedMetadataPath(t *testing.T) {
	var reached bool
	h := requireBearer(passthrough(&reached))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/mcp", nil))

	chal := w.Header().Get("WWW-Authenticate")
	if !strings.Contains(chal, wellKnownProtectedResrc) {
		t.Errorf("challenge = %q, want it to reference the served path %q", chal, wellKnownProtectedResrc)
	}
}

func TestChallengeUsesConfiguredIssuer(t *testing.T) {
	t.Setenv(envIssuer, "https://mcp.dev.buildpulse.io")
	var reached bool
	h := requireBearer(passthrough(&reached))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/mcp", nil))

	want := `resource_metadata="https://mcp.dev.buildpulse.io` + wellKnownProtectedResrc + `"`
	if got := w.Header().Get("WWW-Authenticate"); !strings.Contains(got, want) {
		t.Errorf("challenge = %q, want it to contain %q", got, want)
	}
}

// A trailing slash on MCP_ISSUER must not produce a double slash in the
// advertised metadata URL.
func TestChallengeNormalisesIssuerTrailingSlash(t *testing.T) {
	t.Setenv(envIssuer, "https://mcp.buildpulse.io/")
	var reached bool
	h := requireBearer(passthrough(&reached))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/mcp", nil))

	if got := w.Header().Get("WWW-Authenticate"); strings.Contains(got, "io//") {
		t.Errorf("challenge = %q, has a double slash", got)
	}
}

func TestAuthenticatedMCPRequestPassesThrough(t *testing.T) {
	var reached bool
	h := requireBearer(passthrough(&reached))

	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Authorization", "Bearer bp_"+strings.Repeat("a", 64))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if !reached {
		t.Error("authenticated request did not reach the MCP handler")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Header().Get("WWW-Authenticate") != "" {
		t.Error("authenticated request should not be challenged")
	}
}

func TestMalformedAuthorizationIsChallenged(t *testing.T) {
	for _, hdr := range []string{"Bearer", "Basic dXNlcjpwdw==", "Bearer ", "notascheme token"} {
		var reached bool
		h := requireBearer(passthrough(&reached))
		req := httptest.NewRequest("POST", "/mcp", nil)
		req.Header.Set("Authorization", hdr)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status = %d, want 401", hdr, w.Code)
		}
		if w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("Authorization %q: missing challenge", hdr)
		}
	}
}

// A browser-based MCP client can only read the challenge if it is exposed.
func TestCORSExposesChallengeHeader(t *testing.T) {
	h := withCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("Origin", "https://claude.ai")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if !strings.Contains(w.Header().Get("Access-Control-Expose-Headers"), "WWW-Authenticate") {
		t.Errorf("Expose-Headers = %q, want WWW-Authenticate included",
			w.Header().Get("Access-Control-Expose-Headers"))
	}
}

// CORS preflight must not be challenged, or browser clients never get to send
// the real request.
func TestPreflightIsNotChallenged(t *testing.T) {
	h := withCORS(requireBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	req := httptest.NewRequest("OPTIONS", "/mcp", nil)
	req.Header.Set("Origin", "https://claude.ai")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", w.Code)
	}
}

// --- the wired routing table ---------------------------------------------
//
// The tests above exercise requireBearer directly, which proves the wrapper
// behaves — but not that /mcp is actually behind it. These go through
// newHandler, the same table main() serves, so re-registering the raw SDK
// handler on /mcp fails here.

func testHandler() http.Handler {
	return newHandler(serverDeps{
		platformURL: mcpserver.DefaultPlatformURL,
		hostname:    "test-task",
		oauth:       newOAuthServer(newMemoryStore(), plaintextCrypter{}),
	})
}

func TestWiredMCPRoutesAreGuarded(t *testing.T) {
	h := testHandler()
	for _, path := range []string{"/mcp", "/mcp/"} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("POST %s: status = %d, want 401 — is the route still behind requireBearer?", path, w.Code)
		}
		if w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("POST %s: no challenge on the wired route", path)
		}
	}
}

// The guard must cover the MCP endpoint and nothing else: discovery and the
// OAuth endpoints have to stay reachable without credentials, or a client can
// never bootstrap.
func TestWiredPublicRoutesAreNotGuarded(t *testing.T) {
	h := testHandler()
	for _, path := range []string{
		healthPath,
		wellKnownMCP,
		wellKnownOAuth,
		wellKnownProtectedResrc,
		wellKnownProtectedRsrcMCP,
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))

		if w.Code == http.StatusUnauthorized {
			t.Errorf("GET %s: unexpectedly challenged; discovery must be reachable unauthenticated", path)
		}
		if w.Header().Get("WWW-Authenticate") != "" {
			t.Errorf("GET %s: should not carry a challenge", path)
		}
	}
}

// The strongest form of the invariant: whatever URL the challenge advertises
// must actually be served by this same handler. A challenge pointing at a 404
// is worse than no challenge, because the client follows it and dead-ends.
func TestChallengeURLIsServedByTheSameHandler(t *testing.T) {
	h := testHandler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/mcp", nil))
	chal := w.Header().Get("WWW-Authenticate")

	const marker = `resource_metadata="`
	i := strings.Index(chal, marker)
	if i < 0 {
		t.Fatalf("challenge %q has no resource_metadata", chal)
	}
	rest := chal[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("challenge %q has an unterminated resource_metadata", chal)
	}
	advertised := rest[:j]

	u, err := url.Parse(advertised)
	if err != nil {
		t.Fatalf("advertised metadata URL %q does not parse: %v", advertised, err)
	}

	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest("GET", u.Path, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("GET %s (advertised in the challenge) = %d, want 200", u.Path, w2.Code)
	}

	var doc struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &doc); err != nil {
		t.Fatalf("advertised metadata is not JSON: %v", err)
	}
	if doc.Resource == "" || len(doc.AuthorizationServers) == 0 {
		t.Errorf("metadata at %s is missing resource/authorization_servers: %s", u.Path, w2.Body.String())
	}
}
