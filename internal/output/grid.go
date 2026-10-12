package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Grid is an ordered, typed result set — what SQL returns. Unlike records,
// column order is meaningful and names may repeat.
type Grid struct {
	Columns []string
	Types   []string // SQL type names; numeric ones are right-aligned in tables
	Rows    [][]any
}

// NullText is how a SQL NULL shows in text formats.
const NullText = "NULL"

// IsNumericType reports whether a Trino type aligns right.
func IsNumericType(t string) bool {
	t = strings.ToLower(t)
	for _, p := range []string{"tinyint", "smallint", "integer", "bigint", "real", "double", "decimal"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// GridCell renders one SQL value as text.
func GridCell(v any) string {
	if v == nil {
		return NullText
	}
	return cell(v)
}

// uniqueNames disambiguates repeated column names for object formats (a, a → a, a_2).
func uniqueNames(cols []string) []string {
	seen := map[string]int{}
	out := make([]string, len(cols))
	for i, c := range cols {
		seen[c]++
		if seen[c] == 1 {
			out[i] = c
			continue
		}
		out[i] = fmt.Sprintf("%s_%d", c, seen[c])
	}
	return out
}

// orderedRow marshals a row as a JSON object keeping column order.
type orderedRow struct {
	keys []string
	vals []any
}

func (r orderedRow) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range r.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(r.vals[i])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// GridWriter receives rows as they arrive. Streaming formats write at once;
// buffered ones (table, json, yaml, md, vertical) render at End.
type GridWriter interface {
	Begin(columns, types []string) error
	Row(vals []any) error
	End() error
}

// NewGridWriter picks the writer for a format.
func NewGridWriter(w io.Writer, format string, o Options) (GridWriter, error) {
	switch format {
	case "jsonl":
		return &jsonlGrid{w: w}, nil
	case "csv":
		return &csvGrid{w: csv.NewWriter(w), header: !o.NoHeader}, nil
	case "tsv":
		cw := csv.NewWriter(w)
		cw.Comma = '\t'
		return &csvGrid{w: cw, header: !o.NoHeader}, nil
	case "text":
		return &textGrid{w: w}, nil
	case "", "json", "yaml", "md", "vertical", "table":
		if format == "" {
			format = "json"
		}
		return &bufferedGrid{w: w, format: format, o: o}, nil
	}
	return nil, fmt.Errorf("unknown format %q", format)
}

// textGrid prints the first column of every row as it is (EXPLAIN plans, SHOW CREATE).
type textGrid struct{ w io.Writer }

func (g *textGrid) Begin(_, _ []string) error { return nil }
func (g *textGrid) Row(vals []any) error {
	if len(vals) == 0 {
		return nil
	}
	_, err := fmt.Fprintln(g.w, GridCell(vals[0]))
	return err
}
func (g *textGrid) End() error { return nil }

type jsonlGrid struct {
	w    io.Writer
	keys []string
	enc  *json.Encoder
}

func (g *jsonlGrid) Begin(cols, _ []string) error {
	g.keys = uniqueNames(cols)
	g.enc = json.NewEncoder(g.w)
	g.enc.SetEscapeHTML(false)
	return nil
}
func (g *jsonlGrid) Row(vals []any) error { return g.enc.Encode(orderedRow{g.keys, vals}) }
func (g *jsonlGrid) End() error           { return nil }

type csvGrid struct {
	w      *csv.Writer
	header bool
}

func (g *csvGrid) Begin(cols, _ []string) error {
	if g.header {
		return g.w.Write(cols)
	}
	return nil
}
func (g *csvGrid) Row(vals []any) error {
	rec := make([]string, len(vals))
	for i, v := range vals {
		if v == nil {
			rec[i] = "" // CSV has no NULL; empty is the convention
		} else {
			rec[i] = cell(v)
		}
	}
	return g.w.Write(rec)
}
func (g *csvGrid) End() error {
	g.w.Flush()
	return g.w.Error()
}

type bufferedGrid struct {
	w      io.Writer
	format string
	o      Options
	grid   Grid
}

func (g *bufferedGrid) Begin(cols, types []string) error {
	g.grid.Columns, g.grid.Types = cols, types
	return nil
}
func (g *bufferedGrid) Row(vals []any) error {
	g.grid.Rows = append(g.grid.Rows, vals)
	return nil
}
func (g *bufferedGrid) End() error { return WriteGrid(g.w, &g.grid, g.format, g.o) }

// WriteGrid renders a complete grid.
func WriteGrid(w io.Writer, g *Grid, format string, o Options) error {
	switch format {
	case "", "json":
		keys := uniqueNames(g.Columns)
		rows := make([]orderedRow, len(g.Rows))
		for i, r := range g.Rows {
			rows[i] = orderedRow{keys, r}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(rows)
	case "yaml":
		keys := uniqueNames(g.Columns)
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, r := range g.Rows {
			m := &yaml.Node{Kind: yaml.MappingNode}
			for i, k := range keys {
				var v yaml.Node
				if err := v.Encode(denumber(r[i])); err != nil {
					return err
				}
				m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &v)
			}
			seq.Content = append(seq.Content, m)
		}
		return yaml.NewEncoder(w).Encode(seq)
	case "md":
		esc := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ") }
		fmt.Fprintf(w, "| %s |\n", strings.Join(g.Columns, " | "))
		fmt.Fprintf(w, "|%s\n", strings.Repeat(" --- |", len(g.Columns)))
		for _, r := range g.Rows {
			vals := make([]string, len(r))
			for i, v := range r {
				vals[i] = esc(GridCell(v))
			}
			fmt.Fprintf(w, "| %s |\n", strings.Join(vals, " | "))
		}
		return nil
	case "vertical":
		width := 0
		for _, c := range g.Columns {
			if n := utf8.RuneCountInString(c); n > width {
				width = n
			}
		}
		for i, r := range g.Rows {
			fmt.Fprintf(w, "-[ RECORD %d ]-\n", i+1)
			for j, c := range g.Columns {
				fmt.Fprintf(w, "%-*s  %s\n", width, c, GridCell(r[j]))
			}
		}
		return nil
	case "table":
		return writeGridTable(w, g, o)
	}
	return fmt.Errorf("unknown format %q", format)
}

func writeGridTable(w io.Writer, g *Grid, o Options) error {
	n := len(g.Columns)
	if n == 0 {
		return nil
	}
	cells := make([][]string, 0, len(g.Rows)+1)
	header := make([]string, n)
	copy(header, g.Columns)
	if !o.NoHeader {
		cells = append(cells, header)
	}
	for _, r := range g.Rows {
		line := make([]string, n)
		for i := 0; i < n; i++ {
			var v any
			if i < len(r) {
				v = r[i]
			}
			line[i] = strings.ReplaceAll(GridCell(v), "\n", " ")
		}
		cells = append(cells, line)
	}
	widths := make([]int, n)
	for _, line := range cells {
		for i, s := range line {
			if l := utf8.RuneCountInString(s); l > widths[i] {
				widths[i] = l
			}
		}
	}
	// Fit the terminal: shrink the widest columns first, never below the header or 8 runes.
	if o.Width > 0 && !o.NoTruncate {
		floor := make([]int, n)
		for i, h := range header {
			floor[i] = max(8, utf8.RuneCountInString(h))
		}
		total := func() int {
			t := 2 * (n - 1)
			for _, w := range widths {
				t += w
			}
			return t
		}
		for total() > o.Width {
			widest, wi := -1, -1
			for i, w := range widths {
				if w > floor[i] && w > widest {
					widest, wi = w, i
				}
			}
			if wi < 0 {
				break
			}
			widths[wi] = max(floor[wi], widths[wi]-(total()-o.Width))
		}
		for _, line := range cells {
			for i, s := range line {
				if utf8.RuneCountInString(s) > widths[i] {
					line[i] = string([]rune(s)[:widths[i]-1]) + "…"
				}
			}
		}
	}
	right := make([]bool, n)
	for i := range g.Types {
		if i < n {
			right[i] = IsNumericType(g.Types[i])
		}
	}
	var b strings.Builder
	for li, line := range cells {
		b.Reset()
		for i, s := range line {
			pad := widths[i] - utf8.RuneCountInString(s)
			if right[i] && li > 0 || (right[i] && o.NoHeader) {
				b.WriteString(strings.Repeat(" ", pad))
				b.WriteString(s)
			} else {
				b.WriteString(s)
				if i < n-1 {
					b.WriteString(strings.Repeat(" ", pad))
				}
			}
			if i < n-1 {
				b.WriteString("  ")
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	return nil
}
