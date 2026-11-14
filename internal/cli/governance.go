package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Data governance, groups and roles through the public API (C5 / DEV-4328): tags (they only classify), masking
// policies on tags (who sees what), custom masks, the tags of a table's columns, row rules, the policy and the audit log; groups and their members; roles on
// workspaces and projects. `governance apply` makes the organization's tags and row rules what a file says, so the
// definitions can live in Git.

const govPath = "/organizations/{org}/governance"

func newGovernanceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "governance", Aliases: []string{"gov"}, Short: "Data governance: tags, masking policies, masks, column tags, row rules, policy, audit"}
	cmd.AddCommand(
		group("tags", "Classification tags; what readers see of tagged columns is a masking policy", []string{"tag"},
			opSpec{Use: "list", Short: "List tags", Method: http.MethodGet, Path: govPath + "/tags",
				Unwrap: "items", Fields: []string{"id", "key", "name", "parentId", "sensitivity", "status", "active"}},
			opSpec{Use: "describe TAG", Aliases: []string{"get"}, Short: "Show a tag", Method: http.MethodGet, Path: govPath + "/tags/{tag}",
				Args: []argSpec{{Name: "TAG", Resolve: resolveTag}}},
			opSpec{Use: "create", Short: "Create a tag", Method: http.MethodPost, Path: govPath + "/tags", Body: true,
				Long: `A tag classifies the columns it is on; what each reader sees of them is a masking policy on the tag
(peaka gov masking-policies create).

  peaka gov tags create --name PHI --description "Protected health information"
  peaka gov tags create --from-file phi.json`,
				Flags: []flagSpec{
					{Name: "name", Help: "the tag's name, unique in the organization", Required: true},
					{Name: "description", Help: "what it classifies"},
					{Name: "active", Kind: "bool", Help: "masks while active", Default: true},
					{Name: "key", Help: "a stable key (lowercase letters, digits, _ . -); derived from the name when not given; never changes"},
					{Name: "parent", Help: "the tag above it in the organization's tree: name, key or id", Resolve: resolveParent},
					{Name: "sensitivity", Help: "PUBLIC, INTERNAL, CONFIDENTIAL or RESTRICTED; the organization's default when not given"},
				},
				Prepare: paramsIntoBody("parent", "parentId")},
			opSpec{Use: "update TAG", Short: "Change a tag's name, description, state or place; what is not given stays (202: waits for approval)", Method: http.MethodPatch,
				Path: govPath + "/tags/{tag}", Body: true, Args: []argSpec{{Name: "TAG", Resolve: resolveTag}},
				Flags: []flagSpec{
					{Name: "name", Help: "the tag's name"},
					{Name: "description", Help: "what it classifies"},
					{Name: "active", Kind: "bool", Help: "masks while active"},
					{Name: "parent", Help: "the tag above it: name, key or id; none makes it a root", Resolve: resolveParent},
					{Name: "sensitivity", Help: "PUBLIC, INTERNAL, CONFIDENTIAL or RESTRICTED; none for the organization's default"},
					{Name: "status", Help: "ACTIVE, or DEPRECATED (no new assignment; columns that have it keep it)"},
				},
				Prepare: chain(paramsIntoBody("parent", "parentId"), noneClears("sensitivity"))},
			opSpec{Use: "delete TAG", Short: "Delete a tag and take it off every column (202: waits for approval)", Method: http.MethodDelete,
				Path: govPath + "/tags/{tag}", Args: []argSpec{{Name: "TAG", Resolve: resolveTag}}, Confirm: "Delete tag {0} and take it off every column?", Empty: "Deleted."},
		),
		group("masking-policies", "Masking policies: what readers see of the columns carrying a tag", []string{"masking-policy", "mp"},
			opSpec{Use: "list", Short: "List masking policies (of one tag with --tag)", Method: http.MethodGet, Path: govPath + "/masking-policies",
				Flags:   []flagSpec{{Name: "tag", Help: "only the policies on this tag: name, key or id", Resolve: resolveTag}},
				Prepare: paramsIntoQuery("tag", "tag"),
				Unwrap:  "items", Fields: []string{"id", "name", "tagId", "defaultMaskId", "enabled", "exceptions"}},
			opSpec{Use: "describe POLICY", Aliases: []string{"get"}, Short: "Show a masking policy", Method: http.MethodGet,
				Path: govPath + "/masking-policies/{policy}", Args: []argSpec{{Name: "POLICY", Resolve: resolvePolicy}}},
			opSpec{Use: "create", Short: "Create a masking policy on a tag (202: waits for approval)", Method: http.MethodPost, Path: govPath + "/masking-policies", Body: true,
				Long: `What readers see of the columns carrying a tag (or a tag below it). Every exception naming the reader (as a
person, through a group, or by a role) applies and the most restrictive wins (Deny, then a mask, then Unmasked);
a reader no exception names gets the default; the order of exceptions does not matter.

  peaka gov masking-policies create --tag PHI --name "PHI readers" --default nullify \
      --exception group:Clinicians=unmasked --exception user:ana@example.com=show-last-four@phi.notes`,
				Flags:   policyFlags(true),
				Prepare: chain(paramsIntoBody("tag", "tagId"), resolvePolicyNames)},
			opSpec{Use: "update POLICY", Short: "Change a masking policy; what is not given stays, --exception replaces them all (202: waits for approval)",
				Method: http.MethodPatch, Path: govPath + "/masking-policies/{policy}", Body: true, Args: []argSpec{{Name: "POLICY", Resolve: resolvePolicy}},
				Flags:   policyFlags(false),
				Prepare: chain(paramsIntoBody("tag", "tagId", "policyVersion", "version"), resolvePolicyNames)},
			opSpec{Use: "delete POLICY", Short: "Delete a masking policy (202: waits for approval)", Method: http.MethodDelete,
				Path: govPath + "/masking-policies/{policy}", Args: []argSpec{{Name: "POLICY", Resolve: resolvePolicy}},
				Flags:   []flagSpec{{Name: "reason", In: "query", Help: "why; kept in the Audit log"}},
				Confirm: "Delete masking policy {0}?", Empty: "Deleted."},
		),
		group("masks", "Masks: the predefined ones and the organization's custom masks", []string{"mask"},
			opSpec{Use: "list", Short: "List masks", Method: http.MethodGet, Path: govPath + "/masks", Unwrap: "items", Fields: []string{"id", "key", "name", "expression", "predefined"}},
			opSpec{Use: "create", Short: "Create a custom mask", Method: http.MethodPost, Path: govPath + "/masks", Body: true,
				Long: "  peaka gov masks create --name \"Last four\" --expression \"concat('***', substr({col}, -4))\"",
				Flags: []flagSpec{
					{Name: "name", Help: "the mask's name", Required: true},
					{Name: "expression", Help: "SQL over {col}", Required: true},
					{Name: "description", Help: "what it shows"},
				}},
			opSpec{Use: "update MASK", Short: "Change a custom mask (202: waits for approval when policies use it)", Method: http.MethodPatch, Path: govPath + "/masks/{mask}",
				Body: true, Args: []argSpec{{Name: "MASK", Resolve: resolveMask}},
				Flags: []flagSpec{{Name: "name", Help: "the mask's name"}, {Name: "expression", Help: "SQL over {col}"}, {Name: "description", Help: "what it shows"}}},
			opSpec{Use: "delete MASK", Short: "Delete a custom mask", Method: http.MethodDelete, Path: govPath + "/masks/{mask}",
				Args: []argSpec{{Name: "MASK", Resolve: resolveMask}}, Confirm: "Delete mask {0}?", Empty: "Deleted."},
		),
		group("column-tags", "The tags on columns", []string{"columns"},
			opSpec{Use: "list CATALOG_ID SCHEMA TABLE", Short: "List the tags on a table's columns", Method: http.MethodGet,
				Path:    govPath + "/column-tags",
				Args:    []argSpec{{Name: "CATALOG_ID", Param: "catalog"}, {Name: "SCHEMA", Param: "schema"}, {Name: "TABLE", Param: "table"}},
				Prepare: chain(projectIntoQuery, paramsIntoQuery("catalog", "catalogId", "schema", "schema", "table", "table")),
				Unwrap:  "items", Fields: []string{"id", "column", "tagId", "origin", "reason"}},
			opSpec{Use: "add", Short: "Put a tag on a column", Method: http.MethodPost, Path: govPath + "/column-tags", Body: true,
				Long: "  peaka gov column-tags add --catalog-id 1 --catalog-name peaka --schema table --table patients --column ssn --tag PHI",
				Flags: append(tableFlags(), flagSpec{Name: "column", Key: "column", Help: "the column", Required: true},
					flagSpec{Name: "tag", Help: "tag name or id", Required: true, Resolve: resolveTag},
					flagSpec{Name: "reason", Help: "why"}),
				Prepare: chain(projectIntoBody, paramsIntoBody("tag", "tagId"))},
			opSpec{Use: "apply", Short: "Put a tag on every column of a schema whose name matches a pattern", Method: http.MethodPost, Path: govPath + "/column-tags/apply", Body: true,
				Long: "  peaka gov column-tags apply --catalog-id 7 --schema public --pattern '*ssn*' --tag PHI --preview",
				Flags: []flagSpec{
					{Name: "catalog-id", Key: "catalogId", Help: "the catalog's id", Required: true},
					{Name: "schema", Key: "schema", Help: "the schema", Required: true},
					{Name: "table", Key: "tables", Kind: "strings", Help: "only these tables; repeatable"},
					{Name: "pattern", Key: "columnPattern", Help: "column names: * any characters, ? one", Required: true},
					{Name: "tag", Help: "tag name or id", Required: true, Resolve: resolveTag},
					{Name: "reason", Help: "why"},
					{Name: "preview", Key: "preview", Kind: "bool", Help: "list the matches, change nothing"},
				},
				Prepare: chain(projectIntoBody, paramsIntoBody("tag", "tagId"))},
			opSpec{Use: "remove ID", Short: "Take a tag off a column (202: waits for approval)", Method: http.MethodDelete, Path: govPath + "/column-tags/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}, Empty: "Removed."},
		),
		group("table-tags", "Tags on whole tables and schemas: every column below carries them", []string{"scopes"},
			opSpec{Use: "list", Short: "List the project's table and schema tags", Method: http.MethodGet, Path: govPath + "/table-tags",
				Prepare: projectIntoQuery, Unwrap: "items", Fields: []string{"id", "tagId", "catalogId", "schema", "table", "reason", "createdAt"}},
			opSpec{Use: "add", Short: "Put a tag on a whole table, or on a whole schema when no table is given (202: may wait for approval)",
				Method: http.MethodPost, Path: govPath + "/table-tags", Body: true,
				Long: `Every column of the table (or of every table of the schema) carries the tag, columns added later too. A schema tag
is for a connected catalog: a Peaka table's schema is its own.

  peaka gov table-tags add --catalog-id 1 --catalog-name peaka --schema table --table patients --tag PHI
  peaka gov table-tags add --catalog-id 7 --schema hr --tag PII`,
				Flags: []flagSpec{
					{Name: "catalog-id", Key: "catalogId", Help: "the catalog's id (1 for Peaka tables)", Required: true},
					{Name: "catalog-name", Key: "catalog", Help: "the catalog's name in SQL"},
					{Name: "schema", Key: "schema", Help: "the schema", Required: true},
					{Name: "table", Key: "table", Help: "the table; the whole schema when not given"},
					{Name: "tag", Help: "tag name or id", Required: true, Resolve: resolveTag},
					{Name: "reason", Help: "why"},
				},
				Prepare: chain(projectIntoBody, paramsIntoBody("tag", "tagId"))},
			opSpec{Use: "remove ID", Short: "Take a tag off a whole table or schema (202: may wait for approval)", Method: http.MethodDelete,
				Path: govPath + "/table-tags/{id}", Args: []argSpec{{Name: "ID", Param: "id"}}, Empty: "Removed."},
		),
		group("row-rules", "Row rules: a condition on every read of a table, or of every table with a tagged column", []string{"row-rule", "rules"},
			opSpec{Use: "list", Short: "List row rules", Method: http.MethodGet, Path: "/organizations/{org}/governance/row-rules",
				Unwrap: "items", Fields: []string{"id", "name", "target.type", "target.table", "target.tagId", "enabled", "expression"}},
			opSpec{Use: "describe ID", Aliases: []string{"get"}, Short: "Show a row rule", Method: http.MethodGet, Path: govPath + "/row-rules/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}},
			opSpec{Use: "create", Short: "Write a row rule on a table or a tag", Method: http.MethodPost, Path: govPath + "/row-rules", Body: true,
				Long: `On a table (as the project's SQL names it), or on a tag with {col} for the tagged column:

  peaka gov row-rules create --name "Own region" --table peaka.table.orders --expression "region = current_user_props('region')"
  peaka gov row-rules create --name "Own region" --tag REGION --expression "{col} = current_user_props('region')"`,
				Flags: []flagSpec{
					{Name: "name", Help: "the rule's name", Required: true},
					{Name: "table", Help: "catalog.schema.table, as the project's SQL names it"},
					{Name: "tag", Help: "tag name or id", Resolve: resolveTag},
					{Name: "expression", Help: "a SQL condition", Required: true},
					{Name: "enabled", Kind: "bool", Help: "applies", Default: true},
				},
				Prepare: rowRuleTarget},
			opSpec{Use: "update ID", Short: "Change a row rule's name, condition or state (202: waits for approval)", Method: http.MethodPatch, Path: govPath + "/row-rules/{id}", Body: true,
				Args:  []argSpec{{Name: "ID", Param: "id"}},
				Flags: []flagSpec{{Name: "name", Help: "the rule's name"}, {Name: "expression", Help: "a SQL condition"}, {Name: "enabled", Kind: "bool", Help: "applies"}}},
			opSpec{Use: "delete ID", Short: "Delete a row rule (202: waits for approval)", Method: http.MethodDelete, Path: govPath + "/row-rules/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}, Confirm: "Delete row rule {0}?", Empty: "Deleted."},
		),
		opSpec{Use: "policy", Short: "Show the organization's governance policy", Method: http.MethodGet, Path: govPath + "/policy"}.command(),
		opSpec{Use: "impact", Short: "What turning classification enforcement on would change (-f projectId=… for one project)",
			Long: `Read from metadata alone: untagged columns per table, tables refused for having classified columns and no row rule,
classifications that cannot be read, materialized queries that would be refused, declassifications that lift every path.

  peaka gov impact
  peaka gov impact -f projectId=OtnUT0en`,
			Method: http.MethodGet, Path: govPath + "/enforcement-impact", Query: true}.command(),
		group("audit", "The organization's audit log", nil,
			opSpec{Use: "events", Short: "Classification changes, newest first (-f kind=TAG_APPLIED,ROW_RULE_CREATED -f since=… -f cursor=…)",
				Method: http.MethodGet, Path: govPath + "/audit-events", Query: true, Unwrap: "items",
				Fields: []string{"occurredAt", "kind", "actor", "subject.tag", "subject.table", "subject.column", "reason"}},
			opSpec{Use: "export", Short: "Decision records and policy changes as NDJSON (-f since=… -f until=…)", Method: http.MethodGet, Path: govPath + "/audit-events/export", Query: true},
		),
		group("changes", "Changes waiting for a second administrator", []string{"change-requests", "approvals"},
			opSpec{Use: "list", Short: "Changes waiting for approval (-f status=ALL for every one)", Method: http.MethodGet, Path: govPath + "/change-requests",
				Query: true, Unwrap: "items", Fields: []string{"id", "kind", "summary", "status", "requestedBy", "createdAt"}},
			opSpec{Use: "describe ID", Aliases: []string{"get"}, Short: "Show a change request", Method: http.MethodGet, Path: govPath + "/change-requests/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}},
			opSpec{Use: "approve ID", Short: "Apply a change another administrator asked for", Method: http.MethodPost, Path: govPath + "/change-requests/{id}/approve",
				Body: true, Args: []argSpec{{Name: "ID", Param: "id"}}, Flags: []flagSpec{{Name: "reason", Help: "why; kept in the Audit log"}}},
			opSpec{Use: "reject ID", Short: "Refuse a change (your own: withdraw it)", Method: http.MethodPost, Path: govPath + "/change-requests/{id}/reject",
				Body: true, Args: []argSpec{{Name: "ID", Param: "id"}}, Flags: []flagSpec{{Name: "reason", Help: "why; kept in the Audit log"}}},
		),
		newGovernanceApplyCmd(),
	)
	return cmd
}

func newGroupsCmd() *cobra.Command {
	return group("groups", "The organization's groups and their members", []string{"group"},
		opSpec{Use: "list", Short: "List groups", Method: http.MethodGet, Path: "/organizations/{org}/groups", Unwrap: "items", Fields: []string{"id", "name", "sqlName", "memberCount", "directoryManaged"}},
		opSpec{Use: "describe GROUP", Aliases: []string{"get"}, Short: "Show a group", Method: http.MethodGet, Path: "/organizations/{org}/groups/{group}",
			Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}}},
		opSpec{Use: "create", Short: "Create a group", Method: http.MethodPost, Path: "/organizations/{org}/groups", Body: true,
			Flags: []flagSpec{{Name: "name", Help: "the group's name", Required: true}, {Name: "description", Help: "who is in it"}}},
		opSpec{Use: "delete GROUP", Short: "Delete a group", Method: http.MethodDelete, Path: "/organizations/{org}/groups/{group}",
			Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}}, Confirm: "Delete group {0}?", Empty: "Deleted."},
		opSpec{Use: "members GROUP", Short: "List a group's members", Method: http.MethodGet, Path: "/organizations/{org}/groups/{group}/members",
			Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}}, Unwrap: "items", Fields: []string{"userId", "email", "firstName", "lastName", "kind"}},
		opSpec{Use: "add-member GROUP USER_ID", Short: "Add a member to a group (a member already in it stays)", Method: http.MethodPut, Path: "/organizations/{org}/groups/{group}/members/{user}",
			Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}, {Name: "USER_ID", Param: "user"}}, Empty: "Added."},
		opSpec{Use: "remove-member GROUP USER_ID", Short: "Remove a member from a group", Method: http.MethodDelete, Path: "/organizations/{org}/groups/{group}/members/{user}",
			Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}, {Name: "USER_ID", Param: "user"}}, Empty: "Removed."},
	)
}

// newGrantsCmd: roles on the project in context, or on a workspace with --workspace.
func newGrantsCmd() *cobra.Command {
	scope := []flagSpec{{Name: "workspace", Help: "a workspace (id or name) instead of the project in context", Resolve: resolveGrantWorkspace}}
	path := "/organizations/{org}/workspaces/{ws}/projects/{project}/grants"
	return group("grants", "Roles members and groups hold on the project in context (or a workspace)", []string{"roles-on"},
		opSpec{Use: "list", Short: "List roles given on the project or workspace", Method: http.MethodGet, Path: path, Flags: scope,
			Prepare: grantPath, Unwrap: "items", Fields: []string{"id", "principal.type", "principal.name", "principal.email", "role.name"}},
		opSpec{Use: "add", Short: "Give a member or group a role", Method: http.MethodPost, Path: path, Body: true,
			Long: "  peaka grants add --group Clinicians --role 1a6f5d6c-…\n  peaka grants add --workspace Lab --user 5b4d915b-… --role …",
			Flags: append(scope,
				flagSpec{Name: "group", Help: "a group (id or name)", Resolve: resolveGroup},
				flagSpec{Name: "user", Help: "a member's user id"},
				flagSpec{Name: "role", Key: "roleId", Help: "the role's id", Required: true}),
			Prepare: chain(grantPath, grantee)},
		opSpec{Use: "remove GRANT_ID", Short: "Take a role away", Method: http.MethodDelete, Path: path + "/{grant}", Flags: scope,
			Args: []argSpec{{Name: "GRANT_ID", Param: "grant"}}, Prepare: grantPath, Confirm: "Take role grant {0} away?", Empty: "Removed."},
	)
}

// ==================== resolvers and body shaping ====================

// listAt reads a list of the public API whole: every page, following nextCursor until hasMore is false (Codex review
// round 6, R6-7: a name past the first page was missed, and apply made it again), or a bare array.
// maxPages bounds how many pages listAt reads.
const maxPages = 2000

func listAt(r *Runtime, path string) ([]record, error) {
	client, err := r.api()
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	if p, q, found := strings.Cut(path, "?"); found {
		path = p
		if query, err = url.ParseQuery(q); err != nil {
			return nil, err
		}
	}
	query.Set("limit", "500")
	var all []record
	seen := map[string]bool{}
	for pages := 0; ; pages++ {
		// A bound on a list that never ends (Codex review round 7): 2,000 pages of 500 is a million records.
		if pages == maxPages {
			return nil, fmt.Errorf("the list of %s has more than %d pages; stopped", path, maxPages)
		}
		var raw json.RawMessage
		if err := client.Get(r.Ctx, path, query, &raw); err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(string(raw))
		if !strings.HasPrefix(trimmed, "{") || !strings.Contains(trimmed, `"items"`) {
			return listOf(raw)
		}
		var page struct {
			Items      []record `json:"items"`
			NextCursor string   `json:"nextCursor"`
			HasMore    bool     `json:"hasMore"`
		}
		if err := decodeJSON(raw, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if !page.HasMore {
			return all, nil
		}
		// More, with no way to ask for it: a partial list would read as the whole one, and apply would make a duplicate.
		if page.NextCursor == "" {
			return nil, fmt.Errorf("the list of %s says there is more but gives no cursor", path)
		}
		if seen[page.NextCursor] {
			return nil, fmt.Errorf("the list of %s repeats a page", path)
		}
		seen[page.NextCursor] = true
		query.Set("cursor", page.NextCursor)
	}
}

// resolveTag: a tag's id or name.
func resolveTag(r *Runtime, ref string, params map[string]string) error {
	org, err := r.orgID()
	if err != nil {
		return err
	}
	tags, err := listAt(r, fmt.Sprintf("/organizations/%s/governance/tags", org))
	if err != nil {
		return err
	}
	t, err := matchOne(tags, ref, "tag")
	if err != nil {
		return err
	}
	params["tag"] = str(t["id"])
	return nil
}

// resolveParent: a tag's parent by name, key or id; "none" makes it a root (an empty parentId).
func resolveParent(r *Runtime, ref string, params map[string]string) error {
	if strings.EqualFold(strings.TrimSpace(ref), "none") {
		params["parent"] = ""
		return nil
	}
	found := map[string]string{}
	if err := resolveTag(r, ref, found); err != nil {
		return err
	}
	params["parent"] = found["tag"]
	return nil
}

// noneClears: "none" for a field sends an empty value, which clears it.
func noneClears(field string) func(*callCtx) error {
	return func(c *callCtx) error {
		if v, ok := c.Body[field].(string); ok && strings.EqualFold(strings.TrimSpace(v), "none") {
			c.Body[field] = ""
		}
		return nil
	}
}

// masksForMatch: masks by id, name or a predefined mask's key.
func masksForMatch(r *Runtime) ([]record, error) {
	org, err := r.orgID()
	if err != nil {
		return nil, err
	}
	masks, err := listAt(r, fmt.Sprintf("/organizations/%s/governance/masks", org))
	if err != nil {
		return nil, err
	}
	for _, m := range masks {
		if k := str(m["key"]); k != "" {
			m["displayName"] = k
		}
	}
	return masks, nil
}

func resolveMask(r *Runtime, ref string, params map[string]string) error {
	masks, err := masksForMatch(r)
	if err != nil {
		return err
	}
	m, err := matchOne(masks, ref, "mask")
	if err != nil {
		return err
	}
	params["mask"] = str(m["id"])
	return nil
}

func resolveGroup(r *Runtime, ref string, params map[string]string) error {
	org, err := r.orgID()
	if err != nil {
		return err
	}
	groups, err := listAt(r, fmt.Sprintf("/organizations/%s/groups", org))
	if err != nil {
		return err
	}
	g, err := matchOne(groups, ref, "group")
	if err != nil {
		return err
	}
	params["group"] = str(g["id"])
	return nil
}

func resolveGrantWorkspace(r *Runtime, ref string, params map[string]string) error {
	org, err := r.orgID()
	if err != nil {
		return err
	}
	ws, err := r.resolveWorkspace(org, ref)
	if err != nil {
		return err
	}
	params["ws"] = str(ws["id"])
	params["onWorkspace"] = "1"
	return nil
}

// grantPath: the project's grants (its workspace looked up), or the workspace's when --workspace was given.
func grantPath(c *callCtx) error {
	if c.Params["onWorkspace"] == "1" {
		c.Path = strings.Replace(c.Path, "/projects/{project}/grants", "/grants", 1)
		return nil
	}
	p, err := rt.resolveProject(c.Params["project"])
	if err != nil {
		return err
	}
	c.Params["project"] = str(p["id"])
	c.Params["ws"] = str(p["workspaceId"])
	return nil
}

// grantee: who gets the role, as the API's principal.
func grantee(c *callCtx) error {
	user, _ := c.Body["user"].(string)
	delete(c.Body, "user")
	switch {
	case c.Params["group"] != "" && user != "":
		return usage("give the role to a group or a user, not both")
	case c.Params["group"] != "":
		c.Body["principal"] = map[string]any{"type": "group", "id": c.Params["group"]}
	case user != "":
		c.Body["principal"] = map[string]any{"type": "user", "id": user}
	default:
		return usage("name who gets the role: --group or --user")
	}
	return nil
}

// rowRuleTarget: what the rule is on, as the API's target.
func rowRuleTarget(c *callCtx) error {
	table, _ := c.Body["table"].(string)
	delete(c.Body, "table")
	switch {
	case c.Params["tag"] != "" && table != "":
		return usage("a row rule is on a table or on a tag, not both")
	case c.Params["tag"] != "":
		if expr, _ := c.Body["expression"].(string); !strings.Contains(expr, "{col}") {
			return usage("a rule on a tag names the tagged column as {col}")
		}
		c.Body["target"] = map[string]any{"type": "tag", "tagId": c.Params["tag"]}
	case table != "":
		p, err := rt.needProject()
		if err != nil {
			return err
		}
		c.Body["target"] = map[string]any{"type": "table", "projectId": p, "table": table}
	default:
		return usage("name what the rule is on: --table or --tag")
	}
	return nil
}

// projectIntoBody: the project in context, for routes that take it in the body.
func projectIntoBody(c *callCtx) error {
	p, err := rt.needProject()
	if err != nil {
		return err
	}
	c.Body["projectId"] = p
	return nil
}

// projectIntoQuery: the project in context, for lists filtered by it.
func projectIntoQuery(c *callCtx) error {
	p, err := rt.needProject()
	if err != nil {
		return err
	}
	c.Query.Set("projectId", p)
	return nil
}

func tableFlags() []flagSpec {
	return []flagSpec{
		{Name: "catalog-id", Key: "catalogId", Help: "the catalog's id (1 for Peaka tables)", Required: true},
		{Name: "catalog-name", Key: "catalog", Help: "the catalog's name in SQL"},
		{Name: "schema", Key: "schema", Help: "the schema", Required: true},
		{Name: "table", Key: "table", Help: "the table", Required: true},
	}
}

// policyFlags: a masking policy's flags; on create the tag is required and a policy is enabled unless told.
func policyFlags(create bool) []flagSpec {
	enabled := flagSpec{Name: "enabled", Kind: "bool", Help: "decides while enabled"}
	exceptionHelp := "an exception WHO=MASK[@SCOPE_TAG]: user:ID|EMAIL, group:NAME|ID or role:ID; MASK a mask's name, key or id; " +
		"SCOPE_TAG the policy's tag or one below it; repeatable"
	if create {
		enabled.Default = true
	} else {
		exceptionHelp += " (replaces them all)"
	}
	return []flagSpec{
		{Name: "tag", Help: "the tag it is on: name, key or id", Required: create, Resolve: resolveTag},
		{Name: "name", Help: "the policy's name (default: the tag's)"},
		{Name: "default", Key: "defaultMaskId", Help: "what a reader no exception names gets: a mask's name, key or id; org (or empty) for the organization's default"},
		enabled,
		{Name: "exception", Kind: "strings", Set: exceptionsSetter, Help: exceptionHelp},
		{Name: "reason", In: "query", Help: "why; kept in the Audit log"},
	}
}

// resolvePolicy: a masking policy's id or name; the version it was read at goes with the change.
func resolvePolicy(r *Runtime, ref string, params map[string]string) error {
	org, err := r.orgID()
	if err != nil {
		return err
	}
	policies, err := listAt(r, fmt.Sprintf("/organizations/%s/governance/masking-policies", org))
	if err != nil {
		return err
	}
	p, err := matchOne(policies, ref, "masking policy")
	if err != nil {
		return err
	}
	params["policy"] = str(p["id"])
	if v := str(p["version"]); v != "" {
		params["policyVersion"] = v
	}
	return nil
}

// exceptionsSetter keeps the WHO=MASK[@SCOPE_TAG] exceptions as written; resolvePolicyNames turns them into the API's.
func exceptionsSetter(v any, body map[string]any) {
	out := []any{}
	for _, s := range v.([]string) {
		out = append(out, s)
	}
	body["exceptions"] = out
}

// resolvePolicyNames: masks, groups and scope tags by name to what the API takes.
func resolvePolicyNames(c *callCtx) error {
	var masks []record
	maskOf := func(ref string) (string, error) {
		if masks == nil {
			var err error
			if masks, err = masksForMatch(rt); err != nil {
				return "", err
			}
		}
		m, err := matchOne(masks, ref, "mask")
		if err != nil {
			return "", err
		}
		if k := str(m["key"]); k != "" {
			return k, nil
		}
		return str(m["id"]), nil
	}
	groupOf := func(ref string) (string, error) {
		p := map[string]string{}
		err := resolveGroup(rt, ref, p)
		return p["group"], err
	}
	tagOf := func(ref string) (string, error) {
		p := map[string]string{}
		err := resolveTag(rt, ref, p)
		return p["tag"], err
	}
	return shapePolicy(c.Body, maskOf, groupOf, tagOf)
}

// shapePolicy turns --default and --exception into the API's defaultMaskId and exceptions. A user is sent as given
// (an id, or an email the API looks up); a group, a mask and a scope tag are looked up here.
func shapePolicy(body map[string]any, maskOf, groupOf, tagOf func(string) (string, error)) error {
	if v, ok := body["defaultMaskId"].(string); ok {
		ref := strings.TrimSpace(v)
		if ref == "" || strings.EqualFold(ref, "org") {
			body["defaultMaskId"] = nil
		} else {
			id, err := maskOf(ref)
			if err != nil {
				return err
			}
			body["defaultMaskId"] = id
		}
	}
	raw, ok := body["exceptions"].([]any)
	if !ok {
		return nil
	}
	out := []any{}
	for _, x := range raw {
		s, isText := x.(string)
		if !isText {
			out = append(out, x) // already an object (from --from-file)
			continue
		}
		kind, who, mask, scope, err := parseException(s)
		if err != nil {
			return err
		}
		if kind == "group" {
			if who, err = groupOf(who); err != nil {
				return err
			}
		}
		maskID, err := maskOf(mask)
		if err != nil {
			return err
		}
		e := map[string]any{"subject": map[string]any{"type": kind, "id": who}, "maskId": maskID}
		if scope != "" {
			id, err := tagOf(scope)
			if err != nil {
				return err
			}
			e["scopeTagId"] = id
		}
		out = append(out, e)
	}
	body["exceptions"] = out
	return nil
}

// parseException reads WHO=MASK[@SCOPE_TAG]: WHO is user:ID|EMAIL, group:NAME|ID or role:ID.
func parseException(s string) (kind, who, mask, scope string, err error) {
	w, rest, found := strings.Cut(s, "=")
	if !found || strings.TrimSpace(rest) == "" {
		return "", "", "", "", usage("an exception is WHO=MASK[@SCOPE_TAG]: %q", s)
	}
	mask, scope, _ = strings.Cut(rest, "@")
	mask, scope = strings.TrimSpace(mask), strings.TrimSpace(scope)
	k, id, _ := strings.Cut(w, ":")
	kind, who = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(id)
	switch kind {
	case "everyone":
		return "", "", "", "", usage("everyone is the policy's default: use --default, not an exception")
	case "user", "group", "role":
		if who == "" {
			return "", "", "", "", usage("an exception names whom: %s:ID", kind)
		}
	default:
		return "", "", "", "", usage("who is user:ID|EMAIL, group:NAME|ID or role:ID: %q", w)
	}
	if mask == "" {
		return "", "", "", "", usage("an exception names a mask: %q", s)
	}
	return kind, who, mask, scope, nil
}

// ==================== apply ====================

// govFile is what `governance apply` reads: the tags and row rules an organization should have.
type govFile struct {
	Tags []struct {
		Name        string   `yaml:"name"`
		Label       string   `yaml:"label"` // the older word for name
		Description string   `yaml:"description"`
		Active      *bool    `yaml:"active"`
		Rules       []string `yaml:"rules"` // refused: masking is a masking policy now
	} `yaml:"tags"`
	RowRules []struct {
		Name       string `yaml:"name"`
		Table      string `yaml:"table"`
		Tag        string `yaml:"tag"`
		Expression string `yaml:"expression"`
		Enabled    *bool  `yaml:"enabled"`
	} `yaml:"rowRules"`
	ColumnTags []struct {
		CatalogID   string `yaml:"catalogId"`
		CatalogName string `yaml:"catalogName"`
		Catalog     string `yaml:"catalog"`
		Schema      string `yaml:"schema"`
		Table       string `yaml:"table"`
		Column      string `yaml:"column"`
		Tag         string `yaml:"tag"`
		Reason      string `yaml:"reason"`
	} `yaml:"columnTags"`
}

func newGovernanceApplyCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "apply -f FILE",
		Short: "Make the organization's tags, row rules and column tags what a YAML file says",
		Long: `Creates what is missing and updates what differs, by tag name and row rule name; a column tag already there is
left as it is; nothing is deleted. Tags come first, then column tags, then row rules (a rule on a tag needs a
tagged column).
A change that reveals classified data may wait for a second administrator (202): it is reported, not retried.

  tags:
    - name: PHI
      description: Protected health information
  rowRules:
    - name: Own region
      tag: REGION
      expression: "{col} = current_user_props('region')"
  columnTags:
    - {catalogId: "1", catalog: peaka, schema: table, table: patients, column: ssn, tag: PHI}

  peaka gov apply -f governance.yaml [--dry-run]`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return applyGovernance(file) },
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "the YAML file (- for stdin)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func applyGovernance(file string) error {
	raw, err := readInput(file)
	if err != nil {
		return usage("-f: %v", err)
	}
	var want govFile
	if err := yaml.Unmarshal(raw, &want); err != nil {
		return usage("-f: not a governance file: %v", err)
	}
	org, err := rt.orgID()
	if err != nil {
		return err
	}
	client, err := rt.api()
	if err != nil {
		return err
	}
	base := fmt.Sprintf("/organizations/%s/governance", org)
	var report []map[string]any
	note := func(kind, name, action string, resp *api.Response, err error) {
		entry := map[string]any{"kind": kind, "name": name, "action": action}
		switch {
		case err != nil:
			entry["result"] = "failed: " + err.Error()
		case resp != nil && resp.Status == http.StatusAccepted:
			entry["result"] = "waits for approval"
		case gf.dryRun:
			entry["result"] = "dry run"
		default:
			entry["result"] = "done"
		}
		report = append(report, entry)
	}

	// What the file names, found before anything is sent: a label or name that is two records is refused, as the
	// change would go to one of them by chance (R5-6).
	existing, err := listAt(rt, base+"/tags")
	if err != nil {
		return err
	}
	tagsByLabel := index(existing, tagKey)
	rules, err := listAt(rt, base+"/row-rules")
	if err != nil {
		return err
	}
	rulesByName := index(rules, func(r record) (string, string) { return strings.ToLower(str(r["name"])), str(r["id"]) })
	var ambiguous []string
	for i := range want.Tags {
		if want.Tags[i].Name == "" {
			want.Tags[i].Name = want.Tags[i].Label
		}
		ambiguous = append(ambiguous, twice(tagsByLabel, strings.ToUpper(want.Tags[i].Name), "tag")...)
	}
	for _, ct := range want.ColumnTags {
		ambiguous = append(ambiguous, twice(tagsByLabel, strings.ToUpper(ct.Tag), "tag")...)
	}
	for _, rr := range want.RowRules {
		ambiguous = append(ambiguous, twice(rulesByName, strings.ToLower(rr.Name), "row rule")...)
		if rr.Tag != "" {
			ambiguous = append(ambiguous, twice(tagsByLabel, strings.ToUpper(rr.Tag), "tag")...)
		}
	}
	if len(ambiguous) > 0 {
		return api.Exit(api.ExitConflict, "nothing was applied: %s; rename them in Peaka first", strings.Join(unique(ambiguous), "; "))
	}
	byLabel := map[string]string{}
	for k, ids := range tagsByLabel {
		byLabel[k] = ids[0]
	}
	for _, t := range want.Tags {
		if len(t.Rules) > 0 {
			note("tag", t.Name, "resolve", nil, fmt.Errorf("a tag carries no masking rules; set them with `peaka gov masking-policies create --tag %s`", t.Name))
			continue
		}
		c := &callCtx{Params: map[string]string{}, Body: map[string]any{"name": t.Name, "description": t.Description, "active": t.Active == nil || *t.Active}}
		if id, ok := byLabel[strings.ToUpper(t.Name)]; ok {
			resp, err := send(client, http.MethodPatch, base+"/tags/"+id, c.Body)
			note("tag", t.Name, "update", resp, err)
		} else {
			resp, err := send(client, http.MethodPost, base+"/tags", c.Body)
			note("tag", t.Name, "create", resp, err)
		}
	}
	if len(want.Tags) > 0 && !gf.dryRun {
		if existing, err = listAt(rt, base+"/tags"); err != nil {
			return err
		}
		for k, ids := range index(existing, tagKey) {
			byLabel[k] = ids[0]
		}
	}

	// Column tags: put on; one already there stays.
	project := ""
	if len(want.ColumnTags) > 0 || len(want.RowRules) > 0 {
		project, _ = rt.needProject()
	}
	for _, ct := range want.ColumnTags {
		name := ct.Schema + "." + ct.Table + "." + ct.Column + " " + ct.Tag
		id, ok := byLabel[strings.ToUpper(ct.Tag)]
		if !ok || project == "" {
			note("column tag", name, "add", nil, fmt.Errorf("no tag %q or no project in context", ct.Tag))
			continue
		}
		there, err := listAt(rt, base+"/column-tags?"+url.Values{"projectId": {project}, "catalogId": {ct.CatalogID},
			"schema": {ct.Schema}, "table": {ct.Table}}.Encode())
		if err != nil {
			note("column tag", name, "add", nil, err)
			continue
		}
		tagged := false
		for _, x := range there {
			if strings.EqualFold(str(x["column"]), ct.Column) && str(x["tagId"]) == id {
				tagged = true
			}
		}
		if tagged {
			report = append(report, map[string]any{"kind": "column tag", "name": name, "action": "none", "result": "unchanged"})
			continue
		}
		catalog := ct.Catalog
		if catalog == "" {
			catalog = ct.CatalogName
		}
		resp, err := send(client, http.MethodPost, base+"/column-tags", map[string]any{"projectId": project, "catalogId": ct.CatalogID,
			"catalog": catalog, "schema": ct.Schema, "table": ct.Table, "column": ct.Column, "tagId": id, "reason": ct.Reason})
		note("column tag", name, "add", resp, err)
	}

	// Row rules, by name.
	ruleByID := map[string]record{}
	for _, r := range rules {
		ruleByID[str(r["id"])] = r
	}
	for _, rr := range want.RowRules {
		enabled := rr.Enabled == nil || *rr.Enabled
		if ids, ok := rulesByName[strings.ToLower(rr.Name)]; ok {
			id := ids[0]
			// A rule's target never changes (a new target is a new rule): one named for another target is refused,
			// not reported done (R5-7).
			if why := otherTarget(ruleByID[id], rr.Table, byLabel[strings.ToUpper(rr.Tag)], rr.Tag, project); why != "" {
				note("row rule", rr.Name, "update", nil, fmt.Errorf("%s; give the rule another name, or change it in Peaka", why))
				continue
			}
			resp, err := send(client, http.MethodPatch, base+"/row-rules/"+id, map[string]any{"expression": rr.Expression, "enabled": enabled})
			note("row rule", rr.Name, "update", resp, err)
			continue
		}
		body := map[string]any{"name": rr.Name, "expression": rr.Expression, "enabled": enabled}
		switch {
		case rr.Tag != "":
			id, ok := byLabel[strings.ToUpper(rr.Tag)]
			if !ok {
				note("row rule", rr.Name, "create", nil, fmt.Errorf("no tag %q", rr.Tag))
				continue
			}
			body["target"] = map[string]any{"type": "tag", "tagId": id}
		case rr.Table != "" && project != "":
			body["target"] = map[string]any{"type": "table", "projectId": project, "table": rr.Table}
		default:
			note("row rule", rr.Name, "create", nil, fmt.Errorf("a row rule names a table (with a project in context) or a tag"))
			continue
		}
		resp, err := send(client, http.MethodPost, base+"/row-rules", body)
		note("row rule", rr.Name, "create", resp, err)
	}
	if err := rt.printWith(report, "kind", "name", "action", "result"); err != nil {
		return err
	}
	for _, r := range report {
		if strings.HasPrefix(str(r["result"]), "failed") {
			return api.Exit(api.ExitInvalid, "some of the file was not applied")
		}
	}
	return nil
}

// tagKey indexes the public API's tags by name, whatever its case.
func tagKey(x record) (string, string) {
	return strings.ToUpper(str(x["name"])), str(x["id"])
}

// index groups records by a key: label or name to ids.
func index(rs []record, keyOf func(record) (string, string)) map[string][]string {
	out := map[string][]string{}
	for _, r := range rs {
		k, id := keyOf(r)
		if k != "" && id != "" {
			out[k] = append(out[k], id)
		}
	}
	return out
}

func twice(idx map[string][]string, key, what string) []string {
	if ids := idx[key]; len(ids) > 1 {
		return []string{fmt.Sprintf("%d %ss are named %q (%s)", len(ids), what, key, strings.Join(ids, ", "))}
	}
	return nil
}

func unique(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// otherTarget says how an existing rule's target differs from the file's (a table in the project, or a tag); "" when
// it is the same.
func otherTarget(existing record, table, tagID, tagLabel, project string) string {
	target, _ := existing["target"].(map[string]any)
	kind := strings.ToLower(str(target["type"]))
	switch {
	case tagLabel != "":
		if kind != "tag" || str(target["tagId"]) != tagID {
			return fmt.Sprintf("the rule is on %s, not on the tag %s", describeTarget(target), tagLabel)
		}
	case table != "":
		if kind == "tag" || !strings.EqualFold(str(target["table"]), table) || (project != "" && str(target["projectId"]) != project) {
			return fmt.Sprintf("the rule is on %s, not on %s", describeTarget(target), table)
		}
	}
	return ""
}

func describeTarget(target map[string]any) string {
	if strings.EqualFold(str(target["type"]), "tag") {
		return "the tag " + str(target["tagId"])
	}
	return str(target["table"]) + " in project " + str(target["projectId"])
}

// send writes; under --dry-run the client prints the request instead.
func send(client *api.Client, method, path string, body map[string]any) (*api.Response, error) {
	return client.Do(rt.Ctx, method, path, url.Values{}, body, nil)
}
