package trino

import (
	"fmt"
	"strings"
)

// QueryError is Trino's error object on a failed query.
type QueryError struct {
	Message       string         `json:"message"`
	ErrorCode     int            `json:"errorCode"`
	ErrorName     string         `json:"errorName"`
	ErrorType     string         `json:"errorType"`
	ErrorLocation *ErrorLocation `json:"errorLocation"`
	FailureInfo   map[string]any `json:"failureInfo"`
	QueryID       string         `json:"-"`
}

type ErrorLocation struct {
	LineNumber   int `json:"lineNumber"`
	ColumnNumber int `json:"columnNumber"`
}

func (e *QueryError) Error() string {
	if e.ErrorName != "" {
		return fmt.Sprintf("%s: %s", e.ErrorName, e.Message)
	}
	return e.Message
}

// Canceled reports whether the query was stopped by the user.
func (e *QueryError) Canceled() bool { return e.ErrorName == "USER_CANCELED" }

// HTTPError is a non-200 answer that is not a Trino result page: on dbc the
// proxy's own refusals arrive this way, as problem+json.
type HTTPError struct {
	Status      int
	ContentType string
	Body        []byte
}

func (e *HTTPError) Error() string {
	b := strings.TrimSpace(string(e.Body))
	if len(b) > 200 {
		b = b[:200] + "…"
	}
	return fmt.Sprintf("HTTP %d from the SQL endpoint: %s", e.Status, b)
}

// TransportError is a network failure before any answer.
type TransportError struct{ Err error }

func (e *TransportError) Error() string { return "SQL endpoint unreachable: " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }
