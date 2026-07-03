package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// pkceChallenge computes the S256 code_challenge for a verifier.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// fakeIDToken builds an unsigned JWT whose claims segment carries the
// given sub/email — enough for parseIDTokenClaims (which does not verify
// signatures).
func fakeIDToken(sub, email string) string {
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"sub":"` + sub + `","email":"` + email + `"}`))
	return "aGVhZGVy." + payload + ".c2ln"
}

// stubCognito returns an httptest server standing in for Cognito's
// /oauth2/token endpoint. status controls the HTTP status; on 200 it
// returns an id_token for (sub,email).
func stubCognito(t *testing.T, status int, sub, email string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/oauth2/token") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": fakeIDToken(sub, email)})
	}))
}

func seedRefresh(t *testing.T, s *oauthServer, mem *memoryStore, plaintextRefresh, cognitoRefresh string) {
	t.Helper()
	enc, err := s.crypter.Encrypt(context.Background(), cognitoRefresh)
	if err != nil {
		t.Fatalf("seed encrypt: %v", err)
	}
	_ = mem.PutRefresh(context.Background(), &refreshToken{
		HashedToken:       hashTokenB64(plaintextRefresh),
		ClientID:          "mcp_test",
		Scope:             defaultScope,
		UserSubject:       "user-123",
		UserEmail:         "user@example.com",
		OrganizationIDs:   []string{"org-1"},
		CognitoRefreshEnc: enc,
		Expires:           time.Now().Add(time.Hour),
	})
}

func postRefresh(s *oauthServer, refreshTok, clientID string) *httptest.ResponseRecorder {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshTok}, "client_id": {clientID}}
	req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.token(w, req)
	return w
}

func TestRefreshGrantFullFlow(t *testing.T) {
	s, mem := newTestServer()
	cog := stubCognito(t, 200, "user-123", "user@example.com")
	defer cog.Close()
	s.cognitoDomain = cog.URL
	s.cognitoClientID = "cid"
	seedRefresh(t, s, mem, "old-refresh", "upstream-cognito-refresh")

	w := postRefresh(s, "old-refresh", "mcp_test")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		TokenType    string `json:"token_type"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &tok)
	if len(tok.AccessToken) != 40 {
		t.Errorf("access_token length = %d, want 40", len(tok.AccessToken))
	}
	if tok.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", tok.TokenType)
	}
	if tok.RefreshToken == "" || tok.RefreshToken == "old-refresh" {
		t.Errorf("expected a rotated refresh_token, got %q", tok.RefreshToken)
	}
	// Old token must be single-use — already consumed by the grant.
	if _, err := mem.PopRefresh(context.Background(), hashTokenB64("old-refresh")); err != ErrNotFound {
		t.Errorf("old refresh token should be consumed; got err=%v", err)
	}
	// New token must be persisted.
	if _, err := mem.PopRefresh(context.Background(), hashTokenB64(tok.RefreshToken)); err != nil {
		t.Errorf("rotated refresh token not persisted: %v", err)
	}
}

func TestRefreshGrantUnknownToken(t *testing.T) {
	s, _ := newTestServer()
	cog := stubCognito(t, 200, "user-123", "user@example.com")
	defer cog.Close()
	s.cognitoDomain = cog.URL

	w := postRefresh(s, "never-issued", "mcp_test")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid_grant") {
		t.Errorf("body missing invalid_grant: %s", w.Body.String())
	}
}

// The offboarding case: Cognito rejects the upstream refresh (disabled
// user / global sign-out), so we must NOT resurrect the session.
func TestRefreshGrantCognitoRevoked(t *testing.T) {
	s, mem := newTestServer()
	cog := stubCognito(t, 400, "", "")
	defer cog.Close()
	s.cognitoDomain = cog.URL
	s.cognitoClientID = "cid"
	seedRefresh(t, s, mem, "old-refresh", "upstream-cognito-refresh")

	w := postRefresh(s, "old-refresh", "mcp_test")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no longer valid") {
		t.Errorf("body should signal revoked session: %s", w.Body.String())
	}
	// The presented token was consumed (single-use) and no new one issued.
	if _, err := mem.PopRefresh(context.Background(), hashTokenB64("old-refresh")); err != ErrNotFound {
		t.Errorf("revoked-flow token should still be consumed; got err=%v", err)
	}
}

func TestRefreshGrantClientMismatch(t *testing.T) {
	s, mem := newTestServer()
	cog := stubCognito(t, 200, "user-123", "user@example.com")
	defer cog.Close()
	s.cognitoDomain = cog.URL
	s.cognitoClientID = "cid"
	seedRefresh(t, s, mem, "old-refresh", "upstream-cognito-refresh")

	w := postRefresh(s, "old-refresh", "mcp_other")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "different client") {
		t.Errorf("body should signal client mismatch: %s", w.Body.String())
	}
}

// The code grant issues a refresh_token when the auth code carries an
// encrypted upstream Cognito refresh token.
func TestCodeGrantIssuesRefreshToken(t *testing.T) {
	s, mem := newTestServer()
	enc, _ := s.crypter.Encrypt(context.Background(), "upstream-cognito-refresh")

	verifier := "static-test-verifier-abc"
	challenge := pkceChallenge(verifier)
	_ = mem.PutCode(context.Background(), &authorizationCode{
		Code:              "the-code",
		ClientID:          "mcp_test",
		RedirectURI:       "https://app.example/cb",
		CodeChallenge:     challenge,
		Scope:             defaultScope,
		UserSubject:       "user-123",
		UserEmail:         "user@example.com",
		CognitoRefreshEnc: enc,
		Expires:           time.Now().Add(time.Minute),
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
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &tok)
	if tok.RefreshToken == "" {
		t.Fatalf("expected a refresh_token in the code-grant response: %s", w.Body.String())
	}
	if _, err := mem.PopRefresh(context.Background(), hashTokenB64(tok.RefreshToken)); err != nil {
		t.Errorf("issued refresh token not persisted: %v", err)
	}
}

func TestMetadataAdvertisesRefreshGrant(t *testing.T) {
	s, _ := newTestServer()
	s.cognitoDomain = "https://oauth.example.com"
	s.cognitoClientID = "test"
	req := httptest.NewRequest("GET", "/.well-known/oauth-authorization-server", nil)
	w := httptest.NewRecorder()
	s.metadata(w, req)

	var doc map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &doc)
	grants, _ := doc["grant_types_supported"].([]any)
	found := false
	for _, g := range grants {
		if g == "refresh_token" {
			found = true
		}
	}
	if !found {
		t.Errorf("grant_types_supported must advertise refresh_token: %v", grants)
	}
}
