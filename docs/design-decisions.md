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