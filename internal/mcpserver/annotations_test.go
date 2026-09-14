package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Every tool must advertise a human-readable title in both places MCP
// clients look for one (Tool.Title and Tool.Annotations.Title) and must
// declare itself read-only — this server ships no write tools, and
// clients such as Claude and Cursor use ReadOnlyHint to skip
// confirmation prompts. A blank Annotations.Title falls back to the
// snake_case name in some clients' tool pickers, which is what this
// test exists to prevent.
func TestEveryToolHasTitleAndReadOnlyHint(t *testing.T) {
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer platform.Close()

	cs := newTestServerSession(t, platform)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	const wantTools = 9
	if len(res.Tools) != wantTools {
		t.Errorf("registered %d tools, want %d — update the landing page, llms.txt and READMEs if this changed", len(res.Tools), wantTools)
	}
	for _, tool := range res.Tools {
		if tool.Title == "" {
			t.Errorf("tool %q has no Title", tool.Name)
		}
		if tool.Annotations == nil {
			t.Errorf("tool %q has no Annotations", tool.Name)
			continue
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q is not marked ReadOnlyHint", tool.Name)
		}
		if tool.Annotations.Title == "" {
			t.Errorf("tool %q has no Annotations.Title", tool.Name)
		} else if tool.Annotations.Title != tool.Title {
			t.Errorf("tool %q Annotations.Title %q != Title %q", tool.Name, tool.Annotations.Title, tool.Title)
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no Description", tool.Name)
		}
	}
}
