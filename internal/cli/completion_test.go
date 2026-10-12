package cli

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"

	"github.com/peakacom/peaka-cli/internal/sqltext"
)

func TestNormalizeName(t *testing.T) {
	for in, want := range map[string]string{
		"GOOGLE_SHEETS": "googlesheets", "google-sheets": "googlesheets", "Google Sheets": "googlesheets",
		"Amazon RDS": "amazonrds", "  ": "", "deneme_conn": "denemeconn",
	} {
		if got := normalizeName(in); got != want {
			t.Errorf("normalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuggestNames(t *testing.T) {
	names := []string{"deneme_conn", "warehouse", "crm", "deneme2", "stripe-live", "stripe-test"}
	cases := map[string][]string{
		"deneme":      {"deneme2", "deneme_conn"}, // both start with it; by name
		"denem_conn":  {"deneme_conn"},            // one edit away
		"stripe":      {"stripe-live", "stripe-test"},
		"warehuose":   {"warehouse"},
		"nothinglike": nil,
	}
	for ref, want := range cases {
		got := suggestNames(names, ref, 3)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("suggestNames(%q) = %v, want %v", ref, got, want)
		}
	}
}

func TestCloseConnectionTypes(t *testing.T) {
	ts := []record{
		{"connectionType": "GOOGLE_SHEETS", "connectionTypeLabel": "Google Sheets", "keywords": []any{"sheets", "spreadsheet"}},
		{"connectionType": "GOOGLE_ADS", "connectionTypeLabel": "Google Ads"},
		{"connectionType": "POSTGRESQL", "connectionTypeLabel": "PostgreSQL", "keywords": []any{"postgres", "pg"}},
		{"connectionType": "AURORA", "connectionTypeLabel": "Amazon RDS"},
	}
	for ref, want := range map[string][]string{
		"google-sheets": {"GOOGLE_SHEETS", "GOOGLE_ADS"}, // the exact one first, then the other Google
		"google":        {"GOOGLE_ADS", "GOOGLE_SHEETS"},
		"postgres":      {"POSTGRESQL"},
		"amazon rds":    {"AURORA"},
		"sheet":         {"GOOGLE_SHEETS"},
	} {
		if got := closeConnectionTypes(ts, ref, 3); !reflect.DeepEqual(got, want) {
			t.Errorf("closeConnectionTypes(%q) = %v, want %v", ref, got, want)
		}
	}
	if got := containingConnectionTypes(ts, "ads", 5); !reflect.DeepEqual(got, []string{"GOOGLE_ADS"}) {
		t.Errorf("containingConnectionTypes(ads) = %v", got)
	}
}

func TestWithPrefixKeepsDescriptionsAndDedupes(t *testing.T) {
	got := withPrefix([]string{"crm\thubspot", "crm\thubspot", "CRM2\tsalesforce", "warehouse\tpostgres"}, "cr")
	want := []string{"crm\thubspot", "CRM2\tsalesforce"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("withPrefix = %v, want %v", got, want)
	}
}

func TestCompleterForKnowsEveryResolver(t *testing.T) {
	for name, res := range map[string]resolver{
		"catalog": resolveCatalog, "schema": resolveSchema, "table": resolveTable, "column": resolveColumn,
		"connection": resolveConnection, "query": resolveQuery, "folder": resolveFolder, "cache": resolveCache,
		"catalogOrSchema": resolveCatalogOrSchema, "oneCatalog": resolveOneCatalog,
	} {
		if completerFor(res) == nil {
			t.Errorf("no completer for the %s resolver", name)
		}
	}
	if completerFor(nil) != nil {
		t.Error("a nil resolver got a completer")
	}
}

func TestDottedCompletionStopsAtItsDepth(t *testing.T) {
	rt = nil // no runtime: completers must answer nothing, not panic
	for _, levels := range []int{1, 2, 3} {
		values, directive := completeDotted(levels)("a.b.c.d")
		if values != nil || directive != cobra.ShellCompDirectiveNoFileComp {
			t.Errorf("levels=%d: %v %v", levels, values, directive)
		}
	}
}

func TestMatchesTypedIgnoresQuotes(t *testing.T) {
	for _, c := range []struct {
		value, typed string
		want         bool
	}{
		{`peaka."table".customers`, `peaka.table.cu`, true},
		{`peaka."table".customers`, `peaka."table".cu`, true},
		{`peaka."table".customers`, `peaka."tab`, true},
		{`peaka."table".customers`, `peaka."tax`, false},
		{`"gs_abc"."evliya_çelebi"."2__kitap"`, `gs_abc.evliya_ç`, true},
		{`"gs_abc"."evliya_çelebi"."2__kitap"`, `"gs_abc"."evli`, true},
		{`"gs_abc"."evliya_çelebi"."2__kitap"`, `gs_abc.x`, false},
		{`gs_abc`, `"gs`, true},
		{`SELECT`, `sel`, true},
		{`SELECT`, `a.sel`, false},
		{`anything`, ``, true},
	} {
		if got := matchesTyped(c.value, c.typed); got != c.want {
			t.Errorf("matchesTyped(%s, %s) = %v", c.value, c.typed, got)
		}
	}
}

func TestPathQuotesAsNeededAndAsTyped(t *testing.T) {
	typed := sqltext.SplitIdents(`"peaka".tab`)
	if got := path(typed, "peaka", "table", "2__kitap"); got != `"peaka"."table"."2__kitap"` {
		t.Errorf("path = %s", got)
	}
	if got := path(nil, "gs_abc", "evliya_çelebi"); got != `gs_abc."evliya_çelebi"` {
		t.Errorf("path = %s", got)
	}
}
