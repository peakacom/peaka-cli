package cli

import (
	"strings"
	"testing"
)

// Codex review round 5, R5-6: a label or name that is two records is refused before anything is sent.
func TestTwiceNamesAmbiguousRecords(t *testing.T) {
	idx := index([]record{
		{"tag": map[string]any{"id": "a", "label": "PHI"}},
		{"tag": map[string]any{"id": "b", "label": "phi"}},
		{"tag": map[string]any{"id": "c", "label": "PII"}},
	}, func(x record) (string, string) {
		tag, _ := x["tag"].(map[string]any)
		return strings.ToUpper(str(tag["label"])), str(tag["id"])
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
	onCustomers := record{"targetType": "TABLE", "table": "peaka.table.customers", "projectId": "p1"}
	onRegion := record{"targetType": "TAG", "tagId": "t-region"}
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
