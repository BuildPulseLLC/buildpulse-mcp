package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestServerJSONMeetsRegistryLimits pins the MCP Registry's validation rules
// for server.json so a release does not fail at the very last job. The
// registry rejected mcp-v0.2.0 with "expected length <= 100" on
// body.description after npm had already published the version.
func TestServerJSONMeetsRegistryLimits(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "server.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read server.json: %v", err)
	}
	var sj struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Version     string `json:"version"`
		Packages    []struct {
			Identifier string `json:"identifier"`
			Version    string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(raw, &sj); err != nil {
		t.Fatalf("server.json is not valid JSON: %v", err)
	}
	if sj.Name != "io.github.BuildPulseLLC/buildpulse-mcp" {
		t.Errorf("name = %q", sj.Name)
	}
	if n := len([]rune(sj.Description)); n == 0 || n > 100 {
		t.Errorf("description is %d characters; the MCP Registry requires 1..100: %q", n, sj.Description)
	}
	if n := len([]rune(sj.Title)); n == 0 || n > 100 {
		t.Errorf("title is %d characters; the MCP Registry requires 1..100", n)
	}
	if len(sj.Packages) == 0 || sj.Packages[0].Identifier != "@buildpulse/mcp" {
		t.Errorf("packages[0] must be @buildpulse/mcp, got %+v", sj.Packages)
	}
	if sj.Version == "" || sj.Packages[0].Version != sj.Version {
		t.Errorf("version %q and packages[0].version %q must match (release.yml syncs both from the tag)", sj.Version, sj.Packages[0].Version)
	}
}
