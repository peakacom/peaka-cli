package classify

import (
	"regexp"
	"sort"
	"strings"
)

// Evidence is how much of a column leaves the machine: its name, type and description; also the shapes and
// statistics of its values; or also some of the values themselves.
type Evidence string

const (
	EvidenceNames   Evidence = "names"
	EvidenceShape   Evidence = "shape"
	EvidenceSamples Evidence = "samples"
)

// ParseEvidence validates an --evidence value.
func ParseEvidence(s string) (Evidence, bool) {
	switch Evidence(s) {
	case EvidenceNames, EvidenceShape, EvidenceSamples:
		return Evidence(s), true
	}
	return "", false
}

// ColumnInput is one column as the CLI collected it. Values are the sampled non-null values (nil under
// --evidence names); they are read here and in the prompt and never stored.
type ColumnInput struct {
	Name        string
	Type        string
	Description string
	Catalog     string // the catalog's name, for the template's shortcuts; the engine fills it from the table
	PrimaryKey  bool   // the connector marks the column as a primary key
	System      bool   // the connector marks the column as a system column
	Values      []string
	Nulls       int

	// from the table statistics, when available
	DistinctFraction *float64
	NullsFraction    *float64
}

// Decision is a rule's verdict for a column.
type Decision struct {
	Label      string
	Group      string
	Method     string // rule or shortcut
	Rule       string // what matched: a validator id or "regex" for a rule, the shortcut's id for a shortcut
	Confidence float64
}

// RuleOutcome is everything the rule layer learned about a column.
type RuleOutcome struct {
	Decided   *Decision
	NameHints []string           // labels whose name hints match the column name
	Matches   map[string]float64 // label → fraction of sampled values its rules validated (hints for the model)
}

// EvaluateRules runs the template's shortcuts, the name hints and, when values are there, the value rules
// over a column. A shortcut settles a column that no name hint claims. A label decides when enough values
// were sampled, a deciding rule of it passes along with every co-condition, and no other label decides too;
// shorter matches become hints for the model.
func (t *Template) EvaluateRules(col ColumnInput, ev Evidence) RuleOutcome {
	out := RuleOutcome{Matches: map[string]float64{}}
	name := normalizeName(col.Name)
	for _, d := range t.labels {
		if !d.Enabled || d.ID == NoneLabel {
			continue
		}
		for _, h := range d.hints {
			if h.MatchString(name) {
				out.NameHints = append(out.NameHints, d.ID)
				break
			}
		}
	}
	sort.Strings(out.NameHints)

	if len(out.NameHints) == 0 {
		for _, sc := range t.shortcuts {
			if sc.matches(col, name) {
				out.Decided = &Decision{Label: NoneLabel, Group: noneGroup, Method: "shortcut", Rule: sc.ID, Confidence: 1}
				return out
			}
		}
	}
	if ev == EvidenceNames || len(col.Values) == 0 {
		return out
	}

	var values []string
	for _, v := range col.Values {
		if strings.TrimSpace(v) != "" {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		return out
	}
	enough := len(values) >= t.Settings.RuleMinSamples
	var decided []*Decision
	for _, d := range t.labels {
		if !d.Enabled || len(d.rules) == 0 || !d.Applies(col.Type) {
			continue
		}
		best, deciding, conditions := 0.0, false, true
		for _, r := range d.rules {
			f := fraction(values, r.check)
			if f > best {
				best = f
			}
			if r.Decide != nil && !*r.Decide {
				if f < r.MinFraction {
					conditions = false
				}
				continue
			}
			if f >= r.MinFraction {
				deciding = true
			}
		}
		if deciding && conditions && enough {
			decided = append(decided, &Decision{Label: d.ID, Group: d.Group, Method: "rule", Rule: ruleName(d), Confidence: round(best)})
		}
		if best > 0 && best >= 0.5*minFraction(d) {
			out.Matches[d.ID] = round(best)
		}
	}
	// Several labels can pass on the same values (an SSN also looks like a phone number): the column name
	// settles it when it names exactly one of them. A single rule that the name contradicts (attending_npi
	// passing the phone rule) is the model's call.
	hinted := map[string]bool{}
	for _, h := range out.NameHints {
		hinted[h] = true
	}
	var agreeing []*Decision
	for _, d := range decided {
		if hinted[d.Label] {
			agreeing = append(agreeing, d)
		}
	}
	switch {
	case len(agreeing) == 1:
		out.Decided = agreeing[0]
	case len(decided) == 1 && len(out.NameHints) == 0:
		out.Decided = decided[0]
	}
	return out
}

func fraction(values []string, check func(string) bool) float64 {
	n := 0
	for _, v := range values {
		if check(v) {
			n++
		}
	}
	return float64(n) / float64(len(values))
}

func minFraction(d *LabelDef) float64 {
	m := 1.0
	for _, r := range d.rules {
		if r.Decide == nil || *r.Decide {
			if r.MinFraction < m {
				m = r.MinFraction
			}
		}
	}
	return m
}

func ruleName(d *LabelDef) string {
	var names []string
	for _, r := range d.rules {
		if r.Type == "validator" {
			names = append(names, r.ID)
		} else {
			names = append(names, "regex")
		}
	}
	return strings.Join(names, "+")
}

var camelRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// normalizeName turns CustomerEmail, customer-email and CUSTOMER_EMAIL alike into customer_email.
func normalizeName(s string) string {
	s = camelRe.ReplaceAllString(strings.TrimSpace(s), "${1}_${2}")
	s = strings.ToLower(s)
	s = strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(s)
	return s
}
