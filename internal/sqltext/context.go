package sqltext

import "strings"

// TableRef is a table named in a statement, with its alias when given.
type TableRef struct {
	Path  string // as written, e.g. stripe.payment.charges or charges
	Alias string
}

// Place says what the cursor is positioned to complete.
type Place int

const (
	PlaceStatement Place = iota // start of a statement: keywords
	PlaceTable                  // after FROM/JOIN/INTO/...: a table path
	PlaceSchema                 // after USE / SHOW TABLES FROM: a schema path
	PlaceColumn                 // in SELECT list, WHERE, ON, ...: a column or expression
)

// Context is what the completer needs to know about the text before the cursor.
type Context struct {
	Word     string // the dotted word being typed, "" when at a boundary
	WordPos  int    // rune offset where Word starts
	Prev     string // previous significant token, upper-cased
	Place    Place
	Tables   []TableRef // tables of the statement the cursor is in
	Meta     bool       // the line is a \command
	MetaArgs []string   // words of the meta line before the cursor, the first being the command
}

var tableIntroducers = map[string]bool{"FROM": true, "JOIN": true, "INTO": true, "UPDATE": true, "TABLE": true, "DESCRIBE": true, "DESC": true}
var schemaIntroducers = map[string]bool{"USE": true, "SCHEMA": true}

// Analyze computes the completion context for the text up to the cursor.
func Analyze(text string, cursor int) Context {
	rs := []rune(text)
	if cursor > len(rs) {
		cursor = len(rs)
	}
	before := string(rs[:cursor])
	c := Context{}
	if t := strings.TrimLeft(before, " \t"); strings.HasPrefix(t, "\\") {
		c.Meta = true
		c.MetaArgs = strings.Fields(t)
		if strings.HasSuffix(before, " ") || strings.HasSuffix(before, "\t") {
			c.MetaArgs = append(c.MetaArgs, "")
		}
		if len(c.MetaArgs) > 0 {
			c.Word = c.MetaArgs[len(c.MetaArgs)-1]
		}
		c.WordPos = cursor - len([]rune(c.Word))
		return c
	}

	// The statement the cursor is in: text after the last ';' before the cursor.
	stmtStart := 0
	for _, s := range Split(before) {
		_ = s
	}
	if i := lastStatementStart(before); i >= 0 {
		stmtStart = i
	}
	stmt := string(rs[stmtStart:cursor])
	toks := Lex(stmt)

	// Current word: a trailing Ident/Keyword/QuotedIdent run joined by dots.
	wordStart := len([]rune(stmt))
	j := len(toks) - 1
	for j >= 0 {
		t := toks[j]
		if t.Kind == Ident || t.Kind == Keyword || t.Kind == QuotedIdent || t.Text == "." {
			wordStart = t.Start
			j--
			continue
		}
		break
	}
	c.Word = string([]rune(stmt)[wordStart:])
	c.WordPos = stmtStart + wordStart
	// Previous significant token before the word.
	for k := j; k >= 0; k-- {
		if toks[k].Kind == Space || toks[k].Kind == Comment {
			continue
		}
		c.Prev = strings.ToUpper(toks[k].Text)
		break
	}
	c.Tables = TablesIn(stmt)
	c.Place = place(toks, j)
	return c
}

// lastStatementStart returns the rune offset after the last ';' that ends a
// statement before the end of text, or -1.
func lastStatementStart(text string) int {
	rs := []rune(text)
	last := -1
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\'', '"', '`':
			i = closingQuote(rs, i, rs[i])
		case '-':
			if i+1 < len(rs) && rs[i+1] == '-' {
				for i < len(rs) && rs[i] != '\n' {
					i++
				}
			}
		case '/':
			if i+1 < len(rs) && rs[i+1] == '*' {
				j := i + 2
				for j+1 < len(rs) && !(rs[j] == '*' && rs[j+1] == '/') {
					j++
				}
				i = j + 1
			}
		case ';':
			last = i + 1
		}
	}
	return last
}

// place decides what kind of thing follows the token at index j (the last one
// before the word being typed).
func place(toks []Token, j int) Place {
	// Walk back to the last keyword that sets the clause.
	depth := 0
	for k := j; k >= 0; k-- {
		t := toks[k]
		switch {
		case t.Text == ")":
			depth++
		case t.Text == "(":
			if depth > 0 {
				depth--
			}
		case t.Kind == Keyword && depth == 0:
			up := strings.ToUpper(t.Text)
			if tableIntroducers[up] {
				// FROM t1, t2: still a table place after a comma; after a complete
				// table reference followed by anything else, we are past it.
				if k == j || onlyPathSince(toks, k, j) {
					return PlaceTable
				}
				return PlaceColumn
			}
			if schemaIntroducers[up] {
				return PlaceSchema
			}
			switch up {
			case "SELECT", "WHERE", "ON", "BY", "HAVING", "AND", "OR", "SET", "WHEN", "THEN", "ELSE", "CASE", "DISTINCT",
				"AS", "IN", "NOT", "BETWEEN", "LIKE", "OVER", "PARTITION", "VALUES", "LIMIT", "OFFSET", "USING":
				return PlaceColumn
			}
		}
		if k == 0 {
			break
		}
	}
	// No clause keyword before the cursor: start of a statement.
	for k := j; k >= 0; k-- {
		if toks[k].Kind != Space && toks[k].Kind != Comment {
			return PlaceColumn
		}
	}
	return PlaceStatement
}

// onlyPathSince reports whether tokens between from (exclusive) and to (inclusive)
// form table references separated by commas, i.e. the cursor is still in the FROM list.
func onlyPathSince(toks []Token, from, to int) bool {
	havePath, haveAlias, afterDot := false, false, false
	for k := from + 1; k <= to; k++ {
		t := toks[k]
		switch {
		case t.Kind == Space || t.Kind == Comment:
		case t.Text == ",":
			havePath, haveAlias, afterDot = false, false, false
		case t.Text == ".":
			afterDot = true
		case t.Kind == Ident || t.Kind == QuotedIdent:
			switch {
			case afterDot:
				afterDot = false // another segment of the path
			case !havePath:
				havePath = true
			case !haveAlias:
				haveAlias = true
			default:
				return false
			}
		case t.Kind == Keyword && strings.ToUpper(t.Text) == "AS":
		default:
			return false
		}
	}
	// A complete path (with or without alias) sits before the cursor: what comes next is not a table.
	return !havePath
}

// TablesIn finds the table references of a statement with their aliases.
func TablesIn(stmt string) []TableRef {
	toks := Lex(stmt)
	var out []TableRef
	sig := func(k int) int { // next significant token index from k
		for k < len(toks) && (toks[k].Kind == Space || toks[k].Kind == Comment) {
			k++
		}
		return k
	}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.Kind != Keyword || !tableIntroducers[strings.ToUpper(t.Text)] || strings.ToUpper(t.Text) == "TABLE" {
			continue
		}
		k := i + 1
		for {
			k = sig(k)
			if k >= len(toks) {
				break
			}
			if toks[k].Text == "(" {
				// a subquery or lateral: skip to the matching ')' and its alias, stay in the list
				depth := 0
				for ; k < len(toks); k++ {
					if toks[k].Text == "(" {
						depth++
					} else if toks[k].Text == ")" {
						depth--
						if depth == 0 {
							k++
							break
						}
					}
				}
				k = sig(k)
				if k < len(toks) && toks[k].Kind == Keyword && strings.ToUpper(toks[k].Text) == "AS" {
					k = sig(k + 1)
				}
				if k < len(toks) && toks[k].Kind == Ident {
					k++
				}
				k = sig(k)
				if k < len(toks) && toks[k].Text == "," {
					k++
					continue
				}
				break
			}
			// path: ident(.ident)*
			var path []string
			for k < len(toks) && (toks[k].Kind == Ident || toks[k].Kind == QuotedIdent || toks[k].Kind == Keyword) {
				path = append(path, toks[k].Text)
				if k+1 < len(toks) && toks[k+1].Text == "." {
					k += 2
					continue
				}
				k++
				break
			}
			if len(path) == 0 {
				break
			}
			ref := TableRef{Path: strings.Join(path, ".")}
			k = sig(k)
			if k < len(toks) && toks[k].Kind == Keyword && strings.ToUpper(toks[k].Text) == "AS" {
				k = sig(k + 1)
			}
			if k < len(toks) && (toks[k].Kind == Ident || toks[k].Kind == QuotedIdent) {
				ref.Alias = toks[k].Text
				k++
			}
			out = append(out, ref)
			k = sig(k)
			if k < len(toks) && toks[k].Text == "," {
				k++
				continue
			}
			break
		}
	}
	return out
}
