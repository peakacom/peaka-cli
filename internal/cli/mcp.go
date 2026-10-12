package cli

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/build"
)

//go:embed sql-rules.md
var sqlRules string

// mcpTool maps one MCP tool onto a peaka command line. Every call runs the
// CLI as a subprocess with --format json --no-input, so the tool gets exactly
// what a script would: JSON on success, problem+json and an exit code on failure.
type mcpTool struct {
	Name        string
	Description string
	Params      []mcpParam
	Args        func(a map[string]any) []string
	Format      string // output format for the subprocess (default json)
}

type mcpParam struct {
	Name, Description, Type string // Type: string (default), integer, boolean, array
	Required                bool
}

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mcp", Short: "Model Context Protocol server exposing the CLI to AI agents"}
	var timeout time.Duration
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve the CLI as MCP tools over stdio",
		Long: `Runs an MCP server on stdin/stdout. Each tool runs the corresponding peaka
command with the identity and context of this process (flags, environment,
configuration), so one server per configuration is the rule.

Claude Code:   claude mcp add peaka -- peaka mcp serve --project <id>
Claude Desktop / Cursor (mcpServers): {"peaka": {"command": "peaka", "args": ["mcp", "serve", "--project", "<id>"]}}

Tools: peaka_run (any command line), peaka_sql, and one tool per common
operation (list/describe/create for catalogs, tables, queries, caches,
metadata, connections, semantic catalogs). Resources: the agent guide and the
Trino SQL rule set.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return serveMCP(timeout)
		},
	}
	serve.Flags().DurationVar(&timeout, "tool-timeout", 5*time.Minute, "maximum run time of one tool call")
	cmd.AddCommand(serve)
	return cmd
}

func serveMCP(timeout time.Duration) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "peaka", Version: build.Version}, nil)
	for _, t := range mcpTools() {
		t := t
		server.AddTool(&mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.schema()}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var a map[string]any
			if len(req.Params.Arguments) > 0 {
				if err := json.Unmarshal(req.Params.Arguments, &a); err != nil {
					return toolError(fmt.Sprintf(`{"code":"USAGE","detail":"arguments are not a JSON object: %v"}`, err)), nil
				}
			}
			if a == nil {
				a = map[string]any{}
			}
			for _, p := range t.Params {
				if p.Required {
					if v, ok := a[p.Name]; !ok || v == nil || v == "" {
						return toolError(fmt.Sprintf(`{"code":"VALIDATION_FAILED","detail":"%s is required"}`, p.Name)), nil
					}
				}
			}
			args := t.Args(a)
			format := t.Format
			if format == "" {
				format = "json"
			}
			args = append(args, "--format", format, "--no-input")
			args = append(args, forwardedGlobals(a)...)
			return runTool(ctx, exe, args, timeout), nil
		})
	}
	server.AddResource(&mcp.Resource{URI: "peaka://guide", Name: "peaka_agent_guide", MIMEType: "text/markdown",
		Description: "How to drive the peaka CLI as an agent: grammar, output contract, exit codes"},
		func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "peaka://guide", MIMEType: "text/markdown", Text: agentGuide}}}, nil
		})
	server.AddResource(&mcp.Resource{URI: "peaka://sql-rules", Name: "peaka_sql_query_rule_set", MIMEType: "text/markdown",
		Description: "Rules for writing Trino SQL against Peaka: qualification, quoting, common mistakes to check before running a query"},
		func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "peaka://sql-rules", MIMEType: "text/markdown", Text: sqlRules}}}, nil
		})
	return server.Run(context.Background(), &mcp.StdioTransport{})
}

// forwardedGlobals passes the server's own context flags (and a per-call projectId) to the subprocess.
func forwardedGlobals(a map[string]any) []string {
	var out []string
	add := func(flag, v string) {
		if v != "" {
			out = append(out, flag, v)
		}
	}
	add("--configuration", gf.configuration)
	add("--environment", gf.environment)
	add("--api-url", gf.apiURL)
	add("--dbc-url", gf.dbcURL)
	add("--account", gf.account)
	add("--org", gf.org)
	if p := str(a["projectId"]); p != "" {
		add("--project", p)
	} else {
		add("--project", gf.project)
	}
	return out
}

func runTool(ctx context.Context, exe string, args []string, timeout time.Duration) *mcp.CallToolResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		code := -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = fmt.Sprintf(`{"code":"ERROR","detail":%q}`, err.Error())
		}
		return toolError(fmt.Sprintf("%s\n(exit %d)", msg, code))
	}
	text := strings.TrimSpace(stdout.String())
	if text == "" {
		text = strings.TrimSpace(stderr.String())
		if text == "" {
			text = `{"ok":true}`
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func toolError(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func (t mcpTool) schema() *jsonschema.Schema {
	s := &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{}}
	for _, p := range t.Params {
		typ := p.Type
		if typ == "" {
			typ = "string"
		}
		ps := &jsonschema.Schema{Type: typ, Description: p.Description}
		if typ == "array" {
			ps.Items = &jsonschema.Schema{Type: "string"}
		}
		s.Properties[p.Name] = ps
		if p.Required {
			s.Required = append(s.Required, p.Name)
		}
	}
	return s
}

// helpers for Args builders
func sarg(a map[string]any, k string) string { return str(a[k]) }
func opt(a map[string]any, k, flag string) []string {
	if v := str(a[k]); v != "" {
		return []string{flag, v}
	}
	return nil
}
func optBool(a map[string]any, k, flag string) []string {
	if b, _ := a[k].(bool); b {
		return []string{flag}
	}
	return nil
}
func cat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

var projectParam = mcpParam{Name: "projectId", Description: "Peaka project id; defaults to the server's project context"}

func mcpTools() []mcpTool {
	tbl := mcpParam{Name: "table", Description: "catalog.schema.table (quote reserved words: peaka.\"table\".customers)", Required: true}
	catalog := mcpParam{Name: "catalog", Description: "catalog id or name", Required: true}
	query := mcpParam{Name: "query", Description: "saved query id or name", Required: true}
	cache := mcpParam{Name: "cache", Description: "cache id, or the cached table as catalog.schema.table", Required: true}
	return []mcpTool{
		{Name: "peaka_run", Description: "Run any peaka command line and return its output. Use peaka_help to discover commands and flags. Output is JSON; on failure the problem+json and exit code (2 usage, 3 identity, 4 not found, 5 permission, 6 invalid, 7 conflict, 8 failed, 9 retry).",
			Params: []mcpParam{{Name: "args", Description: "command arguments, e.g. [\"catalogs\",\"list\"] or [\"tables\",\"describe\",\"stripe.payment.charges\"]", Type: "array", Required: true}, projectParam},
			Args: func(a map[string]any) []string {
				var out []string
				if xs, ok := a["args"].([]any); ok {
					for _, x := range xs {
						out = append(out, str(x))
					}
				}
				return out
			}},
		{Name: "peaka_help", Description: "The peaka command tree (JSON) for one command or all of them; with no command, the short agent guide.",
			Params: []mcpParam{{Name: "command", Description: "command path, e.g. \"queries create\""}},
			Args: func(a map[string]any) []string {
				if c := sarg(a, "command"); c != "" {
					return append([]string{"help"}, strings.Fields(c)...)
				}
				return []string{"help", "--agent"}
			}, Format: "json"},
		{Name: "peaka_sql", Description: "Run a Trino SQL statement on the project (read the peaka://sql-rules resource first). Returns rows as JSON Lines. Qualify tables as catalog.schema.table. Use LIMIT; maxRows caps the result (default 1000).",
			Params: []mcpParam{{Name: "statement", Description: "the SQL", Required: true}, {Name: "maxRows", Description: "stop after this many rows (0 = all)", Type: "integer"}, projectParam},
			Args: func(a map[string]any) []string {
				max := 1000
				if v, ok := a["maxRows"].(float64); ok {
					max = int(v)
				}
				return []string{"sql", sarg(a, "statement"), "--max-rows", strconv.Itoa(max), "--no-progress"}
			}, Format: "jsonl"},
		{Name: "peaka_list_projects", Description: "List the projects of the organization in context (or --org).",
			Params: []mcpParam{{Name: "org", Description: "organization id or name"}},
			Args:   func(a map[string]any) []string { return cat([]string{"projects", "list"}, opt(a, "org", "--org")) }},
		{Name: "peaka_list_connections", Description: "List the project's connections.", Params: []mcpParam{projectParam},
			Args: func(a map[string]any) []string { return []string{"connections", "list"} }},
		{Name: "peaka_get_connection_detail", Description: "Detailed view of a connection: status, catalogs built on it.",
			Params: []mcpParam{{Name: "connection", Description: "connection id or name", Required: true}, projectParam},
			Args: func(a map[string]any) []string {
				return []string{"connections", "describe", sarg(a, "connection"), "--detail"}
			}},
		{Name: "peaka_list_catalogs", Description: "List the catalogs of the project.", Params: []mcpParam{projectParam},
			Args: func(a map[string]any) []string { return []string{"catalogs", "list"} }},
		{Name: "peaka_list_schemas", Description: "List the schemas of a catalog.", Params: []mcpParam{catalog, projectParam},
			Args: func(a map[string]any) []string { return []string{"schemas", "list", sarg(a, "catalog")} }},
		{Name: "peaka_list_tables", Description: "List the tables of catalog.schema.",
			Params: []mcpParam{{Name: "schema", Description: "catalog.schema", Required: true}, projectParam},
			Args:   func(a map[string]any) []string { return []string{"tables", "list", sarg(a, "schema")} }},
		{Name: "peaka_list_columns", Description: "Columns and types of a table.", Params: []mcpParam{tbl, projectParam},
			Args: func(a map[string]any) []string { return []string{"tables", "describe", sarg(a, "table")} }},
		{Name: "peaka_get_table_statistics", Description: "Row count and column statistics of a table.", Params: []mcpParam{tbl, projectParam},
			Args: func(a map[string]any) []string { return []string{"tables", "stats", sarg(a, "table")} }},
		{Name: "peaka_search", Description: "Find catalogs, schemas and tables whose name contains a term.",
			Params: []mcpParam{{Name: "term", Description: "search term", Required: true}, projectParam},
			Args:   func(a map[string]any) []string { return []string{"catalogs", "search", sarg(a, "term")} }},
		{Name: "peaka_list_queries", Description: "List saved queries.", Params: []mcpParam{projectParam},
			Args: func(a map[string]any) []string { return []string{"queries", "list"} }},
		{Name: "peaka_get_query", Description: "Show a saved query (SQL, type, schedule, path).", Params: []mcpParam{query, projectParam},
			Args: func(a map[string]any) []string { return []string{"queries", "describe", sarg(a, "query")} }},
		{Name: "peaka_create_query", Description: "Save a query as peaka.query.<name>; a MATERIALIZED query refreshes on a schedule.",
			Params: []mcpParam{{Name: "name", Description: "display name", Required: true}, {Name: "sql", Description: "the SQL", Required: true}, {Name: "path", Description: "folder path, e.g. /sales"}, {Name: "type", Description: "PLAIN (default) or MATERIALIZED"}, {Name: "every", Description: "ISO-8601 refresh interval for MATERIALIZED, e.g. PT6H"}, {Name: "cron", Description: "cron refresh schedule for MATERIALIZED"}, projectParam},
			Args: func(a map[string]any) []string {
				return cat([]string{"queries", "create", "--name", sarg(a, "name"), "--sql", sarg(a, "sql")}, opt(a, "path", "--path"), opt(a, "type", "--type"), opt(a, "every", "--every"), opt(a, "cron", "--cron"))
			}},
		{Name: "peaka_update_query", Description: "Change a saved query's name or SQL.",
			Params: []mcpParam{query, {Name: "name", Description: "new display name"}, {Name: "sql", Description: "new SQL"}, projectParam},
			Args: func(a map[string]any) []string {
				return cat([]string{"queries", "update", sarg(a, "query")}, opt(a, "name", "--name"), opt(a, "sql", "--sql"))
			}},
		{Name: "peaka_delete_query", Description: "Delete a saved query.", Params: []mcpParam{query, projectParam},
			Args: func(a map[string]any) []string { return []string{"queries", "delete", sarg(a, "query"), "--yes"} }},
		{Name: "peaka_execute_query", Description: "Run a saved query and return its rows.", Params: []mcpParam{query, projectParam},
			Args: func(a map[string]any) []string { return []string{"queries", "execute", sarg(a, "query")} }},
		{Name: "peaka_get_materialized_query_statuses", Description: "Refresh status of materialized queries (all, or one).",
			Params: []mcpParam{{Name: "query", Description: "saved query id or name (optional)"}, projectParam},
			Args: func(a map[string]any) []string {
				return cat([]string{"queries", "status"}, func() []string {
					if q := sarg(a, "query"); q != "" {
						return []string{q}
					}
					return nil
				}())
			}},
		{Name: "peaka_refresh_materialized_query", Description: "Refresh a materialized query now.", Params: []mcpParam{query, projectParam},
			Args: func(a map[string]any) []string { return []string{"queries", "refresh", sarg(a, "query")} }},
		{Name: "peaka_get_project_metadata", Description: "AI metadata of the project's tables (descriptions, columns, relations); narrow with catalog and schema.",
			Params: []mcpParam{{Name: "catalog", Description: "catalog id or name"}, {Name: "schema", Description: "schema name"}, projectParam},
			Args: func(a map[string]any) []string {
				return cat([]string{"metadata", "describe"}, opt(a, "catalog", "--catalog"), opt(a, "schema", "--schema"))
			}},
		{Name: "peaka_refresh_project_metadata", Description: "Rebuild the AI metadata of the project or of one catalog (with catalog: waits for the job).",
			Params: []mcpParam{{Name: "catalog", Description: "catalog id or name"}, projectParam},
			Args: func(a map[string]any) []string {
				if c := sarg(a, "catalog"); c != "" {
					return []string{"metadata", "refresh", "--catalog", c, "--wait"}
				}
				return []string{"metadata", "refresh"}
			}},
		{Name: "peaka_get_metadata_refresh_status", Description: "Status of a catalog's metadata refresh job.", Params: []mcpParam{catalog, projectParam},
			Args: func(a map[string]any) []string { return []string{"metadata", "status", sarg(a, "catalog")} }},
		{Name: "peaka_get_relations", Description: "Relations between the tables of a catalog (for joins).", Params: []mcpParam{catalog, projectParam},
			Args: func(a map[string]any) []string { return []string{"metadata", "relations", "list", sarg(a, "catalog")} }},
		{Name: "peaka_query_golden_sqls", Description: "Golden SQL pairs similar to a question: known-good SQL to model on.",
			Params: []mcpParam{{Name: "question", Description: "the question", Required: true}, projectParam},
			Args: func(a map[string]any) []string {
				return []string{"metadata", "golden-sqls", "search", sarg(a, "question")}
			}},
		{Name: "peaka_semantic_search", Description: "Tables and columns relevant to a question, from the AI metadata.",
			Params: []mcpParam{{Name: "question", Description: "the question", Required: true}, projectParam},
			Args:   func(a map[string]any) []string { return []string{"metadata", "search", sarg(a, "question")} }},
		{Name: "peaka_get_cache_statuses", Description: "Caches of the project with their status.",
			Params: []mcpParam{{Name: "catalog", Description: "only this catalog"}, {Name: "schema", Description: "only this catalog.schema"}, projectParam},
			Args: func(a map[string]any) []string {
				return cat([]string{"cache", "list"}, opt(a, "catalog", "--catalog"), opt(a, "schema", "--schema"))
			}},
		{Name: "peaka_create_cache", Description: "Cache a table in Peaka, optionally on a schedule (cron-like expressions).",
			Params: []mcpParam{tbl, {Name: "fullRefresh", Description: "full refresh schedule"}, {Name: "incremental", Description: "incremental update schedule"}, projectParam},
			Args: func(a map[string]any) []string {
				return cat([]string{"cache", "create", "--table", sarg(a, "table")}, opt(a, "fullRefresh", "--full-refresh"), opt(a, "incremental", "--incremental"))
			}},
		{Name: "peaka_update_cache", Description: "Change a cache's schedules.",
			Params: []mcpParam{cache, {Name: "fullRefresh", Description: "full refresh schedule (empty removes)"}, {Name: "incremental", Description: "incremental schedule (empty removes)"}, projectParam},
			Args: func(a map[string]any) []string {
				return cat([]string{"cache", "update", sarg(a, "cache")}, opt(a, "fullRefresh", "--full-refresh"), opt(a, "incremental", "--incremental"))
			}},
		{Name: "peaka_delete_cache", Description: "Delete a cache.", Params: []mcpParam{cache, projectParam},
			Args: func(a map[string]any) []string { return []string{"cache", "delete", sarg(a, "cache"), "--yes"} }},
		{Name: "peaka_refresh_cache_full", Description: "Run a full refresh of a cache now.", Params: []mcpParam{cache, projectParam},
			Args: func(a map[string]any) []string { return []string{"cache", "refresh", sarg(a, "cache")} }},
		{Name: "peaka_refresh_cache_incremental", Description: "Run an incremental update of a cache now.", Params: []mcpParam{cache, projectParam},
			Args: func(a map[string]any) []string {
				return []string{"cache", "refresh", sarg(a, "cache"), "--incremental"}
			}},
		{Name: "peaka_create_semantic_catalog", Description: "Create a semantic catalog.",
			Params: []mcpParam{{Name: "name", Description: "catalog name", Required: true}, projectParam},
			Args:   func(a map[string]any) []string { return []string{"semantic-catalogs", "create", sarg(a, "name")} }},
		{Name: "peaka_create_semantic_table", Description: "Add a table fed by a saved query to a semantic catalog.",
			Params: []mcpParam{catalog, {Name: "schema", Description: "schema name", Required: true}, {Name: "table", Description: "table name", Required: true}, query, projectParam},
			Args: func(a map[string]any) []string {
				return []string{"semantic-catalogs", "add-table", sarg(a, "catalog"), "--schema", sarg(a, "schema"), "--table", sarg(a, "table"), "--query", sarg(a, "query")}
			}},
		{Name: "peaka_delete_semantic_table", Description: "Remove a semantic table.",
			Params: []mcpParam{catalog, {Name: "tableId", Description: "semantic table id", Required: true}, projectParam},
			Args: func(a map[string]any) []string {
				return []string{"semantic-catalogs", "remove-table", sarg(a, "catalog"), sarg(a, "tableId"), "--yes"}
			}},
		{Name: "peaka_list_exports", Description: "Exports of the project (files produced from tables or queries).", Params: []mcpParam{projectParam},
			Args: func(a map[string]any) []string { return []string{"exports", "list"} }},
		{Name: "peaka_export_query", Description: "Export a saved query's result to files and wait; the answer carries presigned download URLs.",
			Params: []mcpParam{query, {Name: "format", Description: "CSV (default), JSON, JSONL or PARQUET"}, projectParam},
			Args: func(a map[string]any) []string {
				return cat([]string{"queries", "export", sarg(a, "query"), "--wait"}, opt(a, "format", "--format-out"))
			}},
	}
}
