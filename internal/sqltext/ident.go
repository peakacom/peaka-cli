package sqltext

import (
	"regexp"
	"strings"
)

// Identifiers as Trino reads them: an unquoted identifier is lower-case
// letters, digits and underscores, not starting with a digit, and not a
// keyword; anything else ("evliya_çelebi", "2__kitap", "table", "MyTable")
// must be written in double quotes, a quote inside doubled.

var plainIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// NeedsQuote says whether an identifier must be double-quoted in SQL.
func NeedsQuote(name string) bool {
	return !plainIdent.MatchString(name) || IsKeyword(name)
}

// QuoteIdent writes an identifier the way SQL needs it: bare when it can be,
// double-quoted otherwise (or when forced).
func QuoteIdent(name string, force bool) string {
	if !force && !NeedsQuote(name) {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Unquote strips the double quotes of an identifier and undoubles inner
// quotes; an unterminated quote (still being typed) is stripped too.
func Unquote(part string) string {
	if !strings.HasPrefix(part, `"`) {
		return part
	}
	var b strings.Builder
	rs := []rune(part[1:])
	for i := 0; i < len(rs); i++ {
		if rs[i] == '"' {
			if i+1 < len(rs) && rs[i+1] == '"' {
				b.WriteRune('"')
				i++
				continue
			}
			break // the closing quote
		}
		b.WriteRune(rs[i])
	}
	return b.String()
}

// Part is one dotted part of a path as typed: its bare name and whether it
// was written in quotes (an opening quote without its closing one counts).
type Part struct {
	Name   string
	Quoted bool
}

// SplitIdents splits a dotted path at the dots outside quotes:
// peaka."table".cust → peaka, table (quoted), cust. A trailing dot yields a
// final empty part, so the caller knows a new part was begun.
func SplitIdents(path string) []Part {
	var parts []Part
	var cur strings.Builder
	quoted, inQuote := false, false
	rs := []rune(path)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '"':
			if inQuote && i+1 < len(rs) && rs[i+1] == '"' {
				cur.WriteRune('"')
				i++
				continue
			}
			inQuote = !inQuote
			quoted = true
		case r == '.' && !inQuote:
			parts = append(parts, Part{Name: cur.String(), Quoted: quoted})
			cur.Reset()
			quoted = false
		default:
			cur.WriteRune(r)
		}
	}
	parts = append(parts, Part{Name: cur.String(), Quoted: quoted})
	return parts
}

// JoinIdents writes parts back as a path, each quoted as it needs (or as it
// was typed).
func JoinIdents(parts []Part) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = QuoteIdent(p.Name, p.Quoted)
	}
	return strings.Join(out, ".")
}
