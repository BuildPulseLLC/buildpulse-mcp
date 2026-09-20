package main

// Guardrails on the open /oauth/register endpoint.
//
// Registration stays unauthenticated on purpose (see consent.go), but "open"
// should not mean "unvalidated". Two things are enforced here:
//
//   1. Redirect URIs must be somewhere a browser can safely hand a code:
//      https, or http on a loopback address (RFC 8252 native-app flow, which
//      is how Claude Code and every other local MCP client connects). This
//      shuts the door on javascript:/data: URIs and on plaintext http to a
//      public host.
//
//   2. Registration is rate limited per client IP. The table is durable with
//      no TTL, so without a limit any unauthenticated caller can write to our
//      DynamoDB indefinitely.

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	maxRedirectURIs   = 5
	maxClientNameLen  = 255
	maxRedirectURILen = 2048

	// registerRateLimit is per client IP per window. Generous: a real
	// client registers once per install, and a loopback client re-registers
	// when its callback port changes. Anything above this is a crawler or
	// an abuser.
	registerRateLimit  = 10
	registerRateWindow = 10 * time.Minute
)

// validateRedirectURI rejects anything we would not want to hand an
// authorization code to.
func validateRedirectURI(raw string) error {
	if raw == "" {
		return fmt.Errorf("redirect_uri must not be empty")
	}
	if len(raw) > maxRedirectURILen {
		return fmt.Errorf("redirect_uri exceeds %d characters", maxRedirectURILen)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("redirect_uri is not a valid URI")
	}
	// RFC 6749 §3.1.2: the endpoint URI must not include a fragment. A
	// fragment would also be dropped by the browser on redirect, so a
	// client registering one is confused about where its code will land.
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("redirect_uri must not contain a fragment")
	}

	switch strings.ToLower(u.Scheme) {
	case "https":
		if u.Hostname() == "" {
			return fmt.Errorf("redirect_uri must include a host")
		}
		return nil

	case "http":
		// Plaintext is only acceptable to the user's own machine, where
		// there is no network hop to intercept. RFC 8252 §7.3.
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("http redirect_uri is only allowed on loopback (localhost / 127.0.0.1 / [::1])")

	case "":
		return fmt.Errorf("redirect_uri must be absolute and include a scheme")

	default:
		// Private-use URI schemes for native apps must be reverse-domain
		// named (RFC 8252 §7.1), e.g. com.example.app:/callback. The dot
		// requirement is what keeps javascript:, data:, vbscript: and
		// file: out.
		if strings.Contains(u.Scheme, ".") {
			return nil
		}
		return fmt.Errorf("unsupported redirect_uri scheme %q; use https, http on loopback, or a reverse-domain private-use scheme", u.Scheme)
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// validateRegistration applies every limit to a decoded registration request.
func validateRegistration(clientName string, redirectURIs []string) error {
	if len(redirectURIs) == 0 {
		return fmt.Errorf("redirect_uris is required and must be non-empty")
	}
	if len(redirectURIs) > maxRedirectURIs {
		return fmt.Errorf("at most %d redirect_uris may be registered", maxRedirectURIs)
	}
	if len(clientName) > maxClientNameLen {
		return fmt.Errorf("client_name exceeds %d characters", maxClientNameLen)
	}
	for _, u := range redirectURIs {
		if err := validateRedirectURI(u); err != nil {
			return err
		}
	}
	return nil
}

// rateLimiter is a fixed-window counter keyed by client IP.
//
// Deliberately per-task and in-memory: the MCP service runs a small number of
// ECS tasks, so the effective ceiling is registerRateLimit x task count. That
// is a blunt instrument, but it turns "unbounded unauthenticated writes" into
// a bounded number, which is the property we actually need here. A shared
// limiter would mean another DynamoDB round-trip on a cold path.
type rateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	limit   int
	buckets map[string]*rateBucket
}

type rateBucket struct {
	count int
	reset time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{window: window, limit: limit, buckets: map[string]*rateBucket{}}
}

// allow reports whether key may proceed, and reaps expired buckets so the map
// cannot grow without bound under a spray of unique source IPs.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()

	for k, b := range rl.buckets {
		if now.After(b.reset) {
			delete(rl.buckets, k)
		}
	}

	b, ok := rl.buckets[key]
	if !ok || now.After(b.reset) {
		rl.buckets[key] = &rateBucket{count: 1, reset: now.Add(rl.window)}
		return true
	}
	if b.count >= rl.limit {
		return false
	}
	b.count++
	return true
}

// clientIP extracts the caller's address. Behind the ALB, X-Forwarded-For is
// "<client-supplied...>, <real client>" because the load balancer appends the
// address it actually saw — so the RIGHTMOST entry is the only one a caller
// cannot forge.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
