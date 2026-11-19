package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if err == nil || !strings.Contains(err.Error(), "2 warnings") || strings.Count(err.Error(), "PEAKA_LINEAGE_UNKNOWN") != 1 ||
		!strings.Contains(err.Error(), "PEAKA_GOVERNANCE_WARNINGS_UNAVAILABLE") {
		t.Errorf("names each warning once: %v", err)
	}
}

// Codex review (N02): with --fail-on-warning a result longer than --max-rows is read to its end, not shown, so a
// warning that comes only after the limit is seen; in table, JSON and quiet output alike. Without the flag the query
// is cancelled at the limit, as before.
func TestFailOnWarningReadsPastTheRowLimit(t *testing.T) {
	var srv *httptest.Server
	var requests []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.Method+" "+req.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		cols := `"columns":[{"name":"n","type":"integer"}]`
		switch {
		case req.Method == http.MethodDelete:
			fmt.Fprint(w, `{}`)
		case req.Method == http.MethodPost:
			fmt.Fprintf(w, `{"id":"q1","nextUri":"%s/v1/statement/q1/1","stats":{"state":"QUEUED"}}`, srv.URL)
		case strings.HasSuffix(req.URL.Path, "/1"):
			fmt.Fprintf(w, `{"id":"q1",%s,"data":[[1],[2],[3]],"nextUri":"%s/v1/statement/q1/2","stats":{"state":"RUNNING"}}`, cols, srv.URL)
		case strings.HasSuffix(req.URL.Path, "/2"):
			fmt.Fprintf(w, `{"id":"q1",%s,"data":[[4],[5]],"nextUri":"%s/v1/statement/q1/3","stats":{"state":"RUNNING"}}`, cols, srv.URL)
		default: // the last page: no rows, the warning
			fmt.Fprintf(w, `{"id":"q1",%s,"stats":{"state":"FINISHED"},
				"warnings":[{"warningCode":{"code":1,"name":"PEAKA_LINEAGE_UNKNOWN"},"message":"PEAKA_LINEAGE_UNKNOWN: peaka.mtquery.sales"}]}`, cols)
		}
	}))
	defer srv.Close()
	previous, wasQuiet := rt, gf.quiet
	defer func() { rt, gf.quiet = previous, wasQuiet }()

	for _, mode := range []struct {
		name, format string
		quiet        bool
	}{{"table", "table", false}, {"json", "json", false}, {"quiet", "json", true}} {
		run := func(fail bool) (string, error) {
			requests = nil
			gf.quiet = mode.quiet
			var out, errOut strings.Builder
			rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Format: mode.format, Source: map[string]string{}}, Out: &out, Err: &errOut}
			s := &sqlSession{f: &sqlFlags{failOnWarn: fail, maxRows: 2}, client: &trino.Client{Endpoint: srv.URL, HTTP: srv.Client()}, out: &out}
			err := s.run(context.Background(), "SELECT n FROM peaka.mtquery.sales")
			return out.String(), err
		}

		out, err := run(true)
		var exit *api.ExitError
		if !errors.As(err, &exit) || exit.Code != api.ExitFailed || !strings.Contains(err.Error(), "PEAKA_LINEAGE_UNKNOWN") {
			t.Errorf("%s: the warning after the limit is seen: %v", mode.name, err)
		}
		if strings.Contains(out, "3") || !strings.Contains(out, "2") {
			t.Errorf("%s: only the first 2 rows are shown: %q", mode.name, out)
		}
		if mode.format == "json" {
			var rows []map[string]any
			if json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 2 {
				t.Errorf("%s: the output is valid JSON of 2 rows: %q", mode.name, out)
			}
		}
		if strings.Join(requests, ",") != "POST /v1/statement,GET /v1/statement/q1/1,GET /v1/statement/q1/2,GET /v1/statement/q1/3" {
			t.Errorf("%s: every page is read, nothing cancelled: %v", mode.name, requests)
		}

		if _, err := run(false); err != nil {
			t.Errorf("%s: without the flag the limit ends it: %v", mode.name, err)
		}
		if len(requests) != 3 || requests[2] != "DELETE /v1/statement/q1/2" {
			t.Errorf("%s: without the flag the query is cancelled at the limit: %v", mode.name, requests)
		}
	}
}

// A script goes on after a warned statement only without --stop-on-error; the first failure is the exit.
func TestFailOnWarningInAScript(t *testing.T) {
	var srv *httptest.Server
	posts := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Method == http.MethodPost {
			posts++
			fmt.Fprintf(w, `{"id":"q%d","nextUri":"%s/v1/statement/q%d/1","stats":{"state":"QUEUED"}}`, posts, srv.URL, posts)
			return
		}
		fmt.Fprint(w, `{"id":"q","columns":[{"name":"n","type":"integer"}],"data":[[1]],"stats":{"state":"FINISHED"},
			"warnings":[{"warningCode":{"name":"PEAKA_GOVERNANCE_WARNINGS_UNAVAILABLE"},"message":"could not be read"}]}`)
	}))
	defer srv.Close()
	previous := rt
	defer func() { rt = previous }()
	for _, stop := range []bool{true, false} {
		posts = 0
		rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Format: "json", Source: map[string]string{}}, Out: io.Discard, Err: io.Discard}
		s := &sqlSession{f: &sqlFlags{failOnWarn: true, stopOnError: stop}, client: &trino.Client{Endpoint: srv.URL, HTTP: srv.Client()}, out: io.Discard}
		err := s.runScript("SELECT 1; SELECT 2")
		var exit *api.ExitError
		if !errors.As(err, &exit) || exit.Code != api.ExitFailed {
			t.Errorf("stop-on-error %v: exit 8: %v", stop, err)
		}
		if want := map[bool]int{true: 1, false: 2}[stop]; posts != want {
			t.Errorf("stop-on-error %v: %d statements ran, want %d", stop, posts, want)
		}
	}
}
