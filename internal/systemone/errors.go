package systemone

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/peakacom/peaka-cli/internal/api"
)

// FieldError is one entry of a 422 body's detail list (pydantic style).
type FieldError struct {
	Loc  []any  `json:"loc"`
	Msg  string `json:"msg"`
	Type string `json:"type"`
}

// Path renders Loc as a.b.c, without pydantic's leading "body".
func (f FieldError) Path() string {
	parts := make([]string, 0, len(f.Loc))
	for i, l := range f.Loc {
		if i == 0 && l == "body" {
			continue
		}
		parts = append(parts, fmt.Sprint(l))
	}
	return strings.Join(parts, ".")
}

// Error is anything the request failed on: a refusal from the server (Status
// set), a transport failure or an exhausted retry (Retryable), or a request
// rejected client-side before it was sent (Status 0, not Retryable).
type Error struct {
	Status    int          // HTTP status; 0 when nothing was received
	Detail    string       // the server's detail, or the client's reason
	Fields    []FieldError // 422 field errors
	Retryable bool         // transient: the same request may succeed later
	Attempts  int          // how many times the request was sent
	Err       error        // transport cause, when there is one
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("systemone: ")
	if e.Status == 0 {
		switch {
		case e.Err != nil && e.Attempts > 1:
			fmt.Fprintf(&b, "%s after %d attempts: %v", e.Detail, e.Attempts, e.Err)
		case e.Err != nil:
			fmt.Fprintf(&b, "%s: %v", e.Detail, e.Err)
		default:
			b.WriteString(e.Detail)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "%d", e.Status)
	if t := http.StatusText(e.Status); t != "" {
		b.WriteString(" " + t)
	}
	if e.Attempts > 1 {
		fmt.Fprintf(&b, " after %d attempts", e.Attempts)
	}
	if len(e.Fields) > 0 {
		parts := make([]string, 0, len(e.Fields))
		for _, f := range e.Fields {
			if p := f.Path(); p != "" {
				parts = append(parts, p+": "+f.Msg)
			} else {
				parts = append(parts, f.Msg)
			}
		}
		b.WriteString(": " + strings.Join(parts, "; "))
	} else if e.Detail != "" {
		b.WriteString(": " + e.Detail)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode maps the failure onto the CLI's exit codes.
func (e *Error) ExitCode() int {
	if e.Retryable {
		return api.ExitRetryable
	}
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return api.ExitAuth
	case 0, http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return api.ExitInvalid
	}
	return api.ExitFailed
}

// parseError turns a non-200 answer into an Error. The body is
// {"detail": string | object | [field errors]} when the server wrote it; a
// proxy's HTML or text is reduced to its first line.
func parseError(status int, data []byte) *Error {
	e := &Error{Status: status}
	switch status {
	case http.StatusTooManyRequests, 529, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		e.Retryable = true
	}
	var body struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Valid(data) && json.Unmarshal(data, &body) == nil && len(body.Detail) > 0 {
		raw := bytes.TrimSpace(body.Detail)
		switch raw[0] {
		case '"':
			_ = json.Unmarshal(raw, &e.Detail)
		case '[':
			var fields []FieldError
			if json.Unmarshal(raw, &fields) == nil && len(fields) > 0 && fields[0].Msg != "" {
				e.Fields = fields
			} else {
				e.Detail = compact(raw)
			}
		case 'n':
		default:
			e.Detail = compact(raw)
		}
		return e
	}
	e.Detail = firstLine(data)
	return e
}

func compact(raw []byte) string {
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return string(raw)
	}
	return buf.String()
}

func firstLine(b []byte) string {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	}
	if len(b) > 200 {
		b = b[:200]
	}
	return strings.TrimSpace(string(b))
}
