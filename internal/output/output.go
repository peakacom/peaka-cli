// Package output renders command results. stdout carries data only; the
// shape per format is part of the CLI contract.
package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/itchyny/gojq"
	"gopkg.in/yaml.v3"
)

type Options struct {
	Format     string   // table | json | jsonl | csv | tsv | yaml | md | vertical
	Fields     []string // dotted paths; selects and orders columns
	JQ         string   // jq expression applied before rendering
	NoHeader   bool
	NoTruncate bool
	Width      int // terminal width for table truncation; 0 = no limit
}

// Print renders v (any JSON-shaped value, or a struct) in the chosen format.
func Print(w io.Writer, v any, o Options) error {
	val, err := normalize(v)
	if err != nil {
		return err
	}
	if o.JQ != "" {
		vals, err := applyJQ(val, o.JQ)
		if err != nil {
			return err
		}
		if len(vals) == 1 {
			val = vals[0]
		} else {
			val = vals
		}
	}
	if len(o.Fields) > 0 {
		val = selectFields(val, o.Fields)
	}
	switch o.Format {
	case "", "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(val)
	case "jsonl":
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		for _, row := range rows(val) {
			if err := enc.Encode(row); err != nil {
				return err
			}
		}
		return nil
	case "yaml":
		return yaml.NewEncoder(w).Encode(val)
	case "csv", "tsv":
		return writeCSV(w, val, o)
	case "md":
		return writeMarkdown(w, val, o)
	case "vertical":
		return writeVertical(w, val, o)
	case "table":
		return writeTable(w, val, o)
	}
	return fmt.Errorf("unknown format %q", o.Format)
}

// normalize turns any value into plain JSON types (maps, slices, strings, float64...).
func normalize(v any) (any, error) {
	switch v.(type) {
	case nil, map[string]any, []any, string, float64, bool, json.Number:
		return v, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func applyJQ(v any, expr string) ([]any, error) {
	q, err := gojq.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("--jq: %w", err)
	}
	// gojq wants float64/int, not json.Number.
	v = denumber(v)
	iter := q.Run(v)
	var out []any
	for {
		x, ok := iter.Next()
		if !ok {
			break
		}
		if e, isErr := x.(error); isErr {
			return nil, fmt.Errorf("--jq: %w", e)
		}
		out = append(out, x)
	}
	return out, nil
}

func denumber(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return int(i)
		}
		f, _ := t.Float64()
		return f
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = denumber(x)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, x := range t {
			s[i] = denumber(x)
		}
		return s
	}
	return v
}

// rows returns the records of v: an array's elements, or the single object.
func rows(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case nil:
		return nil
	default:
		return []any{t}
	}
}

// columns returns the column names in first-seen order across object rows.
func columns(rs []any, fields []string) []string {
	if len(fields) > 0 {
		return fields
	}
	var cols []string
	seen := map[string]bool{}
	for _, r := range rs {
		m, ok := r.(map[string]any)
		if !ok {
			return []string{"value"}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		// Go maps have no order; keep it stable and put id/name first.
		sort.SliceStable(keys, func(i, j int) bool { return rank(keys[i]) < rank(keys[j]) })
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
		}
	}
	return cols
}

func rank(k string) string {
	switch k {
	case "id":
		return "0"
	case "name", "displayName":
		return "1" + k
	case "description":
		return "zz" + k
	case "createdAt", "updatedAt":
		return "zzz" + k
	}
	return "5" + k
}

// lookup walks a dotted path in v; a key that itself contains dots (as after
// selectFields) is tried whole first.
func lookup(v any, path string) any {
	if m, ok := v.(map[string]any); ok {
		if x, found := m[path]; found {
			return x
		}
	}
	cur := v
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

func selectFields(v any, fields []string) any {
	pick := func(r any) any {
		m, ok := r.(map[string]any)
		if !ok {
			return r
		}
		out := make(map[string]any, len(fields))
		for _, f := range fields {
			out[f] = lookup(m, f)
		}
		return out
	}
	switch t := v.(type) {
	case []any:
		out := make([]any, len(t))
		for i, r := range t {
			out[i] = pick(r)
		}
		return out
	default:
		return pick(v)
	}
}

// cell renders a value for a text cell.
func cell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func writeCSV(w io.Writer, v any, o Options) error {
	cw := csv.NewWriter(w)
	if o.Format == "tsv" {
		cw.Comma = '\t'
	}
	rs := rows(v)
	cols := columns(rs, o.Fields)
	if !o.NoHeader {
		if err := cw.Write(cols); err != nil {
			return err
		}
	}
	for _, r := range rs {
		rec := make([]string, len(cols))
		for i, c := range cols {
			if m, ok := r.(map[string]any); ok {
				rec[i] = cell(lookup(m, c))
			} else {
				rec[i] = cell(r)
			}
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func writeMarkdown(w io.Writer, v any, o Options) error {
	rs := rows(v)
	cols := columns(rs, o.Fields)
	esc := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ") }
	fmt.Fprintf(w, "| %s |\n", strings.Join(cols, " | "))
	fmt.Fprintf(w, "|%s\n", strings.Repeat(" --- |", len(cols)))
	for _, r := range rs {
		vals := make([]string, len(cols))
		for i, c := range cols {
			if m, ok := r.(map[string]any); ok {
				vals[i] = esc(cell(lookup(m, c)))
			} else {
				vals[i] = esc(cell(r))
			}
		}
		fmt.Fprintf(w, "| %s |\n", strings.Join(vals, " | "))
	}
	return nil
}

func writeVertical(w io.Writer, v any, o Options) error {
	rs := rows(v)
	cols := columns(rs, o.Fields)
	width := 0
	for _, c := range cols {
		if len(c) > width {
			width = len(c)
		}
	}
	for i, r := range rs {
		if len(rs) > 1 {
			fmt.Fprintf(w, "-[ RECORD %d ]-\n", i+1)
		}
		for _, c := range cols {
			var val any = r
			if m, ok := r.(map[string]any); ok {
				val = lookup(m, c)
			}
			fmt.Fprintf(w, "%-*s  %s\n", width, c, cell(val))
		}
	}
	return nil
}
