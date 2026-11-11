package classify

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Shape reduces a value to its character classes, so the model sees a column's layout without any of its
// content: digits become d, lower-case letters a, upper-case A, other letters l, whitespace _; the delimiters
// that separate identifier families (@ . - / : + ( ) , # ;) stay; everything else becomes x. Runs are kept,
// because length is what tells an SSN (ddd-dd-dddd) from a phone number or a national id.
func Shape(v string) string {
	v = strings.TrimSpace(v)
	var b strings.Builder
	n := 0
	for _, r := range v {
		if n == shapeMaxLen {
			b.WriteString("…")
			break
		}
		n++
		switch {
		case unicode.IsDigit(r):
			b.WriteByte('d')
		case unicode.IsLower(r):
			b.WriteByte('a')
		case unicode.IsUpper(r):
			b.WriteByte('A')
		case unicode.IsLetter(r):
			b.WriteByte('l')
		case unicode.IsSpace(r):
			b.WriteByte('_')
		case strings.ContainsRune("@.-/:+(),#;", r):
			b.WriteRune(r)
		default:
			b.WriteByte('x')
		}
	}
	return b.String()
}

const (
	shapeMaxLen   = 40 // values longer than this are text, not an identifier
	topShapes     = 5
	textLongShare = 0.5 // a column is free text when this share of its values is longer than shapeMaxLen
	textAvgWords  = 6   // or when its values average more words than this
	enumMaxValues = 20  // at most this many distinct values, and
	enumMaxShare  = 0.1 // at most this share of the sample, make a column categorical
)

// ShapeCount is one layout and how much of the sample has it.
type ShapeCount struct {
	Shape    string  `json:"shape"`
	Fraction float64 `json:"fraction"`
}

// TextStats describes a free-text column without quoting it.
type TextStats struct {
	AvgLen       float64 `json:"avg_len"`
	P90Len       int     `json:"p90_len"`
	Words        float64 `json:"words"`
	Lines        float64 `json:"lines"`
	HasDigits    float64 `json:"has_digits"`
	HasEmailLike float64 `json:"has_email_like"`
	HasDateLike  float64 `json:"has_date_like"`
}

// LenStats are the value lengths of a sample.
type LenStats struct {
	Min int     `json:"min"`
	Avg float64 `json:"avg"`
	Max int     `json:"max"`
}

// ValueStats is what a sample of one column reduces to. Nothing in it can reconstruct a value.
type ValueStats struct {
	RowsSampled      int          `json:"rows_sampled"`
	NullFraction     float64      `json:"null_fraction"`
	EmptyFraction    float64      `json:"empty_fraction"`
	DistinctFraction float64      `json:"distinct_fraction"`
	DistinctCount    int          `json:"distinct_count,omitempty"` // only for categorical columns
	Len              *LenStats    `json:"len,omitempty"`
	TopShapes        []ShapeCount `json:"top_shapes,omitempty"`
	Text             *TextStats   `json:"text,omitempty"`
	Categorical      bool         `json:"categorical,omitempty"`
}

var (
	emailLikeRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	dateLikeRe  = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2}|\d{1,2}/\d{1,2}/\d{2,4}|\d{1,2}\.\d{1,2}\.\d{4})\b`)
)

// Summarize reduces the sampled values of a column (nulls counted apart) to shapes and statistics.
func Summarize(values []string, nulls int) ValueStats {
	total := len(values) + nulls
	s := ValueStats{RowsSampled: total}
	if total == 0 {
		return s
	}
	s.NullFraction = round(float64(nulls) / float64(total))
	var nonEmpty []string
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			continue
		}
		nonEmpty = append(nonEmpty, v)
	}
	s.EmptyFraction = round(float64(len(values)-len(nonEmpty)) / float64(total))
	if len(nonEmpty) == 0 {
		return s
	}
	distinct := map[string]struct{}{}
	lengths := make([]int, 0, len(nonEmpty))
	long, sum, words, lines, digits, emails, dates := 0, 0, 0, 0, 0, 0, 0
	for _, v := range nonEmpty {
		distinct[v] = struct{}{}
		n := len([]rune(v))
		lengths = append(lengths, n)
		sum += n
		if n > shapeMaxLen {
			long++
		}
		words += len(strings.Fields(v))
		lines += 1 + strings.Count(v, "\n")
		if strings.ContainsFunc(v, unicode.IsDigit) {
			digits++
		}
		if emailLikeRe.MatchString(v) {
			emails++
		}
		if dateLikeRe.MatchString(v) {
			dates++
		}
	}
	count := float64(len(nonEmpty))
	s.DistinctFraction = round(float64(len(distinct)) / count)
	sort.Ints(lengths)
	s.Len = &LenStats{Min: lengths[0], Avg: round(float64(sum) / count), Max: lengths[len(lengths)-1]}
	if len(distinct) <= enumMaxValues && float64(len(distinct)) <= enumMaxShare*count {
		s.Categorical = true
		s.DistinctCount = len(distinct)
	}
	if float64(long)/count >= textLongShare || float64(words)/count > textAvgWords {
		s.Text = &TextStats{
			AvgLen:       s.Len.Avg,
			P90Len:       lengths[int(math.Ceil(0.9*count))-1],
			Words:        round(float64(words) / count),
			Lines:        round(float64(lines) / count),
			HasDigits:    round(float64(digits) / count),
			HasEmailLike: round(float64(emails) / count),
			HasDateLike:  round(float64(dates) / count),
		}
		return s
	}
	shapes := map[string]int{}
	for _, v := range nonEmpty {
		shapes[Shape(v)]++
	}
	for sh, n := range shapes {
		s.TopShapes = append(s.TopShapes, ShapeCount{Shape: sh, Fraction: round(float64(n) / count)})
	}
	sort.Slice(s.TopShapes, func(i, j int) bool {
		if s.TopShapes[i].Fraction != s.TopShapes[j].Fraction {
			return s.TopShapes[i].Fraction > s.TopShapes[j].Fraction
		}
		return s.TopShapes[i].Shape < s.TopShapes[j].Shape
	})
	if len(s.TopShapes) > topShapes {
		s.TopShapes = s.TopShapes[:topShapes]
	}
	return s
}

func round(f float64) float64 { return math.Round(f*1000) / 1000 }
