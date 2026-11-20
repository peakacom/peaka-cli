package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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

// auditKinds are the audit log's event kinds (Studio's GovernanceEventStore.Kind), offered to `gov audit events --kind`.
var auditKinds = []string{
	"TAG_APPLIED", "TAG_REMOVED", "TAG_INHERITED", "TAG_INHERITANCE_ENDED",
	"DECLASSIFIED", "DECLASSIFICATION_REVOKED", "DECLASSIFICATION_EXPIRED", "DECLASSIFICATION_DROPPED",
	"TAG_CREATED", "TAG_UPDATED", "TAG_DELETED",
	"CUSTOM_MASK_CREATED", "CUSTOM_MASK_UPDATED", "CUSTOM_MASK_DELETED",
	"ROW_RULE_CREATED", "ROW_RULE_UPDATED", "ROW_RULE_DELETED",
	"MASKING_POLICY_CREATED", "MASKING_POLICY_UPDATED", "MASKING_POLICY_DELETED",
	"PUBLISHER_ACCESS_REQUESTED", "PUBLISHER_ACCESS_APPROVED", "PUBLISHER_ACCESS_REJECTED", "PUBLISHER_ACCESS_REVOKED",
	"PUBLISHER_ACCESS_LAPSED",
	"MEMBER_SUSPENDED", "MEMBER_REACTIVATED",
	"CHANGE_REQUESTED", "CHANGE_APPROVED", "CHANGE_REJECTED", "CHANGE_WITHDRAWN", "CHANGE_EXPIRED",
	"ACCESS_REVIEW_STARTED", "ACCESS_REVIEW_KEPT", "ACCESS_REVIEW_REMOVED", "ACCESS_REVIEW_EXPECTED", "ACCESS_REVIEW_UNEXPECTED",
	"ACCESS_REVIEW_CLOSED",
	"CLASSIFICATION_SOURCE_ADDED", "CLASSIFICATION_SOURCE_UPDATED", "CLASSIFICATION_SOURCE_REMOVED", "CLASSIFICATION_MAPPING_CHANGED",
	"TAG_SUGGESTION_APPROVED", "TAG_SUGGESTION_REJECTED", "TAG_SUGGESTION_WITHDRAWN",
	"TAGGED_COLUMN_LOST", "TAGGED_COLUMN_RECREATED", "TAG_MOVE_SUGGESTED", "TAG_MOVE_APPROVED", "TAG_MOVE_REJECTED",
	"COLUMN_REPORT_STALE",
	"GOVERNANCE_SETTINGS_CHANGED",
	"MEMBERSHIP_DISCLOSURE_ACCEPTED",
	"MQ_REFRESH_REQUESTED",
	"GROUP_MEMBER_ADDED", "GROUP_MEMBER_REMOVED",
	"CATALOG_SHARED", "CATALOG_UNSHARED",
	"GRANT_ADDED", "GRANT_CHANGED", "GRANT_REVOKED",
	"GROUP_CREATED", "GROUP_UPDATED", "GROUP_DELETED",
	"ATTRIBUTE_DEFINITION_CREATED", "ATTRIBUTE_DEFINITION_UPDATED",
	"DIRECTORY_MAPPING_CHANGED",
}

// auditFields are the audit log's table columns; "?" ones only when an event on the page has them.
var auditFields = []string{"occurredAt", "kind", "actor", "actorKind", "?impersonator", "subject.tag", "subject.table", "subject.column",
	"?subject.object", "?subject.targetProjectId", "?after.requestedBy", "reason"}

// wrapList joins words with ", " into lines of at most width characters, indented by two spaces.
func wrapList(words []string, width int) string {
	var b strings.Builder
	line := "  "
	for i, w := range words {
		if i < len(words)-1 {
			w += ","
		}
		if len(line) > 2 && len(line)+1+len(w) > width {
			b.WriteString(strings.TrimRight(line, " ") + "\n")
			line = "  "
		}
		if len(line) > 2 {
			line += " "
		}
		line += w
	}
	return b.String() + line
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func newGovernanceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "governance", Aliases: []string{"gov"}, Short: "Data governance: tags, masking policies, masks, column tags, row rules, policy, audit"}
	cmd.AddCommand(
		group("tags", "Classification tags; what readers see of tagged columns is a masking policy", []string{"tag"},
			opSpec{Use: "list", Short: "List tags (the whole tree; --parent one level, --q a search)", Method: http.MethodGet, Path: govPath + "/tags",
				Flags: []flagSpec{
					{Name: "parent", In: "query", Key: "parentId", Help: "only the tags right under this tag (its id), or root for the top level"},
					{Name: "q", In: "query", Help: "text in the tag's name or key"},
					{Name: "status", In: "query", Help: "ACTIVE or DEPRECATED"},
				},
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
					{Name: "active", Kind: "bool", Help: "in use: an inactive tag classifies nothing", Default: true},
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
					{Name: "active", Kind: "bool", Help: "in use: an inactive tag classifies nothing"},
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
				Flags: []flagSpec{{Name: "tag", Help: "only the policies on this tag: name, key or id", Resolve: resolveTag},
					{Name: "enabled", In: "query", Help: "true or false: only the policies switched on or off"},
					{Name: "q", In: "query", Help: "text in the policy's name"}},
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
			opSpec{Use: "describe MASK", Aliases: []string{"get"}, Short: "Show a mask (a custom mask by name or id, a predefined one by key)", Method: http.MethodGet,
				Path: govPath + "/masks/{mask}", Args: []argSpec{{Name: "MASK", Resolve: resolveMask}}},
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
				Unwrap:  "items", Fields: []string{"id", "column", "tagId", "origin", "status", "ineffective", "reason"}},
			opSpec{Use: "search", Short: "The organization's column tags: by tag, project, catalog, schema, table or a text", Method: http.MethodGet,
				Path: govPath + "/column-tags",
				Long: `  peaka gov column-tags search --tag PHI
  peaka gov column-tags search --project OtnUT0en --catalog-id 7 --schema public --q ssn`,
				Flags: []flagSpec{
					{Name: "tag", In: "query", Help: "only this tag: name, key or id"},
					{Name: "project", In: "query", Key: "projectId", Help: "only this project's tables (its id)"},
					{Name: "catalog-id", In: "query", Key: "catalogId", Help: "only this catalog"},
					{Name: "schema", In: "query", Help: "only this schema"},
					{Name: "table", In: "query", Help: "only this table"},
					{Name: "q", In: "query", Help: "text in the column's or table's name"},
				},
				Unwrap: "items", Fields: []string{"projectId", "catalogId", "schema", "table", "column", "tagId", "origin", "status", "id"}},
			opSpec{Use: "set", Short: "Set a table's column tags as a whole: what is not given comes off (inherited tags stay)", Method: http.MethodPut,
				Path: govPath + "/column-tags/table", Body: true,
				Long: `  peaka gov column-tags set --catalog-id 7 --schema public --table people --tag-column ssn=PHI --tag-column email=PII
  peaka gov column-tags set --catalog-id 7 --schema public --table people --none     # takes every tag off`,
				Flags: []flagSpec{
					{Name: "catalog-id", Key: "catalogId", Help: "the catalog's id (1 for Peaka tables)", Required: true},
					{Name: "schema", Key: "schema", Help: "the schema", Required: true},
					{Name: "table", Key: "table", Help: "the table", Required: true},
					{Name: "tag-column", Key: "tagColumns", Kind: "strings", Help: "COLUMN=TAG (name, key or id); repeatable"},
					{Name: "none", Key: "none", Kind: "bool", Help: "no tag on any column (takes them all off)"},
					{Name: "reason", Help: "why"},
				},
				Confirm: "Set the column tags of the table: tags not given come off. Continue?",
				Prepare: chain(projectIntoBody, tableColumnTags)},
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
				Flags: []flagSpec{
					{Name: "tag", In: "query", Help: "only this tag: name, key or id"},
					{Name: "catalog-id", In: "query", Key: "catalogId", Help: "only this catalog"},
					{Name: "schema", In: "query", Help: "only this schema"},
					{Name: "table", In: "query", Help: "only this table"},
				},
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
			opSpec{Use: "list", Short: "List row rules (a table's with --catalog-id --schema --table, a tag's with --tag)", Method: http.MethodGet,
				Path: "/organizations/{org}/governance/row-rules",
				Flags: []flagSpec{
					{Name: "project", In: "query", Key: "projectId", Help: "the table's project (its id)"},
					{Name: "catalog-id", In: "query", Key: "catalogId", Help: "the table's catalog"},
					{Name: "schema", In: "query", Help: "the table's schema"},
					{Name: "table", In: "query", Help: "the table's name"},
					{Name: "tag", In: "query", Help: "only the rules on this tag: name, key or id"},
					{Name: "target", In: "query", Key: "targetType", Help: "TABLE or TAG"},
					{Name: "enabled", In: "query", Help: "true or false"},
					{Name: "q", In: "query", Help: "text in the name or the condition"},
				},
				Unwrap: "items", Fields: []string{"id", "name", "target.type", "target.table", "target.tagId", "enabled", "expression", "exceptions"}},
			opSpec{Use: "describe ID", Aliases: []string{"get"}, Short: "Show a row rule", Method: http.MethodGet, Path: govPath + "/row-rules/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}},
			opSpec{Use: "reaching", Short: "Every row rule a read of a table gets: its own and the tag rules that reach it", Method: http.MethodGet,
				Path: govPath + "/row-rules/reaching",
				Long: "  peaka gov row-rules reaching --catalog-id 7 --schema payment --table charges",
				Flags: []flagSpec{
					{Name: "catalog-id", In: "query", Key: "catalogId", Help: "the table's catalog", Required: true},
					{Name: "schema", In: "query", Help: "the table's schema", Required: true},
					{Name: "table", In: "query", Help: "the table", Required: true},
				},
				Prepare: projectIntoQuery},
			opSpec{Use: "preview", Short: "How many rows a member would read with a condition; nothing is saved", Method: http.MethodPost,
				Path: govPath + "/row-rules/preview", Body: true,
				Long: "  peaka gov row-rules preview --table peaka.table.orders --expression \"region = current_user_props('region')\" --as ana@example.com",
				Flags: []flagSpec{
					{Name: "table", Help: "catalog.schema.table, as the project's SQL names it", Required: true},
					{Name: "expression", Help: "the SQL condition to try", Required: true},
					{Name: "as", Help: "the member to read as: id or email", Required: true, Resolve: resolveMember},
					{Name: "rule", Key: "ruleId", Help: "the rule being changed (its id): left out of the rules already on the table"},
				},
				Prepare: chain(projectIntoBody, paramsIntoBody("user", "userId"))},
			opSpec{Use: "problems", Short: "Rules on a tag whose condition does not hold on a column the tag reaches", Method: http.MethodGet,
				Path: govPath + "/row-rules/problems", Unwrap: "items",
				Fields: []string{"ruleName", "sqlName", "column", "detail", "foundAt"}},
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
					{Name: "except", Key: "except", Kind: "strings", Help: "who reads every row: user:ID, group:SQL_NAME|ID or role:SQL_NAME; repeatable"},
				},
				Prepare: chain(rowRuleTarget, rowRuleExceptions)},
			opSpec{Use: "update ID", Short: "Change a row rule's name, condition or state (202: waits for approval)", Method: http.MethodPatch, Path: govPath + "/row-rules/{id}", Body: true,
				Args: []argSpec{{Name: "ID", Param: "id"}},
				Flags: []flagSpec{{Name: "name", Help: "the rule's name"}, {Name: "expression", Help: "a SQL condition"}, {Name: "enabled", Kind: "bool", Help: "applies"},
					{Name: "except", Key: "except", Kind: "strings", Help: "who reads every row (replaces them all): user:ID, group:SQL_NAME|ID or role:SQL_NAME; repeatable"},
					{Name: "no-exceptions", Key: "noExceptions", Kind: "bool", Help: "take every exception off"}},
				Prepare: rowRuleExceptions},
			opSpec{Use: "delete ID", Short: "Delete a row rule (202: waits for approval)", Method: http.MethodDelete, Path: govPath + "/row-rules/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}, Confirm: "Delete row rule {0}?", Empty: "Deleted."},
		),
		group("policy", "The organization's governance policy: show it, take a template, change fields, proposals, history", nil,
			opSpec{Use: "show", Aliases: []string{"get", "describe"}, Short: "Show the policy", Method: http.MethodGet, Path: govPath + "/policy"},
			opSpec{Use: "templates", Short: "The built-in templates and the field catalog", Method: http.MethodGet, Path: govPath + "/policy/templates"},
			opSpec{Use: "diff TEMPLATE", Short: "Where the policy differs from a template", Method: http.MethodGet, Path: govPath + "/policy/templates/{t}/diff",
				Args: []argSpec{{Name: "TEMPLATE", Param: "t"}}},
			opSpec{Use: "use TEMPLATE", Short: "Take a template's values (202: a proposal waits for approval)", Method: http.MethodPut, Path: govPath + "/policy/template",
				Body: true, Args: []argSpec{{Name: "TEMPLATE", Param: "template"}}, Flags: []flagSpec{{Name: "reason", Help: "why"}},
				Prepare: paramsIntoBody("template", "template"), Confirm: "Replace the policy's values with template {0}?"},
			opSpec{Use: "set", Short: "Change fields: --change untaggedColumn=DENY (202: a proposal waits for approval)", Method: http.MethodPatch,
				Path: govPath + "/policy", Body: true,
				Flags: []flagSpec{{Name: "change", Key: "fieldPairs", Kind: "strings", Help: "NAME=VALUE (camelCase, as `policy show` names it); repeatable", Required: true},
					{Name: "reason", Help: "why"}},
				Prepare: policyFields},
			opSpec{Use: "history", Short: "Every change of the policy, newest first", Method: http.MethodGet, Path: govPath + "/policy/history",
				Unwrap: "items", Fields: []string{"createdAt", "actor", "fromVersion", "toVersion", "direction", "reason"}},
			opSpec{Use: "proposals", Short: "Policy changes waiting for approval (-f status=ALL for every one)", Method: http.MethodGet,
				Path: govPath + "/policy/proposals", Query: true, Unwrap: "items", Fields: []string{"proposal.id", "proposal.direction", "proposal.proposedBy", "stale"}},
			opSpec{Use: "proposal PROPOSAL", Short: "Show a policy proposal, whatever its status, and the changes it makes", Method: http.MethodGet,
				Path: govPath + "/policy/proposals/{id}", Args: []argSpec{{Name: "PROPOSAL", Param: "id"}}},
			opSpec{Use: "approve PROPOSAL", Short: "Apply a proposal another administrator made", Method: http.MethodPost, Path: govPath + "/policy/proposals/{id}/approve",
				Body: true, Args: []argSpec{{Name: "PROPOSAL", Param: "id"}}, Flags: []flagSpec{{Name: "reason", Help: "why"}}},
			opSpec{Use: "reject PROPOSAL", Short: "Refuse a proposal", Method: http.MethodPost, Path: govPath + "/policy/proposals/{id}/reject",
				Body: true, Args: []argSpec{{Name: "PROPOSAL", Param: "id"}}, Flags: []flagSpec{{Name: "reason", Help: "why"}}},
			opSpec{Use: "unlock", Short: "Unlock a locked policy (the organization's owner only)", Method: http.MethodPost, Path: govPath + "/policy/unlock", Body: true,
				Flags: []flagSpec{{Name: "reason", Help: "why", Required: true}}, Confirm: "Unlock the organization's governance policy?"},
		),
		group("reviews", "Access reviews: a round in which every rule and reader of classified data is confirmed, removed or flagged", []string{"access-reviews"},
			opSpec{Use: "list", Short: "Review rounds, newest first", Method: http.MethodGet, Path: govPath + "/access-reviews",
				Unwrap: "items", Fields: []string{"id", "status", "startedBy", "startedAt", "closedAt"}},
			opSpec{Use: "start", Short: "Open a review round", Method: http.MethodPost, Path: govPath + "/access-reviews", Confirm: "Open an access review round?"},
			opSpec{Use: "describe ID", Aliases: []string{"get"}, Short: "A round and its items", Method: http.MethodGet, Path: govPath + "/access-reviews/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}, Unwrap: "items", Fields: []string{"id", "kind", "summary", "decision", "decidedBy"}},
			opSpec{Use: "decide ID ITEM", Short: "Decide an item: --decision CONFIRMED|REMOVED|FLAGGED|PENDING", Method: http.MethodPost,
				Path: govPath + "/access-reviews/{id}/items/{item}/decision", Body: true, Args: []argSpec{{Name: "ID", Param: "id"}, {Name: "ITEM", Param: "item"}},
				Flags: []flagSpec{{Name: "decision", Help: "CONFIRMED, REMOVED (a rule: deleted), FLAGGED or PENDING", Required: true}, {Name: "note", Help: "why"}}},
			opSpec{Use: "close ID", Short: "End a round", Method: http.MethodPost, Path: govPath + "/access-reviews/{id}/close", Args: []argSpec{{Name: "ID", Param: "id"}}},
			opSpec{Use: "export ID", Short: "A round as CSV", Method: http.MethodGet, Path: govPath + "/access-reviews/{id}/export", Args: []argSpec{{Name: "ID", Param: "id"}}},
		),
		group("disclosures", "Membership disclosures a publisher accepts for a query that runs with their access", []string{"disclosure"},
			disclosureAcceptSpec()),
		group("publications", "Requests to publish a query that runs with its author's access", []string{"publication-requests"},
			opSpec{Use: "list", Short: "Requests, newest first (-f status=ALL for every one; PENDING by default)", Method: http.MethodGet,
				Path: govPath + "/publication-requests", Query: true, Unwrap: "items", Fields: []string{"id", "objectName", "requestedBy", "status", "createdAt"}},
			opSpec{Use: "request QUERY", Short: "Ask a governance administrator to let a query in the project in context run with your access",
				Long: `Where the policy requires an approval, a save with --access definer is refused until a request is approved.
Once it is, save the query again with --access definer.

  peaka gov publications request ward_counts --reason "Ward totals for the clinic dashboard"
  peaka gov publications request ward_counts --reason "…" --disclosure-fingerprint 9b2e…`,
				Method: http.MethodPost, Path: govPath + "/publication-requests", Body: true,
				Args: []argSpec{{Name: "QUERY", Resolve: resolvePublicationQuery}},
				Flags: []flagSpec{{Name: "reason", Help: "why it should run with your access", Required: true},
					{Name: "disclosure-fingerprint", Help: "the membership disclosure accepted for it, when it reveals one"}},
				Prepare: publicationRequest},
			opSpec{Use: "approve ID", Short: "Let the query run with its author's access", Method: http.MethodPost, Path: govPath + "/publication-requests/{id}/approve",
				Body: true, Args: []argSpec{{Name: "ID", Param: "id"}}, Flags: []flagSpec{{Name: "reason", Help: "why"}}},
			opSpec{Use: "reject ID", Short: "Refuse a request", Method: http.MethodPost, Path: govPath + "/publication-requests/{id}/reject",
				Body: true, Args: []argSpec{{Name: "ID", Param: "id"}}, Flags: []flagSpec{{Name: "reason", Help: "why"}}},
			opSpec{Use: "revoke ID", Short: "An approved publication runs with its reader's access again", Method: http.MethodDelete,
				Path: govPath + "/publication-requests/{id}", Args: []argSpec{{Name: "ID", Param: "id"}}, Confirm: "Revoke publication {0}?"},
		),
		opSpec{Use: "impact", Short: "What turning classification enforcement on would change (-f projectId=… for one project)",
			Long: `Read from metadata alone: untagged columns per table, tables refused for having classified columns and no row rule,
classifications that cannot be read, materialized queries that would be refused, declassifications that lift every path.

  peaka gov impact
  peaka gov impact -f projectId=OtnUT0en`,
			Method: http.MethodGet, Path: govPath + "/enforcement-impact", Query: true}.command(),
		group("audit", "The organization's audit log", nil,
			opSpec{Use: "events", Short: "Changes to classification, rules, access and the policy, newest first",
				Long: `Each event names its actor and actorKind (USER, or SYSTEM for Peaka itself), and impersonator when someone was
signed in as the actor. --kind takes the kinds below, comma-separated.

  peaka gov audit events --kind TAG_APPLIED,ROW_RULE_CREATED --since 2026-11-01T00:00:00Z
  peaka gov audit events --actor ana@example.com --table charges
  peaka gov audit events --kind MQ_REFRESH_REQUESTED --fields occurredAt,actor,subject.object,after.requestedBy,after.runsAs
  peaka gov audit events --kind GROUP_MEMBER_ADDED,GROUP_MEMBER_REMOVED --since 2026-11-01T00:00:00Z
  peaka gov audit events --kind CATALOG_SHARED,CATALOG_UNSHARED --fields occurredAt,kind,actor,subject.object,subject.targetProjectId,after.via

MQ_REFRESH_REQUESTED: after.requestedBy is USER, API_KEY, SERVICE_ACCOUNT, SCHEDULE or SERVICE; after.runsAs is
PUBLISHER or SERVICE. Row rule events name each exception by kind, id and name. MEMBERSHIP_DISCLOSURE_ACCEPTED
records the fingerprint a publisher accepted. CATALOG_SHARED and CATALOG_UNSHARED name the catalog
(subject.catalogId, its name as subject.object, subject.projectId) and the project it was shared with (subject.targetProjectId);
after.via says how a share ended: SOURCE_UNSHARED, TARGET_REMOVED or CATALOG_DELETED.
GRANT_* name the target (subject.target: WORKSPACE, PROJECT, ORGANIZATION or ORGANIZATION_SEAT) and the grantee
(subject.entityType, subject.entityId); before.roleId and after.roleId hold the role. GROUP_UPDATED keeps only the
fields that changed; GROUP_DELETED keeps before.members. ATTRIBUTE_DEFINITION_* keep the whole definition, and
DIRECTORY_MAPPING_CHANGED every users and groups mapping of subject.connectionId before and after. Masking policy
events keep defaults and exceptions as lists.

Kinds:
` + wrapList(auditKinds, 110),
				Method: http.MethodGet, Path: govPath + "/audit-events", Query: true, Unwrap: "items",
				Flags: []flagSpec{
					{Name: "kind", In: "query", Help: "event kinds, comma-separated (see the list above)", Values: auditKinds},
					{Name: "actor", In: "query", Help: "who made the change"},
					// Not --project: that is the project in context, which most events (a tag made, the policy changed) have none of.
					{Name: "project-id", In: "query", Key: "projectId", Help: "only the events of this project's tables (its id)"},
					{Name: "table", In: "query", Help: "a table's"},
					{Name: "column", In: "query", Help: "a column's"},
					{Name: "since", In: "query", Help: "ISO-8601 instant"},
					{Name: "before", In: "query", Help: "ISO-8601 instant"},
					{Name: "limit", In: "query", Kind: "int", Help: "records a page (at most 500)"},
					{Name: "cursor", In: "query", Help: "the page after: the cursor the page before names on stderr"},
				},
				Fields: auditFields},
			opSpec{Use: "export", Short: "Decision records and policy changes as NDJSON (-f since=… -f until=…)", Method: http.MethodGet, Path: govPath + "/audit-events/export", Query: true},
		),
		group("suggestions", "Suggested column tags: a member suggests, a governance administrator approves or rejects", []string{"suggestion", "suggest"},
			opSpec{Use: "list", Short: "Suggested tags, newest first (--status APPROVED|REJECTED|WITHDRAWN|ALL; pending by default)", Method: http.MethodGet,
				Path: govPath + "/tag-suggestions",
				Flags: []flagSpec{
					{Name: "status", In: "query", Help: "PENDING (default), APPROVED, REJECTED, WITHDRAWN or ALL"},
					{Name: "project", In: "query", Key: "projectId", Help: "only this project's (its id)"},
					{Name: "tag", In: "query", Help: "only this tag: name, key or id"},
					{Name: "source", In: "query", Key: "sourceId", Help: "only this classification source's (its id)"},
					{Name: "q", In: "query", Help: "text in the table's or column's name"},
					{Name: "order", In: "query", Help: "newest (default) or project"},
				},
				Unwrap: "items", Fields: []string{"id", "schema", "table", "column", "tag", "source", "status", "suggestedBy", "createdAt"}},
			opSpec{Use: "describe ID", Aliases: []string{"get"}, Short: "Show a suggestion", Method: http.MethodGet, Path: govPath + "/tag-suggestions/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}},
			opSpec{Use: "create", Short: "Suggest a tag for a column; it waits until a governance administrator approves it", Method: http.MethodPost,
				Path: govPath + "/tag-suggestions", Body: true,
				Long: "  peaka gov suggestions create --catalog-id 7 --schema public --table people --column ssn --tag PII --reason \"holds SSNs\"",
				Flags: []flagSpec{
					{Name: "catalog-id", Key: "catalogId", Help: "the catalog's id (1 for Peaka tables)", Required: true},
					{Name: "schema", Key: "schema", Help: "the schema", Required: true},
					{Name: "table", Key: "table", Help: "the table", Required: true},
					{Name: "column", Key: "column", Help: "the column", Required: true},
					{Name: "tag", Key: "tagId", Help: "the tag: name, key or id", Required: true},
					{Name: "reason", Help: "why; the administrator reads it"},
				},
				Prepare: chain(projectIntoBody, suggestedTag), Waits: true},
			opSpec{Use: "approve ID", Short: "Put the suggested tag on (202: waits for a second administrator)", Method: http.MethodPost,
				Path: govPath + "/tag-suggestions/{id}/approve", Body: true, Args: []argSpec{{Name: "ID", Param: "id"}},
				Flags: []flagSpec{{Name: "note", Key: "reason", Help: "kept with the decision"}}},
			opSpec{Use: "reject ID", Short: "Refuse a suggestion; it does not come back", Method: http.MethodPost,
				Path: govPath + "/tag-suggestions/{id}/reject", Body: true, Args: []argSpec{{Name: "ID", Param: "id"}},
				Flags: []flagSpec{{Name: "note", Key: "reason", Help: "kept with the decision"}}},
			opSpec{Use: "approve-many", Short: "Approve many: --id (repeatable), or every pending one matching the filter flags", Method: http.MethodPost,
				Path: govPath + "/tag-suggestions/approve", Body: true, Flags: manySuggestionFlags(), Prepare: manySuggestions,
				Unwrap: "outcomes", Fields: []string{"id", "outcome", "changeRequestId", "message"}},
			opSpec{Use: "reject-many", Short: "Reject many: --id (repeatable), or every pending one matching the filter flags", Method: http.MethodPost,
				Path: govPath + "/tag-suggestions/reject", Body: true, Flags: manySuggestionFlags(), Prepare: manySuggestions,
				Unwrap: "outcomes", Fields: []string{"id", "outcome", "message"}},
		),
		group("sources", "Classification sources: a catalog whose own system keeps column classes, mapped to tags", []string{"source", "classification-sources"},
			opSpec{Use: "list", Short: "List classification sources", Method: http.MethodGet, Path: govPath + "/classification-sources",
				Unwrap: "items", Fields: []string{"id", "kind", "projectId", "catalogId", "applyAs", "enabled", "lastStatus", "lastSyncAt"}},
			opSpec{Use: "describe ID", Aliases: []string{"get"}, Short: "Show a source and its class mappings", Method: http.MethodGet,
				Path: govPath + "/classification-sources/{id}", Args: []argSpec{{Name: "ID", Param: "id"}}},
			opSpec{Use: "create", Short: "Start reading a catalog's column classes", Method: http.MethodPost, Path: govPath + "/classification-sources", Body: true,
				Long: `  peaka gov sources create --kind SNOWFLAKE --catalog-id 7 --apply-as SUGGESTED --interval 1440
  peaka gov sources create --kind DETECTOR --catalog-id 7`,
				Flags: []flagSpec{
					{Name: "kind", Help: "SNOWFLAKE, OPENMETADATA, DATAPLEX or DETECTOR", Required: true},
					{Name: "catalog-id", Key: "catalogId", Help: "the catalog it reads (the project's own)", Required: true},
					{Name: "apply-as", Key: "applyAs", Help: "EXTERNAL (classes become tags) or SUGGESTED (they wait as suggestions)"},
					{Name: "interval", Key: "syncIntervalMinutes", Kind: "int", Help: "minutes between syncs; none: by hand"},
					{Name: "import-classes", Key: "importClasses", Kind: "bool", Help: "a class without a mapping becomes a tag of its own"},
					{Name: "enabled", Kind: "bool", Help: "syncs", Default: true},
				},
				Prepare: projectIntoBody},
			opSpec{Use: "update ID", Short: "Change a source; what is not given stays", Method: http.MethodPatch, Path: govPath + "/classification-sources/{id}",
				Body: true, Args: []argSpec{{Name: "ID", Param: "id"}},
				Flags: []flagSpec{
					{Name: "apply-as", Key: "applyAs", Help: "EXTERNAL or SUGGESTED"},
					{Name: "interval", Key: "syncIntervalMinutes", Kind: "int", Help: "minutes between syncs"},
					{Name: "import-classes", Key: "importClasses", Kind: "bool", Help: "a class without a mapping becomes a tag of its own"},
					{Name: "enabled", Kind: "bool", Help: "syncs"},
				}},
			opSpec{Use: "delete ID", Short: "Delete a source; the tags it put on stay", Method: http.MethodDelete, Path: govPath + "/classification-sources/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}, Confirm: "Delete classification source {0}?", Empty: "Deleted."},
			opSpec{Use: "sync ID", Short: "Read the source now", Method: http.MethodPost, Path: govPath + "/classification-sources/{id}/sync",
				Args: []argSpec{{Name: "ID", Param: "id"}}},
			opSpec{Use: "map ID", Short: "What a class of the source becomes: a tag, or nothing (--ignore)", Method: http.MethodPut,
				Path: govPath + "/classification-sources/{id}/mappings", Body: true, Args: []argSpec{{Name: "ID", Param: "id"}},
				Long: "  peaka gov sources map 3f2a… --class PII.EMAIL --tag Email",
				Flags: []flagSpec{
					{Name: "class", Key: "externalClass", Help: "the class as the source names it", Required: true},
					{Name: "tag", Key: "tagId", Help: "the tag it becomes: name, key or id"},
					{Name: "ignore", Key: "ignored", Kind: "bool", Help: "the class is not taken"},
				}},
		),
		group("patterns", "The column-name patterns Peaka's detector suggests classes by", []string{"detection-patterns"},
			opSpec{Use: "list", Short: "List detection patterns", Method: http.MethodGet, Path: govPath + "/detection-patterns",
				Unwrap: "items", Fields: []string{"id", "className", "columnPattern", "builtin", "enabled"}},
			opSpec{Use: "set", Short: "Add a pattern, or switch a class's pattern on or off", Method: http.MethodPut, Path: govPath + "/detection-patterns", Body: true,
				Long: "  peaka gov patterns set --class EMAIL --pattern '*e?mail*'",
				Flags: []flagSpec{
					{Name: "class", Key: "className", Help: "the class", Required: true},
					{Name: "pattern", Key: "columnPattern", Help: "column names: * any characters, ? one", Required: true},
					{Name: "enabled", Kind: "bool", Help: "suggests", Default: true},
				},
				Unwrap: "items", Fields: []string{"id", "className", "columnPattern", "builtin", "enabled"}},
			opSpec{Use: "delete ID", Short: "Delete one of the organization's own patterns", Method: http.MethodDelete, Path: govPath + "/detection-patterns/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}, Unwrap: "items", Fields: []string{"id", "className", "columnPattern", "builtin", "enabled"}},
		),
		group("declassifications", "Lifting an inherited tag from a product's column, while its SQL stays the same", []string{"declassify", "declassification"},
			opSpec{Use: "list", Short: "Active declassifications (-f active=false for every one)", Method: http.MethodGet, Path: govPath + "/declassifications",
				Query: true, Unwrap: "items", Fields: []string{"id", "objectKind", "objectId", "column", "tagId", "status", "expiresAt", "approvedBy"}},
			opSpec{Use: "create", Short: "Lift a tag from a product's column (202: waits for approval)", Method: http.MethodPost, Path: govPath + "/declassifications", Body: true,
				Long: `Name the product by --object-kind and --object-id, or by its table; --source narrows it to some source columns.

  peaka gov declassify create --object-kind MATERIALIZED_QUERY --object-id 1289… --column email_domain --tag PII --reason "domain only"`,
				Flags: []flagSpec{
					{Name: "object-kind", Key: "objectKind", Help: "MATERIALIZED_QUERY, SEMANTIC_TABLE, …"},
					{Name: "object-id", Key: "objectId", Help: "the product's id"},
					{Name: "catalog-id", Key: "catalogId", Help: "or the product's table: its catalog"},
					{Name: "schema", Key: "schema", Help: "the table's schema"},
					{Name: "table", Key: "table", Help: "the table"},
					{Name: "column", Key: "column", Help: "the product's column", Required: true},
					{Name: "tag", Key: "tagId", Help: "the inherited tag: name, key or id", Required: true},
					{Name: "reason", Help: "why", Required: true},
					{Name: "scope", Help: "what it is for"},
					{Name: "days", Key: "durationDays", Kind: "int", Help: "how many days it lasts"},
					{Name: "source", Key: "sourceRefs", Kind: "strings", Help: "a source column PROJECT:CATALOG_ID.SCHEMA.TABLE.COLUMN; repeatable"},
				},
				Prepare: chain(projectIntoBody, declassifySources)},
			opSpec{Use: "revoke ID", Short: "The tag applies again from the next read", Method: http.MethodDelete, Path: govPath + "/declassifications/{id}",
				Args: []argSpec{{Name: "ID", Param: "id"}}, Confirm: "Revoke declassification {0}?"},
		),
		group("access", "Who reads what: a column's readers, a member's view, exposure, recent readers", nil,
			opSpec{Use: "column", Short: "Every member and what each sees of a column", Method: http.MethodGet, Path: govPath + "/access/columns",
				Long:    "  peaka gov access column --catalog-id 7 --schema public --table people --column ssn",
				Flags:   append(columnQueryFlags(), flagSpec{Name: "limit", In: "query", Kind: "int", Help: "members a page"}),
				Prepare: projectIntoQuery, Unwrap: "members", Fields: []string{"member.email", "effect", "code", "rule"}},
			opSpec{Use: "holders", Short: "The rules that unmask a column and whom each names", Method: http.MethodGet, Path: govPath + "/access/columns/holders",
				Flags: columnQueryFlags(), Prepare: projectIntoQuery, Unwrap: "holders", Fields: []string{"kind", "name", "tag", "policy", "mask", "members"}},
			opSpec{Use: "member MEMBER", Short: "What a member sees of every classified column (MEMBER: id or email)", Method: http.MethodGet, Path: govPath + "/access/members/{user}",
				Args: []argSpec{{Name: "USER_ID", Resolve: resolveMember}}, Unwrap: "rows", Fields: []string{"table.sqlName", "column", "effect", "rule"}},
			opSpec{Use: "exposure BY", Short: "How far classified data reaches: BY is tags, columns or members", Method: http.MethodGet, Path: govPath + "/access/exposure/{by}",
				Args:   []argSpec{{Name: "BY", Param: "by"}},
				Flags:  []flagSpec{{Name: "q", In: "query", Help: "search"}, {Name: "tag", In: "query", Help: "columns of this tag (name, key or id)"}},
				Unwrap: "items"},
			opSpec{Use: "readers", Short: "Who read a tag's columns lately, and how", Method: http.MethodGet, Path: govPath + "/access/readers",
				Flags:  []flagSpec{{Name: "tag", In: "query", Help: "the tag: name, key or id", Required: true}, {Name: "days", In: "query", Kind: "int", Help: "how far back (90; at most 365)"}},
				Unwrap: "items"},
		),
		opSpec{Use: "decisions", Short: "What was decided on each read, newest first", Method: http.MethodGet, Path: govPath + "/decisions",
			Long: `  peaka gov decisions --user 2b19… --denied
  peaka gov decisions --catalog-id 7 --schema payment --table charges --since 2026-11-01T00:00:00Z`,
			Flags: []flagSpec{
				{Name: "user", In: "query", Key: "userId", Help: "a member's id"},
				{Name: "query", In: "query", Key: "queryId", Help: "a query's id"},
				{Name: "catalog-id", In: "query", Key: "catalogId", Help: "the table's catalog"},
				{Name: "schema", In: "query", Help: "the table's schema"},
				{Name: "table", In: "query", Help: "the table"},
				{Name: "denied", In: "query", Kind: "bool", Help: "only refused reads"},
				{Name: "tag", In: "query", Help: "reads deciding this tag: name, key or id"},
				{Name: "since", In: "query", Help: "ISO-8601 instant"},
				{Name: "limit", In: "query", Kind: "int", Help: "records a page (at most 500)"},
				{Name: "cursor", In: "query", Help: "the page after: the cursor the page before names on stderr"},
			},
			Unwrap: "items", Fields: []string{"createdAt", "userId", "sqlName", "kind", "denied", "policyVersion"}}.command(),
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
		newAttributesCmd(),
	)
	// `peaka gov policy` alone shows the policy, as it did before it had subcommands.
	if policy, _, err := cmd.Find([]string{"policy"}); err == nil && policy.Name() == "policy" {
		if show, _, err := policy.Find([]string{"show"}); err == nil && show.Name() == "show" {
			policy.RunE = show.RunE
			policy.Args = cobra.NoArgs
		}
	}
	return cmd
}

// resolvePublicationQuery names a saved query of the project in context: its id, name and kind.
func resolvePublicationQuery(r *Runtime, ref string, params map[string]string) error {
	project, err := r.needProject()
	if err != nil {
		return err
	}
	qs, err := r.queriesOf(project)
	if err != nil {
		return err
	}
	q, err := matchOne(qs, ref, "query")
	if err != nil {
		return err
	}
	params["query"], params["queryName"], params["queryKind"] = str(q["id"]), str(q["displayName"]), "SAVED_QUERY"
	if strings.EqualFold(str(q["queryType"]), "MATERIALIZED") {
		params["queryKind"] = "MATERIALIZED_QUERY"
	}
	return nil
}

// publicationRequest: the request names the query by its project, kind, id and name.
func publicationRequest(c *callCtx) error {
	project, err := rt.sqlProjectID()
	if err != nil {
		return err
	}
	c.Body["projectId"], c.Body["objectKind"], c.Body["objectId"], c.Body["objectName"] = project, c.Params["queryKind"], c.Params["query"], c.Params["queryName"]
	return nil
}

func newGroupsCmd() *cobra.Command {
	return group("groups", "The organization's groups and their members", []string{"group"},
		opSpec{Use: "list", Short: "List groups", Method: http.MethodGet, Path: "/organizations/{org}/groups", Unwrap: "items", Fields: []string{"id", "name", "sqlName", "memberCount", "directoryManaged"}},
		opSpec{Use: "describe GROUP", Aliases: []string{"get"}, Short: "Show a group", Method: http.MethodGet, Path: "/organizations/{org}/groups/{group}",
			Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}}},
		opSpec{Use: "create", Short: "Create a group", Method: http.MethodPost, Path: "/organizations/{org}/groups", Body: true,
			Flags: []flagSpec{{Name: "name", Help: "the group's name", Required: true}, {Name: "description", Help: "who is in it"}}},
		opSpec{Use: "update GROUP", Short: "Change a group's name or description; what is not given stays (a directory group changes in its directory)",
			Method: http.MethodPatch, Path: "/organizations/{org}/groups/{group}", Body: true, Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}},
			Flags: []flagSpec{{Name: "name", Help: "the group's name"}, {Name: "description", Help: "who is in it"}}},
		opSpec{Use: "delete GROUP", Short: "Delete a group", Method: http.MethodDelete, Path: "/organizations/{org}/groups/{group}",
			Args: []argSpec{{Name: "GROUP", Resolve: resolveGroup}}, Confirm: "Delete group {0}?", Empty: "Deleted."},
		opSpec{Use: "set-sql-name GROUP SQL_NAME", Short: "Set how SQL names the group (current_user_groups(), a row rule's exception)", Method: http.MethodPut,
			Path: "/organizations/{org}/groups/{group}/sql-name", Body: true,
			Args:    []argSpec{{Name: "GROUP", Resolve: resolveGroup}, {Name: "SQL_NAME", Param: "sqlName"}},
			Prepare: paramsIntoBody("sqlName", "sqlName"), Confirm: "Rename group {0} in SQL to {1}? Row rules and saved queries that quote the old name are not rewritten."},
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
		opSpec{Use: "update GRANT_ID", Short: "Give a grant another role", Method: http.MethodPatch, Path: path + "/{grant}", Body: true,
			Long:    "  peaka grants update 3f2c… --role 1a6f5d6c-…\n  peaka grants update 3f2c… --workspace Lab --role …",
			Args:    []argSpec{{Name: "GRANT_ID", Param: "grant"}},
			Flags:   append(scope, flagSpec{Name: "role", Key: "roleId", Help: "the role's id", Required: true}),
			Prepare: grantPath},
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
	// An id is taken as it is: the API says when no tag has it, without the organization's tags read first.
	if uuidRE.MatchString(strings.TrimSpace(ref)) {
		params["tag"] = strings.TrimSpace(ref)
		return nil
	}
	org, err := r.orgID()
	if err != nil {
		return err
	}
	// The project in context is named: one who only suggests tags (a member who manages its tables) reads them by it.
	path := fmt.Sprintf("/organizations/%s/governance/tags", org)
	if r.Settings != nil && r.Settings.Project != "" {
		path += "?" + url.Values{"projectId": {r.Settings.Project}}.Encode()
	}
	tags, err := listAt(r, path)
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

// tableColumnTags: --tag-column COLUMN=TAG pairs (tags by name, key or id) as the body's columns; --none for none.
func tableColumnTags(c *callCtx) error {
	pairs, _ := c.Body["tagColumns"].([]string)
	none, _ := c.Body["none"].(bool)
	delete(c.Body, "tagColumns")
	delete(c.Body, "none")
	if none && len(pairs) > 0 {
		return usage("--none takes every tag off; it does not go with --tag-column")
	}
	if !none && len(pairs) == 0 {
		return usage("give the columns' tags with --tag-column COLUMN=TAG, or --none to take them all off")
	}
	columns := []map[string]any{}
	for _, p := range pairs {
		column, tag, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(column) == "" || strings.TrimSpace(tag) == "" {
			return usage("--tag-column is COLUMN=TAG, not %q", p)
		}
		found := map[string]string{}
		if err := resolveTag(rt, strings.TrimSpace(tag), found); err != nil {
			return err
		}
		columns = append(columns, map[string]any{"column": strings.TrimSpace(column), "tagId": found["tag"]})
	}
	c.Body["columns"] = columns
	return nil
}

// rowRuleExceptions: --except WHO:ID pairs as the rule's exceptions; --no-exceptions for none; neither keeps them.
func rowRuleExceptions(c *callCtx) error {
	asked, _ := c.Body["except"].([]string)
	none, _ := c.Body["noExceptions"].(bool)
	delete(c.Body, "except")
	delete(c.Body, "noExceptions")
	if none && len(asked) > 0 {
		return usage("--no-exceptions takes them all off; it does not go with --except")
	}
	if none {
		c.Body["exceptions"] = []any{}
		return nil
	}
	if len(asked) == 0 {
		return nil
	}
	out := []any{}
	for _, e := range asked {
		kind, id, ok := strings.Cut(e, ":")
		kind = strings.ToLower(strings.TrimSpace(kind))
		if !ok || strings.TrimSpace(id) == "" || (kind != "user" && kind != "group" && kind != "role") {
			return usage("--except is user:ID, group:SQL_NAME|ID or role:SQL_NAME, not %q", e)
		}
		out = append(out, map[string]any{"subject": map[string]any{"type": kind, "id": strings.TrimSpace(id)}})
	}
	c.Body["exceptions"] = out
	return nil
}

func manySuggestionFlags() []flagSpec {
	return []flagSpec{
		{Name: "id", Key: "ids", Kind: "strings", Help: "a suggestion's id; repeatable (up to 500)"},
		{Name: "project", Key: "fProject", Help: "every pending one of this project (its id)"},
		{Name: "tag", Key: "fTag", Help: "every pending one of this tag: name, key or id"},
		{Name: "source", Key: "fSource", Help: "every pending one of this classification source (its id)"},
		{Name: "q", Key: "fQ", Help: "every pending one whose table or column name holds this"},
		{Name: "limit", Key: "limit", Kind: "int", Help: "how many a filter decides in one call (default 100, at most 500)"},
		{Name: "note", Help: "kept with each decision"},
	}
}

// manySuggestions: ids, or a filter, as the body of a decision on many suggestions.
func manySuggestions(c *callCtx) error {
	filter := map[string]any{}
	for flag, key := range map[string]string{"fProject": "projectId", "fTag": "tagId", "fSource": "sourceId", "fQ": "q"} {
		if v, ok := c.Body[flag].(string); ok && v != "" {
			filter[key] = v
		}
		delete(c.Body, flag)
	}
	ids, _ := c.Body["ids"].([]string)
	switch {
	case len(ids) > 0 && len(filter) > 0:
		return usage("name the suggestions by --id or by filter flags, not both")
	case len(ids) == 0 && len(filter) == 0:
		return usage("name the suggestions: --id, or --project / --tag / --source / --q")
	case len(filter) > 0:
		delete(c.Body, "ids")
		c.Body["filter"] = filter
	}
	if v, ok := c.Body["limit"].(int); ok && v == 0 {
		delete(c.Body, "limit")
	}
	return nil
}

// policyFields: --field NAME=VALUE pairs as the body's fields (true/false and numbers as such).
func policyFields(c *callCtx) error {
	pairs, _ := c.Body["fieldPairs"].([]string)
	delete(c.Body, "fieldPairs")
	fields := map[string]any{}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return usage("--change is NAME=VALUE, not %q", p)
		}
		var parsed any
		if err := json.Unmarshal([]byte(v), &parsed); err == nil {
			fields[strings.TrimSpace(k)] = parsed
		} else {
			fields[strings.TrimSpace(k)] = v
		}
	}
	if len(fields) == 0 {
		return usage("name a field to change: --change NAME=VALUE")
	}
	c.Body["fields"] = fields
	return nil
}

func columnQueryFlags() []flagSpec {
	return []flagSpec{
		{Name: "catalog-id", In: "query", Key: "catalogId", Help: "the table's catalog", Required: true},
		{Name: "schema", In: "query", Help: "the table's schema", Required: true},
		{Name: "table", In: "query", Help: "the table", Required: true},
		{Name: "column", In: "query", Help: "the column", Required: true},
	}
}

// declassifySources: --source PROJECT:CATALOG_ID.SCHEMA.TABLE.COLUMN as the body's sources.
func declassifySources(c *callCtx) error {
	refs, _ := c.Body["sourceRefs"].([]string)
	delete(c.Body, "sourceRefs")
	if len(refs) == 0 {
		return nil
	}
	out := []any{}
	for _, r := range refs {
		project, rest, ok := strings.Cut(r, ":")
		parts := strings.Split(rest, ".")
		if !ok || project == "" || len(parts) != 4 {
			return usage("--source is PROJECT:CATALOG_ID.SCHEMA.TABLE.COLUMN, not %q", r)
		}
		out = append(out, map[string]any{"projectId": project, "catalogId": parts[0], "schema": parts[1], "table": parts[2], "column": parts[3]})
	}
	c.Body["sources"] = out
	return nil
}

// suggestedTag: the suggested tag by name, key or id, looked up here: one who only suggests reads the tags a page
// at a time, which is what the lookup does.
func suggestedTag(c *callCtx) error {
	ref, _ := c.Body["tagId"].(string)
	if strings.TrimSpace(ref) == "" {
		return nil
	}
	found := map[string]string{}
	if err := resolveTag(rt, ref, found); err != nil {
		return err
	}
	c.Body["tagId"] = found["tag"]
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

// govFile is what `governance apply` reads: what an organization's governance should hold.
type govFile struct {
	Tags []struct {
		Name        string   `yaml:"name"`
		Label       string   `yaml:"label"` // the older word for name
		Description *string  `yaml:"description"`
		Active      *bool    `yaml:"active"`
		Key         string   `yaml:"key"`
		Parent      *string  `yaml:"parent"`
		Sensitivity *string  `yaml:"sensitivity"`
		Status      string   `yaml:"status"`
		Rules       []string `yaml:"rules"` // refused: masking is a masking policy now
	} `yaml:"tags"`
	Masks []struct {
		Name        string  `yaml:"name"`
		Expression  string  `yaml:"expression"`
		Description *string `yaml:"description"`
	} `yaml:"masks"`
	MaskingPolicies []struct {
		Name       string    `yaml:"name"`
		Tag        string    `yaml:"tag"`
		Default    *string   `yaml:"default"`
		Enabled    *bool     `yaml:"enabled"`
		Exceptions *[]string `yaml:"exceptions"`
	} `yaml:"maskingPolicies"`
	TableTags []struct {
		CatalogID   string `yaml:"catalogId"`
		CatalogName string `yaml:"catalogName"`
		Catalog     string `yaml:"catalog"`
		Schema      string `yaml:"schema"`
		Table       string `yaml:"table"`
		Tag         string `yaml:"tag"`
		Reason      string `yaml:"reason"`
	} `yaml:"tableTags"`
	RowRules []struct {
		Name       string    `yaml:"name"`
		Table      string    `yaml:"table"`
		Tag        string    `yaml:"tag"`
		Expression string    `yaml:"expression"`
		Enabled    *bool     `yaml:"enabled"`
		Except     *[]string `yaml:"except"`
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
		Short: "Make the organization's tags, masks, masking policies, table and column tags and row rules what a YAML file says",
		Long: `Creates what is missing and changes what differs, by name; what the file does not say stays as it is (a tag
without a description keeps its own), what is already as the file says is left alone, and nothing is deleted.
Tags come first (a parent before its children), then masks, masking policies, table tags, column tags and row rules.
A change that waits for a second administrator (202) is reported, not retried.

  tags:
    - {name: Personal}
    - {name: PHI, parent: Personal, sensitivity: RESTRICTED, description: Protected health information}
  masks:
    - {name: Last four, expression: "concat('***', substr({col}, -4))"}
  maskingPolicies:
    - {name: PHI readers, tag: PHI, default: nullify, exceptions: ["group:Clinicians=unmasked"]}
  tableTags:
    - {catalogId: "7", schema: hr, tag: Personal}
  columnTags:
    - {catalogId: "1", catalog: peaka, schema: table, table: patients, column: ssn, tag: PHI}
  rowRules:
    - {name: Own region, tag: REGION, expression: "{col} = current_user_props('region')", except: ["group:auditors"]}

  peaka gov apply -f governance.yaml [--dry-run]`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return applyGovernance(file) },
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "the YAML file (- for stdin)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

// newID stands for a record a dry run would have made: what names it later in the file is shown, not refused.
const newID = "(new) "

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
	same := func(kind, name string) {
		report = append(report, map[string]any{"kind": kind, "name": name, "action": "none", "result": "unchanged"})
	}
	// The id of what a create made; under --dry-run a stand-in, so that what names it later is still shown.
	made := func(resp *api.Response, name string) string {
		if resp != nil && (resp.Status == http.StatusCreated || resp.Status == http.StatusOK) {
			var r record
			if json.Unmarshal(resp.Body, &r) == nil {
				return str(r["id"])
			}
		}
		if gf.dryRun {
			return newID + name
		}
		return ""
	}

	// What the file names, found before anything is sent: a name that is two records is refused, as the change would
	// go to one of them by chance (R5-6).
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
	tagByID := map[string]record{}
	for _, t := range existing {
		tagByID[str(t["id"])] = t
		if k := strings.ToUpper(str(t["key"])); k != "" {
			byLabel[k] = str(t["id"])
		}
	}
	for k, ids := range tagsByLabel {
		byLabel[k] = ids[0]
	}
	tagOf := func(ref string) (string, error) {
		if id, ok := byLabel[strings.ToUpper(strings.TrimSpace(ref))]; ok {
			return id, nil
		}
		if uuidRE.MatchString(strings.TrimSpace(ref)) {
			return strings.TrimSpace(ref), nil
		}
		return "", fmt.Errorf("no tag %q", ref)
	}

	// Tags: only what the file says and what differs is sent; a tag as the file says is left alone.
	for _, t := range want.Tags {
		if len(t.Rules) > 0 {
			note("tag", t.Name, "resolve", nil, fmt.Errorf("a tag carries no masking rules; set them under maskingPolicies"))
			continue
		}
		parent := ""
		if t.Parent != nil && !strings.EqualFold(strings.TrimSpace(*t.Parent), "none") && strings.TrimSpace(*t.Parent) != "" {
			if parent, err = tagOf(*t.Parent); err != nil {
				note("tag", t.Name, "resolve", nil, fmt.Errorf("its parent: %v (a parent comes before its children in the file)", err))
				continue
			}
		}
		id, there := byLabel[strings.ToUpper(t.Name)]
		if !there {
			body := map[string]any{"name": t.Name, "active": t.Active == nil || *t.Active}
			if t.Description != nil {
				body["description"] = *t.Description
			}
			for k, v := range map[string]string{"key": t.Key, "parentId": parent, "status": t.Status} {
				if v != "" {
					body[k] = v
				}
			}
			if t.Sensitivity != nil && !strings.EqualFold(*t.Sensitivity, "none") && *t.Sensitivity != "" {
				body["sensitivity"] = *t.Sensitivity
			}
			resp, err := send(client, http.MethodPost, base+"/tags", body)
			note("tag", t.Name, "create", resp, err)
			if id := made(resp, t.Name); err == nil && id != "" {
				byLabel[strings.ToUpper(t.Name)] = id
			}
			continue
		}
		now := tagByID[id]
		if t.Key != "" && !strings.EqualFold(t.Key, str(now["key"])) {
			note("tag", t.Name, "update", nil, fmt.Errorf("its key is %s and never changes", str(now["key"])))
			continue
		}
		patch := map[string]any{}
		if t.Name != str(now["name"]) {
			patch["name"] = t.Name
		}
		if t.Description != nil && *t.Description != str(now["description"]) {
			patch["description"] = *t.Description
		}
		if a, _ := now["active"].(bool); t.Active != nil && *t.Active != a {
			patch["active"] = *t.Active
		}
		if t.Parent != nil && parent != str(now["parentId"]) {
			patch["parentId"] = parent
		}
		if t.Sensitivity != nil {
			s := *t.Sensitivity
			if strings.EqualFold(s, "none") {
				s = ""
			}
			if !strings.EqualFold(s, str(now["sensitivity"])) {
				patch["sensitivity"] = s
			}
		}
		if t.Status != "" && !strings.EqualFold(t.Status, str(now["status"])) {
			patch["status"] = t.Status
		}
		if len(patch) == 0 {
			same("tag", t.Name)
			continue
		}
		resp, err := send(client, http.MethodPatch, base+"/tags/"+id, patch)
		note("tag", t.Name, "update", resp, err)
	}

	// Masks, by name; a predefined mask is not the file's to change.
	var masks []record
	if len(want.Masks) > 0 || len(want.MaskingPolicies) > 0 {
		if masks, err = masksForMatch(rt); err != nil {
			return err
		}
	}
	for _, m := range want.Masks {
		var now record
		for _, x := range masks {
			if strings.EqualFold(str(x["name"]), m.Name) {
				now = x
			}
		}
		if now == nil {
			body := map[string]any{"name": m.Name, "expression": m.Expression}
			if m.Description != nil {
				body["description"] = *m.Description
			}
			resp, err := send(client, http.MethodPost, base+"/masks", body)
			note("mask", m.Name, "create", resp, err)
			if id := made(resp, m.Name); err == nil && id != "" {
				masks = append(masks, record{"id": id, "name": m.Name})
			}
			continue
		}
		if p, _ := now["predefined"].(bool); p {
			note("mask", m.Name, "update", nil, fmt.Errorf("a predefined mask does not change; give the organization's mask another name"))
			continue
		}
		patch := map[string]any{}
		if m.Expression != "" && m.Expression != str(now["expression"]) {
			patch["expression"] = m.Expression
		}
		if m.Description != nil && *m.Description != str(now["description"]) {
			patch["description"] = *m.Description
		}
		if len(patch) == 0 {
			same("mask", m.Name)
			continue
		}
		resp, err := send(client, http.MethodPatch, base+"/masks/"+str(now["id"]), patch)
		note("mask", m.Name, "update", resp, err)
	}

	// Masking policies, by name.
	if len(want.MaskingPolicies) > 0 {
		policies, err := listAt(rt, base+"/masking-policies")
		if err != nil {
			return err
		}
		maskOf := func(ref string) (string, error) {
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
		for _, mp := range want.MaskingPolicies {
			name := mp.Name
			if name == "" {
				name = mp.Tag
			}
			tagID, err := tagOf(mp.Tag)
			if err != nil {
				note("masking policy", name, "resolve", nil, err)
				continue
			}
			body := map[string]any{}
			if mp.Default != nil {
				body["defaultMaskId"] = *mp.Default
			}
			if mp.Exceptions != nil {
				xs := []any{}
				for _, x := range *mp.Exceptions {
					xs = append(xs, x)
				}
				body["exceptions"] = xs
			}
			if err := shapePolicy(body, maskOf, groupOf, tagOf); err != nil {
				note("masking policy", name, "resolve", nil, err)
				continue
			}
			var now record
			for _, x := range policies {
				if strings.EqualFold(str(x["name"]), name) {
					now = x
				}
			}
			if now == nil {
				body["name"], body["tagId"], body["enabled"] = name, tagID, mp.Enabled == nil || *mp.Enabled
				resp, err := send(client, http.MethodPost, base+"/masking-policies", body)
				note("masking policy", name, "create", resp, err)
				continue
			}
			if str(now["tagId"]) != tagID {
				note("masking policy", name, "update", nil, fmt.Errorf("the policy is on another tag; give it another name"))
				continue
			}
			if d, sent := body["defaultMaskId"]; sent && str(d) == str(now["defaultMaskId"]) {
				delete(body, "defaultMaskId")
			}
			if xs, sent := body["exceptions"]; sent && sameJSON(xs, now["exceptions"]) {
				delete(body, "exceptions")
			}
			if e, _ := now["enabled"].(bool); mp.Enabled != nil && *mp.Enabled != e {
				body["enabled"] = *mp.Enabled
			}
			if len(body) == 0 {
				same("masking policy", name)
				continue
			}
			if v := str(now["version"]); v != "" {
				body["version"] = v
			}
			resp, err := send(client, http.MethodPatch, base+"/masking-policies/"+str(now["id"]), body)
			note("masking policy", name, "update", resp, err)
		}
	}

	project := ""
	if len(want.ColumnTags) > 0 || len(want.RowRules) > 0 || len(want.TableTags) > 0 {
		project, _ = rt.needProject()
	}

	// Table and schema tags: put on; one already there stays.
	for _, tt := range want.TableTags {
		name := strings.TrimSuffix(tt.Schema+"."+tt.Table, ".") + " " + tt.Tag
		id, err := tagOf(tt.Tag)
		if err != nil || project == "" {
			note("table tag", name, "add", nil, fmt.Errorf("no tag %q or no project in context", tt.Tag))
			continue
		}
		q := url.Values{"projectId": {project}, "catalogId": {tt.CatalogID}, "schema": {tt.Schema}}
		if tt.Table != "" {
			q.Set("table", tt.Table)
		}
		there, err := listAt(rt, base+"/table-tags?"+q.Encode())
		if err != nil {
			note("table tag", name, "add", nil, err)
			continue
		}
		on := false
		for _, x := range there {
			if str(x["tagId"]) == id && strings.EqualFold(str(x["table"]), tt.Table) {
				on = true
			}
		}
		if on {
			same("table tag", name)
			continue
		}
		body := map[string]any{"projectId": project, "catalogId": tt.CatalogID, "catalog": firstOf(tt.Catalog, tt.CatalogName), "schema": tt.Schema,
			"tagId": id, "reason": tt.Reason}
		if tt.Table != "" {
			body["table"] = tt.Table
		}
		resp, err := send(client, http.MethodPost, base+"/table-tags", body)
		note("table tag", name, "add", resp, err)
	}

	// Column tags: put on; one already on the column itself stays (an inherited one is not the column's own).
	for _, ct := range want.ColumnTags {
		name := ct.Schema + "." + ct.Table + "." + ct.Column + " " + ct.Tag
		id, err := tagOf(ct.Tag)
		if err != nil || project == "" {
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
			if strings.EqualFold(str(x["column"]), ct.Column) && str(x["tagId"]) == id && !strings.EqualFold(str(x["origin"]), "INHERITED") {
				tagged = true
			}
		}
		if tagged {
			same("column tag", name)
			continue
		}
		resp, err := send(client, http.MethodPost, base+"/column-tags", map[string]any{"projectId": project, "catalogId": ct.CatalogID,
			"catalog": firstOf(ct.Catalog, ct.CatalogName), "schema": ct.Schema, "table": ct.Table, "column": ct.Column, "tagId": id, "reason": ct.Reason})
		note("column tag", name, "add", resp, err)
	}

	// Row rules, by name.
	ruleByID := map[string]record{}
	for _, r := range rules {
		ruleByID[str(r["id"])] = r
	}
	for _, rr := range want.RowRules {
		tagID := ""
		if rr.Tag != "" {
			if tagID, err = tagOf(rr.Tag); err != nil {
				note("row rule", rr.Name, "resolve", nil, err)
				continue
			}
		}
		var exceptions map[string]any
		if rr.Except != nil {
			c := &callCtx{Body: map[string]any{"except": *rr.Except, "noExceptions": len(*rr.Except) == 0}}
			if err := rowRuleExceptions(c); err != nil {
				note("row rule", rr.Name, "resolve", nil, err)
				continue
			}
			exceptions = c.Body
		}
		if ids, ok := rulesByName[strings.ToLower(rr.Name)]; ok {
			now := ruleByID[ids[0]]
			// A rule's target never changes (a new target is a new rule): one named for another target is refused,
			// not reported done (R5-7).
			if why := otherTarget(now, rr.Table, tagID, rr.Tag, project); why != "" {
				note("row rule", rr.Name, "update", nil, fmt.Errorf("%s; give the rule another name, or change it in Peaka", why))
				continue
			}
			patch := map[string]any{}
			if rr.Expression != "" && rr.Expression != str(now["expression"]) {
				patch["expression"] = rr.Expression
			}
			if e, _ := now["enabled"].(bool); rr.Enabled != nil && *rr.Enabled != e {
				patch["enabled"] = *rr.Enabled
			}
			if exceptions != nil && !sameSubjects(exceptions["exceptions"], now["exceptions"]) {
				patch["exceptions"] = exceptions["exceptions"]
			}
			if len(patch) == 0 {
				same("row rule", rr.Name)
				continue
			}
			resp, err := send(client, http.MethodPatch, base+"/row-rules/"+ids[0], patch)
			note("row rule", rr.Name, "update", resp, err)
			continue
		}
		body := map[string]any{"name": rr.Name, "expression": rr.Expression, "enabled": rr.Enabled == nil || *rr.Enabled}
		if exceptions != nil {
			body["exceptions"] = exceptions["exceptions"]
		}
		switch {
		case rr.Tag != "":
			body["target"] = map[string]any{"type": "tag", "tagId": tagID}
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
	for _, r := range report {
		if str(r["result"]) == "waits for approval" {
			return api.Exit(api.ExitPending, "some of the file waits for approval (peaka gov changes list)")
		}
	}
	return nil
}

func firstOf(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// sameJSON: two values that say the same once written as JSON (keys in order, absent and null alike left out).
func sameJSON(a, b any) bool {
	norm := func(v any) string {
		raw, _ := json.Marshal(v)
		var back any
		_ = json.Unmarshal(raw, &back)
		out, _ := json.Marshal(dropNulls(back))
		return string(out)
	}
	return norm(a) == norm(b)
}

func dropNulls(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if e != nil {
				out[k] = dropNulls(e)
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			out = append(out, dropNulls(e))
		}
		return out
	}
	return v
}

// sameSubjects: the same exceptions whatever their order, a subject's type whatever its case.
func sameSubjects(a, b any) bool {
	set := func(v any) map[string]bool {
		raw, _ := json.Marshal(v)
		var list []struct {
			Subject struct{ Type, ID string } `json:"subject"`
		}
		_ = json.Unmarshal(raw, &list)
		out := map[string]bool{}
		for _, e := range list {
			out[strings.ToLower(e.Subject.Type)+":"+strings.ToLower(e.Subject.ID)] = true
		}
		return out
	}
	x, y := set(a), set(b)
	if len(x) != len(y) {
		return false
	}
	for k := range x {
		if !y[k] {
			return false
		}
	}
	return true
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
