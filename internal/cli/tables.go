package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
)

// tables covers two things that share a name: the tables of any catalog,
// addressed as catalog.schema.table, and Peaka's own internal tables
// (schema peaka.table, or BI tables), which can be created, filled and
// altered through the API and are addressed by their bare name.
func newTablesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tables",
		Aliases: []string{"table"},
		Short:   "Tables of a catalog (catalog.schema.table), and Peaka's own internal tables (NAME)",
	}
	var bi bool
	list := &cobra.Command{
		Use:               "list [CATALOG.SCHEMA]",
		ValidArgsFunction: argCompleter(completeDotted(2)),
		Short:             "List the tables of a schema; without an argument, the project's internal Peaka tables",
		Args:              cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return rt.call(http.MethodGet, internalPath(project, bi, ""), nil, nil, "", []string{"catalogName", "schemaName", "tableName", "isCached", "isCacheable"}, "")
			}
			p := map[string]string{}
			if err := resolveSchema(rt, args[0], p); err != nil {
				return err
			}
			return rt.call(http.MethodGet, fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas/%s/tables", project, p["catalog"], p["schema"]), nil, nil, "",
				[]string{"catalogName", "schemaName", "tableName", "isCacheable", "isCached", "isDynamicTable"}, "")
		},
	}
	list.Flags().BoolVar(&bi, "bi", false, "internal BI tables instead of Peaka tables")

	describe := &cobra.Command{
		Use:               "describe TABLE",
		ValidArgsFunction: argCompleter(completeDotted(3)),
		Aliases:           []string{"get", "columns"},
		Short:             "Columns of a table: catalog.schema.table, or the NAME of an internal table",
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			parts := splitPath(args[0])
			if len(parts) == 1 {
				return rt.call(http.MethodGet, internalPath(project, bi, parts[0])+"/columns", nil, nil, "", []string{"name", "dataType", "displayName", "isNotNull", "isUnique", "defaultValue"}, "")
			}
			p := map[string]string{}
			if err := resolveTable(rt, args[0], p); err != nil {
				return err
			}
			return rt.call(http.MethodGet, fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas/%s/tables/%s/columns", project, p["catalog"], p["schema"], p["table"]), nil, nil, "", nil, "")
		},
	}
	describe.Flags().BoolVar(&bi, "bi", false, "the NAME is a BI table")

	stats := opSpec{Use: "stats TABLE", Short: "Row count and column statistics of catalog.schema.table", Method: http.MethodGet,
		Path: "/data/projects/{project}/catalogs/{catalog}/schemas/{schema}/tables/{table}/statistics", Args: []argSpec{{Name: "TABLE", Resolve: resolveTable}}}
	cached := opSpec{Use: "cached TABLE", Short: "Whether catalog.schema.table is cached, and which cache types it supports", Method: http.MethodGet,
		Path: "/data/projects/{project}/catalogs/{catalog}/schemas/{schema}/tables/{table}/isCached", Args: []argSpec{{Name: "TABLE", Resolve: resolveTable}}}

	ef := &exportFlags{}
	export := &cobra.Command{
		Use:               "export TABLE",
		ValidArgsFunction: argCompleter(completeDotted(3)),
		Short:             "Export catalog.schema.table to files (CSV or JSONL)",
		Long: `  peaka tables export stripe.payment.charges --format-out PARQUET --wait
  peaka tables export stripe.payment.charges --limit 1000 --out ./exports`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := map[string]string{}
			if err := resolveTable(rt, args[0], p); err != nil {
				return err
			}
			project, _ := rt.needProject()
			return rt.startExport(fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas/%s/tables/%s/exports", project, p["catalog"], p["schema"], p["table"]), ef)
		},
	}
	ef.bind(export)

	create := &cobra.Command{
		Use:   "create NAME",
		Short: "Create an internal Peaka table (add columns with `tables add-column`)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			return rt.call(http.MethodPost, internalPath(project, bi, args[0]), nil, nil, "", nil, "Created table "+args[0]+".")
		},
	}
	create.Flags().BoolVar(&bi, "bi", false, "create a BI table")

	del := &cobra.Command{
		Use:               "delete NAME",
		ValidArgsFunction: argCompleter(completeInternalTables),
		Short:             "Delete an internal Peaka table",
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			if err := rt.confirm(fmt.Sprintf("Delete table %s and its rows?", args[0])); err != nil {
				return err
			}
			return rt.call(http.MethodDelete, internalPath(project, bi, args[0]), nil, nil, "", nil, "Deleted.")
		},
	}
	del.Flags().BoolVar(&bi, "bi", false, "the NAME is a BI table")

	colFlags := func(c *cobra.Command, b *columnBody) {
		c.Flags().StringVar(&b.dataType, "type", "", "data type: varchar (text), bigint (int), decimal (double), boolean, date, time, timestamp, uuid, json")
		c.Flags().StringVar(&b.displayName, "display-name", "", "display name (default: the name)")
		c.Flags().StringVar(&b.defaultValue, "default", "", "default value")
		c.Flags().BoolVar(&b.notNull, "not-null", false, "NOT NULL")
		c.Flags().BoolVar(&b.unique, "unique", false, "UNIQUE")
		c.Flags().BoolVar(&bi, "bi", false, "the table is a BI table")
	}
	var addBody columnBody
	addCol := &cobra.Command{
		Use:               "add-column TABLE COLUMN",
		ValidArgsFunction: argCompleter(completeInternalTables),
		Short:             "Add a column to an internal table",
		Long:              "  peaka tables add-column customers email --type text --not-null\n  peaka tables add-column orders amount --type decimal --default 0",
		Args:              cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			if addBody.dataType == "" {
				return api.Exit(api.ExitInvalid, "--type is required")
			}
			// the endpoint takes a list of columns
			return rt.call(http.MethodPost, internalPath(project, bi, args[0])+"/columns", nil, []map[string]any{addBody.json(args[1])}, "", nil, "Added column "+args[1]+".")
		},
	}
	colFlags(addCol, &addBody)

	var updBody columnBody
	updCol := &cobra.Command{
		Use:               "update-column TABLE COLUMN",
		ValidArgsFunction: argCompleter(completeInternalTables),
		Short:             "Change a column of an internal table (type, display name, default, constraints)",
		Args:              cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			// unset flags keep the current values
			var cols []record
			client, err := rt.api()
			if err != nil {
				return err
			}
			if err := client.Get(rt.Ctx, internalPath(project, bi, args[0])+"/columns", nil, &cols); err != nil {
				return err
			}
			cur, err := matchOne(cols, args[1], "column")
			if err != nil {
				return err
			}
			body := updBody.json(args[1])
			for k, v := range map[string]string{"type": "dataType", "display-name": "displayName", "default": "defaultValue", "not-null": "isNotNull", "unique": "isUnique"} {
				if !cmd.Flags().Changed(k) {
					if cv, ok := cur[v]; ok && cv != nil {
						body[v] = cv
					} else {
						delete(body, v)
					}
				}
			}
			return rt.call(http.MethodPut, internalPath(project, bi, args[0])+"/columns/"+args[1], nil, body, "", nil, "Updated column "+args[1]+".")
		},
	}
	colFlags(updCol, &updBody)

	dropCol := &cobra.Command{
		Use:               "drop-column TABLE COLUMN",
		ValidArgsFunction: argCompleter(completeInternalTables),
		Short:             "Remove a column from an internal table",
		Args:              cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			if err := rt.confirm(fmt.Sprintf("Drop column %s of %s?", args[1], args[0])); err != nil {
				return err
			}
			return rt.call(http.MethodDelete, internalPath(project, bi, args[0])+"/columns/"+args[1], nil, nil, "", nil, "Dropped.")
		},
	}
	dropCol.Flags().BoolVar(&bi, "bi", false, "the table is a BI table")

	cmd.AddCommand(list, describe, stats.command(), cached.command(), export, create, del, addCol, updCol, dropCol, newTablesImportCmd(), newTablesSampleCmd())
	return cmd
}

func internalPath(project string, bi bool, table string) string {
	kind := "table"
	if bi {
		kind = "bitable"
	}
	p := fmt.Sprintf("/data/projects/%s/%s", project, kind)
	if table != "" {
		p += "/" + table
	}
	return p
}

type columnBody struct {
	dataType, displayName, defaultValue string
	notNull, unique                     bool
}

// columnType maps everyday type names onto the ones the API accepts
// (VARCHAR, BIGINT, BOOLEAN, DECIMAL, TIMESTAMP, TIME, DATE, UUID, JSON).
func columnType(t string) string {
	u := strings.ToUpper(strings.TrimSpace(t))
	switch u {
	case "TEXT", "STRING", "STR", "CHAR", "VARCHAR":
		return "VARCHAR"
	case "INT", "INTEGER", "LONG", "BIGINT", "NUMBER", "SMALLINT", "TINYINT":
		return "BIGINT"
	case "BOOL", "BOOLEAN":
		return "BOOLEAN"
	case "FLOAT", "DOUBLE", "REAL", "NUMERIC", "DECIMAL":
		return "DECIMAL"
	case "DATETIME", "TIMESTAMP":
		return "TIMESTAMP"
	}
	return u
}

func (b columnBody) json(name string) map[string]any {
	m := map[string]any{"name": name, "dataType": columnType(b.dataType), "displayName": firstNonEmpty(b.displayName, name), "isNotNull": b.notNull, "isUnique": b.unique}
	if b.defaultValue != "" {
		m["defaultValue"] = b.defaultValue
	}
	return m
}

func newTablesImportCmd() *cobra.Command {
	var file string
	var noHeader bool
	var mappings []string
	cmd := &cobra.Command{
		Use:               "import TABLE",
		ValidArgsFunction: argCompleter(completeInternalTables),
		Short:             "Import a CSV file into an internal Peaka table",
		Long: `Columns are matched by header name unless --map says otherwise.

  peaka tables import customers --file customers.csv
  peaka tables import customers --file raw.csv --no-header --map id=0 --map name=1
  peaka tables import customers --file export.csv --map email=E-Mail`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			if file == "" {
				return api.Exit(api.ExitInvalid, "--file is required")
			}
			data, err := readInput(file)
			if err != nil {
				return usage("--file: %v", err)
			}
			client, err := rt.api()
			if err != nil {
				return err
			}
			var cols []record
			if err := client.Get(rt.Ctx, internalPath(project, false, args[0])+"/columns", nil, &cols); err != nil {
				return err
			}
			explicit := map[string]string{}
			for _, m := range mappings {
				k, v, ok := strings.Cut(m, "=")
				if !ok {
					return usage("--map %q: expected COLUMN=CSV_HEADER or COLUMN=INDEX", m)
				}
				explicit[k] = v
			}
			// with a header, unmapped table columns are matched to CSV columns of the same name
			header := map[string]bool{}
			if !noHeader {
				first := strings.SplitN(string(data), "\n", 2)[0]
				for _, h := range strings.Split(strings.TrimRight(first, "\r"), ",") {
					header[strings.ToLower(strings.Trim(strings.TrimSpace(h), `"`))] = true
				}
			}
			var maps []map[string]any
			for _, c := range cols {
				name := str(c["name"])
				if strings.HasPrefix(name, "_") && explicit[name] == "" {
					continue // system columns are filled by Peaka
				}
				src, ok := explicit[name]
				if !ok {
					if noHeader || !header[strings.ToLower(name)] {
						continue
					}
					src = name
				}
				m := map[string]any{"name": name}
				if n, err := fmt.Sscanf(src, "%d", new(int)); err == nil && n == 1 && noHeader {
					var idx int
					fmt.Sscanf(src, "%d", &idx)
					m["csvColumnIndex"] = idx
				} else {
					m["csvColumnName"] = src
				}
				maps = append(maps, m)
			}
			if len(maps) == 0 {
				return api.Exit(api.ExitInvalid, "no CSV column matches a column of %s; pass --map COLUMN=CSV_HEADER", args[0])
			}
			reqJSON, _ := json.Marshal(map[string]any{"mappings": maps, "containsHeader": !noHeader})
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			fw, _ := mw.CreateFormFile("file", filepath.Base(firstNonEmpty(strings.TrimSuffix(file, "-"), "data.csv")))
			fw.Write(data)
			mw.WriteField("request", string(reqJSON))
			mw.Close()
			return rt.call(http.MethodPost, internalPath(project, false, args[0])+"/import", nil, &api.RawBody{ContentType: mw.FormDataContentType(), Data: buf.Bytes()}, "", nil, "Imported.")
		},
	}
	cmd.Flags().StringVarP(&file, "file", "F", "", "CSV file (- for stdin)")
	cmd.Flags().BoolVar(&noHeader, "no-header", false, "the CSV has no header row; map columns by index")
	cmd.Flags().StringArrayVar(&mappings, "map", nil, "COLUMN=CSV_HEADER or COLUMN=INDEX; repeatable")
	return cmd
}

func newTablesSampleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "sample TABLE",
		ValidArgsFunction: argCompleter(completeDotted(3)),
		Short:             "A sample CSV in the shape `tables import` expects",
		Args:              cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := rt.needProject()
			if err != nil {
				return err
			}
			client, err := rt.api()
			if err != nil {
				return err
			}
			var raw []byte
			if _, err := client.Do(rt.Ctx, http.MethodGet, internalPath(project, false, args[0])+"/sample", nil, nil, &raw); err != nil {
				return err
			}
			os.Stdout.Write(raw)
			if !bytes.HasSuffix(raw, []byte("\n")) {
				fmt.Fprintln(rt.Out)
			}
			return nil
		},
	}
	return cmd
}
