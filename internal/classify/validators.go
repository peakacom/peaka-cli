package classify

import (
	"net"
	"regexp"
	"sort"
	"strings"
)

// Validators are the checks a template rule of type "validator" can name. Each one answers whether a single
// value is well-formed for that identifier; the rule decides on the fraction of a sample that passes.
var validators = map[string]func(string) bool{
	"luhn":       luhn,
	"iban_mod97": ibanMod97,
	"email":      isEmail,
	"ip":         isIP,
	"uuid":       isUUID,
	"e164_phone": isPhone,
	"us_ssn":     isUSSSN,
	"tr_tckn":    isTCKN,
	"uk_nhs":     isUKNHS,
}

// ValidatorIDs lists the validators a template can use, sorted.
func ValidatorIDs() []string {
	ids := make([]string, 0, len(validators))
	for id := range validators {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

var (
	emailRe = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}$`)
	uuidRe  = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	phoneRe = regexp.MustCompile(`^\+?[0-9 ()-]{7,24}$`) // no dot: 12345.67 is an amount
	ssnRe   = regexp.MustCompile(`^(\d{3})-?(\d{2})-?(\d{4})$`)
)

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// luhn is the checksum of payment card numbers (and US NPIs, after their 80840 prefix).
func luhn(s string) bool {
	d := digitsOf(s)
	if len(d) < 8 || len(d) > 19 || len(d) != len(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), " ", ""), "-", "")) {
		return false
	}
	sum, double := 0, false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum%10 == 0
}

// ibanMod97 checks an IBAN: country code, two check digits, then mod-97 of the rearranged number equals 1.
func ibanMod97(s string) bool {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	if len(s) < 15 || len(s) > 34 {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	if !(s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z' && s[2] >= '0' && s[2] <= '9' && s[3] >= '0' && s[3] <= '9') {
		return false
	}
	r := s[4:] + s[:4]
	rem := 0
	for _, c := range r {
		if c >= 'A' && c <= 'Z' {
			n := int(c-'A') + 10
			rem = (rem*100 + n) % 97
		} else {
			rem = (rem*10 + int(c-'0')) % 97
		}
	}
	return rem == 1
}

func isEmail(s string) bool { return emailRe.MatchString(strings.TrimSpace(s)) }

func isIP(s string) bool { return net.ParseIP(strings.TrimSpace(s)) != nil }

func isUUID(s string) bool { return uuidRe.MatchString(strings.TrimSpace(s)) }

// isPhone accepts international and national phone layouts: 8–15 digits with optional +, spaces, dashes, parentheses.
func isPhone(s string) bool {
	s = strings.TrimSpace(s)
	if !phoneRe.MatchString(s) || dateLikeRe.MatchString(s) { // 2024-01-05 is a date
		return false
	}
	n := len(digitsOf(s))
	return n >= 8 && n <= 15
}

// isUSSSN applies the SSA's structural rules: no 000/666/9xx area, no 00 group, no 0000 serial.
func isUSSSN(s string) bool {
	m := ssnRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return false
	}
	area, group, serial := m[1], m[2], m[3]
	if area == "000" || area == "666" || area[0] == '9' || group == "00" || serial == "0000" {
		return false
	}
	return true
}

// isTCKN checks a Turkish national identity number: 11 digits, first non-zero, two checksum digits.
func isTCKN(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 11 || s[0] == '0' {
		return false
	}
	d := make([]int, 11)
	for i, c := range s {
		if c < '0' || c > '9' {
			return false
		}
		d[i] = int(c - '0')
	}
	odd := d[0] + d[2] + d[4] + d[6] + d[8]
	even := d[1] + d[3] + d[5] + d[7]
	if (odd*7-even+10)%10 != d[9] {
		return false
	}
	sum := 0
	for i := 0; i < 10; i++ {
		sum += d[i]
	}
	return sum%10 == d[10]
}

// isUKNHS checks an NHS number: 10 digits with a mod-11 check digit.
func isUKNHS(s string) bool {
	d := digitsOf(s)
	if len(d) != 10 || len(d) != len(strings.ReplaceAll(strings.TrimSpace(s), " ", "")) {
		return false
	}
	sum := 0
	for i := 0; i < 9; i++ {
		sum += int(d[i]-'0') * (10 - i)
	}
	check := 11 - sum%11
	if check == 11 {
		check = 0
	}
	if check == 10 {
		return false
	}
	return check == int(d[9]-'0')
}
