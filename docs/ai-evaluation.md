# AI documentation retrieval evaluation

Run on 2026-11-12 with `gemini-embedding-001` (768 dimensions) over this repository's `README.md`, `docs/*.md` and
`docs/openapi.yaml` (221 chunks in the first two runs, 132 after ADR-026), stored in pgvector 0.8.1 on PostgreSQL 18. The chat model (`gemini-3.6-flash`) is
**not** called by the evaluation, so nothing here says anything about answer quality. Each figure below is one run. Raw
output of the second run: [ai-evaluation-holdout.md](ai-evaluation-holdout.md).

## Run 1: tuning set (`backend/eval/docs_questions.json`, 20 answerable + 4 not in the docs)

Threshold in effect: 0.55, the original untuned guess.

| Metric | Value |
| --- | --- |
| recall@5, expected source file among the top 5 | 100% (20 of 20) |
| recall@5, expected source file and heading | 95% (19 of 20) |
| correct "not found" rate | **0%** (0 of 4) |
| mean / p95 retrieval latency | 496 ms / 583 ms |
| best similarity, answerable questions | min 0.699, median 0.763, max 0.806 |
| best similarity, questions not in the docs | min 0.585, median 0.630, max 0.667 |

The 0% shows the 0.55 guess was too low for this model: every question that is not in the docs scored above it. (An
earlier run of the same set measured 476 ms / 687 ms latency; latency varies from run to run.)

## Choosing the threshold

The two score ranges did not overlap (0.667 vs 0.699), so the threshold was set to their midpoint, **0.68**
(`EM_AI_MIN_SIMILARITY`, now the built-in default). That choice used the 24 questions above, so re-scoring that same set at
0.68 would be 100% by construction and is not reported as a result.

## Run 2: hold-out set (`backend/eval/docs_questions_holdout.json`, 6 answerable + 4 not in the docs)

Written before any scores were seen, and not used to choose the threshold. Threshold 0.68.

| Metric | Value |
| --- | --- |
| recall@5, source file | 100% (6 of 6) |
| recall@5, source file and heading | 100% (6 of 6) |
| correct "not found" rate | **75%** (3 of 4) |
| mean / p95 retrieval latency | 502 ms / 719 ms |
| best similarity, answerable questions | min 0.739, median 0.748, max 0.786 |
| best similarity, questions not in the docs | min 0.608, median 0.656, max 0.688 |

The miss: "Does EventMesh support GraphQL subscriptions?" scored 0.688, just over 0.68, so the chat model would be called
for it. The threshold was **not** adjusted afterwards, because that would make the hold-out set a second tuning set.

## Re-run after excluding meta documents (ADR-026)

The first two runs were made over 221 chunks that included `docs/PROMPT.md` (the build specification) and
`docs/PROGRESS.md`. Those were then excluded (along with `ai-evaluation*.md`, which quote the evaluation questions), which
left **132 chunks**, and both question sets were rerun with the threshold unchanged at 0.68. Raw reports:
[ai-evaluation-run-main.md](ai-evaluation-run-main.md) and [ai-evaluation-run-holdout.md](ai-evaluation-run-holdout.md).

| Set | recall@5 (file / file + heading) | correct "not found" | mean / p95 latency | best similarity, in docs | best similarity, not in docs |
| --- | --- | --- | --- | --- | --- |
| Tuning (20 + 4) | 100% / 95% | 100% (4 of 4, but tuned on this set) | 468 ms / 543 ms | 0.699 to 0.806 | 0.579 to 0.667 |
| Hold-out (6 + 4) | 100% / 100% | **75% (3 of 4)** | 459 ms / 550 ms | 0.739 to 0.786 | 0.608 to 0.688 |

Compared with the 221-chunk runs: the recall and every in-docs score range are unchanged, the hold-out ranges are
identical, and the same question ("Does EventMesh support GraphQL subscriptions?", 0.688) still passes the threshold. The
tuning-set "not found" rate went from 0% (at the old 0.55 threshold) to 100% only because the threshold was chosen on that
set; the hold-out result is the one to read. The numbers in the two tables above this section describe the 221-chunk
index and are kept for the record.

## What this does and does not show

- Retrieval found the expected source file in the top 5 for all 26 answerable questions, and the expected heading for 25.
- Across all 34 questions the ranges are 0.699 to 0.806 (in the docs) and 0.585 to 0.688 (not in the docs). The gap is
  about 0.01 wide, so the threshold is fragile: a differently phrased question could land on the wrong side. Ten
  questions of each kind is a small sample, and the true false-pass rate could be well above or below 25%.
- A false pass is a soft failure: the prompt tells the model to reply "Not found in the docs." when the excerpts do not
  answer, and the API then drops the citations. Whether the model obeys was not measured as a rate. One real observation (2026-11-12, `gemini-3.5-flash-lite`, in the UI): a mistyped question ("retrieved" for "retried") passed the 0.68 threshold, reached the chat model, and was answered "Not found in the docs." with no sources. A correctly worded question ("How are failed deliveries retried?") got an answer that matched `docs/reliability.md`.
- Scores depend on this embedding model and these documents; a different model or corpus needs its own measurement.
- Latency is the embedding call plus the vector search, from one machine and one network, excluding rate-limit waits.
- The free tier limits embeddings to 100 requests and 30K tokens per minute; the first indexing attempt hit that, and
  the indexer now waits and retries.

## Reproducing

```powershell
scripts\index-docs.ps1
scripts\eval-docs.ps1
$env:EM_EVAL_QUESTIONS='eval\docs_questions_holdout.json'; $env:EM_EVAL_OUTPUT='..\docs\ai-evaluation-holdout.md'; scripts\eval-docs.ps1
```

Paths are relative to the `backend` folder. Without `EM_EVAL_OUTPUT`, `scripts\eval-docs.ps1` overwrites this file with a
raw report, so copy this write-up first if you rerun the main set.
