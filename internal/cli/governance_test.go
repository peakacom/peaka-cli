package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"errors"
	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
	"github.com/spf13/cobra"
	"io"
	"os"
	"path/filepath"
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

// Milestone 1 (DEV-4250): the commands and their flags.
func TestMilestoneOneCommands(t *testing.T) {
	gov := newGovernanceCmd()
	for _, c := range []struct {
		path  []string
		flags []string
	}{
		{[]string{"tags", "list"}, []string{"parent", "q", "status"}},
		{[]string{"masking-policies", "list"}, []string{"tag", "enabled", "q"}},
		{[]string{"column-tags", "search"}, []string{"tag", "project", "catalog-id", "schema", "table", "q"}},
		{[]string{"column-tags", "set"}, []string{"catalog-id", "schema", "table", "tag-column", "none", "reason"}},
		{[]string{"table-tags", "list"}, []string{"tag", "catalog-id", "schema", "table"}},
		{[]string{"row-rules", "list"}, []string{"project", "catalog-id", "schema", "table", "tag", "target", "enabled", "q"}},
		{[]string{"row-rules", "create"}, []string{"except"}},
		{[]string{"row-rules", "update"}, []string{"except", "no-exceptions"}},
		{[]string{"suggestions", "list"}, []string{"status", "project", "tag", "source", "q", "order"}},
		{[]string{"suggestions", "describe"}, nil},
		{[]string{"suggestions", "create"}, []string{"catalog-id", "schema", "table", "column", "tag", "reason"}},
		{[]string{"suggestions", "approve"}, []string{"note"}},
		{[]string{"suggestions", "reject"}, []string{"note"}},
		{[]string{"suggestions", "approve-many"}, []string{"id", "project", "tag", "source", "q", "limit", "note"}},
		{[]string{"suggestions", "reject-many"}, []string{"id", "project", "tag", "source", "q", "limit", "note"}},
	} {
		cmd, _, err := gov.Find(c.path)
		if err != nil || cmd.Name() != c.path[len(c.path)-1] {
			t.Fatalf("%v: %v", c.path, err)
		}
		for _, f := range c.flags {
			if cmd.Flags().Lookup(f) == nil {
				t.Errorf("%v has no --%s", c.path, f)
			}
		}
	}
}

func TestRowRuleExceptions(t *testing.T) {
	c := &callCtx{Body: map[string]any{"except": []string{"group:auditors", "USER:2b1988d5-fff8-4bb2-bb44-bf00c810cf32", "role:admins"}}}
	if err := rowRuleExceptions(c); err != nil {
		t.Fatal(err)
	}
	got := c.Body["exceptions"].([]any)
	if len(got) != 3 || got[1].(map[string]any)["subject"].(map[string]any)["type"] != "user" {
		t.Fatalf("exceptions: %v", got)
	}
	if _, left := c.Body["except"]; left {
		t.Fatal("--except is not sent as it is")
	}
	none := &callCtx{Body: map[string]any{"noExceptions": true}}
	if err := rowRuleExceptions(none); err != nil || len(none.Body["exceptions"].([]any)) != 0 {
		t.Fatalf("--no-exceptions sends an empty list: %v %v", none.Body, err)
	}
	keep := &callCtx{Body: map[string]any{}}
	if err := rowRuleExceptions(keep); err != nil || keep.Body["exceptions"] != nil {
		t.Fatalf("neither flag keeps them: %v", keep.Body)
	}
	for _, bad := range [][]string{{"team:x"}, {"group:"}, {"auditors"}} {
		if err := rowRuleExceptions(&callCtx{Body: map[string]any{"except": bad}}); err == nil {
			t.Errorf("%v is refused", bad)
		}
	}
	if err := rowRuleExceptions(&callCtx{Body: map[string]any{"except": []string{"group:a"}, "noExceptions": true}}); err == nil {
		t.Error("--except with --no-exceptions is refused")
	}
}

func TestManySuggestions(t *testing.T) {
	byIDs := &callCtx{Body: map[string]any{"ids": []string{"a", "b"}, "limit": 0}}
	if err := manySuggestions(byIDs); err != nil || byIDs.Body["filter"] != nil || byIDs.Body["limit"] != nil {
		t.Fatalf("ids: %v %v", byIDs.Body, err)
	}
	byFilter := &callCtx{Body: map[string]any{"fTag": "PII", "fProject": "p1", "limit": 50}}
	if err := manySuggestions(byFilter); err != nil {
		t.Fatal(err)
	}
	f := byFilter.Body["filter"].(map[string]any)
	if f["tagId"] != "PII" || f["projectId"] != "p1" || byFilter.Body["fTag"] != nil {
		t.Fatalf("filter: %v", byFilter.Body)
	}
	if err := manySuggestions(&callCtx{Body: map[string]any{}}); err == nil {
		t.Error("nothing named is refused")
	}
	if err := manySuggestions(&callCtx{Body: map[string]any{"ids": []string{"a"}, "fQ": "x"}}); err == nil {
		t.Error("ids and a filter together are refused")
	}
}

func TestTableColumnTags(t *testing.T) {
	id := "b220ce38-f2e8-4454-a17b-1c3b9f87f64e"
	c := &callCtx{Body: map[string]any{"tagColumns": []string{"ssn=" + id, " email = " + id}}}
	if err := tableColumnTags(c); err != nil {
		t.Fatal(err)
	}
	cols := c.Body["columns"].([]map[string]any)
	if len(cols) != 2 || cols[1]["column"] != "email" || cols[1]["tagId"] != id {
		t.Fatalf("columns: %v", cols)
	}
	none := &callCtx{Body: map[string]any{"none": true}}
	if err := tableColumnTags(none); err != nil || len(none.Body["columns"].([]map[string]any)) != 0 {
		t.Fatalf("--none sends no column: %v %v", none.Body, err)
	}
	if err := tableColumnTags(&callCtx{Body: map[string]any{}}); err == nil {
		t.Error("neither --tag-column nor --none is refused")
	}
	if err := tableColumnTags(&callCtx{Body: map[string]any{"tagColumns": []string{"ssn"}}}); err == nil {
		t.Error("a pair without = is refused")
	}
}

// A list command prints every page: the first alone would read as the whole list.
func TestAListFollowsEveryPage(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		asked = append(asked, req.URL.Query().Get("cursor"))
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Query().Get("cursor") {
		case "":
			fmt.Fprint(w, `{"items":[{"id":"a"}],"nextCursor":"c2","hasMore":true}`)
		case "c2":
			fmt.Fprint(w, `{"items":[{"id":"b"}],"nextCursor":"c3","hasMore":true}`)
		case "c3":
			fmt.Fprint(w, `{"items":[{"id":"c"}],"nextCursor":null,"hasMore":false}`)
		}
	}))
	defer srv.Close()
	var out strings.Builder
	r := &Runtime{Ctx: context.Background(), Settings: &config.Settings{Source: map[string]string{}}, client: api.New(srv.URL, "test", time.Second),
		Out: &out, Err: &out}
	gf.format = "json"
	defer func() { gf.format = "" }()
	if err := r.call("GET", "/organizations/o/governance/tags", url.Values{}, nil, "items", nil, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Join(asked, ",") != ",c2,c3" {
		t.Fatalf("pages asked: %v", asked)
	}
	for _, id := range []string{`"a"`, `"b"`, `"c"`} {
		if !strings.Contains(out.String(), id) {
			t.Errorf("%s missing from %s", id, out.String())
		}
	}
}

// Round 2 (DEV-4250): the commands and their flags.
func TestRoundTwoCommands(t *testing.T) {
	gov := newGovernanceCmd()
	for _, c := range []struct {
		path  []string
		flags []string
	}{
		{[]string{"sources", "list"}, nil},
		{[]string{"sources", "create"}, []string{"kind", "catalog-id", "apply-as", "interval", "import-classes", "enabled"}},
		{[]string{"sources", "update"}, []string{"apply-as", "interval", "import-classes", "enabled"}},
		{[]string{"sources", "sync"}, nil},
		{[]string{"sources", "map"}, []string{"class", "tag", "ignore"}},
		{[]string{"patterns", "set"}, []string{"class", "pattern", "enabled"}},
		{[]string{"declassifications", "create"}, []string{"object-kind", "object-id", "catalog-id", "schema", "table", "column", "tag", "reason", "scope", "days", "source"}},
		{[]string{"declassifications", "revoke"}, nil},
		{[]string{"access", "column"}, []string{"catalog-id", "schema", "table", "column", "limit"}},
		{[]string{"access", "holders"}, []string{"catalog-id", "schema", "table", "column"}},
		{[]string{"access", "member"}, nil},
		{[]string{"access", "exposure"}, []string{"q", "tag"}},
		{[]string{"access", "readers"}, []string{"tag", "days"}},
		{[]string{"decisions"}, []string{"user", "query", "catalog-id", "schema", "table", "denied", "tag", "since", "limit", "cursor"}},
		{[]string{"row-rules", "reaching"}, []string{"catalog-id", "schema", "table"}},
		{[]string{"row-rules", "problems"}, nil},
	} {
		cmd, _, err := gov.Find(c.path)
		if err != nil || cmd.Name() != c.path[len(c.path)-1] {
			t.Fatalf("%v: %v", c.path, err)
		}
		for _, f := range c.flags {
			if cmd.Flags().Lookup(f) == nil {
				t.Errorf("%v has no --%s", c.path, f)
			}
		}
	}
}

func TestDeclassifySources(t *testing.T) {
	c := &callCtx{Body: map[string]any{"sourceRefs": []string{"p1:7.public.people.ssn"}}}
	if err := declassifySources(c); err != nil {
		t.Fatal(err)
	}
	got := c.Body["sources"].([]any)[0].(map[string]any)
	if got["projectId"] != "p1" || got["catalogId"] != "7" || got["column"] != "ssn" || c.Body["sourceRefs"] != nil {
		t.Fatalf("sources: %v", c.Body)
	}
	for _, bad := range []string{"7.public.people.ssn", "p1:7.public.people", "p1:"} {
		if err := declassifySources(&callCtx{Body: map[string]any{"sourceRefs": []string{bad}}}); err == nil {
			t.Errorf("%q is refused", bad)
		}
	}
	if err := declassifySources(&callCtx{Body: map[string]any{}}); err != nil {
		t.Error("no --source sends none")
	}
}

func TestLogsAreNotWalkedWhole(t *testing.T) {
	if !isLog("/organizations/o/governance/decisions") || !isLog("/organizations/o/governance/audit-events") || isLog("/organizations/o/governance/tags") {
		t.Fatal("decisions and the audit log are read a page at a time; other lists whole")
	}
}

// Round 3 (DEV-4250): policy, access reviews, publication requests.
func TestRoundThreeCommands(t *testing.T) {
	gov := newGovernanceCmd()
	for _, c := range []struct {
		path  []string
		flags []string
	}{
		{[]string{"policy", "show"}, nil},
		{[]string{"policy", "templates"}, nil},
		{[]string{"policy", "diff"}, nil},
		{[]string{"policy", "use"}, []string{"reason"}},
		{[]string{"policy", "set"}, []string{"change", "reason"}},
		{[]string{"policy", "history"}, nil},
		{[]string{"policy", "proposals"}, nil},
		{[]string{"policy", "approve"}, []string{"reason"}},
		{[]string{"policy", "reject"}, []string{"reason"}},
		{[]string{"policy", "unlock"}, []string{"reason"}},
		{[]string{"reviews", "start"}, nil},
		{[]string{"reviews", "decide"}, []string{"decision", "note"}},
		{[]string{"reviews", "close"}, nil},
		{[]string{"reviews", "export"}, nil},
		{[]string{"publications", "list"}, nil},
		{[]string{"publications", "approve"}, []string{"reason"}},
		{[]string{"publications", "revoke"}, nil},
	} {
		cmd, _, err := gov.Find(c.path)
		if err != nil || cmd.Name() != c.path[len(c.path)-1] {
			t.Fatalf("%v: %v", c.path, err)
		}
		for _, f := range c.flags {
			if cmd.Flags().Lookup(f) == nil {
				t.Errorf("%v has no --%s", c.path, f)
			}
		}
	}
	policy, _, _ := gov.Find([]string{"policy"})
	if policy.RunE == nil {
		t.Error("`gov policy` alone still shows the policy")
	}
}

func TestPolicyFields(t *testing.T) {
	c := &callCtx{Body: map[string]any{"fieldPairs": []string{"untaggedColumn=DENY", "auditRetentionDays=400", "nativeQueryOnTaggedCatalog=ALLOW", "x=true"}}}
	if err := policyFields(c); err != nil {
		t.Fatal(err)
	}
	f := c.Body["fields"].(map[string]any)
	if f["untaggedColumn"] != "DENY" || f["auditRetentionDays"] != float64(400) || f["x"] != true || c.Body["fieldPairs"] != nil {
		t.Fatalf("fields: %v", f)
	}
	if err := policyFields(&callCtx{Body: map[string]any{"fieldPairs": []string{"novalue"}}}); err == nil {
		t.Error("a pair without = is refused")
	}
	if err := policyFields(&callCtx{Body: map[string]any{}}); err == nil {
		t.Error("no field is refused")
	}
}

// A 202 is not done: the record that waits is printed and the command ends with exit 10, except where waiting is
// what the command is for (a suggestion).
func TestAHeldChangeIsNotDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"id":"cr-1","kind":"TAG_DELETE","status":"PENDING"}`)
	}))
	defer srv.Close()
	var out strings.Builder
	r := &Runtime{Ctx: context.Background(), Settings: &config.Settings{Source: map[string]string{}}, client: api.New(srv.URL, "test", time.Second),
		Out: &out, Err: &out}
	gf.format = "json"
	defer func() { gf.format = "" }()
	err := r.call("DELETE", "/organizations/o/governance/tags/t", url.Values{}, nil, "", nil, "Deleted.")
	var exit *api.ExitError
	if !errors.As(err, &exit) || exit.Code != api.ExitPending || !strings.Contains(err.Error(), "cr-1") {
		t.Fatalf("a held delete: %v", err)
	}
	if !strings.Contains(out.String(), "cr-1") {
		t.Errorf("the change request is not printed: %s", out.String())
	}
	if err := r.call("POST", "/organizations/o/governance/tag-suggestions", url.Values{}, map[string]any{}, "", nil, "", true); err != nil {
		t.Fatalf("a suggestion waits by its nature: %v", err)
	}
}

// An answer paged inside a record is read to its end, and what was left undecided is said.
func TestAnAnswerInARecordIsReadWhole(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == "POST":
			fmt.Fprint(w, `{"outcomes":[{"id":"s1","outcome":"APPROVED"}],"remaining":240}`)
		case req.URL.Query().Get("cursor") == "":
			fmt.Fprint(w, `{"members":[{"member":{"email":"a@x"}}],"next":"m2","truncated":true}`)
		default:
			fmt.Fprint(w, `{"members":[{"member":{"email":"b@x"}}]}`)
		}
	}))
	defer srv.Close()
	var out, notes strings.Builder
	r := &Runtime{Ctx: context.Background(), Settings: &config.Settings{Source: map[string]string{}}, client: api.New(srv.URL, "test", time.Second),
		Out: &out, Err: &notes}
	gf.format = "json"
	defer func() { gf.format = "" }()
	if err := r.call("GET", "/organizations/o/governance/access/columns", url.Values{}, nil, "members", nil, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "a@x") || !strings.Contains(out.String(), "b@x") || !strings.Contains(notes.String(), "truncated") {
		t.Errorf("members: %s / %s", out.String(), notes.String())
	}
	if err := r.call("POST", "/organizations/o/governance/tag-suggestions/approve", url.Values{}, map[string]any{}, "outcomes", nil, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notes.String(), "240 more") {
		t.Errorf("what is left is not said: %s", notes.String())
	}
}

// apply sends only what the file says and what differs; a dry run of a file that makes a tag and uses it does not fail.
func TestApplyLeavesWhatTheFileDoesNotSay(t *testing.T) {
	org := "11111111-1111-1111-1111-111111111111"
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.Method != "GET" {
			body, _ := io.ReadAll(req.Body)
			writes = append(writes, req.Method+" "+req.URL.Path[strings.Index(req.URL.Path, "/governance"):]+" "+string(body))
			fmt.Fprint(w, `{"id":"x"}`)
			return
		}
		switch {
		case strings.HasSuffix(req.URL.Path, "/governance/tags"):
			fmt.Fprint(w, `{"items":[{"id":"t1","name":"PHI","key":"phi","description":"Health","active":false,"status":"ACTIVE"}],"hasMore":false}`)
		case strings.HasSuffix(req.URL.Path, "/governance/row-rules"):
			fmt.Fprint(w, `{"items":[{"id":"r1","name":"Own region","expression":"{col} = 1","enabled":true,"target":{"type":"tag","tagId":"t1"}}],"hasMore":false}`)
		case strings.Contains(req.URL.Path, "/governance/"):
			fmt.Fprint(w, `{"items":[],"hasMore":false}`)
		default:
			fmt.Fprintf(w, `{"id":%q,"name":"Org"}`, org)
		}
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "gov.yaml")
	if err := os.WriteFile(file, []byte(`
tags:
  - {name: PHI}
  - {name: Email, parent: PHI}
columnTags:
  - {catalogId: "7", schema: public, table: people, column: email, tag: Email}
rowRules:
  - {name: Own region, tag: PHI, expression: "{col} = 1"}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	previous, wasDry := rt, gf.dryRun
	defer func() { rt, gf.dryRun, gf.format = previous, wasDry, "" }()
	gf.format = "json"
	for _, dry := range []bool{true, false} {
		writes = nil
		var out strings.Builder
		client := api.New(srv.URL, "test", time.Second)
		if dry {
			client.DryRun = io.Discard
		}
		gf.dryRun = dry
		rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Org: org, Project: "p", Source: map[string]string{}}, client: client, Out: &out, Err: io.Discard}
		if err := applyGovernance(file); err != nil {
			t.Fatalf("dry %v: %v\n%s", dry, err, out.String())
		}
		if strings.Contains(out.String(), "failed") || strings.Count(out.String(), "unchanged") != 2 {
			t.Errorf("dry %v: PHI and the rule are as the file says, the rest is new: %s", dry, out.String())
		}
		for _, w := range writes {
			if strings.Contains(w, "/tags/t1") || strings.Contains(w, "/row-rules/r1") {
				t.Errorf("what is as the file says was written again: %s", w)
			}
		}
		if !dry && len(writes) != 2 {
			t.Errorf("writes: %v", writes)
		}
	}
}

// N02 (DEV-4250): the audit log shows who acted and how, an impersonator only when there is one, and offers every kind.
func TestAuditEventsShowActorKindAndImpersonator(t *testing.T) {
	events := []any{
		map[string]any{"occurredAt": "t1", "kind": "MQ_REFRESH_REQUESTED", "actor": "u1", "actorKind": "USER",
			"subject": map[string]any{"object": "sales"}, "after": map[string]any{"requestedBy": "SERVICE_ACCOUNT"}},
		map[string]any{"occurredAt": "t2", "kind": "TAG_APPLIED", "actor": "system", "actorKind": "SYSTEM", "impersonator": ""},
	}
	got := strings.Join(presentFields(events, auditFields), ",")
	if got != "occurredAt,kind,actor,actorKind,subject.tag,subject.table,subject.column,subject.object,after.requestedBy,reason" {
		t.Errorf("without an impersonator: %s", got)
	}
	events[1].(map[string]any)["impersonator"] = "admin@example.com"
	if got := presentFields(events, auditFields); got[4] != "impersonator" {
		t.Errorf("with an impersonator: %v", got)
	}
	if got := presentFields(nil, auditFields); strings.Contains(strings.Join(got, ","), "impersonator") || got[3] != "actorKind" {
		t.Errorf("an empty page: %v", got)
	}

	for _, k := range []string{"MQ_REFRESH_REQUESTED", "GROUP_MEMBER_ADDED", "GROUP_MEMBER_REMOVED", "MEMBERSHIP_DISCLOSURE_ACCEPTED",
		"ROW_RULE_UPDATED", "PUBLISHER_ACCESS_LAPSED", "GOVERNANCE_SETTINGS_CHANGED", "TAGGED_COLUMN_LOST", "TAG_SUGGESTION_WITHDRAWN",
		"CATALOG_SHARED", "CATALOG_UNSHARED", "GRANT_ADDED", "GRANT_CHANGED", "GRANT_REVOKED", "GROUP_CREATED", "GROUP_UPDATED",
		"GROUP_DELETED", "ATTRIBUTE_DEFINITION_CREATED", "ATTRIBUTE_DEFINITION_UPDATED", "DIRECTORY_MAPPING_CHANGED"} {
		found := false
		for _, a := range auditKinds {
			found = found || a == k
		}
		if !found {
			t.Errorf("%s is not a known kind", k)
		}
	}
	events2, _, err := newGovernanceCmd().Find([]string{"audit", "events"})
	if err != nil || !strings.Contains(events2.Long, "GROUP_MEMBER_REMOVED") || !strings.Contains(events2.Long, "SERVICE_ACCOUNT") {
		t.Fatalf("help lists the kinds and requestedBy: %v", err)
	}
	complete, _ := events2.GetFlagCompletionFunc("kind")
	if complete == nil {
		t.Fatal("--kind completes")
	}
	if got, _ := complete(events2, nil, "TAG_APPLIED,group_member_r"); len(got) != 1 || got[0] != "TAG_APPLIED,GROUP_MEMBER_REMOVED" {
		t.Errorf("the last item of the list completes: %v", got)
	}
	// K23: a catalog share's event shows the catalog and the project it was shared with in the table.
	shared := []any{map[string]any{"occurredAt": "t3", "kind": "CATALOG_SHARED", "actor": "u1", "actorKind": "USER",
		"subject": map[string]any{"objectKind": "CATALOG", "object": "Stripe", "targetProjectId": "p2"}}}
	if got := strings.Join(presentFields(shared, auditFields), ","); got != "occurredAt,kind,actor,actorKind,subject.tag,subject.table,subject.column,subject.object,subject.targetProjectId,reason" {
		t.Errorf("a catalog share: %s", got)
	}
	// K23: a catalog shared and unshared are kinds the help names, with how a share ended, and --kind completes them.
	if !strings.Contains(events2.Long, "CATALOG_UNSHARED") || !strings.Contains(events2.Long, "CATALOG_DELETED") {
		t.Errorf("help names the catalog share kinds and after.via")
	}
	if got, _ := complete(events2, nil, "catalog_"); len(got) != 2 || got[0] != "CATALOG_SHARED" || got[1] != "CATALOG_UNSHARED" {
		t.Errorf("--kind completes the catalog share kinds: %v", got)
	}
	// K15: grants, groups, attribute definitions and directory mappings are kinds the help names and --kind completes.
	if !strings.Contains(events2.Long, "DIRECTORY_MAPPING_CHANGED") || !strings.Contains(events2.Long, "before.roleId") {
		t.Errorf("help names the K15 kinds and a grant's role")
	}
	if got, _ := complete(events2, nil, "grant_"); len(got) != 3 || got[0] != "GRANT_ADDED" || got[2] != "GRANT_REVOKED" {
		t.Errorf("--kind completes the grant kinds: %v", got)
	}
}

// N02 (DEV-4250): the gateway routes the CLI had no command for: a mask read, a group changed, a grant given another
// role, a policy proposal read.
func TestCommandsForExistingRoutes(t *testing.T) {
	const o = "11111111-1111-1111-1111-111111111111"
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(req.Body)
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/organizations":
			fmt.Fprintf(w, `[{"id":%q,"name":"Org"}]`, o)
			return
		case req.Method == http.MethodGet && req.URL.Path == "/organizations/"+o+"/groups":
			fmt.Fprint(w, `{"items":[{"id":"g1","name":"Clinicians"}]}`)
			return
		case req.Method == http.MethodGet && req.URL.Path == "/organizations/"+o+"/governance/masks":
			fmt.Fprint(w, `{"items":[{"id":"m1","key":"last_four","name":"Last four"},{"id":"pm7","key":"show_last_four","name":"Show last four","predefined":true}]}`)
			return
		case req.Method == http.MethodGet && req.URL.Path == "/organizations/"+o+"/workspaces":
			fmt.Fprint(w, `[{"id":"w1","name":"Lab"}]`)
			return
		case req.Method == http.MethodGet && req.URL.Path == "/organizations/"+o+"/workspaces/w1/projects":
			fmt.Fprint(w, `[{"id":"AbCdEf12","name":"Clinic"}]`)
			return
		}
		sent = append(sent, strings.TrimSpace(req.Method+" "+req.URL.Path+" "+string(body)))
		fmt.Fprint(w, `{"id":"x"}`)
	}))
	defer srv.Close()
	previous := rt
	defer func() { rt, gf.format = previous, "" }()
	gf.format = "json"
	rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Org: o, Project: "Clinic", Source: map[string]string{}},
		client: api.New(srv.URL, "test", time.Second), Out: io.Discard, Err: io.Discard}

	for _, c := range []struct {
		cmd  func() *cobra.Command
		args []string
		want string
	}{
		{newGovernanceCmd, []string{"masks", "describe", "Last four"}, "GET /organizations/" + o + "/governance/masks/m1"},
		{newGovernanceCmd, []string{"masks", "describe", "show_last_four"}, "GET /organizations/" + o + "/governance/masks/pm7"},
		{newGovernanceCmd, []string{"policy", "proposal", "pr1"}, "GET /organizations/" + o + "/governance/policy/proposals/pr1"},
		{newGroupsCmd, []string{"update", "Clinicians", "--description", "Care team"}, "PATCH /organizations/" + o + `/groups/g1 {"description":"Care team"}`},
		{newGrantsCmd, []string{"update", "gr1", "--role", "r2"}, "PATCH /organizations/" + o + `/workspaces/w1/projects/AbCdEf12/grants/gr1 {"roleId":"r2"}`},
		{newGrantsCmd, []string{"update", "gr1", "--workspace", "Lab", "--role", "r2"}, "PATCH /organizations/" + o + `/workspaces/w1/grants/gr1 {"roleId":"r2"}`},
	} {
		sent = nil
		cmd := c.cmd()
		cmd.SetArgs(c.args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Errorf("%v: %v", c.args, err)
			continue
		}
		if len(sent) != 1 || sent[0] != c.want {
			t.Errorf("%v sent %q, want %q", c.args, sent, c.want)
		}
	}
	grants := newGrantsCmd()
	grants.SetArgs([]string{"update", "gr1"})
	grants.SetOut(io.Discard)
	grants.SetErr(io.Discard)
	if err := grants.Execute(); err == nil {
		t.Error("grants update needs --role")
	}
}

// The gateway's DEV-4250 additions reach the person: a publication request, an audit event's impersonator, a column
// tag's status and a materialized query's readsLive.
func TestGatewayAdditionsAreShown(t *testing.T) {
	const o = "11111111-1111-1111-1111-111111111111"
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(req.Body)
		switch {
		case req.URL.Path == "/organizations":
			fmt.Fprintf(w, `[{"id":%q,"name":"Org"}]`, o)
		case req.URL.Path == "/data/projects/AbCdEf12/queries":
			fmt.Fprint(w, `[{"id":"q1","displayName":"ward_counts","queryType":"MATERIALIZED"}]`)
		case strings.HasSuffix(req.URL.Path, "/audit-events"):
			fmt.Fprint(w, `{"items":[{"occurredAt":"t1","kind":"TAG_APPLIED","actor":"ana","actorKind":"USER","impersonator":"support@peaka.com"}],"hasMore":false}`)
		case strings.HasSuffix(req.URL.Path, "/column-tags"):
			fmt.Fprint(w, `{"items":[{"id":"ct1","column":"ward","tagId":"t1","origin":"MANUAL","status":"LOST","ineffective":true}],"hasMore":false}`)
		case strings.HasSuffix(req.URL.Path, "/materialized-queries/q1/status"):
			fmt.Fprint(w, `{"queryId":"q1","queryName":"ward_counts","status":"SUCCEEDED","readsLive":true}`)
		default:
			sent = append(sent, req.Method+" "+req.URL.Path+" "+string(body))
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"pr1","status":"PENDING"}`)
		}
	}))
	defer srv.Close()
	previous := rt
	defer func() { rt, gf.format = previous, "" }()
	run := func(format string, cmd *cobra.Command, args ...string) string {
		gf.format = format
		var out strings.Builder
		rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Org: o, Project: "AbCdEf12", Format: format, Source: map[string]string{}},
			client: api.New(srv.URL, "test", time.Second), Out: &out, Err: io.Discard}
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out.String()
	}

	run("json", newGovernanceCmd(), "publications", "request", "ward_counts", "--reason", "clinic dashboard")
	if len(sent) != 1 || sent[0] != "POST /organizations/"+o+`/governance/publication-requests {"objectId":"q1","objectKind":"MATERIALIZED_QUERY","objectName":"ward_counts","projectId":"AbCdEf12","reason":"clinic dashboard"}` {
		t.Errorf("publication request: %q", sent)
	}
	if out := run("table", newGovernanceCmd(), "audit", "events"); !strings.Contains(out, "support@peaka.com") {
		t.Errorf("the impersonator is a column: %s", out)
	}
	if out := run("table", newGovernanceCmd(), "column-tags", "list", "7", "public", "people"); !strings.Contains(out, "LOST") || !strings.Contains(out, "STATUS") {
		t.Errorf("a column tag's status is a column: %s", out)
	}
	if out := run("table", newQueriesCmd(), "status", "ward_counts"); !strings.Contains(strings.ToLower(out), "readslive") {
		t.Errorf("readsLive is shown: %s", out)
	}
}
