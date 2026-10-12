// Package sqltext has the text-level SQL helpers the CLI needs before a full
// parser: splitting a script into statements and deciding whether input is
// complete. It understands 'strings', "quoted identifiers", -- and /* */
// comments, so a ';' inside any of them does not end a statement.
package sqltext

import "strings"

// Statement is one statement of a script with the line it starts on (1-based).
type Statement struct {
	Text string
	Line int
}

// Split cuts text into statements on ';' outside quotes and comments. The
// terminating ';' is dropped; empty statements are skipped.
func Split(text string) []Statement {
	var out []Statement
	var cur strings.Builder
	line, startLine := 1, 1
	started := false
	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s != "" {
			out = append(out, Statement{Text: s, Line: startLine})
		}
		cur.Reset()
		started = false
	}
	rs := []rune(text)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if !started && !isSpace(r) {
			started, startLine = true, line
		}
		switch {
		case r == '\n':
			line++
			cur.WriteRune(r)
		case r == '\'' || r == '"' || r == '`':
			j := closingQuote(rs, i, r)
			for k := i; k <= j && k < len(rs); k++ {
				if rs[k] == '\n' {
					line++
				}
				cur.WriteRune(rs[k])
			}
			i = j
		case r == '-' && i+1 < len(rs) && rs[i+1] == '-':
			j := i
			for j < len(rs) && rs[j] != '\n' {
				cur.WriteRune(rs[j])
				j++
			}
			i = j - 1
		case r == '/' && i+1 < len(rs) && rs[i+1] == '*':
			j := i + 2
			for j+1 < len(rs) && !(rs[j] == '*' && rs[j+1] == '/') {
				j++
			}
			end := j + 1
			if end >= len(rs) {
				end = len(rs) - 1
			}
			for k := i; k <= end; k++ {
				if rs[k] == '\n' {
					line++
				}
				cur.WriteRune(rs[k])
			}
			i = end
		case r == ';':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// Complete reports whether text ends a statement: non-blank, not inside a
// quote or comment, and ending with ';'. The REPL runs on Enter when true.
func Complete(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if !strings.HasSuffix(trimmed, ";") {
		return false
	}
	return !Open(text)
}

// Open reports whether text ends inside a quote or a block comment.
func Open(text string) bool {
	rs := []rune(text)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\'' || r == '"' || r == '`':
			j := closingQuote(rs, i, r)
			if j >= len(rs) {
				return true
			}
			i = j
		case r == '-' && i+1 < len(rs) && rs[i+1] == '-':
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		case r == '/' && i+1 < len(rs) && rs[i+1] == '*':
			j := i + 2
			for j+1 < len(rs) && !(rs[j] == '*' && rs[j+1] == '/') {
				j++
			}
			if j+1 >= len(rs) {
				return true
			}
			i = j + 1
		}
	}
	return false
}

// closingQuote returns the index of the quote closing the one at i (doubled
// quotes escape), or len(rs) when it is not closed.
func closingQuote(rs []rune, i int, q rune) int {
	for j := i + 1; j < len(rs); j++ {
		if rs[j] == q {
			if j+1 < len(rs) && rs[j+1] == q {
				j++
				continue
			}
			return j
		}
	}
	return len(rs)
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }
