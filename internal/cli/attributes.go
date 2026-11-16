package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/peakacom/peaka-cli/internal/api"
	"github.com/spf13/cobra"
)

// Member attributes, members and roles through the public API (DEV-4250): what a row rule's
// current_user_props('key') reads, the values members and groups hold, and the ids and SQL names a rule's exception,
// a masking policy and a grant name.

const attrPath = "/organizations/{org}/attributes"

func newAttributesCmd() *cobra.Command {
	definition := func(create bool) []flagSpec {
		return []flagSpec{
			{Name: "key", Help: "how SQL names it: current_user_props('KEY'); it never changes", Required: create},
			{Name: "type", Help: "STRING (default), NUMBER, BOOLEAN or DATE"},
			{Name: "cardinality", Help: "SINGLE (default) or MULTI"},
			{Name: "source", Key: "sources", Kind: "strings", Help: "where values come from, in order: CUSTOM (set in Peaka), DIRECTORY, PEAKA_GROUP, DIRECTORY_GROUP, TABLE; repeatable"},
			{Name: "description", Help: "what it says about a member"},
			{Name: "sensitive", Kind: "bool", Help: "its values are hidden on the screens"},
			{Name: "on-error", Key: "onError", Help: "when its source cannot be read: FAIL (the query fails) or NULL"},
		}
	}
	cmd := group("attributes", "Member attributes: what current_user_props('key') reads in a row rule", []string{"attribute", "attrs"},
		opSpec{Use: "list", Short: "List the organization's member attributes", Method: http.MethodGet, Path: attrPath,
			Unwrap: "items", Fields: []string{"key", "type", "cardinality", "sources", "description"}},
		opSpec{Use: "describe KEY", Aliases: []string{"get"}, Short: "Show an attribute", Method: http.MethodGet, Path: attrPath + "/{key}",
			Args: []argSpec{{Name: "KEY", Param: "key"}}},
		opSpec{Use: "create", Short: "Define an attribute", Method: http.MethodPost, Path: attrPath, Body: true,
			Long: `  peaka gov attributes create --key region --description "Sales region"
  peaka gov attributes create --key departments --cardinality MULTI --source DIRECTORY --source CUSTOM`,
			Flags: definition(true), Prepare: attributeDefaults},
		opSpec{Use: "update KEY", Short: "Change an attribute; what is not given stays", Method: http.MethodPut, Path: attrPath + "/{key}", Body: true,
			Args: []argSpec{{Name: "KEY", Param: "key"}}, Flags: definition(false)[1:], Prepare: attributeMerged},
		opSpec{Use: "delete KEY", Short: "Delete an attribute (refused while a row rule reads it)", Method: http.MethodDelete, Path: attrPath + "/{key}",
			Args: []argSpec{{Name: "KEY", Param: "key"}}, Confirm: "Delete attribute {0} and every value of it?", Empty: "Deleted."},
		opSpec{Use: "values MEMBER", Short: "A member's values and where each came from (MEMBER: id or email)", Method: http.MethodGet,
			Path: attrPath + "/values/members/{user}", Args: []argSpec{{Name: "MEMBER", Resolve: resolveMember}}},
		opSpec{Use: "set MEMBER KEY", Short: "Set a member's value in Peaka: --value, repeatable for a MULTI attribute", Method: http.MethodPut,
			Path: attrPath + "/values/members/{user}/{key}", Body: true,
			Long:  "  peaka gov attributes set ana@example.com region --value EMEA",
			Args:  []argSpec{{Name: "MEMBER", Resolve: resolveMember}, {Name: "KEY", Param: "key"}},
			Flags: []flagSpec{{Name: "value", Key: "values", Kind: "strings", Help: "the value; repeatable", Required: true}}},
		opSpec{Use: "unset MEMBER KEY", Short: "Take off the value set in Peaka for a member (a group's or the directory's stays)", Method: http.MethodDelete,
			Path: attrPath + "/values/members/{user}/{key}", Args: []argSpec{{Name: "MEMBER", Resolve: resolveMember}, {Name: "KEY", Param: "key"}},
			Empty: "Removed."},
		opSpec{Use: "group-values GROUP", Short: "The values held on a group", Method: http.MethodGet, Path: attrPath + "/values/groups/{group}",
			Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}}},
		opSpec{Use: "set-group GROUP KEY", Short: "Set a value on a group: every member of it gets it", Method: http.MethodPut,
			Path: attrPath + "/values/groups/{group}/{key}", Body: true,
			Args:  []argSpec{{Name: "GROUP", Resolve: resolveGroup}, {Name: "KEY", Param: "key"}},
			Flags: []flagSpec{{Name: "value", Key: "values", Kind: "strings", Help: "the value; repeatable", Required: true}}},
		opSpec{Use: "unset-group GROUP KEY", Short: "Take a value off a group", Method: http.MethodDelete,
			Path: attrPath + "/values/groups/{group}/{key}", Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}, {Name: "KEY", Param: "key"}},
			Empty: "Removed."},
		opSpec{Use: "history", Short: "Who set or removed which value, newest first", Method: http.MethodGet, Path: attrPath + "/values/history",
			Flags: []flagSpec{
				{Name: "target-type", In: "query", Key: "targetType", Help: "USER or GROUP"},
				{Name: "target", In: "query", Key: "targetId", Help: "the member's or group's id"},
				{Name: "limit", In: "query", Kind: "int", Help: "records a page (at most 500)"},
				{Name: "cursor", In: "query", Help: "the page after"},
			},
			Unwrap: "items", Fields: []string{"createdAt", "actor", "targetType", "targetId", "key", "oldValue", "newValue"}},
	)
	cmd.AddCommand(newAttributesImportCmd())
	return cmd
}

// attributeDefaults: an attribute set in Peaka, one text value, unless told.
func attributeDefaults(c *callCtx) error {
	for k, v := range map[string]any{"type": "STRING", "cardinality": "SINGLE"} {
		if s, _ := c.Body[k].(string); s == "" {
			c.Body[k] = v
		}
	}
	if _, given := c.Body["sources"]; !given {
		c.Body["sources"] = []string{"CUSTOM"}
	}
	return upperFields(c.Body, "type", "cardinality", "onError")
}

// attributeMerged: the API replaces an attribute whole; what the command line does not give is kept from the one
// that is there.
func attributeMerged(c *callCtx) error {
	client, err := rt.api()
	if err != nil {
		return err
	}
	var now map[string]any
	if err := client.Get(rt.Ctx, fmt.Sprintf("/organizations/%s/attributes/%s", c.Params["org"], url.PathEscape(c.Params["key"])), nil, &now); err != nil {
		return err
	}
	for _, k := range []string{"key", "type", "cardinality", "sources", "mergeGroups", "sensitive", "description", "onError", "table"} {
		if _, given := c.Body[k]; !given && now[k] != nil {
			c.Body[k] = now[k]
		}
	}
	return upperFields(c.Body, "type", "cardinality", "onError")
}

func upperFields(body map[string]any, fields ...string) error {
	for _, f := range fields {
		if s, ok := body[f].(string); ok {
			body[f] = strings.ToUpper(strings.TrimSpace(s))
		}
	}
	if xs, ok := body["sources"].([]string); ok {
		for i := range xs {
			xs[i] = strings.ToUpper(strings.TrimSpace(xs[i]))
		}
	}
	return nil
}

func newAttributesImportCmd() *cobra.Command {
	var file string
	var apply bool
	cmd := &cobra.Command{
		Use:   "import -f FILE.csv",
		Short: "Set many values from a CSV of target,key,value; reports what would change unless --apply",
		Long: `Rows of target,key,value: a member by email or id, a group by name. Without --apply nothing is written and the
report says what would change; a file with any error is never applied.

  peaka gov attributes import -f regions.csv
  peaka gov attributes import -f regions.csv --apply`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := readInput(file)
			if err != nil {
				return usage("-f: %v", err)
			}
			org, err := rt.orgID()
			if err != nil {
				return err
			}
			q := url.Values{"dryRun": {fmt.Sprint(!apply)}}
			return rt.call(http.MethodPost, fmt.Sprintf("/organizations/%s/attributes/values/import", org), q,
				&api.RawBody{ContentType: "text/csv", Data: raw}, "", nil, "")
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "the CSV file (- for stdin)")
	cmd.Flags().BoolVar(&apply, "apply", false, "write the values (the default only reports)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newMembersCmd() *cobra.Command {
	return group("members", "The organization's members: the ids a rule, a policy and a value name", []string{"member"},
		opSpec{Use: "list", Short: "List members (--q finds by name or email)", Method: http.MethodGet, Path: "/organizations/{org}/members",
			Flags:  []flagSpec{{Name: "q", In: "query", Help: "text in the member's name or email"}},
			Unwrap: "items", Fields: []string{"id", "email", "firstName", "lastName"}},
	)
}

func newRolesCmd() *cobra.Command {
	return group("roles", "The organization's roles: a grant names one by id, a row rule by its SQL name", []string{"role"},
		opSpec{Use: "list", Short: "List roles", Method: http.MethodGet, Path: "/organizations/{org}/roles",
			Unwrap: "items", Fields: []string{"id", "name", "sqlName", "scope", "predefined"}},
	)
}

// resolveMember: a member's id as it is, or an email looked up among the organization's members.
func resolveMember(r *Runtime, ref string, params map[string]string) error {
	ref = strings.TrimSpace(ref)
	if !strings.Contains(ref, "@") {
		params["user"] = ref
		return nil
	}
	org, err := r.orgID()
	if err != nil {
		return err
	}
	found, err := listAt(r, fmt.Sprintf("/organizations/%s/members?%s", org, url.Values{"q": {ref}}.Encode()))
	if err != nil {
		return err
	}
	for _, m := range found {
		if strings.EqualFold(str(m["email"]), ref) {
			params["user"] = str(m["id"])
			return nil
		}
	}
	return api.Exit(api.ExitNotFound, "no member with email %s in the organization", ref)
}
