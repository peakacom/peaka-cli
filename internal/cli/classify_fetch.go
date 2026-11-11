package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/classify"
	"github.com/peakacom/peaka-cli/internal/sqltext"
)

// tableRef is one table of the target, with the catalog id the API wants and the name SQL wants.
type tableRef struct {
	CatalogID, Catalog, Schema, Table string
}

func (t tableRef) String() string { return t.Catalog + "." + t.Schema + "." + t.Table }

// enumerateTarget lists the tables of catalog, catalog.schema or catalog.schema.table. Peaka's internal
// catalog resolves to several ids; all of them are walked.
func (r *Runtime) enumerateTarget(target string) ([]tableRef, error) {
	project, err := r.needProject()
	if err != nil {
		return nil, err
	}
	p := map[string]string{}
	parts := splitPath(target)
	switch len(parts) {
	case 1:
		if err := resolveCatalog(r, parts[0], p); err != nil {
			return nil, err
		}
	case 2:
		if err := resolveSchema(r, target, p); err != nil {
			return nil, err
		}
	case 3:
		if err := resolveTable(r, target, p); err != nil {
			return nil, err
		}
		return []tableRef{{CatalogID: p["catalog"], Catalog: p["catalogName"], Schema: p["schema"], Table: p["table"]}}, nil
	default:
		return nil, usage("expected catalog, catalog.schema or catalog.schema.table, got %q", target)
	}
	var refs []tableRef
	seen := map[string]bool{}
	for _, id := range catalogIDs(p) {
		var schemas []string
		if p["schema"] != "" {
			schemas = []string{p["schema"]}
		} else {
			ss, err := r.listRecords(fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas", project, url.PathEscape(id)), nil, "")
			if err != nil {
				return nil, err
			}
			for _, s := range ss {
				if n := firstNonEmpty(str(s["schemaName"]), str(s["name"])); n != "" {
					schemas = append(schemas, n)
				}
			}
		}
		for _, schema := range schemas {
			ts, err := r.listRecords(fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas/%s/tables", project, url.PathEscape(id), url.PathEscape(schema)), nil, "")
			if err != nil {
				if p["schema"] != "" && len(catalogIDs(p)) > 1 {
					continue // the schema lives under the other id of the same name
				}
				return nil, err
			}
			for _, t := range ts {
				n := firstNonEmpty(str(t["tableName"]), str(t["name"]), str(t["table"]))
				key := schema + "." + n
				if n == "" || seen[key] {
					continue
				}
				seen[key] = true
				refs = append(refs, tableRef{CatalogID: id, Catalog: firstNonEmpty(str(t["catalogName"]), p["catalogName"]), Schema: schema, Table: n})
			}
		}
	}
	if len(refs) == 0 {
		return nil, api.Exit(api.ExitNotFound, "no tables in %s", target)
	}
	return refs, nil
}

// fetchTableInput reads the columns of a table and, when asked, its statistics. A statistics failure is
// returned apart, since the classification can go on without them.
func (r *Runtime) fetchTableInput(ref tableRef, withStats bool) (classify.TableInput, *classify.TableError, error) {
	project, err := r.needProject()
	if err != nil {
		return classify.TableInput{}, nil, err
	}
	base := fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas/%s/tables/%s", project, url.PathEscape(ref.CatalogID), url.PathEscape(ref.Schema), url.PathEscape(ref.Table))
	in := classify.TableInput{CatalogID: ref.CatalogID, Catalog: ref.Catalog, Schema: ref.Schema, Table: ref.Table}
	cols, err := r.listRecords(base+"/columns", nil, "")
	if err != nil {
		return in, nil, err
	}
	for _, c := range cols {
		n := firstNonEmpty(str(c["columnName"]), str(c["name"]), str(c["column"]))
		if n == "" {
			continue
		}
		in.Columns = append(in.Columns, classify.ColumnInput{
			Name:        n,
			Type:        firstNonEmpty(str(c["dataType"]), str(c["type"]), str(c["columnType"])),
			Description: firstNonEmpty(str(c["desc"]), str(c["description"]), str(c["columnDescription"])),
			Catalog:     ref.Catalog,
			PrimaryKey:  c["isPrimary"] == true,
			System:      c["isSystem"] == true, // what counts as housekeeping beyond that is the template's shortcuts
		})
	}
	if !withStats {
		return in, nil, nil
	}
	client, err := r.api()
	if err != nil {
		return in, nil, err
	}
	var stats struct {
		Columns []struct {
			Name     string   `json:"columnName"`
			Distinct *float64 `json:"distinctFraction"`
			Nulls    *float64 `json:"nullsFraction"`
		} `json:"columnStatistics"`
	}
	if err := client.Get(r.Ctx, base+"/statistics", nil, &stats); err != nil {
		return in, &classify.TableError{Catalog: ref.Catalog, Schema: ref.Schema, Table: ref.Table, Stage: "stats", Detail: err.Error()}, nil
	}
	for i := range in.Columns {
		for _, s := range stats.Columns {
			if strings.EqualFold(s.Name, in.Columns[i].Name) {
				in.Columns[i].DistinctFraction, in.Columns[i].NullsFraction = s.Distinct, s.Nulls
			}
		}
	}
	return in, nil, nil
}

// sampleTableAPI reads up to n rows of every column through the REST API's query execution, which any
// identity the API accepts can use, and attaches the values to the columns.
func (r *Runtime) sampleTableAPI(ref tableRef, in *classify.TableInput, n int) error {
	if len(in.Columns) == 0 {
		return nil
	}
	project, err := r.needProject()
	if err != nil {
		return err
	}
	client, err := r.api()
	if err != nil {
		return err
	}
	names := make([]string, len(in.Columns))
	for i, c := range in.Columns {
		names[i] = c.Name
	}
	body := map[string]any{
		"columns": names,
		"from":    []map[string]string{{"catalogId": ref.CatalogID, "schemaName": ref.Schema, "tableName": ref.Table}},
		"limit":   n,
	}
	var res struct {
		Columns []struct {
			Name string `json:"columnName"`
		} `json:"columns"`
		Data []json.RawMessage `json:"data"`
	}
	if _, err := client.Do(r.Ctx, http.MethodPost, fmt.Sprintf("/data/projects/%s/queries/execute", url.PathEscape(project)), url.Values{"format": {"COMPACT"}}, body, &res); err != nil {
		return err
	}
	index := map[string]int{}
	for i, c := range in.Columns {
		index[strings.ToLower(c.Name)] = i
	}
	for _, raw := range res.Data {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		var row []any
		if err := dec.Decode(&row); err != nil {
			return fmt.Errorf("decoding a result row: %w", err)
		}
		for j, cell := range row {
			if j >= len(res.Columns) {
				break
			}
			i, ok := index[strings.ToLower(res.Columns[j].Name)]
			if !ok {
				continue
			}
			if v, ok := cellString(cell); ok {
				in.Columns[i].Values = append(in.Columns[i].Values, v)
			} else {
				in.Columns[i].Nulls++
			}
		}
	}
	return nil
}

// sampleTable reads up to n rows of every column with one SQL query and attaches the values to the columns.
func sampleTable(ctx context.Context, s *sqlSession, ref tableRef, in *classify.TableInput, n int) error {
	if len(in.Columns) == 0 {
		return nil
	}
	names := make([]string, len(in.Columns))
	for i, c := range in.Columns {
		names[i] = sqltext.QuoteIdent(c.Name, true)
	}
	stmt := fmt.Sprintf("SELECT %s FROM %s.%s.%s LIMIT %d", strings.Join(names, ", "),
		sqltext.QuoteIdent(ref.Catalog, true), sqltext.QuoteIdent(ref.Schema, true), sqltext.QuoteIdent(ref.Table, true), n)
	q, err := s.client.Execute(ctx, stmt)
	if err != nil {
		return s.wrap(err, stmt)
	}
	defer q.Cancel()
	for {
		rows, done, err := q.Next(ctx)
		if err != nil {
			return s.wrap(err, stmt)
		}
		for _, row := range rows {
			for i := range in.Columns {
				if i >= len(row) {
					break
				}
				if v, ok := cellString(row[i]); ok {
					in.Columns[i].Values = append(in.Columns[i].Values, v)
				} else {
					in.Columns[i].Nulls++
				}
			}
		}
		if done {
			return nil
		}
	}
}

// cellString renders one cell for the rules and shapes; nil is a null.
func cellString(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return fmt.Sprint(x), true
	case float64, int64, int:
		return fmt.Sprint(x), true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v), true
	}
	return string(b), true
}
