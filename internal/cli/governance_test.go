package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
)

// Codex review round 5, R5-6: a label or name that is two records is refused before anything is sent.
func TestTwiceNamesAmbiguousRecords(t *testing.T) {
	idx := index([]record{
		{"id": "a", "name": "PHI"},
		{"id": "b", "name": "phi"},
		{"id": "c", "name": "PII"},
	}, func(x record) (string, string) {
		return strings.ToUpper(str(x["name"])), str(x["id"])
	})
	if got := twice(idx, "PHI", "tag"); len(got) != 1 || !strings.Contains(got[0], "a, b") {
		t.Fatalf("PHI is two tags: %v", got)
	}
	if got := twice(idx, "PII", "tag"); got != nil {
		t.Fatalf("PII is one tag: %v", got)
	}
}

// R5-7: an existing rule named for another target is not updated as if it were the file's.
func TestOtherTarget(t *testing.T) {
	onCustomers := record{"target": map[string]any{"type": "table", "table": "peaka.table.customers", "projectId": "p1"}}
	onRegion := record{"target": map[string]any{"type": "tag", "tagId": "t-region"}}
	cases := []struct {
		name                         string
		rule                         record
		table, tagID, tagLabel, proj string
		differs                      bool
	}{
		{"same table", onCustomers, "peaka.table.customers", "", "", "p1", false},
		{"same table, other case", onCustomers, "PEAKA.table.Customers", "", "", "p1", false},
		{"other table", onCustomers, "peaka.table.employees", "", "", "p1", true},
		{"same table, other project", onCustomers, "peaka.table.customers", "", "", "p2", true},
		{"table rule asked as a tag rule", onCustomers, "", "t-region", "REGION", "p1", true},
		{"same tag", onRegion, "", "t-region", "REGION", "", false},
		{"other tag", onRegion, "", "t-other", "OTHER", "", true},
		{"tag rule asked as a table rule", onRegion, "peaka.table.customers", "", "", "p1", true},
	}
	for _, c := range cases {
		if got := otherTarget(c.rule, c.table, c.tagID, c.tagLabel, c.proj) != ""; got != c.differs {
			t.Errorf("%s: differs=%v, want %v", c.name, got, c.differs)
		}
	}
}

// Tags are indexed by the public API's flat name, after a create as before it.
func TestTagKeyReadsTheFlatName(t *testing.T) {
	idx := index([]record{{"id": "a", "name": "Phi"}}, tagKey)
	if ids := idx["PHI"]; len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("PHI is tag a: %v", idx)
	}
}

// R6-7: listAt follows nextCursor to the last page.
func TestListAtReadsEveryPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Query().Get("cursor") {
		case "":
			fmt.Fprint(w, `{"items":[{"id":"a","name":"A"}],"nextCursor":"c2","hasMore":true}`)
		case "c2":
			fmt.Fprint(w, `{"items":[{"id":"b","name":"B"}],"nextCursor":null,"hasMore":false}`)
		}
	}))
	defer srv.Close()
	r := &Runtime{Ctx: context.Background(), Settings: &config.Settings{Source: map[string]string{}}, client: api.New(srv.URL, "test", time.Second)}
	got, err := listAt(r, "/organizations/o/governance/tags")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || str(got[1]["id"]) != "b" {
		t.Fatalf("both pages: %v", got)
	}
}

func TestListAtRefusesMoreWithoutACursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[{"id":"a","name":"A"}],"nextCursor":null,"hasMore":true}`)
	}))
	defer srv.Close()
	r := &Runtime{Ctx: context.Background(), Settings: &config.Settings{Source: map[string]string{}}, client: api.New(srv.URL, "test", time.Second)}
	if _, err := listAt(r, "/organizations/o/governance/tags"); err == nil {
		t.Fatal("a list that says there is more with no cursor is not the whole list")
	}
}
