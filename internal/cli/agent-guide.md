# peaka — guide for agents

`peaka` is Peaka's CLI. Grammar: `peaka <resource> <verb> [args] [flags]`.
Verbs: `list`, `describe`, `create`, `update`, `delete`, `use`, plus resource-specific ones.
Resources: `orgs`, `workspaces`, `projects`, `api-keys`, `connections` (+ `types`), `catalogs`, `schemas`,
`tables`, `cache`, `queries`, `folders`, `exports`, `semantic-catalogs`, `parametric-tables`, `metadata`
(+ `relations`, `golden-sqls`, `categorical`, `callback`), `ai`, `drivers`, `embed`, `sql`, `api`.
`peaka help --format json` lists the full tree with every flag; `peaka <resource> --help` the verbs.
References: tables are `catalog.schema.table`; catalogs, connections, queries and folders accept an id or a name
(folders also a path like `/analytics`); an ambiguous name is exit 6 listing the ids.
Writes: `create`/`update` take flags, `-f KEY=VALUE` / `-f KEY:=JSON` body fields, or `--from-file body.json`.
Long jobs: `exports` and `metadata refresh` have `--wait`; `connections create` for an OAuth type with `--no-input`
prints `{"id","url","status":"PENDING_AUTHORIZATION"}` — a person opens `url`, then `peaka connections wait ID`.
SQL: `peaka sql "STATEMENT"` runs Trino SQL on the project in context; `--format jsonl` streams rows,
`--max-rows N` caps them, `--query-timeout 30s` bounds the run. Qualify tables as `catalog.schema.table`
(quote reserved words: `peaka."table".customers`). `SHOW CATALOGS`, `SHOW SCHEMAS FROM c`,
`SHOW TABLES FROM c.s`, `DESCRIBE c.s.t` discover the data. A failed query is exit 8 with
`code: QUERY_FAILED` and `params.errorName`, `params.line`, `params.column` when known.

## Rules
1. You never get a prompt: when stdin is not a terminal, `--no-input` is on. A missing value is exit 6 with the field named; a destructive command without `--yes` is exit 2.
2. stdout is data, stderr is for people. When stdout is a pipe the format is `json`; pass `--format jsonl|csv|yaml` to change it, `--fields a,b` to pick columns, `--jq EXPR` to filter.
3. Errors are the API's problem+json on stderr, one line: `{"code":"PERMISSION_REQUIRED","status":403,"detail":"...","retryable":false,"traceId":"..."}`. Branch on `code`, never on `detail`.
4. Exit codes: 0 ok · 1 unexpected · 2 usage · 3 identity missing/invalid · 4 not found · 5 not allowed · 6 invalid request · 7 conflict · 8 query/job failed · 9 retryable (wait, then retry the same command) · 130 interrupted.
5. Identity: `PEAKA_API_KEY=<key>` is enough. Otherwise a stored account is used (`peaka auth login --api-key`).
6. Context: `PEAKA_PROJECT=<id>` and `PEAKA_ORG=<id>` (or `--project`, `--org`). Run `peaka me` first: it prints the identity and the context.
7. `--dry-run` prints the request a writing command would send instead of sending it; `--if-not-exists` makes a create succeed when the thing exists; `--debug` prints the HTTP exchange to stderr.
8. `peaka api METHOD PATH [-f k=v] [--input body.json]` reaches any REST endpoint; see `peaka api --help`.
9. `peaka mcp serve --project <id>` serves these commands as MCP tools over stdio (`peaka_sql`, `peaka_list_tables`, `peaka_run`, …) with the resources `peaka://guide` and `peaka://sql-rules`.

## Typical session
```
peaka me --format json
peaka orgs list --format json
peaka projects list --org <orgId> --format json
peaka catalogs list --project <projectId>
peaka tables list stripe.payment --project <projectId>
peaka tables describe stripe.payment.charges --project <projectId>
peaka sql "SELECT count(*) FROM stripe.payment.charges" --project <projectId>
peaka queries create --name charges_by_day --sql-file q.sql --project <projectId>
peaka queries export charges_by_day --format-out PARQUET --out ./data --project <projectId>
```
