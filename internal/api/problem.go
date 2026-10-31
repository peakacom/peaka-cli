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

// Hint is the CLI's one-line suggestion for the code, or "".
func (p *Problem) Hint() string { return hintByCode[p.Code] }

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
