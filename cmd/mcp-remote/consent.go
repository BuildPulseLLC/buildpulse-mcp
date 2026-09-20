package main

// User consent for dynamically registered OAuth clients.
//
// Why this exists: /oauth/register is an open RFC 7591 endpoint, because
// the MCP authorization spec expects MCP clients (Claude Code, Antigravity,
// Cursor, ...) to self-register — we cannot allowlist clients we have never
// seen. That makes the consent screen the control that keeps open
// registration safe: anyone can mint a client_id pointing at a redirect URI
// they own, so the only thing standing between a crafted /authorize link and
// a victim's authorization code is an explicit, human decision.
//
// Placement matters. Consent is collected AFTER the Cognito hop, not before:
// at that point we know *who* is being asked, so the page can name the
// account whose CI data is about to be shared. It also closes the silent
// pass-through — previously a victim with a live Cognito session was bounced
// straight through the Hosted UI and back out to the client's redirect URI
// with a code attached, without ever seeing a BuildPulse screen.

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// consentPrefix namespaces consent-stage records inside the existing
	// pending table. The Cognito hop keys that table by its internal
	// state; prefixing keeps the two stages from ever colliding without
	// needing a fourth DynamoDB table (and the Terraform to go with it).
	consentPrefix = "consent:"

	// consentTTL is how long the user has to decide. Long enough to
	// actually read the page, short enough that an abandoned decision
	// does not leave an approvable request lying around. The pending
	// table's own TTL sweeps anything we fail to pop.
	consentTTL = 10 * time.Minute
)

// consentPage is rendered straight from /oauth/callback. Everything
// interpolated here is attacker-controlled (client_name and redirect_uri
// come from unauthenticated registration), so this is html/template, never
// fmt.Sprintf into HTML.
var consentPage = template.Must(template.New("consent").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Authorize access &middot; BuildPulse</title>
<style>
  :root { color-scheme: light dark; }
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center;
         background:#f4f5f7; font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
         color:#14171f; padding:24px; box-sizing:border-box; }
  .card { background:#fff; max-width:460px; width:100%; border-radius:12px; padding:32px;
          box-shadow:0 1px 3px rgba(0,0,0,.08),0 8px 24px rgba(0,0,0,.06); box-sizing:border-box; }
  h1 { font-size:20px; margin:0 0 4px; letter-spacing:-.01em; }
  .sub { color:#5b6270; margin:0 0 24px; font-size:14px; }
  .warn { background:#fff8e6; border:1px solid #f0d491; border-radius:8px; padding:12px 14px;
          font-size:13px; color:#6b4e00; margin:0 0 20px; }
  dl { margin:0 0 24px; padding:16px; background:#f7f8fa; border-radius:8px; font-size:13px; }
  dt { color:#5b6270; margin-bottom:2px; }
  dd { margin:0 0 12px; font-weight:600; word-break:break-all; }
  dd:last-of-type { margin-bottom:0; }
  ul { margin:0 0 24px; padding-left:20px; font-size:14px; color:#2d3341; }
  li { margin-bottom:4px; }
  .row { display:flex; gap:12px; }
  button { flex:1; padding:11px 16px; font-size:14px; font-weight:600; border-radius:8px;
           cursor:pointer; font-family:inherit; border:1px solid transparent; }
  .deny { background:#fff; border-color:#d3d7de; color:#2d3341; }
  .allow { background:#1f6feb; color:#fff; }
  .deny:hover { background:#f4f5f7; }
  .allow:hover { background:#1a5fd0; }
  @media (prefers-color-scheme: dark) {
    body { background:#0f1116; color:#e6e8ec; }
    .card { background:#181b22; box-shadow:none; border:1px solid #272b35; }
    .sub, dt { color:#9aa2b1; }
    dl { background:#1f232b; }
    ul { color:#c9cede; }
    .warn { background:#2a2312; border-color:#5c4a12; color:#e8cf8a; }
    .deny { background:#1f232b; border-color:#3a404d; color:#e6e8ec; }
    .deny:hover { background:#272b35; }
  }
</style>
</head>
<body>
  <div class="card">
    <h1>Authorize access</h1>
    <p class="sub"><strong>{{.ClientName}}</strong> wants to read BuildPulse data for <strong>{{.UserEmail}}</strong>.</p>

    <div class="warn">
      This application registered itself and has <strong>not</strong> been verified by BuildPulse.
      Approve it only if you started this connection yourself.
    </div>

    <dl>
      <dt>Application</dt><dd>{{.ClientName}}</dd>
      <dt>Will send your data to</dt><dd>{{.RedirectURI}}</dd>
      <dt>Signed in as</dt><dd>{{.UserEmail}}</dd>
    </dl>

    <ul>
      <li>Read your organizations and repositories</li>
      <li>Read test results, flaky tests, failures and coverage</li>
    </ul>

    <form method="POST" action="/oauth/consent" class="row">
      <input type="hidden" name="consent_key" value="{{.ConsentKey}}">
      <button type="submit" name="action" value="deny" class="deny">Cancel</button>
      <button type="submit" name="action" value="approve" class="allow">Allow access</button>
    </form>
  </div>
</body>
</html>`))

type consentView struct {
	ClientName  string
	RedirectURI string
	UserEmail   string
	ConsentKey  string
}

// renderConsent writes the interstitial. The page carries no cookie and no
// ambient authority — the only thing that authorizes the subsequent POST is
// the single-use ConsentKey in the form — but it is still framed-denied so a
// malicious page cannot overlay it and harvest the click.
func renderConsent(w http.ResponseWriter, v consentView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; default-src 'none'; style-src 'unsafe-inline'; form-action 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if err := consentPage.Execute(w, v); err != nil {
		log.Printf("consent: render failed: %v", err)
	}
}

// consent handles the user's decision. Approve mints the authorization code
// that /oauth/callback used to mint unconditionally; deny returns the RFC
// 6749 §4.1.2.1 access_denied error to the client.
func (s *oauthServer) consent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "could not parse form body")
		return
	}
	key := r.PostFormValue("consent_key")
	if key == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "missing consent_key")
		return
	}

	// Single-use: the pop means a replayed or double-submitted form finds
	// nothing, so one approval can never mint two codes.
	pending, err := s.store.PopPending(r.Context(), consentPrefix+key)
	if err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "this authorization request is no longer valid; restart from /authorize")
		return
	}
	if time.Now().After(pending.Expires) {
		oauthError(w, http.StatusBadRequest, "invalid_request", "this authorization request has expired; restart from /authorize")
		return
	}

	// Re-check the redirect URI against the client's current registration.
	// The registration could have been replaced between /authorize and the
	// click; we refuse to emit a code to a URI that is not registered now.
	client, cerr := s.store.GetClient(r.Context(), pending.ClientID)
	if cerr != nil || !contains(client.RedirectURIs, pending.RedirectURI) {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uri is no longer registered for this client")
		return
	}

	if r.PostFormValue("action") != "approve" {
		log.Printf("consent: denied by user sub=%s client=%s", pending.UserSubject, pending.ClientID)
		http.Redirect(w, r, appendQuery(pending.RedirectURI, url.Values{
			"error":             {"access_denied"},
			"error_description": {"the user denied the authorization request"},
			"state":             {pending.OriginalState},
		}), http.StatusFound)
		return
	}

	code := randomHex(32)
	if err := s.store.PutCode(r.Context(), &authorizationCode{
		Code:              code,
		ClientID:          pending.ClientID,
		RedirectURI:       pending.RedirectURI,
		CodeChallenge:     pending.CodeChallenge,
		Scope:             pending.Scope,
		UserSubject:       pending.UserSubject,
		UserEmail:         pending.UserEmail,
		OrganizationIDs:   pending.OrganizationIDs,
		CognitoRefreshEnc: pending.CognitoRefreshEnc,
		Expires:           time.Now().Add(authCodeTTL),
	}); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "failed to persist authorization code")
		return
	}

	log.Printf("consent: approved sub=%s client=%s (%s)", pending.UserSubject, pending.ClientID, pending.ClientName)
	http.Redirect(w, r, appendQuery(pending.RedirectURI, url.Values{
		"code":  {code},
		"state": {pending.OriginalState},
	}), http.StatusFound)
}

// stashConsent persists the post-authentication state that the consent POST
// needs, and returns the single-use key that ties the rendered form to it.
func (s *oauthServer) stashConsent(ctx context.Context, p *pendingAuth) (string, error) {
	key := randomHex(32)
	p.Expires = time.Now().Add(consentTTL)
	if err := s.store.PutPending(ctx, consentPrefix+key, p); err != nil {
		return "", fmt.Errorf("persist consent: %w", err)
	}
	return key, nil
}

// appendQuery adds params to a redirect URI, preserving any query string the
// client registered. Replaces the hand-rolled "?" / "&" concatenation that
// the callback used to do inline.
func appendQuery(redirectURI string, params url.Values) string {
	sep := "?"
	if strings.Contains(redirectURI, "?") {
		sep = "&"
	}
	return redirectURI + sep + params.Encode()
}
