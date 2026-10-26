package trino

import "strings"

// numberType is Trino's arbitrary-precision decimal (480+), e.g. a plain Oracle NUMBER.
const numberType = "number"

// plainNumbers rewrites the number columns of rows in plain notation. Trino keeps a number without
// trailing zeros and prints it that way, so 20000 arrives as "2E+4"; nobody reading a result wants that.
func plainNumbers(rows [][]any, columns []Column) {
	for i, column := range columns {
		if column.Type != numberType {
			continue
		}
		for _, row := range rows {
			if i < len(row) {
				if text, ok := row[i].(string); ok {
					row[i] = plainDecimal(text)
				}
			}
		}
	}
}

// plainDecimal writes a decimal in scientific notation ("1.2E+3", "1E-7") without its exponent.
// Anything else, NaN and the infinities included, is returned as it is.
func plainDecimal(text string) string {
	e := strings.IndexAny(text, "eE")
	if e < 0 {
		return text
	}
	mantissa, exponentText := text[:e], text[e+1:]
	exponent := 0
	negativeExponent := false
	switch {
	case strings.HasPrefix(exponentText, "+"):
		exponentText = exponentText[1:]
	case strings.HasPrefix(exponentText, "-"):
		exponentText = exponentText[1:]
		negativeExponent = true
	}
	if exponentText == "" {
		return text
	}
	for _, c := range exponentText {
		if c < '0' || c > '9' {
			return text
		}
		exponent = exponent*10 + int(c-'0')
	}
	if negativeExponent {
		exponent = -exponent
	}

	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign, mantissa = "-", mantissa[1:]
	}
	integer, fraction, _ := strings.Cut(mantissa, ".")
	digits := integer + fraction
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return text
	}
	// the decimal point sits after len(integer) digits; the exponent moves it
	point := len(integer) + exponent
	switch {
	case point <= 0:
		digits = "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		digits = digits + strings.Repeat("0", point-len(digits))
	default:
		digits = digits[:point] + "." + digits[point:]
	}
	if whole, _, found := strings.Cut(digits, "."); !found || whole != "" {
		digits = strings.TrimLeft(digits, "0")
		if digits == "" || strings.HasPrefix(digits, ".") {
			digits = "0" + digits
		}
	}
	return sign + digits
}
