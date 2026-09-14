package main

import (
	"embed"
	"net/http"
)

// Public, unauthenticated documentation surfaces for the hosted server.
//
// mcp.buildpulse.io used to 404 on everything except /mcp, /health and
// the OAuth + well-known routes, so a person (or a crawler) landing on
// the bare host learned nothing. These routes give the host a
// server-rendered landing page, a robots.txt that welcomes search and
// AI crawlers, a sitemap, and an llms.txt (https://llmstxt.org) so
// agents can read what the server offers without opening an MCP
// session.
//
// The files live under ./public and are embedded at build time, so
// the distroless runtime image needs nothing beyond the binary.
// Everything here is static and cacheable; nothing reads a request
// header or touches a datastore.
//
//go:embed public/index.html public/robots.txt public/sitemap.xml public/llms.txt
var publicFS embed.FS

// publicCacheControl is short on purpose: CloudFront/ALB sit in front
// of this, and a deploy should be visible within minutes.
const publicCacheControl = "public, max-age=300"

// registerPublicRoutes mounts the documentation surfaces on mux.
//
// The landing page is registered on the exact-match pattern `GET /{$}`
// (Go 1.22+ ServeMux) so it never shadows /mcp, /health, /oauth/* or
// /.well-known/*, and unknown paths still fall through to the mux's
// default 404.
func registerPublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", servePublicFile("public/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /robots.txt", servePublicFile("public/robots.txt", "text/plain; charset=utf-8"))
	mux.HandleFunc("GET /sitemap.xml", servePublicFile("public/sitemap.xml", "application/xml; charset=utf-8"))
	mux.HandleFunc("GET /llms.txt", servePublicFile("public/llms.txt", "text/plain; charset=utf-8"))
}

// servePublicFile returns a handler that writes one embedded file with
// the given content type. The file is read from the embed FS on each
// request; it is a few KB and embed.FS reads are in-memory, so there
// is nothing to cache in-process.
func servePublicFile(name, contentType string) http.HandlerFunc {
	body, err := publicFS.ReadFile(name)
	if err != nil {
		// Only reachable if the go:embed directive and the path above
		// disagree — a build-time mistake, not a runtime condition.
		panic("mcp-remote: missing embedded public file " + name + ": " + err.Error())
	}
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", contentType)
		h.Set("Cache-Control", publicCacheControl)
		h.Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}
