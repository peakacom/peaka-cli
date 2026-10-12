package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/api"
)

// opSpec describes one REST operation as a command: how the arguments and
// flags become path parameters, query parameters and the JSON body, and how
// the answer is shown. Most of the command tree is built from these.
type opSpec struct {
	Use     string
	Short   string
	Long    string
	Aliases []string
	Hidden  bool

	Method string
	Path   string // template with {project}, {org} and {<param>} placeholders

	Args  []argSpec
	Flags []flagSpec
	Body  bool // accept --from-file (JSON object) and -f KEY=VALUE fields into the body
	Query bool // for GET/DELETE: accept -f KEY=VALUE as query parameters

	// Prepare runs after arguments and flags are parsed and before the
	// request: it may copy resolved parameters into the body, change the
	// path, or validate.
	Prepare func(c *callCtx) error

	Unwrap  string   // response key that holds the list (e.g. "tree", "items")
	Fields  []string // default table columns
	Confirm string   // asked before running; {0} {1} are the arguments
	Empty   string   // note printed when the response has no body ("Deleted.")
}

// callCtx is the request being assembled.
type callCtx struct {
	Args   []string
	Params map[string]string // path parameters, resolved
	Query  url.Values
	Body   map[string]any
	Path   string // template; Prepare may replace it
}

// argSpec is one positional argument. Resolve turns the value into path
// parameters (an id lookup, a dotted table path split); without it the
// argument sets the parameter named Param.
type argSpec struct {
	Name     string
	Help     string
	Optional bool
	Param    string
	Resolve  resolver
}

type resolver func(r *Runtime, value string, params map[string]string) error

// flagSpec is one flag. In says where it goes: "body" (default), "query" or "path".
type flagSpec struct {
	Name     string
	Short    string
	Help     string
	Kind     string // string (default), int, bool, strings, json, file (contents of a file, - for stdin)
	In       string // body (default), query, path
	Key      string // parameter/field name (default: Name in camelCase)
	Required bool
	Default  any
	// Set replaces the default placement: it receives the parsed value and the body.
	Set func(v any, body map[string]any)
	// Resolve maps the value to path parameters (like argSpec.Resolve); nothing goes to the body.
	Resolve resolver
}

type flagValue struct {
	spec flagSpec
	s    string
	i    int
	b    bool
	ss   []string
}

// command builds the cobra command for a spec.
func (o opSpec) command() *cobra.Command {
	min, max := 0, 0
	for _, a := range o.Args {
		if !a.Optional {
			min++
		}
		max++
	}
	values := make([]*flagValue, len(o.Flags))
	var fromFile string
	var fields []string
	cmd := &cobra.Command{
		Use:     o.Use,
		Short:   o.Short,
		Long:    o.Long,
		Aliases: o.Aliases,
		Hidden:  o.Hidden,
		Args:    cobra.RangeArgs(min, max),
		RunE: func(cmd *cobra.Command, args []string) error {
			return o.run(cmd, args, values, fromFile, fields)
		},
	}
	if strings.HasSuffix(o.Use, "...") {
		cmd.Args = cobra.MinimumNArgs(min)
	}
	for i, f := range o.Flags {
		v := &flagValue{spec: f}
		values[i] = v
		switch f.Kind {
		case "int":
			d, _ := f.Default.(int)
			cmd.Flags().IntVarP(&v.i, f.Name, f.Short, d, f.Help)
		case "bool":
			d, _ := f.Default.(bool)
			cmd.Flags().BoolVarP(&v.b, f.Name, f.Short, d, f.Help)
		case "strings":
			cmd.Flags().StringSliceVarP(&v.ss, f.Name, f.Short, nil, f.Help)
		default:
			d, _ := f.Default.(string)
			cmd.Flags().StringVarP(&v.s, f.Name, f.Short, d, f.Help)
		}
	}
	if o.Body {
		cmd.Flags().StringVar(&fromFile, "from-file", "", "JSON file with the request body (- for stdin); flags override its fields")
		cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "body field KEY=VALUE (string) or KEY:=JSON; repeatable")
	} else if o.Query {
		cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "extra query parameter KEY=VALUE; repeatable")
	}
	attachCompletions(cmd, o)
	return cmd
}

func (o opSpec) run(cmd *cobra.Command, args []string, values []*flagValue, fromFile string, fields []string) error {
	c := &callCtx{Args: args, Params: map[string]string{}, Query: url.Values{}, Body: map[string]any{}, Path: o.Path}
	if strings.Contains(o.Path, "{project}") {
		p, err := rt.needProject()
		if err != nil {
			return err
		}
		c.Params["project"] = p
	}
	if strings.Contains(o.Path, "{org}") {
		id, err := rt.orgID()
		if err != nil {
			return err
		}
		c.Params["org"] = id
	}
	for i, a := range o.Args {
		if i >= len(args) {
			break
		}
		if a.Resolve != nil {
			if err := a.Resolve(rt, args[i], c.Params); err != nil {
				return err
			}
			continue
		}
		c.Params[a.Param] = args[i]
	}

	if fromFile != "" {
		b, err := readInput(fromFile)
		if err != nil {
			return usage("--from-file: %v", err)
		}
		if err := decodeJSON(b, &c.Body); err != nil {
			return usage("--from-file: not a JSON object: %v", err)
		}
	}
	for _, v := range values {
		f := v.spec
		changed := cmd.Flags().Changed(f.Name)
		if !changed && f.Required {
			return api.Exit(api.ExitInvalid, "--%s is required", f.Name)
		}
		if !changed && f.Default == nil {
			continue
		}
		var val any
		switch f.Kind {
		case "int":
			val = v.i
		case "bool":
			val = v.b
		case "strings":
			val = v.ss
		case "json":
			var j any
			if err := json.Unmarshal([]byte(v.s), &j); err != nil {
				return usage("--%s: not JSON: %v", f.Name, err)
			}
			val = j
		case "file":
			b, err := readInput(v.s)
			if err != nil {
				return usage("--%s: %v", f.Name, err)
			}
			val = string(b)
		default:
			val = v.s
		}
		if f.Resolve != nil {
			if err := f.Resolve(rt, v.s, c.Params); err != nil {
				return err
			}
			continue
		}
		if f.Set != nil {
			f.Set(val, c.Body)
			continue
		}
		key := f.Key
		if key == "" {
			key = camel(f.Name)
		}
		switch f.In {
		case "query":
			switch x := val.(type) {
			case []string:
				for _, s := range x {
					c.Query.Add(key, s)
				}
			default:
				c.Query.Set(key, fmt.Sprint(val))
			}
		case "path":
			c.Params[key] = fmt.Sprint(val)
		default:
			c.Body[key] = val
		}
	}
	for _, f := range fields {
		k, v, _, err := parseField(f)
		if err != nil {
			return err
		}
		if o.Body {
			c.Body[k] = v
		} else {
			c.Query.Set(k, fmt.Sprint(v))
		}
	}
	if o.Prepare != nil {
		if err := o.Prepare(c); err != nil {
			return err
		}
	}

	path := c.Path
	for k, v := range c.Params {
		path = strings.ReplaceAll(path, "{"+k+"}", url.PathEscape(v))
	}
	if i := strings.Index(path, "{"); i >= 0 {
		return usage("missing value for %s", path[i:strings.Index(path, "}")+1])
	}
	if o.Confirm != "" {
		q := o.Confirm
		for i, a := range args {
			q = strings.ReplaceAll(q, fmt.Sprintf("{%d}", i), a)
		}
		if err := rt.confirm(q); err != nil {
			return err
		}
	}
	var payload any
	if o.Body || len(c.Body) > 0 {
		payload = c.Body
	}
	return rt.call(o.Method, path, c.Query, payload, o.Unwrap, o.Fields, o.Empty)
}

// call sends the request and prints the answer by the output contract.
func (r *Runtime) call(method, path string, query url.Values, body any, unwrap string, fields []string, empty string) error {
	client, err := r.api()
	if err != nil {
		return err
	}
	var raw []byte
	resp, err := client.Do(r.Ctx, method, path, query, body, &raw)
	if err != nil {
		return r.tolerateConflict(err)
	}
	if resp.Status == 0 { // --dry-run printed the request
		return nil
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		if empty != "" {
			r.note("%s", empty)
		} else if r.StdoutTTY {
			r.note("OK (HTTP %d)", resp.Status)
		}
		return nil
	}
	if !json.Valid(raw) {
		_, err := r.Out.Write(raw)
		return err
	}
	var v any
	if err := decodeJSON(raw, &v); err != nil {
		return err
	}
	if unwrap != "" {
		if m, ok := v.(map[string]any); ok {
			if inner, ok := m[unwrap]; ok {
				v = inner
			}
		}
	}
	return r.printWith(v, fields...)
}

// readInput reads a file, or stdin for "-".
func readInput(name string) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(rt.In)
	}
	return os.ReadFile(name)
}

func usage(format string, args ...any) error {
	return api.Exit(api.ExitUsage, format, args...)
}

// camel turns a flag name (incremental-schedule) into a JSON key (incrementalSchedule).
func camel(name string) string {
	parts := strings.Split(name, "-")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// group makes a parent command with its generated children.
func group(use, short string, aliases []string, specs ...opSpec) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, Aliases: aliases}
	for _, s := range specs {
		cmd.AddCommand(s.command())
	}
	return cmd
}

// paramsIntoBody copies resolved path parameters into body fields: pairs of (param, field).
func paramsIntoBody(pairs ...string) func(*callCtx) error {
	return func(c *callCtx) error {
		for i := 0; i+1 < len(pairs); i += 2 {
			if v, ok := c.Params[pairs[i]]; ok {
				c.Body[pairs[i+1]] = v
			}
		}
		return nil
	}
}

// paramsIntoQuery copies resolved path parameters into query parameters: pairs of (param, name).
func paramsIntoQuery(pairs ...string) func(*callCtx) error {
	return func(c *callCtx) error {
		for i := 0; i+1 < len(pairs); i += 2 {
			if v, ok := c.Params[pairs[i]]; ok {
				c.Query.Set(pairs[i+1], v)
			}
		}
		return nil
	}
}

// pathWhen switches to another path template when the parameter is present.
func pathWhen(param, path string) func(*callCtx) error {
	return func(c *callCtx) error {
		if _, ok := c.Params[param]; ok {
			c.Path = path
		}
		return nil
	}
}

func chain(fs ...func(*callCtx) error) func(*callCtx) error {
	return func(c *callCtx) error {
		for _, f := range fs {
			if err := f(c); err != nil {
				return err
			}
		}
		return nil
	}
}

// --- resolvers -------------------------------------------------------------

// listRecords GETs a list endpoint, unwrapping one key when asked.
func (r *Runtime) listRecords(path string, query url.Values, unwrap string) ([]record, error) {
	client, err := r.api()
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := client.Get(r.Ctx, path, query, &raw); err != nil {
		return nil, err
	}
	if unwrap != "" {
		var m map[string]json.RawMessage
		if err := decodeJSON(raw, &m); err == nil {
			if inner, ok := m[unwrap]; ok {
				raw = inner
			}
		}
	}
	return listOf(raw)
}

// catalogsOf lists the catalogs of the project (once per process).
func (r *Runtime) catalogsOf(project string) ([]record, error) {
	if r.catalogs != nil {
		return r.catalogs, nil
	}
	cs, err := r.listRecords(fmt.Sprintf("/data/projects/%s/catalogs", project), nil, "")
	if err != nil {
		return nil, err
	}
	r.catalogs = cs
	return cs, nil
}

// resolveCatalog maps a catalog id or name to its id. Peaka's internal
// catalog appears as several entries named "peaka" (one per schema family);
// a name that matches several sets params["catalogs"] to all ids, and the
// schema-aware resolvers pick the one that holds the schema.
func resolveCatalog(r *Runtime, ref string, params map[string]string) error {
	project, err := r.needProject()
	if err != nil {
		return err
	}
	cs, err := r.catalogsOf(project)
	if err != nil {
		return err
	}
	var hits []record
	for _, c := range cs {
		if str(c["id"]) == ref {
			hits = []record{c}
			break
		}
		if strings.EqualFold(str(c["name"]), ref) || strings.EqualFold(str(c["displayName"]), ref) {
			hits = append(hits, c)
		}
	}
	if len(hits) == 0 {
		// spelled loosely (case, dashes, underscores): the same catalog
		for _, c := range cs {
			if normalizeName(str(c["name"])) == normalizeName(ref) {
				hits = append(hits, c)
			}
		}
	}
	if len(hits) == 0 {
		if close := suggestNames(names(cs, "name"), ref, 3); len(close) > 0 {
			return api.Exit(api.ExitNotFound, "no catalog with id or name %q; did you mean %s?", ref, strings.Join(close, ", "))
		}
		return api.Exit(api.ExitNotFound, "no catalog with id or name %q", ref)
	}
	params["catalog"] = str(hits[0]["id"])
	params["catalogName"] = str(hits[0]["name"])
	var ids []string
	for _, h := range hits {
		ids = append(ids, str(h["id"]))
	}
	params["catalogs"] = strings.Join(ids, ",")
	return nil
}

// resolveOneCatalog is resolveCatalog for endpoints that take exactly one catalog id.
func resolveOneCatalog(r *Runtime, ref string, params map[string]string) error {
	if err := resolveCatalog(r, ref, params); err != nil {
		return err
	}
	return needOneCatalog(ref, params)
}

// catalogIDs returns the ids a catalog reference resolved to (one, or several for an internal name).
func catalogIDs(params map[string]string) []string {
	if params["catalogs"] == "" {
		return []string{params["catalog"]}
	}
	return strings.Split(params["catalogs"], ",")
}

// needOneCatalog fails when the reference matched several catalogs.
func needOneCatalog(ref string, params map[string]string) error {
	ids := catalogIDs(params)
	if len(ids) > 1 {
		return api.Exit(api.ExitInvalid, "%d catalogs are named %q; use an id: %s", len(ids), ref, strings.Join(ids, ", "))
	}
	return nil
}

// pickCatalogForSchema narrows an ambiguous catalog to the id whose schemas include schema.
func pickCatalogForSchema(r *Runtime, params map[string]string, schema string) error {
	ids := catalogIDs(params)
	if len(ids) == 1 {
		return nil
	}
	project, _ := r.needProject()
	for _, id := range ids {
		ss, err := r.listRecords(fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas", project, url.PathEscape(id)), nil, "")
		if err != nil {
			continue
		}
		for _, s := range ss {
			if strings.EqualFold(str(s["schemaName"]), schema) {
				params["catalog"] = id
				params["catalogs"] = id
				return nil
			}
		}
	}
	return api.Exit(api.ExitNotFound, "no schema %q in catalog %s", schema, params["catalogName"])
}

// resolveCatalogOrSchema accepts catalog or catalog.schema.
func resolveCatalogOrSchema(r *Runtime, ref string, params map[string]string) error {
	parts := splitPath(ref)
	switch len(parts) {
	case 1:
		if err := resolveCatalog(r, parts[0], params); err != nil {
			return err
		}
		return needOneCatalog(ref, params)
	case 2:
		return resolveSchema(r, ref, params)
	}
	return usage("expected catalog or catalog.schema, got %q", ref)
}

// resolveTable splits catalog.schema.table (quotes allowed) into catalog id, schema and table.
func resolveTable(r *Runtime, ref string, params map[string]string) error {
	parts := splitPath(ref)
	if len(parts) != 3 {
		return usage("expected catalog.schema.table, got %q", ref)
	}
	if err := resolveCatalog(r, parts[0], params); err != nil {
		return err
	}
	if err := pickCatalogForSchema(r, params, parts[1]); err != nil {
		return err
	}
	params["schema"], params["table"] = parts[1], parts[2]
	return nil
}

// resolveSchema splits catalog.schema.
func resolveSchema(r *Runtime, ref string, params map[string]string) error {
	parts := splitPath(ref)
	if len(parts) != 2 {
		return usage("expected catalog.schema, got %q", ref)
	}
	if err := resolveCatalog(r, parts[0], params); err != nil {
		return err
	}
	if err := pickCatalogForSchema(r, params, parts[1]); err != nil {
		return err
	}
	params["schema"] = parts[1]
	return nil
}

// resolveColumn splits catalog.schema.table.column.
func resolveColumn(r *Runtime, ref string, params map[string]string) error {
	parts := splitPath(ref)
	if len(parts) != 4 {
		return usage("expected catalog.schema.table.column, got %q", ref)
	}
	if err := resolveCatalog(r, parts[0], params); err != nil {
		return err
	}
	if err := pickCatalogForSchema(r, params, parts[1]); err != nil {
		return err
	}
	params["schema"], params["table"], params["column"] = parts[1], parts[2], parts[3]
	return nil
}

// splitPath splits a dotted path honoring double quotes: a."b.c".d → [a, b.c, d].
func splitPath(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, ch := range s {
		switch {
		case ch == '"':
			inQuote = !inQuote
		case ch == '.' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(ch)
		}
	}
	parts = append(parts, cur.String())
	return parts
}

func (r *Runtime) connectionsOf(project string) ([]record, error) {
	return r.listRecords(fmt.Sprintf("/connections/%s", project), nil, "")
}

func resolveConnection(r *Runtime, ref string, params map[string]string) error {
	project, err := r.needProject()
	if err != nil {
		return err
	}
	cs, err := r.connectionsOf(project)
	if err != nil {
		return err
	}
	c, err := matchOne(cs, ref, "connection")
	if err != nil {
		return err
	}
	params["connection"] = str(c["id"])
	params["connectionType"] = str(c["type"])
	params["connectionName"] = str(c["name"])
	return nil
}

func (r *Runtime) queriesOf(project string) ([]record, error) {
	return r.listRecords(fmt.Sprintf("/data/projects/%s/queries", project), nil, "")
}

func resolveQuery(r *Runtime, ref string, params map[string]string) error {
	project, err := r.needProject()
	if err != nil {
		return err
	}
	qs, err := r.queriesOf(project)
	if err != nil {
		return err
	}
	q, err := matchOne(qs, ref, "query")
	if err != nil {
		return err
	}
	params["query"] = str(q["id"])
	params["queryName"] = str(q["displayName"])
	return nil
}

// resolveFolder accepts a folder id or its path (/analytics/dashboards).
func resolveFolder(r *Runtime, ref string, params map[string]string) error {
	project, err := r.needProject()
	if err != nil {
		return err
	}
	fs, err := r.listRecords(fmt.Sprintf("/data/projects/%s/queries/folders", project), nil, "items")
	if err != nil {
		return err
	}
	want := "/" + strings.Trim(ref, "/")
	for _, f := range fs {
		if str(f["id"]) == ref || str(f["path"]) == want {
			params["folder"] = str(f["id"])
			return nil
		}
	}
	return api.Exit(api.ExitNotFound, "no query folder with id or path %q", ref)
}

// resolveCache accepts a cache id or the cached table's catalog.schema.table.
func resolveCache(r *Runtime, ref string, params map[string]string) error {
	project, err := r.needProject()
	if err != nil {
		return err
	}
	parts := splitPath(ref)
	if len(parts) != 3 {
		params["cache"] = ref
		return nil
	}
	if err := resolveCatalog(r, parts[0], params); err != nil {
		return err
	}
	cs, err := r.listRecords(fmt.Sprintf("/data/projects/%s/cache/status", project), nil, "")
	if err != nil {
		return err
	}
	for _, c := range cs {
		if contains(catalogIDs(params), str(c["catalogId"])) && strings.EqualFold(str(c["schemaName"]), parts[1]) && strings.EqualFold(str(c["tableName"]), parts[2]) {
			params["cache"] = str(c["id"])
			return nil
		}
	}
	return api.Exit(api.ExitNotFound, "no cache for table %s", ref)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// tolerateConflict turns a 409 into success under --if-not-exists.
func (r *Runtime) tolerateConflict(err error) error {
	var p *api.Problem
	if gf.ifNotExists && asProblem(err, &p) && p.Status == http.StatusConflict {
		r.note("Already exists (%s); continuing because of --if-not-exists.", p.Detail)
		return nil
	}
	return err
}
