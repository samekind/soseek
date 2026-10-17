# soseek 0.2 output efficiency

Measured on 2026-10-17. The 0.2 CLI makes the everyday response a compact evidence
view; the authenticated HTTP result and saved archive remain complete. Existing
automation that needs `attempts`, `routing`, raw payloads or citation spans must
add `--full`. The project evaluation and acceptance runners do this explicitly.

## What was wasting context

- `doctor --json` included every operation's input schema, although access
  checking only needs the check result. The compact check is 145 bytes in replay.
- Ordinary results repeated canonical URLs, source bookkeeping, routing and
  successful attempts. These remain available in the saved full result.
- A native Grok sample spent approximately 42,247 estimated tokens on `raw`
  alone, and another 3,478 on citation records containing original answer text.
  Compact output keeps the complete final answer, all source identities and
  reference links; raw and original citation spans are opt-in.
- A research sample spent approximately 19,765 estimated tokens on source
  records. Compact limits page excerpts; this is deliberate omission of text,
  **not lossless compression**. Saved-source windows recover the stored text.
- Exa standard search did not ask for highlights, even though the adapter could
  parse them. Missing excerpts encouraged a second read just to judge relevance.
- Routine status messages went to stderr in JSON/agent use. They are now opt-in
  with `--progress`; errors, partial-result warnings and recovery information stay.

## Same-result replay

The before CLI is commit `3fd4412` (0.1.0). Both binaries read the **same saved
results** through an offline loopback fixture. No search, page fetch or model
generation occurs in this comparison. Counts include the final JSON newline.
Tokens use `tiktoken` `o200k_base`; they are context-size estimates, not Claude,
Grok, OpenAI or provider invoice measurements.

| Saved sample | Before bytes | After bytes | Before tokens | After tokens | Token reduction |
| --- | ---: | ---: | ---: | ---: | ---: |
| Doctor, five provider catalogs | 25,453 | 145 | 6,735 | 40 | 99.41% |
| Exa search, five references | 2,837 | 1,645 | 917 | 501 | 45.37% |
| Grok-led research | 88,235 | 18,690 | 23,327 | 5,542 | 76.24% |
| Short saved page read | 1,376 | 896 | 447 | 266 | 40.49% |
| Grok native search | 90,927 | 4,433 | 48,674 | 1,243 | 97.45% |

Research and native Grok samples were `partial`; both retain their status, gaps,
usage and partial warning (56 stderr bytes). A smaller output does not turn an
incomplete research run into a successful one. The search sample predates
highlights, so its reduction measures metadata projection, not the size of a
new snippet-bearing search. Five samples demonstrate the issue; they do not
establish an average saving across all queries.

The replay asserts that all saved `results`, answer, attempts, usage, queries,
citations, retrieval events, raw, routing, warnings and gaps survive `--full`.
Compact must preserve answer, status, usage and the source ID/URL list.

```sh
node scripts/measure-output.mjs /path/to/old-soseek /path/to/new-soseek \
  /private/output-dir /private/old-doctor.json /private/saved-result.json
# RESULT_JSON can be either a Result or an acceptance artifact's {result: Result}.
# Multiple result files are supported. Keep private queries/evidence out of Git.
uv run --with tiktoken python -c 'import sys,tiktoken; print(len(tiktoken.get_encoding("o200k_base").encode(open(sys.argv[1]).read())))' /private/output-dir/search-after.json
```

## Default delivery contract

- Compact preserves full answer, every source stub, query coverage, provenance
  provider names, warnings/gaps, full usage and failed/unknown/unsettled attempts.
  Original channel/rank, successful attempts and citation offsets are in `--full`.
- Snippets: 500 Unicode code points per source. Research/answer page excerpts:
  1,200. Read page content: 12,000. Search can use a stored-content excerpt when
  the provider supplied content but no snippet; `snippet_from: content` labels it.
- `--fields` projects the original saved result, not a previously clipped view.
  Source aliases `title,url,published_at` work; `results.title` remains valid.
- `--max-chars` limits displayed answer and source text. It also removes raw
  payloads and reduces citations to links, without leaving offsets attached to
  shortened text. It is **not** a hard limit on total JSON or token count.
- `output_truncated` identifies CLI omission; `source_content_truncated`
  identifies an already incomplete saved source. `--full` only restores saved
  data. It does not magically recover an upstream page that was never stored.
- `runs source RUN_ID SOURCE_ID --offset 0 --max-chars 4000 --json` reads stored
  content or a stored snippet. Follow `next_offset` until absent. Limits are
  Unicode code points, up to 20,000 per window. Authentication, principal
  isolation and expiry apply; no paid upstream calls are made.

## Exa excerpt choice and cost boundary

Standard search adds `contents.highlights` with the actual query and a 500
character limit. This uses the original search request; soseek adds no second
page fetch or Grok call. Explicit `contents` is preserved, and native
`call exa.search` stays under the caller's control. Missing highlights produce
a warning rather than fabricated snippets, implicit reads, or silent retries.
The changed request options participate in cache identity.

Exa's [highlights documentation](https://exa.ai/docs/search/highlights),
[OpenAPI](https://exa.ai/docs/exa-spec.yaml), and
[pricing](https://exa.ai/docs/admin/pricing) document these parameters and
currently include text/highlights in standard search pricing. Exa internally
uses extraction models. Third-party proxy pricing may differ: configured search
cost ceilings, reservations and upstream-reported amounts remain authoritative
for this service's accounting, with the vendor bill needed for final settlement.

Output projection reduces the calling agent's context. It does not reduce
already executed upstream model tokens or charges. It also does not yet reduce
the full capability/result JSON transported to the CLI; source-window reads do
bound their own network response. No claim of end-to-end billing savings or
unchanged research quality follows from these size measurements alone.
