package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

// The commands an agent uses to pick tables before writing SQL: the table
// list of the project, the chosen tables' metadata in a compact shape, and
// a ranked search when the list is too large. Their answers carry text meant
// to be read, so it is printed as text unless JSON was asked for.

// readableText reports whether a text answer should be printed as text: the
// output format was not chosen (or is table) and no --jq or --fields applies.
func (r *Runtime) readableText() bool {
	if gf.jq != "" || gf.fields != "" {
		return false
	}
	return r.Settings.Format == "table" || r.Settings.Source["format"] == "default"
}

// getJSON GETs a path and decodes the answer into a map; raw is the body as received.
func (r *Runtime) getJSON(path string, query url.Values) (map[string]any, []byte, error) {
	client, err := r.api()
	if err != nil {
		return nil, nil, err
	}
	var raw []byte
	if _, err := client.Do(r.Ctx, http.MethodGet, path, query, nil, &raw); err != nil {
		return nil, nil, err
	}
	var m map[string]any
	if err := decodeJSON(raw, &m); err != nil {
		return nil, nil, err
	}
	return m, raw, nil
}

func newMetadataTableListCmd() *cobra.Command {
	var catalog, schema string
	cmd := &cobra.Command{
		Use:   "table-list",
		Short: "Every table of the project on one line each: the place to start answering a data question",
		Long: `Lists the project's tables grouped under "## catalog.schema" headers, one
line per table with the start of its description, a few column names and the
tables it joins to. Printed as text; --format json gives the whole answer.

When the project is too large for one list the answer says so and shows the
schemas with their table counts: narrow with --catalog and --schema, or use
` + "`peaka metadata relevant-tables QUESTION`" + `.

  peaka metadata table-list
  peaka metadata table-list --catalog stripe --schema payment`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			q := url.Values{}
			if catalog != "" {
				p := map[string]string{}
				if err := resolveOneCatalog(rt, catalog, p); err != nil {
					return err
				}
				q.Set("catalogId", p["catalog"])
			}
			if schema != "" {
				q.Set("schemaName", schema)
			}
			m, raw, err := rt.getJSON(fmt.Sprintf("/metadata/%s/table-list", project), q)
			if err != nil || m == nil {
				return err
			}
			return printTableList(m, raw)
		},
	}
	cmd.Flags().StringVar(&catalog, "catalog", "", "only this catalog (id or name)")
	cmd.Flags().StringVar(&schema, "schema", "", "only this schema")
	_ = cmd.RegisterFlagCompletionFunc("catalog", flagCompleter(completeDotted(1)))
	return cmd
}

// printTableList prints the list as text, or the schemas when the project is too large for one.
func printTableList(m map[string]any, raw []byte) error {
	if !rt.readableText() {
		var v any
		_ = decodeJSON(raw, &v)
		return rt.print(v)
	}
	if str(m["status"]) == "too_large" {
		rt.note("Too many tables for one list: %s", str(m["reason"]))
		if next := str(m["nextAction"]); next != "" {
			rt.note("%s", next)
		}
		return rt.printWith(m["schemas"], "catalog", "schema", "tables", "catalogId")
	}
	if legend := str(m["legend"]); legend != "" {
		rt.note("%s", legend)
	}
	_, err := fmt.Fprintln(rt.Out, strings.TrimRight(str(m["list"]), "\n"))
	return err
}

func newMetadataRelevantTablesCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "relevant-tables QUESTION",
		Short: "The tables most likely to answer a question, ranked (when the table list is too large)",
		Long: `Ranks the project's tables for a question by their names, descriptions and,
when the AI index is built, by meaning. One line per table; --format json
gives the whole answer with how each table was found.

  peaka metadata relevant-tables "monthly revenue by customer segment" --limit 5`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			q := url.Values{"question": {args[0]}}
			if cmd.Flags().Changed("limit") {
				if limit < 1 || limit > 10 {
					return usage("--limit must be between 1 and 10")
				}
				q.Set("limit", fmt.Sprint(limit))
			}
			m, raw, err := rt.getJSON(fmt.Sprintf("/metadata/%s/relevant-tables", project), q)
			if err != nil || m == nil {
				return err
			}
			return printRelevantTables(m, raw)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "number of tables, 1–10 (server default when omitted)")
	return cmd
}

// printRelevantTables prints one line per candidate; the state of the search goes to stderr.
func printRelevantTables(m map[string]any, raw []byte) error {
	if !rt.readableText() {
		var v any
		_ = decodeJSON(raw, &v)
		return rt.print(v)
	}
	state := []string{"status " + str(m["status"])}
	if v := str(m["index"]); v != "" {
		state = append(state, "index "+v)
	}
	if v := str(m["coverage"]); v != "" {
		state = append(state, "coverage "+v)
	}
	rt.note("%s", strings.Join(state, ", "))
	if v := str(m["reason"]); v != "" {
		rt.note("%s", v)
	}
	if v := str(m["nextAction"]); v != "" {
		rt.note("%s", v)
	}
	tables, _ := m["tables"].([]any)
	for _, t := range tables {
		row, _ := t.(map[string]any)
		line := firstNonEmpty(str(row["line"]), str(row["table"]))
		if _, err := fmt.Fprintln(rt.Out, line); err != nil {
			return err
		}
	}
	return nil
}

// newMetadataDescriptionsCmd reads and writes catalog and schema descriptions.
func newMetadataDescriptionsCmd() *cobra.Command {
	cmd := group("descriptions", "Descriptions of catalogs and schemas, read by people and the AI agent", []string{"description"},
		opSpec{Use: "list", Short: "Catalog and schema descriptions with their versions", Method: http.MethodGet, Path: "/metadata/{project}/descriptions", Unwrap: "catalogs",
			Flags:   []flagSpec{{Name: "catalog", Help: "only this catalog (id or name)", Resolve: resolveOneCatalog}},
			Prepare: paramsIntoQuery("catalog", "catalogId"), Fields: []string{"catalog", "catalogId", "editable", "version", "description"}},
	)
	cmd.AddCommand(group("set", "Set a catalog's or schema's description (an empty one removes it)", nil,
		opSpec{Use: "catalog CATALOG DESCRIPTION", Short: "Set a catalog's description", Method: http.MethodPut, Path: "/metadata/{project}/descriptions/{catalog}", Body: true,
			Long: `Pass --version with the version ` + "`peaka metadata descriptions list`" + ` showed: if someone changed the
description since, the write is refused with CONFLICT and the current text and version.
A catalog shared into the project is described in its own project.

  peaka metadata descriptions set catalog stripe "Stripe test account, amounts in cents" --version 2
  peaka metadata descriptions set catalog stripe ""          # remove it`,
			Args:    []argSpec{{Name: "CATALOG", Resolve: resolveOneCatalog}, {Name: "DESCRIPTION", Param: "description"}},
			Flags:   []flagSpec{{Name: "version", Kind: "int", Help: "the version you read; refused if the description changed since"}},
			Prepare: paramsIntoBody("description", "description")},
		opSpec{Use: "schema CATALOG.SCHEMA DESCRIPTION", Short: "Set a schema's description", Method: http.MethodPut, Path: "/metadata/{project}/descriptions/{catalog}/{schema}", Body: true,
			Long:    `  peaka metadata descriptions set schema stripe.payment "One table per Stripe list endpoint" --version 2`,
			Args:    []argSpec{{Name: "SCHEMA", Resolve: resolveSchema}, {Name: "DESCRIPTION", Param: "description"}},
			Flags:   []flagSpec{{Name: "version", Kind: "int", Help: "the version you read; refused if the description changed since"}},
			Prepare: paramsIntoBody("description", "description")},
	))
	return cmd
}

// prepareMetadataShape checks the compact/ddl flags of `metadata describe` and
// turns --samples into include=samples.
func prepareMetadataShape(c *callCtx) error {
	format := c.Query.Get("format")
	switch format {
	case "", "compact", "ddl":
	default:
		return usage("--format-out must be compact or ddl, got %q", format)
	}
	if c.Query.Get("levels") != "" && format == "" {
		return usage("--levels needs --format-out compact or ddl")
	}
	if c.Query.Get("samples") == "true" {
		c.Query.Set("include", "samples")
	}
	c.Query.Del("samples")
	if n := len(c.Query["table"]); n > 20 {
		return usage("at most 20 --table values, got %d", n)
	}
	return nil
}

// executedTable is one entry of the `tables` field of a query execution: the SaaS tables
// the statement read and how (live, ready, preparing, unknown).
type executedTable struct {
	Table string `json:"table"`
	Cache string `json:"cache"`
}

// noteExecutedTables tells, after the rows, which tables were read live or
// from a cache still filling: their rows may stop at the source's first page.
func noteExecutedTables(tables []executedTable) {
	var live, preparing []string
	for _, t := range tables {
		switch t.Cache {
		case "live":
			live = append(live, t.Table)
		case "preparing":
			preparing = append(preparing, t.Table)
		}
	}
	if len(live) > 0 {
		rt.note("Read live from the source, so possibly only its first page of records: %s. Cache a table for complete results: peaka cache create --table %s", strings.Join(live, ", "), live[0])
	}
	if len(preparing) > 0 {
		rt.note("Cache not filled yet (first sync running), so possibly only the first page of records: %s. Check with peaka cache status %s", strings.Join(preparing, ", "), preparing[0])
	}
}
