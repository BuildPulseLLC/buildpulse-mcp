package main

// OAuth 2.1 + PKCE + Dynamic Client Registration scaffolding for the
// hosted BuildPulse MCP. This is the path required by the Anthropic
// Connectors program and ChatGPT's connector partner program — both
// expect the MCP server to be a proper OAuth 2.1 authorization server
// at the same origin as the MCP endpoint.
//
// Design:
//
//   - The MCP server is the OAuth authorization server (issuer =
//     https://mcp.buildpulse.io). It owns /authorize, /token, and
//     /register endpoints, plus the discovery document at
//     /.well-known/oauth-authorization-server.
//
//   - Actual user authentication is delegated to Cognito Hosted UI
//     (already in production for the BuildPulse web app). After the
//     user authorizes there, Cognito redirects back to our /oauth/callback,
//     we exchange the Cognito code for an ID token, map the user to
//     their BuildPulse organization, and mint our own short-lived
//     access token that the MCP server accepts as a Bearer.
//
//   - Bearer tokens issued here are 40-char hex strings, persisted to
//     DocumentDB's `mcpSessions` collection. platform-api's auth
//     middleware looks them up there on every tool call. (This is a
//     separate credential type from user-supplied BuildPulse API
//     tokens, which can be either `bp_<64-hex>` or legacy `<40-hex>`
//     and live in the `apiTokens` collection.) See `Production TODOs`
//     below for how to persist these.
//
// Production TODOs (intentionally not blocking the scaffold):
//
//   1. **Persistence** — clients, codes, and tokens currently live in
//      in-memory sync.Maps. For multi-task ECS deploys (and for any
//      session that has to survive task restarts) these need to move
//      to DynamoDB. Each store has its own table; the access patterns
//      are simple key/value with TTL.
//
//   2. **Platform-api integration** — currently the issued access
//      tokens are never validated against any real org. Wire one of:
//        (a) Have platform-api accept "mcp:<random>" tokens by
//            consulting a new collection `mcpSessionTokens` keyed
//            by hashedToken (matches the existing apiTokens pattern).
//        (b) When the MCP receives a Bearer token, look it up in
//            DynamoDB and forward the underlying BuildPulse API
//            token to platform-api. (Current code path; just needs
//            the storage swap from in-memory to DynamoDB.)
//
//   3. **Cognito wiring** — the constants below assume Cognito Hosted
//      UI at oauth.buildpulse.io. Plumb the actual client ID +
//      secret via environment / Secrets Manager. The current code
//      reads env vars but tolerates missing values (returns 501 with a
//      clear message).
//
//   4. **Scopes** — currently a single `buildpulse.read` scope. If we
//      ever issue write capabilities (e.g. quarantine tests), add a
//      `buildpulse.write` scope and enforce per-tool.
//
//   5. **Token revocation** — RFC 7009 /oauth/revoke endpoint is not
//      yet implemented. Mostly a nicety; tokens have a 1h TTL anyway.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// Access tokens expire in one hour. Short enough that compromised
	// tokens are time-limited; long enough that we don't churn through
	// refresh-token flows.
	accessTokenTTL = 1 * time.Hour

	// Authorization codes are single-use and expire in 60 seconds —
	// just long enough for the client to make the /token request.
	authCodeTTL = 60 * time.Second

	// Single scope today. Read-only.
	defaultScope = "buildpulse.read"

	// Cognito Hosted UI domain. Resolved at request time so deploys can
	// override via env var without rebuilding.
	envCognitoDomain    = "COGNITO_DOMAIN"
	envCognitoClientID  = "COGNITO_CLIENT_ID"
	envCognitoSecret    = "COGNITO_CLIENT_SECRET"
	envIssuer           = "MCP_ISSUER"
	defaultIssuer       = "https://mcp.buildpulse.io"
	defaultRedirectPath = "/oauth/callback"
)

// oauthServer holds the in-memory authorization-server state. All
// stores are concurrent-safe.
type oauthServer struct {
	issuer string

	// Cognito upstream — when these are empty, /authorize returns 501
	// with a clear message instead of half-implementing the flow.
	cognitoDomain   string
	cognitoClientID string
	cognitoSecret   string

	// State persistence — abstracted behind the Store interface so the
	// same OAuth code path runs whether we're backed by sync.Maps
	// (local dev / desired_count=1) or DynamoDB (production HA). See
	// store.go for the design context.
	store Store

	// Internal HTTP client for Cognito calls. Carved out so tests can
	// inject a fake.
	http *http.Client
}

func newOAuthServer(store Store) *oauthServer {
	if store == nil {
		store = newMemoryStore()
	}
	return &oauthServer{
		issuer:          envOr(envIssuer, defaultIssuer),
		cognitoDomain:   envOr(envCognitoDomain, ""),
		cognitoClientID: envOr(envCognitoClientID, ""),
		cognitoSecret:   envOr(envCognitoSecret, ""),
		store:           store,
		http:            &http.Client{Timeout: 10 * time.Second},
	}
}

// registeredClient is the in-memory representation of a dynamically
// registered OAuth client (RFC 7591). Today we don't authenticate
// clients on /token at all because PKCE handles the cross-app
// integrity guarantee — clients are public per the spec when they
// can't keep secrets (any local stdio/desktop MCP client).
type registeredClient struct {
	ClientID     string    `json:"client_id"`
	ClientName   string    `json:"client_name,omitempty"`
	RedirectURIs []string `json:"redirect_uris"`
	GrantTypes   []string `json:"grant_types"`
	// CreatedAt is serialized as Unix epoch seconds (an integer) per
	// RFC 7591 §3.2.1 — NOT as Go's default RFC 3339 string. The
	// MCP SDK / Claude Code's OAuth client validates this field
	// against a number-schema and rejects the registration response
	// when it's a string. See MarshalJSON below.
	CreatedAt time.Time `json:"-"`
}

// MarshalJSON renders registeredClient with `client_id_issued_at`
// as the RFC 7591-compliant Unix-seconds integer. Otherwise we'd
// inherit Go's time.Time → RFC 3339 string default and break OAuth
// clients that validate against the spec.
func (c *registeredClient) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ClientID            string   `json:"client_id"`
		ClientName          string   `json:"client_name,omitempty"`
		RedirectURIs        []string `json:"redirect_uris"`
		GrantTypes          []string `json:"grant_types"`
		ClientIDIssuedAt    int64    `json:"client_id_issued_at"`
		TokenEndpointAuth   string   `json:"token_endpoint_auth_method"`
	}{
		ClientID:          c.ClientID,
		ClientName:        c.ClientName,
		RedirectURIs:      c.RedirectURIs,
		GrantTypes:        c.GrantTypes,
		ClientIDIssuedAt:  c.CreatedAt.Unix(),
		TokenEndpointAuth: "none", // PKCE-only, public clients
	})
}

type authorizationCode struct {
	Code            string
	ClientID        string
	RedirectURI     string
	CodeChallenge   string
	Scope           string
	UserSubject     string   // Cognito user sub
	UserEmail       string
	OrganizationIDs []string // org UUIDs resolved at callback time
	Expires         time.Time
}

// pendingAuth bridges the original /authorize request to the Cognito
// callback. We use our own state value to find it; the original
// client state is preserved so we can echo it back unchanged.
type pendingAuth struct {
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	OriginalState string
	Scope         string
	Expires       time.Time
}

// register handles RFC 7591 dynamic client registration. We accept
// any reasonable input; the only real constraint is that
// `redirect_uris` is non-empty.
func (s *oauthServer) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		GrantTypes              []string `json:"grant_types"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "could not decode JSON body")
		return
	}
	if len(req.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required and must be non-empty")
		return
	}

	clientID := "mcp_" + randomHex(16)
	c := &registeredClient{
		ClientID:     clientID,
		ClientName:   req.ClientName,
		RedirectURIs: req.RedirectURIs,
		GrantTypes:   defaultIfEmpty(req.GrantTypes, []string{"authorization_code", "refresh_token"}),
		CreatedAt:    time.Now(),
	}
	if err := s.store.PutClient(r.Context(), c); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "failed to persist client registration")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(c)
}

// authorize is the first leg of the OAuth dance. Validates the
// client + PKCE challenge, stashes the request in `pending`, and
// redirects the browser to Cognito Hosted UI.
func (s *oauthServer) authorize(w http.ResponseWriter, r *http.Request) {
	if s.cognitoDomain == "" || s.cognitoClientID == "" {
		oauthError(w, http.StatusNotImplemented, "server_misconfigured",
			"OAuth is not configured (COGNITO_DOMAIN / COGNITO_CLIENT_ID env vars). Use Bearer-token auth instead — see https://platform.buildpulse.io/docs/mcp")
		return
	}

	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	responseType := q.Get("response_type")
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")
	scope := q.Get("scope")
	state := q.Get("state")

	if responseType != "code" {
		oauthError(w, http.StatusBadRequest, "unsupported_response_type", "only 'code' is supported")
		return
	}
	client, err := s.store.GetClient(r.Context(), clientID)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client", "unknown client_id; register via /oauth/register first")
		return
	}
	if !contains(client.RedirectURIs, redirectURI) {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uri was not registered")
		return
	}
	if codeChallenge == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "code_challenge is required (PKCE)")
		return
	}
	if codeChallengeMethod != "S256" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "code_challenge_method must be 'S256'")
		return
	}
	if scope == "" {
		scope = defaultScope
	}

	internalState := randomHex(16)
	if err := s.store.PutPending(r.Context(), internalState, &pendingAuth{
		ClientID:      clientID,
		RedirectURI:   redirectURI,
		CodeChallenge: codeChallenge,
		OriginalState: state,
		Scope:         scope,
		Expires:       time.Now().Add(15 * time.Minute),
	}); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "failed to persist authorization state")
		return
	}

	cognitoURL := s.cognitoDomain + "/oauth2/authorize?" + url.Values{
		"response_type": {"code"},
		"client_id":     {s.cognitoClientID},
		"redirect_uri":  {s.issuer + defaultRedirectPath},
		"scope":         {"openid email profile"},
		"state":         {internalState},
	}.Encode()
	http.Redirect(w, r, cognitoURL, http.StatusFound)
}

// callback finishes the dance: Cognito redirects here with a code, we
// exchange it for an ID token, mint our own short-lived authorization
// code, and redirect back to the original client.
func (s *oauthServer) callback(w http.ResponseWriter, r *http.Request) {
	cognitoCode := r.URL.Query().Get("code")
	internalState := r.URL.Query().Get("state")
	if cognitoCode == "" || internalState == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "missing code or state")
		return
	}

	pending, err := s.store.PopPending(r.Context(), internalState)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "no pending authorization for this state")
		return
	}
	if time.Now().After(pending.Expires) {
		oauthError(w, http.StatusBadRequest, "invalid_request", "authorization request has expired; restart from /authorize")
		return
	}

	// Exchange the Cognito code for an ID token. We don't validate
	// the ID token signature here — Cognito is trusted as the upstream
	// IdP, and we're going through TLS to its token endpoint.
	idClaims, err := s.exchangeCognitoCode(r.Context(), cognitoCode)
	if err != nil {
		oauthError(w, http.StatusBadGateway, "upstream_error", "failed to exchange Cognito code: "+err.Error())
		return
	}

	// Resolve the Cognito user's BuildPulse org memberships so the
	// mcpSession persisted at /token can scope subsequent tool calls.
	// Logged either way so the downstream-tool-call 401 has a
	// breadcrumb.
	orgIDs, oerr := resolveUserOrgs(r.Context(), idClaims.Sub, idClaims.Email)
	if oerr != nil {
		log.Printf("resolveUserOrgs failed for sub=%s email=%s: %v", idClaims.Sub, idClaims.Email, oerr)
		orgIDs = nil
	} else {
		log.Printf("resolveUserOrgs ok for sub=%s email=%s: %d orgs", idClaims.Sub, idClaims.Email, len(orgIDs))
	}

	code := randomHex(32)
	if err := s.store.PutCode(r.Context(), &authorizationCode{
		Code:            code,
		ClientID:        pending.ClientID,
		RedirectURI:     pending.RedirectURI,
		CodeChallenge:   pending.CodeChallenge,
		Scope:           pending.Scope,
		UserSubject:     idClaims.Sub,
		UserEmail:       idClaims.Email,
		OrganizationIDs: orgIDs,
		Expires:         time.Now().Add(authCodeTTL),
	}); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "failed to persist authorization code")
		return
	}

	finalRedirect := pending.RedirectURI
	sep := "?"
	if strings.Contains(finalRedirect, "?") {
		sep = "&"
	}
	finalRedirect += sep + url.Values{
		"code":  {code},
		"state": {pending.OriginalState},
	}.Encode()
	http.Redirect(w, r, finalRedirect, http.StatusFound)
}

// token is the /oauth/token endpoint. Supports the authorization_code
// grant with PKCE today; refresh_token is left as a TODO.
func (s *oauthServer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "could not parse form body")
		return
	}
	grantType := r.PostFormValue("grant_type")
	if grantType != "authorization_code" {
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"only 'authorization_code' is supported")
		return
	}

	code := r.PostFormValue("code")
	codeVerifier := r.PostFormValue("code_verifier")
	clientID := r.PostFormValue("client_id")
	redirectURI := r.PostFormValue("redirect_uri")

	auth, err := s.store.PopCode(r.Context(), code)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code is unknown or has already been used")
		return
	}
	if time.Now().After(auth.Expires) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code has expired (60s TTL)")
		return
	}
	if auth.ClientID != clientID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code was issued to a different client")
		return
	}
	if redirectURI != "" && redirectURI != auth.RedirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri must match the one used in /authorize")
		return
	}

	// Verify PKCE: SHA256(code_verifier) base64url-encoded (no padding)
	// must equal the stored code_challenge.
	if codeVerifier == "" {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier is required (PKCE)")
		return
	}
	if !verifyPKCE(codeVerifier, auth.CodeChallenge) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}

	accessToken := randomHex(20) // 40-hex chars; persisted to mcpSessions (separate from apiTokens)

	// Persist the session in DocumentDB so platform-api's auth
	// middleware accepts the token on tool calls. Errors are logged
	// but don't fail the OAuth flow — the downstream 401 (if any) is
	// the actionable surface; the OAuth client did nothing wrong.
	if perr := persistMCPSession(
		r.Context(), accessToken, auth.UserSubject, auth.UserEmail,
		auth.OrganizationIDs, accessTokenTTL,
	); perr != nil {
		log.Printf("persistMCPSession failed for %s (%d orgs): %v", auth.UserEmail, len(auth.OrganizationIDs), perr)
	} else {
		log.Printf("persistMCPSession ok for %s (%d orgs)", auth.UserEmail, len(auth.OrganizationIDs))
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   int(accessTokenTTL.Seconds()),
		"scope":        auth.Scope,
	})
}

// metadata returns the RFC 8414 / RFC 7591 discovery document. MCP
// clients (Claude.ai, ChatGPT) fetch this from
// /.well-known/oauth-authorization-server to learn endpoint URLs and
// supported capabilities.
func (s *oauthServer) metadata(w http.ResponseWriter, r *http.Request) {
	configured := s.cognitoDomain != "" && s.cognitoClientID != ""

	doc := map[string]any{
		"issuer":                                s.issuer,
		"authorization_endpoint":                s.issuer + "/oauth/authorize",
		"token_endpoint":                        s.issuer + "/oauth/token",
		"registration_endpoint":                 s.issuer + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{defaultScope},
		"service_documentation":                 "https://platform.buildpulse.io/docs/mcp",
	}
	if !configured {
		// Surface the misconfiguration in-band so Anthropic Connectors
		// won't list a half-working server.
		doc["x-buildpulse-oauth-status"] = "unconfigured"
		doc["x-buildpulse-oauth-fallback"] = "Use `Authorization: Bearer <BuildPulse API token>` directly (either `bp_<64-hex>` or legacy `<40-hex>` shape) until COGNITO_DOMAIN and COGNITO_CLIENT_ID are set."
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

// (LookupToken / s.tokens removed — the in-process token map was
// redundant once mcpSessions in DocumentDB became the source of
// truth. platform-api's auth middleware reads mcpSessions on every
// /api call; nothing in the MCP HTTP path needed a local cache.)

// --- Cognito glue --------------------------------------------------------

type cognitoIDClaims struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
}

// exchangeCognitoCode posts to Cognito's /oauth2/token endpoint with
// the code+grant_type=authorization_code and returns the parsed `sub`
// and `email` claims from the returned id_token.
//
// We do NOT validate the id_token signature here; we trust that the
// TLS connection to Cognito + the freshly-minted code-exchange
// authenticates this leg. For a hardened deploy, swap to a JWKS-based
// verification against Cognito's published JWKS endpoint.
func (s *oauthServer) exchangeCognitoCode(_ /* ctx */ interface{}, code string) (*cognitoIDClaims, error) {
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"client_id":    {s.cognitoClientID},
		"code":         {code},
		"redirect_uri": {s.issuer + defaultRedirectPath},
	}
	req, err := http.NewRequest("POST", s.cognitoDomain+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if s.cognitoSecret != "" {
		req.SetBasicAuth(s.cognitoClientID, s.cognitoSecret)
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("cognito returned status %d", resp.StatusCode)
	}

	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.IDToken == "" {
		return nil, errors.New("cognito returned no id_token")
	}

	// Decode the middle segment of the JWT — claims. We're not
	// verifying the signature; see the doc above for why.
	parts := strings.Split(body.IDToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("id_token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("base64 decode id_token payload: %w", err)
	}
	var claims cognitoIDClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("decode id_token claims: %w", err)
	}
	if claims.Sub == "" {
		return nil, errors.New("id_token has no `sub` claim")
	}
	return &claims, nil
}

// --- helpers --------------------------------------------------------------

// verifyPKCE returns true iff base64url(sha256(verifier)) == challenge.
// Per RFC 7636 §4.6, no padding is used in the base64url encoding.
func verifyPKCE(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtleConstantTimeEq(computed, challenge)
}

// subtleConstantTimeEq does a length-independent byte-equality check
// to defeat timing side channels. Inputs are short ASCII so reaching
// for crypto/subtle directly is fine but this keeps it inline.
func subtleConstantTimeEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand should never fail; if it does, panic so the
		// caller doesn't get a deterministic token.
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func defaultIfEmpty(s, fallback []string) []string {
	if len(s) == 0 {
		return fallback
	}
	return s
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":             code,
		"error_description": description,
	})
}

func envOr(key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

// getenv is a wrapper to make this file unit-testable without
// monkey-patching os.Getenv. main.go calls os.Getenv directly; here
// we go through a small indirection.
var getenv = func(key string) string {
	return osGetenv(key)
}
