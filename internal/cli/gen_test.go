package cli

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSplitPath(t *testing.T) {
	cases := map[string][]string{
		"a.b.c":                   {"a", "b", "c"},
		`peaka."table".customers`: {"peaka", "table", "customers"},
		`a."b.c".d`:               {"a", "b.c", "d"},
		"single":                  {"single"},
		`"only.quoted"`:           {"only.quoted"},
	}
	for in, want := range cases {
		if got := splitPath(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCamel(t *testing.T) {
	for in, want := range map[string]string{"name": "name", "to-project": "toProject", "full-refresh-schedule": "fullRefreshSchedule", "use-with-ai": "useWithAi"} {
		if got := camel(in); got != want {
			t.Errorf("camel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScheduleSetter(t *testing.T) {
	b := map[string]any{}
	scheduleSetter("fullRefreshCacheSchedule")("0 3 * * *", b)
	scheduleSetter("incrementalCacheSchedule")("", b)
	want := map[string]any{
		"fullRefreshCacheSchedule": map[string]any{"type": "BASIC", "expression": "0 3 * * *"},
		"incrementalCacheSchedule": map[string]any{"type": "NONE"},
	}
	if !reflect.DeepEqual(b, want) {
		t.Errorf("schedules = %v", b)
	}
}

func TestColumnType(t *testing.T) {
	for in, want := range map[string]string{"text": "VARCHAR", "int": "BIGINT", "Integer": "BIGINT", "bool": "BOOLEAN", "double": "DECIMAL", "date": "DATE", "json": "JSON", "uuid": "UUID", "datetime": "TIMESTAMP"} {
		if got := columnType(in); got != want {
			t.Errorf("columnType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCredentialSetterAndCoercion(t *testing.T) {
	body := map[string]any{}
	credentialSetter([]string{"host=db", "port=5432", "useSsl=true", `options:={"a":1}`, "n:=3"}, body)
	cred := body["credential"].(map[string]any)
	cfg := []any{
		map[string]any{"fieldName": "port", "fieldType": "number"},
		map[string]any{"fieldName": "useSsl", "fieldType": "boolean"},
		map[string]any{"fieldName": "host", "fieldType": "text"},
	}
	coerceCredential(cred, cfg)
	if cred["host"] != "db" {
		t.Errorf("host = %v", cred["host"])
	}
	if n, ok := cred["port"].(json.Number); !ok || n.String() != "5432" {
		t.Errorf("port = %#v", cred["port"])
	}
	if cred["useSsl"] != true {
		t.Errorf("useSsl = %#v", cred["useSsl"])
	}
	if m, ok := cred["options"].(map[string]any); !ok || m["a"] != float64(1) {
		t.Errorf("options = %#v", cred["options"])
	}
	if cred["n"] != float64(3) {
		t.Errorf("n = %#v", cred["n"])
	}
}

func TestOpSpecArgsAndFlags(t *testing.T) {
	spec := opSpec{Use: "add ID QUERY...", Method: "PUT", Path: "/x/{project}/{folder}",
		Args:  []argSpec{{Name: "ID", Param: "folder"}},
		Flags: []flagSpec{{Name: "limit", Kind: "int", In: "query"}, {Name: "to", Key: "path"}, {Name: "tags", Kind: "strings"}},
		Body:  true}
	cmd := spec.command()
	if cmd.Flags().Lookup("from-file") == nil || cmd.Flags().Lookup("field") == nil {
		t.Fatal("body specs get --from-file and -f")
	}
	if cmd.Flags().Lookup("limit") == nil || cmd.Flags().Lookup("tags") == nil {
		t.Fatal("flags missing")
	}
	if err := cmd.Args(cmd, []string{}); err == nil {
		t.Error("at least one arg is required")
	}
	if err := cmd.Args(cmd, []string{"f", "q1", "q2"}); err != nil {
		t.Errorf("variadic args: %v", err)
	}
}
