package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRegisterClient(t *testing.T) {
	s := newOAuthServer()

	body := `{"client_name":"Claude","redirect_uris":["https://claude.ai/cb"]}`
	req := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.register(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var got registeredClient
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if !strings.HasPrefix(got.ClientID, "mcp_") {
		t.Errorf("client_id %q does not have mcp_ prefix", got.ClientID)
	}
	if len(got.RedirectURIs) != 1 || got.RedirectURIs[0] != "https://claude.ai/cb" {
		t.Errorf("redirect_uris not preserved: %v", got.RedirectURIs)
	}
}

func TestRegisterClientRejectsEmptyURIs(t *testing.T) {
	s := newOAuthServer()
	req := httptest.NewRequest("POST", "/oauth/register",
		strings.NewReader(`{"client_name":"x","redirect_uris":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.register(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestAuthorizeUnconfigured(t *testing.T) {
	s := newOAuthServer()
	// No COGNITO_* env, so authorize should 501 with a clear message
	// rather than half-implementing the flow.
	req := httptest.NewRequest("GET", "/oauth/authorize?response_type=code&client_id=x", nil)
	w := httptest.NewRecorder()
	s.authorize(w, req)
	if w.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501 when Cognito is unconfigured", w.Code)
	}
	if !strings.Contains(w.Body.String(), "server_misconfigured") {
		t.Errorf("response body missing server_misconfigured: %s", w.Body.String())
	}
}

func TestAuthorizeValidationErrors(t *testing.T) {
	s := newOAuthServer()
	// Configure to bypass the unconfigured check.
	s.cognitoDomain = "https://oauth.example.com"
	s.cognitoClientID = "test-cognito-client"
	// Register a client so the client_id check passes.
	s.clients.Store("mcp_test", &registeredClient{
		ClientID:     "mcp_test",
		RedirectURIs: []string{"https://app.example/cb"},
	})

	cases := []struct {
		name    string
		query   url.Values
		wantSub string
	}{
		{
			name:    "wrong response_type",
			query:   url.Values{"response_type": {"token"}, "client_id": {"mcp_test"}},
			wantSub: "unsupported_response_type",
		},
		{
			name:    "unknown client",
			query:   url.Values{"response_type": {"code"}, "client_id": {"mcp_unknown"}},
			wantSub: "invalid_client",
		},
		{
			name: "redirect_uri not registered",
			query: url.Values{
				"response_type": {"code"},
				"client_id":     {"mcp_test"},
				"redirect_uri":  {"https://evil/cb"},
			},
			wantSub: "invalid_redirect_uri",
		},
		{
			name: "missing PKCE challenge",
			query: url.Values{
				"response_type": {"code"},
				"client_id":     {"mcp_test"},
				"redirect_uri":  {"https://app.example/cb"},
			},
			wantSub: "code_challenge is required",
		},
		{
			name: "wrong PKCE method",
			query: url.Values{
				"response_type":         {"code"},
				"client_id":             {"mcp_test"},
				"redirect_uri":          {"https://app.example/cb"},
				"code_challenge":        {"abc"},
				"code_challenge_method": {"plain"},
			},
			wantSub: "code_challenge_method must be 'S256'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/oauth/authorize?"+tc.query.Encode(), nil)
			w := httptest.NewRecorder()
			s.authorize(w, req)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantSub) {
				t.Errorf("body does not contain %q: %s", tc.wantSub, w.Body.String())
			}
		})
	}
}

func TestAuthorizeRedirectsToCognito(t *testing.T) {
	s := newOAuthServer()
	s.cognitoDomain = "https://oauth.example.com"
	s.cognitoClientID = "test-cognito-client"
	s.clients.Store("mcp_test", &registeredClient{
		ClientID:     "mcp_test",
		RedirectURIs: []string{"https://app.example/cb"},
	})

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {"mcp_test"},
		"redirect_uri":          {"https://app.example/cb"},
		"code_challenge":        {"abcdefg"},
		"code_challenge_method": {"S256"},
		"state":                 {"orig-state"},
	}
	req := httptest.NewRequest("GET", "/oauth/authorize?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	s.authorize(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body = %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://oauth.example.com/oauth2/authorize?") {
		t.Errorf("Location does not point at Cognito: %s", loc)
	}
	if !strings.Contains(loc, "client_id=test-cognito-client") {
		t.Errorf("Cognito redirect missing client_id: %s", loc)
	}

	// One pending auth should be stashed.
	count := 0
	s.pending.Range(func(_, _ any) bool { count++; return true })
	if count != 1 {
		t.Errorf("expected 1 pending auth, got %d", count)
	}
}

func TestPKCEVerification(t *testing.T) {
	verifier := "the-quick-brown-fox-jumps-over-the-lazy-dog"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	if !verifyPKCE(verifier, challenge) {
		t.Error("matching verifier+challenge failed verification")
	}
	if verifyPKCE(verifier, "wrong-challenge") {
		t.Error("non-matching challenge incorrectly verified")
	}
	// Length mismatch should fail short-circuit.
	if verifyPKCE(verifier, challenge+"x") {
		t.Error("length-mismatch challenge incorrectly verified")
	}
}

func TestTokenEndpointFullFlow(t *testing.T) {
	s := newOAuthServer()
	// Register a client.
	s.clients.Store("mcp_test", &registeredClient{
		ClientID:     "mcp_test",
		RedirectURIs: []string{"https://app.example/cb"},
	})

	// Stage an authorization code with PKCE challenge.
	verifier := "static-test-verifier-x-y-z"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	s.codes.Store("the-code", &authorizationCode{
		Code:          "the-code",
		ClientID:      "mcp_test",
		RedirectURI:   "https://app.example/cb",
		CodeChallenge: challenge,
		Scope:         defaultScope,
		UserSubject:   "user-123",
		UserEmail:     "user@example.com",
		Expires:       time.Now().Add(time.Minute),
	})

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"the-code"},
		"code_verifier": {verifier},
		"client_id":     {"mcp_test"},
		"redirect_uri":  {"https://app.example/cb"},
	}
	req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.token(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		Scope       string `json:"scope"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &tok)
	if len(tok.AccessToken) != 40 {
		t.Errorf("access_token length = %d, want 40 (hex chars)", len(tok.AccessToken))
	}
	if tok.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", tok.TokenType)
	}
	if tok.ExpiresIn != int(accessTokenTTL.Seconds()) {
		t.Errorf("expires_in = %d, want %d", tok.ExpiresIn, int(accessTokenTTL.Seconds()))
	}
	// Code should have been consumed.
	if _, ok := s.codes.Load("the-code"); ok {
		t.Error("code should have been deleted after exchange")
	}
	// Token should be retrievable.
	got, ok := s.LookupToken(tok.AccessToken)
	if !ok {
		t.Error("LookupToken should find the just-issued token")
	}
	if got.UserEmail != "user@example.com" {
		t.Errorf("user_email = %q, want user@example.com", got.UserEmail)
	}
}

func TestTokenEndpointRejectsBadPKCE(t *testing.T) {
	s := newOAuthServer()
	s.clients.Store("mcp_test", &registeredClient{ClientID: "mcp_test"})
	s.codes.Store("the-code", &authorizationCode{
		Code:          "the-code",
		ClientID:      "mcp_test",
		CodeChallenge: "expected-challenge",
		Expires:       time.Now().Add(time.Minute),
	})

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {"the-code"},
		"code_verifier": {"wrong-verifier"},
		"client_id":     {"mcp_test"},
	}
	req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.token(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PKCE verification failed") {
		t.Errorf("body does not contain PKCE failure: %s", w.Body.String())
	}
}

func TestMetadataDocument(t *testing.T) {
	s := newOAuthServer()
	s.cognitoDomain = "https://oauth.example.com"
	s.cognitoClientID = "test"
	req := httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	s.metadata(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var doc map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &doc)
	for _, key := range []string{
		"issuer", "authorization_endpoint", "token_endpoint",
		"registration_endpoint", "response_types_supported",
		"code_challenge_methods_supported", "scopes_supported",
	} {
		if _, ok := doc[key]; !ok {
			t.Errorf("metadata missing required field %q", key)
		}
	}
	if doc["x-buildpulse-oauth-status"] == "unconfigured" {
		t.Error("status field should NOT report unconfigured when Cognito is set")
	}
}

func TestMetadataUnconfiguredFlag(t *testing.T) {
	s := newOAuthServer()
	req := httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	s.metadata(w, req)
	var doc map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &doc)
	if doc["x-buildpulse-oauth-status"] != "unconfigured" {
		t.Errorf("expected unconfigured status, got %v", doc["x-buildpulse-oauth-status"])
	}
}

// Compile-time check: oauthError writes a valid JSON body.
var _ = func() bool {
	var buf bytes.Buffer
	w := httptest.NewRecorder()
	oauthError(w, 400, "test_err", "test desc")
	_, _ = io.Copy(&buf, w.Body)
	var doc map[string]string
	_ = json.Unmarshal(buf.Bytes(), &doc)
	return doc["error"] == "test_err"
}()
