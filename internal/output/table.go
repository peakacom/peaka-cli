package output

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const maxCell = 60

// writeTable prints records as an aligned table: upper-case headers, two-space
// gutters, cells truncated with … unless NoTruncate. A single object prints as
// a key/value list, which reads better for `describe`.
func writeTable(w io.Writer, v any, o Options) error {
	rs := rows(v)
	if len(rs) == 0 {
		return nil
	}
	if _, isList := v.([]any); !isList {
		if m, ok := v.(map[string]any); ok {
			return writeKeyValue(w, m, o)
		}
	}
	cols := columns(rs, o.Fields)
	grid := make([][]string, 0, len(rs)+1)
	header := make([]string, len(cols))
	for i, c := range cols {
		header[i] = strings.ToUpper(c)
	}
	if !o.NoHeader {
		grid = append(grid, header)
	}
	for _, r := range rs {
		line := make([]string, len(cols))
		for i, c := range cols {
			var val any = r
			if m, ok := r.(map[string]any); ok {
				val = lookup(m, c)
			}
			s := strings.ReplaceAll(cell(val), "\n", " ")
			if !o.NoTruncate && utf8.RuneCountInString(s) > maxCell {
				s = string([]rune(s)[:maxCell-1]) + "…"
			}
			line[i] = s
		}
		grid = append(grid, line)
	}
	widths := make([]int, len(cols))
	for _, line := range grid {
		for i, s := range line {
			if n := utf8.RuneCountInString(s); n > widths[i] {
				widths[i] = n
			}
		}
	}
	for _, line := range grid {
		var b strings.Builder
		for i, s := range line {
			b.WriteString(s)
			if i < len(line)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(s)+2))
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	return nil
}

func writeKeyValue(w io.Writer, m map[string]any, o Options) error {
	cols := columns([]any{m}, o.Fields)
	width := 0
	for _, c := range cols {
		if n := utf8.RuneCountInString(c); n > width {
			width = n
		}
	}
	for _, c := range cols {
		fmt.Fprintf(w, "%-*s  %s\n", width, c, cell(lookup(m, c)))
	}
	return nil
}
