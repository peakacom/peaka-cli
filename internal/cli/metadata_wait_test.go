package cli

import (
	"context"
	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
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
