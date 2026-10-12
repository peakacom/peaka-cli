package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/reeflective/readline/inputrc"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reeflective/readline"
	"golang.org/x/term"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
	"github.com/peakacom/peaka-cli/internal/sqltext"
)

// repl is the interactive SQL session.
type repl struct {
	s           *sqlSession
	rl          *readline.Shell
	md          *metadata
	projectName string
	outFile     *os.File // \o
	pager       bool
	expanded    bool
}

type metaCommand struct {
	help string
	run  func(r *repl, args []string) error
}

var metaCommands map[string]metaCommand

// keymapMenuSelect is readline's local keymap while the completion menu is open.
const keymapMenuSelect = "menu-select"

func init() {
	metaCommands = map[string]metaCommand{
		`\q`:        {"quit", func(r *repl, _ []string) error { return io.EOF }},
		`\?`:        {"this help", (*repl).help},
		`\h`:        {"this help", (*repl).help},
		`\dc`:       {"list catalogs", (*repl).listCatalogs},
		`\dn`:       {"list schemas: \\dn [catalog]", (*repl).listSchemas},
		`\dt`:       {"list tables: \\dt [catalog.schema]", (*repl).listTables},
		`\d`:        {"describe a table: \\d catalog.schema.table", (*repl).describe},
		`\use`:      {"default catalog and schema: \\use catalog[.schema]", (*repl).use},
		`\x`:        {"toggle expanded (vertical) output", (*repl).toggleExpanded},
		`\format`:   {"output format: \\format table|vertical|json|jsonl|csv|tsv|yaml|md", (*repl).setFormat},
		`\timing`:   {"toggle the summary line (rows, time)", (*repl).toggleTiming},
		`\pager`:    {"toggle paging of long results", (*repl).togglePager},
		`\max-rows`: {"rows to show per statement: \\max-rows N (0 = all)", (*repl).setMaxRows},
		`\o`:        {"send results to a file: \\o [file]; \\o alone returns to the screen", (*repl).setOutput},
		`\i`:        {"run a script: \\i file.sql", (*repl).include},
		`\e`:        {"edit the last statement in $EDITOR and run it", (*repl).edit},
		`\refresh`:  {"reload the catalog metadata used for completion", (*repl).refresh},
		`\project`:  {"show or change the project: \\project [id|name]", (*repl).project},
		`\stats`:    {"toggle query statistics after each statement", (*repl).toggleStats},
		`\explain`:  {"show the plan of a statement: \\explain SELECT ...", (*repl).explain},
	}
}

func runREPL(f *sqlFlags) error {
	// Ctrl-C is handled per statement here; the root signal context would die at the first one.
	rt.Ctx = context.Background()
	s, err := newSQLSession(f)
	if err != nil {
		return err
	}
	r := &repl{s: s, md: newMetadata(rt, s.project), pager: true}
	r.projectName = s.project
	if rt.Settings.Org != "" {
		if p, err := rt.resolveProject(s.project); err == nil {
			r.projectName = str(p["name"])
		}
	}
	r.md.Prefetch()

	rl := readline.NewShell()
	r.rl = rl
	r.md.onChange = rl.RefreshCompletions
	rl.Prompt.Primary(r.prompt)
	rl.Prompt.Secondary(func() string { return "\x1b[2m  … \x1b[0m" })
	rl.AcceptMultiline = func(line []rune) bool {
		t := strings.TrimSpace(string(line))
		return t == "" || strings.HasPrefix(t, `\`) || sqltext.Complete(t)
	}
	rl.SyntaxHighlighter = func(line []rune) string { return sqltext.Highlight(string(line)) }
	rl.Completer = r.complete
	if hist := r.historyFile(); hist != "" {
		rl.History.AddFromFile("peaka", hist)
	}
	_ = rl.Config.Set("history-autosuggest", true)
	_ = rl.Config.Set("menu-complete-display-prefix", true)
	// Enter in the completion menu takes the candidate and nothing more; the
	// line's own Enter (run, or continue on the next line) is the next one.
	if rl.Config.Binds[keymapMenuSelect] == nil {
		rl.Config.Binds[keymapMenuSelect] = map[string]inputrc.Bind{}
	}
	rl.Config.Binds[keymapMenuSelect][inputrc.Unescape(`\C-M`)] = inputrc.Bind{Action: "menu-complete-accept"}
	rl.Config.Binds[keymapMenuSelect][inputrc.Unescape(`\C-J`)] = inputrc.Bind{Action: "menu-complete-accept"}

	fmt.Fprintf(rt.Err, "peaka sql — %s on %s. Type \\? for help, \\q to quit.\n", r.projectName, rt.Settings.DBC)
	for {
		line, err := rl.Readline()
		switch {
		case errors.Is(err, readline.ErrInterrupt):
			continue
		case errors.Is(err, io.EOF):
			fmt.Fprintln(rt.Err)
			return nil
		case err != nil:
			return err
		}
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		if err := r.handle(text); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			reportError(err)
		}
	}
}

func (r *repl) prompt() string {
	p := "\x1b[1mpeaka\x1b[0m \x1b[36m" + r.projectName + "\x1b[0m"
	if c := r.s.client.Catalog; c != "" {
		p += " \x1b[33m" + c
		if s := r.s.client.Schema; s != "" {
			p += "." + s
		}
		p += "\x1b[0m"
	}
	return p + " › "
}

func (r *repl) historyFile() string {
	dir := filepath.Join(config.Dir(), "history")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return filepath.Join(dir, rt.Settings.APIHost()+"-"+r.s.project+".history")
}

// handle runs a meta command or the SQL text (one or more statements).
func (r *repl) handle(text string) error {
	if strings.HasPrefix(text, `\`) {
		args := strings.Fields(text)
		cmd, ok := metaCommands[args[0]]
		if !ok {
			return api.Exit(api.ExitUsage, "unknown command %s; \\? lists them", args[0])
		}
		return cmd.run(r, args[1:])
	}
	for _, st := range sqltext.Split(text) {
		if err := r.runStatement(st.Text); err != nil {
			return err
		}
	}
	return nil
}

// runStatement runs one statement with Ctrl-C bound to cancelling it, paging long output.
func (r *repl) runStatement(statement string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var buf bytes.Buffer
	if r.outFile != nil {
		r.s.out = r.outFile
	} else {
		r.s.out = &buf
	}
	err := r.s.run(ctx, statement)
	r.s.out = rt.Out
	if r.outFile == nil && buf.Len() > 0 {
		r.show(buf.Bytes())
	}
	if err != nil {
		var xe *api.ExitError
		if errors.As(err, &xe) && xe.Code == api.ExitInterrupted {
			fmt.Fprintln(rt.Err, "cancelled")
			return nil
		}
	}
	return err
}

// show writes a result to the screen, through the pager when it does not fit.
func (r *repl) show(b []byte) {
	lines := bytes.Count(b, []byte("\n"))
	_, height, err := term.GetSize(int(os.Stdout.Fd()))
	if r.pager && err == nil && lines > height-3 {
		pager := rt.Settings.Pager
		if pager == "" {
			pager = os.Getenv("PAGER")
		}
		if pager == "" {
			pager = "less -S -R -X -F"
		}
		cmd := exec.Command("sh", "-c", pager)
		cmd.Stdin = bytes.NewReader(b)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err == nil {
			return
		}
	}
	os.Stdout.Write(b)
}

// --- meta commands ---

func (r *repl) help(_ []string) error {
	names := make([]string, 0, len(metaCommands))
	for n := range metaCommands {
		names = append(names, n)
	}
	sort.Strings(names)
	w := 0
	for _, n := range names {
		w = max(w, len(n))
	}
	for _, n := range names {
		fmt.Fprintf(rt.Out, "  %-*s  %s\n", w, n, metaCommands[n].help)
	}
	fmt.Fprintln(rt.Out, "\nStatements end with ';'. Enter inside an unfinished statement adds a line. Tab completes tables, columns and keywords.")
	return nil
}

func (r *repl) listCatalogs(_ []string) error {
	return r.runStatement("SHOW CATALOGS")
}

func (r *repl) listSchemas(args []string) error {
	cat := r.s.client.Catalog
	if len(args) > 0 {
		cat = args[0]
	}
	if cat == "" {
		return api.Exit(api.ExitUsage, `\dn CATALOG (or \use CATALOG first)`)
	}
	return r.runStatement("SHOW SCHEMAS FROM " + quotePath(cat))
}

func (r *repl) listTables(args []string) error {
	path := ""
	if len(args) > 0 {
		path = args[0]
	} else if r.s.client.Catalog != "" && r.s.client.Schema != "" {
		path = r.s.client.Catalog + "." + r.s.client.Schema
	}
	if path == "" {
		return api.Exit(api.ExitUsage, `\dt CATALOG.SCHEMA (or \use CATALOG.SCHEMA first)`)
	}
	if !strings.Contains(path, ".") && r.s.client.Catalog != "" {
		path = r.s.client.Catalog + "." + path
	}
	return r.runStatement("SHOW TABLES FROM " + quotePath(path))
}

func (r *repl) describe(args []string) error {
	if len(args) == 0 {
		return api.Exit(api.ExitUsage, `\d [CATALOG.SCHEMA.]TABLE`)
	}
	return r.runStatement("DESCRIBE " + quotePath(args[0]))
}

func (r *repl) use(args []string) error {
	if len(args) == 0 {
		if r.s.client.Catalog == "" {
			fmt.Fprintln(rt.Err, "no default catalog")
		} else {
			fmt.Fprintf(rt.Err, "%s.%s\n", r.s.client.Catalog, r.s.client.Schema)
		}
		return nil
	}
	parts := strings.SplitN(args[0], ".", 2)
	if len(parts) == 1 {
		// USE needs a schema in Trino; set the catalog only on the client side.
		r.s.client.Catalog, r.s.client.Schema = parts[0], ""
		return nil
	}
	return r.runStatement("USE " + quotePath(args[0]))
}

func (r *repl) toggleExpanded(_ []string) error {
	r.expanded = !r.expanded
	if r.expanded {
		r.s.format = "vertical"
	} else {
		r.s.format = ""
	}
	fmt.Fprintf(rt.Err, "expanded display is %s\n", onOff(r.expanded))
	return nil
}

func (r *repl) setFormat(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(rt.Err, r.s.outputFormat())
		return nil
	}
	if !config.ValidFormat(args[0]) {
		return api.Exit(api.ExitUsage, "format %q: one of %s", args[0], strings.Join(config.Formats, ", "))
	}
	r.s.format = args[0]
	r.expanded = args[0] == "vertical"
	return nil
}

func (r *repl) toggleTiming(_ []string) error {
	r.s.noSummary = !r.s.noSummary
	fmt.Fprintf(rt.Err, "timing is %s\n", onOff(!r.s.noSummary))
	return nil
}

func (r *repl) togglePager(_ []string) error {
	r.pager = !r.pager
	fmt.Fprintf(rt.Err, "pager is %s\n", onOff(r.pager))
	return nil
}

func (r *repl) toggleStats(_ []string) error {
	r.s.f.stats = !r.s.f.stats
	fmt.Fprintf(rt.Err, "stats are %s\n", onOff(r.s.f.stats))
	return nil
}

func (r *repl) setMaxRows(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(rt.Err, r.s.f.maxRows)
		return nil
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 0 {
		return api.Exit(api.ExitUsage, `\max-rows N`)
	}
	r.s.f.maxRows = n
	return nil
}

func (r *repl) setOutput(args []string) error {
	if r.outFile != nil {
		r.outFile.Close()
		r.outFile = nil
	}
	if len(args) == 0 {
		fmt.Fprintln(rt.Err, "results go to the screen")
		return nil
	}
	f, err := os.OpenFile(args[0], os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return api.Exit(api.ExitUsage, "%v", err)
	}
	r.outFile = f
	fmt.Fprintf(rt.Err, "results go to %s\n", args[0])
	return nil
}

func (r *repl) include(args []string) error {
	if len(args) == 0 {
		return api.Exit(api.ExitUsage, `\i FILE`)
	}
	b, err := os.ReadFile(args[0])
	if err != nil {
		return api.Exit(api.ExitUsage, "%v", err)
	}
	return r.handle(string(b))
}

func (r *repl) edit(_ []string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	f, err := os.CreateTemp("", "peaka-*.sql")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	f.WriteString(r.rl.History.GetLast())
	f.Close()
	cmd := exec.Command("sh", "-c", editor+" "+f.Name())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return api.Exit(api.ExitUsage, "editor: %v", err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		return err
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return nil
	}
	fmt.Fprintln(rt.Err, sqltext.Highlight(text))
	return r.handle(text)
}

func (r *repl) refresh(_ []string) error {
	r.md.Reset()
	time.Sleep(300 * time.Millisecond)
	fmt.Fprintln(rt.Err, "metadata reloading in the background; "+r.md.Summary())
	return nil
}

func (r *repl) project(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(rt.Err, "%s (%s)\n", r.projectName, r.s.project)
		return nil
	}
	rt.Settings.Project = args[0]
	id, err := rt.sqlProjectID()
	if err != nil {
		return err
	}
	r.s.project = id
	r.s.client.Extra["peakaProject"] = id
	r.s.client.Catalog, r.s.client.Schema = "", ""
	r.projectName = id
	if rt.Settings.Org != "" {
		if p, err := rt.resolveProject(id); err == nil {
			r.projectName = str(p["name"])
		}
	}
	r.md = newMetadata(rt, id)
	r.md.onChange = r.rl.RefreshCompletions
	r.md.Prefetch()
	return nil
}

func (r *repl) explain(args []string) error {
	if len(args) == 0 {
		return api.Exit(api.ExitUsage, `\explain STATEMENT`)
	}
	r.s.f.explain = true
	defer func() { r.s.f.explain = false }()
	return r.runStatement(strings.Join(args, " "))
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// quotePath quotes the segments of a dotted path that need it.
func quotePath(path string) string {
	parts := strings.Split(path, ".")
	for i, p := range parts {
		if strings.HasPrefix(p, `"`) {
			continue
		}
		if sqltext.IsKeyword(p) || strings.ContainsAny(p, "- ") {
			parts[i] = `"` + p + `"`
		}
	}
	return strings.Join(parts, ".")
}
