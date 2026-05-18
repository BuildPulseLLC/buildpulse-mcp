// Command mcp is the BuildPulse Model Context Protocol stdio server.
//
// It exposes the BuildPulse Platform API as a set of intent-shaped
// tools (find_flaky_tests, get_test_history, …) plus a small library
// of guided prompts and resources. The transport is stdio, suitable
// for Claude Desktop, Cursor, Cline, Continue, Windsurf, Zed, VS Code
// Copilot, and any other MCP-aware agent that spawns local servers.
//
// For the hosted Streamable HTTP variant (used by Claude.ai web and
// ChatGPT), see cmd/mcp-remote.
//
// Configuration:
//
//	BUILDPULSE_TOKEN   Required. BuildPulse API token from
//	                   app.buildpulse.io. Either shape works:
//	                   `bp_<64-hex>` (current) or `<40-hex>` (legacy).
//	PLATFORM_API_URL   Optional. Default https://platform.buildpulse.io.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/BuildPulseLLC/buildpulse-mcp/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	token := strings.TrimSpace(os.Getenv("BUILDPULSE_TOKEN"))
	if token == "" {
		// Stderr only — stdout is the MCP transport channel.
		fmt.Fprintln(os.Stderr, "buildpulse-mcp: BUILDPULSE_TOKEN environment variable is required.")
		fmt.Fprintln(os.Stderr, "Get a token at https://app.buildpulse.io → Organization Settings → API Tokens.")
		os.Exit(1)
	}

	client := mcpserver.NewClient(os.Getenv("PLATFORM_API_URL"), token)
	server := mcpserver.New(client)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.SetOutput(os.Stderr)
	log.SetPrefix("buildpulse-mcp: ")

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		log.Printf("server terminated: %v", err)
		os.Exit(1)
	}
}
