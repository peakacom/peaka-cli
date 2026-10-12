package cli

import "testing"

func TestParseSheetFlag(t *testing.T) {
	cases := map[string]pickedFile{
		"1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms":                                                         {ID: "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms", Name: "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms"},
		"1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms=budget":                                                  {ID: "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms", Name: "budget"},
		"https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms/edit#gid=0":       {ID: "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms", Name: "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms"},
		"https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms/edit?usp=sharing": {ID: "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms", Name: "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms"},
	}
	for in, want := range cases {
		got, err := parseSheetFlag(in)
		if err != nil || got != want {
			t.Errorf("parseSheetFlag(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "not a sheet id", "https://example.com/x"} {
		if _, err := parseSheetFlag(bad); err == nil {
			t.Errorf("parseSheetFlag(%q) accepted", bad)
		}
	}
}

func TestSheetsSetterFillsExtraParameters(t *testing.T) {
	body := map[string]any{"extraParameters": map[string]any{"other": 1}}
	sheetsSetter([]string{"abc=one", "def"}, body)
	extra := body["extraParameters"].(map[string]any)
	files := extra["sheets"].([]pickedFile)
	if len(files) != 2 || files[0].Name != "one" || files[1].ID != "def" || extra["other"] != 1 {
		t.Fatalf("got %+v", extra)
	}
}
