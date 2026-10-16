# CLI usage

The `soseek` client talks to a `soseekd` server. All upstream searches, page reads,
and model calls execute on that server. The client needs a server URL and its own
soseek token, not upstream credentials. See [self-hosting](../deploy/README.md) if
you need to run a server.

## Login and discovery

```sh
soseek auth login --server https://soseek.example.com
# Or read an existing private token file:
soseek auth login --server https://soseek.example.com --token-file /private/token

soseek doctor --json
soseek capabilities --json
soseek providers --json
soseek profiles --json
soseek operations exa --json
soseek usage --json
```

Interactive token input is hidden. `--token-stdin` accepts a token from a pipe;
avoid placing its literal value in shell history. Unix token files require mode
`0600`. `doctor` checks authenticated API access without calling paid providers.

Discovery separates implemented operations from configured channels and their
verification state. Results are cached per server and identity for five minutes;
`--fresh` refreshes them. `doctor` and `usage` always query the server. Only use
provider names, options, and profiles that your deployment advertises.

`auth logout` removes the client's managed login files. It does not revoke the
server token or delete an externally supplied credential file. Server token
revocation is an administrator operation.

## Search, read, research, answer

| Command | Result |
| --- | --- |
| `soseek search "QUERY"` | Search results and routing/attempt metadata |
| `soseek read URL` | Page content without starting an answer model |
| `soseek research "QUERY"` | Evidence collected by the cloud research planner |
| `soseek answer "QUERY"` | A generated answer together with evidence |
| `soseek call PROVIDER.OPERATION --input FILE` | A registered provider-native operation |

```sh
soseek search "Go HTTP client timeout" --json
soseek search "query" --provider exa --channel exa-direct --json
soseek search "query" --providers exa,tavily,serper --json
soseek search "query" --provider grok --json
soseek read https://example.org/article --json
soseek research "Compare two technical approaches" --profile deep --json
soseek answer "Explain the tradeoffs with sources" --json
soseek research "Collect related sources" --mode pipeline --json
```

Examples with named providers/channels require matching server configuration.
An explicit provider is not permission to silently switch to another provider.
`--provider` and `--providers` conflict; an explicit `--channel` requires
`--provider`. A provider list cannot be combined with `--profile`.

`research` and `answer` default to cloud agent mode: the configured Grok model
chooses tools and follow-up actions. `--mode pipeline` selects the fixed workflow
without an autonomous planner. Grok's native search is also independently
available through `search --provider grok` when configured.

`--intent` supplies a task-type hint supported by server discovery. It does not
infer or replace domain, language, time, or geographic restrictions. Provider
options are namespaced and checked by the server:

```sh
soseek search "vector search" --provider exa \
  --opt 'exa.category=research paper' --json
```

Option values may be JSON (`true`, `5`, `["example.org"]`) or plain strings.
Unknown or unsupported parameters produce an error instead of being silently
discarded. Inspect the relevant operation schema before selecting options.

## Plan and bound the work

```sh
soseek research "A technical question" \
  --budget-usd 0.50 --deadline 180s --plan --json

soseek research "A technical question" \
  --budget-usd 0.50 --deadline 180s --wait-timeout 4m --json
```

- `--plan` validates and returns the planned operations and cost ceilings without
  issuing upstream calls. An agent plan can be dynamic; it is not a prediction
  of every decision the model will make.
- `--budget-usd` limits the run within the server/user limits. Amounts use up to
  six decimal places. They do not change provider prices.
- `--deadline` controls execution on the server. It accepts seconds or a Go
  duration, such as `180` or `3m`.
- `--wait-timeout` controls only how long this CLI waits. The default is five
  minutes. Reaching it leaves the cloud task running.
- `--limit` requests a result count; server limits still apply.
- `--fresh` on an execution command bypasses upstream content caches.

## Native operations

Use `operations` to inspect a registered schema, then provide one JSON object:

```sh
soseek operations exa --json

cat > request.json <<'JSON'
{"url":"https://example.org/article"}
JSON

soseek call exa.find_similar --input request.json --plan --json
soseek call exa.find_similar --input request.json --json
```

`--input -` reads the object from stdin. Native calls pass through the same
authentication, validation, budget, and run lifecycle as ordinary search. They
are not an arbitrary HTTP proxy: callers cannot override upstream credentials,
base URLs, or methods.

## Runs and submission recovery

```sh
soseek research "Investigate a question" --detach --json
soseek runs get RUN_ID --json
soseek runs wait RUN_ID --wait-timeout 10m --json
soseek runs cancel RUN_ID --json
```

`--detach` returns after the server has accepted a persistent run. `runs get`
returns the current snapshot while it is running and the result when terminal.
`runs wait` follows the same run. Ctrl-C stops local waiting and displays the run
ID; it does not send cancellation. `runs cancel` explicitly asks the server to
stop, and a task that already finished can remain finished.

A connection failure during submission may occur after the server has accepted
the work. The error contains a pending ID and the exact recovery command:

```sh
soseek pending list --json
soseek pending resume PENDING_ID --json
```

The pending record contains the original request and idempotency key in a
private file. Resuming reuses both and requires the same server and token
identity. After acknowledgement, the local request body is removed and the run
ID is retained. Unacknowledged bodies expire after 24 hours; expired entries are
deleted when pending state is read or cleaned up, and are not resubmitted.

Use a known run ID directly if you already have one. Creating a new search is a
new request and can incur another charge. A stopped or failed upstream call can
still have an unknown final charge; client recovery does not make the upstream
provider exactly-once.

## Output and exit status

```sh
soseek search "query" --json
soseek research "query" --stream
soseek runs get RUN_ID --fields answer,results.title,results.url --max-chars 8000 --json
```

`--json` writes one final JSON object to stdout, including errors. Progress and
warnings go to stderr. Use the JSON `status`, `warnings`, and `attempts` to assess
partial results.

`--stream` writes NDJSON with `status`, `event`, and final `result` records. It
consumes SSE progress when available and checks run snapshots independently;
losing the event stream does not lose the result. A local error is an `error`
object. `--stream` cannot be combined with `--json` or `--detach`.

`--fields` selects result fields; `results.FIELD` selects fields within each
source. Run identity, status, warnings, and source URLs remain available.
`--max-chars` clips answer/source text across the returned result, counting
Unicode characters. It does not reduce executed work or billing. Clipped results
set `truncated: true` and provide the command/URL to retrieve the complete result.
Human-readable output uses short source summaries and removes terminal control
sequences from provider text.

| Exit code | Meaning |
| --- | --- |
| `0` | Success, or an explicitly reported partial result |
| `2` | Invalid command or local input |
| `3` | Login or permission error |
| `4` | Failure; also partial results with `--require-all` |
| `5` | Local waiting timed out or was interrupted; cloud work is not cancelled |
| `6` | The run is cancelled |

JSON usage separates `reported_micros`, `estimated_micros`, `reserved_micros`,
`unsettled_attempts`, tokens, and provider credits. One USD is 1,000,000 micro-USD.
These are different accounting views, not three amounts to blindly add.
Unsettled charges are unknown, even if the reported amount is zero. Credits are
not USD. See the provider's bill for final settlement.

## Local files and overrides

| Location | Contents |
| --- | --- |
| `$XDG_CONFIG_HOME/soseek/client.json` | Server URL and token-file reference |
| `$XDG_CONFIG_HOME/soseek/client-token` | Managed soseek access token |
| `$XDG_STATE_HOME/soseek/pending/` | Private submission recovery records |
| `$XDG_STATE_HOME/soseek/discovery/` | Capability cache isolated by server and identity |

Defaults are `~/.config` and `~/.local/state` when the respective XDG variables
are unset. `SOSEEK_URL` and `SOSEEK_TOKEN_FILE` override the saved connection.
Token/config/pending files are written privately with mode `0600` on Unix. Keep
them outside source control and protect local state as it can contain queries.

For all options, run `soseek --help` or `soseek COMMAND --help`.
