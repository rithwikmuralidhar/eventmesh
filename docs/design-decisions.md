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

## ADR-020: Milestone 11 (AI features) not built
It is optional and runs only after milestone 10; no AI code, tables or dependencies exist. Nothing in `docs/security.md` is sent to any provider.
