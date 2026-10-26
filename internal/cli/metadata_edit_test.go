package cli

import (
	"context"
	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMetadataDescriptionEditKeepsVersionAndInheritance(t *testing.T) {
	body := map[string]any{"tableDescription": "new"}
	current := map[string]any{"version": float64(12), "useWithAI": false, "useWithAISetting": "inherit", "columns": []any{"do not copy"}}
	if err := prepareMetadataEdit(body, current); err != nil {
		t.Fatal(err)
	}
	if body["version"] != float64(12) || body["useWithAISetting"] != "inherit" || body["useWithAI"] != false {
		t.Fatal(body)
	}
	if len(body["columns"].([]any)) != 0 {
		t.Fatal("unrequested columns copied")
	}
}
func TestMetadataExplicitAiChangeDoesNotCopyOldSetting(t *testing.T) {
	body := map[string]any{"useWithAI": true, "version": float64(9)}
	if err := prepareMetadataEdit(body, map[string]any{"version": float64(12), "useWithAISetting": "off"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["useWithAISetting"]; ok {
		t.Fatal("old setting defeats requested toggle")
	}
	if body["version"] != float64(9) {
		t.Fatal("stale caller revision replaced")
	}
}
func TestMetadataEditFailsClosedWithoutVersion(t *testing.T) {
	for _, current := range []map[string]any{nil, {}, {"version": nil}} {
		if err := prepareMetadataEdit(map[string]any{}, current); err == nil {
			t.Fatal("unversioned write allowed")
		}
	}
	if err := prepareMetadataEdit(map[string]any{"version": nil}, map[string]any{"version": float64(1)}); err == nil {
		t.Fatal("null bypass allowed")
	}
}

func TestMetadataEditUsesExactReadAndDoesNotFallback(t *testing.T) {
	for _, status := range []int{200, 403, 404, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.EscapedPath() != "/metadata/p/tables/c/care%20team/patient%23record" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == 200 {
					_, _ = w.Write([]byte(`{"metadata":{"version":4,"useWithAI":false,"useWithAISetting":"inherit"}}`))
				}
			}))
			defer server.Close()
			previous := rt
			defer func() { rt = previous }()
			rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Project: "p"}, client: api.New(server.URL, "test", time.Second)}
			c := &callCtx{Params: map[string]string{"catalog": "c", "schema": "care team", "table": "patient#record"}, Body: map[string]any{"tableDescription": "edited"}}
			err := mergeTableMetadata(c)
			if status == 200 && err != nil {
				t.Fatal(err)
			}
			if status != 200 && err == nil {
				t.Fatal("failure ignored")
			}
			if calls < 1 || (status != 503 && calls != 1) {
				t.Fatalf("unexpected request count: %d", calls)
			}
		})
	}
}

// DEV-4133: a table inheriting "on" can be pinned to its own "on"; the setting is sent alone.
func TestMetadataAiSettingPinsTheTableOwnSetting(t *testing.T) {
	body := map[string]any{"useWithAISetting": "on"}
	if err := prepareMetadataEdit(body, map[string]any{"version": float64(4), "useWithAI": true, "useWithAISetting": "inherit"}); err != nil {
		t.Fatal(err)
	}
	if body["useWithAISetting"] != "on" || body["version"] != float64(4) {
		t.Fatal(body)
	}
	if _, copied := body["useWithAI"]; copied {
		t.Fatal("the current boolean was copied next to an explicit setting")
	}
	if err := prepareMetadataEdit(map[string]any{"useWithAISetting": "yes"}, map[string]any{"version": float64(4)}); err == nil {
		t.Fatal("an unknown setting was accepted")
	}
}
