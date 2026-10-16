# Search evaluation

The completed [2026-10-17 live baseline](RESULTS.md) covers 24 questions with Exa,
Tavily, and their merged result. It reports costs and the remaining evaluation
limits without claiming that fusion is generally best.

`questions.json` contains 24 public queries covering technical documentation,
factual lookups, Chinese queries, changing release information, niche topics,
and research papers. These are a reproducible smoke/coverage set, not a claim of
representative real-world search quality.

The runner submits each question through the actual CLI and configured cloud
server in a fixed order: the selected individual providers, then their merged
result. The default selection is Exa and Tavily (three variants). Each variant asks for five returned sources. It saves the complete CLI stdout,
structured result, stderr, timings, and question in a private output directory.
No provider API is called directly by the runner.

## Run

First inspect the server's remaining budget and confirm that the advertised
providers are available. The example permits up to USD 0.20 per run; this is a
ceiling, not an expected price. The server may retain a conservative reserve for
operations whose final USD charge is not reported. The runner does not change
any limits or credentials.

```sh
soseek usage --json
soseek providers --fresh --json
node scripts/evaluate.mjs --live --binary /path/to/soseek \
  --providers exa,tavily --budget-usd 0.20 \
  --out /private/path/new-evaluation-directory
```

The client uses its saved login or `SOSEEK_URL` and `SOSEEK_TOKEN_FILE`. Never
supply a token's literal value as an argument. Output directories must be new;
the runner refuses to overwrite an existing evaluation. `--limit 1` runs the
first question with the selected variants. The default 24-question run submits
72 search tasks. `--providers` changes the selection; do not include a provider
with an unresolved rate-limit response. `--exclude-reason 'serper=Prior live
HTTP 429; excluded without retry'` records that exclusion in the manifest and
summary.

Each run has a 60-second server deadline and a 90-second local waiting limit.
The runner checks usage before starting and after every completed question.
It does not automatically retry an error, resume ambiguous submissions, or
change the server's quotas.

Any structured 429/rate-limit signal or positive `retry_after_seconds` stops the
experiment immediately after saving that response and a stop report. Budget
blocks and ambiguous client results also stop further submissions. Inspect the
saved evidence and any run/pending ID before deciding on another experiment.
A stop report is an incomplete evaluation, not a completed benchmark.

## Outputs and interpretation

All output files are written with mode `0600` inside a `0700` directory:

- `manifest.json`: start time, variant order, per-run budget, and methodology.
- `QUESTION-VARIANT.json`: complete request question and CLI response record.
- `metrics.json`: per-run metrics, including private run IDs.
- `usage.json`: read-only server usage snapshots.
- `summary.json`: completion status, aggregate metrics, and any stop reason.
- `stop-report.json`: present if the runner stopped early.

These raw files can contain identity-linked run IDs and full upstream responses.
Keep them outside source control. A public report should omit run/identity IDs,
private endpoints, token paths, and raw logs. The query set itself is public.

The expected-domain check asks whether a result links to one of the query's
listed domains. Two questions intentionally have no expected-domain label and
are excluded from that denominator. Domain presence is only a source-location
heuristic: it does not establish factual accuracy, correct dates, useful content,
or relevance of an answer. Failed cases remain in the labeled denominator.

The merged variant runs after the selected single-provider calls and can reuse
same-user cached responses. `cached_attempts` is reported explicitly. Therefore
its latency cannot be interpreted as cold multi-provider search performance,
and its additional USD use may be lower because earlier runs already paid for
those responses. P50/P95 use the nearest-rank definition and include CLI startup,
networking, service scheduling, and cache behavior.

Costs retain upstream-reported USD, estimates, held reserves, unsettled attempts,
and provider credits separately. Missing usage is not recorded as known zero.
A held reserve is a budget/risk ceiling, not proof of the final bill. Report the
remaining unknown amounts alongside any known charges; do not sum provider
credits into USD or label zero reported USD as free usage.

This first experiment does not include blinded relevance judgments or a
cache-neutral randomized comparison. It must not be used on its own to claim
that merging is more accurate, faster, or generally better than one provider.

## Runner tests

These tests use a mock CLI and do not incur upstream usage:

```sh
node --test evals/evaluate.test.mjs
```

They cover structured rate-limit variants, unknown-cost preservation, immediate
stop behavior, and private output permissions.
