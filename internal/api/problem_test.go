package api

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

func TestExitCodeByProblemCode(t *testing.T) {
	cases := map[string]int{
		"AUTHENTICATION_REQUIRED": ExitAuth, "API_KEY_INVALID": ExitAuth,
		"NOT_FOUND": ExitNotFound, "ROUTE_NOT_FOUND": ExitNotFound,
		"PERMISSION_REQUIRED": ExitPermission, "OUTSIDE_KEY_SCOPE": ExitPermission,
		"VALIDATION_FAILED": ExitInvalid, "UNKNOWN_CONNECTION_TYPE": ExitInvalid,
		"CONFLICT": ExitConflict, "QUERY_FAILED": ExitFailed, "QUERY_TIMEOUT": ExitFailed,
		"RATE_LIMITED": ExitRetryable, "UPSTREAM_UNAVAILABLE": ExitRetryable, "INTERNAL_ERROR": ExitFailure,
	}
	for code, want := range cases {
		if got := (&Problem{Code: code}).ExitCode(); got != want {
			t.Errorf("%s: exit %d, want %d", code, got, want)
		}
	}
	if got := (&Problem{Code: "SOMETHING_NEW", Retryable: true}).ExitCode(); got != ExitRetryable {
		t.Errorf("unknown retryable code should be %d, got %d", ExitRetryable, got)
	}
	if got := (&Problem{Code: "SOMETHING_NEW", Status: 404}).ExitCode(); got != ExitNotFound {
		t.Errorf("unknown code falls back to the status: got %d", got)
	}
}

func TestParseProblemKeepsBodyAndFillsGaps(t *testing.T) {
	body := []byte(`{"type":"https://docs.peaka.com/errors/route-not-found","title":"No such endpoint","status":404,"detail":"There is no such endpoint.","code":"ROUTE_NOT_FOUND","traceId":"abc","retryable":false}`)
	resp := &http.Response{StatusCode: 404, Header: http.Header{"Content-Type": {"application/problem+json"}}}
	p := parseProblem(resp, body)
	if p.Code != "ROUTE_NOT_FOUND" || p.TraceID != "abc" || p.Status != 404 {
		t.Fatalf("parsed: %+v", p)
	}
	var buf bytes.Buffer
	p.WriteJSON(&buf)
	if strings.TrimSpace(buf.String()) != string(body) {
		t.Fatalf("json passthrough changed the body: %s", buf.String())
	}

	// A non-problem 503 (e.g. from an ingress) becomes a retryable UPSTREAM_UNAVAILABLE.
	resp = &http.Response{StatusCode: 503, Header: http.Header{"Retry-After": {"7"}}}
	p = parseProblem(resp, []byte("Service Unavailable\n"))
	if p.Code != "UPSTREAM_UNAVAILABLE" || !p.Retryable || p.RetryAfter != 7 || p.Detail != "Service Unavailable" {
		t.Fatalf("synthesized: %+v", p)
	}
	buf.Reset()
	p.WriteHuman(&buf, false)
	if !strings.HasPrefix(buf.String(), "ERROR  UPSTREAM_UNAVAILABLE  (503)\n  Service Unavailable\n  retry  after 7 s\n") {
		t.Fatalf("human: %q", buf.String())
	}
}
