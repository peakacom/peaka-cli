package sqltext

import (
	"strings"
	"unicode"
)

// Kind classifies a token.
type Kind int

const (
	Space Kind = iota
	Keyword
	Ident
	QuotedIdent
	String
	Number
	Comment
	Operator
	Punct
	Meta // a \command line
)

// Token is one lexical unit with rune offsets into the text.
type Token struct {
	Kind  Kind
	Text  string
	Start int
	End   int
}

// Lex tokenizes SQL text. It never fails: unknown characters become Punct,
// an unterminated string or comment runs to the end.
func Lex(text string) []Token {
	rs := []rune(text)
	var toks []Token
	emit := func(k Kind, s, e int) { toks = append(toks, Token{Kind: k, Text: string(rs[s:e]), Start: s, End: e}) }
	i := 0
	if t := strings.TrimLeft(text, " \t"); strings.HasPrefix(t, "\\") {
		return []Token{{Kind: Meta, Text: text, Start: 0, End: len(rs)}}
	}
	for i < len(rs) {
		r := rs[i]
		switch {
		case isSpace(r):
			j := i
			for j < len(rs) && isSpace(rs[j]) {
				j++
			}
			emit(Space, i, j)
			i = j
		case r == '-' && i+1 < len(rs) && rs[i+1] == '-':
			j := i
			for j < len(rs) && rs[j] != '\n' {
				j++
			}
			emit(Comment, i, j)
			i = j
		case r == '/' && i+1 < len(rs) && rs[i+1] == '*':
			j := i + 2
			for j+1 < len(rs) && !(rs[j] == '*' && rs[j+1] == '/') {
				j++
			}
			j = min(j+2, len(rs))
			emit(Comment, i, j)
			i = j
		case r == '\'':
			j := closingQuote(rs, i, '\'')
			j = min(j+1, len(rs))
			emit(String, i, j)
			i = j
		case r == '"':
			j := closingQuote(rs, i, '"')
			j = min(j+1, len(rs))
			emit(QuotedIdent, i, j)
			i = j
		case unicode.IsDigit(r) || (r == '.' && i+1 < len(rs) && unicode.IsDigit(rs[i+1])):
			j := i
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.' || rs[j] == 'e' || rs[j] == 'E' ||
				((rs[j] == '+' || rs[j] == '-') && j > i && (rs[j-1] == 'e' || rs[j-1] == 'E'))) {
				j++
			}
			emit(Number, i, j)
			i = j
		case isIdentStart(r):
			j := i
			for j < len(rs) && isIdentPart(rs[j]) {
				j++
			}
			word := string(rs[i:j])
			if IsKeyword(word) {
				emit(Keyword, i, j)
			} else {
				emit(Ident, i, j)
			}
			i = j
		case strings.ContainsRune("<>=!|+-*/%", r):
			j := i + 1
			for j < len(rs) && strings.ContainsRune("<>=|", rs[j]) {
				j++
			}
			emit(Operator, i, j)
			i = j
		default:
			emit(Punct, i, i+1)
			i++
		}
	}
	return toks
}

func isIdentStart(r rune) bool { return r == '_' || r == '$' || unicode.IsLetter(r) }
func isIdentPart(r rune) bool  { return isIdentStart(r) || unicode.IsDigit(r) }

// Keywords is the Trino vocabulary used for highlighting and completion.
var Keywords = []string{
	"ALL", "ALTER", "AND", "ANY", "ARRAY", "AS", "ASC", "AT", "BERNOULLI", "BETWEEN", "BY", "CALL", "CASCADE", "CASE", "CAST",
	"CATALOGS", "COLUMN", "COLUMNS", "COMMENT", "COMMIT", "CONSTRAINT", "CREATE", "CROSS", "CUBE", "CURRENT", "CURRENT_DATE",
	"CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_USER", "DATA", "DATE", "DAY", "DEALLOCATE", "DELETE", "DESC", "DESCRIBE",
	"DISTINCT", "DROP", "ELSE", "END", "ESCAPE", "EXCEPT", "EXCLUDING", "EXECUTE", "EXISTS", "EXPLAIN", "EXTRACT", "FALSE",
	"FETCH", "FILTER", "FIRST", "FOLLOWING", "FOR", "FORMAT", "FROM", "FULL", "FUNCTIONS", "GRANT", "GROUP", "GROUPING",
	"GROUPS", "HAVING", "HOUR", "IF", "IGNORE", "IN", "INCLUDING", "INNER", "INSERT", "INTERSECT", "INTERVAL", "INTO", "IS",
	"ISOLATION", "JOIN", "JSON", "LAST", "LATERAL", "LEFT", "LEVEL", "LIKE", "LIMIT", "LOCAL", "LOCALTIME", "LOCALTIMESTAMP",
	"MAP", "MATERIALIZED", "MINUTE", "MONTH", "NATURAL", "NEXT", "NFC", "NFD", "NFKC", "NFKD", "NO", "NORMALIZE", "NOT", "NULL",
	"NULLS", "OFFSET", "ON", "ONLY", "OPTION", "OR", "ORDER", "ORDINALITY", "OUTER", "OVER", "PARTITION", "PARTITIONS",
	"POSITION", "PRECEDING", "PREPARE", "PRIVILEGES", "PROPERTIES", "RANGE", "READ", "RECURSIVE", "REFRESH", "RENAME",
	"REPEATABLE", "REPLACE", "RESET", "RESPECT", "RESTRICT", "REVOKE", "RIGHT", "ROLLBACK", "ROLLUP", "ROW", "ROWS",
	"SCHEMA", "SCHEMAS", "SECOND", "SELECT", "SERIALIZABLE", "SESSION", "SET", "SETS", "SHOW", "SOME", "START", "STATS",
	"SUBSTRING", "SYSTEM", "TABLE", "TABLES", "TABLESAMPLE", "TEXT", "THEN", "TIES", "TIME", "TIMESTAMP", "TO", "TRANSACTION",
	"TRUE", "TRY_CAST", "TYPE", "UESCAPE", "UNBOUNDED", "UNCOMMITTED", "UNION", "UNNEST", "UPDATE", "USE", "USING", "VALUES",
	"VERBOSE", "VIEW", "WHEN", "WHERE", "WINDOW", "WITH", "WITHIN", "WITHOUT", "WORK", "WRITE", "YEAR", "ZONE",
	"BOOLEAN", "TINYINT", "SMALLINT", "INTEGER", "INT", "BIGINT", "REAL", "DOUBLE", "DECIMAL", "VARCHAR", "CHAR", "VARBINARY",
}

// Functions is a working set of Trino functions for completion and highlighting.
var Functions = []string{
	"abs", "approx_distinct", "approx_percentile", "array_agg", "array_distinct", "array_join", "array_max", "array_min",
	"array_sort", "arrays_overlap", "avg", "bool_and", "bool_or", "cardinality", "ceil", "ceiling", "coalesce", "concat",
	"concat_ws", "contains", "corr", "count", "count_if", "covar_pop", "covar_samp", "current_date", "current_timestamp",
	"date", "date_add", "date_diff", "date_format", "date_parse", "date_trunc", "day", "day_of_week", "day_of_year",
	"dense_rank", "element_at", "every", "exp", "filter", "first_value", "floor", "format", "format_datetime", "from_base64",
	"from_iso8601_date", "from_iso8601_timestamp", "from_unixtime", "from_utf8", "greatest", "hour", "if", "is_json_scalar",
	"json_array_length", "json_extract", "json_extract_scalar", "json_format", "json_parse", "json_query", "json_value",
	"lag", "last_value", "lead", "least", "length", "listagg", "ln", "log", "log10", "log2", "lower", "lpad", "ltrim",
	"map_agg", "map_keys", "map_values", "max", "max_by", "md5", "min", "min_by", "minute", "mod", "month", "now",
	"nth_value", "ntile", "nullif", "parse_datetime", "percent_rank", "pi", "position", "pow", "power", "quarter", "radians",
	"rand", "random", "rank", "reduce", "regexp_extract", "regexp_extract_all", "regexp_like", "regexp_replace",
	"regexp_split", "repeat", "replace", "reverse", "round", "row_number", "rpad", "rtrim", "second", "sequence", "sha256",
	"sign", "slice", "split", "split_part", "sqrt", "starts_with", "stddev", "stddev_pop", "stddev_samp", "strpos",
	"substr", "substring", "sum", "to_base64", "to_char", "to_date", "to_hex", "to_iso8601", "to_unixtime", "to_utf8",
	"transform", "trim", "trunc", "truncate", "try", "try_cast", "typeof", "upper", "url_decode", "url_encode", "uuid",
	"var_pop", "var_samp", "variance", "week", "width_bucket", "year", "zip",
}

var keywordSet = func() map[string]bool {
	m := make(map[string]bool, len(Keywords))
	for _, k := range Keywords {
		m[k] = true
	}
	return m
}()

var functionSet = func() map[string]bool {
	m := make(map[string]bool, len(Functions))
	for _, f := range Functions {
		m[f] = true
	}
	return m
}()

// IsKeyword reports whether word (any case) is a keyword.
func IsKeyword(word string) bool { return keywordSet[strings.ToUpper(word)] }

// IsFunction reports whether word (any case) is a known function.
func IsFunction(word string) bool { return functionSet[strings.ToLower(word)] }

// ANSI styles for the highlighter.
const (
	styleKeyword  = "\x1b[1;34m"
	styleString   = "\x1b[32m"
	styleNumber   = "\x1b[36m"
	styleComment  = "\x1b[2m"
	styleFunction = "\x1b[35m"
	styleQuoted   = "\x1b[33m"
	styleMeta     = "\x1b[1;33m"
	styleReset    = "\x1b[0m"
)

// Highlight returns text with ANSI colors per token.
func Highlight(text string) string {
	toks := Lex(text)
	var b strings.Builder
	for i, t := range toks {
		style := ""
		switch t.Kind {
		case Keyword:
			style = styleKeyword
		case String:
			style = styleString
		case Number:
			style = styleNumber
		case Comment:
			style = styleComment
		case QuotedIdent:
			style = styleQuoted
		case Meta:
			style = styleMeta
		case Ident:
			if IsFunction(t.Text) && nextIsParen(toks, i) {
				style = styleFunction
			}
		}
		if style == "" {
			b.WriteString(t.Text)
			continue
		}
		b.WriteString(style)
		b.WriteString(t.Text)
		b.WriteString(styleReset)
	}
	return b.String()
}

func nextIsParen(toks []Token, i int) bool {
	for j := i + 1; j < len(toks); j++ {
		if toks[j].Kind == Space {
			continue
		}
		return toks[j].Text == "("
	}
	return false
}
