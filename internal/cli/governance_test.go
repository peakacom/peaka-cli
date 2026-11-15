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
