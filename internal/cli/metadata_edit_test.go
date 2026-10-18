package cli

import "testing"

func TestMetadataDescriptionEditKeepsVersionAndInheritance(t *testing.T) {
	body := map[string]any{"tableDescription": "new"}
	current := map[string]any{"version": float64(12), "useWithAI": false, "useWithAISetting": "inherit", "columns": []any{"do not copy"}}
	if err := prepareMetadataEdit(body, current); err != nil {
		t.Fatal(err)
	}
	if body["version"] != float64(12) || body["useWithAISetting"] != "inherit" || body["useWithAI"] != false {
		t.Fatal(body)
	}
	if len(body["columns"].([]any)) != 0 {
		t.Fatal("unrequested columns copied")
	}
}
func TestMetadataExplicitAiChangeDoesNotCopyOldSetting(t *testing.T) {
	body := map[string]any{"useWithAI": true, "version": float64(9)}
	if err := prepareMetadataEdit(body, map[string]any{"version": float64(12), "useWithAISetting": "off"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["useWithAISetting"]; ok {
		t.Fatal("old setting defeats requested toggle")
	}
	if body["version"] != float64(9) {
		t.Fatal("stale caller revision replaced")
	}
}
func TestMetadataEditFailsClosedWithoutVersion(t *testing.T) {
	for _, current := range []map[string]any{nil, {}, {"version": nil}} {
		if err := prepareMetadataEdit(map[string]any{}, current); err == nil {
			t.Fatal("unversioned write allowed")
		}
	}
	if err := prepareMetadataEdit(map[string]any{"version": nil}, map[string]any{"version": float64(1)}); err == nil {
		t.Fatal("null bypass allowed")
	}
}
