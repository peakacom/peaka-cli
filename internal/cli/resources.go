package cli

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/peakacom/peaka-cli/internal/api"

	"github.com/spf13/cobra"
)

// The resource commands built from opSpec. Hand-written verbs (connections
// create, exports download, queries execute, tables import, …) are added to
// the same groups from their own files.

func newCatalogsCmd() *cobra.Command {
	cmd := group("catalogs", "Catalogs of the project: the data sources attached through connections", []string{"catalog"},
		opSpec{Use: "list", Short: "List catalogs", Method: http.MethodGet, Path: "/data/projects/{project}/catalogs",
			Fields: []string{"id", "name", "displayName", "catalogType", "connectionId"}},
		opSpec{Use: "create", Short: "Create a catalog on a connection", Method: http.MethodPost, Path: "/data/projects/{project}/catalogs", Body: true,
			Long: `A catalog is how a connection's data appears in SQL, as catalog.schema.table.

  peaka catalogs create --name hubspot --connection crm
  peaka catalogs create --name sheets --connection sheets -f extraParameters:='{"spreadsheetId":"…"}'`,
			Flags: []flagSpec{
				{Name: "name", Help: "catalog name, as used in SQL", Required: true},
				{Name: "connection", Help: "connection id or name", Required: true, Resolve: resolveConnection},
			},
			Prepare: paramsIntoBody("connection", "connectionId")},
		opSpec{Use: "delete CATALOG", Short: "Delete a catalog", Method: http.MethodDelete, Path: "/data/projects/{project}/catalogs/{catalog}",
			Args: []argSpec{{Name: "CATALOG", Resolve: resolveOneCatalog}}, Confirm: "Delete catalog {0} and its caches?", Empty: "Deleted."},
		opSpec{Use: "share CATALOG", Short: "Share a catalog with another project", Method: http.MethodPost, Path: "/data/projects/{project}/catalogs/{catalog}/share",
			Args:  []argSpec{{Name: "CATALOG", Resolve: resolveOneCatalog}},
			Flags: []flagSpec{{Name: "to-project", Key: "targetProjectId", Help: "target project id", Required: true}}},
	)
	cmd.AddCommand(newCatalogsDescribeCmd(), newSearchCmd())
	return cmd
}

func newSchemasCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "schemas", Aliases: []string{"schema"}, Short: "Schemas of a catalog"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list CATALOG",
		Short: "List the schemas of a catalog (id or name)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := map[string]string{}
			if err := resolveCatalog(rt, args[0], p); err != nil {
				return err
			}
			project, _ := rt.needProject()
			var all []record
			for _, id := range catalogIDs(p) {
				ss, err := rt.listRecords(fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas", project, url.PathEscape(id)), nil, "")
				if err != nil {
					return err
				}
				all = append(all, ss...)
			}
			return rt.printWith(all, "catalogId", "catalogName", "schemaName")
		},
	})
	return cmd
}

func newCatalogsDescribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "describe CATALOG",
		Aliases: []string{"get"},
		Short:   "Show one catalog (id or name)",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := map[string]string{}
			if err := resolveCatalog(rt, args[0], p); err != nil {
				return err
			}
			project, _ := rt.needProject()
			client, err := rt.api()
			if err != nil {
				return err
			}
			var out []any
			for _, id := range catalogIDs(p) {
				var c any
				if err := client.Get(rt.Ctx, fmt.Sprintf("/data/projects/%s/catalogs/%s", project, url.PathEscape(id)), nil, &c); err != nil {
					// the read endpoint does not know internal catalogs; the list entry is all there is
					var pr *api.Problem
					if !asProblem(err, &pr) || pr.Status != http.StatusNotFound {
						return err
					}
					for _, r := range rt.catalogs {
						if str(r["id"]) == id {
							c = r
						}
					}
					if c == nil {
						return err
					}
				}
				out = append(out, c)
			}
			if len(out) == 1 {
				return rt.print(out[0])
			}
			rt.note("%d catalogs are named %q (Peaka's internal catalog has one entry per schema family)", len(out), args[0])
			return rt.print(out)
		},
	}
}

func newCacheCmd() *cobra.Command {
	cmd := group("cache", "Caches: scheduled copies of external tables kept in Peaka", []string{"caches"},
		opSpec{Use: "describe ID|TABLE", Aliases: []string{"get"}, Short: "Show a cache's settings (by id, or by catalog.schema.table)", Method: http.MethodGet, Path: "/data/projects/{project}/cache/{cache}",
			Args: []argSpec{{Name: "ID", Resolve: resolveCache}}},
		opSpec{Use: "status ID|TABLE", Short: "Status of a cache and its last executions", Method: http.MethodGet, Path: "/data/projects/{project}/cache/{cache}/status",
			Args:  []argSpec{{Name: "ID", Resolve: resolveCache}},
			Flags: []flagSpec{{Name: "logs", Kind: "bool", Help: "include the action log"}},
			Prepare: func(c *callCtx) error {
				if _, logs := c.Body["logs"]; !logs {
					c.Query.Set("excludeLogs", "true")
				}
				delete(c.Body, "logs")
				return nil
			}},
		opSpec{Use: "create", Short: "Cache a table on a schedule", Method: http.MethodPost, Path: "/data/projects/{project}/cache", Body: true,
			Long: `Schedules are BASIC expressions (cron-like); without one the cache is filled once.

  peaka cache create --table stripe.payment.charges --full-refresh '0 3 * * *'
  peaka cache create --table stripe.payment.charges --incremental '*/15 * * * *'`,
			Flags: []flagSpec{
				{Name: "table", Help: "catalog.schema.table to cache", Required: true, Resolve: resolveTable},
				{Name: "full-refresh", Help: "full refresh schedule expression", Set: scheduleSetter("fullRefreshCacheSchedule")},
				{Name: "incremental", Help: "incremental update schedule expression", Set: scheduleSetter("incrementalCacheSchedule")},
			},
			Prepare: paramsIntoBody("catalog", "catalogId", "schema", "schemaName", "table", "tableName")},
		opSpec{Use: "update ID|TABLE", Short: "Change a cache's schedules", Method: http.MethodPut, Path: "/data/projects/{project}/cache/{cache}", Body: true,
			Args: []argSpec{{Name: "ID", Resolve: resolveCache}},
			Flags: []flagSpec{
				{Name: "full-refresh", Help: "full refresh schedule expression (empty to remove)", Set: scheduleSetter("fullRefreshCacheSchedule")},
				{Name: "incremental", Help: "incremental update schedule expression (empty to remove)", Set: scheduleSetter("incrementalCacheSchedule")},
			}},
		opSpec{Use: "delete ID|TABLE", Short: "Delete a cache", Method: http.MethodDelete, Path: "/data/projects/{project}/cache/{cache}",
			Args: []argSpec{{Name: "ID", Resolve: resolveCache}}, Confirm: "Delete cache {0}?", Empty: "Deleted."},
		opSpec{Use: "history ID|TABLE", Short: "Execution history of a cache", Method: http.MethodGet, Path: "/data/projects/{project}/cache/{cache}/executionHistory", Query: true,
			Args: []argSpec{{Name: "ID", Resolve: resolveCache}},
			Flags: []flagSpec{
				{Name: "limit", Kind: "int", Default: 20, In: "query", Help: "rows to return"},
				{Name: "offset", Kind: "int", In: "query", Help: "rows to skip"},
			}},
	)
	cmd.AddCommand(newCacheListCmd(),
		newCacheActionCmd("refresh", "fullRefreshUpdate", "incrementalUpdate", "Run a cache update now (full refresh, or --incremental)"),
		newCacheActionCmd("cancel", "cancelFullRefreshUpdate", "cancelIncrementalUpdate", "Cancel a running cache update (full refresh, or --incremental)"))
	return cmd
}

func scheduleSetter(key string) func(any, map[string]any) {
	return func(v any, b map[string]any) {
		expr := str(v)
		if expr == "" {
			b[key] = map[string]any{"type": "NONE"}
			return
		}
		b[key] = map[string]any{"type": "BASIC", "expression": expr}
	}
}

func newQueriesCmd() *cobra.Command {
	cmd := group("queries", "Saved queries: SQL kept in the project as peaka.query.<name>", []string{"query"},
		opSpec{Use: "list", Short: "List saved queries", Method: http.MethodGet, Path: "/data/projects/{project}/queries",
			Fields: []string{"id", "displayName", "queryType", "path", "valid"}},
		opSpec{Use: "describe ID|NAME", Aliases: []string{"get"}, Short: "Show one saved query", Method: http.MethodGet, Path: "/data/projects/{project}/queries/{query}",
			Args: []argSpec{{Name: "ID", Resolve: resolveQuery}}},
		opSpec{Use: "create", Short: "Save a query", Method: http.MethodPost, Path: "/data/projects/{project}/queries", Body: true,
			Long: `  peaka queries create --name daily_revenue --sql 'SELECT …'
  peaka queries create --name daily_revenue --sql-file q.sql --path /finance
  peaka queries create --name revenue_mv --type MATERIALIZED --materialize daily_revenue --every PT6H`,
			Flags: queryFlags(true), Prepare: paramsIntoBody("materialize", "inputQueryRefId")},
		opSpec{Use: "update ID|NAME", Short: "Change a saved query (unset fields keep their value)", Method: http.MethodPut, Path: "/data/projects/{project}/queries/{query}", Body: true,
			Args: []argSpec{{Name: "ID", Resolve: resolveQuery}}, Flags: queryFlags(false),
			Prepare: chain(paramsIntoBody("materialize", "inputQueryRefId"), mergeCurrent("/data/projects/{project}/queries/{query}", "displayName", "inputQuery", "queryType", "schedule", "inputQueryRefId", "path"))},
		opSpec{Use: "delete ID|NAME", Short: "Delete a saved query", Method: http.MethodDelete, Path: "/data/projects/{project}/queries/{query}",
			Args: []argSpec{{Name: "ID", Resolve: resolveQuery}}, Confirm: "Delete query {0}?", Empty: "Deleted."},
		opSpec{Use: "move ID|NAME", Short: "Move a saved query to a folder path", Method: http.MethodPatch, Path: "/data/projects/{project}/queries/{query}/path",
			Args:  []argSpec{{Name: "ID", Resolve: resolveQuery}},
			Flags: []flagSpec{{Name: "to", Key: "path", Help: "folder path, e.g. /analytics/dashboards (intermediate folders are created)", Required: true}}, Empty: "Moved."},
		opSpec{Use: "status [ID|NAME]", Short: "Refresh status of materialized queries (all, or one)", Method: http.MethodGet, Path: "/data/projects/{project}/materialized-queries/status",
			Args:    []argSpec{{Name: "ID", Optional: true, Resolve: resolveQuery}},
			Prepare: pathWhen("query", "/data/projects/{project}/materialized-queries/{query}/status")},
		opSpec{Use: "refresh ID|NAME", Short: "Refresh a materialized query now", Method: http.MethodPost, Path: "/data/projects/{project}/materialized-queries/{query}/refresh",
			Args: []argSpec{{Name: "ID", Resolve: resolveQuery}}, Empty: "Refresh started."},
		opSpec{Use: "cancel-refresh ID|NAME", Short: "Cancel a running materialized query refresh", Method: http.MethodPost, Path: "/data/projects/{project}/materialized-queries/{query}/cancel",
			Args: []argSpec{{Name: "ID", Resolve: resolveQuery}}, Empty: "Cancelled."},
	)
	cmd.AddCommand(newQueriesExecuteCmd(), newQueriesExportCmd())
	return cmd
}

func queryFlags(create bool) []flagSpec {
	return []flagSpec{
		{Name: "name", Key: "displayName", Help: "display name", Required: create},
		{Name: "sql", Key: "inputQuery", Help: "the SQL"},
		{Name: "sql-file", Kind: "file", Key: "inputQuery", Help: "file with the SQL (- for stdin)"},
		{Name: "type", Key: "queryType", Help: "PLAIN or MATERIALIZED"},
		{Name: "materialize", Help: "saved query (id or name) to materialize, with --type MATERIALIZED", Resolve: func(r *Runtime, v string, p map[string]string) error {
			tmp := map[string]string{}
			if err := resolveQuery(r, v, tmp); err != nil {
				return err
			}
			p["materialize"] = tmp["query"]
			return nil
		}},
		{Name: "path", Help: "folder path to place the query in"},
		{Name: "cron", Help: "refresh schedule as a UNIX cron expression (materialized)", Set: func(v any, b map[string]any) { sched(b)["type"] = "cron"; sched(b)["cronExpression"] = v }},
		{Name: "every", Help: "refresh interval as an ISO-8601 duration, e.g. PT6H (materialized)", Set: func(v any, b map[string]any) { sched(b)["type"] = "interval"; sched(b)["repeatDuration"] = v }},
		{Name: "timezone", Help: "IANA timezone for --cron", Set: func(v any, b map[string]any) { sched(b)["timezone"] = v }},
		{Name: "check-schema", Kind: "bool", In: "query", Key: "checkSchemaExists", Help: "fail when the SQL names a schema that does not exist"},
	}
}

func sched(b map[string]any) map[string]any {
	s, ok := b["schedule"].(map[string]any)
	if !ok {
		s = map[string]any{}
		b["schedule"] = s
	}
	return s
}

func newFoldersCmd() *cobra.Command {
	cmd := group("folders", "Folders that organize saved queries", []string{"folder"},
		opSpec{Use: "list", Short: "List folders", Method: http.MethodGet, Path: "/data/projects/{project}/queries/folders", Unwrap: "items",
			Fields: []string{"id", "name", "path"}},
		opSpec{Use: "describe ID|PATH", Aliases: []string{"get"}, Short: "Show one folder", Method: http.MethodGet, Path: "/data/projects/{project}/queries/folders/{folder}",
			Args: []argSpec{{Name: "ID", Resolve: resolveFolder}}},
		opSpec{Use: "create NAME", Short: "Create a folder at the root, or under --parent", Method: http.MethodPost, Path: "/data/projects/{project}/queries/folders",
			Args:    []argSpec{{Name: "NAME", Param: "folderName"}},
			Flags:   []flagSpec{{Name: "parent", Help: "parent folder id or path", Resolve: resolveFolder}},
			Prepare: chain(paramsIntoBody("folderName", "folderName"), pathWhen("folder", "/data/projects/{project}/queries/folders/{folder}"))},
		opSpec{Use: "rename ID|PATH NAME", Short: "Rename a folder", Method: http.MethodPatch, Path: "/data/projects/{project}/queries/folders/{folder}/name",
			Args: []argSpec{{Name: "ID", Resolve: resolveFolder}, {Name: "NAME", Param: "name"}}, Prepare: paramsIntoBody("name", "name")},
		opSpec{Use: "move ID|PATH", Short: "Move a folder into another path", Method: http.MethodPatch, Path: "/data/projects/{project}/queries/folders/{folder}/path",
			Args:  []argSpec{{Name: "ID", Resolve: resolveFolder}},
			Flags: []flagSpec{{Name: "to", Key: "path", Help: "destination folder path, or / for the root", Required: true}}},
		opSpec{Use: "delete ID|PATH", Short: "Delete a folder", Method: http.MethodDelete, Path: "/data/projects/{project}/queries/folders/{folder}",
			Args:    []argSpec{{Name: "ID", Resolve: resolveFolder}},
			Flags:   []flagSpec{{Name: "force", Kind: "bool", In: "query", Help: "delete even when the folder has content"}},
			Confirm: "Delete folder {0}?", Empty: "Deleted."},
		opSpec{Use: "add ID|PATH QUERY...", Short: "Put saved queries (id or name) into a folder", Method: http.MethodPut, Path: "/data/projects/{project}/queries/folders/{folder}",
			Args: []argSpec{{Name: "ID", Resolve: resolveFolder}}, Prepare: folderQueries("queryIdsAdded")},
		opSpec{Use: "remove ID|PATH QUERY...", Short: "Take saved queries out of a folder", Method: http.MethodPut, Path: "/data/projects/{project}/queries/folders/{folder}",
			Args: []argSpec{{Name: "ID", Resolve: resolveFolder}}, Prepare: folderQueries("queryIdsRemoved")},
	)
	cmd.AddCommand(newFoldersTreeCmd())
	return cmd
}

// folderQueries resolves the query arguments after the folder into ids.
func folderQueries(key string) func(*callCtx) error {
	return func(c *callCtx) error {
		if len(c.Args) < 2 {
			return usage("name at least one query")
		}
		var ids []string
		for _, q := range c.Args[1:] {
			p := map[string]string{}
			if err := resolveQuery(rt, q, p); err != nil {
				return err
			}
			ids = append(ids, p["query"])
		}
		c.Body[key] = ids
		return nil
	}
}

func newExportsCmd() *cobra.Command {
	cmd := group("exports", "Exports: files produced from a table or a saved query", []string{"export"},
		opSpec{Use: "list", Short: "List exports", Method: http.MethodGet, Path: "/data/projects/{project}/exports", Query: true,
			Flags: []flagSpec{
				{Name: "status", In: "query", Help: "only this status: PENDING, RUNNING, SUCCEEDED, FAILED, CANCELLED, EXPIRED"},
				{Name: "limit", Kind: "int", In: "query", Help: "rows to return"},
				{Name: "created-before", In: "query", Key: "createdBefore", Help: "ISO-8601 instant, for paging"},
			},
			Fields: []string{"id", "status", "format", "rowCount", "bytes", "source", "createdAt"}},
		opSpec{Use: "describe ID", Aliases: []string{"get"}, Short: "Show one export with its download links", Method: http.MethodGet, Path: "/data/projects/{project}/exports/{export}",
			Args: []argSpec{{Name: "ID", Param: "export"}}},
		opSpec{Use: "cancel ID", Short: "Cancel a running export", Method: http.MethodDelete, Path: "/data/projects/{project}/exports/{export}",
			Args: []argSpec{{Name: "ID", Param: "export"}}, Empty: "Cancelled."},
	)
	cmd.AddCommand(newExportsDownloadCmd(), newExportsWaitCmd())
	return cmd
}

func newSemanticCatalogsCmd() *cobra.Command {
	return group("semantic-catalogs", "Semantic catalogs: tables defined by saved queries, shareable across projects", []string{"semantic-catalog"},
		opSpec{Use: "create NAME", Short: "Create a semantic catalog", Method: http.MethodPost, Path: "/data/projects/{project}/semantic-catalogs",
			Args: []argSpec{{Name: "NAME", Param: "name"}}, Prepare: paramsIntoBody("name", "name")},
		opSpec{Use: "add-table CATALOG", Short: "Add a table fed by a saved query", Method: http.MethodPost, Path: "/data/projects/{project}/semantic-catalogs/{catalog}",
			Args: []argSpec{{Name: "CATALOG", Resolve: resolveOneCatalog}},
			Flags: []flagSpec{
				{Name: "schema", Key: "schemaName", Help: "schema name", Required: true},
				{Name: "table", Key: "tableName", Help: "table name", Required: true},
				{Name: "query", Help: "saved query id or name", Required: true, Resolve: resolveQuery},
			},
			Prepare: paramsIntoBody("query", "queryId")},
		opSpec{Use: "remove-table CATALOG TABLE_ID", Short: "Remove a semantic table", Method: http.MethodDelete, Path: "/data/projects/{project}/semantic-catalogs/{catalog}/table/{tableId}",
			Args: []argSpec{{Name: "CATALOG", Resolve: resolveOneCatalog}, {Name: "TABLE_ID", Param: "tableId"}}, Confirm: "Remove semantic table {1}?", Empty: "Removed."},
		opSpec{Use: "share CATALOG", Short: "Share a semantic catalog with another project", Method: http.MethodPost, Path: "/data/projects/{project}/semantic-catalogs/{catalog}/share",
			Args:  []argSpec{{Name: "CATALOG", Resolve: resolveOneCatalog}},
			Flags: []flagSpec{{Name: "to-project", Key: "target", Help: "target project id", Required: true}}},
	)
}

func newMetadataCmd() *cobra.Command {
	cmd := group("metadata", "AI metadata: descriptions, relations, golden SQL and semantics of the project's tables", nil,
		opSpec{Use: "describe", Aliases: []string{"get"}, Short: "Project metadata (all, or one catalog or schema)", Method: http.MethodGet, Path: "/metadata/{project}", Unwrap: "metadata",
			Flags: []flagSpec{
				{Name: "catalog", Help: "catalog id or name", Resolve: resolveOneCatalog},
				{Name: "schema", In: "query", Key: "schemaName", Help: "schema name"},
			},
			Prepare: paramsIntoQuery("catalog", "catalogId")},
		opSpec{Use: "status CATALOG", Short: "Status of the metadata refresh job of a catalog", Method: http.MethodGet, Path: "/metadata/{project}/refresh/{catalog}",
			Args: []argSpec{{Name: "CATALOG", Resolve: resolveOneCatalog}}},
		opSpec{Use: "update TABLE", Short: "Set a table's description, columns and AI usage (catalog.schema.table)", Method: http.MethodPut, Path: "/metadata/{project}/{catalog}/{schema}/{table}", Body: true,
			Long: `  peaka metadata update hubspot.crm.deals --description "Closed and open deals" --use-with-ai
  peaka metadata update hubspot.crm.deals --from-file deals-metadata.json   # tableDescription, columns[], useWithAI`,
			Args: []argSpec{{Name: "TABLE", Resolve: resolveTable}},
			Flags: []flagSpec{
				{Name: "description", Key: "tableDescription", Help: "table description"},
				{Name: "use-with-ai", Kind: "bool", Key: "useWithAI", Help: "expose the table to the AI agent"},
			},
			Prepare: mergeTableMetadata},
		opSpec{Use: "ai-usage CATALOG[.SCHEMA]", Short: "Turn AI usage on or off for a whole catalog or schema", Method: http.MethodPut, Path: "/metadata/{project}/{catalog}",
			Args:    []argSpec{{Name: "CATALOG", Resolve: resolveCatalogOrSchema}},
			Flags:   []flagSpec{{Name: "use-with-ai", Kind: "bool", Key: "useWithAI", Help: "true or false", Required: true}},
			Prepare: pathWhen("schema", "/metadata/{project}/{catalog}/{schema}")},
		opSpec{Use: "search QUESTION", Short: "Semantic search over the metadata: tables and columns relevant to a question", Method: http.MethodGet, Path: "/metadata/{project}/query",
			Args:    []argSpec{{Name: "QUESTION", Param: "q"}},
			Flags:   []flagSpec{{Name: "top-k", Kind: "int", In: "query", Key: "topK", Help: "number of results"}},
			Prepare: paramsIntoQuery("q", "table")},
		opSpec{Use: "questions", Short: "Generate sample questions the AI agent can answer", Method: http.MethodGet, Path: "/metadata/{project}/questions/generate",
			Flags: []flagSpec{{Name: "about", In: "query", Key: "question", Help: "a seed question"}}},
		opSpec{Use: "generate-semantics TABLE", Short: "Generate semantics for a table with the AI agent", Method: http.MethodPost, Path: "/metadata/{project}/semantics/{catalog}/{schema}/{table}", Body: true,
			Args: []argSpec{{Name: "TABLE", Resolve: resolveTable}},
			Flags: []flagSpec{
				{Name: "thread", Key: "threadId", Help: "AI thread id"},
				{Name: "prompt", Key: "initialPrompt", Help: "initial prompt"},
				{Name: "limit", Kind: "int", Help: "limit"},
			}},
	)
	cmd.AddCommand(
		newMetadataRefreshCmd(),
		group("relations", "Relations between tables, used by the AI agent for joins", nil,
			opSpec{Use: "list CATALOG", Short: "Relations of a catalog", Method: http.MethodGet, Path: "/metadata/{project}/relations/{catalog}",
				Args: []argSpec{{Name: "CATALOG", Resolve: resolveOneCatalog}}},
			opSpec{Use: "add COLUMN", Short: "Add a relation from catalog.schema.table.column to --target", Method: http.MethodPost, Path: "/metadata/{project}/relations/{catalog}/{schema}/{table}/{column}", Body: true,
				Args: []argSpec{{Name: "COLUMN", Resolve: resolveColumn}},
				Flags: []flagSpec{
					{Name: "target", Help: "target catalog.schema.table.column", Required: true, Resolve: func(r *Runtime, v string, p map[string]string) error {
						t := map[string]string{}
						if err := resolveColumn(r, v, t); err != nil {
							return err
						}
						p["targetCatalog"], p["targetSchema"], p["targetTable"], p["targetColumn"] = t["catalog"], t["schema"], t["table"], t["column"]
						return nil
					}},
					{Name: "type", Help: "relation type", Required: true},
				},
				Prepare: paramsIntoBody("targetCatalog", "targetCatalogId", "targetSchema", "targetSchemaName", "targetTable", "targetTableName", "targetColumn", "targetColumnName")},
			opSpec{Use: "update TABLE RELATION_ID", Short: "Change a relation's type", Method: http.MethodPut, Path: "/metadata/{project}/relations/{catalog}/{schema}/{table}/{relation}",
				Args:  []argSpec{{Name: "TABLE", Resolve: resolveTable}, {Name: "RELATION_ID", Param: "relation"}},
				Flags: []flagSpec{{Name: "type", Help: "relation type", Required: true}}},
			opSpec{Use: "delete TABLE RELATION_ID", Short: "Delete a relation", Method: http.MethodDelete, Path: "/metadata/{project}/relations/{catalog}/{schema}/{table}/{relation}",
				Args: []argSpec{{Name: "TABLE", Resolve: resolveTable}, {Name: "RELATION_ID", Param: "relation"}}, Confirm: "Delete relation {1}?", Empty: "Deleted."},
		),
		group("golden-sqls", "Golden SQL: question and SQL pairs that teach the AI agent", []string{"golden-sql"},
			opSpec{Use: "list", Short: "List golden SQL pairs", Method: http.MethodGet, Path: "/metadata/{project}/golden-sqls", Unwrap: "result"},
			opSpec{Use: "add", Short: "Add a pair", Method: http.MethodPost, Path: "/metadata/{project}/golden-sqls", Body: true,
				Flags: []flagSpec{{Name: "prompt", Help: "the question", Required: true}, {Name: "sql", Help: "the SQL that answers it"}, {Name: "sql-file", Kind: "file", Key: "sql", Help: "file with the SQL"}}},
			opSpec{Use: "delete ID", Short: "Delete a pair", Method: http.MethodDelete, Path: "/metadata/{project}/golden-sqls/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}, Empty: "Deleted."},
			opSpec{Use: "search QUESTION", Short: "Find pairs similar to a question", Method: http.MethodGet, Path: "/metadata/{project}/golden-sql/query",
				Args: []argSpec{{Name: "QUESTION", Param: "q"}}, Prepare: paramsIntoQuery("q", "q")},
		),
		group("categorical", "Categorical column values, generated for the AI agent", nil,
			opSpec{Use: "generate COLUMN", Short: "Generate the categorical values of catalog.schema.table.column", Method: http.MethodPut, Path: "/metadata/{project}/categorical/{catalog}/{schema}/{table}/{column}", Body: true,
				Args:  []argSpec{{Name: "COLUMN", Resolve: resolveColumn}},
				Flags: []flagSpec{{Name: "limit", Kind: "int", Help: "maximum number of values"}, {Name: "frequency", Help: "refresh frequency"}}},
			opSpec{Use: "delete COLUMN", Short: "Remove the categorical values of a column", Method: http.MethodDelete, Path: "/metadata/{project}/categorical/{catalog}/{schema}/{table}/{column}",
				Args: []argSpec{{Name: "COLUMN", Resolve: resolveColumn}}, Empty: "Deleted."},
		),
		group("callback", "Webhook called when a metadata refresh finishes", nil,
			opSpec{Use: "describe", Aliases: []string{"get"}, Short: "Show the registered callback", Method: http.MethodGet, Path: "/metadata/{project}/refresh/callback"},
			opSpec{Use: "set", Short: "Register the callback", Method: http.MethodPost, Path: "/metadata/{project}/refresh/callback", Body: true,
				Flags: []flagSpec{{Name: "url", Key: "callbackURL", Help: "URL to call", Required: true}, {Name: "token", Key: "callbackToken", Help: "bearer token sent with the call"}}},
			opSpec{Use: "unset", Short: "Remove the callback", Method: http.MethodDelete, Path: "/metadata/{project}/refresh/callback", Empty: "Removed."},
		),
	)
	return cmd
}

func newAICmd() *cobra.Command {
	cmd := group("ai", "The AI agent: chat with your data, manage threads", nil,
		opSpec{Use: "threads", Short: "List chat threads", Method: http.MethodGet, Path: "/ai-agent/{project}/threads", Unwrap: "threads",
			Fields: []string{"threadId", "displayName", "source", "updatedAt"}},
		opSpec{Use: "describe THREAD", Aliases: []string{"get"}, Short: "Show a thread with its messages", Method: http.MethodGet, Path: "/ai-agent/{project}/threads/{thread}",
			Args: []argSpec{{Name: "THREAD", Param: "thread"}}},
		opSpec{Use: "rename THREAD NAME", Short: "Rename a thread", Method: http.MethodPut, Path: "/ai-agent/{project}/threads/{thread}",
			Args: []argSpec{{Name: "THREAD", Param: "thread"}, {Name: "NAME", Param: "displayName"}}, Prepare: paramsIntoBody("displayName", "displayName")},
		opSpec{Use: "delete THREAD", Short: "Delete a thread", Method: http.MethodDelete, Path: "/ai-agent/{project}/threads/{thread}",
			Args: []argSpec{{Name: "THREAD", Param: "thread"}}, Confirm: "Delete thread {0}?", Empty: "Deleted."},
	)
	cmd.AddCommand(newAIChatCmd(), newAIWorkflowCmd())
	return cmd
}

func newAPIKeysCmd() *cobra.Command {
	return group("api-keys", "API keys of the project", []string{"api-key", "keys"},
		opSpec{Use: "list", Short: "List the project's API keys (secrets are never returned)", Method: http.MethodGet, Path: "/projects/{project}/apiKeys",
			Fields: []string{"apiKeyId", "name", "expiresAt"}},
		opSpec{Use: "create", Short: "Create an API key; the secret is shown once", Method: http.MethodPost, Path: "/projects/{project}/apiKeys", Body: true,
			Flags: []flagSpec{{Name: "name", Help: "key name", Required: true}, {Name: "expires-at", Key: "expiresAt", Help: "ISO-8601 expiry, e.g. 2027-01-01T00:00:00Z"}}},
		opSpec{Use: "delete ID", Short: "Delete an API key", Method: http.MethodDelete, Path: "/projects/{project}/apiKeys/{key}",
			Args: []argSpec{{Name: "ID", Param: "key"}}, Confirm: "Delete API key {0}?", Empty: "Deleted."},
	)
}

func newDriversCmd() *cobra.Command {
	return group("drivers", "Driver downloads and connection strings for BI tools", []string{"driver"},
		opSpec{Use: "list", Short: "List supported drivers", Method: http.MethodGet, Path: "/supportedDrivers", Query: true,
			Flags: []flagSpec{{Name: "catalog", In: "query", Key: "catalogName", Help: "catalog name"}}},
		opSpec{Use: "jdbc", Short: "The JDBC driver", Method: http.MethodGet, Path: "/supportedDrivers/jdbc"},
		opSpec{Use: "sqlalchemy", Short: "The SQLAlchemy driver", Method: http.MethodGet, Path: "/supportedDrivers/sql_alchemy",
			Flags: []flagSpec{{Name: "catalog", In: "query", Key: "catalogName", Help: "catalog name"}}},
	)
}

func newEmbedCmd() *cobra.Command {
	return group("embed", "Embedded Peaka (partner keys): sessions that open the studio inside your product", nil,
		opSpec{Use: "session", Short: "Open an embedded session for the project in context", Method: http.MethodPost, Path: "/ui/initSession", Body: true,
			Flags: []flagSpec{
				{Name: "theme", Help: "theme name"},
				{Name: "theme-override", Kind: "bool", Key: "themeOverride", Help: "let the theme override the user's"},
				{Name: "mode", Key: "sessionMode", Help: "session mode"},
				{Name: "timeout", Kind: "int", Key: "timeoutInSeconds", Help: "session timeout in seconds"},
			},
			Prepare: func(c *callCtx) error {
				p, err := rt.needProject()
				if err != nil {
					return err
				}
				c.Body["projectId"] = p
				return nil
			}},
	)
}

func newParametricTablesCmd() *cobra.Command {
	return group("parametric-tables", "Parametric tables: a dynamic table materialized with fixed parameter values", []string{"parametric-table"},
		opSpec{Use: "list CATALOG", Short: "List parametric tables of a catalog", Method: http.MethodGet, Path: "/data/projects/{project}/catalogs/{catalog}/parametricTables",
			Args: []argSpec{{Name: "CATALOG", Resolve: resolveOneCatalog}}},
		opSpec{Use: "create CATALOG", Short: "Create a parametric table (body from --from-file or -f fields)", Method: http.MethodPost, Path: "/data/projects/{project}/catalogs/{catalog}/parametricTables", Body: true,
			Long: `  peaka parametric-tables create hubspot -f fromSchema=crm -f fromTable=deals_by_stage \
      -f toSchema=crm -f toTable=deals_won -f paramValues:='[{"key":"stage","value":"won"}]'`,
			Args: []argSpec{{Name: "CATALOG", Resolve: resolveOneCatalog}}},
		opSpec{Use: "delete CATALOG SCHEMA.TABLE", Short: "Delete a parametric table", Method: http.MethodDelete, Path: "/data/projects/{project}/catalogs/{catalog}/parametricTables",
			Args: []argSpec{{Name: "CATALOG", Resolve: resolveOneCatalog}, {Name: "TABLE", Resolve: func(r *Runtime, v string, p map[string]string) error {
				parts := splitPath(v)
				if len(parts) != 2 {
					return usage("expected schema.table, got %q", v)
				}
				p["toSchema"], p["toTable"] = parts[0], parts[1]
				return nil
			}}},
			Prepare: paramsIntoQuery("toSchema", "toSchema", "toTable", "toTable"),
			Confirm: "Delete parametric table {1}?", Empty: "Deleted."},
	)
}
