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

// Data governance, groups and roles through the public API (C5 / DEV-4328): tags and their masking rules, custom
// masks, the tags of a table's columns, row rules, the policy and the audit log; groups and their members; roles on
// workspaces and projects. `governance apply` makes the organization's tags and row rules what a file says, so the
// definitions can live in Git.

const govPath = "/organizations/{org}/governance"

func newGovernanceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "governance", Aliases: []string{"gov"}, Short: "Data governance: tags, masks, column tags, row rules, policy, audit"}
	cmd.AddCommand(
		group("tags", "Classification tags and their masking rules", []string{"tag"},
			opSpec{Use: "list", Short: "List tags with their masking rules", Method: http.MethodGet, Path: govPath + "/tags",
				Fields: []string{"tag.id", "tag.label", "tag.active", "maskRules"}},
			opSpec{Use: "describe TAG", Aliases: []string{"get"}, Short: "Show a tag", Method: http.MethodGet, Path: govPath + "/tags/{tag}",
				Args: []argSpec{{Name: "TAG", Resolve: resolveTag}}},
			opSpec{Use: "create", Short: "Create a tag", Method: http.MethodPost, Path: govPath + "/tags", Body: true,
				Long: `A tag masks the columns it is on, for each reader by its rules (in order).

  peaka gov tags create --label PHI --description "Protected health information" \
      --rule everyone=Nullify --rule group:Clinicians=Unmasked
  peaka gov tags create --from-file phi.json`,
				Flags: []flagSpec{
					{Name: "label", Help: "the tag's name", Required: true},
					{Name: "description", Help: "what it classifies"},
					{Name: "active", Kind: "bool", Help: "masks while active", Default: true},
					{Name: "rule", Kind: "strings", Help: "a masking rule WHO=MASK, in order: everyone, user:ID, group:NAME|ID, role:ID; MASK a mask name or id; repeatable", Set: rulesSetter},
				},
				Prepare: resolveRuleNames},
			opSpec{Use: "update TAG", Short: "Replace a tag's label, description, state and rules (202: waits for approval)", Method: http.MethodPut,
				Path: govPath + "/tags/{tag}", Body: true, Args: []argSpec{{Name: "TAG", Resolve: resolveTag}},
				Flags: []flagSpec{
					{Name: "label", Help: "the tag's name"},
					{Name: "description", Help: "what it classifies"},
					{Name: "active", Kind: "bool", Help: "masks while active"},
					{Name: "rule", Kind: "strings", Help: "the masking rules WHO=MASK, in order (replaces them); repeatable", Set: rulesSetter},
				},
				Prepare: chain(resolveRuleNames, keepTagFields)},
			opSpec{Use: "delete TAG", Short: "Delete a tag and take it off every column (202: waits for approval)", Method: http.MethodDelete,
				Path: govPath + "/tags/{tag}", Args: []argSpec{{Name: "TAG", Resolve: resolveTag}}, Confirm: "Delete tag {0} and take it off every column?", Empty: "Deleted."},
		),
		group("masks", "Masks: the predefined ones and the organization's custom masks", []string{"mask"},
			opSpec{Use: "list", Short: "List masks", Method: http.MethodGet, Path: govPath + "/masks", Fields: []string{"id", "label", "expression", "predefined"}},
			opSpec{Use: "create", Short: "Create a custom mask", Method: http.MethodPost, Path: govPath + "/masks", Body: true,
				Long: "  peaka gov masks create --label \"Last four\" --expression \"concat('***', substr({col}, -4))\"",
				Flags: []flagSpec{
					{Name: "label", Help: "the mask's name", Required: true},
					{Name: "expression", Help: "SQL over {col}", Required: true},
					{Name: "description", Help: "what it shows"},
				}},
			opSpec{Use: "update MASK", Short: "Change a custom mask (202: waits for approval when rules use it)", Method: http.MethodPut, Path: govPath + "/masks/{mask}",
				Body: true, Args: []argSpec{{Name: "MASK", Resolve: resolveMask}},
				Flags: []flagSpec{{Name: "label", Help: "the mask's name"}, {Name: "expression", Help: "SQL over {col}"}, {Name: "description", Help: "what it shows"}}},
			opSpec{Use: "delete MASK", Short: "Delete a custom mask", Method: http.MethodDelete, Path: govPath + "/masks/{mask}",
				Args: []argSpec{{Name: "MASK", Resolve: resolveMask}}, Confirm: "Delete mask {0}?", Empty: "Deleted."},
		),
		group("column-tags", "The tags on columns", []string{"columns"},
			opSpec{Use: "list CATALOG_ID SCHEMA TABLE", Short: "List the tags on a table's columns", Method: http.MethodGet,
				Path:   govPath + "/projects/{project}/catalogs/{catalog}/schemas/{schema}/tables/{table}/column-tags",
				Args:   []argSpec{{Name: "CATALOG_ID", Param: "catalog"}, {Name: "SCHEMA", Param: "schema"}, {Name: "TABLE", Param: "table"}},
				Fields: []string{"id", "columnName", "tagId", "origin", "reason"}},
			opSpec{Use: "add", Short: "Put a tag on a column", Method: http.MethodPost, Path: govPath + "/column-tags", Body: true,
				Long: "  peaka gov column-tags add --catalog-id 1 --catalog-name peaka --schema table --table patients --column ssn --tag PHI",
				Flags: append(tableFlags(), flagSpec{Name: "column", Key: "columnName", Help: "the column", Required: true},
					flagSpec{Name: "tag", Help: "tag label or id", Required: true, Resolve: resolveTag},
					flagSpec{Name: "reason", Help: "why"}),
				Prepare: chain(projectIntoBody, paramsIntoBody("tag", "tagId"))},
			opSpec{Use: "apply", Short: "Put a tag on every column of a schema whose name matches a pattern", Method: http.MethodPost, Path: govPath + "/column-tags/apply", Body: true,
				Long: "  peaka gov column-tags apply --catalog-id 7 --schema public --pattern '*ssn*' --tag PHI --preview",
				Flags: []flagSpec{
					{Name: "catalog-id", Key: "catalogId", Help: "the catalog's id", Required: true},
					{Name: "schema", Key: "schemaName", Help: "the schema", Required: true},
					{Name: "table", Key: "tables", Kind: "strings", Help: "only these tables; repeatable"},
					{Name: "pattern", Key: "columnPattern", Help: "column names: * any characters, ? one", Required: true},
					{Name: "tag", Help: "tag label or id", Required: true, Resolve: resolveTag},
					{Name: "reason", Help: "why"},
					{Name: "preview", Key: "dryRun", Kind: "bool", Help: "list the matches, change nothing"},
				},
				Prepare: chain(projectIntoBody, paramsIntoBody("tag", "tagId"))},
			opSpec{Use: "remove ID", Short: "Take a tag off a column (202: waits for approval)", Method: http.MethodDelete, Path: govPath + "/column-tags/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}, Empty: "Removed."},
		),
		group("row-rules", "Row rules: a condition on every read of a table, or of every table with a tagged column", []string{"row-rule", "rules"},
			opSpec{Use: "list", Short: "List row rules", Method: http.MethodGet, Path: "/organizations/{org}/governance/row-rules",
				Fields: []string{"id", "name", "targetType", "table", "tagId", "enabled", "expression"}},
			opSpec{Use: "describe ID", Aliases: []string{"get"}, Short: "Show a row rule", Method: http.MethodGet, Path: govPath + "/row-rules/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}},
			opSpec{Use: "create", Short: "Write a row rule on a table or a tag", Method: http.MethodPost, Path: govPath + "/row-rules", Body: true,
				Long: `On a table (as the project's SQL names it), or on a tag with {col} for the tagged column:

  peaka gov row-rules create --name "Own region" --table peaka.table.orders --expression "region = current_user_props('region')"
  peaka gov row-rules create --name "Own region" --tag REGION --expression "{col} = current_user_props('region')"`,
				Flags: []flagSpec{
					{Name: "name", Help: "the rule's name", Required: true},
					{Name: "table", Help: "catalog.schema.table, as the project's SQL names it"},
					{Name: "tag", Help: "tag label or id", Resolve: resolveTag},
					{Name: "expression", Help: "a SQL condition", Required: true},
					{Name: "enabled", Kind: "bool", Help: "applies", Default: true},
				},
				Prepare: rowRuleTarget},
			opSpec{Use: "update ID", Short: "Change a row rule (202: waits for approval)", Method: http.MethodPut, Path: govPath + "/row-rules/{id}", Body: true,
				Args:  []argSpec{{Name: "ID", Param: "id"}},
				Flags: []flagSpec{{Name: "name", Help: "the rule's name"}, {Name: "expression", Help: "a SQL condition"}, {Name: "enabled", Kind: "bool", Help: "applies"}}},
			opSpec{Use: "delete ID", Short: "Delete a row rule (202: waits for approval)", Method: http.MethodDelete, Path: govPath + "/row-rules/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}, Confirm: "Delete row rule {0}?", Empty: "Deleted."},
		),
		opSpec{Use: "policy", Short: "Show the organization's governance policy", Method: http.MethodGet, Path: govPath + "/policy"}.command(),
		group("audit", "The organization's audit log", nil,
			opSpec{Use: "events", Short: "Classification changes, newest first (-f kind=TAG_APPLIED,ROW_RULE_CREATED -f since=… -f cursor=…)",
				Method: http.MethodGet, Path: govPath + "/audit-events", Query: true, Unwrap: "items",
				Fields: []string{"occurredAt", "kind", "actor", "subject.tag", "subject.table", "subject.column", "reason"}},
			opSpec{Use: "export", Short: "Decision records and policy changes as NDJSON (-f from=… -f to=…)", Method: http.MethodGet, Path: govPath + "/audit-log/export", Query: true},
		),
		newGovernanceApplyCmd(),
	)
	return cmd
}

func newGroupsCmd() *cobra.Command {
	return group("groups", "The organization's groups and their members", []string{"group"},
		opSpec{Use: "list", Short: "List groups", Method: http.MethodGet, Path: "/organizations/{org}/groups", Fields: []string{"id", "name", "sqlName", "description"}},
		opSpec{Use: "describe GROUP", Aliases: []string{"get"}, Short: "Show a group", Method: http.MethodGet, Path: "/organizations/{org}/groups/{group}",
			Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}}},
		opSpec{Use: "create", Short: "Create a group", Method: http.MethodPost, Path: "/organizations/{org}/groups", Body: true,
			Flags: []flagSpec{{Name: "name", Help: "the group's name", Required: true}, {Name: "description", Help: "who is in it"}}},
		opSpec{Use: "delete GROUP", Short: "Delete a group", Method: http.MethodDelete, Path: "/organizations/{org}/groups/{group}",
			Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}}, Confirm: "Delete group {0}?", Empty: "Deleted."},
		opSpec{Use: "members GROUP", Short: "List a group's members", Method: http.MethodGet, Path: "/organizations/{org}/groups/{group}/members",
			Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}}},
		opSpec{Use: "add-member GROUP USER_ID", Short: "Add a member to a group", Method: http.MethodPost, Path: "/organizations/{org}/groups/{group}/members/{user}",
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
			Prepare: grantPath, Fields: []string{"grantId", "entityType", "email", "name", "roleName"}},
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

func listAt(r *Runtime, path string) ([]record, error) {
	client, err := r.api()
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := client.Get(r.Ctx, path, nil, &raw); err != nil {
		return nil, err
	}
	return listOf(raw)
}

// resolveTag: a tag's id or label (the list nests each as {tag: {...}, maskRules}).
func resolveTag(r *Runtime, ref string, params map[string]string) error {
	org, err := r.orgID()
	if err != nil {
		return err
	}
	rs, err := listAt(r, fmt.Sprintf("/organizations/%s/governance/tags", org))
	if err != nil {
		return err
	}
	var tags []record
	for _, x := range rs {
		if t, ok := x["tag"].(map[string]any); ok {
			tags = append(tags, record{"id": t["id"], "name": t["label"]})
		}
	}
	t, err := matchOne(tags, ref, "tag")
	if err != nil {
		return err
	}
	params["tag"] = str(t["id"])
	return nil
}

func resolveMask(r *Runtime, ref string, params map[string]string) error {
	org, err := r.orgID()
	if err != nil {
		return err
	}
	rs, err := listAt(r, fmt.Sprintf("/organizations/%s/governance/masks", org))
	if err != nil {
		return err
	}
	for _, m := range rs {
		m["name"] = m["label"]
	}
	m, err := matchOne(rs, ref, "mask")
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
	rs, err := listAt(r, fmt.Sprintf("/organizations/%s/groups", org))
	if err != nil {
		return err
	}
	var groups []record
	for _, x := range rs {
		if g, ok := x["group"].(map[string]any); ok {
			x = record(g)
		}
		groups = append(groups, x)
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

func grantee(c *callCtx) error {
	user, _ := c.Body["user"].(string)
	delete(c.Body, "user")
	switch {
	case c.Params["group"] != "" && user != "":
		return usage("give the role to a group or a user, not both")
	case c.Params["group"] != "":
		c.Body["entityType"], c.Body["entityId"] = "group", c.Params["group"]
	case user != "":
		c.Body["entityType"], c.Body["entityId"] = "user", user
	default:
		return usage("name who gets the role: --group or --user")
	}
	return nil
}

func rowRuleTarget(c *callCtx) error {
	table, _ := c.Body["table"].(string)
	switch {
	case c.Params["tag"] != "" && table != "":
		return usage("a row rule is on a table or on a tag, not both")
	case c.Params["tag"] != "":
		c.Body["tagId"] = c.Params["tag"]
		if expr, _ := c.Body["expression"].(string); !strings.Contains(expr, "{col}") {
			return usage("a rule on a tag names the tagged column as {col}")
		}
	case table != "":
		p, err := rt.needProject()
		if err != nil {
			return err
		}
		c.Body["projectId"] = p
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

func tableFlags() []flagSpec {
	return []flagSpec{
		{Name: "catalog-id", Key: "catalogId", Help: "the catalog's id (1 for Peaka tables)", Required: true},
		{Name: "catalog-name", Key: "catalogName", Help: "the catalog's name in SQL"},
		{Name: "schema", Key: "schemaName", Help: "the schema", Required: true},
		{Name: "table", Key: "tableName", Help: "the table", Required: true},
	}
}

const everyoneID = "00000000-0000-0000-0000-000000000001"

// rulesSetter keeps the WHO=MASK rules as written; resolveRuleNames turns names into ids.
func rulesSetter(v any, body map[string]any) {
	var rules []any
	for _, s := range v.([]string) {
		rules = append(rules, s)
	}
	body["maskRules"] = rules
}

// resolveRuleNames: everyone, user:ID, group:NAME|ID, role:ID; a mask by name or id.
func resolveRuleNames(c *callCtx) error {
	raw, ok := c.Body["maskRules"].([]any)
	if !ok {
		return nil
	}
	var masks []record
	var out []any
	for _, x := range raw {
		s, isText := x.(string)
		if !isText {
			out = append(out, x) // already an object (from --from-file)
			continue
		}
		who, mask, found := strings.Cut(s, "=")
		if !found {
			return usage("a rule is WHO=MASK: %q", s)
		}
		if masks == nil {
			org, err := rt.orgID()
			if err != nil {
				return err
			}
			if masks, err = listAt(rt, fmt.Sprintf("/organizations/%s/governance/masks", org)); err != nil {
				return err
			}
			for _, m := range masks {
				m["name"] = m["label"]
			}
		}
		m, err := matchOne(masks, mask, "mask")
		if err != nil {
			return err
		}
		rule := map[string]any{"maskId": str(m["id"])}
		kind, id, _ := strings.Cut(who, ":")
		switch strings.ToLower(kind) {
		case "everyone":
			rule["entityType"], rule["entityId"] = "user", everyoneID
		case "user":
			rule["entityType"], rule["entityId"] = "user", id
		case "group":
			p := map[string]string{}
			if err := resolveGroup(rt, id, p); err != nil {
				return err
			}
			rule["entityType"], rule["entityId"] = "group", p["group"]
		case "role":
			rule["roleId"] = id
		default:
			return usage("who is everyone, user:ID, group:NAME or role:ID: %q", who)
		}
		out = append(out, rule)
	}
	c.Body["maskRules"] = out
	return nil
}

// keepTagFields: an update replaces the tag; what the flags leave out is read from the tag as it is.
func keepTagFields(c *callCtx) error {
	org, err := rt.orgID()
	if err != nil {
		return err
	}
	client, err := rt.api()
	if err != nil {
		return err
	}
	var current struct {
		Tag       map[string]any   `json:"tag"`
		MaskRules []map[string]any `json:"maskRules"`
	}
	if err := client.Get(rt.Ctx, fmt.Sprintf("/organizations/%s/governance/tags/%s", org, c.Params["tag"]), nil, &current); err != nil {
		return err
	}
	for _, k := range []string{"label", "description", "active"} {
		if _, set := c.Body[k]; !set {
			c.Body[k] = current.Tag[k]
		}
	}
	if _, set := c.Body["maskRules"]; !set {
		var rules []any
		for _, r := range current.MaskRules {
			rule := map[string]any{"maskId": r["maskId"]}
			for _, k := range []string{"entityType", "entityId", "roleId", "customMaskExpression"} {
				if r[k] != nil {
					rule[k] = r[k]
				}
			}
			rules = append(rules, rule)
		}
		c.Body["maskRules"] = rules
	}
	return nil
}

// ==================== apply ====================

// govFile is what `governance apply` reads: the tags and row rules an organization should have.
type govFile struct {
	Tags []struct {
		Label       string   `yaml:"label"`
		Description string   `yaml:"description"`
		Active      *bool    `yaml:"active"`
		Rules       []string `yaml:"rules"`
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
		Long: `Creates what is missing and updates what differs, by tag label and row rule name; a column tag already there is
left as it is; nothing is deleted. Tags come first, then column tags, then row rules (a rule on a tag needs a
tagged column).
A change that reveals classified data may wait for a second administrator (202): it is reported, not retried.

  tags:
    - label: PHI
      description: Protected health information
      rules: ["group:Clinicians=Unmasked", "everyone=Nullify"]
  rowRules:
    - name: Own region
      tag: REGION
      expression: "{col} = current_user_props('region')"
  columnTags:
    - {catalogId: "1", catalogName: peaka, schema: table, table: patients, column: ssn, tag: PHI}

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
	tagsByLabel := index(existing, func(x record) (string, string) {
		t, _ := x["tag"].(map[string]any)
		return strings.ToUpper(str(t["label"])), str(t["id"])
	})
	rules, err := listAt(rt, base+"/row-rules")
	if err != nil {
		return err
	}
	rulesByName := index(rules, func(r record) (string, string) { return strings.ToLower(str(r["name"])), str(r["id"]) })
	var ambiguous []string
	for _, t := range want.Tags {
		ambiguous = append(ambiguous, twice(tagsByLabel, strings.ToUpper(t.Label), "tag")...)
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
		rules := make([]any, len(t.Rules))
		for i, r := range t.Rules {
			rules[i] = r
		}
		c := &callCtx{Params: map[string]string{}, Body: map[string]any{"label": t.Label, "description": t.Description, "active": t.Active == nil || *t.Active,
			"maskRules": rules}}
		if err := resolveRuleNames(c); err != nil {
			note("tag", t.Label, "resolve", nil, err)
			continue
		}
		if id, ok := byLabel[strings.ToUpper(t.Label)]; ok {
			resp, err := send(client, http.MethodPut, base+"/tags/"+id, c.Body)
			note("tag", t.Label, "update", resp, err)
		} else {
			resp, err := send(client, http.MethodPost, base+"/tags", c.Body)
			note("tag", t.Label, "create", resp, err)
		}
	}
	if len(want.Tags) > 0 && !gf.dryRun {
		if existing, err = listAt(rt, base+"/tags"); err != nil {
			return err
		}
		for k, ids := range index(existing, func(x record) (string, string) {
			t, _ := x["tag"].(map[string]any)
			return strings.ToUpper(str(t["label"])), str(t["id"])
		}) {
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
		there, err := listAt(rt, fmt.Sprintf("%s/projects/%s/catalogs/%s/schemas/%s/tables/%s/column-tags", base, project,
			url.PathEscape(ct.CatalogID), url.PathEscape(ct.Schema), url.PathEscape(ct.Table)))
		if err != nil {
			note("column tag", name, "add", nil, err)
			continue
		}
		tagged := false
		for _, x := range there {
			if strings.EqualFold(str(x["columnName"]), ct.Column) && str(x["tagId"]) == id {
				tagged = true
			}
		}
		if tagged {
			report = append(report, map[string]any{"kind": "column tag", "name": name, "action": "none", "result": "unchanged"})
			continue
		}
		resp, err := send(client, http.MethodPost, base+"/column-tags", map[string]any{"projectId": project, "catalogId": ct.CatalogID,
			"catalogName": ct.CatalogName, "schemaName": ct.Schema, "tableName": ct.Table, "columnName": ct.Column, "tagId": id, "reason": ct.Reason})
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
			resp, err := send(client, http.MethodPut, base+"/row-rules/"+id, map[string]any{"expression": rr.Expression, "enabled": enabled})
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
			body["tagId"] = id
		case rr.Table != "" && project != "":
			body["projectId"], body["table"] = project, rr.Table
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
	switch {
	case tagLabel != "":
		if str(existing["targetType"]) != "TAG" || str(existing["tagId"]) != tagID {
			return fmt.Sprintf("the rule is on %s, not on the tag %s", describeTarget(existing), tagLabel)
		}
	case table != "":
		sameTable := strings.EqualFold(str(existing["table"]), table) ||
			strings.EqualFold(str(existing["catalogName"])+"."+str(existing["schemaName"])+"."+str(existing["tableName"]), table)
		if str(existing["targetType"]) == "TAG" || !sameTable || (project != "" && str(existing["projectId"]) != project) {
			return fmt.Sprintf("the rule is on %s, not on %s", describeTarget(existing), table)
		}
	}
	return ""
}

func describeTarget(r record) string {
	if str(r["targetType"]) == "TAG" {
		return "the tag " + str(r["tagId"])
	}
	if t := str(r["table"]); t != "" {
		return t + " in project " + str(r["projectId"])
	}
	return str(r["catalogName"]) + "." + str(r["schemaName"]) + "." + str(r["tableName"]) + " in project " + str(r["projectId"])
}

// send writes; under --dry-run the client prints the request instead.
func send(client *api.Client, method, path string, body map[string]any) (*api.Response, error) {
	return client.Do(rt.Ctx, method, path, url.Values{}, body, nil)
}
