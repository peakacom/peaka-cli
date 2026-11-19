package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/peakacom/peaka-cli/internal/config"
)

// A membership disclosure is never accepted for the person (N02, DEV-4250): a save refused with 409
// MEMBERSHIP_DISCLOSURE is sent once, nothing is accepted, the save is not repeated with its draft, and the command
// exits 7, even with --yes. Only --accept-disclosure with the exact fingerprint, or "accept" typed at a prompt, accepts.
func TestADisclosureIsNeverAcceptedForThePerson(t *testing.T) {
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		sent = append(sent, req.Method+" "+req.URL.Path+" "+string(body))
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"status":409,"code":"MEMBERSHIP_DISCLOSURE","detail":"Readers of this publication can tell who belongs to a sensitive group.",
			"params":{"field":"securityMode","fingerprint":"fp-1","discloses":"TRUE","items":[{"kind":"ROW_RULE_EXCEPTION","source":"c.s.t.ward"}],
			"objectKind":"SAVED_QUERY","objectId":"draft:d1","publisherUserId":"u1","draftId":"d1"}}`)
	}))
	defer srv.Close()
	previous, wasYes := rt, gf.yes
	defer func() { rt, gf.yes, gf.format = previous, wasYes, "" }()
	gf.format, gf.yes = "json", true // even --yes does not accept a disclosure
	rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Project: "AbCdEf12", Source: map[string]string{}},
		client: api.New(srv.URL, "test", time.Second), Out: io.Discard, Err: io.Discard}

	cmd := newQueriesCmd()
	cmd.SetArgs([]string{"create", "--name", "ward_counts", "--sql", "SELECT 1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	var p *api.Problem
	if !errors.As(err, &p) || p.Code != "MEMBERSHIP_DISCLOSURE" || p.ExitCode() != api.ExitConflict {
		t.Fatalf("the refusal is the command's error, exit 7: %v", err)
	}
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "POST /data/projects/AbCdEf12/queries ") {
		t.Fatalf("one save, nothing else: %q", sent)
	}
	for _, s := range sent {
		if strings.Contains(s, "membership-acceptances") || strings.Contains(s, "membershipDraftId") || strings.Contains(s, "fp-1") {
			t.Errorf("an acceptance or a retry with the draft was sent: %s", s)
		}
	}
}

// Only disclosure.go names the acceptance route: every acceptance goes through acceptanceGiven or the gov
// disclosures command, whose flag is required. The behavior is what the tests below check; this keeps a second path
// from appearing unnoticed.
func TestOnlyTheExplicitPathNamesTheAcceptanceRoute(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "membership-acceptances") && filepath.Base(path) != "disclosure.go" {
			t.Errorf("%s names the acceptance route", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

const testOrg = "11111111-1111-1111-1111-111111111111"

// disclosureServer answers each save from answers in turn (status and body), an acceptance with 200, and the reads
// a command makes first; sent is every request after those reads.
func disclosureServer(t *testing.T, answers ...string) (*httptest.Server, *[]string) {
	var sent []string
	next := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/organizations":
			fmt.Fprintf(w, `[{"id":%q,"name":"Org"}]`, testOrg)
			return
		case req.Method == http.MethodGet && req.URL.Path == "/data/projects/AbCdEf12/queries":
			fmt.Fprint(w, `[{"id":"q1","displayName":"ward_counts","queryType":"PLAIN"}]`)
			return
		case req.Method == http.MethodGet && req.URL.Path == "/data/projects/AbCdEf12/queries/q1" && len(sent) == 0:
			fmt.Fprint(w, `{"id":"q1","displayName":"ward_counts","inputQuery":"SELECT 1","queryType":"PLAIN"}`)
			return
		}
		sent = append(sent, strings.TrimSpace(req.Method+" "+req.URL.Path+" "+string(body)))
		if strings.HasSuffix(req.URL.Path, "/membership-acceptances") {
			fmt.Fprint(w, `{"id":"a1","fingerprint":"fp-1"}`)
			return
		}
		if next >= len(answers) {
			t.Errorf("unexpected request %s %s", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		status, answer, _ := strings.Cut(answers[next], " ")
		next++
		code := http.StatusOK
		fmt.Sscan(status, &code)
		if code >= 400 {
			w.Header().Set("Content-Type", "application/problem+json")
		}
		w.WriteHeader(code)
		fmt.Fprint(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv, &sent
}

const disclosure409 = `409 {"status":409,"code":"MEMBERSHIP_DISCLOSURE","detail":"Readers can tell who belongs to a sensitive group.",
	"params":{"field":"securityMode","fingerprint":"fp-1","discloses":"TRUE","items":[{"kind":"ROW_RULE_EXCEPTION","source":"c.s.t.ward","tags":[{"label":"HIV clinic"}]}],
	"objectKind":"SAVED_QUERY","objectId":"%s","publisherUserId":"u1"%s}}`

func runSave(t *testing.T, srv *httptest.Server, in string, tty bool, args ...string) (string, error) {
	t.Helper()
	previous, wasYes, wasTerm := rt, gf.yes, stderrIsTerminal
	t.Cleanup(func() { rt, gf.yes, gf.format, stderrIsTerminal = previous, wasYes, "", wasTerm })
	gf.format, gf.yes = "json", true
	stderrIsTerminal = func() bool { return tty }
	var out, errOut strings.Builder
	rt = &Runtime{Ctx: context.Background(), Settings: &config.Settings{Org: testOrg, Project: "AbCdEf12", Source: map[string]string{}},
		client: api.New(srv.URL, "test", time.Second), Out: &out, Err: &errOut, In: strings.NewReader(in), StdinTTY: tty}
	var cmd interface {
		SetArgs([]string)
		Execute() error
	}
	if args[0] == "gov" {
		g := newGovernanceCmd()
		g.SetOut(io.Discard)
		g.SetErr(io.Discard)
		cmd, args = g, args[1:]
	} else {
		q := newQueriesCmd()
		q.SetOut(io.Discard)
		q.SetErr(io.Discard)
		cmd = q
	}
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String() + errOut.String(), err
}

// The explicit flag with the exact fingerprint accepts as the publisher and sends the SAME create once more, with
// its draft; a flag naming another fingerprint accepts nothing.
func TestAcceptDisclosureOnCreate(t *testing.T) {
	create := []string{"create", "--name", "ward_counts", "--sql", "SELECT 1", "--access", "definer"}
	refused := fmt.Sprintf(disclosure409, "draft:d1", `,"draftId":"d1"`)

	srv, sent := disclosureServer(t, refused, `201 {"id":"q9","displayName":"ward_counts"}`)
	out, err := runSave(t, srv, "", false, append(create, "--accept-disclosure", "fp-1")...)
	if err != nil {
		t.Fatalf("accepted and saved: %v\n%s", err, out)
	}
	want := []string{
		`POST /data/projects/AbCdEf12/queries {"displayName":"ward_counts","inputQuery":"SELECT 1","securityMode":"DEFINER"}`,
		`POST /organizations/` + testOrg + `/governance/membership-acceptances {"fingerprint":"fp-1","objectId":"draft:d1","objectKind":"SAVED_QUERY","projectId":"AbCdEf12"}`,
		`POST /data/projects/AbCdEf12/queries {"displayName":"ward_counts","inputQuery":"SELECT 1","membershipDraftId":"d1","securityMode":"DEFINER"}`,
	}
	if strings.Join(*sent, "\n") != strings.Join(want, "\n") {
		t.Errorf("sent:\n%s\nwant:\n%s", strings.Join(*sent, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(out, "HIV clinic") || !strings.Contains(out, "fp-1") || !strings.Contains(out, "q9") {
		t.Errorf("what was accepted is shown, then the query: %s", out)
	}

	srv, sent = disclosureServer(t, refused)
	_, err = runSave(t, srv, "", false, append(create, "--accept-disclosure", "fp-OLD")...)
	if p := problemOf(err); p == nil || p.Code != "MEMBERSHIP_DISCLOSURE" || len(*sent) != 1 {
		t.Errorf("another fingerprint accepts nothing: %v %q", err, *sent)
	}

	// The same create run again with its draft: the server answers with the query an earlier attempt made.
	srv, sent = disclosureServer(t, refused, `409 {"status":409,"code":"CONFLICT","detail":"This save already created query q9.","params":{"field":"membershipDraftId","objectId":"q9"}}`,
		`200 {"id":"q9","displayName":"ward_counts"}`)
	out, err = runSave(t, srv, "", false, append(create, "--accept-disclosure", "fp-1")...)
	if err != nil || len(*sent) != 4 || (*sent)[3] != "GET /data/projects/AbCdEf12/queries/q9" || !strings.Contains(out, "already made query q9") {
		t.Errorf("an already made query is shown, not made again: %v %q\n%s", err, *sent, out)
	}

	// A draft that cannot be used: no further attempt; the person saves again without it.
	srv, sent = disclosureServer(t, refused, `409 {"status":409,"code":"CONFLICT","detail":"This draft has expired.","params":{"field":"membershipDraftId","reason":"DRAFT_EXPIRED"}}`)
	_, err = runSave(t, srv, "", false, append(create, "--accept-disclosure", "fp-1")...)
	if p := problemOf(err); p == nil || p.Param("reason") != "DRAFT_EXPIRED" || p.ExitCode() != api.ExitConflict || len(*sent) != 3 ||
		!strings.Contains(p.Hint(), "without --draft") {
		t.Errorf("an expired draft ends it: %v %q", err, *sent)
	}

	// --draft sends a refused create's draft again.
	srv, sent = disclosureServer(t, `201 {"id":"q9"}`)
	if _, err := runSave(t, srv, "", false, append(create, "--draft", "d1")...); err != nil || !strings.Contains((*sent)[0], `"membershipDraftId":"d1"`) {
		t.Errorf("--draft: %v %q", err, *sent)
	}
}

// An update is accepted by the query's id and sent again as it was: no draft.
func TestAcceptDisclosureOnUpdate(t *testing.T) {
	srv, sent := disclosureServer(t, fmt.Sprintf(disclosure409, "q1", ""), `200 {"id":"q1"}`)
	out, err := runSave(t, srv, "", false, "update", "ward_counts", "--access", "definer", "--accept-disclosure", "fp-1")
	if err != nil || len(*sent) != 3 {
		t.Fatalf("accepted and saved: %v %q\n%s", err, *sent, out)
	}
	if !strings.Contains((*sent)[1], `"objectId":"q1"`) || (*sent)[0] != (*sent)[2] || strings.Contains((*sent)[2], "membershipDraftId") ||
		!strings.Contains((*sent)[2], `"securityMode":"DEFINER"`) {
		t.Errorf("sent: %q", *sent)
	}
}

// On a terminal the disclosure is shown and accepted only by typing accept; anything else, --yes included, saves nothing.
func TestAcceptDisclosureAtThePrompt(t *testing.T) {
	refused := fmt.Sprintf(disclosure409, "draft:d1", `,"draftId":"d1"`)
	create := []string{"create", "--name", "ward_counts", "--sql", "SELECT 1", "--access", "definer"}

	srv, sent := disclosureServer(t, refused)
	out, err := runSave(t, srv, "y\n", true, create...)
	var exit *api.ExitError
	if !errors.As(err, &exit) || exit.Code != api.ExitConflict || len(*sent) != 1 || !strings.Contains(out, "HIV clinic") {
		t.Errorf("y is not accept: %v %q\n%s", err, *sent, out)
	}

	srv, sent = disclosureServer(t, refused, `201 {"id":"q9"}`)
	out, err = runSave(t, srv, "accept\n", true, create...)
	if err != nil || len(*sent) != 3 || !strings.Contains((*sent)[1], "membership-acceptances") || !strings.Contains(out, "Type accept") {
		t.Errorf("accept typed: %v %q\n%s", err, *sent, out)
	}

	defer func() { gf.noInput = false }()
	gf.noInput = true
	srv, sent = disclosureServer(t, refused)
	if _, err := runSave(t, srv, "accept\n", true, create...); problemOf(err) == nil || len(*sent) != 1 {
		t.Errorf("--no-input asks nothing: %v %q", err, *sent)
	}
}

// gov disclosures accept records an acceptance only with --accept-disclosure, and sends that fingerprint.
func TestGovDisclosuresAccept(t *testing.T) {
	srv, sent := disclosureServer(t)
	if _, err := runSave(t, srv, "", false, "gov", "disclosures", "accept", "--object-kind", "SAVED_QUERY", "--object-id", "q1"); err == nil || len(*sent) != 0 {
		t.Errorf("without the flag (--yes is set) nothing is sent: %v %q", err, *sent)
	}
	srv, sent = disclosureServer(t)
	_, err := runSave(t, srv, "", false, "gov", "disclosures", "accept", "--object-kind", "SAVED_QUERY", "--object-id", "q1",
		"--accept-disclosure", "fp-1", "-f", "fingerprint=fp-OTHER")
	if err != nil || len(*sent) != 1 || (*sent)[0] != `POST /organizations/`+testOrg+`/governance/membership-acceptances {"fingerprint":"fp-1","objectId":"q1","objectKind":"SAVED_QUERY","projectId":"AbCdEf12"}` {
		t.Errorf("the flag's fingerprint is accepted: %v %q", err, *sent)
	}
}

// Under `peaka mcp serve` without --allow-governance-writes (GuardEnv=governance) an acceptance never reaches the
// server, even with the exact fingerprint: the save may be refused for its disclosure, the acceptance is refused by
// the guard (exit 5) and nothing is sent again; gov disclosures accept is refused the same way.
func TestTheMCPGuardBlocksAnAcceptance(t *testing.T) {
	t.Setenv(api.GuardEnv, "governance")
	refused := fmt.Sprintf(disclosure409, "draft:d1", `,"draftId":"d1"`)

	srv, sent := disclosureServer(t, refused)
	_, err := runSave(t, srv, "", false, "create", "--name", "ward_counts", "--sql", "SELECT 1", "--access", "definer", "--accept-disclosure", "fp-1")
	var exit *api.ExitError
	if !errors.As(err, &exit) || exit.Code != api.ExitPermission || !strings.Contains(err.Error(), "allow-governance-writes") {
		t.Errorf("the guard refuses the acceptance: %v", err)
	}
	if len(*sent) != 1 || !strings.HasPrefix((*sent)[0], "POST /data/projects/AbCdEf12/queries ") {
		t.Errorf("only the save reached the server: %q", *sent)
	}

	srv, sent = disclosureServer(t)
	_, err = runSave(t, srv, "", false, "gov", "disclosures", "accept", "--object-kind", "SAVED_QUERY", "--object-id", "q1", "--accept-disclosure", "fp-1")
	if !errors.As(err, &exit) || exit.Code != api.ExitPermission || len(*sent) != 0 {
		t.Errorf("gov disclosures accept is refused under the guard: %v %q", err, *sent)
	}
}
