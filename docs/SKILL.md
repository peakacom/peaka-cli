---
name: peaka
description: Work with Peaka (data connections, catalogs, SQL over many sources, saved queries, caches, exports, AI metadata) from the terminal through the `peaka` CLI. Use when a task mentions Peaka, a Peaka project, or querying data that lives in Peaka.
---

# Peaka CLI

`peaka` talks to Peaka's public API and SQL endpoint. Grammar: `peaka <resource> <verb> [args] [flags]`.
Before anything else run `peaka help --agent` (two-kilobyte guide) and, for a specific command, `peaka <resource> <verb> --help` or `peaka help --format json`.

## Identity and context
- Identity: `PEAKA_API_KEY=<key>` in the environment, or a stored account (`peaka auth login`). `peaka me` shows who you are and the org/project in context.
- Context: `--project <id>` / `PEAKA_PROJECT`, `--org <id>` / `PEAKA_ORG`, or `peaka projects use <name>`. Most commands need a project.
- Environment: `--environment test|eu|prod` or a configuration (`peaka config set environment test`). A 401 names the API address it hit.

## Machine contract
- stdout is data; stderr is for people. Piped output is JSON (`--format jsonl|csv|yaml` to change; `--fields a,b`; `--jq EXPR`).
- No prompts when piped (`--no-input`); destructive verbs need `--yes`.
- Errors are problem+json on stderr, one line. Branch on `code`: `NOT_FOUND`, `VALIDATION_FAILED` (with `errors[].pointer`), `PERMISSION_REQUIRED`, `CONFLICT`, `QUERY_FAILED` (with `params.errorName`, `line`, `column`), `RATE_LIMITED`/`UPSTREAM_UNAVAILABLE` (retryable).
- Exit codes: 0 ok · 1 unexpected · 2 usage · 3 identity · 4 not found · 5 permission · 6 invalid · 7 conflict · 8 query/job failed (or `peaka sql --fail-on-warning` after a warning) · 9 retryable · 10 accepted but waits for approval (not applied) · 130 interrupted.
- Governance conflicts (exit 7), by `code`: `PUBLICATION_LAPSED` (a refresh of a query that runs with its publisher's access; `params.requires` says what is missing: `GOVERNANCE_APPROVAL`, `AUTHOR_PERMISSION`, `NOT_A_MEMBER`, `NO_PROJECT_ACCESS`, `SUSPENDED`, `SOURCE_IMPERSONATION`); `MEMBERSHIP_DISCLOSURE` (a save whose rows would reveal who is in a sensitive group: `params.items`, `params.fingerprint`). Never accept a disclosure for the person: report the items and fingerprint and let the publisher decide; they accept with `--accept-disclosure <fingerprint>` on the same save (a create also `--draft <draftId>`) or `peaka gov disclosures accept`, never by `--yes`. A create refused with `params.field=membershipDraftId`: with `params.objectId` it was already made (read it, do not create again); with a `DRAFT_*` reason save again without `--draft`. `CONFLICT` with `params.reason=REFRESH_NOT_STOPPED`: the running refresh could not be confirmed stopped; check `peaka queries status` before retrying. `REFRESH_ALREADY_RUNNING`: wait for it to end, then retry.
- `--dry-run` prints the request a writing command would send instead of sending it. `--if-not-exists` makes a create succeed when the thing exists.

## Answer a data question
```
peaka metadata table-list                                          # every table, one line each, by catalog.schema
peaka metadata describe --format-out compact \
  --table <catalog>.<schema>.<table> --table …                     # only the chosen tables (up to 20): columns, keys, joins
peaka sql "SELECT … FROM <catalog>.<schema>.<table> LIMIT 100" --format jsonl
```
1. Read the table list first and pick the tables; a schema header says whether its tables are read live (a SaaS table read live may return only its first page of records unless marked `[cached]`).
2. Read only the chosen tables with `--format-out compact` (`ddl` gives CREATE TABLE text; `--samples` adds sample values). Avoid a bare `metadata describe` on a large project.
3. Write the SQL. When the list says `too_large`, narrow it with `--catalog`/`--schema`, or rank tables with `peaka metadata relevant-tables "<question>"`.
Exploring by hand: `peaka tables list <catalog>.<schema>`, `peaka tables describe <catalog>.<schema>.<table>`, `peaka catalogs search <term>`.
SQL is Trino. Qualify tables fully; quote reserved words (`peaka."table".customers`). Add `LIMIT` while exploring; `--max-rows N` caps output and cancels the query server-side; `--query-timeout 60s` bounds it. Read `peaka help --agent` for the SQL rules, and never issue DML through SQL (Peaka tables change through `tables import` and `tables add-column`).

## Common tasks
- Save and reuse SQL: `peaka queries create --name x --sql-file q.sql --path /folder`; `peaka queries execute x`; `peaka queries export x --format-out PARQUET --out ./data`.
- Export a table: `peaka tables export c.s.t --limit 100000 --out ./data`.
- Cache a slow external table: `peaka tables cached c.s.t` (is it cacheable?), then `peaka cache create --table c.s.t --full-refresh '0 3 * * *'`; `peaka cache status c.s.t`.
- AI metadata for better text-to-SQL: `peaka metadata refresh --catalog <c> --wait`; `peaka metadata describe --catalog <c>`; `peaka metadata golden-sqls add --prompt "…" --sql "…"`.
- Catalog and schema descriptions: `peaka metadata descriptions list`; `peaka metadata descriptions set catalog <c> "…" --version <v>` (or `set schema <c>.<s>`); a stale `--version` is exit 7 CONFLICT with the current text and version, an empty description removes it.
- Connect a source: `peaka connections types describe <type>` lists the credential fields; `peaka connections create --type <type> --name <n> -c key=value …`; OAuth types with `--no-input` print an authorization URL for a person, then `peaka connections wait <id>`; then `peaka catalogs create --name <sqlname> --connection <n>`. A Google Sheets catalog also needs the spreadsheets: `--sheet <id or URL>[=name]` (repeatable); a person can pick them in the browser with `peaka connections pick <n>` and pass the ids.
- Data governance (an organization key with manage_data_governance, `--org <id>`): tags `peaka gov tags list --q PII`, `create --name PII --parent Personal`; masking policies `peaka gov mp create --tag PII --default nullify --exception group:Clinicians=unmasked`; column tags `peaka gov column-tags search --tag PII`, `column-tags set --catalog-id 7 --schema public --table people --tag-column ssn=PII` (what is not given comes off; `--none` empties); row rules `peaka gov row-rules create --name "Own region" --table c.s.t --expression "region = current_user_props('region')" --except group:auditors`.
- Member attributes (what `current_user_props('key')` reads): `peaka gov attributes create --key region`, `peaka gov attributes set ana@example.com region --value EMEA`, `attributes values ana@example.com`, `attributes import -f values.csv [--apply]`; try a rule before saving it with `peaka gov row-rules preview --table c.s.t --expression "…" --as ana@example.com`. Ids and SQL names a rule or a grant needs: `peaka members list --q ana`, `peaka roles list`, `peaka groups list`.
- Governance as a file: `peaka gov apply -f governance.yaml [--dry-run]` makes tags, masks, masking policies, table and column tags and row rules what the file says (what the file leaves out stays; nothing is deleted).
- A governance change that waits for approval ends with exit 10 and prints what waits (a change request, a policy proposal, a suggestion): it is not applied yet.
- Publisher access: `peaka queries create|update … --access definer|invoker`; where the policy needs an approval, `peaka gov publications request <query> --reason …`. Administrators: `peaka gov publications list -f status=ALL` (PENDING, APPROVED, REJECTED, LAPSED, REVOKED), `approve <id> --reason …`, `reject <id>`, `revoke <id>`. Policy proposals: `peaka gov policy proposals`, `policy proposal <id>`, `approve <id>`.
- Who sees what: `peaka gov access member ana@example.com`, `access column --catalog-id 7 --schema public --table people --column ssn`, `access holders …`, `access exposure tags --tag PII`, `access readers --tag PII --days 30`; each read decision: `peaka gov decisions --user <id> --denied`; what turning enforcement on would refuse: `peaka gov impact`.
- Reviews and exceptions: `peaka gov reviews start`, `describe <id>`, `decide <id> <item> --decision CONFIRMED|REMOVED|FLAGGED`, `close <id>`, `export <id>`; lift a tag from a derived column with `peaka gov declassifications create --object-kind MATERIALIZED_QUERY --object-id <id> --column c --tag PII --reason …` (it waits for approval), `list`, `revoke <id>`.
- Automatic classification: `peaka gov sources create --kind SNOWFLAKE|DETECTOR --catalog-id 7 --apply-as SUGGESTED`, `sources sync <id>`, `sources map <id> --class … --tag …`; detection patterns `peaka gov patterns list`, `patterns set --class … --pattern …`.
- Column tags show `status` (ACTIVE, LOST: the column is gone at its source, MOVED); `peaka queries status <mq>` shows `readsLive`.
- Audit: `peaka gov audit events --kind MQ_REFRESH_REQUESTED,GROUP_MEMBER_ADDED --since …` shows `actor`, `actorKind` and an `impersonator` when there is one; `--fields …,after.requestedBy` for who asked for a refresh (USER, API_KEY, SERVICE_ACCOUNT, SCHEDULE, SERVICE). `peaka gov audit export` streams NDJSON.
- Suggest and review classification: a project member suggests `peaka gov suggestions create --catalog-id 7 --schema public --table people --column ssn --tag PII --reason "…"` (it waits; `describe <id>` follows it); an administrator reviews `peaka gov suggestions list --tag PII`, `approve <id>`, `reject <id> --note "…"`, or many with `approve-many --tag PII --project <id>`. A change that needs a second administrator answers 202: `peaka gov changes list`, `approve <id>`.
- Anything else: `peaka api GET|POST <path> [-f k=v] [--input body.json]` reaches every endpoint of the REST API.

## As an MCP server
`peaka mcp serve --project <id>` exposes the same commands as MCP tools over stdio; data governance, groups, grants and member attributes are read-only through it unless it is started with `--allow-governance-writes`. It exposes them as tools (`peaka_table_list`, `peaka_get_project_metadata`, `peaka_sql`, `peaka_run`, …) with the resources `peaka://guide` and `peaka://sql-rules`.
