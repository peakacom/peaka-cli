# peaka

Peaka from the terminal — for people and for agents. One grammar over the whole
public API, a SQL REPL over the same endpoint the drivers use, machine-readable
by default when piped.

```
peaka <resource> <verb> [args] [flags]

peaka auth login                            # browser login (OAuth 2.0 + PKCE); --api-key for a key
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
peaka catalogs create --name sheets --connection sheets    # Google Sheets: picks the spreadsheets in the browser (or --sheet ID|URL[=name])
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
for tables, a path like `/sales` for folders), spelled loosely (`google-sheets`
is `GOOGLE_SHEETS`); a miss suggests the nearest names. With shell completion
installed, Tab completes them live: catalogs, `catalog.schema.table` paths,
connections, queries, folders, projects, workspaces, connection types and the
context flags (`--project`, `--org`, `--catalog`, `--connection`). The brew
install sets completion up; otherwise `peaka completion install` adds the one
line your shell needs (bash, zsh, fish or PowerShell) to its profile, once. `create` and `update` take flags,
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

## Install

Pick your platform; each block is complete, including shell completion.

**macOS and Linux with Homebrew**

```sh
brew install peakacom/tap/peaka
```

Completion for bash, zsh and fish comes with the cask; open a new shell.

**Linux and macOS without Homebrew**

```sh
curl -fsSL https://raw.githubusercontent.com/peakacom/peaka-cli/main/install.sh | sh
peaka completion install
```

The script puts `peaka` in `/usr/local/bin` (or `~/.local/bin` when that is not
writable; `PEAKA_INSTALL_DIR` chooses) after checking the release's checksum.
Debian and RPM packages are on the release page too (`peaka_<version>_linux_amd64.deb`,
`…_arm64.deb`, `….rpm`): download one and `sudo dpkg -i` or `sudo rpm -i` it.

**Windows**

```powershell
scoop bucket add peaka https://github.com/peakacom/scoop-bucket
scoop install peaka
peaka completion install
```

Without Scoop: download `peaka_<version>_windows_amd64.zip` (or `arm64`) from
the [release page](https://github.com/peakacom/peaka-cli/releases), put
`peaka.exe` on your `PATH`, then `peaka completion install`. Use Windows
Terminal or PowerShell 7 for the SQL session.

**Any platform, by hand**

Every release carries an archive per platform and `checksums.txt`:
https://github.com/peakacom/peaka-cli/releases. Unpack, put `peaka` on your
`PATH`, run `peaka completion install`.

**Check**

```sh
peaka version
peaka auth login
```

**Upgrading**

```sh
peaka upgrade
```

`peaka upgrade` fetches the latest release, verifies its checksum and replaces
the executable in place; `peaka upgrade --check` only reports. An install owned
by a package manager is upgraded through it instead, and `peaka upgrade` says
so: `brew upgrade peaka`, `scoop update peaka`, or your distribution's package
tool. At most once a day a terminal session is told when a newer release
exists (`PEAKA_NO_UPDATE_CHECK=1` turns that off).

## Build

```
make build            # bin/peaka
make test
make sync-spec        # refresh api/openapi.json from the test gateway
make release-snapshot # every platform into dist/, nothing published (needs goreleaser)
```

Go 1.24+. Releases are cut by hand from a developer machine (no CI):
`make release VERSION=0.2.0` tests, tags `v0.2.0`, pushes the tag and runs
goreleaser, which builds every platform and publishes the GitHub Release;
`scripts/tap.sh` then pushes the Homebrew cask to `peakacom/homebrew-tap`
(`make release TAP=` leaves the tap alone; `scripts/tap.sh --print` shows the
cask to submit by hand). It needs `goreleaser`
(`go install github.com/goreleaser/goreleaser/v2@latest`) and a `gh` login
that can write to both repositories.

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

## Login

`peaka auth login` opens the browser: you log in to Peaka, approve the CLI, and
the terminal continues. Under the hood it is OAuth 2.0 authorization code with
PKCE through a loopback redirect (RFC 8252) against the environment's
authorization server (`oauth` property, `PEAKA_OAUTH_URL`); the CLI registers
itself once per server as a public client (`oauth-clients.json`). Access tokens
last an hour and are refreshed silently; the refresh token lasts months.
`peaka auth status` shows the expiry, `peaka auth logout` revokes and forgets.
`--no-browser` prints the URL for SSH sessions. API keys remain available with
`--api-key` and `PEAKA_API_KEY` (CI, agents, service accounts).

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
`peaka help --format json` prints the whole command tree. `--dry-run` prints
the request a writing command would send; `--if-not-exists` makes a create
succeed when the thing exists.

## MCP

`peaka mcp serve` exposes the CLI as Model Context Protocol tools over stdio,
one server per configuration/project:

```
claude mcp add peaka -- peaka mcp serve --project <id>
```

Tools: `peaka_sql`, `peaka_run` (any command line), and one tool per common
operation (catalogs, schemas, tables, columns, queries, caches, metadata,
connections, semantic catalogs, exports). Resources: `peaka://guide` and
`peaka://sql-rules`.

## Skill

The skill (`docs/SKILL.md`, also inside the binary) is the short guide an AI
coding agent loads when a task mentions Peaka. Put it where your agent looks:

```
peaka skill install                                  # Claude Code, for you: ~/.claude/skills/peaka/SKILL.md
peaka skill install --agent claude,codex,cursor --project-dir .   # in this repository, for the team
peaka skill show                                     # print it
```

### Bounded metadata management

`peaka metadata list --project PROJECT --catalog CATALOG --limit 20` returns one page, including AI-excluded tables the caller can manage. Continue with `--cursor NEXT_CURSOR` and unchanged scope/limit. The envelope retains `items`, `nextCursor`, `hasMore`, `completeness`, and `snapshot`; an empty page can have a continuation. Maximum page size50; cursors expire after15minutes. This lists collected metadata, not a complete source inventory. Existing `metadata describe` is unchanged.

`peaka metadata ai-usage CATALOG --setting inherit` restores inherited AI preference; `on` and `off` are also accepted. The legacy `--use-with-ai` flag remains supported. AI preference does not grant or remove data access.

These commands require the DEV-4102 gateway/search capabilities. Unsupported endpoints and permission failures are explicit errors, without automatic full-project fallback. `metadata refresh --wait` follows the submitted run IDs, never the latest catalog job; only complete success exits successfully. Deploy the backend/gateway before releasing this CLI. Rolling the server back leaves legacy reads available; do not downgrade safe edits into blind writes.
