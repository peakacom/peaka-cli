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
// exits 7. Accepting will take an explicit flag once the gateway has the route.
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

// No code outside the tests names the acceptance route or the draft field: nothing can accept by itself. When the
// gateway has the route, its one caller must sit behind an explicit flag, and this test is changed with it.
func TestNoCodeSendsAnAcceptance(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, s := range []string{"membership-acceptances", "membershipDraftId"} {
			if strings.Contains(string(b), s) {
				t.Errorf("%s names %s", path, s)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
