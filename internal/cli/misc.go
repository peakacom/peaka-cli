package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/output"
)

func asProblem(err error, p **api.Problem) bool { return errors.As(err, p) }

// mergeCurrent is a Prepare hook for update verbs: it GETs the current record
// and copies the listed fields the caller did not set, so a PUT changes only
// what was asked.
func mergeCurrent(pathTemplate string, keys ...string) func(*callCtx) error {
	return func(c *callCtx) error {
		client, err := rt.api()
		if err != nil {
			return err
		}
		path := pathTemplate
		for k, v := range c.Params {
			path = strings.ReplaceAll(path, "{"+k+"}", url.PathEscape(v))
		}
		var cur record
		if err := client.Get(rt.Ctx, path, nil, &cur); err != nil {
			return err
		}
		for _, k := range keys {
			if _, set := c.Body[k]; set {
				continue
			}
			if v, ok := cur[k]; ok && v != nil {
				c.Body[k] = v
			}
		}
		return nil
	}
}

// mergeTableMetadata prepares a versioned edit through the exact, authorized management read.
// Never downgrade to an unversioned save when that endpoint is unavailable.
func mergeTableMetadata(c *callCtx) error {
	project, err := rt.needProject()
	if err != nil {
		return err
	}
	client, err := rt.api()
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/metadata/%s/tables/%s/%s/%s", url.PathEscape(project), url.PathEscape(c.Params["catalog"]), url.PathEscape(c.Params["schema"]), url.PathEscape(c.Params["table"]))
	var current struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := client.Get(rt.Ctx, path, nil, &current); err != nil {
		return err
	}
	return prepareMetadataEdit(c.Body, current.Metadata)
}

func prepareMetadataEdit(body, current map[string]any) error {
	if current == nil || current["version"] == nil {
		return api.Exit(api.ExitFailed, "metadata has no editable version; refresh metadata before editing")
	}
	if version, present := body["version"]; present && version == nil {
		return api.Exit(api.ExitFailed, "version cannot be null")
	}
	if _, supplied := body["version"]; !supplied {
		body["version"] = current["version"]
	}
	// No column edits means no column updates; copying source projections can overwrite unrelated fields.
	if _, supplied := body["columns"]; !supplied {
		body["columns"] = []any{}
	}
	// An explicit setting (DEV-4133: --ai-setting) is the whole answer: the current boolean is not copied next to it,
	// or "on" for a table that inherits off would arrive with useWithAI=false and be refused as a conflict.
	if setting, explicit := body["useWithAISetting"]; explicit {
		if s, ok := setting.(string); !ok || (s != "inherit" && s != "on" && s != "off") {
			return api.Exit(api.ExitUsage, "--ai-setting must be inherit, on or off")
		}
		return nil
	}
	if _, supplied := body["useWithAI"]; !supplied {
		if setting := current["useWithAISetting"]; setting != nil {
			body["useWithAISetting"] = setting
		}
		if value, exists := current["useWithAI"]; exists {
			body["useWithAI"] = value
		}
	}
	return nil
}

func newSearchCmd() *cobra.Command {
	var catalog, schema string
	var limit, offset int
	cmd := &cobra.Command{
		Use:   "search TERM",
		Short: "Find catalogs, schemas and tables whose name contains a term",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			body := map[string]any{"query": args[0]}
			if catalog != "" {
				body["catalog"] = catalog
			}
			if schema != "" {
				body["schema"] = schema
			}
			if limit > 0 {
				body["limit"] = limit
			}
			if offset > 0 {
				body["offset"] = offset
			}
			client, err := rt.api()
			if err != nil {
				return err
			}
			var res record
			if _, err := client.Do(rt.Ctx, http.MethodPost, fmt.Sprintf("/data/projects/%s/search", project), nil, body, &res); err != nil {
				return err
			}
			var rows []record
			add := func(kind string, items any) {
				list, _ := items.([]any)
				for _, it := range list {
					m, _ := it.(map[string]any)
					r := record{"kind": kind}
					for k, v := range m {
						r[k] = v
					}
					rows = append(rows, r)
				}
			}
			add("catalog", res["matchedCatalogs"])
			add("schema", res["matchedSchemas"])
			add("table", res["matchedTables"])
			if rt.Settings.Format != "table" && rt.Settings.Format != "md" && rt.Settings.Format != "csv" && rt.Settings.Format != "tsv" {
				return rt.print(res)
			}
			return rt.printWith(rows, "kind", "catalog", "schema", "table")
		},
	}
	cmd.Flags().StringVar(&catalog, "catalog", "", "only this catalog (name)")
	_ = cmd.RegisterFlagCompletionFunc("catalog", flagCompleter(completeDotted(1)))
	cmd.Flags().StringVar(&schema, "schema", "", "only this schema")
	cmd.Flags().IntVar(&limit, "limit", 0, "results to return (default 100)")
	cmd.Flags().IntVar(&offset, "offset", 0, "results to skip")
	return cmd
}

// --- cache list / actions ------------------------------------------------------

func newCacheListCmd() *cobra.Command {
	var catalog, schema string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Caches of the project with their status (--catalog or --schema narrows)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			path := fmt.Sprintf("/data/projects/%s/cache/status", project)
			p := map[string]string{}
			switch {
			case schema != "":
				if err := resolveSchema(rt, schema, p); err != nil {
					return err
				}
				path = fmt.Sprintf("/data/projects/%s/catalog/%s/schema/%s/cache/status", project, p["catalog"], url.PathEscape(p["schema"]))
			case catalog != "":
				if err := resolveCatalog(rt, catalog, p); err != nil {
					return err
				}
				path = fmt.Sprintf("/data/projects/%s/catalog/%s/cache/status", project, p["catalog"])
			}
			return rt.call(http.MethodGet, path, nil, nil, "", []string{"id", "catalogId", "schemaName", "tableName", "status"}, "")
		},
	}
	cmd.Flags().StringVar(&catalog, "catalog", "", "catalog id or name")
	_ = cmd.RegisterFlagCompletionFunc("catalog", flagCompleter(completeDotted(1)))
	cmd.Flags().StringVar(&schema, "schema", "", "catalog.schema")
	return cmd
}

func newCacheActionCmd(use, fullOp, incOp, short string) *cobra.Command {
	var incremental bool
	cmd := &cobra.Command{
		Use:   use + " ID|TABLE",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := map[string]string{}
			if err := resolveCache(rt, args[0], p); err != nil {
				return err
			}
			project, _ := rt.needProject()
			op := fullOp
			if incremental {
				op = incOp
			}
			return rt.call(http.MethodPost, fmt.Sprintf("/data/projects/%s/cache/%s/%s", project, p["cache"], op), nil, nil, "", nil, "OK.")
		},
	}
	cmd.Flags().BoolVar(&incremental, "incremental", false, "the incremental update instead of the full refresh")
	return cmd
}

// --- queries execute -----------------------------------------------------------

func newQueriesExecuteCmd() *cobra.Command {
	var timeout int
	cmd := &cobra.Command{
		Use:               "execute ID|NAME",
		ValidArgsFunction: argCompleter(completeQueries),
		Short:             "Run a saved query through the REST API and print its rows",
		Long: `Runs the saved query server-side (POST /queries/execute) and prints the rows
in the usual formats. For ad-hoc SQL, streaming and cancellation use ` + "`peaka sql`" + `.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := map[string]string{}
			if err := resolveQuery(rt, args[0], p); err != nil {
				return err
			}
			project, _ := rt.needProject()
			body := map[string]any{"id": p["query"]}
			if timeout > 0 {
				body["timeout"] = timeout
			}
			client, err := rt.api()
			if err != nil {
				return err
			}
			var res struct {
				Columns []map[string]any `json:"columns"`
				Data    [][]struct {
					Name     string `json:"name"`
					DataType string `json:"dataType"`
					Value    any    `json:"value"`
				} `json:"data"`
				Tables []executedTable `json:"tables"`
			}
			if _, err := client.Do(rt.Ctx, http.MethodPost, fmt.Sprintf("/data/projects/%s/queries/execute", project), url.Values{"format": {"CELL_TYPED"}}, body, &res); err != nil {
				return err
			}
			defer noteExecutedTables(res.Tables)
			g := &output.Grid{}
			for _, c := range res.Columns {
				g.Columns = append(g.Columns, str(c["columnName"]))
				g.Types = append(g.Types, "")
			}
			for _, row := range res.Data {
				vals := make([]any, len(g.Columns))
				for i, cell := range row {
					if i < len(vals) {
						vals[i] = typedCell(cell.DataType, cell.Value)
						if g.Types[i] == "" {
							g.Types[i] = cell.DataType
						}
					}
				}
				g.Rows = append(g.Rows, vals)
			}
			format := rt.Settings.Format
			if format == "table" && gf.jq == "" && len(gf.fields) == 0 {
				return output.WriteGrid(rt.Out, g, format, rt.outputOptions())
			}
			if gf.jq != "" || gf.fields != "" || format == "yaml" || format == "vertical" || format == "md" {
				var rows []record
				for _, r := range g.Rows {
					m := record{}
					for i, c := range g.Columns {
						m[c] = r[i]
					}
					rows = append(rows, m)
				}
				return rt.printWith(rows, g.Columns...)
			}
			return output.WriteGrid(rt.Out, g, format, rt.outputOptions())
		},
	}
	cmd.Flags().IntVar(&timeout, "query-timeout", 0, "server-side timeout in seconds")
	return cmd
}

// --- metadata refresh ------------------------------------------------------------

func newMetadataRefreshCmd() *cobra.Command {
	var catalog, callbackURL, callbackToken string
	var wait bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Refresh source metadata of the project or a catalog; does not rebuild embeddings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if wait && timeout <= 0 {
				return api.Exit(api.ExitUsage, "--wait-timeout must be positive")
			}
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			body := map[string]any{}
			p := map[string]string{}
			if catalog != "" {
				if err := resolveCatalog(rt, catalog, p); err != nil {
					return err
				}
				body["catalogId"] = p["catalog"]
			}
			if callbackURL != "" {
				body["callbackURL"] = callbackURL
			}
			if callbackToken != "" {
				body["callbackToken"] = callbackToken
			}
			client, err := rt.api()
			if err != nil {
				return err
			}
			var started any
			if _, err := client.Do(rt.Ctx, http.MethodPost, fmt.Sprintf("/metadata/%s/refresh", project), nil, body, &started); err != nil {
				return err
			}
			if !wait || gf.dryRun {
				if started == nil {
					return nil
				}
				return rt.print(started)
			}
			return waitMetadataOperations(client, project, started, timeout)

		},
	}
	cmd.Flags().StringVar(&catalog, "catalog", "", "catalog id or name (default: the whole project)")
	_ = cmd.RegisterFlagCompletionFunc("catalog", flagCompleter(completeDotted(1)))
	cmd.Flags().StringVar(&callbackURL, "callback-url", "", "URL to call when the job finishes")
	cmd.Flags().StringVar(&callbackToken, "callback-token", "", "bearer token for the callback")
	cmd.Flags().BoolVar(&wait, "wait", false, "follow the exact accepted operations until they finish")
	cmd.Flags().DurationVar(&timeout, "wait-timeout", 30*time.Minute, "how long to wait; when it runs out the run IDs are printed and the exit code is 9 (the refresh goes on)")
	return cmd
}

// --- ai -----------------------------------------------------------------------

func newAIChatCmd() *cobra.Command {
	var thread string
	var noExecute, stream bool
	cmd := &cobra.Command{
		Use:   "chat MESSAGE",
		Short: "Ask the AI agent a question about the project's data",
		Long: `  peaka ai chat "How many deals closed last month?"
  peaka ai chat "…" --thread THREAD_ID          # continue a conversation
  peaka ai chat "…" --no-execute                 # the SQL, without running it
  peaka ai chat "…" --stream                     # server-sent events as they arrive (jsonl)

An answer can take minutes: without --timeout this command waits up to 10m.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			body := map[string]any{"message": args[0]}
			if thread != "" {
				body["threadId"] = thread
			}
			if noExecute {
				body["noExecute"] = true
			}
			// An answer takes minutes (retrieval, SQL, checks), longer than the per-request default.
			if f := cmd.Flags().Lookup("timeout"); f != nil && !f.Changed && gf.timeout < aiChatTimeout {
				gf.timeout = aiChatTimeout
			}
			client, err := rt.api()
			if err != nil {
				return err
			}
			if !stream {
				var out any
				if _, err := client.Do(rt.Ctx, http.MethodPost, fmt.Sprintf("/ai-agent/%s/chat", project), nil, body, &out); err != nil {
					return err
				}
				return rt.print(out)
			}
			// streaming: forward each event line as JSON on stdout
			payload, _ := json.Marshal(body)
			req, err := http.NewRequestWithContext(rt.Ctx, http.MethodPost, client.BaseURL+fmt.Sprintf("/ai-agent/%s/chat/stream", project), strings.NewReader(string(payload)))
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+client.Token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream, application/x-ndjson, application/json")
			resp, err := (&http.Client{}).Do(req)
			if err != nil {
				return &api.Problem{Code: "UPSTREAM_UNAVAILABLE", Detail: err.Error(), Retryable: true}
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				b, _ := io.ReadAll(resp.Body)
				return api.ProblemFromBody(resp.StatusCode, resp.Header, b)
			}
			sc := bufio.NewScanner(resp.Body)
			sc.Buffer(make([]byte, 1<<20), 16<<20)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if line == "" || strings.HasPrefix(line, ":") {
					continue
				}
				// The event name repeats the data's type; only data lines are output, one JSON value each.
				if strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:") {
					continue
				}
				line = strings.TrimPrefix(line, "data:")
				line = strings.TrimSpace(line)
				if rt.StdoutTTY && rt.Settings.Format == "table" {
					var ev map[string]any
					if json.Unmarshal([]byte(line), &ev) == nil {
						if t := firstNonEmpty(str(ev["content"]), str(ev["message"]), str(ev["text"])); t != "" {
							fmt.Fprint(rt.Out, t)
							continue
						}
					}
				}
				fmt.Fprintln(rt.Out, line)
			}
			if rt.StdoutTTY {
				fmt.Fprintln(rt.Out)
			}
			return sc.Err()
		},
	}
	cmd.Flags().StringVar(&thread, "thread", "", "thread id to continue")
	cmd.Flags().BoolVar(&noExecute, "no-execute", false, "produce the SQL without executing it")
	cmd.Flags().BoolVar(&stream, "stream", false, "stream the answer as it is produced")
	return cmd
}

// aiChatTimeout is how long `ai chat` waits for an answer unless --timeout is given.
const aiChatTimeout = 10 * time.Minute

func newAIWorkflowCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "workflow-image",
		Short: "Save the AI agent's workflow diagram as PNG",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := rt.api()
			if err != nil {
				return err
			}
			var raw []byte
			if _, err := client.Do(rt.Ctx, http.MethodGet, "/ai-agent/workflow/png", nil, nil, &raw); err != nil {
				return err
			}
			if out == "-" {
				_, err := rt.Out.Write(raw)
				return err
			}
			if err := os.WriteFile(out, raw, 0o644); err != nil {
				return err
			}
			rt.note("%s  %s", out, humanBytes(int64(len(raw))))
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "O", "workflow.png", "file to write (- for stdout)")
	return cmd
}

// --- sql transpile ---------------------------------------------------------------

func newSQLTranspileCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "transpile DIALECT [SQL]",
		Short: "Translate SQL from another dialect into Peaka's (Trino) SQL",
		Long: `  peaka sql transpile postgres "SELECT now()::date"
  peaka sql transpile mysql -f legacy.sql`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sql string
			switch {
			case len(args) == 2:
				sql = args[1]
			case file != "":
				b, err := readInput(file)
				if err != nil {
					return usage("-f: %v", err)
				}
				sql = string(b)
			case !rt.StdinTTY:
				b, _ := readInput("-")
				sql = string(b)
			default:
				return usage("give the SQL as an argument, with -f FILE, or on stdin")
			}
			client, err := rt.api()
			if err != nil {
				return err
			}
			var out record
			if _, err := client.Do(rt.Ctx, http.MethodPost, "/sql/transpile/"+url.PathEscape(strings.ToLower(args[0])), nil, map[string]any{"query": sql}, &out); err != nil {
				return err
			}
			if rt.Settings.Format == "table" {
				for _, k := range []string{"query", "sql", "transpiledQuery", "result"} {
					if s := str(out[k]); s != "" {
						fmt.Fprintln(rt.Out, s)
						return nil
					}
				}
			}
			return rt.print(out)
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "file with the SQL (- for stdin)")
	return cmd
}

// --- folders tree ---------------------------------------------------------------

func newFoldersTreeCmd() *cobra.Command {
	var withQueries bool
	var root string
	cmd := &cobra.Command{
		Use:   "tree",
		Short: "Folder tree, with --queries the queries in each folder",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			q := url.Values{}
			if withQueries {
				q.Set("includeQueries", "true")
			}
			if root != "" {
				p := map[string]string{}
				if err := resolveFolder(rt, root, p); err != nil {
					return err
				}
				q.Set("rootFolderId", p["folder"])
			}
			client, err := rt.api()
			if err != nil {
				return err
			}
			var res struct {
				Tree []any `json:"tree"`
			}
			if err := client.Get(rt.Ctx, fmt.Sprintf("/data/projects/%s/queries/folders/tree", project), q, &res); err != nil {
				return err
			}
			if rt.Settings.Format != "table" {
				return rt.print(res.Tree)
			}
			if len(res.Tree) == 0 {
				rt.note("no folders")
				return nil
			}
			var walk func(nodes []any, prefix string)
			walk = func(nodes []any, prefix string) {
				for i, nv := range nodes {
					n, _ := nv.(map[string]any)
					last := i == len(nodes)-1
					branch, next := "├── ", "│   "
					if last {
						branch, next = "└── ", "    "
					}
					fmt.Fprintf(rt.Out, "%s%s%s/\n", prefix, branch, str(n["name"]))
					qs, _ := n["queries"].([]any)
					children, _ := n["children"].([]any)
					for k, qv := range qs {
						qm, _ := qv.(map[string]any)
						b := "├── "
						if k == len(qs)-1 && len(children) == 0 {
							b = "└── "
						}
						fmt.Fprintf(rt.Out, "%s%s%s%s  (%s)\n", prefix, next, b, str(qm["displayName"]), str(qm["id"]))
					}
					walk(children, prefix+next)
				}
			}
			walk(res.Tree, "")
			return nil
		},
	}
	cmd.Flags().BoolVar(&withQueries, "queries", false, "include the queries of each folder")
	cmd.Flags().StringVar(&root, "root", "", "start from this folder (id or path)")
	return cmd
}

// typedCell turns the string a CELL_TYPED answer carries into the value its
// type names, so JSON output has numbers and booleans as `peaka sql` does.
// Decimals stay strings (no precision loss), as they do over dbc.
func typedCell(dataType string, v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	t := strings.ToLower(dataType)
	if i := strings.IndexByte(t, '('); i > 0 {
		t = t[:i]
	}
	switch t {
	case "bigint", "integer", "int", "smallint", "tinyint":
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	case "double", "real", "float":
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	case "boolean":
		if b, err := strconv.ParseBool(s); err == nil {
			return b
		}
	}
	return v
}
