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
- Exit codes: 0 ok · 1 unexpected · 2 usage · 3 identity · 4 not found · 5 permission · 6 invalid · 7 conflict · 8 query/job failed · 9 retryable · 130 interrupted.
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
- Anything else: `peaka api GET|POST <path> [-f k=v] [--input body.json]` reaches every endpoint of the REST API.

## As an MCP server
`peaka mcp serve --project <id>` exposes the same commands as MCP tools over stdio (`peaka_table_list`, `peaka_get_project_metadata`, `peaka_sql`, `peaka_run`, …) with the resources `peaka://guide` and `peaka://sql-rules`.
