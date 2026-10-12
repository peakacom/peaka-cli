# peaka

Peaka from the terminal — for people and for agents. One grammar over the whole
public API, a SQL REPL over the same endpoint the drivers use, machine-readable
by default when piped.

```
peaka <resource> <verb> [args] [flags]

peaka auth login --api-key                 # store an identity (browser login: next phase)
peaka orgs use acme && peaka projects use analytics
peaka projects list                         # table on a terminal, JSON in a pipe
peaka api GET /connections/config --jq length
```

Design document: see the Peaka CLI Tasarımı doc (internal). This tree holds
phase 0 (configurations, API-key identities, `orgs`/`workspaces`/`projects`,
`api`, output formats, exit codes), phase 2 (`peaka sql`) and phase 3 (the
resource commands over the whole public API).

## Resources

```
peaka connections types list --category Database     # what can be connected
peaka connections types describe postgres            # the credential fields
peaka connections create --type postgres --name warehouse -c url=db.example.com -c port=5432 \
    -c databaseName=app -c user=analyst              # asks for the password on a terminal
peaka connections create --type hubspot --name crm   # opens the browser, finishes in the terminal
peaka catalogs create --name hubspot --connection crm
peaka schemas list hubspot
peaka tables list hubspot.crm
peaka tables describe hubspot.crm.deals              # columns
peaka tables export hubspot.crm.deals --format-out PARQUET --out ./data
peaka cache create --table hubspot.crm.deals --full-refresh '0 3 * * *'
peaka queries create --name won_deals --sql-file won.sql --path /sales
peaka queries execute won_deals --format csv
peaka folders tree --queries
peaka metadata refresh --catalog hubspot --wait
peaka metadata update hubspot.crm.deals --description "Open and closed deals" --use-with-ai
peaka ai chat "How many deals closed last month?"
peaka tables create leads && peaka tables add-column leads email --type text
peaka tables import leads --file leads.csv
```

Every resource has `--help`. References accept ids or names (`catalog.schema.table`
for tables, a path like `/sales` for folders). `create` and `update` take flags,
`-f KEY=VALUE` / `-f KEY:=JSON` body fields, or `--from-file body.json`.
Long-running work (`exports`, `metadata refresh`, OAuth connections) has `--wait`;
with `--no-input` an OAuth `connections create` prints the authorization URL and
`peaka connections wait ID` finishes later.

Most of these commands are declared as `opSpec` entries in
`internal/cli/resources.go`: method, path template, arguments with resolvers,
flags with their place in the request. Hand-written verbs sit next to them.

## SQL

`peaka sql` speaks Trino's HTTP protocol to the `dbc` endpoint — the same path
the JDBC, Python and Node drivers take — so progress, cancellation (Ctrl-C),
`USE`, session properties and error positions all come from the engine.

```
peaka sql "SELECT * FROM stripe.payment.charges LIMIT 10"
peaka sql -f report.sql --format csv > out.csv
cat q.sql | peaka sql --format jsonl | jq .amount
peaka sql --catalog stripe --schema payment "SELECT count(*) FROM charges"
peaka sql --explain "SELECT ..."         # the plan, as text
peaka sql "SELECT ..." --stats             # statistics on stderr
peaka sql                                  # interactive session
```

On a terminal the first 1000 rows are shown (`--max-rows 0` for all) and
wide tables are fitted to the terminal; in a pipe every row streams. Several
statements separated by `;` run in order.

The interactive session has multi-line editing (Enter runs a statement ending
in `;`), syntax highlighting, history, and Tab completion of catalogs,
schemas, tables, columns (by alias too), keywords and functions. Meta
commands: `\dc` `\dn` `\dt` `\d` `\use` `\x` `\format` `\timing` `\pager`
`\max-rows` `\o` `\i` `\e` `\refresh` `\project` `\stats` `\explain` `\?` `\q`.

## Build

```
make build            # bin/peaka
make test
make sync-spec        # refresh api/openapi.json from the test gateway
```

Go 1.24+.

## Configuration

`~/.config/peaka/configurations/<name>.yaml` (or `$PEAKA_CONFIG_DIR`). A
configuration carries two addresses, `api` (REST) and `dbc` (SQL); the
`environment` preset (`prod`, `eu`, `test`) fills both, and each can be
overridden — a test, EU, on-prem or local install is just another
configuration.

Precedence for every property: flag > environment variable > configuration >
preset > default. `peaka config list` shows each value with its source.

| Property | Env | Flag |
| --- | --- | --- |
| environment | `PEAKA_ENVIRONMENT` | `--environment` |
| api | `PEAKA_API_URL` | `--api-url` |
| dbc | `PEAKA_DBC_URL` | `--dbc-url` |
| account | `PEAKA_ACCOUNT` | `--account` |
| org | `PEAKA_ORG` | `--org` |
| project | `PEAKA_PROJECT` | `--project`, `-p` |
| format | `PEAKA_FORMAT` | `--format`, `-o` |

`PEAKA_API_KEY` bypasses stored accounts. `PEAKA_CONFIGURATION` selects the
configuration. Secrets go to the OS keychain, or to `credentials.json` (0600)
when there is none or `PEAKA_CREDENTIALS_STORE=file`.

## Contract

- stdout is data, stderr is for people. `--format` defaults to `table` on a
  terminal and `json` in a pipe; `jsonl`, `csv`, `tsv`, `yaml`, `md`,
  `vertical` are always available; `--fields` picks columns, `--jq` filters.
- Errors are the API's problem+json, passed through on stderr (one line when
  piped, readable on a terminal). Branch on `code`.
- Exit codes: 0 ok · 1 unexpected · 2 usage · 3 identity · 4 not found ·
  5 permission · 6 invalid request · 7 conflict · 8 query/job failed ·
  9 retryable · 130 interrupted.
- No prompts when stdin is not a terminal (`--no-input`); destructive
  commands need `--yes` there.

`peaka help --agent` prints the short guide for AI agents;
`peaka help --format json` prints the whole command tree.
