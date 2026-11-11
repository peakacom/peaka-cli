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

// DEV-4344: a tag is found by its stable key as by its id.
func TestATagIsFoundByItsKey(t *testing.T) {
	tags := []record{{"id": "a1", "name": "Protected Health", "key": "phi"}, {"id": "b2", "name": "PII", "key": "pii"}}
	got, err := matchOne(tags, "PHI", "tag")
	if err != nil || str(got["id"]) != "a1" {
		t.Fatalf("by key: got %v, %v", got, err)
	}
	if got, _ := matchOne(tags, "b2", "tag"); str(got["id"]) != "b2" {
		t.Fatalf("by id: got %v", got)
	}
}

// A tag only classifies: its commands take no masking rules, and its list shows none.
func TestTagsCarryNoMaskingRules(t *testing.T) {
	gov := newGovernanceCmd()
	for _, path := range [][]string{{"tags", "create"}, {"tags", "update"}} {
		cmd, _, err := gov.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		if cmd.Flags().Lookup("rule") != nil {
			t.Errorf("%v still has --rule", path)
		}
		if cmd.Flags().Lookup("active") == nil {
			t.Errorf("%v lost --active", path)
		}
	}
}

func TestMaskingPolicyCommands(t *testing.T) {
	gov := newGovernanceCmd()
	for _, c := range []struct {
		path  []string
		flags []string
	}{
		{[]string{"masking-policies", "list"}, []string{"tag"}},
		{[]string{"masking-policies", "describe"}, nil},
		{[]string{"masking-policies", "get"}, nil},
		{[]string{"masking-policies", "create"}, []string{"tag", "name", "default", "enabled", "exception", "reason"}},
		{[]string{"masking-policies", "update"}, []string{"tag", "name", "default", "enabled", "exception", "reason"}},
		{[]string{"masking-policies", "delete"}, []string{"reason"}},
	} {
		cmd, _, err := gov.Find(c.path)
		if err != nil || cmd.Name() != strings.Fields(cmd.Use)[0] || cmd.Parent().Name() != "masking-policies" {
			t.Fatalf("%v: %v", c.path, err)
		}
		for _, f := range c.flags {
			if cmd.Flags().Lookup(f) == nil {
				t.Errorf("%v has no --%s", c.path, f)
			}
		}
	}
	create, _, _ := gov.Find([]string{"masking-policies", "create"})
	if f := create.Flags().Lookup("enabled"); f.DefValue != "true" {
		t.Errorf("a new policy is enabled unless told: %q", f.DefValue)
	}
	update, _, _ := gov.Find([]string{"masking-policies", "update"})
	if f := update.Flags().Lookup("enabled"); f.DefValue != "false" || update.Flags().Changed("enabled") {
		t.Errorf("update leaves enabled alone unless told")
	}
}

func TestParseException(t *testing.T) {
	cases := []struct {
		in                     string
		kind, who, mask, scope string
		bad                    bool
	}{
		{"group:Clinicians=unmasked", "group", "Clinicians", "unmasked", "", false},
		{"user:ana@example.com=show-last-four@phi.notes", "user", "ana@example.com", "show-last-four", "phi.notes", false},
		{"ROLE:r1 = deny", "role", "r1", "deny", "", false},
		{"everyone=nullify", "", "", "", "", true},
		{"group:=nullify", "", "", "", "", true},
		{"team:x=nullify", "", "", "", "", true},
		{"group:Clinicians", "", "", "", "", true},
		{"group:Clinicians=@phi", "", "", "", "", true},
	}
	for _, c := range cases {
		kind, who, mask, scope, err := parseException(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("%q: want an error", c.in)
			}
			continue
		}
		if err != nil || kind != c.kind || who != c.who || mask != c.mask || scope != c.scope {
			t.Errorf("%q: got %q %q %q %q %v", c.in, kind, who, mask, scope, err)
		}
	}
}

func TestShapePolicyResolvesNamesAndTheDefault(t *testing.T) {
	masks := map[string]string{"nullify": "nullify", "Unmasked": "unmasked", "Last four": "c0ffee"}
	maskOf := func(ref string) (string, error) {
		if id, ok := masks[ref]; ok {
			return id, nil
		}
		return "", fmt.Errorf("no mask %q", ref)
	}
	groupOf := func(ref string) (string, error) { return "g-" + ref, nil }
	tagOf := func(ref string) (string, error) { return "t-" + ref, nil }

	body := map[string]any{"defaultMaskId": "nullify", "exceptions": []any{
		"group:Clinicians=Unmasked", "user:ana@example.com=Last four@phi.notes", "role:r1=nullify",
		map[string]any{"subject": map[string]any{"type": "role", "id": "r2"}, "maskId": "deny"},
	}}
	if err := shapePolicy(body, maskOf, groupOf, tagOf); err != nil {
		t.Fatal(err)
	}
	if body["defaultMaskId"] != "nullify" {
		t.Errorf("default: %v", body["defaultMaskId"])
	}
	ex := body["exceptions"].([]any)
	first := ex[0].(map[string]any)
	if first["maskId"] != "unmasked" || first["subject"].(map[string]any)["id"] != "g-Clinicians" || first["scopeTagId"] != nil {
		t.Errorf("group exception: %v", first)
	}
	second := ex[1].(map[string]any)
	if second["subject"].(map[string]any)["id"] != "ana@example.com" || second["maskId"] != "c0ffee" || second["scopeTagId"] != "t-phi.notes" {
		t.Errorf("user exception: %v", second)
	}
	if third := ex[2].(map[string]any); third["subject"].(map[string]any)["type"] != "role" {
		t.Errorf("role exception: %v", third)
	}
	if len(ex) != 4 {
		t.Errorf("an exception from a file is kept: %v", ex)
	}

	for _, org := range []string{"org", "ORG", ""} {
		b := map[string]any{"defaultMaskId": org}
		if err := shapePolicy(b, maskOf, groupOf, tagOf); err != nil || b["defaultMaskId"] != nil {
			t.Errorf("--default %q is the organization's default (null): %v %v", org, b["defaultMaskId"], err)
		}
	}
	if b := (map[string]any{"name": "x"}); shapePolicy(b, maskOf, groupOf, tagOf) != nil || len(b) != 1 {
		t.Errorf("nothing given, nothing sent: %v", b)
	}
	if err := shapePolicy(map[string]any{"exceptions": []any{"group:A=nope"}}, maskOf, groupOf, tagOf); err == nil {
		t.Error("an unknown mask is refused")
	}
}
