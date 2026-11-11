package classify

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/peakacom/peaka-cli/internal/systemone"
)

// Classifier answers System One questions: *systemone.Client, or a fake in tests.
type Classifier interface {
	Ask(ctx context.Context, state any, questions map[string]systemone.Question) (*systemone.Response, error)
	ModelName() string
}

// TableInput is one table with its columns, as the CLI collected it.
type TableInput struct {
	CatalogID   string
	Catalog     string
	Schema      string
	Table       string
	Description string
	Columns     []ColumnInput
}

// Engine classifies the columns of a table: shapes, then rules, then the model for what rules left open.
type Engine struct {
	Template      *Template
	Classifier    Classifier
	Evidence      Evidence
	MinConfidence float64 // 0: the template's model_accept
	SampleValues  int     // values per column at EvidenceSamples; 0: DefaultSamples
	Concurrency   int     // model requests in flight; 0: 1
	IncludeSample bool    // keep the sent sample values in the result

	// DryRun receives each request instead of the classifier; nothing is sent.
	DryRun func(req systemone.Request)
	// Progress is told after each column, when set.
	Progress func(done int, usage systemone.Usage)
}

// Status of a column result.
const (
	StatusAccepted = "accepted"
	StatusReview   = "review"
	StatusError    = "error"
	StatusDryRun   = "dry-run"
)

// ColumnResult is one classified column.
type ColumnResult struct {
	CatalogID string `json:"catalog_id"`
	Catalog   string `json:"catalog"`
	Schema    string `json:"schema"`
	Table     string `json:"table"`
	Column    string `json:"column"`
	DataType  string `json:"data_type"`

	Status      string  `json:"status"`
	Label       string  `json:"label,omitempty"`
	Group       string  `json:"group,omitempty"`
	Title       string  `json:"title,omitempty"`
	Sensitivity string  `json:"sensitivity,omitempty"`
	PHI         bool    `json:"phi"`
	Confidence  float64 `json:"confidence"`
	Method      string  `json:"method,omitempty"` // rule, shortcut or model
	Rule        string  `json:"rule,omitempty"`

	Evidence ColumnEvidence `json:"evidence"`
	Model    *ModelResult   `json:"model,omitempty"`
	Note     string         `json:"note,omitempty"`
}

// ColumnEvidence is what the classification was based on; it never contains values unless samples were asked to be kept.
type ColumnEvidence struct {
	Level                 Evidence           `json:"level"`
	Stats                 *ValueStats        `json:"stats,omitempty"`
	NameHints             []string           `json:"name_hints,omitempty"`
	RuleMatches           map[string]float64 `json:"rule_matches,omitempty"`
	Samples               []string           `json:"samples,omitempty"`
	DescriptionSuspicious bool               `json:"description_suspicious,omitempty"`
}

// ModelResult is what the model answered.
type ModelResult struct {
	Returned string             `json:"returned"`
	Choice   string             `json:"choice"`
	Top      map[string]float64 `json:"probabilities_top3"`
	Personal float64            `json:"personal"`
	Health   float64            `json:"health"`
	Groups   map[string]float64 `json:"group_probabilities"`
}

// ClassifyTable classifies every column of a table. Columns with a model error carry StatusError and the
// error text; the call fails only when the context ends or the classifier refuses the key.
func (e *Engine) ClassifyTable(ctx context.Context, in TableInput) ([]ColumnResult, systemone.Usage, error) {
	results := make([]ColumnResult, len(in.Columns))
	var (
		mu    sync.Mutex
		usage systemone.Usage
		done  int
		fatal error
		wg    sync.WaitGroup
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	n := e.Concurrency
	if n <= 0 {
		n = 1
	}
	sem := make(chan struct{}, n)
	for i := range in.Columns {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			r, u, err := e.classifyColumn(ctx, in, i)
			mu.Lock()
			defer mu.Unlock()
			results[i] = r
			usage.InputTokens += u.InputTokens
			usage.OutputTokens += u.OutputTokens
			done++
			if err != nil && fatal == nil {
				fatal = err
				cancel()
			}
			if e.Progress != nil {
				e.Progress(done, usage)
			}
		}(i)
	}
	wg.Wait()
	if fatal != nil {
		return results, usage, fatal
	}
	if ctx.Err() != nil {
		return results, usage, ctx.Err()
	}
	e.flagPHIByContext(results)
	return results, usage, nil
}

// flagPHIByContext settles the PHI flag of the columns the model was not asked about. A label of the phi
// group is health information by definition; an identifier decided by a rule (an SSN, a phone) is PHI when
// the table holds health information, which the model's health gate on the sibling columns tells.
func (e *Engine) flagPHIByContext(results []ColumnResult) {
	gate := e.Template.Settings.PhiGate
	clinical := 0
	for _, r := range results {
		if r.Model != nil && r.Label != "" && r.Label != NoneLabel && r.Model.Health >= gate {
			clinical++
		}
	}
	for i := range results {
		r := &results[i]
		if r.PHI || r.Model != nil || r.Label == "" || r.Label == NoneLabel {
			continue
		}
		switch {
		case r.Group == "phi":
			r.PHI, r.Sensitivity = true, "high"
		case clinical > 0 && (r.Group == "pii" || r.Group == "pii_quasi"):
			r.PHI, r.Sensitivity = true, "high"
			if r.Note == "" {
				r.Note = fmt.Sprintf("PHI by table context: %d sibling columns hold health information", clinical)
			}
		}
	}
}

func (e *Engine) classifyColumn(ctx context.Context, in TableInput, i int) (ColumnResult, systemone.Usage, error) {
	col := in.Columns[i]
	if col.Catalog == "" {
		col.Catalog = in.Catalog
	}
	r := ColumnResult{CatalogID: in.CatalogID, Catalog: in.Catalog, Schema: in.Schema, Table: in.Table, Column: col.Name, DataType: col.Type,
		Evidence: ColumnEvidence{Level: e.Evidence}}
	var stats *ValueStats
	if e.Evidence != EvidenceNames && (len(col.Values) > 0 || col.Nulls > 0) {
		s := Summarize(col.Values, col.Nulls)
		stats = &s
		r.Evidence.Stats = stats
	}
	ro := e.Template.EvaluateRules(col, e.Evidence)
	r.Evidence.NameHints = ro.NameHints
	if len(ro.Matches) > 0 {
		r.Evidence.RuleMatches = ro.Matches
	}
	if d := ro.Decided; d != nil {
		e.fill(&r, d.Label, d.Group)
		r.Status, r.Method, r.Rule, r.Confidence = StatusAccepted, d.Method, d.Rule, d.Confidence
		return r, systemone.Usage{}, nil
	}
	if e.Evidence != EvidenceNames && stats != nil && len(col.Values) == 0 && len(ro.NameHints) == 0 {
		// nothing to judge: no values in the sample and a name that says nothing
		r.Status, r.Note = StatusReview, "no values in the sample"
		return r, systemone.Usage{}, nil
	}

	state, suspicious := buildState(in, col, stats, ro, e.Evidence, e.SampleValues)
	r.Evidence.DescriptionSuspicious = suspicious
	if e.IncludeSample {
		r.Evidence.Samples = state.Samples
	}
	questions, _ := e.Template.questions(col.Type)
	if e.DryRun != nil {
		e.DryRun(systemone.Request{Model: e.Classifier.ModelName(), State: state, Questions: questions})
		r.Status, r.Method = StatusDryRun, "model"
		return r, systemone.Usage{}, nil
	}
	resp, err := e.Classifier.Ask(ctx, state, questions)
	if err != nil {
		var se *systemone.Error
		if ctx.Err() != nil || (asError(err, &se) && (se.Status == 401 || se.Status == 403)) {
			return r, systemone.Usage{}, err
		}
		r.Status, r.Method, r.Note = StatusError, "model", err.Error()
		return r, systemone.Usage{}, nil
	}
	e.judge(&r, resp, ro, suspicious)
	return r, resp.Usage, nil
}

// judge turns the answers into a label, a status and the PHI flag.
func (e *Engine) judge(r *ColumnResult, resp *systemone.Response, ro RuleOutcome, suspicious bool) {
	s := e.Template.Settings
	accept := s.ModelAccept
	if e.MinConfidence > 0 {
		accept = e.MinConfidence
	}
	label, okL := resp.Answers[qLabel]
	personal, okP := resp.Answers[qPersonal]
	health, okH := resp.Answers[qHealth]
	if !okL || !okP || !okH {
		r.Status, r.Method, r.Note = StatusError, "model", "the model did not answer every question"
		return
	}
	m := &ModelResult{Returned: resp.Model, Choice: label.Choice, Top: topN(label.Probabilities, 3), Personal: round(personal.Noul), Health: round(health.Noul), Groups: map[string]float64{}}
	for id, p := range label.Probabilities {
		if d := e.Template.Label(id); d != nil {
			m.Groups[d.Group] = round(m.Groups[d.Group] + p)
		}
	}
	r.Model, r.Method = m, "model"
	conf := label.Confidence
	chosen := label.Choice
	// a column name that says the same thing as the model is worth a little confidence, before the thresholds apply
	hinted, agrees := len(ro.NameHints) > 0, false
	for _, h := range ro.NameHints {
		if h == chosen {
			agrees = true
		}
	}
	if agrees && chosen != NoneLabel {
		conf = round(math.Min(1, conf+0.05))
	}
	// The choice is the primary signal: "none" is one of its options. The personal gate settles "none" when the
	// choice alone would not, and is reported, not obeyed, next to a confident label: System One models answer
	// yes/no questions more cautiously than they pick options.
	switch {
	case personal.Noul < s.NoneGate && chosen == NoneLabel:
		e.fill(r, NoneLabel, noneGroup)
		r.Status, r.Confidence = StatusAccepted, round(math.Max(conf, 1-personal.Noul))
	case chosen == NoneLabel && personal.Noul >= 0.75:
		r.Group = argmax(m.Groups)
		r.Status, r.Confidence, r.Note = StatusReview, round(conf), "personal data, but no label fits"
	case conf >= accept:
		e.fill(r, chosen, "")
		r.Status, r.Confidence = StatusAccepted, round(conf)
	case conf >= s.ModelReview:
		e.fill(r, chosen, "")
		r.Status, r.Confidence, r.Note = StatusReview, round(conf), fmt.Sprintf("confidence below %.2f", accept)
	default:
		r.Group = argmax(m.Groups)
		r.Status, r.Confidence, r.Note = StatusReview, round(conf), fmt.Sprintf("low confidence; best guess %s", chosen)
	}
	if r.Label != "" && r.Label != NoneLabel && hinted && !agrees && r.Status == StatusAccepted && r.Confidence < 0.95 {
		r.Status = StatusReview
		r.Note = fmt.Sprintf("the column name suggests %v", ro.NameHints)
	}
	if r.Label != "" && r.Label != NoneLabel && personal.Noul < s.NoneGate && r.Note == "" {
		r.Note = fmt.Sprintf("the personal-data gate is low (%.2f)", personal.Noul)
	}
	if health.Noul >= s.PhiGate {
		r.PHI = true
		r.Sensitivity = "high"
	}
	if suspicious && r.Status == StatusAccepted {
		r.Status, r.Note = StatusReview, "the column description reads like an instruction"
	}
}

func (e *Engine) fill(r *ColumnResult, label, group string) {
	r.Label = label
	if d := e.Template.Label(label); d != nil {
		r.Group, r.Title, r.Sensitivity = d.Group, d.Label.Title, d.Label.Sensitivity
	} else if group != "" {
		r.Group = group
	}
}

func topN(p map[string]float64, n int) map[string]float64 {
	type kv struct {
		k string
		v float64
	}
	var all []kv
	for k, v := range p {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	out := map[string]float64{}
	for i := 0; i < len(all) && i < n; i++ {
		out[all[i].k] = round(all[i].v)
	}
	return out
}

func argmax(p map[string]float64) string {
	best, bv := "", -1.0
	for k, v := range p {
		if v > bv || (v == bv && k < best) {
			best, bv = k, v
		}
	}
	return best
}

func asError(err error, target **systemone.Error) bool {
	e, ok := err.(*systemone.Error)
	if ok {
		*target = e
	}
	return ok
}
