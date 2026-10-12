package sqltext

import "testing"

func TestSplit(t *testing.T) {
	script := `-- header; not a split
SELECT 'a;b' AS s, "we;ird" AS c;
/* block ; comment */
SELECT 1
;

SELECT 'it''s';`
	got := Split(script)
	if len(got) != 3 {
		t.Fatalf("want 3 statements, got %d: %+v", len(got), got)
	}
	if got[0].Text != "-- header; not a split\nSELECT 'a;b' AS s, \"we;ird\" AS c" || got[0].Line != 1 {
		t.Errorf("first: %+v", got[0])
	}
	if got[1].Text != "/* block ; comment */\nSELECT 1" || got[1].Line != 3 {
		t.Errorf("second: %+v", got[1])
	}
	if got[2].Text != "SELECT 'it''s'" || got[2].Line != 7 {
		t.Errorf("third: %+v", got[2])
	}
	if n := len(Split("  ;; \n")); n != 0 {
		t.Errorf("empty statements kept: %d", n)
	}
}

func TestComplete(t *testing.T) {
	cases := map[string]bool{
		"SELECT 1;":               true,
		"SELECT 1":                false,
		"SELECT 'a;":              false,
		"SELECT 'a';":             true,
		"SELECT 1; -- trailing":   false,
		"SELECT /* open ; ":       false,
		"SELECT /* closed */ 1;":  true,
		"   ":                     false,
		"SELECT \"x;\n\" FROM t;": true,
		"SELECT 'it''s' FROM t;":  true,
	}
	for in, want := range cases {
		if got := Complete(in); got != want {
			t.Errorf("Complete(%q) = %v, want %v", in, got, want)
		}
	}
}
