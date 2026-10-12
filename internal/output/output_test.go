package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

var rowsFixture = []map[string]any{
	{"id": "a1", "name": "alpha", "createdAt": "2026-01-01", "nested": map[string]any{"k": 1}},
	{"id": "b2", "name": "beta", "createdAt": "2026-01-02", "extra": true},
}

func render(t *testing.T, v any, o Options) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Print(&buf, v, o); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestTableColumnsOrderAndHeader(t *testing.T) {
	out := render(t, rowsFixture, Options{Format: "table"})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 rows, got %q", out)
	}
	if !strings.HasPrefix(lines[0], "ID  NAME") {
		t.Fatalf("id and name first: %q", lines[0])
	}
	if !strings.Contains(lines[0], "CREATEDAT") || strings.Index(lines[0], "CREATEDAT") < strings.Index(lines[0], "NESTED") {
		t.Fatalf("createdAt should be last: %q", lines[0])
	}
	if !strings.Contains(lines[1], `{"k":1}`) {
		t.Fatalf("nested values render as compact json: %q", lines[1])
	}
}

func TestTableTruncatesWideCells(t *testing.T) {
	wide := []map[string]any{{"id": "x", "text": strings.Repeat("y", 200)}}
	out := render(t, wide, Options{Format: "table"})
	if !strings.Contains(out, "…") || len(out) > 200 {
		t.Fatalf("cell not truncated: %d bytes", len(out))
	}
	out = render(t, wide, Options{Format: "table", NoTruncate: true})
	if strings.Contains(out, "…") {
		t.Fatal("NoTruncate ignored")
	}
}

func TestSingleObjectIsKeyValue(t *testing.T) {
	out := render(t, map[string]any{"id": "a1", "name": "alpha"}, Options{Format: "table"})
	if !strings.HasPrefix(out, "id    a1\nname  alpha\n") {
		t.Fatalf("key/value expected: %q", out)
	}
}

func TestFieldsAndDottedPaths(t *testing.T) {
	out := render(t, rowsFixture, Options{Format: "csv", Fields: []string{"name", "nested.k"}})
	if out != "name,nested.k\nalpha,1\nbeta,\n" {
		t.Fatalf("csv: %q", out)
	}
}

func TestJQ(t *testing.T) {
	out := render(t, rowsFixture, Options{Format: "json", JQ: "map(.name)"})
	if strings.TrimSpace(out) != "[\n  \"alpha\",\n  \"beta\"\n]" {
		t.Fatalf("jq: %q", out)
	}
	out = render(t, rowsFixture, Options{Format: "json", JQ: "length"})
	if strings.TrimSpace(out) != "2" {
		t.Fatalf("jq scalar: %q", out)
	}
}

func TestJSONLAndMarkdownAndVertical(t *testing.T) {
	out := render(t, rowsFixture, Options{Format: "jsonl", Fields: []string{"id"}})
	if out != "{\"id\":\"a1\"}\n{\"id\":\"b2\"}\n" {
		t.Fatalf("jsonl: %q", out)
	}
	out = render(t, rowsFixture, Options{Format: "md", Fields: []string{"id", "name"}})
	if !strings.HasPrefix(out, "| id | name |\n| --- | --- |\n| a1 | alpha |\n") {
		t.Fatalf("md: %q", out)
	}
	out = render(t, rowsFixture, Options{Format: "vertical", Fields: []string{"id"}})
	if !strings.HasPrefix(out, "-[ RECORD 1 ]-\nid  a1\n-[ RECORD 2 ]-\nid  b2\n") {
		t.Fatalf("vertical: %q", out)
	}
}

func TestNumbersKeepTheirText(t *testing.T) {
	out := render(t, map[string]any{"big": "9007199254740993"}, Options{Format: "table"})
	if !strings.Contains(out, "9007199254740993") {
		t.Fatalf("string kept: %q", out)
	}
	// API bodies arrive as raw JSON; a large integer must not pass through float64.
	out = render(t, json.RawMessage(`[{"n":12345678901234567,"f":1.50}]`), Options{Format: "csv"})
	if !strings.Contains(out, "12345678901234567") || !strings.Contains(out, "1.50") {
		t.Fatalf("numbers should keep their text: %q", out)
	}
}
