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

## Discover data, then query
```
peaka catalogs list
peaka schemas list <catalog>
peaka tables list <catalog>.<schema>
peaka tables describe <catalog>.<schema>.<table>        # columns and types
peaka catalogs search <term>                            # find tables by name
peaka sql "SELECT … FROM <catalog>.<schema>.<table> LIMIT 100" --format jsonl
```
SQL is Trino. Qualify tables fully; quote reserved words (`peaka."table".customers`). Add `LIMIT` while exploring; `--max-rows N` caps output and cancels the query server-side; `--query-timeout 60s` bounds it. Read `peaka help --agent` for the SQL rules, and never issue DML through SQL (Peaka tables change through `tables import` and `tables add-column`).

## Common tasks
- Save and reuse SQL: `peaka queries create --name x --sql-file q.sql --path /folder`; `peaka queries execute x`; `peaka queries export x --format-out PARQUET --out ./data`.
- Export a table: `peaka tables export c.s.t --limit 100000 --out ./data`.
- Cache a slow external table: `peaka tables cached c.s.t` (is it cacheable?), then `peaka cache create --table c.s.t --full-refresh '0 3 * * *'`; `peaka cache status c.s.t`.
- AI metadata for better text-to-SQL: `peaka metadata refresh --catalog <c> --wait`; `peaka metadata describe --catalog <c>`; `peaka metadata golden-sqls add --prompt "…" --sql "…"`.
- Connect a source: `peaka connections types describe <type>` lists the credential fields; `peaka connections create --type <type> --name <n> -c key=value …`; OAuth types with `--no-input` print an authorization URL for a person, then `peaka connections wait <id>`; then `peaka catalogs create --name <sqlname> --connection <n>`.
- Anything else: `peaka api GET|POST <path> [-f k=v] [--input body.json]` reaches every endpoint of the REST API.

## As an MCP server
`peaka mcp serve --project <id>` exposes the same commands as MCP tools over stdio (`peaka_sql`, `peaka_list_tables`, `peaka_run`, …) with the resources `peaka://guide` and `peaka://sql-rules`.
