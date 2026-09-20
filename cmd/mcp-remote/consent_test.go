package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// --- helpers --------------------------------------------------------------

// registerClient drives the real /oauth/register endpoint and returns the
// issued client_id. Using the endpoint (rather than seeding the store)
// keeps these tests honest about what an unauthenticated caller can do.
func registerClient(t *testing.T, s *oauthServer, name, redirectURI string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"client_name": name, "redirect_uris": []string{redirectURI}})
	req := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	s.register(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	return out.ClientID
}

// authorizeAndGetState runs /oauth/authorize and digs the internal state out
// of the Cognito redirect, which is what /oauth/callback keys on.
func authorizeAndGetState(t *testing.T, s *oauthServer, clientID, redirectURI, verifier string) string {
	t.Helper()
	q := url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"response_type":         {"code"},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"state":                 {"client-state"},
	}
	req := httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	s.authorize(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302; body = %s", w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse cognito redirect: %v", err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("no internal state on the cognito redirect")
	}
	return state
}

// callbackFor replays Cognito's redirect back into /oauth/callback.
func callbackFor(s *oauthServer, state string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/oauth/callback?code=cognito-code&state="+state, nil)
	w := httptest.NewRecorder()
	s.callback(w, req)
	return w
}

func postConsent(s *oauthServer, key, action string) *httptest.ResponseRecorder {
	form := url.Values{"consent_key": {key}, "action": {action}}
	req := httptest.NewRequest("POST", "/oauth/consent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.consent(w, req)
	return w
}

// consentKeyFrom scrapes the single-use key out of the rendered form.
func consentKeyFrom(t *testing.T, body string) string {
	t.Helper()
	const marker = `name="consent_key" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatal("no consent_key in rendered page")
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("malformed consent_key field")
	}
	return rest[:j]
}

// attackerSetup performs the whole hostile-client setup: an unauthenticated
// party registers a client pointing at infrastructure it controls, then a
// victim with a live Cognito session is walked through /authorize.
func attackerSetup(t *testing.T) (*oauthServer, *memoryStore, string) {
	t.Helper()
	s, mem := newTestServer()
	cog := stubCognito(t, 200, "victim-sub", "victim@customer.com")
	t.Cleanup(cog.Close)
	s.cognitoDomain = cog.URL
	s.cognitoClientID = "cid"

	clientID := registerClient(t, s, "BuildPulse Official", "https://attacker.example.com/cb")
	state := authorizeAndGetState(t, s, clientID, "https://attacker.example.com/cb", "verifier-123")
	return s, mem, state
}

// --- the invariant --------------------------------------------------------

// TestCallbackNeverIssuesCodeWithoutConsent is the regression test for the
// disclosed flaw. The property under test is not "a consent page renders" —
// it is that completing the Cognito hop hands the client NOTHING. Delete the
// consent interstitial from callback() and this test fails on the Location
// header, which is exactly the bug that was reported.
func TestCallbackNeverIssuesCodeWithoutConsent(t *testing.T) {
	s, _, state := attackerSetup(t)

	w := callbackFor(s, state)

	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("callback redirected without consent to %q; it must render an interstitial instead", loc)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("callback status = %d, want 200 (consent page); body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Allow access") || !strings.Contains(body, "attacker.example.com") {
		t.Error("consent page must name the destination and offer an explicit choice")
	}
	// The page has to tell the user this client vouched for itself.
	if !strings.Contains(body, "not") || !strings.Contains(strings.ToLower(body), "verified") {
		t.Error("consent page must warn that the client is unverified")
	}
}

func TestConsentApproveIssuesCodeToClient(t *testing.T) {
	s, _, state := attackerSetup(t)
	key := consentKeyFrom(t, callbackFor(s, state).Body.String())

	w := postConsent(s, key, "approve")
	if w.Code != http.StatusFound {
		t.Fatalf("approve status = %d, want 302; body = %s", w.Code, w.Body.String())
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatal("approve must deliver an authorization code")
	}
	if got := loc.Query().Get("state"); got != "client-state" {
		t.Errorf("state = %q, want the client's original state", got)
	}
	// And that code must be real — redeemable at /token.
	if _, err := s.store.PopCode(context.Background(), code); err != nil {
		t.Errorf("issued code was not persisted: %v", err)
	}
}

func TestConsentDenyIssuesNoCode(t *testing.T) {
	s, _, state := attackerSetup(t)
	key := consentKeyFrom(t, callbackFor(s, state).Body.String())

	w := postConsent(s, key, "deny")
	if w.Code != http.StatusFound {
		t.Fatalf("deny status = %d, want 302", w.Code)
	}
	loc, _ := url.Parse(w.Header().Get("Location"))
	if loc.Query().Get("code") != "" {
		t.Error("deny must not deliver an authorization code")
	}
	if got := loc.Query().Get("error"); got != "access_denied" {
		t.Errorf("error = %q, want access_denied", got)
	}
}

// A consent grant is single-use: replaying the form must not mint a second
// code for the same interactive login.
func TestConsentKeyIsSingleUse(t *testing.T) {
	s, _, state := attackerSetup(t)
	key := consentKeyFrom(t, callbackFor(s, state).Body.String())

	if w := postConsent(s, key, "approve"); w.Code != http.StatusFound {
		t.Fatalf("first approve status = %d, want 302", w.Code)
	}
	w := postConsent(s, key, "approve")
	if w.Code == http.StatusFound {
		t.Fatal("replayed consent_key must not mint a second code")
	}
}

func TestConsentRejectsUnknownKey(t *testing.T) {
	s, _, _ := attackerSetup(t)
	if w := postConsent(s, "not-a-real-key", "approve"); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// The client_name is attacker-controlled and rendered into HTML, so it must
// be escaped rather than reflected.
func TestConsentPageEscapesHostileClientName(t *testing.T) {
	s, _ := newTestServer()
	cog := stubCognito(t, 200, "victim-sub", "victim@customer.com")
	defer cog.Close()
	s.cognitoDomain = cog.URL
	s.cognitoClientID = "cid"

	const xss = `<script>alert(1)</script>`
	clientID := registerClient(t, s, xss, "https://attacker.example.com/cb")
	state := authorizeAndGetState(t, s, clientID, "https://attacker.example.com/cb", "v")

	body := callbackFor(s, state).Body.String()
	if strings.Contains(body, xss) {
		t.Error("client_name was reflected unescaped into the consent page")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("expected the hostile client_name to appear HTML-escaped")
	}
}

func TestConsentPageIsFrameDenied(t *testing.T) {
	s, _, state := attackerSetup(t)
	h := callbackFor(s, state).Header()
	if h.Get("X-Frame-Options") != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", h.Get("X-Frame-Options"))
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Errorf("CSP = %q, want frame-ancestors 'none'", h.Get("Content-Security-Policy"))
	}
}

// --- registration guardrails ---------------------------------------------

func TestValidateRedirectURI(t *testing.T) {
	ok := []string{
		"https://claude.ai/cb",
		"https://app.example.com/oauth/callback?tenant=1",
		"http://localhost:59988/callback",
		"http://127.0.0.1:1234/cb",
		"http://[::1]:8080/cb",
		"com.example.app:/oauth2redirect",
	}
	for _, u := range ok {
		if err := validateRedirectURI(u); err != nil {
			t.Errorf("validateRedirectURI(%q) = %v, want nil", u, err)
		}
	}

	bad := []string{
		"",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"file:///etc/passwd",
		"http://attacker.example.com/cb", // plaintext to a public host
		"https://example.com/cb#frag",    // fragment
		"/relative/only",
		"notaurl",
	}
	for _, u := range bad {
		if err := validateRedirectURI(u); err == nil {
			t.Errorf("validateRedirectURI(%q) = nil, want an error", u)
		}
	}
}

func TestRegisterRejectsDangerousRedirectURI(t *testing.T) {
	s, _ := newTestServer()
	body := `{"client_name":"x","redirect_uris":["javascript:alert(1)"]}`
	req := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.register(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestRegisterRejectsTooManyRedirectURIs(t *testing.T) {
	s, _ := newTestServer()
	uris := make([]string, maxRedirectURIs+1)
	for i := range uris {
		uris[i] = "https://example.com/cb"
	}
	body, _ := json.Marshal(map[string]any{"client_name": "x", "redirect_uris": uris})
	req := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	s.register(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestRegisterIsRateLimitedPerIP(t *testing.T) {
	s, _ := newTestServer()
	body := `{"client_name":"x","redirect_uris":["https://example.com/cb"]}`

	post := func(ip string) int {
		req := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(body))
		req.Header.Set("X-Forwarded-For", "10.0.0.1, "+ip)
		w := httptest.NewRecorder()
		s.register(w, req)
		return w.Code
	}

	for i := 0; i < registerRateLimit; i++ {
		if got := post("203.0.113.9"); got != http.StatusCreated {
			t.Fatalf("registration %d status = %d, want 201", i+1, got)
		}
	}
	if got := post("203.0.113.9"); got != http.StatusTooManyRequests {
		t.Errorf("over-limit status = %d, want 429", got)
	}
	// A different source address must be unaffected.
	if got := post("198.51.100.7"); got != http.StatusCreated {
		t.Errorf("other IP status = %d, want 201", got)
	}
}

// The ALB appends the address it actually saw, so a caller cannot shift the
// bucket by spoofing a leading X-Forwarded-For entry.
func TestClientIPUsesRightmostForwardedFor(t *testing.T) {
	req := httptest.NewRequest("POST", "/oauth/register", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8, 203.0.113.9")
	if got := clientIP(req); got != "203.0.113.9" {
		t.Errorf("clientIP = %q, want 203.0.113.9", got)
	}
}

// A replayed approval is the common real-world case: the user approved, the
// redirect went to the client, they hit back and clicked again. They get a
// browser page, not the JSON an API client would want.
func TestReplayedConsentRendersReadablePage(t *testing.T) {
	s, _, state := attackerSetup(t)
	key := consentKeyFrom(t, callbackFor(s, state).Body.String())
	postConsent(s, key, "approve") // consumes it

	w := postConsent(s, key, "approve")
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html for a browser-facing error", ct)
	}
	body := w.Body.String()
	if strings.Contains(body, `"error"`) || strings.Contains(body, "invalid_request") {
		t.Error("replayed consent returned a raw JSON error to a browser")
	}
	if !strings.Contains(body, "already been used") {
		t.Errorf("error page does not explain what happened: %s", body)
	}
}

func TestConsentTTLMatchesPendingWindow(t *testing.T) {
	if consentTTL != 15*time.Minute {
		t.Errorf("consentTTL = %v, want 15m to match the pending table window", consentTTL)
	}
}

// --- destination classification ------------------------------------------

func TestClassifyDestination(t *testing.T) {
	cases := []struct {
		uri        string
		recognised bool
		label      string
	}{
		{"https://antigravity.google/oauth-callback", true, "Google Antigravity"},
		{"https://claude.ai/api/mcp/auth_callback", true, "Claude"},
		{"https://console.cursor.com/cb", true, "Cursor"},
		{"http://localhost:59988/callback", true, "an application running on your own computer"},
		{"http://127.0.0.1:1234/cb", true, "an application running on your own computer"},
		{"https://attacker.example.com/cb", false, ""},
		{"https://antigravity.google.evil.com/cb", false, ""},
		{"https://notclaude.ai/cb", false, ""},
	}
	for _, c := range cases {
		label, ok := classifyDestination(c.uri)
		if ok != c.recognised {
			t.Errorf("classifyDestination(%q) recognised = %v, want %v", c.uri, ok, c.recognised)
		}
		if label != c.label {
			t.Errorf("classifyDestination(%q) label = %q, want %q", c.uri, label, c.label)
		}
	}
}

// The security property that makes the allowlist safe: a hostile client can
// call itself anything, so trust must come from the destination host alone.
// An attacker impersonating a known product by NAME must still be warned on.
func TestImpersonatingClientNameStillWarns(t *testing.T) {
	s, _ := newTestServer()
	cog := stubCognito(t, 200, "victim-sub", "victim@customer.com")
	defer cog.Close()
	s.cognitoDomain = cog.URL
	s.cognitoClientID = "cid"

	// Same name as a trusted product, attacker-controlled destination.
	clientID := registerClient(t, s, "Google Antigravity", "https://attacker.example.com/cb")
	state := authorizeAndGetState(t, s, clientID, "https://attacker.example.com/cb", "v")

	body := callbackFor(s, state).Body.String()
	if !strings.Contains(body, "not</strong> been verified") {
		t.Error("a client impersonating a known product by name must still be warned on")
	}
	if strings.Contains(body, "This looks like") {
		t.Error("attacker-controlled destination was presented as recognised")
	}
}

func TestRecognisedDestinationSuppressesWarning(t *testing.T) {
	s, _ := newTestServer()
	cog := stubCognito(t, 200, "u", "u@example.com")
	defer cog.Close()
	s.cognitoDomain = cog.URL
	s.cognitoClientID = "cid"

	clientID := registerClient(t, s, "Google Antigravity", "https://antigravity.google/oauth-callback")
	state := authorizeAndGetState(t, s, clientID, "https://antigravity.google/oauth-callback", "v")

	body := callbackFor(s, state).Body.String()
	if strings.Contains(body, "not</strong> been verified") {
		t.Error("a recognised destination should not raise the unverified warning")
	}
	if !strings.Contains(body, "Google Antigravity") {
		t.Error("recognised destination should be named")
	}
	// The destination itself must still be shown, recognised or not.
	if !strings.Contains(body, "antigravity.google/oauth-callback") {
		t.Error("destination URI must always be visible")
	}
}

// --- CSP must not block the redirect that carries the code ----------------

// Regression test for a silent, total breakage: `form-action 'self'` is also
// enforced against the redirect that follows the form POST, so the 302
// delivering the authorization code to the client was blocked by the browser
// and Allow appeared to do nothing.
func TestConsentCSPAllowsTheClientRedirect(t *testing.T) {
	cases := []struct{ redirect, wantOrigin string }{
		{"http://localhost:58613/callback", "http://localhost:58613"},
		{"http://127.0.0.1:1234/cb", "http://127.0.0.1:1234"},
		{"https://antigravity.google/oauth-callback", "https://antigravity.google"},
		{"com.example.app:/oauth2redirect", "com.example.app:"},
	}
	for _, c := range cases {
		csp := consentCSP(c.redirect)
		if !strings.Contains(csp, "form-action 'self' "+c.wantOrigin) {
			t.Errorf("consentCSP(%q) = %q, want form-action to allow %q", c.redirect, csp, c.wantOrigin)
		}
	}
}

// The header actually served must permit the destination shown on the page.
func TestRenderedConsentHeaderPermitsDestination(t *testing.T) {
	s, _ := newTestServer()
	cog := stubCognito(t, 200, "u", "u@example.com")
	defer cog.Close()
	s.cognitoDomain = cog.URL
	s.cognitoClientID = "cid"

	const redirect = "http://localhost:58613/callback"
	clientID := registerClient(t, s, "Claude Code", redirect)
	state := authorizeAndGetState(t, s, clientID, redirect, "v")

	csp := callbackFor(s, state).Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "form-action") && !strings.Contains(csp, "http://localhost:58613") {
		t.Errorf("CSP %q restricts form-action without allowing the redirect target", csp)
	}
	// The protections that matter must survive.
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Error("frame-ancestors protection was lost")
	}
}

// A destination we cannot parse must not produce a policy that blocks the
// redirect outright.
func TestConsentCSPOmitsFormActionWhenUnparseable(t *testing.T) {
	csp := consentCSP("::::not a uri")
	if strings.Contains(csp, "form-action") {
		t.Errorf("CSP %q should omit form-action rather than block the flow", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Error("frame-ancestors must still be present")
	}
}
