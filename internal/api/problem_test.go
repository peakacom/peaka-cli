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

func TestGovernanceGuard(t *testing.T) {
	for path, refused := range map[string]bool{
		"/organizations/o/governance/tags":                      true,
		"/organizations/o/governance/policy":                    true,
		"/organizations/o/groups/g/members/u":                   true,
		"/organizations/o/workspaces/w/projects/p/grants":       true,
		"/organizations/o/attributes/definitions":               true,
		"/organizations/o/governance/tag-suggestions":           false,
		"/organizations/o/governance/tag-suggestions/s/approve": true,
		"/projects/p/queries":                                   false,
		"organizations/o/governance/row-rules?reason=x":         true,
	} {
		if got := governanceGuard("governance", "POST", path) != ""; got != refused {
			t.Errorf("%s: refused %v, want %v", path, got, refused)
		}
		if governanceGuard("", "POST", path) != "" {
			t.Errorf("%s: refused without the guard", path)
		}
	}
}

// DEV-4250: the governance refusals of a save or a refresh are conflicts (exit 7) with a hint that says what they need.
func TestGovernanceConflictHints(t *testing.T) {
	for _, code := range []string{"PUBLICATION_LAPSED", "MEMBERSHIP_DISCLOSURE"} {
		if got := (&Problem{Code: code, Status: 409}).ExitCode(); got != ExitConflict {
			t.Errorf("%s: exit %d, want %d", code, got, ExitConflict)
		}
	}
	lapsed := func(requires string) string {
		return (&Problem{Code: "PUBLICATION_LAPSED", Status: 409, Params: map[string]any{"reason": "PUBLICATION_LAPSED", "requires": requires}}).Hint()
	}
	for requires, want := range map[string]string{
		"GOVERNANCE_APPROVAL":  "approval",
		"AUTHOR_PERMISSION":    "publish_definer",
		"NOT_A_MEMBER":         "no longer a member",
		"NO_PROJECT_ACCESS":    "can no longer run queries in this project",
		"SUSPENDED":            "suspended",
		"SOURCE_IMPERSONATION": "directory user",
		"":                     "publish it again",
	} {
		if h := lapsed(requires); !strings.Contains(h, want) || !strings.Contains(h, "each reader's access") {
			t.Errorf("requires %q: hint %q", requires, h)
		}
	}

	disclosure := &Problem{Code: "MEMBERSHIP_DISCLOSURE", Status: 409, Params: map[string]any{
		"fingerprint": "f1", "discloses": "TRUE", "items": []any{map[string]any{"kind": "ROW_RULE"}, map[string]any{"kind": "TAG"}},
		"objectKind": "MATERIALIZED_QUERY", "objectId": "draft:d1", "draftId": "d1"}}
	if h := disclosure.Hint(); !strings.Contains(h, "2 reasons") || !strings.Contains(h, "accept") || !strings.Contains(h, "Studio") {
		t.Errorf("disclosure hint: %q", h)
	}
	disclosure.Params["discloses"], disclosure.Params["items"] = "UNKNOWN", []any{map[string]any{"kind": "UNKNOWN"}}
	if h := disclosure.Hint(); !strings.Contains(h, "may be able") || !strings.Contains(h, "1 reason)") {
		t.Errorf("unknown disclosure hint: %q", h)
	}

	notStopped := &Problem{Code: "CONFLICT", Status: 409, Params: map[string]any{"reason": "REFRESH_NOT_STOPPED", "resource": "query:q1"}}
	if h := notStopped.Hint(); notStopped.ExitCode() != ExitConflict || !strings.Contains(h, "could not be confirmed stopped") ||
		!strings.Contains(h, "status") || strings.Contains(h, "nothing was changed") {
		t.Errorf("REFRESH_NOT_STOPPED: exit %d, hint %q", notStopped.ExitCode(), notStopped.Hint())
	}
	for name, running := range map[string]*Problem{
		"by reason":                    {Code: "CONFLICT", Status: 409, Detail: "Bu sorgu zaten yenileniyor.", Params: map[string]any{"reason": "REFRESH_ALREADY_RUNNING"}},
		"older service, by its detail": {Code: "CONFLICT", Status: 409, Detail: "This cache is already being refreshed; try again when the running refresh ends."},
	} {
		if running.ExitCode() != ExitConflict || !strings.Contains(running.Hint(), "already running") {
			t.Errorf("a refresh already running %s: exit %d, hint %q", name, running.ExitCode(), running.Hint())
		}
	}
	other := &Problem{Code: "CONFLICT", Status: 409, Detail: "This cache is already being refreshed.", Params: map[string]any{"reason": "SOMETHING_ELSE"}}
	if strings.Contains(other.Hint(), "already running") {
		t.Error("a conflict with another reason is not read from its detail")
	}
	if (&Problem{Code: "CONFLICT", Status: 409, Detail: "stale version"}).Hint() != "" {
		t.Error("another conflict gets no refresh hint")
	}

	var buf bytes.Buffer
	(&Problem{Code: "PUBLICATION_LAPSED", Status: 409, Detail: "q runs with its publisher's access…", Params: map[string]any{"requires": "NOT_A_MEMBER"}}).WriteHuman(&buf, false)
	if !strings.Contains(buf.String(), "  hint   the publisher is no longer a member") {
		t.Errorf("human: %q", buf.String())
	}
}

// A disclosure is shown, never accepted: each item, its tags and the fingerprint are printed for the person.
func TestMembershipDisclosureIsShown(t *testing.T) {
	p := &Problem{Code: "MEMBERSHIP_DISCLOSURE", Status: 409, Detail: "Readers of this publication can tell who belongs to a sensitive group…",
		Params: map[string]any{"fingerprint": "fp-123", "discloses": "TRUE", "objectKind": "SAVED_QUERY", "objectId": "q1",
			"items": []any{map[string]any{"kind": "ROW_RULE_EXCEPTION", "source": "crm.public.patients.ward", "via": "SAVED_QUERY:q0",
				"tags": []any{map[string]any{"label": "HIV clinic"}}}}}}
	var buf bytes.Buffer
	p.WriteHuman(&buf, false)
	for _, want := range []string{"  disclosed  ROW_RULE_EXCEPTION crm.public.patients.ward via SAVED_QUERY:q0 (tags: HIV clinic)\n",
		"  fingerprint  fp-123\n", "  hint   readers can tell"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in %q", want, buf.String())
		}
	}
	if (&Problem{Code: "CONFLICT", Params: map[string]any{"fingerprint": "x"}}).disclosureLines() != nil {
		t.Error("only a disclosure is shown this way")
	}
}
