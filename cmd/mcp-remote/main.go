// Command mcp-remote is the BuildPulse Model Context Protocol server
// over Streamable HTTP. It is the hosted variant of cmd/mcp, deployed
// at https://mcp.buildpulse.io for clients that can't spawn local
// stdio servers — Claude.ai web, Claude Desktop integrations panel,
// ChatGPT Connectors, etc.
//
// Authentication is per-session: the Streamable HTTP client supplies
// an Authorization header (`Bearer <token>` or `token <token>`) on
// every MCP request, and the server builds a request-scoped MCP
// server bound to that token. The token is the same BuildPulse API
// token that authenticates against platform-api, so customer
// integrations work without provisioning a second credential.
//
// Two API token shapes are accepted (both validated server-side by
// platform-api, so we don't second-guess here):
//   - `bp_<64-hex>` — current format minted by web-client.
//   - `<40-hex>`    — legacy Rails-era format; still works.
//
// OAuth-minted mcpSession tokens (issued by /oauth/token) are a third,
// orthogonal credential — they happen to also be 40-hex, but that's an
// implementation detail of the OAuth flow, not a constraint on the
// caller's chosen API token.
//
// OAuth-based auth (required for Anthropic's Connectors program) is
// scaffolded but not yet enabled — see /oauth/* routes.
//
// Configuration:
//
//	PORT            HTTP listen port (default 8080)
//	ENVIRONMENT     production | development (default development)
//	PLATFORM_API_URL  Default https://platform.buildpulse.io
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/BuildPulseLLC/buildpulse-mcp/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultPort      = "8080"
	streamableHTTP   = "/mcp"
	healthPath       = "/health"
	wellKnownOAuth            = "/.well-known/oauth-authorization-server"
	wellKnownMCP              = "/.well-known/mcp"
	wellKnownProtectedResrc   = "/.well-known/oauth-protected-resource"
	wellKnownProtectedRsrcMCP = "/.well-known/oauth-protected-resource/mcp"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}
	env := os.Getenv("ENVIRONMENT")
	if env == "" {
		env = "development"
	}
	platformURL := os.Getenv("PLATFORM_API_URL")
	if platformURL == "" {
		platformURL = mcpserver.DefaultPlatformURL
	}

	// Connect to DocumentDB so the OAuth callback can write mcpSession
	// records that platform-api's auth middleware accepts on tool
	// calls. Best-effort: if MONGODB_URI is unset or unreachable the
	// rest of the server keeps working, but OAuth-minted tokens won't
	// authenticate against platform-api. See cmd/mcp-remote/mongo.go.
	initMongo(context.Background())

	mux := http.NewServeMux()

	// hostname is captured at startup so /health and other endpoints can
	// echo back which ECS task served the request — used to verify ALB
	// target-group stickiness from the outside. On Fargate this is
	// `ip-10-0-x-y` derived from the task ENI.
	hostname, _ := os.Hostname()
	mux.HandleFunc("GET "+healthPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Instance-Id", hostname)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "instance": hostname})
	})

	// The Streamable HTTP MCP transport. The Go SDK's
	// NewStreamableHTTPHandler asks us for a *Server per inbound
	// request — we use that hook to bind the request's Authorization
	// token to the server's outbound calls.
	//
	// Stateless: true skips Mcp-Session-Id validation and treats every
	// POST as a fresh, self-contained request. This is the right mode
	// for BuildPulse because:
	//   1. Every tool is read-only against platform-api; we never need
	//      server->client requests (the only thing Stateless mode
	//      rejects — see the StreamableHTTPOptions godoc).
	//   2. Without per-session in-process state, the ALB can freely
	//      round-robin requests across ECS tasks. This is what lets
	//      mcp-remote run min/max_capacity=2 (or more) safely; cookie
	//      stickiness still works for browser clients but is no longer
	//      load-bearing for SDK clients that don't keep a cookie jar.
	// OAuth-flow state (clients, codes, pending) is separately persisted
	// to DynamoDB via store_dynamo.go, so the OAuth surface area is also
	// task-independent.
	streamable := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			token, err := extractToken(r.Header.Get("Authorization"))
			if err != nil {
				// Returning nil makes the SDK respond 400.
				// We can't shape the response body here, but the OAuth
				// metadata endpoint below tells well-behaved clients
				// where to look for credentials.
				log.Printf("rejecting MCP session: %v (remote=%s)", err, r.RemoteAddr)
				return nil
			}
			client := mcpserver.NewClient(platformURL, token)
			return mcpserver.New(client)
		},
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	mux.Handle("/mcp", streamable)
	mux.Handle("/mcp/", streamable)

	// MCP discovery — clients (Claude.ai's Connector picker, etc.)
	// fetch /.well-known/mcp to learn what the server offers.
	mux.HandleFunc("GET "+wellKnownMCP, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"server": map[string]any{
				"name":    mcpserver.ServerImplementation.Name,
				"title":   mcpserver.ServerImplementation.Title,
				"version": mcpserver.ServerImplementation.Version,
			},
			"endpoints": map[string]string{
				"streamable_http": "/mcp",
			},
			"authentication": map[string]any{
				"types": []string{"bearer"},
				"bearer": map[string]any{
					"header":      "Authorization",
					"scheme":      "Bearer",
					"description": "BuildPulse API token (created at https://app.buildpulse.io). Accepted shapes: `bp_<64-hex>` (current) or `<40-hex>` (legacy).",
				},
			},
			"documentation": "https://platform.buildpulse.io/docs/mcp",
		})
	})

	// OAuth 2.1 authorization server. See oauth.go for the full
	// design. The flow is:
	//   /.well-known/oauth-authorization-server  → RFC 8414 metadata
	//   POST /oauth/register                     → RFC 7591 dynamic registration
	//   GET  /oauth/authorize                    → redirects to Cognito Hosted UI
	//   GET  /oauth/callback                     → Cognito redirects back here
	//   POST /oauth/token                        → code exchange (PKCE)
	//
	// When COGNITO_DOMAIN / COGNITO_CLIENT_ID are unset, /authorize
	// returns 501 with a clear message and the metadata document
	// surfaces `x-buildpulse-oauth-status=unconfigured`. Bearer-token
	// auth on the MCP endpoint continues to work either way.
	//
	// Store: DynamoDB when the three OAUTH_* table names are set,
	// in-memory otherwise. See store.go for the design.
	store := buildOAuthStore(context.Background())
	oauth := newOAuthServer(store)
	mux.HandleFunc("GET "+wellKnownOAuth, oauth.metadata)
	mux.HandleFunc("POST /oauth/register", oauth.register)
	mux.HandleFunc("GET /oauth/authorize", oauth.authorize)
	mux.HandleFunc("GET /oauth/callback", oauth.callback)
	mux.HandleFunc("POST /oauth/token", oauth.token)

	// RFC 9728 OAuth 2.0 Protected Resource Metadata. Newer MCP
	// clients (Claude Code, Cursor) probe this endpoint to learn
	// which authorization server protects the `/mcp` resource.
	// We point them at our own RFC 8414 metadata document.
	protectedResource := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"resource":              "https://mcp.buildpulse.io/mcp",
			"authorization_servers": []string{"https://mcp.buildpulse.io"},
			"bearer_methods_supported": []string{"header"},
			"resource_documentation":   "https://platform.buildpulse.io/docs/mcp",
		})
	}
	mux.HandleFunc("GET "+wellKnownProtectedResrc, protectedResource)
	mux.HandleFunc("GET "+wellKnownProtectedRsrcMCP, protectedResource)

	handler := withRequestID(withLogging(withCORS(mux)))

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0, // Streamable HTTP keeps the connection open for SSE.
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("buildpulse-mcp-remote listening on :%s (env=%s, platform=%s)", port, env, platformURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// extractToken parses a `Bearer <token>` or legacy `token <token>`
// header into its credential. Returns an error only for missing scheme
// or empty credential — token-shape validation is delegated to
// platform-api, which knows about all supported formats (current
// `bp_<64-hex>`, legacy `<40-hex>`, and OAuth-minted mcpSession
// tokens). Doing format validation here would couple the MCP edge to
// whichever shapes happen to be valid this week.
func extractToken(header string) (string, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("malformed Authorization header")
	}
	scheme := strings.ToLower(parts[0])
	if scheme != "bearer" && scheme != "token" {
		return "", fmt.Errorf("unsupported Authorization scheme %q (use Bearer or token)", parts[0])
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", fmt.Errorf("empty Authorization credential")
	}
	return token, nil
}

// withCORS opens CORS for browser-based MCP clients (Claude.ai,
// ChatGPT). Only the headers MCP actually needs are exposed. No
// credentials cookie — we use bearer tokens.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Mcp-Session-Id, Mcp-Protocol-Version, Accept")
			w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id, Mcp-Protocol-Version")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s -> %d %s",
			r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo a client-supplied X-Request-Id, otherwise mint a tiny one
		// from the start time. CloudWatch-side correlation is plenty.
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = fmt.Sprintf("mcp-%d", time.Now().UnixNano())
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
