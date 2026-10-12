// Package cli is the cobra command tree. Every command receives a Runtime:
// resolved settings, the identity, output options, and whether a human is at
// the terminal.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
	"github.com/peakacom/peaka-cli/internal/output"
)

type globalFlags struct {
	configuration string
	environment   string
	apiURL        string
	dbcURL        string
	oauthURL      string
	account       string
	org           string
	project       string
	format        string
	fields        string
	jq            string
	quiet         bool
	noInput       bool
	yes           bool
	debug         bool
	verbose       bool
	noHeader      bool
	noTruncate    bool
	timeout       time.Duration
	dryRun        bool
	ifNotExists   bool
}

var gf globalFlags

// Runtime is what commands run against.
type Runtime struct {
	Ctx       context.Context
	Config    *config.Configuration
	Settings  *config.Settings
	Out       io.Writer
	Err       io.Writer
	In        io.Reader
	StdoutTTY bool
	StdinTTY  bool

	client    *api.Client
	catalogs  []record // catalogs of the project in context, once listed
	connTypes []record // /connections/config, once listed

	identity string // which credential was sent, for 401/403 messages: "PEAKA_API_KEY qlHy…" or "account owner (kLgg…)"
}

var rt *Runtime

func Execute() int {
	root := newRoot()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cmd, err := root.ExecuteContextC(ctx)
	code := api.ExitOK
	if err != nil {
		code = reportError(err)
	}
	if ctx.Err() == nil {
		notifyUpdate(topLevelName(cmd))
	}
	return code
}

// topLevelName is the first word after "peaka" of the command that ran.
func topLevelName(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	for cmd.HasParent() && cmd.Parent().HasParent() {
		cmd = cmd.Parent()
	}
	if !cmd.HasParent() {
		return ""
	}
	return cmd.Name()
}

func reportError(err error) int {
	stderr := os.Stderr
	var p *api.Problem
	var xe *api.ExitError
	var se *sqlError
	switch {
	case errors.As(err, &p):
		if rt != nil && !rt.StdoutTTY || (rt != nil && rt.Settings != nil && rt.Settings.Format == "json") {
			p.WriteJSON(stderr)
		} else {
			p.WriteHuman(stderr, gf.verbose || gf.debug)
			// a refused identity: say which one was sent, so a stale env var or the wrong account is obvious
			if (p.Status == 401 || p.Status == 403) && rt != nil && rt.identity != "" {
				fmt.Fprintf(stderr, "  key    %s\n", rt.identity)
				fmt.Fprintf(stderr, "  api    %s  (environment %s, from %s)\n", rt.Settings.API, rt.Settings.Environment, rt.Settings.Source["environment"])
				if p.Status == 401 && rt.Settings.Source["environment"] == "default" {
					fmt.Fprintf(stderr, "  hint   a key from another environment? `peaka config set environment test` (or eu), or --environment\n")
				}
			}
		}
		return p.ExitCode()
	case errors.As(err, &se):
		if rt != nil && !rt.StdoutTTY || (rt != nil && rt.Settings != nil && rt.Settings.Format == "json") {
			se.problem().WriteJSON(stderr)
		} else {
			se.writeHuman(stderr)
		}
		return se.exitCode()
	case errors.As(err, &xe):
		// the CLI's own refusals follow the same contract as the API's: problem+json when piped
		if rt != nil && !rt.StdoutTTY || (rt != nil && rt.Settings != nil && rt.Settings.Format == "json") {
			(&api.Problem{Code: codeForExit(xe.Code), Detail: xe.Err.Error(), Retryable: xe.Code == api.ExitRetryable}).WriteJSON(stderr)
		} else {
			fmt.Fprintf(stderr, "ERROR  %s\n", xe.Err)
		}
		return xe.Code
	case errors.Is(err, context.Canceled):
		return api.ExitInterrupted
	}
	// cobra's own errors (unknown flag, missing arg) are usage errors.
	fmt.Fprintf(stderr, "ERROR  %s\n", err)
	return api.ExitUsage
}

// codeForExit names the CLI's own refusals the way the API names its problems.
func codeForExit(code int) string {
	switch code {
	case api.ExitUsage:
		return "USAGE"
	case api.ExitAuth:
		return "AUTHENTICATION_REQUIRED"
	case api.ExitNotFound:
		return "NOT_FOUND"
	case api.ExitPermission:
		return "PERMISSION_REQUIRED"
	case api.ExitInvalid:
		return "VALIDATION_FAILED"
	case api.ExitConflict:
		return "CONFLICT"
	case api.ExitFailed:
		return "FAILED"
	case api.ExitRetryable:
		return "RETRYABLE"
	case api.ExitInterrupted:
		return "INTERRUPTED"
	}
	return "ERROR"
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "peaka",
		Short: "Peaka from the terminal: SQL, projects, connections, catalogs — for people and agents",
		Long: `peaka talks to Peaka's public API and SQL endpoint.

Grammar:  peaka <resource> <verb> [args] [flags]
Context:  peaka config set project <name>   (or --project, PEAKA_PROJECT)
Machine:  --format json, --no-input, PEAKA_API_KEY; errors are problem+json on stderr
Help:     peaka help --agent   (the short guide for AI agents)`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return initRuntime(cmd)
		},
	}
	pf := root.PersistentFlags()
	pf.StringVar(&gf.configuration, "configuration", "", "named configuration to use (PEAKA_CONFIGURATION)")
	pf.StringVar(&gf.environment, "environment", "", "preset for api and dbc: prod, eu, test (PEAKA_ENVIRONMENT)")
	pf.StringVar(&gf.apiURL, "api-url", "", "REST API base URL (PEAKA_API_URL)")
	pf.StringVar(&gf.dbcURL, "dbc-url", "", "SQL endpoint URL (PEAKA_DBC_URL)")
	pf.StringVar(&gf.oauthURL, "oauth-url", "", "OAuth authorization server URL for browser login (PEAKA_OAUTH_URL)")
	pf.StringVar(&gf.account, "account", "", "stored account to act as (PEAKA_ACCOUNT)")
	pf.StringVar(&gf.org, "org", "", "organization id or name (PEAKA_ORG)")
	pf.StringVarP(&gf.project, "project", "p", "", "project id or name (PEAKA_PROJECT)")
	pf.StringVarP(&gf.format, "format", "o", "", "output: table, json, jsonl, csv, tsv, yaml, md, vertical (default: table on a terminal, json otherwise)")
	pf.StringVar(&gf.fields, "fields", "", "comma-separated fields to show, dotted paths allowed")
	pf.StringVar(&gf.jq, "jq", "", "jq expression applied to the result")
	pf.BoolVarP(&gf.quiet, "quiet", "q", false, "only the result; no progress or hints on stderr")
	pf.BoolVar(&gf.noInput, "no-input", false, "never prompt; fail instead (automatic when stdin is not a terminal)")
	pf.BoolVarP(&gf.yes, "yes", "y", false, "answer yes to confirmations")
	pf.BoolVar(&gf.debug, "debug", false, "print HTTP requests and responses to stderr")
	pf.BoolVarP(&gf.verbose, "verbose", "v", false, "more detail in errors")
	pf.BoolVar(&gf.noHeader, "no-header", false, "omit the header row (table, csv, tsv)")
	pf.BoolVar(&gf.noTruncate, "no-truncate", false, "do not shorten wide cells in tables")
	pf.DurationVar(&gf.timeout, "timeout", 60*time.Second, "per-request timeout")
	pf.BoolVar(&gf.dryRun, "dry-run", false, "print the request a writing command would send (method, URL, body) instead of sending it; reads still run")
	pf.BoolVar(&gf.ifNotExists, "if-not-exists", false, "treat an already-exists conflict (409) from a create as success")

	root.AddCommand(
		newAuthCmd(), newConfigCmd(), newMeCmd(),
		newOrgsCmd(), newWorkspacesCmd(), newProjectsCmd(), newAPIKeysCmd(),
		newConnectionsCmd(), newCatalogsCmd(), newSchemasCmd(), newTablesCmd(), newCacheCmd(),
		newQueriesCmd(), newFoldersCmd(), newExportsCmd(), newSemanticCatalogsCmd(), newParametricTablesCmd(),
		newMetadataCmd(), newAICmd(), newDriversCmd(), newEmbedCmd(),
		newSQLCmd(), newAPICmd(), newMCPCmd(), newSkillCmd(), newVersionCmd(), newUpgradeCmd(),
	)
	root.SetHelpCommand(newHelpCmd(root))
	// Tab completion of the context flags: live names where they are names.
	_ = root.RegisterFlagCompletionFunc("project", flagCompleter(completeProjects))
	_ = root.RegisterFlagCompletionFunc("org", flagCompleter(completeOrgs))
	_ = root.RegisterFlagCompletionFunc("environment", staticCompleter(config.EnvironmentNames()...))
	_ = root.RegisterFlagCompletionFunc("format", staticCompleter("table", "json", "jsonl", "csv", "tsv", "yaml", "md", "vertical"))
	return root
}

func initRuntime(cmd *cobra.Command) error {
	name := gf.configuration
	if name == "" {
		name = config.ActiveName()
	}
	cfg, err := config.Load(name)
	if err != nil {
		return api.Exit(api.ExitUsage, "%v", err)
	}
	settings := config.Resolve(cfg, config.Overrides{
		Environment: gf.environment, API: gf.apiURL, DBC: gf.dbcURL, OAuth: gf.oauthURL,
		Account: gf.account, Org: gf.org, Project: gf.project, Format: gf.format,
	})
	rt = &Runtime{
		Ctx: cmd.Context(), Config: cfg, Settings: settings,
		Out: os.Stdout, Err: os.Stderr, In: os.Stdin,
		StdoutTTY: api.IsTerminal(os.Stdout), StdinTTY: api.IsTerminal(os.Stdin),
	}
	if settings.Format == "" {
		if rt.StdoutTTY {
			settings.Format = "table"
		} else {
			settings.Format = "json"
		}
		settings.Source["format"] = "default"
	} else if !config.ValidFormat(settings.Format) {
		return api.Exit(api.ExitUsage, "unknown format %q: one of %s", settings.Format, strings.Join(config.Formats, ", "))
	}
	if !rt.StdinTTY {
		gf.noInput = true
	}
	return nil
}

// outputOptions builds the printer options from the global flags.
func (r *Runtime) outputOptions() output.Options {
	o := output.Options{Format: r.Settings.Format, JQ: gf.jq, NoHeader: gf.noHeader, NoTruncate: gf.noTruncate}
	if r.StdoutTTY {
		if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
			o.Width = w
		}
	}
	if gf.fields != "" {
		for _, f := range strings.Split(gf.fields, ",") {
			if f = strings.TrimSpace(f); f != "" {
				o.Fields = append(o.Fields, f)
			}
		}
	}
	return o
}

// print renders a result on stdout.
func (r *Runtime) print(v any) error {
	return output.Print(r.Out, v, r.outputOptions())
}

// printWith renders with a different default field set for tables (ignored when --fields is given).
func (r *Runtime) printWith(v any, tableFields ...string) error {
	o := r.outputOptions()
	if len(o.Fields) == 0 && (o.Format == "table" || o.Format == "md" || o.Format == "csv" || o.Format == "tsv") && len(tableFields) > 0 {
		o.Fields = tableFields
	}
	return output.Print(r.Out, v, o)
}

// note writes a line for the person on stderr (silenced by --quiet).
func (r *Runtime) note(format string, args ...any) {
	if gf.quiet {
		return
	}
	fmt.Fprintf(r.Err, format+"\n", args...)
}

// confirm asks a yes/no question; --yes answers it, --no-input refuses it.
func (r *Runtime) confirm(question string) error {
	if gf.yes {
		return nil
	}
	if gf.noInput {
		return api.Exit(api.ExitUsage, "%s — pass --yes to confirm without a prompt", question)
	}
	fmt.Fprintf(r.Err, "%s [y/N] ", question)
	line, _ := bufio.NewReader(r.In).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return api.Exit(api.ExitUsage, "cancelled")
}

// token returns the credential to send: PEAKA_API_KEY wins, else the account's secret.
func (r *Runtime) token() (string, *config.Account, error) {
	if k := os.Getenv("PEAKA_API_KEY"); k != "" {
		r.identity = fmt.Sprintf("PEAKA_API_KEY %s", keyPrefix(k))
		return k, nil, nil
	}
	host := r.Settings.APIHost()
	name := r.Settings.Account
	if name == "" {
		accts, err := config.Accounts(host)
		if err != nil {
			return "", nil, err
		}
		switch len(accts) {
		case 0:
			return "", nil, api.Exit(api.ExitAuth, "no identity for %s: run `peaka auth login`, or set PEAKA_API_KEY", host)
		case 1:
			name = accts[0].Name
		default:
			return "", nil, api.Exit(api.ExitAuth, "%d accounts stored for %s: choose one with `peaka config set account <name>` or --account", len(accts), host)
		}
	}
	a, err := config.FindAccount(host, name)
	if err != nil {
		return "", nil, err
	}
	if a == nil {
		return "", nil, api.Exit(api.ExitAuth, "account %q is not stored for %s: `peaka auth list`", name, host)
	}
	if a.Kind == "oauth" {
		tok, err := r.oauthToken(*a)
		if err != nil {
			return "", nil, err
		}
		r.identity = fmt.Sprintf("browser login %q (%s) for %s", a.Name, a.Subject, host)
		return tok, a, nil
	}
	secret, err := config.Secret(*a)
	if err != nil {
		return "", nil, api.Exit(api.ExitAuth, "%v", err)
	}
	r.identity = fmt.Sprintf("account %q (%s) for %s", a.Name, keyPrefix(secret), host)
	return secret, a, nil
}

// api returns the authenticated client (built once).
func (r *Runtime) api() (*api.Client, error) {
	if r.client != nil {
		return r.client, nil
	}
	tok, _, err := r.token()
	if err != nil {
		return nil, err
	}
	r.client = r.newClient(tok)
	return r.client, nil
}

func (r *Runtime) newClient(token string) *api.Client {
	c := api.New(r.Settings.API, token, gf.timeout)
	if gf.debug {
		c.Debug = r.Err
	}
	if gf.dryRun {
		c.DryRun = r.Out
	}
	return c
}

func (r *Runtime) needProject() (string, error) {
	if r.Settings.Project == "" {
		return "", api.Exit(api.ExitUsage, "no project in context: `peaka projects use <name>`, --project, or PEAKA_PROJECT")
	}
	return r.Settings.Project, nil
}

func (r *Runtime) needOrg() (string, error) {
	if r.Settings.Org == "" {
		return "", api.Exit(api.ExitUsage, "no organization in context: `peaka orgs use <name>`, --org, or PEAKA_ORG")
	}
	return r.Settings.Org, nil
}

// keyPrefix shows the public part of an API key (the part before the dot, which
// the API itself reports as apiKeyPrefix) and hides the secret.
func keyPrefix(key string) string {
	if i := strings.IndexByte(key, '.'); i > 0 && i <= 16 {
		return key[:i] + ".…"
	}
	if len(key) <= 4 {
		return "…"
	}
	return key[:4] + "…"
}
