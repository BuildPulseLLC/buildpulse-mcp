package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
