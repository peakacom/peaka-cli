package cli

import (
	"bufio"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/peakacom/peaka-cli/internal/api"
)

// Membership disclosure (K5, DEV-4250). A save of a query that runs with its author's access (securityMode DEFINER)
// is refused with 409 MEMBERSHIP_DISCLOSURE when its readers could tell who belongs to a sensitive group. Only the
// publisher accepts that, and only by an explicit act: --accept-disclosure naming the exact fingerprint, or typing
// "accept" at a prompt on a terminal after the disclosure is shown. --yes never accepts. An accepted create is sent
// again once, the same create with its membershipDraftId; an update is sent again once as it was.

// stderrIsTerminal: whether a person reads stderr (a prompt is only shown to one). A variable for the tests.
var stderrIsTerminal = func() bool { return api.IsTerminal(os.Stderr) }

// acceptancePath is the public route that records an acceptance; it is the only place the CLI names it.
const acceptancePath = "/organizations/%s/governance/membership-acceptances"

// disclosureFlags are the save's flags: who it runs as, and the explicit acceptance.
func disclosureFlags(create bool) []flagSpec {
	flags := []flagSpec{
		{Name: "access", Help: "whose access it runs with: invoker (each reader's) or definer (its publisher's)",
			Set: func(v any, b map[string]any) { b["securityMode"] = strings.ToUpper(fmt.Sprint(v)) }},
		{Name: "accept-disclosure", In: "path", Key: "acceptDisclosure",
			Help: "as its publisher, accept the membership disclosure with this fingerprint (shown by the refused save) and save again"},
	}
	if create {
		flags = append(flags, flagSpec{Name: "draft", Key: "membershipDraftId",
			Help: "the draft a refused create named (params.draftId): send the same create again with it"})
	}
	return flags
}

// saveQuery sends a query create or update, and answers a membership disclosure or a draft refusal.
func saveQuery(create bool) func(o opSpec, c *callCtx, path string) error {
	return func(o opSpec, c *callCtx, path string) error {
		send := func() error { return rt.call(o.Method, path, c.Query, c.Body, o.Unwrap, o.Fields, o.Empty, o.Waits) }
		err := send()
		p := problemOf(err)
		if p == nil {
			return err
		}
		if create {
			if done, err := answerDraft(c, p); done {
				return err
			}
		}
		if p.Code != "MEMBERSHIP_DISCLOSURE" {
			return err
		}
		accepted, aerr := acceptanceGiven(p, c.Params["acceptDisclosure"])
		if aerr != nil || !accepted {
			if aerr != nil {
				return aerr
			}
			return err
		}
		if err := acceptDisclosure(p.Param("objectKind"), p.Param("objectId"), p.Param("fingerprint")); err != nil {
			return err
		}
		rt.note("Accepted disclosure %s for %s; saving again.", p.Param("fingerprint"), p.Param("objectId"))
		if create {
			if d := p.Param("draftId"); d != "" {
				c.Body["membershipDraftId"] = d
			}
		}
		err = send() // once: a second disclosure is the person's to review again
		if p2 := problemOf(err); p2 != nil && create {
			if done, err := answerDraft(c, p2); done {
				return err
			}
		}
		return err
	}
}

func problemOf(err error) *api.Problem {
	var p *api.Problem
	if errors.As(err, &p) {
		return p
	}
	return nil
}

// answerDraft: a create refused for its draft. One the server says it already made is read and shown, never made
// again; any other draft refusal is the problem (its hint says to save again without --draft, or wait).
func answerDraft(c *callCtx, p *api.Problem) (bool, error) {
	if p.Status != http.StatusConflict || p.Param("field") != "membershipDraftId" {
		return false, nil
	}
	if id := p.Param("objectId"); id != "" && p.Param("reason") == "" {
		rt.note("This create already made query %s; showing it, not creating it again.", id)
		return true, rt.call(http.MethodGet, strings.Replace(c.Path, "{project}", c.Params["project"], 1)+"/"+id, nil, nil, "", nil, "")
	}
	return true, p
}

// acceptanceGiven decides whether the person accepted this disclosure: the flag naming its exact fingerprint, or
// "accept" typed at a prompt on a terminal after the disclosure is shown. Nothing else accepts, --yes included.
func acceptanceGiven(p *api.Problem, flag string) (bool, error) {
	fp := p.Param("fingerprint")
	if fp == "" {
		return false, nil
	}
	if flag != "" {
		if flag != fp {
			rt.note("--accept-disclosure names %s, but this save discloses %s: nothing was accepted. Review it, then accept that one.", flag, fp)
			return false, nil
		}
		showDisclosure(p)
		return true, nil
	}
	if !rt.StdinTTY || !stderrIsTerminal() || gf.noInput {
		return false, nil
	}
	showDisclosure(p)
	fmt.Fprint(rt.Err, "As its publisher, accept this disclosure and save again? Type accept to accept: ")
	line, _ := bufio.NewReader(rt.In).ReadString('\n')
	if strings.TrimSpace(line) != "accept" {
		return false, api.Exit(api.ExitConflict, "the disclosure was not accepted; nothing was saved")
	}
	return true, nil
}

// showDisclosure tells the person what is being accepted: whole on a terminal, its items and fingerprint as notes
// otherwise (stderr stays free of a problem line for a save that then succeeds).
func showDisclosure(p *api.Problem) {
	if stderrIsTerminal() {
		p.WriteHuman(rt.Err, false)
		return
	}
	rt.note("%s", p.Detail)
	for _, l := range p.DisclosureLines() {
		rt.note("  %s", l)
	}
}

// acceptDisclosure records the acceptance, as the caller, for the project in context.
func acceptDisclosure(objectKind, objectID, fingerprint string) error {
	org, err := rt.orgID()
	if err != nil {
		return err
	}
	project, err := rt.sqlProjectID()
	if err != nil {
		return err
	}
	client, err := rt.api()
	if err != nil {
		return err
	}
	body := map[string]any{"projectId": project, "objectKind": objectKind, "objectId": objectID, "fingerprint": fingerprint}
	_, err = client.Do(rt.Ctx, http.MethodPost, fmt.Sprintf(acceptancePath, org), nil, body, nil)
	return err
}

// disclosureAcceptSpec: an acceptance recorded on its own, for a save refused earlier (a script that reviews first).
func disclosureAcceptSpec() opSpec {
	return opSpec{Use: "accept", Short: "As its publisher, accept what a query discloses (the fingerprint a refused save named)",
		Long: `A save of a query that runs with its publisher's access is refused with 409 MEMBERSHIP_DISCLOSURE when its
readers could tell who belongs to a sensitive group. Review what the refused save showed (its items and tags), then
accept it by its exact fingerprint; --accept-disclosure is the acceptance, --yes is not. Then save again: a create
with --draft <draftId>. Or pass --accept-disclosure to the save itself.

  peaka gov disclosures accept --object-kind MATERIALIZED_QUERY --object-id draft:6f1c… --accept-disclosure 9b2e…`,
		Method: http.MethodPost, Path: govPath + "/membership-acceptances", Body: true,
		Flags: []flagSpec{
			{Name: "object-kind", Help: "params.objectKind: SAVED_QUERY or MATERIALIZED_QUERY", Required: true},
			{Name: "object-id", Help: "params.objectId: the query's id, or draft:<draftId> for one being created", Required: true},
			{Name: "accept-disclosure", In: "path", Key: "acceptDisclosure", Required: true,
				Help: "the fingerprint the refused save named; giving it is the acceptance"},
		},
		Prepare: func(c *callCtx) error {
			project, err := rt.sqlProjectID()
			if err != nil {
				return err
			}
			// What is accepted is the flag's fingerprint, whatever -f or --from-file say.
			c.Body["fingerprint"] = c.Params["acceptDisclosure"]
			c.Body["projectId"] = project
			return nil
		}}
}
