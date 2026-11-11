package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/classify"
	"github.com/peakacom/peaka-cli/internal/config"
	"github.com/peakacom/peaka-cli/internal/systemone"
)

type classifyFlags struct {
	evidence, template, endpoint, model, out, failOn, sampleVia string
	samples, concurrency                                        int
	minConfidence                                               float64
	noStats, noProgress, includeSamples                         bool
}

func newClassifyCmd() *cobra.Command {
	f := &classifyFlags{}
	cmd := &cobra.Command{
		Use:               "classify TARGET",
		ValidArgsFunction: argCompleter(completeDotted(3)),
		Short:             "Find the PII and PHI columns of a catalog, schema or table with rules and a System One model (Jev)",
		Long: `Every column is classified by the template's rules first (checksums, patterns, column names) and by the
model for what the rules leave open. The model is TypeSafe's Jev, or any server with the same System One API
(classifier-url, classifier-model, classifier-key in the configuration).

What leaves the machine is --evidence: names (column names, types and descriptions only), shape (also the
layout and statistics of sampled values, never a value) or samples (also up to 10 values per column; asks
for confirmation). One query per table reads --samples rows for shape and samples, through the REST API
(--sample-via api, the default) or the Trino endpoint (--sample-via sql).

  peaka classify hubspot.crm --out report.json
  peaka classify postgres.public.patients --evidence samples --yes
  peaka classify postgres --evidence names --fail-on phi,review     # CI: exit 8 on a PHI or uncertain column
  peaka classify postgres.public --dry-run                          # the requests that would be sent, and an estimate

The report lists a label, group, sensitivity, PHI flag, confidence and method (rule, shortcut or model) per
column; uncertain columns are "review". Exit codes: 8 when --fail-on matches or a table could not be read;
3 when the model refuses the key; 9 when the model stayed unavailable; 130 when interrupted.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runClassify(f, args[0]) },
	}
	fl := cmd.Flags()
	fl.StringVar(&f.evidence, "evidence", string(classify.EvidenceShape), "what the model sees: names, shape or samples")
	_ = cmd.RegisterFlagCompletionFunc("evidence", staticCompleter("names", "shape", "samples"))
	fl.IntVar(&f.samples, "samples", 100, "rows read per table for shape and samples evidence")
	fl.StringVar(&f.sampleVia, "sample-via", "api", "how rows are read: api (the REST query execution, any identity) or sql (the Trino endpoint, API keys)")
	_ = cmd.RegisterFlagCompletionFunc("sample-via", staticCompleter("api", "sql"))
	fl.StringVar(&f.template, "template", "", "template file (default: classifier-template, else the built-in)")
	fl.StringVar(&f.endpoint, "endpoint", "", "System One endpoint (default: classifier-url)")
	fl.StringVar(&f.model, "model", "", "model name (default: classifier-model)")
	fl.StringVar(&f.out, "out", "", "write the full JSON report to this file")
	fl.IntVar(&f.concurrency, "concurrency", 8, "model requests in flight")
	fl.Float64Var(&f.minConfidence, "min-confidence", 0, "model confidence that accepts a label (default: the template's model_accept)")
	fl.StringVar(&f.failOn, "fail-on", "", "exit 8 when a column matches: label ids, group ids, phi, review, error or any (comma-separated)")
	fl.BoolVar(&f.noStats, "no-stats", false, "do not read table statistics")
	fl.BoolVar(&f.noProgress, "no-progress", false, "no progress line on stderr")
	fl.BoolVar(&f.includeSamples, "include-samples", false, "keep the sample values that were sent in the report (samples evidence)")
	cmd.AddCommand(newClassifyTemplateCmd(f))
	return cmd
}

func newClassifyTemplateCmd(f *classifyFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Print the effective classification template, to copy and customise",
		Long: `  peaka classify template > my-template.yaml
  peaka config set classifier-template my-template.yaml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, _, err := loadTemplate(f.template)
			if err != nil {
				return err
			}
			if gf.format == "json" { // YAML is the file people edit; a pipe does not change that
				return rt.print(t)
			}
			b, err := t.YAML()
			if err != nil {
				return err
			}
			_, err = rt.Out.Write(b)
			return err
		},
	}
	cmd.Flags().StringVar(&f.template, "template", "", "template file (default: classifier-template, else the built-in)")
	return cmd
}

// loadTemplate applies flag > configuration > built-in and says which.
func loadTemplate(flag string) (*classify.Template, string, error) {
	path, source := flag, "flag"
	if path == "" && rt.Settings.ClassifierTemplate != "" {
		path, source = rt.Settings.ClassifierTemplate, "config"
	}
	if path == "" {
		t, err := classify.Default()
		return t, "builtin", err
	}
	t, err := classify.Load(path)
	if err != nil {
		return nil, "", api.Exit(api.ExitInvalid, "%v", err)
	}
	return t, source, nil
}

func runClassify(f *classifyFlags, target string) error {
	started := time.Now()
	ev, ok := classify.ParseEvidence(f.evidence)
	if !ok {
		return usage("--evidence %q: names, shape or samples", f.evidence)
	}
	if f.samples <= 0 {
		return usage("--samples must be positive")
	}
	if f.minConfidence < 0 || f.minConfidence > 1 {
		return usage("--min-confidence must be between 0 and 1")
	}
	if f.sampleVia != "api" && f.sampleVia != "sql" {
		return usage("--sample-via %q: api or sql", f.sampleVia)
	}
	var failOn []string
	if f.failOn != "" {
		failOn = strings.Split(f.failOn, ",")
	}
	tpl, source, err := loadTemplate(f.template)
	if err != nil {
		return err
	}
	for _, c := range failOn {
		c = strings.TrimSpace(strings.ToLower(c))
		switch c {
		case "", "phi", "review", "error", "any":
			continue
		}
		if _, isGroup := tpl.Groups[c]; !isGroup && tpl.Label(c) == nil {
			return usage("--fail-on %q: not a label or group of template %s", c, tpl.Name)
		}
	}
	project, err := rt.needProject()
	if err != nil {
		return err
	}
	refs, err := rt.enumerateTarget(target)
	if err != nil {
		return err
	}

	endpoint := firstNonEmpty(f.endpoint, rt.Settings.ClassifierURL, systemone.DefaultEndpoint)
	model := firstNonEmpty(f.model, rt.Settings.ClassifierModel, systemone.DefaultModel)
	client := systemone.New(endpoint, "", model, gf.timeout)
	client.GzipAbove = -1 // a column's request is a few KB; compatible servers do not all inflate
	if !gf.dryRun {
		key, _, err := config.ClassifierKey(configurationName())
		if err != nil {
			return err
		}
		if key == "" && endpoint == systemone.DefaultEndpoint {
			return api.Exit(api.ExitAuth, "no classifier API key: set PEAKA_CLASSIFIER_API_KEY or `peaka config set classifier-key KEY`")
		}
		if key == "" {
			rt.note("no classifier API key; asking %s without one", client.Host()) // a local or self-hosted server
		}
		client.Token = key
	}
	if gf.debug {
		client.Debug = rt.Err
		client.Redact = redactSamples
	}
	if ev == classify.EvidenceSamples && !gf.dryRun {
		if err := rt.confirm(fmt.Sprintf("Send up to %d values per column of %s to %s?", classify.DefaultSamples, plural(len(refs), "table"), client.Host())); err != nil {
			return err
		}
	}

	var session *sqlSession
	if ev != classify.EvidenceNames && f.sampleVia == "sql" {
		if session, err = newSQLSession(&sqlFlags{noProgress: true}); err != nil {
			return err
		}
	}
	sample := func(ref tableRef, in *classify.TableInput) error {
		if session != nil {
			return sampleTable(rt.Ctx, session, ref, in, f.samples)
		}
		return rt.sampleTableAPI(ref, in, f.samples)
	}

	report := &classify.Report{Schema: classify.ReportSchema, GeneratedAt: started.UTC(), Project: project, Target: target,
		Evidence: ev, SamplesPerTable: f.samples, IncludeSamples: f.includeSamples, Thresholds: tpl.Settings}
	report.Template.Name, report.Template.Version, report.Template.Hash, report.Template.Source = tpl.Name, tpl.Version, tpl.Fingerprint(), source
	report.Model.Endpoint, report.Model.Requested = client.Host(), model
	if f.minConfidence > 0 {
		report.Thresholds.ModelAccept = f.minConfidence
	}

	prog := newClassifyProgress(rt.Err, api.IsTerminal(os.Stderr) && !gf.quiet && !f.noProgress, len(refs))
	var dryTokens, dryRequests int
	engine := &classify.Engine{Template: tpl, Classifier: client, Evidence: ev, MinConfidence: f.minConfidence,
		Concurrency: f.concurrency, IncludeSample: f.includeSamples}
	if gf.dryRun {
		enc := json.NewEncoder(rt.Out)
		engine.DryRun = func(req systemone.Request) {
			b, _ := json.Marshal(req)
			dryRequests++
			dryTokens += len(b) / 4
			prog.clear()
			_ = enc.Encode(map[string]any{"dryRun": true, "endpoint": endpoint, "model": req.Model, "state": req.State, "questions": req.Questions})
		}
	}

	var fatal error
	for i, ref := range refs {
		if rt.Ctx.Err() != nil {
			fatal = rt.Ctx.Err()
			break
		}
		prog.table(i, ref.String())
		in, statsErr, err := rt.fetchTableInput(ref, !f.noStats && ev != classify.EvidenceNames)
		if err != nil {
			if isFatal(err) {
				fatal = err
				break
			}
			report.Errors = append(report.Errors, classify.TableError{Catalog: ref.Catalog, Schema: ref.Schema, Table: ref.Table, Stage: "columns", Detail: err.Error()})
			continue
		}
		if statsErr != nil {
			report.Errors = append(report.Errors, *statsErr)
		}
		if ev != classify.EvidenceNames {
			if err := sample(ref, &in); err != nil {
				if rt.Ctx.Err() != nil || isFatal(err) {
					fatal = firstNonNilErr(rt.Ctx.Err(), err)
					break
				}
				report.Errors = append(report.Errors, classify.TableError{Catalog: ref.Catalog, Schema: ref.Schema, Table: ref.Table, Stage: "sample", Detail: err.Error()})
			}
		}
		engine.Progress = func(done int, _ systemone.Usage) {
			prog.column(done, len(in.Columns), report.Summary.Usage.InputTokens)
		}
		results, usage, err := engine.ClassifyTable(rt.Ctx, in)
		report.Columns = append(report.Columns, results...)
		report.Summary.Usage.InputTokens += usage.InputTokens
		report.Summary.Usage.OutputTokens += usage.OutputTokens
		if err != nil {
			fatal = err
			break
		}
	}
	prog.clear()
	report.Finish(len(refs), started)
	if gf.dryRun {
		report.Summary.Requests = dryRequests
		report.Summary.Usage.InputTokens = dryTokens
		report.Summary.EstimatedCostUSD = float64(dryTokens) / 1e6 * classify.InputTokenPriceUSD
		for _, e := range report.Errors {
			rt.note("%s.%s.%s: %s: %s", e.Catalog, e.Schema, e.Table, e.Stage, e.Detail)
		}
		rt.note("dry run: %s, %d columns, %d model requests, about %s input tokens, about $%.4f at TypeSafe's list price (nothing was sent)",
			plural(len(refs), "table"), len(report.Columns), dryRequests, humanCount(int64(dryTokens)), report.Summary.EstimatedCostUSD)
		if fatal != nil {
			return fatalError(fatal)
		}
		if n := unreadTables(report.Errors); n > 0 {
			return api.Exit(api.ExitFailed, "%s could not be fully read", plural(n, "table"))
		}
		return nil
	}

	if f.out != "" {
		b, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(f.out, append(b, '\n'), 0o600); err != nil {
			return fmt.Errorf("--out: %w", err)
		}
	}
	format := rt.Settings.Format
	if format == "table" || format == "md" || format == "csv" || format == "tsv" {
		if err := rt.printWith(report.Rows(), classify.RowFields...); err != nil {
			return err
		}
	} else if err := rt.print(report); err != nil {
		return err
	}
	s := report.Summary
	rt.note("%s, %d columns: %d accepted, %d review, %d failed; %d PHI; %d model requests, %s input tokens, %s",
		plural(s.Tables, "table"), s.Columns, s.Accepted, s.Review, s.Failed, s.PHIFlagged, s.Requests, humanCount(int64(s.Usage.InputTokens)), time.Since(started).Round(100*time.Millisecond))
	if f.out != "" {
		rt.note("report written to %s", f.out)
	}
	if fatal != nil {
		return fatalError(fatal)
	}
	if hits := report.Matches(failOn); len(hits) > 0 {
		return api.Exit(api.ExitFailed, "%d columns matched --fail-on %s", len(hits), f.failOn)
	}
	if n := unreadTables(report.Errors); n > 0 {
		return api.Exit(api.ExitFailed, "%s could not be fully read; see errors", plural(n, "table"))
	}
	return nil
}

// unreadTables counts the errors that cost evidence; missing statistics only cost a detail.
func unreadTables(errs []classify.TableError) int {
	n := 0
	for _, e := range errs {
		if e.Stage != "stats" {
			n++
		}
	}
	return n
}

func firstNonNilErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func configurationName() string {
	if rt.Config != nil && rt.Config.Name != "" {
		return rt.Config.Name
	}
	return config.DefaultName
}

// isFatal says whether an API error ends the run rather than one table: identity and context problems do.
func isFatal(err error) bool {
	var p *api.Problem
	if errors.As(err, &p) {
		return p.Status == 401 || p.Status == 403
	}
	var xe *api.ExitError
	return errors.As(err, &xe) || errors.Is(err, context.Canceled)
}

// fatalError maps what ended the run to the CLI's error contract.
func fatalError(err error) error {
	if err == nil {
		return nil
	}
	var se *systemone.Error
	if errors.As(err, &se) {
		code := "CLASSIFIER_FAILED"
		switch se.ExitCode() {
		case api.ExitAuth:
			code = "CLASSIFIER_AUTH"
		case api.ExitInvalid:
			code = "CLASSIFIER_INVALID"
		case api.ExitRetryable:
			code = "CLASSIFIER_UNAVAILABLE"
		}
		return &api.Problem{Status: se.Status, Code: code, Detail: se.Error(), Retryable: se.Retryable}
	}
	if errors.Is(err, context.Canceled) {
		return api.Exit(api.ExitInterrupted, "interrupted")
	}
	return err
}

var samplesRe = regexp.MustCompile(`"samples":\[[^\]]*\]`)

// redactSamples keeps sample values out of --debug output.
func redactSamples(body []byte) []byte {
	return samplesRe.ReplaceAllFunc(body, func(m []byte) []byte {
		n := bytes.Count(m, []byte(`","`)) + 1
		return []byte(fmt.Sprintf(`"samples":"<redacted n=%d>"`, n))
	})
}

// --- progress line ---

type classifyProgress struct {
	w      io.Writer
	on     bool
	shown  bool
	tables int
	index  int
	name   string
}

func newClassifyProgress(w io.Writer, on bool, tables int) *classifyProgress {
	return &classifyProgress{w: w, on: on, tables: tables}
}

func (p *classifyProgress) table(i int, name string) {
	p.index, p.name = i+1, name
	p.column(0, 0, 0)
}

func (p *classifyProgress) column(done, total, tokens int) {
	if !p.on {
		return
	}
	p.shown = true
	fmt.Fprintf(p.w, "\r\x1b[2Kclassifying %d/%d %s  columns %d/%d  %s tokens", p.index, p.tables, p.name, done, total, humanCount(int64(tokens)))
}

func (p *classifyProgress) clear() {
	if p.shown {
		fmt.Fprint(p.w, "\r\x1b[2K")
		p.shown = false
	}
}
