# Writing SQL for Peaka

Peaka runs Trino SQL. Before running a statement, check it against these rules.

## Naming and quoting
- Qualify every table as `catalog.schema.table`. Quote an identifier with double quotes when it is a reserved word or has spaces or capitals: `peaka."table".customers`, `hubspot.crm."Deal Stages"`.
- Use table aliases and prefix columns with them when more than one table is involved.
- String literals use single quotes; double quotes are only for identifiers.
- Discover names instead of guessing: `SHOW CATALOGS`, `SHOW SCHEMAS FROM c`, `SHOW TABLES FROM c.s`, `DESCRIBE c.s.t` (or the CLI's `tables describe`).

## Reads only
- Do not issue DML or DDL (INSERT, UPDATE, DELETE, DROP, CREATE); Peaka tables change through the API (`tables import`, `tables add-column`).
- Add `LIMIT` to exploratory queries. Uncached external tables can be slow; check `isCached`/`isCacheable` and consider a cache before scanning a large table repeatedly.

## Common mistakes to check
- `NOT IN` with NULL values returns nothing; prefer `NOT EXISTS` or a join.
- `UNION` deduplicates; use `UNION ALL` unless duplicates must go.
- `BETWEEN` is inclusive.
- Type mismatches in predicates: compare dates with dates, not strings. Do not use `FROM_ISO8601_TIMESTAMP` on date or timestamp columns, only on varchar columns holding ISO strings. Do not compare timestamp columns to date strings; cast or use `DATE '2026-01-01'`.
- Interval literals quote only the number: `INTERVAL '7' DAY`.
- `GROUP BY` cannot use a SELECT alias: repeat the expression (or use positions).
- `HAVING` cannot contain window functions.
- Every column in the SELECT list that is not aggregated must be in `GROUP BY`.
- Join on the proper key columns, with `ON`, never through `WHERE` alone; name ambiguous columns with their alias.
- Subqueries in FROM need an alias.
- Do not use `JSON_CONTAINS`; use `json_extract_scalar`, `json_array_contains`, or `CAST(x AS JSON)` functions.
- Check balanced quotes and parentheses, the statement order (SELECT, FROM, JOIN, WHERE, GROUP BY, HAVING, ORDER BY, LIMIT), and the spelling of table and column names.

## When a query fails
The CLI reports `QUERY_FAILED` with `params.errorName` and, when known, `line` and `column`. `COLUMN_NOT_FOUND` and `TABLE_NOT_FOUND` mean a name is wrong: describe the table and retry. `SYNTAX_ERROR` points at the position. `EXCEEDED_TIME_LIMIT` or a cancellation means the scan is too large: add filters, a LIMIT, or cache the table.
