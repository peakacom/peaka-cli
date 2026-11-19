package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
	"github.com/peakacom/peaka-cli/internal/trino"
)

// N02 (DEV-4250): --fail-on-warning makes a governance warning (PEAKA_LINEAGE_UNKNOWN) exit 8, after the rows.
func TestFailOnWarning(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Method == http.MethodPost { // the statement is queued; its rows and warnings come on the next page
			fmt.Fprintf(w, `{"id":"q1","nextUri":"%s/v1/statement/q1/1","stats":{"state":"QUEUED"}}`, srv.URL)
			return
		}
		fmt.Fprint(w, `{"id":"q1","columns":[{"name":"n","type":"integer"}],"data":[[42]],"stats":{"state":"FINISHED"},
			"warnings":[{"warningCode":{"code":1,"name":"PEAKA_LINEAGE_UNKNOWN"},"message":"PEAKA_LINEAGE_UNKNOWN: peaka.mtquery.sales"}]}`)
	}))
	defer srv.Close()
	previous := rt
	defer func() { rt = previous }()
	var out, errOut strings.Builder
	rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Format: "json", Source: map[string]string{}}, Out: &out, Err: &errOut}

	run := func(fail bool) error {
		s := &sqlSession{f: &sqlFlags{failOnWarn: fail}, client: &trino.Client{Endpoint: srv.URL, HTTP: srv.Client()}, out: &out}
		return s.run(context.Background(), "SELECT n FROM peaka.mtquery.sales")
	}
	if err := run(false); err != nil {
		t.Fatalf("without the flag a warning is printed, not a failure: %v", err)
	}
	if !strings.Contains(errOut.String(), "warning: PEAKA_LINEAGE_UNKNOWN") {
		t.Errorf("the warning is printed: %q", errOut.String())
	}
	out.Reset()
	err := run(true)
	var exit *api.ExitError
	if !errors.As(err, &exit) || exit.Code != api.ExitFailed || !strings.Contains(err.Error(), "PEAKA_LINEAGE_UNKNOWN") {
		t.Fatalf("with the flag: %v", err)
	}
	if !strings.Contains(out.String(), "42") {
		t.Errorf("the rows are still printed: %q", out.String())
	}

	if warningFailure(nil) != nil {
		t.Error("no warning, no failure")
	}
	err = warningFailure([]trino.Warning{{Code: map[string]any{"name": "PEAKA_LINEAGE_UNKNOWN"}}, {Code: map[string]any{"name": "PEAKA_LINEAGE_UNKNOWN"}},
		{Message: "PEAKA_GOVERNANCE_WARNINGS_UNAVAILABLE: could not be read"}})
	if err == nil || !strings.Contains(err.Error(), "3 warnings") || strings.Count(err.Error(), "PEAKA_LINEAGE_UNKNOWN") != 1 ||
		!strings.Contains(err.Error(), "PEAKA_GOVERNANCE_WARNINGS_UNAVAILABLE") {
		t.Errorf("names each warning once: %v", err)
	}
}
