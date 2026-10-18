package cli

import (
	"bytes"
	"context"
	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMetadataListKeepsEnvelopeAndNeverFallsBack(t *testing.T) {
	for _, status := range []int{200, 403, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/metadata/p/tables" || r.URL.Query().Get("limit") != "1" {
					t.Errorf("unexpected request %s", r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == 200 {
					_, _ = w.Write([]byte(`{"items":[],"hasMore":true,"nextCursor":"next"}`))
				}
			}))
			defer server.Close()
			old := rt
			defer func() { rt = old }()
			var out bytes.Buffer
			rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Project: "p", Format: "json"}, Out: &out, Err: io.Discard, client: api.New(server.URL, "test", time.Second)}
			cmd := newMetadataCmd()
			cmd.SetArgs([]string{"list", "--limit", "1"})
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if status == 200 && (err != nil || !bytes.Contains(out.Bytes(), []byte(`"nextCursor"`))) {
				t.Fatal(err, out.String())
			}
			if status != 200 && err == nil {
				t.Fatal("unsupported or forbidden response ignored")
			}
			if calls != 1 {
				t.Fatalf("unexpected fallback or additional page: %d", calls)
			}
		})
	}
}
