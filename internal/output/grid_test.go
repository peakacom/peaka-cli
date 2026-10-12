package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func sampleGrid() *Grid {
	return &Grid{
		Columns: []string{"name", "amount", "name"},
		Types:   []string{"varchar", "decimal(10,2)", "varchar"},
		Rows: [][]any{
			{"alpha", json.Number("12.50"), nil},
			{"beta", json.Number("1234.00"), "x"},
		},
	}
}

func TestGridTableAlignsNumbersRightAndShowsNull(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteGrid(&buf, sampleGrid(), "table", Options{}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: %q", buf.String())
	}
	if lines[1] != "alpha    12.50  NULL" || lines[2] != "beta   1234.00  x" {
		t.Fatalf("alignment: %q / %q", lines[1], lines[2])
	}
}

func TestGridJSONKeepsOrderAndDisambiguates(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteGrid(&buf, sampleGrid(), "json", Options{}); err != nil {
		t.Fatal(err)
	}
	want := "[\n  {\n    \"name\": \"alpha\",\n    \"amount\": 12.50,\n    \"name_2\": null\n  },"
	if !strings.HasPrefix(buf.String(), want) {
		t.Fatalf("json: %q", buf.String())
	}
}

func TestGridStreamingWriters(t *testing.T) {
	var buf bytes.Buffer
	gw, _ := NewGridWriter(&buf, "csv", Options{})
	g := sampleGrid()
	_ = gw.Begin(g.Columns, g.Types)
	for _, r := range g.Rows {
		_ = gw.Row(r)
	}
	_ = gw.End()
	if buf.String() != "name,amount,name\nalpha,12.50,\nbeta,1234.00,x\n" {
		t.Fatalf("csv: %q", buf.String())
	}
	buf.Reset()
	gw, _ = NewGridWriter(&buf, "jsonl", Options{})
	_ = gw.Begin(g.Columns, g.Types)
	_ = gw.Row(g.Rows[0])
	if buf.String() != "{\"name\":\"alpha\",\"amount\":12.50,\"name_2\":null}\n" {
		t.Fatalf("jsonl: %q", buf.String())
	}
}

func TestGridYAMLAndMarkdown(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteGrid(&buf, sampleGrid(), "yaml", Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "- name: alpha\n  amount: 12.5\n  name_2: null\n") {
		t.Fatalf("yaml: %q", buf.String())
	}
	buf.Reset()
	_ = WriteGrid(&buf, sampleGrid(), "md", Options{})
	if !strings.HasPrefix(buf.String(), "| name | amount | name |\n| --- | --- | --- |\n| alpha | 12.50 | NULL |\n") {
		t.Fatalf("md: %q", buf.String())
	}
}
