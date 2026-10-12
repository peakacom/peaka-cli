package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/peakacom/peaka-cli/internal/api"
)

// record is one API object; the CLI never renames its fields.
type record = map[string]any

// listOf decodes a list response; a single object becomes a one-element list.
func listOf(raw json.RawMessage) ([]record, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '[' {
		var rs []record
		return rs, decodeJSON(raw, &rs)
	}
	var r record
	if err := decodeJSON(raw, &r); err != nil {
		return nil, err
	}
	return []record{r}, nil
}

// decodeJSON decodes keeping numbers as json.Number, so ids and amounts are never rounded through float64.
func decodeJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(v)
}

// matchOne picks the record whose id equals ref, else the records whose name equals ref (case-insensitive).
func matchOne(rs []record, ref, what string) (record, error) {
	for _, r := range rs {
		if str(r["id"]) == ref {
			return r, nil
		}
	}
	var hits []record
	for _, r := range rs {
		if strings.EqualFold(str(r["name"]), ref) || strings.EqualFold(str(r["displayName"]), ref) {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return nil, api.Exit(api.ExitNotFound, "no %s with id or name %q", what, ref)
	}
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, str(h["id"]))
	}
	return nil, api.Exit(api.ExitInvalid, "%d %ss are named %q; use an id: %s", len(hits), what, ref, strings.Join(ids, ", "))
}

func (r *Runtime) listOrgs() ([]record, error) {
	client, err := r.api()
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := client.Get(r.Ctx, "/organizations", nil, &raw); err != nil {
		return nil, err
	}
	return listOf(raw)
}

// resolveOrg turns an id or name into the organization record.
func (r *Runtime) resolveOrg(ref string) (record, error) {
	orgs, err := r.listOrgs()
	if err != nil {
		return nil, err
	}
	return matchOne(orgs, ref, "organization")
}

// orgID returns the id of the org in context (resolving a name if needed).
func (r *Runtime) orgID() (string, error) {
	ref, err := r.needOrg()
	if err != nil {
		return "", err
	}
	org, err := r.resolveOrg(ref)
	if err != nil {
		return "", err
	}
	return str(org["id"]), nil
}

func (r *Runtime) listWorkspaces(orgID string) ([]record, error) {
	client, err := r.api()
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := client.Get(r.Ctx, fmt.Sprintf("/organizations/%s/workspaces", orgID), nil, &raw); err != nil {
		return nil, err
	}
	return listOf(raw)
}

func (r *Runtime) resolveWorkspace(orgID, ref string) (record, error) {
	wss, err := r.listWorkspaces(orgID)
	if err != nil {
		return nil, err
	}
	return matchOne(wss, ref, "workspace")
}

// listProjects lists the projects of every workspace of the org (or one workspace), adding workspace fields.
func (r *Runtime) listProjects(orgID, workspaceRef string) ([]record, error) {
	client, err := r.api()
	if err != nil {
		return nil, err
	}
	wss, err := r.listWorkspaces(orgID)
	if err != nil {
		return nil, err
	}
	if workspaceRef != "" {
		ws, err := matchOne(wss, workspaceRef, "workspace")
		if err != nil {
			return nil, err
		}
		wss = []record{ws}
	}
	type result struct {
		ws  record
		ps  []record
		err error
	}
	results := make([]result, len(wss))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, ws := range wss {
		wg.Add(1)
		go func(i int, ws record) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var raw json.RawMessage
			err := client.Get(r.Ctx, fmt.Sprintf("/organizations/%s/workspaces/%s/projects", orgID, str(ws["id"])), nil, &raw)
			if err != nil {
				results[i] = result{err: err}
				return
			}
			ps, err := listOf(raw)
			results[i] = result{ws: ws, ps: ps, err: err}
		}(i, ws)
	}
	wg.Wait()
	var out []record
	for _, res := range results {
		if res.err != nil {
			return nil, res.err
		}
		for _, p := range res.ps {
			p["workspace"] = str(res.ws["name"])
			if p["workspaceId"] == nil {
				p["workspaceId"] = res.ws["id"]
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// resolveProject finds a project by id or name within the org in context.
func (r *Runtime) resolveProject(ref string) (record, error) {
	orgID, err := r.orgID()
	if err != nil {
		return nil, err
	}
	ps, err := r.listProjects(orgID, "")
	if err != nil {
		return nil, err
	}
	return matchOne(ps, ref, "project")
}
