package trino

import "testing"

func TestPlainDecimal(t *testing.T) {
	cases := map[string]string{
		"2E+4":      "20000",
		"1.2E+3":    "1200",
		"1E-7":      "0.0000001",
		"1.23E-40":  "0.000000000000000000000000000000000000000123",
		"-5.25E+1":  "-52.5",
		"-1E-2":     "-0.01",
		"1.5E+1":    "15",
		"12.5E-1":   "1.25",
		"1000.5":    "1000.5",
		"0.000001":  "0.000001",
		"7":         "7",
		"NaN":       "NaN",
		"Infinity":  "Infinity",
		"-Infinity": "-Infinity",
		"1E+":       "1E+",
		"abc":       "abc",
	}
	for in, want := range cases {
		if got := plainDecimal(in); got != want {
			t.Errorf("plainDecimal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlainNumbersTouchesOnlyNumberColumns(t *testing.T) {
	rows := [][]any{{"2E+4", "2E+4", nil}}
	plainNumbers(rows, []Column{{Name: "n", Type: "number"}, {Name: "s", Type: "varchar"}, {Name: "m", Type: "number"}})
	if rows[0][0] != "20000" || rows[0][1] != "2E+4" || rows[0][2] != nil {
		t.Errorf("got %v", rows[0])
	}
}
