package classify

import (
	"sort"
	"strings"
	"time"

	"github.com/peakacom/peaka-cli/internal/systemone"
)

const ReportSchema = "peaka.classify.report/v1"

// Report is the result of one run: enough to review, and later to turn into tags.
type Report struct {
	Schema      string    `json:"schema_version"`
	GeneratedAt time.Time `json:"generated_at"`
	Project     string    `json:"project"`
	Target      string    `json:"target"`
	Template    struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
		Hash    string `json:"hash"`
		Source  string `json:"source"` // builtin, config or flag
	} `json:"template"`
	Model struct {
		Endpoint  string `json:"endpoint"`
		Requested string `json:"requested"`
		Returned  string `json:"returned,omitempty"`
	} `json:"model"`
	Evidence        Evidence       `json:"evidence_level"`
	SamplesPerTable int            `json:"samples_per_table"`
	IncludeSamples  bool           `json:"include_samples"`
	Thresholds      Settings       `json:"thresholds"`
	Summary         Summary        `json:"summary"`
	Columns         []ColumnResult `json:"columns"`
	Errors          []TableError   `json:"errors"`
}

// Summary counts the run.
type Summary struct {
	Tables           int             `json:"tables"`
	Columns          int             `json:"columns"`
	Accepted         int             `json:"accepted"`
	Review           int             `json:"review"`
	Failed           int             `json:"failed"`
	ByGroup          map[string]int  `json:"by_group"`
	ByLabel          map[string]int  `json:"by_label"`
	PHIFlagged       int             `json:"phi_flagged"`
	Requests         int             `json:"requests"`
	Usage            systemone.Usage `json:"usage"`
	EstimatedCostUSD float64         `json:"estimated_cost_usd"`
	ElapsedMS        int64           `json:"elapsed_ms"`
}

// TableError is a table the run could not finish.
type TableError struct {
	Catalog string `json:"catalog"`
	Schema  string `json:"schema"`
	Table   string `json:"table"`
	Stage   string `json:"stage"` // enumerate, columns, stats, sample or model
	Detail  string `json:"detail"`
}

// TypeSafe's list price for input tokens, per million (output is free). Only an estimate for other servers.
const InputTokenPriceUSD = 0.042

// Finish fills the summary from the columns.
func (r *Report) Finish(tables int, started time.Time) {
	s := Summary{Tables: tables, ByGroup: map[string]int{}, ByLabel: map[string]int{}}
	s.Columns = len(r.Columns)
	for _, c := range r.Columns {
		switch c.Status {
		case StatusAccepted:
			s.Accepted++
		case StatusReview:
			s.Review++
		case StatusError:
			s.Failed++
		}
		if c.Group != "" {
			s.ByGroup[c.Group]++
		}
		if c.Label != "" {
			s.ByLabel[c.Label]++
		}
		if c.PHI {
			s.PHIFlagged++
		}
		if c.Model != nil {
			s.Requests++
			if r.Model.Returned == "" {
				r.Model.Returned = c.Model.Returned
			}
		}
	}
	s.Usage = r.Summary.Usage
	s.Requests = max(s.Requests, r.Summary.Requests)
	s.EstimatedCostUSD = round(float64(s.Usage.InputTokens) / 1e6 * InputTokenPriceUSD)
	s.ElapsedMS = time.Since(started).Milliseconds()
	r.Summary = s
	if r.Errors == nil {
		r.Errors = []TableError{}
	}
	if r.Columns == nil {
		r.Columns = []ColumnResult{}
	}
}

// RowFields are the table-format columns, in order.
var RowFields = []string{"table", "column", "type", "label", "group", "sens", "phi", "conf", "method", "status"}

// Rows flattens the columns for table, csv and md output.
func (r *Report) Rows() []map[string]any {
	rows := make([]map[string]any, 0, len(r.Columns))
	for _, c := range r.Columns {
		label := c.Label
		if c.Status == StatusReview && label != "" {
			label += "?"
		}
		rows = append(rows, map[string]any{
			"table": c.Catalog + "." + c.Schema + "." + c.Table, "column": c.Column, "type": c.DataType,
			"label": label, "group": c.Group, "sens": c.Sensitivity, "phi": c.PHI, "conf": c.Confidence,
			"method": c.Method, "status": c.Status, "note": c.Note,
		})
	}
	return rows
}

// Matches returns the columns a --fail-on list selects: label ids, group ids, "phi", "review", "error" or "any"
// (anything that is not accepted as none).
func (r *Report) Matches(failOn []string) []ColumnResult {
	want := map[string]bool{}
	for _, f := range failOn {
		if f = strings.TrimSpace(strings.ToLower(f)); f != "" {
			want[f] = true
		}
	}
	var out []ColumnResult
	for _, c := range r.Columns {
		switch {
		case want["any"] && !(c.Status == StatusAccepted && c.Label == NoneLabel):
		case want["phi"] && c.PHI:
		case want["review"] && c.Status == StatusReview:
		case want["error"] && c.Status == StatusError:
		case c.Label != "" && c.Label != NoneLabel && want[c.Label]:
		case c.Group != "" && c.Group != noneGroup && want[c.Group]:
		default:
			continue
		}
		out = append(out, c)
	}
	return out
}

// SortColumns orders results by table then column position is already kept; this orders by name for stable diffs.
func SortColumns(cs []ColumnResult) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Catalog != b.Catalog {
			return a.Catalog < b.Catalog
		}
		if a.Schema != b.Schema {
			return a.Schema < b.Schema
		}
		return a.Table < b.Table
	})
}
