package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/build"
	"github.com/peakacom/peaka-cli/internal/output"
	"github.com/peakacom/peaka-cli/internal/sqltext"
	"github.com/peakacom/peaka-cli/internal/trino"
)

type sqlFlags struct {
	file         string
	maxRows      int
	catalog      string
	schema       string
	explain      bool
	stats        bool
	stopOnError  bool
	noProgress   bool
	session      []string
	queryTimeout time.Duration
	completeAt   bool // hidden: print the completions for the text instead of running it
}

func newSQLCmd() *cobra.Command {
	var f sqlFlags
	cmd := &cobra.Command{
		Use:   "sql [STATEMENT]",
		Short: "Run SQL against the project in context (Trino dialect)",
		Long: `Runs SQL on Peaka's SQL endpoint (dbc), the same path the JDBC, Python and Node drivers use.

  peaka sql "SELECT * FROM stripe.payment.charges LIMIT 10"
  peaka sql -f report.sql --format csv > out.csv
  cat q.sql | peaka sql --format jsonl | jq .amount
  peaka sql --catalog stripe --schema payment "SELECT count(*) FROM charges"
  peaka sql --explain "SELECT ..."

Several statements separated by ';' run in order. On a terminal the first 1000 rows are shown
(--max-rows 0 for all); in a pipe every row streams. Ctrl-C cancels the query on the server.
Without a statement on a terminal, the interactive session starts.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := statementText(args, f.file)
			if err != nil {
				return err
			}
			if text == "" {
				if rt.StdinTTY && rt.StdoutTTY {
					return runREPL(&f)
				}
				return api.Exit(api.ExitUsage, "no statement: pass it as an argument, -f FILE, or on stdin")
			}
			s, err := newSQLSession(&f)
			if err != nil {
				return err
			}
			if f.completeAt {
				return debugComplete(s, text)
			}
			return s.runScript(text)
		},
	}
	cmd.Flags().StringVarP(&f.file, "file", "f", "", "read the statement(s) from a file ('-' for stdin)")
	cmd.Flags().IntVar(&f.maxRows, "max-rows", -1, "stop after this many rows (default: 1000 on a terminal, unlimited in a pipe; 0 = unlimited)")
	cmd.Flags().StringVar(&f.catalog, "catalog", "", "default catalog for unqualified names")
	cmd.Flags().StringVar(&f.schema, "schema", "", "default schema for unqualified names")
	cmd.Flags().BoolVar(&f.explain, "explain", false, "show the plan instead of running (EXPLAIN)")
	cmd.Flags().BoolVar(&f.stats, "stats", false, "print query statistics to stderr")
	cmd.Flags().BoolVar(&f.stopOnError, "stop-on-error", true, "stop a multi-statement script at the first failure")
	cmd.Flags().BoolVar(&f.noProgress, "no-progress", false, "no progress line on stderr")
	cmd.Flags().StringArrayVar(&f.session, "session", nil, "session property NAME=VALUE (repeatable)")
	cmd.Flags().DurationVar(&f.queryTimeout, "query-timeout", 0, "cancel a query running longer than this (0 = no limit)")
	cmd.Flags().BoolVar(&f.completeAt, "complete", false, "print the completions offered at the end of the text (development aid)")
	_ = cmd.Flags().MarkHidden("complete")
	cmd.AddCommand(newSQLTranspileCmd())
	return cmd
}

func statementText(args []string, file string) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	if file == "-" || (file == "" && !rt.StdinTTY) {
		b, err := io.ReadAll(rt.In)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", api.Exit(api.ExitUsage, "-f: %v", err)
		}
		return string(b), nil
	}
	return "", nil
}

// sqlSession holds the Trino client with its session state for a run or a REPL.
type sqlSession struct {
	f         *sqlFlags
	client    *trino.Client
	project   string
	stderrTTY bool

	out       io.Writer // where results go (stdout, a REPL buffer, or \o file)
	format    string    // REPL override of the output format ("" = the setting)
	noSummary bool      // REPL \timing off
}

func newSQLSession(f *sqlFlags) (*sqlSession, error) {
	project, err := rt.sqlProjectID()
	if err != nil {
		return nil, err
	}
	tok, acct, err := rt.token()
	if err != nil {
		return nil, err
	}
	user := "peaka-cli"
	if acct != nil {
		user = acct.Name
	}
	c := &trino.Client{
		Endpoint: rt.Settings.DBC,
		HTTP:     &http.Client{Timeout: gf.timeout},
		User:     user,
		Source:   "peaka-cli/" + build.Version,
		Extra:    map[string]string{"peakaKey": tok, "peakaProject": project},
		Catalog:  f.catalog,
		Schema:   f.schema,
	}
	for _, kv := range f.session {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, api.Exit(api.ExitUsage, "--session %q: expected NAME=VALUE", kv)
		}
		if c.Session == nil {
			c.Session = map[string]string{}
		}
		c.Session[k] = v
	}
	if gf.debug {
		c.Debug = rt.Err
	}
	return &sqlSession{f: f, client: c, project: project, stderrTTY: api.IsTerminal(os.Stderr), out: rt.Out}, nil
}

var projectIDRe = regexp.MustCompile(`^[A-Za-z0-9]{8}$`)

// sqlProjectID returns the project id for the SQL endpoint; a project given
// by name is resolved through the org in context.
func (r *Runtime) sqlProjectID() (string, error) {
	ref, err := r.needProject()
	if err != nil {
		return "", err
	}
	if projectIDRe.MatchString(ref) {
		return ref, nil
	}
	if r.Settings.Org == "" {
		return "", api.Exit(api.ExitUsage, "project %q is a name; set the organization (`peaka orgs use`) or use the project id", ref)
	}
	p, err := r.resolveProject(ref)
	if err != nil {
		return "", err
	}
	return str(p["id"]), nil
}

// outputFormat is the grid format for this run: EXPLAIN plans are plain text on a terminal.
func (s *sqlSession) outputFormat() string {
	f := rt.Settings.Format
	if s.format != "" {
		f = s.format
	}
	if s.f.explain && f == "table" {
		return "text"
	}
	return f
}

// runScript runs every statement of text in order.
func (s *sqlSession) runScript(text string) error {
	stmts := sqltext.Split(text)
	if len(stmts) == 0 {
		return api.Exit(api.ExitUsage, "no statement found")
	}
	var firstErr error
	for i, st := range stmts {
		if len(stmts) > 1 && rt.Settings.Format == "table" && !gf.quiet {
			rt.note("-- statement %d (line %d)", i+1, st.Line)
		}
		if err := s.run(rt.Ctx, st.Text); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if s.f.stopOnError {
				return err
			}
			reportError(err)
		}
	}
	return firstErr
}

// run executes one statement and prints its result.
func (s *sqlSession) run(ctx context.Context, statement string) error {
	if s.f.explain && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(statement)), "EXPLAIN") {
		statement = "EXPLAIN " + statement
	}
	if s.f.queryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.f.queryTimeout)
		defer cancel()
	}
	maxRows := s.f.maxRows
	if maxRows < 0 {
		if rt.StdoutTTY {
			maxRows = 1000
		} else {
			maxRows = 0
		}
	}

	q, err := s.client.Execute(ctx, statement)
	if err != nil {
		return s.wrap(err, statement)
	}
	defer q.Cancel()

	prog := newProgress(rt.Err, s.stderrTTY && !gf.quiet && !s.f.noProgress)
	var gw output.GridWriter
	rows := 0
	truncated := false
	for {
		batch, done, err := q.Next(ctx)
		if err != nil {
			prog.clear()
			if ctx.Err() != nil {
				_ = q.Cancel()
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return api.Exit(api.ExitFailed, "query cancelled after --query-timeout %s", s.f.queryTimeout)
				}
				return api.Exit(api.ExitInterrupted, "interrupted; query %s cancelled", q.ID)
			}
			return s.wrap(err, statement)
		}
		prog.update(q.Stats)
		if gw == nil && len(q.Columns) > 0 {
			prog.stop()
			cols := make([]string, len(q.Columns))
			types := make([]string, len(q.Columns))
			for i, c := range q.Columns {
				cols[i], types[i] = c.Name, c.Type
			}
			if gw, err = output.NewGridWriter(s.out, s.outputFormat(), rt.outputOptions()); err != nil {
				return err
			}
			if err := gw.Begin(cols, types); err != nil {
				return err
			}
		}
		for _, r := range batch {
			if maxRows > 0 && rows >= maxRows {
				truncated = true
				break
			}
			if err := gw.Row(r); err != nil {
				return err
			}
			rows++
		}
		if truncated {
			_ = q.Cancel()
			break
		}
		if done {
			break
		}
	}
	prog.clear()
	if gw != nil {
		if err := gw.End(); err != nil {
			return err
		}
	}
	s.summary(q, rows, truncated, maxRows)
	return nil
}

func (s *sqlSession) summary(q *trino.Query, rows int, truncated bool, maxRows int) {
	if gf.quiet || s.noSummary {
		return
	}
	st := q.Stats
	if s.f.stats {
		if rt.StdoutTTY {
			fmt.Fprintf(rt.Err, "query     %s\nstate     %s\nelapsed   %s (server %s, cpu %s, queued %s)\nrows      %s processed, %s returned\nbytes     %s processed, %s input\nsplits    %d/%d\nmemory    %s peak\n",
				q.ID, st.State, q.Elapsed().Round(time.Millisecond), time.Duration(st.ElapsedTimeMillis)*time.Millisecond,
				time.Duration(st.CPUTimeMillis)*time.Millisecond, time.Duration(st.QueuedTimeMillis)*time.Millisecond,
				humanCount(st.ProcessedRows), humanCount(int64(rows)), humanBytes(st.ProcessedBytes), humanBytes(st.PhysicalInputBytes),
				st.CompletedSplits, st.TotalSplits, humanBytes(st.PeakMemoryBytes))
		} else {
			b, _ := json.Marshal(map[string]any{"queryId": q.ID, "rowsReturned": rows, "clientElapsedMs": q.Elapsed().Milliseconds(), "stats": st})
			fmt.Fprintln(rt.Err, string(b))
		}
	}
	for _, w := range q.Warnings {
		fmt.Fprintf(rt.Err, "warning: %s\n", w.Message)
	}
	if !rt.StdoutTTY {
		return
	}
	if q.UpdateType != "" && len(q.Columns) == 0 {
		if q.UpdateCount != nil {
			fmt.Fprintf(rt.Err, "%s  (%d rows)  %s\n", q.UpdateType, *q.UpdateCount, q.Elapsed().Round(time.Millisecond))
		} else {
			fmt.Fprintf(rt.Err, "%s  %s\n", q.UpdateType, q.Elapsed().Round(time.Millisecond))
		}
		return
	}
	line := fmt.Sprintf("%s · %s", plural(rows, "row"), q.Elapsed().Round(10*time.Millisecond))
	if st.ProcessedRows > 0 {
		line += " · " + humanCount(st.ProcessedRows) + " rows scanned"
	}
	if st.ProcessedBytes > 0 {
		line += " · " + humanBytes(st.ProcessedBytes)
	}
	if truncated {
		line += fmt.Sprintf(" · first %d shown (--max-rows 0 for all)", maxRows)
	}
	fmt.Fprintln(rt.Err, line)
}

// wrap turns transport and protocol errors into the CLI's error types.
func (s *sqlSession) wrap(err error, statement string) error {
	var he *trino.HTTPError
	var qe *trino.QueryError
	var te *trino.TransportError
	switch {
	case errors.As(err, &qe):
		return &sqlError{q: qe, statement: statement}
	case errors.As(err, &he):
		p := api.ProblemFromBody(he.Status, http.Header{"Content-Type": {he.ContentType}}, he.Body)
		if _, ok := p.Params["line"]; ok {
			return &sqlError{p: p, statement: statement}
		}
		return p
	case errors.As(err, &te):
		return &api.Problem{Status: 0, Code: "UPSTREAM_UNAVAILABLE", Detail: te.Error(), Retryable: true}
	}
	return err
}

// sqlError is a failed Trino query, rendered like a problem.
type sqlError struct {
	q         *trino.QueryError // a Trino error, or
	p         *api.Problem      // the proxy's problem+json carrying params.line/column
	statement string
}

func (e *sqlError) Error() string {
	if e.p != nil {
		return e.p.Error()
	}
	return e.q.Error()
}

func (e *sqlError) exitCode() int {
	if e.p != nil {
		return e.p.ExitCode()
	}
	if e.q.Canceled() {
		return api.ExitInterrupted
	}
	return api.ExitFailed
}

// location returns the 1-based line and column of the error, if known.
func (e *sqlError) location() (line, col int, ok bool) {
	if e.p != nil {
		l, lok := e.p.Params["line"].(float64)
		c, _ := e.p.Params["column"].(float64)
		return int(l), int(c), lok
	}
	if e.q.ErrorLocation != nil {
		return e.q.ErrorLocation.LineNumber, e.q.ErrorLocation.ColumnNumber, true
	}
	return 0, 0, false
}

func (e *sqlError) problem() *api.Problem {
	if e.p != nil {
		return e.p
	}
	params := map[string]any{"errorName": e.q.ErrorName, "errorType": e.q.ErrorType, "errorCode": e.q.ErrorCode, "queryId": e.q.QueryID}
	if e.q.ErrorLocation != nil {
		params["line"], params["column"] = e.q.ErrorLocation.LineNumber, e.q.ErrorLocation.ColumnNumber
	}
	return &api.Problem{Type: "https://docs.peaka.com/errors/query-failed", Title: "Query failed", Status: 400, Code: "QUERY_FAILED", Detail: e.q.Message, Params: params}
}

func (e *sqlError) writeHuman(w io.Writer) {
	if e.p != nil {
		e.p.WriteHuman(w, gf.verbose || gf.debug)
	} else {
		fmt.Fprintf(w, "ERROR  QUERY_FAILED  (%s)\n  %s\n", e.q.ErrorName, e.q.Message)
	}
	if line, col, ok := e.location(); ok {
		lines := strings.Split(e.statement, "\n")
		if line >= 1 && line <= len(lines) {
			fmt.Fprintf(w, "  %4d │ %s\n       │ %s^\n", line, lines[line-1], strings.Repeat(" ", max(col-1, 0)))
		}
	}
	if e.q != nil && e.q.QueryID != "" {
		fmt.Fprintf(w, "  query  %s\n", e.q.QueryID)
	}
}

// --- progress line ---

type progress struct {
	w       io.Writer
	on      bool
	started time.Time
	last    time.Time
	shown   bool
}

func newProgress(w io.Writer, on bool) *progress {
	return &progress{w: w, on: on, started: time.Now()}
}

func (p *progress) update(st trino.Stats) {
	if !p.on || time.Since(p.started) < 300*time.Millisecond || time.Since(p.last) < 100*time.Millisecond {
		return
	}
	p.last = time.Now()
	p.shown = true
	line := fmt.Sprintf("%-8s", st.State)
	if st.ProgressPercentage > 0 {
		line += fmt.Sprintf(" %3.0f%%", st.ProgressPercentage)
	} else {
		line += "     "
	}
	line += fmt.Sprintf("  %s rows  %s  %s", humanCount(st.ProcessedRows), humanBytes(st.ProcessedBytes), time.Since(p.started).Round(100*time.Millisecond))
	if st.TotalSplits > 0 {
		line += fmt.Sprintf("  splits %d/%d", st.CompletedSplits, st.TotalSplits)
	}
	fmt.Fprintf(p.w, "\r\x1b[2K%s", line)
}

// stop clears the line and ends updates: once rows are flowing, stdout owns the screen.
func (p *progress) stop() {
	p.clear()
	p.on = false
}

func (p *progress) clear() {
	if p.shown {
		fmt.Fprint(p.w, "\r\x1b[2K")
		p.shown = false
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func humanCount(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f kB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
