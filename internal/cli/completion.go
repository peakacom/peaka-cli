package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/config"
)

// Shell completion of names: catalogs, schemas, tables and columns (as dotted
// paths), connections, queries, folders, caches, projects, organizations and
// connection types. Every argument or flag that resolves a name completes the
// same names, from the same lists. Each Tab is a new process, so what a list
// answered is kept on disk for a short while.

type completer func(toComplete string) ([]string, cobra.ShellCompDirective)

const (
	completionTTL     = 45 * time.Second
	completionTimeout = 4 * time.Second
)

// completerFor pairs a resolver with the completer of the names it resolves.
func completerFor(res resolver) completer {
	if res == nil {
		return nil
	}
	switch reflect.ValueOf(res).Pointer() {
	case reflect.ValueOf(resolveCatalog).Pointer(), reflect.ValueOf(resolveOneCatalog).Pointer():
		return completeDotted(1)
	case reflect.ValueOf(resolveCatalogOrSchema).Pointer(), reflect.ValueOf(resolveSchema).Pointer():
		return completeDotted(2)
	case reflect.ValueOf(resolveTable).Pointer():
		return completeDotted(3)
	case reflect.ValueOf(resolveColumn).Pointer():
		return completeDotted(4)
	case reflect.ValueOf(resolveConnection).Pointer():
		return completeConnections
	case reflect.ValueOf(resolveQuery).Pointer():
		return completeQueries
	case reflect.ValueOf(resolveFolder).Pointer():
		return completeFolders
	case reflect.ValueOf(resolveCache).Pointer():
		return completeCaches
	}
	return nil
}

// attachCompletions wires a spec's arguments and flags to their completers.
func attachCompletions(cmd *cobra.Command, o opSpec) {
	if len(o.Args) > 0 {
		cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			i := len(args)
			if i >= len(o.Args) {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			if c := completerFor(o.Args[i].Resolve); c != nil {
				return c(toComplete)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	}
	for _, f := range o.Flags {
		if c := completerFor(f.Resolve); c != nil {
			_ = cmd.RegisterFlagCompletionFunc(f.Name, flagCompleter(c))
		}
	}
}

func flagCompleter(c completer) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return c(toComplete)
	}
}

func argCompleter(c completer) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return c(toComplete)
	}
}

func staticCompleter(values ...string) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return withPrefix(values, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// completing says whether the runtime is up for a completion request; it
// shortens the request timeout so a slow network does not freeze the shell.
func completing() bool {
	if rt == nil || rt.Settings == nil {
		return false
	}
	gf.timeout = completionTimeout
	return true
}

func withPrefix(values []string, prefix string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		name := v
		if i := strings.IndexByte(v, '\t'); i >= 0 {
			name = v[:i]
		}
		if seen[name] {
			continue
		}
		if prefix == "" || strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			seen[name] = true
			out = append(out, v)
		}
	}
	return out
}

// cached answers a list from the on-disk cache when it is fresh, else from fetch.
func cached(kind string, fetch func() ([]string, error)) []string {
	key := rt.Settings.API + "|" + rt.Settings.Project + "|" + kind
	path := filepath.Join(config.Dir(), "completion-cache.json")
	type entry struct {
		At     time.Time `json:"at"`
		Values []string  `json:"values"`
	}
	store := map[string]entry{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &store)
	}
	if e, ok := store[key]; ok && time.Since(e.At) < completionTTL {
		return e.Values
	}
	values, err := fetch()
	if err != nil {
		return nil
	}
	store[key] = entry{At: time.Now(), Values: values}
	for k, e := range store {
		if time.Since(e.At) > 24*time.Hour {
			delete(store, k)
		}
	}
	if b, err := json.Marshal(store); err == nil {
		_ = os.MkdirAll(config.Dir(), 0o700)
		_ = os.WriteFile(path, b, 0o600)
	}
	return values
}

func names(rs []record, keys ...string) []string {
	var out []string
	for _, r := range rs {
		for _, k := range keys {
			if v := str(r[k]); v != "" {
				out = append(out, v)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func completeConnections(tc string) ([]string, cobra.ShellCompDirective) {
	if !completing() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return withPrefix(cached("connections", func() ([]string, error) {
		project, err := rt.needProject()
		if err != nil {
			return nil, err
		}
		cs, err := rt.connectionsOf(project)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, c := range cs {
			out = append(out, str(c["name"])+"\t"+str(c["type"]))
		}
		sort.Strings(out)
		return out, nil
	}), tc), cobra.ShellCompDirectiveNoFileComp
}

func completeQueries(tc string) ([]string, cobra.ShellCompDirective) {
	if !completing() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return withPrefix(cached("queries", func() ([]string, error) {
		project, err := rt.needProject()
		if err != nil {
			return nil, err
		}
		qs, err := rt.queriesOf(project)
		if err != nil {
			return nil, err
		}
		return names(qs, "displayName", "name"), nil
	}), tc), cobra.ShellCompDirectiveNoFileComp
}

func completeFolders(tc string) ([]string, cobra.ShellCompDirective) {
	if !completing() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return withPrefix(cached("folders", func() ([]string, error) {
		project, err := rt.needProject()
		if err != nil {
			return nil, err
		}
		fs, err := rt.listRecords(fmt.Sprintf("/data/projects/%s/queries/folders", project), nil, "items")
		if err != nil {
			return nil, err
		}
		return names(fs, "path"), nil
	}), tc), cobra.ShellCompDirectiveNoFileComp
}

func completeCaches(tc string) ([]string, cobra.ShellCompDirective) {
	if !completing() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return withPrefix(cached("caches", func() ([]string, error) {
		project, err := rt.needProject()
		if err != nil {
			return nil, err
		}
		cs, err := rt.listRecords(fmt.Sprintf("/data/projects/%s/cache/status", project), nil, "")
		if err != nil {
			return nil, err
		}
		var out []string
		for _, c := range cs {
			if n := str(c["catalogName"]); n != "" {
				out = append(out, n+"."+str(c["schemaName"])+"."+str(c["tableName"]))
			}
		}
		sort.Strings(out)
		return out, nil
	}), tc), cobra.ShellCompDirectiveNoFileComp
}

func completeConnectionTypes(tc string) ([]string, cobra.ShellCompDirective) {
	if !completing() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return withPrefix(cached("connection-types", func() ([]string, error) {
		ts, err := rt.connectionTypes()
		if err != nil {
			return nil, err
		}
		var out []string
		for _, t := range ts {
			out = append(out, str(t["connectionType"])+"\t"+str(t["connectionTypeLabel"]))
		}
		sort.Strings(out)
		return out, nil
	}), tc), cobra.ShellCompDirectiveNoFileComp
}

func completeProjects(tc string) ([]string, cobra.ShellCompDirective) {
	if !completing() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return withPrefix(cached("projects", func() ([]string, error) {
		org, err := rt.orgID()
		if err != nil {
			return nil, err
		}
		ps, err := rt.listProjects(org, "")
		if err != nil {
			return nil, err
		}
		var out []string
		for _, p := range ps {
			out = append(out, str(p["name"])+"\t"+str(p["id"]))
		}
		sort.Strings(out)
		return out, nil
	}), tc), cobra.ShellCompDirectiveNoFileComp
}

func completeOrgs(tc string) ([]string, cobra.ShellCompDirective) {
	if !completing() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return withPrefix(cached("orgs", func() ([]string, error) {
		os, err := rt.listOrgs()
		if err != nil {
			return nil, err
		}
		return names(os, "name"), nil
	}), tc), cobra.ShellCompDirectiveNoFileComp
}

// completeDotted completes catalog.schema.table.column paths up to `levels`
// parts: the names of the next part, after what is typed so far. A part that
// may be followed by a dot is offered without a trailing space.
func completeDotted(levels int) completer {
	return func(tc string) ([]string, cobra.ShellCompDirective) {
		if !completing() {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		parts := strings.Split(tc, ".")
		level := len(parts) - 1
		if level >= levels {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		head := strings.Join(parts[:level], ".")
		var values []string
		switch level {
		case 0:
			values = cached("catalogs", func() ([]string, error) {
				project, err := rt.needProject()
				if err != nil {
					return nil, err
				}
				cs, err := rt.catalogsOf(project)
				if err != nil {
					return nil, err
				}
				return names(cs, "name"), nil
			})
		case 1:
			values = cached("schemas|"+head, func() ([]string, error) {
				project, err := rt.needProject()
				if err != nil {
					return nil, err
				}
				p := map[string]string{}
				if err := resolveCatalog(rt, parts[0], p); err != nil {
					return nil, err
				}
				var all []record
				for _, id := range catalogIDs(p) {
					ss, err := rt.listRecords(fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas", project, url.PathEscape(id)), nil, "")
					if err != nil {
						return nil, err
					}
					all = append(all, ss...)
				}
				return names(all, "schemaName", "name"), nil
			})
		case 2:
			values = cached("tables|"+head, func() ([]string, error) {
				project, err := rt.needProject()
				if err != nil {
					return nil, err
				}
				p := map[string]string{}
				if err := resolveSchema(rt, head, p); err != nil {
					return nil, err
				}
				ts, err := rt.listRecords(fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas/%s/tables", project, p["catalog"], url.PathEscape(p["schema"])), nil, "")
				if err != nil {
					return nil, err
				}
				return names(ts, "tableName", "name"), nil
			})
		case 3:
			values = cached("columns|"+head, func() ([]string, error) {
				project, err := rt.needProject()
				if err != nil {
					return nil, err
				}
				p := map[string]string{}
				if err := resolveTable(rt, head, p); err != nil {
					return nil, err
				}
				cs, err := rt.listRecords(fmt.Sprintf("/data/projects/%s/catalogs/%s/schemas/%s/tables/%s/columns", project, p["catalog"], url.PathEscape(p["schema"]), url.PathEscape(p["table"])), nil, "")
				if err != nil {
					return nil, err
				}
				return names(cs, "columnName", "name"), nil
			})
		}
		var out []string
		for _, v := range withPrefix(values, parts[level]) {
			if head != "" {
				v = head + "." + v
			}
			out = append(out, v)
		}
		directive := cobra.ShellCompDirectiveNoFileComp
		if level < levels-1 {
			directive |= cobra.ShellCompDirectiveNoSpace
		}
		return out, directive
	}
}

func completeInternalTables(tc string) ([]string, cobra.ShellCompDirective) {
	if !completing() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return withPrefix(cached("internal-tables", func() ([]string, error) {
		project, err := rt.needProject()
		if err != nil {
			return nil, err
		}
		ts, err := rt.listRecords(internalPath(project, false, ""), nil, "")
		if err != nil {
			return nil, err
		}
		return names(ts, "tableName", "name"), nil
	}), tc), cobra.ShellCompDirectiveNoFileComp
}

func completeWorkspaces(tc string) ([]string, cobra.ShellCompDirective) {
	if !completing() {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return withPrefix(cached("workspaces", func() ([]string, error) {
		org, err := rt.orgID()
		if err != nil {
			return nil, err
		}
		wss, err := rt.listWorkspaces(org)
		if err != nil {
			return nil, err
		}
		return names(wss, "name"), nil
	}), tc), cobra.ShellCompDirectiveNoFileComp
}
