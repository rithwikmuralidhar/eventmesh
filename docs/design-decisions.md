# Design decisions

Short ADRs. The full list required by the specification is still to be written (milestone 10); only decisions
taken so far are recorded here.

## ADR-001: Redis key namespace via `EM_REDIS_KEY_PREFIX`
Context: integration tests must not collide with each other or with the dev stack on one Memurai instance.
Decision: queue keys, rate-limit buckets and pub/sub channels are all `<prefix>:...`, built from one config value
(`queue.New`, `ratelimit.New`, `realtime.NewRedisPublisher/NewHub` take the prefix). Tests use `emtest-<random>`.
Trade-off: the prefix is a constructor argument everywhere instead of a package constant.

## ADR-002: Config variables use the `EM_` prefix
`config.Load` reads every variable as `EM_<NAME>`; tests pass an unprefixed map through a helper. Cookie `Secure`
is derived from `EM_ENV=production` (no separate switch), as the specification says.

## ADR-003: Development API port is 8090, not 8080
Context: on the build machine port 8080 is held by an unrelated Apache `httpd`. Decision: `.env.example` sets
`EM_HTTP_ADDR=127.0.0.1:8090`; the code default stays `127.0.0.1:8080`. Everything else uses the configured value.
Deviation from the specification's port table; reversible by editing `.env`.

## ADR-004: Receiver switch modes
`PUT /control/switch/:name?mode=<ok|fail|client-error|rate-limited|drop>` (a JSON body `{"mode": ...}` also works).
The specification names the endpoint but not the mode vocabulary.

## ADR-005: Worker records the outcome with fencing, then touches Redis
`workers.Pool.record` writes the attempt row and the new delivery state in one transaction guarded by
`locked_by = worker`; only after commit does it ack or schedule in Redis. If Redis fails after commit the
reconciler repairs it; if PostgreSQL fails the lease expires and the delivery is sent again (at-least-once).
## ADR-006: Modular monolith over microservices
Context: one developer, one machine, interview-defensible. Alternatives: separate ingestion/delivery services. Decision: one Go
module, several binaries (api, worker, receiver, tools) sharing packages. Trade-off: no independent deploy cadence; the process
split (api vs worker) already allows scaling workers separately.

## ADR-007: PostgreSQL is the only source of truth
Redis can lose data (restart, flush); PostgreSQL commits are durable. Redis holds dispatch state that the reconciler rebuilds
from PostgreSQL. Trade-off: a reconciler loop and a grace-period delay after a lost enqueue.

## ADR-008: Hand-built Redis queue instead of a library or `FOR UPDATE SKIP LOCKED`
Alternatives: asynq (hides the mechanics), SKIP LOCKED polling (simple, but polls the source of truth). Decision: Lua scripts on
lists and sorted sets (ready/delayed/processing) with a PostgreSQL row claim as the arbiter. Trade-off: two stores to keep
consistent; benefit: I can explain every line.

## ADR-009: At-least-once delivery
Exactly-once is impossible without endpoint cooperation (reliability.md). Consumers dedupe on the stable delivery ID.

## ADR-010: Bounded worker pool
A dispatcher feeds `EM_WORKER_COUNT` workers through an unbuffered channel and a token channel; never a goroutine per
delivery. Bounds DB connections and outgoing sockets; the pgx pool is sized `workers + 4`, the HTTP transport `MaxConnsPerHost = workers`.

## ADR-011: sqlc over an ORM
SQL stays visible and reviewable, results are typed, and `user_id` conditions live in the queries themselves. Trade-off: generated
code to keep in sync (CI diff check); the generated files in the repository were produced before `sqlc` was installed on the dev machine.

## ADR-012: SSE over WebSockets
Updates are one-directional, `EventSource` reconnects natively and uses the session cookie. The server drops slow clients.

## ADR-013: Server-side sessions over JWT
Instant revocation, cookie works with `EventSource`; CSRF is handled with a custom header and origin check.

## ADR-014: Token bucket for rate limiting
One Lua script serves three limits and allows bursts. Alternative: sliding window (smoother, more memory/compute). Redis `TIME` avoids
clock skew between workers. API limits fail open; the outgoing limit does not.

## ADR-015: Replay resets the delivery instead of creating a new one
Keeps `(event_id, endpoint_id)` unique and the delivery ID stable for consumer deduplication; attempt history is append-only.

## ADR-016: Native Windows scripts instead of Make and Docker
The target is a native Windows machine without WSL; PowerShell scripts with a shared `.env` loader are enough. Docker Compose is optional.

## ADR-017: Memurai locally, Redis in CI
Memurai is the Windows-native Redis-compatible server; CI uses Linux Redis service containers. Only commands common to both are used
and `cmd/redischeck` verifies the Lua features on Memurai. Risk: Memurai-specific behaviour is not covered by CI.

## ADR-018: The API serves the built frontend
With `EM_SERVE_FRONTEND=true` one origin serves UI and API (no nginx). This exposed a real bug: the page's own origin must pass the
CSRF origin check (`middleware.sameOrigin`).

## ADR-019: Seed-only secret chosen by the operator
`endpoints.Service.CreateWithSecret` exists so seeded demo endpoints share `EM_RECEIVER_SECRET` with the receiver. It is not exposed over HTTP.
The receiver can also register a secret at run time (`PUT /control/secret/:name`) for the end-to-end test.

## ADR-020: Milestone 11 (AI features) is built, optional and off by default
Code lives in `backend/internal/ai`, wired in only by `internal/httpapi`; no delivery, queue, retry, auth or rate-limit
package imports it. With the key unset or the flags false, no provider client exists. Details: `docs/security.md`.

## ADR-021: Gemini models and SDK (checked 2026-11-12)
SDK `google.golang.org/genai` v1.77.0. Chat model `gemini-3.6-flash` (listed as stable Flash on Google's models page).
Embedding model `gemini-embedding-001`: text-only, with a task type (`RETRIEVAL_DOCUMENT` / `RETRIEVAL_QUERY`). The newer
`gemini-embedding-2` has no task-type field and uses prompt prefixes, so the task-type API was chosen for a plainer
asymmetric-retrieval setup. Embeddings are truncated to 768 dimensions (`EM_AI_EMBED_DIMENSIONS`; the default output is
3072) and re-normalized, which fits a pgvector HNSW index (limit 2000 dimensions). Free-tier rate limits were not on the
fetched pages, so `EM_AI_REQUESTS_PER_MINUTE=10` is the spec default, not a measured limit. Names are configuration.

## ADR-022: The pgvector table is created at runtime, not by the core migrations
The spec has the migration run `CREATE EXTENSION vector`. That would make `migrate` fail on any machine without pgvector and
break the rule that the core works with AI off. `ai.EnsureDocsSchema` creates the extension and `doc_chunks` when the docs
feature is enabled. `doc_chunks` also stores `start_line`/`end_line` (needed for the citation line range) and has
`UNIQUE (source_path, content_hash)`, which makes re-indexing idempotent. The explanation cache is an ordinary table in
migration `000002`.

## ADR-023: Search returns filters; the events page runs them
`POST /ai/search/events` returns validated filters only. The UI shows them as removable chips and, on "Apply", loads results
through `GET /events`, which uses `events.Service.List`. This keeps "results only after the user confirms" without the AI
endpoint touching event data. `GET /events` gained an `endpoint_id` filter (events with a delivery to that endpoint) so the
search schema's endpoint field has something to drive. The event type is an exact match, not a pattern, because that is what
the existing list supports.

## ADR-024: Explanation cache is keyed by delivery and invalidated by change
One row per delivery, valid 24 hours, and only if generated after the delivery's last update (so a replay that dead-letters
again is not served a stale answer) and with the same "include excerpt" choice.

## ADR-025: "Not found" similarity threshold is 0.68
The spec's first guess (0.55) gave 0% correct "not found" on a first measurement with `gemini-embedding-001`. 0.68 is the midpoint of the gap between the in-docs and not-in-docs score ranges on a 24-question tuning set, then checked once on a separate 10-question hold-out set (75% correct "not found", one false pass at 0.688). The gap is only about 0.01 wide, so it is configurable (`EM_AI_MIN_SIMILARITY`) and must be re-measured if the embedding model or the documents change. Numbers and caveats: `docs/ai-evaluation.md`.

## ADR-026: Meta documents are not indexed for docs Q&A
`docs/PROMPT.md` (the build specification), `docs/PROGRESS.md` and `docs/ai-evaluation*.md` are excluded from the index. The first two are not product documentation and surfaced as cited sources for user questions; the evaluation reports quote the evaluation questions verbatim, so indexing them would let the "not in the docs" questions match their own write-up and invalidate the measurement. Chunks of a file that becomes excluded are deleted on the next indexing run. This changes the indexed corpus after the first evaluation, so that evaluation was rerun (see `docs/ai-evaluation.md`).
