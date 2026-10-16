# First live search baseline — 2026-10-17

The full 24-question, three-variant evaluation completed through the real
**CLI → HTTPS cloud service → configured upstream providers** path. All 72 CLI
tasks succeeded. This run establishes an operational and source-location
baseline; it does not establish factual accuracy or that fusion is best.

The experiment ran from **00:44:43 to 00:50:16 Asia/Taipei on 2026-10-17**
(16:44:43–16:50:16 UTC on 2026-10-16). It used the public
[24-question set](questions.json), covering six categories with four questions
each. Per question, the order was Exa, Tavily, then Exa + Tavily merged; each task
requested five returned results, a USD 0.20 run ceiling, and a 60-second server
deadline. The runner did not retry any request or change account limits.

**Serper was excluded before this run** because an earlier live request returned
upstream HTTP 429. No Serper request or retry was made by this evaluation. These
results therefore compare three variants, not four.

## Results

| Variant | Completed successfully | Expected-domain hits | P50 elapsed | P95 elapsed | Upstream attempts | Reported cache hits |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Exa | 24 / 24 | 22 / 22 | 5.532 s | 10.193 s | 24 | 0 |
| Tavily | 24 / 24 | 18 / 22 | 3.199 s | 4.491 s | 24 | 0 |
| Exa + Tavily merged | 24 / 24 | 22 / 22 | 2.049 s | 6.339 s | 48 | 0 |

There were no partial, failed, cancelled, rate-limited, or budget-rejected tasks
in this run. The 72 tasks produced 96 recorded upstream attempts. Success here
means the service delivered a successful result; it does not certify every
returned page's content or every query's factual answer.

The domain heuristic checks whether at least one returned URL belongs to a
listed expected domain. Two questions deliberately have no such label, leaving
22 evaluated cases per variant. This is neither answer accuracy nor a blinded
relevance score. Failed cases would have remained in the denominator.

Tavily did not return a listed expected domain in these four top-five results:

| Question ID | Query topic | Expected domain(s) |
| --- | --- | --- |
| `tech-01` | Go HTTP client timeout documentation | `pkg.go.dev`, `go.dev` |
| `facts-03` | Unicode UTF-8 encoding standard | `unicode.org` |
| `cn-04` | Git rebase / merge documentation in a Chinese query | `git-scm.com` |
| `current-03` | SQLite release history | `sqlite.org` |

These are domain-check misses, not API failures or proof that the other sources
were wrong. Merging matched Exa's domain coverage in this set; it did not improve
that metric over Exa alone.

## Usage and unresolved charges

| Variant | Upstream-reported USD | Estimated USD | Held USD reserve | Unknown-charge attempts | Tavily credits |
| --- | ---: | ---: | ---: | ---: | ---: |
| Exa | $0.168 | $0.000 | $0.000 | 0 | — |
| Tavily | $0.000 reported; final USD unknown | $0.000 | $1.200 | 24 | 24 |
| Exa + Tavily merged | $0.168, from Exa | $0.000 | $1.200 | 24 | 24 |
| **Total for this evaluation** | **$0.336 reported** | **$0.000** | **$2.400 held** | **48** | **48** |

The known USD amounts were reported by Exa. Tavily returned credits without a
settled USD amount; its 48 calls retain conservative reserves. **The USD 2.400
reserve is not a measured bill, and zero reported Tavily USD does not mean free
usage.** Credits are a separate unit and were not added to USD. The numbers above
sum this experiment's run records, excluding other activity on the same account.

## Timing and cache boundary

The runner allows reuse of preceding same-user search cache entries, but **all
96 attempts in this run were recorded as non-cached**. The merged tasks therefore
show two recorded upstream calls each, rather than a measured soseek cache hit.
The reason no service cache hits occurred was not established by this evaluation.

The provider order was fixed, merged searches repeated the same question after
the single-provider calls, and upstream cache behavior is not observable here.
Connection warming, upstream caching, parallel execution, scheduling, and network
variation can all influence these observations. The lower merged P50 **does not
establish a cold-search speed advantage**. Timings include CLI startup, HTTPS,
server queue/rate-limit waits, and response retrieval. P50/P95 use nearest rank.

## What this does and does not verify

This baseline verifies that the deployed CLI/cloud search path completed the
selected 24 questions for Exa, Tavily, and their combination, with usable result
records, attribution, accounting fields, and no hidden retry by the runner.
The full CLI responses, timings, run IDs, and per-question usage snapshots are
retained privately outside the repository. All 76 artifact files were checked
as mode `0600` inside a private directory; no run ID, identity ID, token, private
endpoint, or raw response is included in this public report.

This run does not evaluate Grok research quality, page-reading accuracy,
semantic citation support, recall against a complete truth set, or Serper. It
also lacks randomized order, cache-neutral repeats, and blinded relevance
judgments. The result does not justify a general claim that more providers yield
better answers, or that one provider is universally preferable.

See [the evaluation procedure](README.md) and
[runner](../scripts/evaluate.mjs) for the reproducible method and stop conditions.
The runner's rate-limit detection and no-retry behavior are separately covered
by [mock tests](evaluate.test.mjs); this live run did not trigger the stop path.
