package cli

import (
	"sort"
	"strings"

	"github.com/reeflective/readline"

	"github.com/peakacom/peaka-cli/internal/sqltext"
)

// candidate is one completion with its tag group and description.
type candidate struct {
	value, desc, tag string
}

// complete is the readline completer: it reads the context before the cursor
// and offers tables, columns, schemas, catalogs, keywords, functions or meta
// commands, filtered by the word being typed.
func (r *repl) complete(line []rune, cursor int) readline.Completions {
	ctx := sqltext.Analyze(string(line), cursor)
	var cands []candidate
	if ctx.Meta {
		cands = r.metaCandidates(ctx)
	} else {
		cands = r.sqlCandidates(ctx)
	}
	word := strings.ToLower(ctx.Word)
	byTag := map[string][]candidate{}
	var tags []string
	for _, c := range cands {
		if word != "" && !strings.HasPrefix(strings.ToLower(c.value), word) {
			continue
		}
		if _, ok := byTag[c.tag]; !ok {
			tags = append(tags, c.tag)
		}
		byTag[c.tag] = append(byTag[c.tag], c)
	}
	var comps readline.Completions
	first := true
	for _, tag := range tags {
		vals := make([]string, 0, 2*len(byTag[tag]))
		for _, c := range byTag[tag] {
			vals = append(vals, c.value, c.desc)
		}
		one := readline.CompleteValuesDescribed(vals...).Tag(tag).NoSort()
		if first {
			comps, first = one, false
		} else {
			comps = comps.Merge(one)
		}
	}
	if first {
		comps = readline.CompleteValues()
	}
	comps.PREFIX = ctx.Word
	return comps
}

func (r *repl) sqlCandidates(ctx sqltext.Context) []candidate {
	var out []candidate
	parts := strings.Split(ctx.Word, ".")
	head := parts[:len(parts)-1]
	cat, sch := r.s.client.Catalog, r.s.client.Schema

	if len(head) > 0 {
		// Dotted: a table alias or name, a catalog, a catalog.schema, or a full path.
		prefix := strings.Join(head, ".") + "."
		if len(head) == 1 {
			if ref, ok := r.tableFor(ctx.Tables, head[0]); ok {
				if c, s, t, ok := r.md.ResolveTable(ref.Path, cat, sch); ok {
					for _, col := range r.md.Columns(c, s, t) {
						out = append(out, candidate{prefix + col.Name, col.Type, "columns"})
					}
					return out
				}
			}
			for _, s := range r.md.Schemas(head[0]) {
				out = append(out, candidate{prefix + s, "schema", "schemas"})
			}
			if cat != "" && len(r.md.Schemas(head[0])) == 0 {
				// maybe schema.table under the session catalog
				for _, t := range r.md.Tables(cat, head[0]) {
					out = append(out, candidate{prefix + t, "table", "tables"})
				}
			}
			return out
		}
		if len(head) == 2 {
			for _, t := range r.md.Tables(head[0], head[1]) {
				out = append(out, candidate{prefix + t, "table", "tables"})
			}
			return out
		}
		if len(head) == 3 {
			for _, col := range r.md.Columns(head[0], head[1], head[2]) {
				out = append(out, candidate{prefix + col.Name, col.Type, "columns"})
			}
		}
		return out
	}

	switch ctx.Place {
	case sqltext.PlaceTable, sqltext.PlaceSchema:
		if cat != "" && sch != "" && ctx.Place == sqltext.PlaceTable {
			for _, t := range r.md.Tables(cat, sch) {
				out = append(out, candidate{t, cat + "." + sch, "tables"})
			}
		}
		if cat != "" {
			for _, s := range r.md.Schemas(cat) {
				out = append(out, candidate{s, "schema in " + cat, "schemas"})
			}
		}
		for _, c := range r.md.Catalogs() {
			out = append(out, candidate{c, "catalog", "catalogs"})
		}
	case sqltext.PlaceColumn:
		for _, ref := range ctx.Tables {
			if ref.Alias != "" {
				out = append(out, candidate{ref.Alias, ref.Path, "tables"})
			}
			if c, s, t, ok := r.md.ResolveTable(ref.Path, cat, sch); ok {
				for _, col := range r.md.Columns(c, s, t) {
					out = append(out, candidate{col.Name, col.Type + "  " + t, "columns"})
				}
			}
		}
		for _, f := range sqltext.Functions {
			out = append(out, candidate{f + "(", "function", "functions"})
		}
		out = append(out, keywordCandidates()...)
	case sqltext.PlaceStatement:
		for _, k := range []string{"SELECT", "SHOW", "DESCRIBE", "EXPLAIN", "USE", "WITH", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "SET", "PREPARE", "EXECUTE"} {
			out = append(out, candidate{k, "", "statements"})
		}
		out = append(out, r.metaCandidates(sqltext.Context{Meta: true, MetaArgs: []string{""}})...)
	}
	return out
}

func keywordCandidates() []candidate {
	out := make([]candidate, 0, len(sqltext.Keywords))
	for _, k := range sqltext.Keywords {
		out = append(out, candidate{k, "", "keywords"})
	}
	return out
}

// tableFor finds the table a word refers to: an alias, or a (last segment of a) path.
func (r *repl) tableFor(tables []sqltext.TableRef, word string) (sqltext.TableRef, bool) {
	for _, t := range tables {
		if strings.EqualFold(t.Alias, word) {
			return t, true
		}
	}
	for _, t := range tables {
		segs := strings.Split(t.Path, ".")
		if strings.EqualFold(segs[len(segs)-1], word) || strings.EqualFold(t.Path, word) {
			return t, true
		}
	}
	return sqltext.TableRef{}, false
}

func (r *repl) metaCandidates(ctx sqltext.Context) []candidate {
	var out []candidate
	if len(ctx.MetaArgs) <= 1 {
		names := make([]string, 0, len(metaCommands))
		for name := range metaCommands {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, candidate{n, metaCommands[n].help, "commands"})
		}
		return out
	}
	cat, sch := r.s.client.Catalog, r.s.client.Schema
	switch ctx.MetaArgs[0] {
	case `\d`, `\dt`, `\dn`, `\use`, `\dc`:
		parts := strings.Split(ctx.Word, ".")
		switch len(parts) {
		case 1:
			for _, c := range r.md.Catalogs() {
				out = append(out, candidate{c, "catalog", "catalogs"})
			}
			if cat != "" {
				for _, s := range r.md.Schemas(cat) {
					out = append(out, candidate{s, "schema in " + cat, "schemas"})
				}
				if sch != "" {
					for _, t := range r.md.Tables(cat, sch) {
						out = append(out, candidate{t, "table", "tables"})
					}
				}
			}
		case 2:
			for _, s := range r.md.Schemas(parts[0]) {
				out = append(out, candidate{parts[0] + "." + s, "schema", "schemas"})
			}
		case 3:
			for _, t := range r.md.Tables(parts[0], parts[1]) {
				out = append(out, candidate{parts[0] + "." + parts[1] + "." + t, "table", "tables"})
			}
		}
	case `\format`:
		for _, f := range []string{"table", "vertical", "json", "jsonl", "csv", "tsv", "yaml", "md"} {
			out = append(out, candidate{f, "", "formats"})
		}
	case `\i`, `\o`, `\e`:
		// file names: left to the shell's own file completion later
	}
	return out
}
