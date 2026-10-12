package sqltext

import (
	"reflect"
	"testing"
)

func TestQuoteIdent(t *testing.T) {
	for name, want := range map[string]string{
		"customers":        "customers",
		"gs_abc":           "gs_abc",
		"evliya_çelebi":    `"evliya_çelebi"`,
		"2__kitap_1__cilt": `"2__kitap_1__cilt"`,
		"table":            `"table"`,
		"MyTable":          `"MyTable"`,
		"with space":       `"with space"`,
		`say "hi"`:         `"say ""hi"""`,
	} {
		if got := QuoteIdent(name, false); got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", name, got, want)
		}
	}
	if got := QuoteIdent("plain", true); got != `"plain"` {
		t.Errorf("forced: %s", got)
	}
}

func TestUnquote(t *testing.T) {
	for in, want := range map[string]string{
		`"table"`: "table", `"evli`: "evli", `plain`: "plain", `"say ""hi"""`: `say "hi"`, `"`: "",
	} {
		if got := Unquote(in); got != want {
			t.Errorf("Unquote(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestSplitIdents(t *testing.T) {
	cases := map[string][]Part{
		`peaka."table".cust`:    {{"peaka", false}, {"table", true}, {"cust", false}},
		`gs_abc."evliya_çelebi`: {{"gs_abc", false}, {"evliya_çelebi", true}},
		`"gs`:                   {{"gs", true}},
		`a.b.`:                  {{"a", false}, {"b", false}, {"", false}},
		`"dotted.name".x`:       {{"dotted.name", true}, {"x", false}},
		`"say ""hi""".y`:        {{`say "hi"`, true}, {"y", false}},
		``:                      {{"", false}},
	}
	for in, want := range cases {
		if got := SplitIdents(in); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitIdents(%s) = %+v, want %+v", in, got, want)
		}
	}
	if got := JoinIdents([]Part{{"peaka", false}, {"table", false}, {"2__k", false}}); got != `peaka."table"."2__k"` {
		t.Errorf("JoinIdents = %s", got)
	}
}
