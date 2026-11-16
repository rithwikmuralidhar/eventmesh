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

## ADR-027: Why not Kafka, RabbitMQ or SQS for the queue (addendum to ADR-008)
ADR-008 compared a library and `SKIP LOCKED`; it never wrote down the brokers. The deciding fact is ADR-007: PostgreSQL already holds every delivery durably, and the queue only carries delivery IDs that the reconciler can rebuild. So the queue needs speed, delayed delivery and a visibility timeout, but **not** durability, which is the main thing a broker sells.

| Option | What it would give | Why not here |
| --- | --- | --- |
| RabbitMQ | Acks, redelivery, dead-letter exchanges, delayed messages (plugin) out of the box | A second durable system to keep consistent with PostgreSQL (the dual-write problem again), and its DLQ would duplicate the `dead_letter` state the dashboard reads from SQL. The strongest alternative; choose it when you do not want to own queue code |
| Kafka | A replayable, partitioned log; many independent consumers; very high throughput | A log is consumed in order per partition: one slow or failing endpoint blocks the partition behind it, and per-message retry-with-delay needs retry topics. Built for streams, awkward for a work queue with individual retries |
| SQS | Managed, visibility timeout, DLQ | Cloud-only, max delay 15 minutes, and the project must run on one Windows machine |
| PostgreSQL `SKIP LOCKED` only | No Redis at all; one store, no dual write | Polls the source of truth; workable at this scale and the honest simplest design. Redis was kept because it is also the rate limiter and the pub/sub bus |

Trade-off accepted: about 150 lines of Lua and Go that a broker would provide, in exchange for one fewer moving part and a queue that can be thrown away. Revisit if volume outgrows one PostgreSQL or if other teams need to consume the event stream (then Kafka, fed by an outbox table).

## ADR-028: Dashboard statistics use a TTL cache, not invalidation
`GET /dashboard/stats` runs three aggregate queries per request. They are cached in Redis for `EM_STATS_CACHE_TTL` (5 s). Invalidate-on-write was rejected: the inputs change on every delivery attempt, so under load the entry would be deleted thousands of times a second and nothing would ever be served from cache. A TTL bounds staleness to 5 s, which a dashboard tolerates; the cost is that the Overview tiles can lag the live delivery list by up to 5 s. The key includes the user ID (tenant isolation), the range and the window start. Concurrent misses share one computation (`singleflight`) so an expiring entry does not stampede the database. Redis errors are treated as misses. Not cached: lists and detail pages, which must be current and are already index-served in well under a millisecond.

## ADR-029: Trace context travels through PostgreSQL, not Redis
The queue carries only delivery IDs, so the worker has no link to the request that created an event. The W3C `traceparent` of the ingestion span is stored in `events.trace_context` (migration 000003) and returned by `ClaimDelivery`, so every delivery attempt, including a retry hours later or a replay, joins the original trace. Putting it in the Redis job was rejected: the reconciler rebuilds jobs from PostgreSQL and would lose it. Tracing is off by default (`EM_TRACE_EXPORTER=none` installs no provider, so spans are no-ops). Span attributes include event, delivery and endpoint IDs, which are deliberately *not* metric labels: a trace is one record, a metric label value is a time series. Spans never carry payloads, secrets, or the path and query of a customer URL. The outgoing webhook does not carry `traceparent`; adding it would let consumers continue the trace and is a possible extension.

## ADR-030: Docker is offered alongside the native scripts (amends ADR-016)
ADR-016 chose native Windows scripts so nothing depends on Docker. That stays the supported development path. A multi-stage `Dockerfile` (static Go binaries plus the built frontend on Alpine, non-root user) and `docker-compose.app.yml` (PostgreSQL with pgvector, Redis, a one-shot migration, API, worker, receiver) now provide "the same app on a laptop and in CI". It is built and smoke-tested by the `docker` CI job, not on the development machine, whose Docker engine was not usable. One image serves every process; compose selects the command.

## ADR-031: Several dispatchers are allowed; one stays the default
The worker pool had one goroutine claiming jobs from Redis. The drain benchmark (docs/performance.md) measured about +15 to 20% throughput with four at 25 workers, so `EM_DISPATCHER_COUNT` exists. Safety does not depend on there being one: each dispatcher takes an idle-worker token before claiming, so together they cannot claim more jobs than there are free workers, and the PostgreSQL row claim remains the arbiter. The default stays 1 because the gain was measured only at 25 workers on one machine, and more dispatchers means more Redis polling when the queue is empty.
