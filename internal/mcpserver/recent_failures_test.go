package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// get_recent_failures fetches each submission's failed tests concurrently but
// MUST merge them in submission order. Submissions arrive newest-first and the
// most_recent_* fields are taken from the first sighting of a testCaseId and
// never overwritten, so merging in completion order would attribute the wrong
// build URL, message and timestamp to any test that failed more than once.
//
// These tests make completion order deliberately disagree with submission
// order: the NEWEST submission is served slowest, so a naive
// merge-as-responses-arrive implementation reports the oldest build instead.

// newFailuresServer serves a submissions list plus per-submission failed tests.
// delays[submissionID] controls how long that submission's tests take, letting
// a test invert completion order relative to submission order.
func newFailuresServer(t *testing.T, subs []map[string]string, tests map[string][]map[string]any, delays map[string]time.Duration, inFlight *int32, maxSeen *int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/api/repos/acme/widgets/submissions", func(w http.ResponseWriter, r *http.Request) {
		// Only the bare list endpoint; per-submission paths are longer.
		if strings.Contains(strings.TrimPrefix(r.URL.Path, "/api/repos/acme/widgets/submissions"), "/") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"submissions": subs})
	})

	mux.HandleFunc("/api/repos/acme/widgets/submissions/", func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(inFlight, 1)
		for {
			old := atomic.LoadInt32(maxSeen)
			if cur <= old || atomic.CompareAndSwapInt32(maxSeen, old, cur) {
				break
			}
		}
		defer atomic.AddInt32(inFlight, -1)

		// .../submissions/{id}/tests
		rest := strings.TrimPrefix(r.URL.Path, "/api/repos/acme/widgets/submissions/")
		id := strings.TrimSuffix(rest, "/tests")

		if d, ok := delays[id]; ok {
			time.Sleep(d)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tests": tests[id]})
	})

	return httptest.NewServer(mux)
}

func failedTestDoc(testCaseID, name, ranAt, message string) map[string]any {
	return map[string]any{
		"test_case_id": testCaseID,
		"name":         name,
		"ran_at":       ranAt,
		"message":      message,
		"duration_us":  1000,
	}
}

func TestGetRecentFailuresMergesInSubmissionOrderNotCompletionOrder(t *testing.T) {
	// sub-new is newest (listed first) but is served SLOWEST, so it completes
	// last. The most_recent_* fields must still come from it.
	subs := []map[string]string{
		{"id": "sub-new", "build_url": "https://ci/build/NEWEST"},
		{"id": "sub-mid", "build_url": "https://ci/build/middle"},
		{"id": "sub-old", "build_url": "https://ci/build/oldest"},
	}
	tests := map[string][]map[string]any{
		"sub-new": {failedTestDoc("tc-1", "flaky login", "2026-07-25T10:00:00Z", "newest failure")},
		"sub-mid": {failedTestDoc("tc-1", "flaky login", "2026-07-24T10:00:00Z", "middle failure")},
		"sub-old": {failedTestDoc("tc-1", "flaky login", "2026-07-23T10:00:00Z", "oldest failure")},
	}
	delays := map[string]time.Duration{
		"sub-new": 120 * time.Millisecond, // newest finishes LAST
		"sub-mid": 40 * time.Millisecond,
		"sub-old": 0, // oldest finishes FIRST
	}

	var inFlight, maxSeen int32
	srv := newFailuresServer(t, subs, tests, delays, &inFlight, &maxSeen)
	defer srv.Close()

	out := callRecentFailures(t, srv.URL)

	if len(out.Failures) != 1 {
		t.Fatalf("expected 1 aggregated failure, got %d", len(out.Failures))
	}
	got := out.Failures[0]

	if got.FailureCount != 3 {
		t.Errorf("FailureCount = %d, want 3 (one per submission)", got.FailureCount)
	}
	if got.MostRecentBuildURL != "https://ci/build/NEWEST" {
		t.Errorf("MostRecentBuildURL = %q, want the NEWEST submission's build.\n"+
			"The oldest submission's response arrives first — this is the merge-in-completion-order bug.",
			got.MostRecentBuildURL)
	}
	if got.MostRecentRanAt != "2026-07-25T10:00:00Z" {
		t.Errorf("MostRecentRanAt = %q, want 2026-07-25T10:00:00Z", got.MostRecentRanAt)
	}
	if got.MostRecentMessage == nil || *got.MostRecentMessage != "newest failure" {
		t.Errorf("MostRecentMessage = %v, want \"newest failure\"", got.MostRecentMessage)
	}
}

func TestGetRecentFailuresRunsConcurrentlyButBounded(t *testing.T) {
	// 12 submissions, each slow enough that a serial implementation could not
	// overlap them. Concurrency must exceed 1 (proving parallelism) and never
	// exceed the cap (proving we don't stampede platform-api/DocumentDB).
	const n = 12
	subs := make([]map[string]string, 0, n)
	tests := map[string][]map[string]any{}
	delays := map[string]time.Duration{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("sub-%02d", i)
		subs = append(subs, map[string]string{"id": id, "build_url": "https://ci/" + id})
		tests[id] = []map[string]any{failedTestDoc(fmt.Sprintf("tc-%02d", i), "t", "2026-07-25T10:00:00Z", "m")}
		delays[id] = 30 * time.Millisecond
	}

	var inFlight, maxSeen int32
	srv := newFailuresServer(t, subs, tests, delays, &inFlight, &maxSeen)
	defer srv.Close()

	out := callRecentFailures(t, srv.URL)

	if len(out.Failures) != n {
		t.Errorf("got %d unique failures, want %d", len(out.Failures), n)
	}
	peak := atomic.LoadInt32(&maxSeen)
	if peak < 2 {
		t.Errorf("peak concurrency = %d — requests were serialized, the fan-out is not working", peak)
	}
	if peak > 6 {
		t.Errorf("peak concurrency = %d, exceeds the cap of 6 — unbounded fan-out would move the "+
			"bottleneck onto the shared DocumentDB cluster", peak)
	}
}

func TestGetRecentFailuresSkipsErroringSubmissions(t *testing.T) {
	// A single failing submission must not sink the whole response.
	subs := []map[string]string{
		{"id": "sub-ok", "build_url": "https://ci/ok"},
		{"id": "sub-boom", "build_url": "https://ci/boom"},
	}
	tests := map[string][]map[string]any{
		"sub-ok": {failedTestDoc("tc-1", "works", "2026-07-25T10:00:00Z", "m")},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/repos/acme/widgets/submissions", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(strings.TrimPrefix(r.URL.Path, "/api/repos/acme/widgets/submissions"), "/") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"submissions": subs})
	})
	mux.HandleFunc("/api/repos/acme/widgets/submissions/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "sub-boom") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tests": tests["sub-ok"]})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out := callRecentFailures(t, srv.URL)

	if len(out.Failures) != 1 {
		t.Fatalf("expected the healthy submission to still be aggregated, got %d failures", len(out.Failures))
	}
	if out.Failures[0].MostRecentBuildURL != "https://ci/ok" {
		t.Errorf("MostRecentBuildURL = %q, want https://ci/ok", out.Failures[0].MostRecentBuildURL)
	}
	if out.SubmissionsInspected != 2 {
		t.Errorf("SubmissionsInspected = %d, want 2 (both were attempted)", out.SubmissionsInspected)
	}
}

func TestGetRecentFailuresCollapsesRetriesWithinOneSubmission(t *testing.T) {
	// Retries of the same test inside ONE submission count once.
	subs := []map[string]string{{"id": "sub-1", "build_url": "https://ci/one"}}
	tests := map[string][]map[string]any{
		"sub-1": {
			failedTestDoc("tc-1", "retried", "2026-07-25T10:00:00Z", "attempt 1"),
			failedTestDoc("tc-1", "retried", "2026-07-25T10:01:00Z", "attempt 2"),
			failedTestDoc("tc-1", "retried", "2026-07-25T10:02:00Z", "attempt 3"),
		},
	}
	var inFlight, maxSeen int32
	srv := newFailuresServer(t, subs, tests, map[string]time.Duration{}, &inFlight, &maxSeen)
	defer srv.Close()

	out := callRecentFailures(t, srv.URL)

	if len(out.Failures) != 1 {
		t.Fatalf("expected 1 unique failure, got %d", len(out.Failures))
	}
	if out.Failures[0].FailureCount != 1 {
		t.Errorf("FailureCount = %d, want 1 — retries within a submission are one failure",
			out.Failures[0].FailureCount)
	}
}

func callRecentFailures(t *testing.T, baseURL string) recentFailuresOutput {
	t.Helper()
	c := NewClient(baseURL, "test-token")
	handler := getRecentFailures(c)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, out, err := handler(ctx, nil, recentFailuresInput{
		Owner:          "acme",
		Name:           "widgets",
		OrganizationID: "org-1",
	})
	if err != nil {
		t.Fatalf("getRecentFailures returned error: %v", err)
	}
	return out
}
