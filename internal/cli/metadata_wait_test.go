package cli

import (
	"bytes"
	"context"
	"errors"
	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRefreshRunIDsFailClosed(t *testing.T) {
	for _, v := range []any{nil, map[string]any{"queued": true}, map[string]any{"runIds": []any{"a"}, "trackingComplete": false}, map[string]any{"trackingComplete": true, "runIds": []any{nil}}} {
		if _, err := refreshRunIDs(v); err == nil {
			t.Fatal("untracked admission accepted")
		}
	}
	ids, err := refreshRunIDs(map[string]any{"trackingComplete": true, "runIds": []any{"a", "a", "b"}})
	if err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
}
func TestRefreshTerminalStates(t *testing.T) {
	for _, state := range []string{"failed", "partially_completed", "cancelled", "timed_out", "paused", "skipped", "unknown"} {
		done, err := refreshRunState(state)
		if !done || err == nil {
			t.Fatal(state)
		}
	}
	for _, state := range []string{"queued", "running", "cancel_requested"} {
		done, err := refreshRunState(state)
		if done || err != nil {
			t.Fatal(state)
		}
	}
	done, err := refreshRunState("done")
	if !done || err != nil {
		t.Fatal("done")
	}
}
func TestRefreshWaitUsesSubmittedIdentitiesOnly(t *testing.T) {
	previous := rt
	defer func() { rt = previous }()
	rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Format: "json"}, Out: io.Discard, Err: io.Discard}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/metadata/p/operations/a" && r.URL.Path != "/metadata/p/operations/b" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		id := r.URL.Path[len(r.URL.Path)-1:]
		_, _ = w.Write([]byte(`{"id":"` + id + `","projectId":"p","kind":"refresh","status":"done"}`))
	}))
	defer server.Close()
	err := waitMetadataOperations(api.New(server.URL, "test", time.Second), "p", map[string]any{"trackingComplete": true, "runIds": []any{"a", "b"}}, time.Second)
	if err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
}
func TestRefreshWaitRejectsWrongGeneration(t *testing.T) {
	previous := rt
	defer func() { rt = previous }()
	rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Format: "json"}, Out: io.Discard, Err: io.Discard}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"different","projectId":"p","kind":"refresh","status":"done"}`))
	}))
	defer server.Close()
	if err := waitMetadataOperations(api.New(server.URL, "test", time.Second), "p", map[string]any{"trackingComplete": true, "runIds": []any{"a"}}, time.Second); err == nil {
		t.Fatal("wrong generation accepted")
	}
}

// DEV-4133: whatever ends the wait, the run IDs are printed; transient failures are waited out; one exit code for
// a timeout.
func waitRuntime(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous, initial, limit := rt, refreshPollInitial, refreshTransientLimit
	t.Cleanup(func() { rt, refreshPollInitial, refreshTransientLimit = previous, initial, limit })
	refreshPollInitial = 10 * time.Millisecond
	out := &bytes.Buffer{}
	rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Format: "json"}, Out: out, Err: io.Discard}
	return out
}

func exitCode(err error) int {
	var exit *api.ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}
	return -1
}

var oneRun = map[string]any{"trackingComplete": true, "runIds": []any{"run-1"}}

func TestRefreshWaitPrintsRunIDsWhenItStopsOnAnError(t *testing.T) {
	out := waitRuntime(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":403,"code":"PERMISSION_REQUIRED","detail":"no"}`))
	}))
	defer server.Close()
	if err := waitMetadataOperations(api.New(server.URL, "t", time.Second), "p", oneRun, time.Second); err == nil {
		t.Fatal("a 403 ended the wait without an error")
	}
	if !strings.Contains(out.String(), "run-1") {
		t.Fatalf("run IDs not printed: %q", out.String())
	}
}

func TestRefreshWaitRidesOutOneDroppedConnection(t *testing.T) {
	waitRuntime(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"run-1","projectId":"p","kind":"refresh","status":"done"}`))
	}))
	defer server.Close()
	if err := waitMetadataOperations(api.New(server.URL, "t", time.Second), "p", oneRun, 5*time.Second); err != nil {
		t.Fatalf("one dropped connection ended the wait: %v", err)
	}
}

func TestRefreshWaitTimeoutIsOneExitCodeWithTheRunIDs(t *testing.T) {
	for _, slow := range []time.Duration{0, 300 * time.Millisecond} {
		out := waitRuntime(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(slow) // 0: the deadline passes between polls; 300 ms: during a request
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"run-1","projectId":"p","kind":"refresh","status":"running"}`))
		}))
		err := waitMetadataOperations(api.New(server.URL, "t", time.Second), "p", oneRun, 100*time.Millisecond)
		server.Close()
		if exitCode(err) != api.ExitRetryable {
			t.Fatalf("slow=%s: exit %d (%v), want %d", slow, exitCode(err), err, api.ExitRetryable)
		}
		if !strings.Contains(out.String(), "run-1") {
			t.Fatalf("slow=%s: run IDs not printed", slow)
		}
	}
}

func TestRefreshWaitInterruptedStays130(t *testing.T) {
	waitRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	rt.Ctx = ctx
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"run-1","projectId":"p","kind":"refresh","status":"running"}`))
	}))
	defer server.Close()
	if code := exitCode(waitMetadataOperations(api.New(server.URL, "t", time.Second), "p", oneRun, time.Minute)); code != api.ExitInterrupted {
		t.Fatalf("exit %d, want %d", code, api.ExitInterrupted)
	}
}
