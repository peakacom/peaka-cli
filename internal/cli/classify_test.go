package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
	"github.com/peakacom/peaka-cli/internal/systemone"
)

// classifyStubs is a project with one table, served by stubbed Peaka, Trino and System One servers.
type classifyStubs struct {
	peaka, trino, model *httptest.Server
	trinoCalls          atomic.Int32
	apiSamples          atomic.Int32
	modelCalls          atomic.Int32
	modelStatus         int
	sampleStatus        int
	mu                  sync.Mutex
	states              []map[string]any
	out                 *bytes.Buffer
}

func newClassifyStubs(t *testing.T) *classifyStubs {
	t.Helper()
	s := &classifyStubs{modelStatus: 200, sampleStatus: 200, out: &bytes.Buffer{}}
	s.peaka = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body string
		switch r.URL.EscapedPath() {
		case "/data/projects/abcdefgh/catalogs":
			body = `[{"id":"c1","name":"pg","displayName":"pg"}]`
		case "/data/projects/abcdefgh/catalogs/c1/schemas":
			body = `[{"catalogId":"c1","catalogName":"pg","schemaName":"public"}]`
		case "/data/projects/abcdefgh/catalogs/c1/schemas/public/tables":
			body = `[{"catalogName":"pg","schemaName":"public","tableName":"patients"}]`
		case "/data/projects/abcdefgh/catalogs/c1/schemas/public/tables/patients/columns":
			body = `[{"name":"id","dataType":"integer"},{"name":"email","dataType":"varchar"},{"name":"dob","dataType":"date","desc":"birth date"},{"name":"active","dataType":"boolean"}]`
		case "/data/projects/abcdefgh/catalogs/c1/schemas/public/tables/patients/statistics":
			body = `{"rowCount":100,"columnStatistics":[{"columnName":"dob","distinctFraction":0.9,"nullsFraction":0.01}]}`
		case "/data/projects/abcdefgh/queries/execute":
			s.apiSamples.Add(1)
			var req struct {
				Columns []string         `json:"columns"`
				From    []map[string]any `json:"from"`
				Limit   int              `json:"limit"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if r.URL.Query().Get("format") != "COMPACT" || len(req.Columns) != 4 || req.Limit == 0 || req.From[0]["tableName"] != "patients" {
				t.Errorf("unexpected execute request %s %+v", r.URL, req)
			}
			if s.sampleStatus != 200 {
				w.WriteHeader(s.sampleStatus)
				body = `{"status":500,"code":"QUERY_FAILED","detail":"boom"}`
				break
			}
			var rows []string
			for i := 0; i < 30; i++ {
				rows = append(rows, fmt.Sprintf(`[%d,"user%d@example.com","19%02d-01-01",true]`, i, i, 50+i))
			}
			body = fmt.Sprintf(`{"columns":[{"columnName":"id"},{"columnName":"email"},{"columnName":"dob"},{"columnName":"active"}],"data":[%s]}`, strings.Join(rows, ","))
		default:
			w.WriteHeader(404)
			body = `{"status":404,"code":"NOT_FOUND","detail":"` + r.URL.Path + `"}`
		}
		_, _ = w.Write([]byte(body))
	}))
	s.trino = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/statement":
			s.trinoCalls.Add(1)
			stmt, _ := io.ReadAll(r.Body)
			if !strings.HasPrefix(string(stmt), `SELECT "id", "email", "dob", "active" FROM "pg"."public"."patients" LIMIT `) {
				t.Errorf("unexpected statement %q", stmt)
			}
			if s.sampleStatus != 200 {
				w.WriteHeader(s.sampleStatus)
				_, _ = w.Write([]byte(`{"detail":"boom"}`))
				return
			}
			fmt.Fprintf(w, `{"id":"q1","nextUri":"%s/v1/statement/q1/1","stats":{"state":"QUEUED"}}`, s.trino.URL)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/statement/q1/1":
			var rows []string
			for i := 0; i < 30; i++ {
				rows = append(rows, fmt.Sprintf(`[%d,"user%d@example.com","19%02d-01-01",true]`, i, i, 50+i))
			}
			fmt.Fprintf(w, `{"id":"q1","columns":[{"name":"id","type":"integer"},{"name":"email","type":"varchar"},{"name":"dob","type":"date"},{"name":"active","type":"boolean"}],"data":[%s],"stats":{"state":"FINISHED"}}`, strings.Join(rows, ","))
		default:
			w.WriteHeader(404)
		}
	}))
	s.model = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.modelCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer jev-key" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"detail":"bad key"}`))
			return
		}
		if s.modelStatus != 200 {
			w.WriteHeader(s.modelStatus)
			_, _ = w.Write([]byte(`{"detail":"no"}`))
			return
		}
		var req struct {
			State     map[string]any            `json:"state"`
			Questions map[string]map[string]any `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		s.mu.Lock()
		s.states = append(s.states, req.State)
		s.mu.Unlock()
		choice, conf, personal, health := "none", 0.9, 0.1, 0.05
		if req.State["column"] == "dob" {
			choice, conf, personal, health = "date_of_birth", 0.95, 0.9, 0.85
		}
		fmt.Fprintf(w, `{"model":"jev-1.14.0","answers":{"label":{"type":"choice","choice":"%s","confidence":%v,"probabilities":{"%s":%v}},"personal":{"type":"noul","noul":%v},"health":{"type":"noul","noul":%v}},"usage":{"input_tokens":500,"output_tokens":0}}`,
			choice, conf, choice, conf, personal, health)
	}))
	t.Cleanup(func() { s.peaka.Close(); s.trino.Close(); s.model.Close() })

	previous, flags := rt, gf
	t.Cleanup(func() { rt, gf = previous, flags })
	gf = globalFlags{timeout: 5 * time.Second}
	t.Setenv("PEAKA_API_KEY", "peaka-key")
	t.Setenv("PEAKA_CLASSIFIER_API_KEY", "jev-key")
	t.Setenv("PEAKA_CREDENTIALS_STORE", "file")
	t.Setenv("PEAKA_CONFIG_DIR", t.TempDir())
	rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Project: "abcdefgh", Format: "json", DBC: s.trino.URL, ClassifierURL: s.model.URL, ClassifierModel: "jev-latest"},
		Out: s.out, Err: io.Discard, client: api.New(s.peaka.URL, "peaka-key", time.Second)}
	return s
}

func (s *classifyStubs) run(args ...string) error {
	s.out.Reset()
	cmd := newClassifyCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs(args)
	cmd.SetOut(s.out)
	cmd.SetErr(io.Discard)
	return cmd.Execute()
}

func (s *classifyStubs) report(t *testing.T) map[string]any {
	t.Helper()
	var rep map[string]any
	if err := json.Unmarshal(s.out.Bytes(), &rep); err != nil {
		t.Fatalf("%v: %s", err, s.out.String())
	}
	return rep
}

func columnsOf(rep map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, c := range rep["columns"].([]any) {
		m := c.(map[string]any)
		out[m["column"].(string)] = m
	}
	return out
}

func codeOf(err error) int {
	var p *api.Problem
	if errors.As(err, &p) {
		return p.ExitCode()
	}
	return exitCode(err)
}

func TestClassifyShapeRunMixesRulesAndModel(t *testing.T) {
	s := newClassifyStubs(t)
	if err := s.run("pg.public"); err != nil {
		t.Fatal(err, s.out.String())
	}
	rep := s.report(t)
	cols := columnsOf(rep)
	if s.apiSamples.Load() != 1 || s.trinoCalls.Load() != 0 || s.modelCalls.Load() != 2 {
		t.Fatalf("api %d trino %d model %d", s.apiSamples.Load(), s.trinoCalls.Load(), s.modelCalls.Load())
	}
	if c := cols["email"]; c["method"] != "rule" || c["label"] != "email" || c["status"] != "accepted" {
		t.Errorf("email: %v", c)
	}
	if c := cols["dob"]; c["method"] != "model" || c["label"] != "date_of_birth" || c["phi"] != true || c["group"] != "pii" {
		t.Errorf("dob: %v", c)
	}
	if c := cols["active"]; c["method"] != "shortcut" {
		t.Errorf("active: %v", c)
	}
	if c := cols["id"]; c["method"] != "model" || c["label"] != "none" {
		t.Errorf("id: %v", c)
	}
	st := s.states[0]
	if st["table"] != "patients" || len(st["sibling_columns"].([]any)) != 4 || st["stats"] == nil || st["samples"] != nil {
		t.Errorf("state: %v", st)
	}
	summary := rep["summary"].(map[string]any)
	if summary["requests"] != 2.0 || summary["usage"].(map[string]any)["input_tokens"] != 1000.0 || rep["model"].(map[string]any)["returned"] != "jev-1.14.0" {
		t.Errorf("summary: %v %v", summary, rep["model"])
	}
	if rep["template"].(map[string]any)["source"] != "builtin" || rep["evidence_level"] != "shape" {
		t.Errorf("header: %v", rep)
	}

	// the same through the Trino endpoint
	s.modelCalls.Store(0)
	if err := s.run("pg.public", "--sample-via", "sql"); err != nil {
		t.Fatal(err, s.out.String())
	}
	if s.trinoCalls.Load() != 1 || s.apiSamples.Load() != 1 || s.modelCalls.Load() != 2 || columnsOf(s.report(t))["email"]["method"] != "rule" {
		t.Fatalf("sql sampling: trino %d api %d model %d", s.trinoCalls.Load(), s.apiSamples.Load(), s.modelCalls.Load())
	}
	if err := s.run("pg.public", "--sample-via", "nope"); codeOf(err) != api.ExitUsage {
		t.Fatalf("bad --sample-via accepted: %v", err)
	}
}

func TestClassifyNamesEvidenceNeverQueries(t *testing.T) {
	s := newClassifyStubs(t)
	rt.Settings.DBC = "http://127.0.0.1:1"
	if err := s.run("pg.public.patients", "--evidence", "names"); err != nil {
		t.Fatal(err)
	}
	if s.trinoCalls.Load() != 0 || s.modelCalls.Load() != 3 {
		t.Fatalf("trino %d model %d", s.trinoCalls.Load(), s.modelCalls.Load())
	}
	if st := s.states[0]; st["stats"] != nil {
		t.Errorf("stats sent under names evidence: %v", st)
	}
}

func TestClassifySamplesNeedConfirmation(t *testing.T) {
	s := newClassifyStubs(t)
	gf.noInput = true
	err := s.run("pg.public", "--evidence", "samples")
	if codeOf(err) != api.ExitUsage || s.modelCalls.Load() != 0 {
		t.Fatalf("err %v model %d", err, s.modelCalls.Load())
	}
	gf.yes = true
	if err := s.run("pg.public", "--evidence", "samples", "--include-samples"); err != nil {
		t.Fatal(err)
	}
	st := s.states[0]
	if st["samples"] == nil {
		t.Fatalf("no samples sent: %v", st)
	}
	if c := columnsOf(s.report(t))["dob"]; c["evidence"].(map[string]any)["samples"] == nil {
		t.Errorf("samples not kept: %v", c)
	}
}

func TestClassifyDryRunSendsNothing(t *testing.T) {
	s := newClassifyStubs(t)
	gf.dryRun = true
	t.Setenv("PEAKA_CLASSIFIER_API_KEY", "")
	if err := s.run("pg.public"); err != nil {
		t.Fatal(err)
	}
	if s.modelCalls.Load() != 0 || !strings.Contains(s.out.String(), `"dryRun":true`) || strings.Count(s.out.String(), "\n") != 2 {
		t.Fatalf("model %d out %s", s.modelCalls.Load(), s.out.String())
	}
}

func TestClassifyFailOnAndOut(t *testing.T) {
	s := newClassifyStubs(t)
	out := filepath.Join(t.TempDir(), "r.json")
	err := s.run("pg.public", "--fail-on", "phi", "--out", out)
	if codeOf(err) != api.ExitFailed {
		t.Fatalf("exit: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil || !bytes.Contains(b, []byte(`"date_of_birth"`)) {
		t.Fatalf("report not written: %v", err)
	}
	if err := s.run("pg.public", "--fail-on", "nope"); codeOf(err) != api.ExitUsage {
		t.Fatalf("unknown --fail-on accepted: %v", err)
	}
}

func TestClassifyModelAuthAndMissingKey(t *testing.T) {
	s := newClassifyStubs(t)
	t.Setenv("PEAKA_CLASSIFIER_API_KEY", "wrong")
	if err := s.run("pg.public"); codeOf(err) != api.ExitAuth {
		t.Fatalf("401 not mapped: %v", err)
	}
	// no key: refused for TypeSafe's endpoint, allowed for any other (a local server)
	calls := s.modelCalls.Load()
	t.Setenv("PEAKA_CLASSIFIER_API_KEY", "")
	rt.Settings.ClassifierURL = systemone.DefaultEndpoint
	if err := s.run("pg.public"); codeOf(err) != api.ExitAuth || s.modelCalls.Load() != calls {
		t.Fatalf("missing key: %v", err)
	}
	rt.Settings.ClassifierURL = s.model.URL
	if err := s.run("pg.public"); s.modelCalls.Load() == calls {
		t.Fatalf("local server not asked without a key: %v", err)
	}
}

func TestClassifyGoesOnWhenSamplingFails(t *testing.T) {
	s := newClassifyStubs(t)
	s.sampleStatus = 500
	err := s.run("pg.public")
	if codeOf(err) != api.ExitFailed {
		t.Fatalf("exit: %v", err)
	}
	rep := s.report(t)
	errs := rep["errors"].([]any)
	if len(errs) != 1 || errs[0].(map[string]any)["stage"] != "sample" {
		t.Fatalf("errors: %v", errs)
	}
	if cols := columnsOf(rep); cols["email"]["method"] != "model" {
		t.Errorf("email classified without values: %v", cols["email"])
	}
}

func TestClassifyTemplateCommand(t *testing.T) {
	s := newClassifyStubs(t)
	if err := s.run("template"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.out.String(), "name: default\n") || !strings.Contains(s.out.String(), "date_of_birth:") {
		t.Fatalf("template: %s", s.out.String())
	}
}
