# AI documentation retrieval evaluation

Run on 2026-11-12.

- Embedding model: `gemini-embedding-001` (768 dimensions)
- Chat model configured: `gemini-3.5-flash-lite` (not called by this evaluation)
- Similarity threshold for "not found": 0.68 (cosine, `EM_AI_MIN_SIMILARITY`)
- Questions: 6 answerable, 4 not in the docs

| Metric | Value |
| --- | --- |
| recall@5 (expected source file among top 5) | 100% |
| recall@5 (expected source and heading) | 100% |
| correct "not found" rate | 75% |
| mean retrieval latency (embed question + vector search) | 459 ms |
| p95 retrieval latency | 550 ms |

- best similarity, answerable questions: min 0.739, median 0.748, max 0.786 (n=6)
- best similarity, questions not in the docs: min 0.608, median 0.656, max 0.688 (n=4)

Questions not in the docs that still cleared the threshold:

- Does EventMesh support GraphQL subscriptions?

What this shows: retrieval quality on this fixed question set over this repository's own docs, one run. It does not measure answer quality (the chat model is not called), does not generalize to other documents, and a single run with ~20 questions cannot support fine comparisons.
