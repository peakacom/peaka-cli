package output

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGridTableFitsTerminalWidth(t *testing.T) {
	g := &Grid{
		Columns: []string{"id", "text", "n"},
		Types:   []string{"integer", "varchar", "integer"},
		Rows: [][]any{
			{"1", strings.Repeat("x", 200), "7"},
			{"2", "short", "42"},
		},
	}
	var buf bytes.Buffer
	if err := WriteGrid(&buf, g, "table", Options{Width: 60}); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if w := utf8.RuneCountInString(line); w > 60 {
			t.Fatalf("line wider than the terminal (%d): %q", w, line)
		}
	}
	if !strings.Contains(buf.String(), "…") {
		t.Fatal("the wide column should be shortened with an ellipsis")
	}
	if !strings.Contains(buf.String(), "short") || !strings.Contains(buf.String(), "42") {
		t.Fatalf("other cells must survive: %q", buf.String())
	}
	// Wide enough: nothing is cut.
	buf.Reset()
	_ = WriteGrid(&buf, g, "table", Options{Width: 300})
	if strings.Contains(buf.String(), "…") {
		t.Fatal("nothing should be cut when it fits")
	}
	// NoTruncate wins over Width.
	buf.Reset()
	_ = WriteGrid(&buf, g, "table", Options{Width: 60, NoTruncate: true})
	if strings.Contains(buf.String(), "…") {
		t.Fatal("NoTruncate ignored")
	}
}
