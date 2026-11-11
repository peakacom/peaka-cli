package classify

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed default.yaml
var defaultYAML []byte

// Template is a classification taxonomy: groups of labels with the descriptions the model chooses from, the
// name hints and value rules that decide or hint before the model is asked, and the thresholds. Companies
// extend the built-in one (extends: default) with their own identifiers.
type Template struct {
	Name        string            `yaml:"name" json:"name"`
	Version     int               `yaml:"version" json:"version"`
	Extends     string            `yaml:"extends,omitempty" json:"extends,omitempty"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Settings    Settings          `yaml:"settings" json:"settings"`
	Shortcuts   []*Shortcut       `yaml:"shortcuts,omitempty" json:"shortcuts,omitempty"`
	Groups      map[string]*Group `yaml:"groups" json:"groups"`
	Disable     []string          `yaml:"disable,omitempty" json:"disable,omitempty"`

	labels    []*LabelDef        // compiled by Validate, sorted by id
	shortcuts []compiledShortcut // compiled by Validate, enabled ones in file order
}

// Shortcut settles a column as "none" from its metadata alone, before any value rule and without the model,
// at every evidence level. Every condition it lists must hold. A column whose name matches a label's name
// hint is never shortcut; it goes to the model instead. An extending template replaces a base shortcut of
// the same id (enabled: false turns it off) and appends new ones.
type Shortcut struct {
	ID         string   `yaml:"id" json:"id"`
	Title      string   `yaml:"title,omitempty" json:"title,omitempty"`
	Enabled    *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Name       string   `yaml:"name,omitempty" json:"name,omitempty"`               // regex on the normalised column name (customer_email)
	Catalog    string   `yaml:"catalog,omitempty" json:"catalog,omitempty"`         // regex on the catalog name
	Types      []string `yaml:"types,omitempty" json:"types,omitempty"`             // data type classes the column must be one of
	PrimaryKey bool     `yaml:"primary_key,omitempty" json:"primary_key,omitempty"` // the connector marks the column as a primary key
	System     bool     `yaml:"system,omitempty" json:"system,omitempty"`           // the connector marks the column as a system column
}

type compiledShortcut struct {
	*Shortcut
	name, catalog *regexp.Regexp
}

// matches says whether every condition of the shortcut holds for a column; name is the normalised column name.
func (s compiledShortcut) matches(col ColumnInput, name string) bool {
	if s.name != nil && !s.name.MatchString(name) {
		return false
	}
	if s.catalog != nil && !s.catalog.MatchString(col.Catalog) {
		return false
	}
	if s.PrimaryKey && !col.PrimaryKey {
		return false
	}
	if s.System && !col.System {
		return false
	}
	if len(s.Types) > 0 {
		class := TypeClass(col.Type)
		ok := false
		for _, t := range s.Types {
			if t == class {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// Settings are the thresholds of a template; the CLI can override model_accept with --min-confidence.
type Settings struct {
	RuleMinSamples int     `yaml:"rule_min_samples" json:"rule_min_samples"` // non-null sampled values a rule needs to decide
	ModelAccept    float64 `yaml:"model_accept" json:"model_accept"`         // Choice confidence that accepts a label
	ModelReview    float64 `yaml:"model_review" json:"model_review"`         // below accept but at least this: review, label kept
	NoneGate       float64 `yaml:"none_gate" json:"none_gate"`               // p(personal) below this means the column is not personal
	PhiGate        float64 `yaml:"phi_gate" json:"phi_gate"`                 // p(health) at or above this flags PHI
}

// Group gathers labels of one kind; a disabled group's labels are not offered to the model.
type Group struct {
	Title       string            `yaml:"title" json:"title"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Enabled     *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Labels      map[string]*Label `yaml:"labels" json:"labels"`
}

// Label is one classification; its id is stable and becomes the tag later.
type Label struct {
	Title       string   `yaml:"title" json:"title"`
	Sensitivity string   `yaml:"sensitivity" json:"sensitivity"` // high, medium or low
	Description string   `yaml:"description" json:"description"` // one discriminative sentence: the model's criteria text
	Types       []string `yaml:"types,omitempty" json:"types,omitempty"`
	NameHints   []string `yaml:"name_hints,omitempty" json:"name_hints,omitempty"`
	Rules       []Rule   `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// Rule is a check on sampled values: a named validator or a regular expression that a fraction of them must pass.
type Rule struct {
	Type        string  `yaml:"type" json:"type"`                 // validator or regex
	ID          string  `yaml:"id,omitempty" json:"id,omitempty"` // validator id
	Pattern     string  `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	MinFraction float64 `yaml:"min_fraction" json:"min_fraction"`
	Decide      *bool   `yaml:"decide,omitempty" json:"decide,omitempty"` // false: a co-condition that cannot decide alone
}

// LabelDef is a label with its group and compiled patterns.
type LabelDef struct {
	ID      string
	Group   string
	Label   *Label
	Enabled bool
	hints   []*regexp.Regexp
	rules   []compiledRule
}

type compiledRule struct {
	Rule
	check func(string) bool
}

const (
	TemplateSchema = "peaka.classify.template/v1"
	NoneLabel      = "none"
	noneGroup      = "none"
	maxLabels      = 254 // the model takes 255 options; one is "none"
)

var (
	defaultOnce     sync.Once
	defaultTemplate *Template
	defaultErr      error
	labelIDRe       = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// Default is the built-in template.
func Default() (*Template, error) {
	defaultOnce.Do(func() {
		defaultTemplate, defaultErr = Parse(defaultYAML)
	})
	return defaultTemplate, defaultErr
}

// Load reads a template file (YAML or JSON), merges it onto the one it extends, and validates it.
func Load(path string) (*Template, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	t, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

// Parse decodes a template, applies extends, and validates it.
func Parse(b []byte) (*Template, error) {
	var t Template
	if err := yaml.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("template: %w", err)
	}
	if t.Extends != "" {
		if t.Extends != "default" {
			return nil, fmt.Errorf("template: extends %q: only the built-in \"default\" can be extended", t.Extends)
		}
		base, err := Default()
		if err != nil {
			return nil, err
		}
		t = *base.merge(&t)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// merge lays child over the base: settings and groups merge by key, a child label replaces the base label of
// the same id entirely, and disable removes labels.
func (base *Template) merge(child *Template) *Template {
	out := &Template{Name: child.Name, Version: child.Version, Description: child.Description, Settings: base.Settings, Groups: map[string]*Group{}}
	if out.Name == "" {
		out.Name = base.Name
	}
	if out.Description == "" {
		out.Description = base.Description
	}
	s := child.Settings
	if s.RuleMinSamples != 0 {
		out.Settings.RuleMinSamples = s.RuleMinSamples
	}
	if s.ModelAccept != 0 {
		out.Settings.ModelAccept = s.ModelAccept
	}
	if s.ModelReview != 0 {
		out.Settings.ModelReview = s.ModelReview
	}
	if s.NoneGate != 0 {
		out.Settings.NoneGate = s.NoneGate
	}
	if s.PhiGate != 0 {
		out.Settings.PhiGate = s.PhiGate
	}
	out.Shortcuts = append(out.Shortcuts, base.Shortcuts...)
	for _, sc := range child.Shortcuts {
		replaced := false
		for i, b := range out.Shortcuts {
			if b != nil && sc != nil && b.ID == sc.ID {
				out.Shortcuts[i] = sc
				replaced = true
			}
		}
		if !replaced {
			out.Shortcuts = append(out.Shortcuts, sc)
		}
	}
	for id, g := range base.Groups {
		cp := *g
		cp.Labels = map[string]*Label{}
		for lid, l := range g.Labels {
			cp.Labels[lid] = l
		}
		out.Groups[id] = &cp
	}
	for id, g := range child.Groups {
		cur, ok := out.Groups[id]
		if !ok {
			out.Groups[id] = g
			continue
		}
		if g.Title != "" {
			cur.Title = g.Title
		}
		if g.Description != "" {
			cur.Description = g.Description
		}
		if g.Enabled != nil {
			cur.Enabled = g.Enabled
		}
		for lid, l := range g.Labels {
			// the same id in another base group is replaced, not duplicated
			for _, og := range out.Groups {
				delete(og.Labels, lid)
			}
			cur.Labels[lid] = l
		}
	}
	for _, lid := range child.Disable {
		for _, g := range out.Groups {
			delete(g.Labels, lid)
		}
	}
	return out
}

// Validate checks ids, thresholds, validators and patterns, and compiles the labels.
func (t *Template) Validate() error {
	if t.Name == "" {
		return fmt.Errorf("template: name is required")
	}
	s := &t.Settings
	if s.RuleMinSamples <= 0 {
		s.RuleMinSamples = 20
	}
	if s.ModelAccept <= 0 {
		s.ModelAccept = 0.9
	}
	if s.ModelReview <= 0 {
		s.ModelReview = 0.6
	}
	if s.NoneGate <= 0 {
		s.NoneGate = 0.5
	}
	if s.PhiGate <= 0 {
		s.PhiGate = 0.75
	}
	if s.ModelReview > s.ModelAccept {
		return fmt.Errorf("template: model_review (%v) must not exceed model_accept (%v)", s.ModelReview, s.ModelAccept)
	}
	t.shortcuts = nil
	seenShortcut := map[string]bool{}
	for i, sc := range t.Shortcuts {
		if sc == nil {
			return fmt.Errorf("template: shortcut %d is empty", i+1)
		}
		if !labelIDRe.MatchString(sc.ID) {
			return fmt.Errorf("template: shortcut %d: id %q: lower-case letters, digits and _ only", i+1, sc.ID)
		}
		if seenShortcut[sc.ID] {
			return fmt.Errorf("template: shortcut %q is listed twice", sc.ID)
		}
		seenShortcut[sc.ID] = true
		if sc.Enabled != nil && !*sc.Enabled {
			continue
		}
		cs := compiledShortcut{Shortcut: sc}
		if sc.Name != "" {
			re, err := regexp.Compile("(?i)" + sc.Name)
			if err != nil {
				return fmt.Errorf("template: shortcut %q: name %q: %v", sc.ID, sc.Name, err)
			}
			cs.name = re
		}
		if sc.Catalog != "" {
			re, err := regexp.Compile("(?i)" + sc.Catalog)
			if err != nil {
				return fmt.Errorf("template: shortcut %q: catalog %q: %v", sc.ID, sc.Catalog, err)
			}
			cs.catalog = re
		}
		for _, ty := range sc.Types {
			if !knownTypeClass(ty) {
				return fmt.Errorf("template: shortcut %q: type %q: one of %s", sc.ID, ty, strings.Join(typeClasses, ", "))
			}
		}
		if sc.Name == "" && sc.Catalog == "" && len(sc.Types) == 0 && !sc.PrimaryKey && !sc.System {
			return fmt.Errorf("template: shortcut %q: at least one of name, catalog, types, primary_key or system", sc.ID)
		}
		t.shortcuts = append(t.shortcuts, cs)
	}
	seen := map[string]string{}
	t.labels = nil
	for gid, g := range t.Groups {
		if g == nil {
			return fmt.Errorf("template: group %q is empty", gid)
		}
		if !labelIDRe.MatchString(gid) {
			return fmt.Errorf("template: group id %q: lower-case letters, digits and _ only", gid)
		}
		for lid, l := range g.Labels {
			if l == nil {
				return fmt.Errorf("template: label %q is empty", lid)
			}
			if !labelIDRe.MatchString(lid) {
				return fmt.Errorf("template: label id %q: lower-case letters, digits and _ only", lid)
			}
			if og, dup := seen[lid]; dup {
				return fmt.Errorf("template: label %q is in both %s and %s", lid, og, gid)
			}
			seen[lid] = gid
			switch l.Sensitivity {
			case "high", "medium", "low":
			case "":
				l.Sensitivity = "medium"
			default:
				return fmt.Errorf("template: label %q: sensitivity %q: high, medium or low", lid, l.Sensitivity)
			}
			if strings.TrimSpace(l.Description) == "" {
				return fmt.Errorf("template: label %q: description is required", lid)
			}
			for _, ty := range l.Types {
				if !knownTypeClass(ty) {
					return fmt.Errorf("template: label %q: type %q: one of %s", lid, ty, strings.Join(typeClasses, ", "))
				}
			}
			d := &LabelDef{ID: lid, Group: gid, Label: l, Enabled: g.Enabled == nil || *g.Enabled}
			for _, h := range l.NameHints {
				re, err := regexp.Compile("(?i)" + h)
				if err != nil {
					return fmt.Errorf("template: label %q: name hint %q: %v", lid, h, err)
				}
				d.hints = append(d.hints, re)
			}
			for i := range l.Rules {
				r := l.Rules[i]
				if r.MinFraction <= 0 || r.MinFraction > 1 {
					return fmt.Errorf("template: label %q: rule %d: min_fraction must be in (0, 1]", lid, i+1)
				}
				cr := compiledRule{Rule: r}
				switch r.Type {
				case "validator":
					f, ok := validators[r.ID]
					if !ok {
						return fmt.Errorf("template: label %q: unknown validator %q: one of %s", lid, r.ID, strings.Join(ValidatorIDs(), ", "))
					}
					cr.check = f
				case "regex":
					re, err := regexp.Compile(r.Pattern)
					if err != nil {
						return fmt.Errorf("template: label %q: rule %d: %v", lid, i+1, err)
					}
					cr.check = func(v string) bool { return re.MatchString(strings.TrimSpace(v)) }
				default:
					return fmt.Errorf("template: label %q: rule %d: type %q: validator or regex", lid, i+1, r.Type)
				}
				d.rules = append(d.rules, cr)
			}
			t.labels = append(t.labels, d)
		}
	}
	if _, ok := seen[NoneLabel]; !ok {
		return fmt.Errorf("template: the %q label is required", NoneLabel)
	}
	if len(t.labels) > maxLabels+1 {
		return fmt.Errorf("template: %d labels; at most %d", len(t.labels), maxLabels+1)
	}
	sort.Slice(t.labels, func(i, j int) bool { return t.labels[i].ID < t.labels[j].ID })
	return nil
}

// Labels are the compiled labels, sorted by id (the order the model sees them in).
func (t *Template) Labels() []*LabelDef { return t.labels }

// Label looks one up by id.
func (t *Template) Label(id string) *LabelDef {
	i := sort.Search(len(t.labels), func(i int) bool { return t.labels[i].ID >= id })
	if i < len(t.labels) && t.labels[i].ID == id {
		return t.labels[i]
	}
	return nil
}

// Fingerprint identifies the effective template content, so a report can be traced to it.
func (t *Template) Fingerprint() string {
	b, _ := json.Marshal(t)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// YAML renders the template, for `classify template`.
func (t *Template) YAML() ([]byte, error) { return yaml.Marshal(t) }

// --- data type classes: a label can restrict the column types it applies to ---

var typeClasses = []string{"string", "number", "date", "boolean", "binary", "other"}

func knownTypeClass(c string) bool {
	for _, k := range typeClasses {
		if k == c {
			return true
		}
	}
	return false
}

// TypeClass maps a connector's data type (varchar, bigint, timestamp(3) with time zone, …) to a class.
func TypeClass(dataType string) string {
	t := strings.ToLower(strings.TrimSpace(dataType))
	if i := strings.IndexAny(t, "( "); i > 0 {
		t = t[:i]
	}
	switch t {
	case "varchar", "char", "string", "text", "nvarchar", "nchar", "clob", "character", "citext", "uuid", "json", "jsonb", "enum", "name":
		return "string"
	case "tinyint", "smallint", "integer", "int", "bigint", "real", "double", "float", "decimal", "numeric", "number", "money", "serial", "bigserial":
		return "number"
	case "date", "time", "timestamp", "datetime", "interval", "timestamptz", "timetz":
		return "date"
	case "boolean", "bool", "bit":
		return "boolean"
	case "varbinary", "binary", "blob", "bytea", "bytes":
		return "binary"
	}
	return "other"
}

// Applies says whether a label is offered for a column of that data type.
func (d *LabelDef) Applies(dataType string) bool {
	if len(d.Label.Types) == 0 {
		return true
	}
	c := TypeClass(dataType)
	for _, t := range d.Label.Types {
		if t == c {
			return true
		}
	}
	return false
}
