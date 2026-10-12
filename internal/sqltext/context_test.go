package sqltext

import (
	"strings"
	"testing"
)

func TestAnalyzePlaces(t *testing.T) {
	cases := []struct {
		text  string
		place Place
		word  string
		prev  string
	}{
		{"", PlaceStatement, "", ""},
		{"SEL", PlaceStatement, "SEL", ""},
		{"SELECT ", PlaceColumn, "", "SELECT"},
		{"SELECT c.na", PlaceColumn, "c.na", "SELECT"},
		{"SELECT * FROM ", PlaceTable, "", "FROM"},
		{"SELECT * FROM stripe.pay", PlaceTable, "stripe.pay", "FROM"},
		{"SELECT * FROM a.b.c t JOIN ", PlaceTable, "", "JOIN"},
		{"SELECT * FROM a.b.c t, d.e", PlaceTable, "d.e", ","},
		{"SELECT * FROM a.b.c t WHERE t.", PlaceColumn, "t.", "WHERE"},
		{"SELECT * FROM a.b.c t ", PlaceColumn, "", "T"},
		{"USE stripe.", PlaceSchema, "stripe.", "USE"},
		{"SELECT 1; SELECT * FROM ", PlaceTable, "", "FROM"},
		{"SELECT count(*) FROM t GROUP BY ", PlaceColumn, "", "BY"},
	}
	for _, c := range cases {
		got := Analyze(c.text, len([]rune(c.text)))
		if got.Place != c.place || got.Word != c.word || got.Prev != c.prev {
			t.Errorf("%q: place %v word %q prev %q; want %v %q %q", c.text, got.Place, got.Word, got.Prev, c.place, c.word, c.prev)
		}
	}
}

func TestAnalyzeMeta(t *testing.T) {
	got := Analyze(`\d stripe.pay`, 13)
	if !got.Meta || len(got.MetaArgs) != 2 || got.Word != "stripe.pay" || got.MetaArgs[0] != `\d` {
		t.Fatalf("meta: %+v", got)
	}
	got = Analyze(`\dt `, 4)
	if !got.Meta || len(got.MetaArgs) != 2 || got.Word != "" {
		t.Fatalf("meta with trailing space: %+v", got)
	}
}

func TestTablesIn(t *testing.T) {
	refs := TablesIn(`SELECT c.name, count(*) FROM hubspot.crm.companies AS c, other.t x JOIN hubspot.crm.deals d ON d.company_id = c.id WHERE 1=1`)
	want := []TableRef{{"hubspot.crm.companies", "c"}, {"other.t", "x"}, {"hubspot.crm.deals", "d"}}
	if len(refs) != len(want) {
		t.Fatalf("refs: %+v", refs)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("ref %d: %+v, want %+v", i, refs[i], want[i])
		}
	}
	refs = TablesIn(`SELECT * FROM (SELECT 1) sub, peaka.query.q WHERE`)
	if len(refs) != 1 || refs[0].Path != "peaka.query.q" {
		t.Errorf("subquery should be skipped: %+v", refs)
	}
}

func TestHighlightAndLex(t *testing.T) {
	out := Highlight(`SELECT count(*) FROM t WHERE s = 'x' -- c`)
	for _, want := range []string{styleKeyword + "SELECT", styleFunction + "count", styleString + "'x'", styleComment + "-- c"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	toks := Lex(`a.b."c d" 1.5e3 >= 'it''s'`)
	kinds := []Kind{}
	for _, tk := range toks {
		if tk.Kind != Space {
			kinds = append(kinds, tk.Kind)
		}
	}
	want := []Kind{Ident, Punct, Ident, Punct, QuotedIdent, Number, Operator, String}
	if len(kinds) != len(want) {
		t.Fatalf("kinds: %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("token %d kind %v, want %v", i, kinds[i], want[i])
		}
	}
}
