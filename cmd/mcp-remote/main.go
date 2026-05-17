// Command mcp-remote is the BuildPulse Model Context Protocol server
// over Streamable HTTP. It is the hosted variant of cmd/mcp, deployed
// at https://mcp.buildpulse.io for clients that can't spawn local
// stdio servers — Claude.ai web, Claude Desktop integrations panel,
// ChatGPT Connectors, etc.
//
// Authentication is per-session: the Streamable HTTP client supplies
// an Authorization header (`Bearer <40-hex>` or `token <40-hex>`) on
// every MCP request. The server validates the format locally, then
// builds a request-scoped MCP server bound to that token. Tokens are
// the same 40-hex tokens used by the platform-api itself, so customer
// integrations work without provisioning a second credential.
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
	"regexp"
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
	wellKnownOAuth   = "/.well-known/oauth-authorization-server"
	wellKnownMCP     = "/.well-known/mcp"
)

// tokenRegexp validates the 40-char hex token shape before we ever
// touch the platform API. Mirrors the regex on the platform-api side.
var tokenRegexp = regexp.MustCompile(`^[0-9a-f]{40}$`)

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

	mux := http.NewServeMux()

	mux.HandleFunc("GET "+healthPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// The Streamable HTTP MCP transport. The Go SDK's
	// NewStreamableHTTPHandler asks us for a *Server per inbound
	// request — we use that hook to bind the request's Authorization
	// token to the server's outbound calls.
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
		nil,
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
					"description": "40-character BuildPulse API token (created at https://app.buildpulse.io)",
				},
			},
			"documentation": "https://platform.buildpulse.io/docs/mcp",
		})
	})

	// OAuth authorization-server metadata. We do not implement the
	// flow yet — this stub returns a `Not Implemented` body that
	// directs clients to use the Bearer token form for now. When we
	// wire OAuth, this endpoint advertises authorization_endpoint /
	// token_endpoint / scopes_supported per RFC 8414.
	mux.HandleFunc("GET "+wellKnownOAuth, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "oauth_not_yet_supported",
			"error_description": "Use Bearer <40-hex BuildPulse API token> for now. See https://platform.buildpulse.io/docs/mcp.",
		})
	})

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

// extractToken parses a `Bearer <hex>` or legacy `token <hex>` header
// into its 40-char hex token. Returns an error for missing / malformed
// inputs. The hex validation here is the same shape we enforce on the
// platform-api side (see internal/middleware/auth.go) — no point
// taking a round trip just to discover a malformed token.
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
	if !tokenRegexp.MatchString(token) {
		return "", fmt.Errorf("token is not a 40-char lowercase hex string")
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
