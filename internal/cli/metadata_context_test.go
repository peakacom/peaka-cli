package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
)

// apiRequest is what a fake gateway received.
type apiRequest struct {
	Method, Path string
	Query        url.Values
	Body         map[string]any
}

// fakeGateway answers by path; the catalog and query lists are always there for the resolvers.
func fakeGateway(t *testing.T, answers map[string]string) (*httptest.Server, *[]apiRequest) {
	t.Helper()
	var seen []apiRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := apiRequest{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query()}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &req.Body)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Path {
		case "/data/projects/p/catalogs":
			_, _ = w.Write([]byte(`[{"id":"1","name":"peaka"},{"id":"2","name":"peaka"},{"id":"111","name":"stripe"}]`))
			return
		case "/data/projects/p/catalogs/1/schemas":
			_, _ = w.Write([]byte(`[{"schemaName":"table"},{"schemaName":"bitable"}]`))
			return
		case "/data/projects/p/catalogs/2/schemas":
			_, _ = w.Write([]byte(`[{"schemaName":"query"},{"schemaName":"mtquery"}]`))
			return
		case "/data/projects/p/catalogs/111/schemas":
			_, _ = w.Write([]byte(`[{"schemaName":"payment"}]`))
			return
		case "/data/projects/p/queries":
			_, _ = w.Write([]byte(`[{"id":"q1","displayName":"charges"}]`))
			return
		}
		seen = append(seen, req)
		answer, ok := answers[r.Method+" "+req.Path]
		if !ok {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if strings.HasPrefix(answer, "text:") {
			w.Header().Set("Content-Type", "text/plain")
			answer = strings.TrimPrefix(answer, "text:")
		}
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

// runCmd runs a command against the fake gateway; format "" is the default (not chosen).
func runCmd(t *testing.T, server *httptest.Server, format string, cmd *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	previous := rt
	t.Cleanup(func() { rt = previous })
	settings := &config.Settings{Project: "p", Format: format, Source: map[string]string{}}
	if format == "" {
		settings.Format, settings.Source["format"] = "json", "default"
	}
	var out, errOut bytes.Buffer
	rt = &Runtime{Ctx: context.Background(), Settings: settings, Out: &out, Err: &errOut, client: api.New(server.URL, "test", time.Second)}
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestMetadataDescribeCompactSendsOneParameterPerTable(t *testing.T) {
	server, seen := fakeGateway(t, map[string]string{"GET /metadata/p": `{"catalogs":[],"schemas":[],"tables":[]}`})
	_, _, err := runCmd(t, server, "json", newMetadataCmd(), "describe", "--table", "stripe.payment.customers", "--table", "stripe.payment.charges", "--format-out", "compact", "--samples", "--levels", "schemas,tables")
	if err != nil {
		t.Fatal(err)
	}
	q := (*seen)[0].Query
	if strings.Join(q["table"], "|") != "stripe.payment.customers|stripe.payment.charges" || q.Get("format") != "compact" || q.Get("include") != "samples" || q.Get("levels") != "schemas,tables" {
		t.Fatal(q)
	}
	if _, ok := q["samples"]; ok {
		t.Fatal("samples flag leaked as a query parameter")
	}
}

func TestMetadataDescribeDdlIsPrintedAsText(t *testing.T) {
	ddl := "CREATE TABLE stripe.payment.customers (\n  id varchar\n);\n"
	server, _ := fakeGateway(t, map[string]string{"GET /metadata/p": "text:" + ddl})
	out, _, err := runCmd(t, server, "json", newMetadataCmd(), "describe", "--table", "stripe.payment.customers", "--format-out", "ddl")
	if err != nil || out != ddl {
		t.Fatalf("%v %q", err, out)
	}
}

func TestMetadataDescribeRefusesLevelsWithoutFormatAndUnknownFormats(t *testing.T) {
	server, seen := fakeGateway(t, nil)
	for _, args := range [][]string{{"describe", "--levels", "catalogs"}, {"describe", "--format-out", "yaml"}} {
		if _, _, err := runCmd(t, server, "json", newMetadataCmd(), args...); err == nil {
			t.Fatal(args, "accepted")
		}
	}
	if len(*seen) != 0 {
		t.Fatal("request sent", *seen)
	}
}

func TestTableListPrintsTheListAsText(t *testing.T) {
	answer := `{"status":"listed","scope":{},"tables":2,"tokens":40,"legend":"Grouped by catalog.schema.","list":"## stripe.payment\ncharges | columns: id\ncustomers | columns: id\n"}`
	server, seen := fakeGateway(t, map[string]string{"GET /metadata/p/table-list": answer})
	out, errOut, err := runCmd(t, server, "", newMetadataCmd(), "table-list", "--catalog", "stripe", "--schema", "payment")
	if err != nil {
		t.Fatal(err)
	}
	if out != "## stripe.payment\ncharges | columns: id\ncustomers | columns: id\n" || !strings.Contains(errOut, "Grouped by") {
		t.Fatalf("%q %q", out, errOut)
	}
	if q := (*seen)[0].Query; q.Get("catalogId") != "111" || q.Get("schemaName") != "payment" {
		t.Fatal(q)
	}
	out, _, err = runCmd(t, server, "json", newMetadataCmd(), "table-list")
	if err != nil || !strings.Contains(out, `"status": "listed"`) || !strings.Contains(out, `"legend"`) {
		t.Fatalf("chosen json is not the whole answer: %v %s", err, out)
	}
}

func TestTableListTooLargeShowsTheSchemas(t *testing.T) {
	answer := `{"status":"too_large","reason":"5000 tables","schemas":[{"catalog":"stripe","catalogId":"111","schema":"payment","tables":4999}],"nextAction":"Narrow with catalogId."}`
	server, _ := fakeGateway(t, map[string]string{"GET /metadata/p/table-list": answer})
	out, errOut, err := runCmd(t, server, "", newMetadataCmd(), "table-list")
	if err != nil || !strings.Contains(out, "4999") || !strings.Contains(out, "payment") || !strings.Contains(errOut, "5000 tables") || !strings.Contains(errOut, "Narrow with") {
		t.Fatalf("%v %q %q", err, out, errOut)
	}
}

func TestRelevantTablesPrintsOneLinePerTable(t *testing.T) {
	answer := `{"status":"ready","index":"not_built","coverage":"partial","tables":[{"table":"stripe.payment.customers","line":"stripe.payment.customers [cached]: customers | columns: id"},{"table":"stripe.payment.charges","line":""}]}`
	server, seen := fakeGateway(t, map[string]string{"GET /metadata/p/relevant-tables": answer})
	out, errOut, err := runCmd(t, server, "", newMetadataCmd(), "relevant-tables", "how many customers", "--limit", "3")
	if err != nil {
		t.Fatal(err)
	}
	if out != "stripe.payment.customers [cached]: customers | columns: id\nstripe.payment.charges\n" || !strings.Contains(errOut, "coverage partial") {
		t.Fatalf("%q %q", out, errOut)
	}
	if q := (*seen)[0].Query; q.Get("question") != "how many customers" || q.Get("limit") != "3" {
		t.Fatal(q)
	}
	if _, _, err := runCmd(t, server, "", newMetadataCmd(), "relevant-tables", "x", "--limit", "11"); err == nil {
		t.Fatal("limit 11 accepted")
	}
}

func TestDescriptionsSetSendsTheVersionOnlyWhenGiven(t *testing.T) {
	server, seen := fakeGateway(t, map[string]string{
		"PUT /metadata/p/descriptions/111":         `{"description":"amounts in cents","version":3}`,
		"PUT /metadata/p/descriptions/111/payment": `{"description":"","version":1}`,
		"GET /metadata/p/descriptions":             `{"available":true,"catalogs":[]}`,
	})
	if _, _, err := runCmd(t, server, "json", newMetadataCmd(), "descriptions", "set", "catalog", "stripe", "amounts in cents", "--version", "2"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmd(t, server, "json", newMetadataCmd(), "descriptions", "set", "schema", "stripe.payment", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCmd(t, server, "json", newMetadataCmd(), "descriptions", "list", "--catalog", "stripe"); err != nil {
		t.Fatal(err)
	}
	catalog, schema, list := (*seen)[0], (*seen)[1], (*seen)[2]
	if catalog.Body["description"] != "amounts in cents" || catalog.Body["version"] != float64(2) {
		t.Fatal(catalog.Body)
	}
	if d, ok := schema.Body["description"]; !ok || d != "" {
		t.Fatal("an empty description must be sent to remove it", schema.Body)
	}
	if _, ok := schema.Body["version"]; ok {
		t.Fatal("version sent though not given", schema.Body)
	}
	if list.Query["catalogId"][0] != "111" {
		t.Fatal(list.Query)
	}
}

func TestDescriptionsSetConflictIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/data/projects/p/catalogs" {
			_, _ = w.Write([]byte(`[{"id":"111","name":"stripe"}]`))
			return
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"CONFLICT","status":409,"detail":"changed","params":{"version":5,"description":"newer"}}`))
	}))
	defer server.Close()
	_, _, err := runCmd(t, server, "json", newMetadataCmd(), "descriptions", "set", "catalog", "stripe", "x", "--version", "2")
	var p *api.Problem
	if !asProblem(err, &p) || p.Status != http.StatusConflict || p.Params["version"] != float64(5) {
		t.Fatal(err)
	}
}

func TestQueriesExecuteNotesLiveAndPreparingTables(t *testing.T) {
	answer := `{"columns":[{"columnName":"n"}],"data":[[{"name":"n","dataType":"bigint","value":"3","order":0}]],
		"tables":[{"table":"stripe.payment.charges","cache":"live"},{"table":"stripe.payment.customers","cache":"ready"},{"table":"hubspot.crm.deals","cache":"preparing"}]}`
	server, _ := fakeGateway(t, map[string]string{"POST /data/projects/p/queries/execute": answer})
	out, errOut, err := runCmd(t, server, "json", newQueriesCmd(), "execute", "charges")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "stripe.payment") {
		t.Fatalf("stdout changed: %s", out)
	}
	if !strings.Contains(errOut, "live from the source") || !strings.Contains(errOut, "stripe.payment.charges") ||
		!strings.Contains(errOut, "hubspot.crm.deals") || strings.Contains(errOut, "stripe.payment.customers") {
		t.Fatalf("%q", errOut)
	}
}

func TestPeakaWithASchemaIsTheCatalogThatHoldsIt(t *testing.T) {
	server, seen := fakeGateway(t, map[string]string{
		"GET /metadata/p/table-list": `{"status":"listed","list":""}`,
		"GET /metadata/p":            `{"catalogs":[],"schemas":[],"tables":[]}`,
		"GET /metadata/p/tables":     `{"items":[]}`,
	})
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"table-list", "--catalog", "peaka", "--schema", "query"}, "2"},
		{[]string{"describe", "--catalog", "peaka", "--schema", "table", "--format-out", "compact"}, "1"},
		{[]string{"list", "--catalog", "Peaka", "--schema", "mtquery"}, "2"},
	} {
		*seen = nil
		if _, _, err := runCmd(t, server, "json", newMetadataCmd(), c.args...); err != nil {
			t.Fatal(c.args, err)
		}
		if got := (*seen)[0].Query.Get("catalogId"); got != c.want {
			t.Fatalf("%v: catalogId %q, want %q", c.args, got, c.want)
		}
	}
	for _, args := range [][]string{{"table-list", "--catalog", "peaka"}, {"describe", "--catalog", "peaka"}} {
		*seen = nil
		if _, _, err := runCmd(t, server, "json", newMetadataCmd(), args...); err == nil || len(*seen) != 0 {
			t.Fatalf("%v: peaka without a schema was sent: %v %v", args, err, *seen)
		}
	}
}

func TestExportsCancelSendsNothingForAFinishedExport(t *testing.T) {
	server, seen := fakeGateway(t, map[string]string{
		"GET /data/projects/p/exports/e1":    `{"id":"e1","status":"SUCCEEDED"}`,
		"GET /data/projects/p/exports/e2":    `{"id":"e2","status":"RUNNING"}`,
		"DELETE /data/projects/p/exports/e2": ``,
	})
	_, errOut, err := runCmd(t, server, "json", newExportsCmd(), "cancel", "e1")
	if err != nil || !strings.Contains(errOut, "already finished (SUCCEEDED)") {
		t.Fatalf("%v %q", err, errOut)
	}
	if len(*seen) != 1 {
		t.Fatalf("a cancel was sent for a finished export: %v", *seen)
	}
	*seen = nil
	if _, _, err := runCmd(t, server, "json", newExportsCmd(), "cancel", "e2"); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 || (*seen)[1].Method != http.MethodDelete {
		t.Fatalf("a running export was not cancelled: %v", *seen)
	}
}

func TestExportLeavesSystemColumnsOutUnlessAsked(t *testing.T) {
	server, seen := fakeGateway(t, map[string]string{
		"POST /data/projects/p/catalogs/111/schemas/payment/tables/charges/exports": `{"id":"e1","status":"PENDING"}`,
	})
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"export", "stripe.payment.charges"}, false},
		{[]string{"export", "stripe.payment.charges", "--system-columns"}, true},
	} {
		*seen = nil
		if _, _, err := runCmd(t, server, "json", newTablesCmd(), c.args...); err != nil {
			t.Fatal(c.args, err)
		}
		if got, ok := (*seen)[0].Body["includeSystemColumns"]; !ok || got != c.want {
			t.Fatalf("%v: includeSystemColumns %v (sent %v), want %v", c.args, got, ok, c.want)
		}
	}
}
