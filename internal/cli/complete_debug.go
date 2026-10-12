package cli

import (
	"fmt"
	"time"

	"github.com/peakacom/peaka-cli/internal/sqltext"
)

// debugComplete prints what the completer would offer at the end of text,
// waiting for the metadata the candidates need. A development aid for
// `peaka sql --complete "SELECT * FROM stripe.pay"`.
func debugComplete(s *sqlSession, text string) error {
	r := &repl{s: s, md: newMetadata(rt, s.project)}
	changed := make(chan struct{}, 64)
	r.md.onChange = func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	ctx := sqltext.Analyze(text, len([]rune(text)))
	fmt.Fprintf(rt.Err, "place=%v word=%q prev=%q tables=%v\n", ctx.Place, ctx.Word, ctx.Prev, ctx.Tables)
	var last []candidate
	deadline := time.After(8 * time.Second)
	for {
		var cands []candidate
		if ctx.Meta {
			cands = r.metaCandidates(ctx)
		} else {
			cands = r.sqlCandidates(ctx)
		}
		var kept []candidate
		for _, c := range cands {
			if matchesTyped(c.value, ctx.Word) {
				kept = append(kept, c)
			}
		}
		last = kept
		if !r.md.Loading() {
			break
		}
		select {
		case <-changed:
			continue
		case <-deadline:
			fmt.Fprintln(rt.Err, "metadata still loading after 8s; partial result")
		}
		break
	}
	rows := make([]record, 0, len(last))
	for _, c := range last {
		rows = append(rows, record{"value": c.value, "description": c.desc, "tag": c.tag})
	}
	return rt.printWith(rows, "tag", "value", "description")
}
