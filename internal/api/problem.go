package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Problem is the gateway's application/problem+json body, kept as it came.
type Problem struct {
	Type       string          `json:"type,omitempty"`
	Title      string          `json:"title,omitempty"`
	Status     int             `json:"status,omitempty"`
	Detail     string          `json:"detail,omitempty"`
	Instance   string          `json:"instance,omitempty"`
	Code       string          `json:"code,omitempty"`
	Retryable  bool            `json:"retryable"`
	RetryAfter int             `json:"retryAfter,omitempty"`
	TraceID    string          `json:"traceId,omitempty"`
	Errors     []FieldError    `json:"errors,omitempty"`
	Params     map[string]any  `json:"params,omitempty"`
	Cause      json.RawMessage `json:"cause,omitempty"`

	// Raw is the body as received, so `--format json` can pass it through unchanged.
	Raw json.RawMessage `json:"-"`
}

// FieldError is one entry of errors[] on VALIDATION_FAILED.
type FieldError struct {
	Field   string `json:"field,omitempty"`
	Pointer string `json:"pointer,omitempty"`
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`
}

func (p *Problem) Error() string {
	if p.Detail != "" {
		return fmt.Sprintf("%s: %s", p.Code, p.Detail)
	}
	return fmt.Sprintf("%s (HTTP %d)", p.Code, p.Status)
}

// ExitCode maps the problem code to the CLI's exit code.
func (p *Problem) ExitCode() int {
	if c, ok := exitByCode[p.Code]; ok {
		return c
	}
	if p.Retryable {
		return ExitRetryable
	}
	switch p.Status {
	case 401:
		return ExitAuth
	case 403:
		return ExitPermission
	case 404:
		return ExitNotFound
	case 409:
		return ExitConflict
	case 400, 422:
		return ExitInvalid
	}
	return ExitFailure
}

// Hint is the CLI's one-line suggestion for the problem, or "". Some codes are answered by their params.
func (p *Problem) Hint() string {
	switch {
	case p.Code == "PUBLICATION_LAPSED":
		return publicationLapsedHint(p.param("requires"))
	case p.Code == "MEMBERSHIP_DISCLOSURE":
		return membershipDisclosureHint(p)
	case p.param("field") == "membershipDraftId":
		return draftHint(p)
	case p.param("reason") == "REFRESH_NOT_STOPPED":
		// Not "nothing was changed": an older service, or a later stage of the change, may have changed something.
		return "the running refresh could not be confirmed stopped; check the query's status (`peaka queries status ID`) before retrying"
	case p.param("reason") == "REFRESH_ALREADY_RUNNING",
		// An older service says it only in the detail.
		p.param("reason") == "" && p.Status == 409 && strings.Contains(p.Detail, "already being refreshed"):
		return "a refresh is already running; wait for it to end (`peaka queries status ID`), then try again"
	}
	return hintByCode[p.Code]
}

// Param is a string parameter of the problem, or "".
func (p *Problem) Param(key string) string { return p.param(key) }

// param is a string parameter of the problem, or "".
func (p *Problem) param(key string) string {
	s, _ := p.Params[key].(string)
	return s
}

// publicationLapsedHint says what a refresh refused with PUBLICATION_LAPSED needs, by params.requires.
func publicationLapsedHint(requires string) string {
	const otherwise = "; or switch the query to run with each reader's access"
	switch requires {
	case "GOVERNANCE_APPROVAL", "APPROVAL":
		return "it needs a governance admin's approval for the query as it is now: request publishing again in Studio; admins see requests in `peaka gov publications list`" + otherwise
	case "AUTHOR_PERMISSION":
		return "the publisher needs the publish_definer permission on the organization: ask an admin for it" + otherwise
	case "NOT_A_MEMBER":
		return "the publisher is no longer a member of the organization: restore their membership, or publish it again as a current member (a save keeps the original publisher)" + otherwise
	case "NO_PROJECT_ACCESS":
		return "the publisher can no longer run queries in this project: give them access again, or have someone with access publish it again" + otherwise
	case "SUSPENDED":
		return "the publisher's account is suspended: reactivate it, or publish it again as a current member (a save keeps the original publisher)" + otherwise
	case "SOURCE_IMPERSONATION":
		return "a source signs in as each directory user, so the query cannot run with its publisher's access" + otherwise
	}
	return "publish it again; `peaka gov publications list -f status=ALL` shows its request" + otherwise
}

// draftHint answers a create refused for its draft: already made, still being saved, or a draft that cannot be used.
func draftHint(p *Problem) string {
	switch reason := p.param("reason"); {
	case reason == "" && p.param("objectId") != "":
		return "an earlier attempt of this create already made query " + p.param("objectId") + ": read it (`peaka queries describe " +
			p.param("objectId") + "`); do not create it again"
	case reason == "DRAFT_IN_USE":
		return "an earlier attempt of this create is still being saved: try again in a moment with the same --draft"
	case strings.HasPrefix(reason, "DRAFT_"):
		return "this draft cannot be used (" + reason + "): run the same create again without --draft; a new disclosure may follow"
	}
	return ""
}

// membershipDisclosureHint explains a 409 MEMBERSHIP_DISCLOSURE: the publisher must accept the disclosure first.
func membershipDisclosureHint(p *Problem) string {
	what := "readers can tell who belongs to a sensitive group"
	if p.param("discloses") == "UNKNOWN" {
		what = "readers may be able to tell who belongs to a sensitive group (it could not all be checked)"
	}
	if items, ok := p.Params["items"].([]any); ok && len(items) > 0 {
		what += fmt.Sprintf(" (%d %s)", len(items), plural(len(items), "reason", "reasons"))
	}
	accept := "--accept-disclosure " + p.param("fingerprint")
	if d := p.param("draftId"); d != "" {
		accept = "--draft " + d + " " + accept
	}
	return what + "; nothing was saved. Only the publisher accepts it, after reviewing what is disclosed: run the same save again with " +
		accept + " (or `peaka gov disclosures accept`)"
}

// DisclosureLines show what a MEMBERSHIP_DISCLOSURE says is disclosed: each item (its kind, source and the tags it
// names) and the fingerprint an acceptance would record. The CLI never accepts a disclosure itself: it only shows it.
func (p *Problem) DisclosureLines() []string {
	if p.Code != "MEMBERSHIP_DISCLOSURE" {
		return nil
	}
	var lines []string
	items, _ := p.Params["items"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		kind, _ := m["kind"].(string)
		line := "disclosed  " + kind
		if src, _ := m["source"].(string); src != "" {
			line += " " + src
		}
		if via, _ := m["via"].(string); via != "" {
			line += " via " + via
		}
		var tags []string
		ts, _ := m["tags"].([]any)
		for _, t := range ts {
			tm, _ := t.(map[string]any)
			if label, _ := tm["label"].(string); label != "" {
				tags = append(tags, label)
			}
		}
		if len(tags) > 0 {
			line += " (tags: " + strings.Join(tags, ", ") + ")"
		}
		if reason, _ := m["reason"].(string); reason != "" {
			line += ": " + reason
		}
		lines = append(lines, line)
	}
	if f := p.param("fingerprint"); f != "" {
		lines = append(lines, "fingerprint  "+f)
	}
	return lines
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// WriteHuman renders the problem for a person (stderr, TTY).
func (p *Problem) WriteHuman(w io.Writer, verbose bool) {
	code := p.Code
	if code == "" {
		code = "HTTP_ERROR"
	}
	fmt.Fprintf(w, "ERROR  %s  (%d)\n", code, p.Status)
	if p.Detail != "" {
		fmt.Fprintf(w, "  %s\n", p.Detail)
	}
	for _, e := range p.Errors {
		field := e.Field
		if field == "" {
			field = e.Pointer
		}
		fmt.Fprintf(w, "  field  %s: %s\n", field, e.Message)
	}
	if p.RetryAfter > 0 {
		fmt.Fprintf(w, "  retry  after %d s\n", p.RetryAfter)
	}
	if p.TraceID != "" {
		fmt.Fprintf(w, "  trace  %s\n", p.TraceID)
	}
	for _, d := range p.DisclosureLines() {
		fmt.Fprintf(w, "  %s\n", d)
	}
	if h := p.Hint(); h != "" {
		fmt.Fprintf(w, "  hint   %s\n", h)
	}
	if verbose && len(p.Cause) > 0 {
		fmt.Fprintf(w, "  cause  %s\n", strings.TrimSpace(string(p.Cause)))
	}
}

// WriteJSON writes the body as received (or a synthesized one) on one line.
func (p *Problem) WriteJSON(w io.Writer) {
	if len(p.Raw) > 0 && json.Valid(p.Raw) {
		var buf bytes.Buffer
		if err := json.Compact(&buf, p.Raw); err == nil {
			fmt.Fprintln(w, buf.String())
			return
		}
	}
	b, _ := json.Marshal(p)
	fmt.Fprintln(w, string(b))
}
