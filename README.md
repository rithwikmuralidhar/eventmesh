# EventMesh

A self-hosted webhook delivery and event-processing platform. You send an event through an API; EventMesh delivers
it asynchronously to every subscribed endpoint, signs each request, retries failures with backoff, moves exhausted
deliveries to a dead-letter state you can inspect and replay, and shows everything live in a dashboard.

**Delivery guarantee: at-least-once.** An endpoint can receive the same delivery twice (for example after a worker
crash). Consumers deduplicate on the `X-EventMesh-Delivery-Id` header. Exactly-once is impossible here; see
[docs/reliability.md](docs/reliability.md).

## Architecture

```
API client --(API key)--> api --> PostgreSQL: event + deliveries in one transaction
                           |
                           +--> Redis/Memurai ready queue (best effort, after commit)

worker: claim job (Redis) -> claim row (PostgreSQL) -> rate limit -> signed POST
        -> record attempt + state (PostgreSQL) -> ack/reschedule (Redis) -> publish update (pub/sub)

api: pub/sub -> Server-Sent Events -> dashboard
```

PostgreSQL is the only source of truth. Redis holds dispatch state that the worker's reconciler can rebuild from
PostgreSQL. Details: [architecture](docs/architecture.md), [database](docs/database.md), [reliability](docs/reliability.md),
[security](docs/security.md), [decisions](docs/design-decisions.md), [performance](docs/performance.md).
API reference: [docs/openapi.yaml](docs/openapi.yaml), served at `/docs`.

## Windows setup (native, no WSL, no Docker)

Install PostgreSQL 16+ (as a service), Memurai Developer (Redis-compatible, as a service), Go, Node.js LTS and Git.
For the race detector (`go test -race`) a C toolchain is also required (MSYS2 `gcc` or TDM-GCC). Full steps and common
errors: [docs/windows-setup.md](docs/windows-setup.md).

Windows Defender Firewall will ask for permission the first time the API, worker and receiver listen on a port.
They bind to `127.0.0.1` only, so "Cancel"/private-network-only is sufficient.

## Quick start (PowerShell)

```powershell
scripts\setup.ps1             # dependencies, sqlc, Playwright browser, creates .env from .env.example
scripts\init-db.ps1           # once: creates the eventmesh role and database (prompts for the postgres password)
scripts\check-prereqs.ps1     # every line should say PASS
scripts\migrate.ps1
scripts\seed.ps1              # demo account, demo endpoints and ~40 really delivered events
scripts\start-stack.ps1       # receiver, worker, API (http://127.0.0.1:8090) and Vite dev UI (http://127.0.0.1:3000)
```

Sign in at <http://127.0.0.1:3000> (dev UI) with the demo account: **demo@eventmesh.local** / **eventmesh-demo-1**.
To have the API serve the built UI on one origin instead: `cd frontend; npm run build`, set `EM_SERVE_FRONTEND=true`.
Stop everything with `scripts\stop-stack.ps1`. The API listens on port **8090** by default in `.env.example` because
8080 is often taken on developer machines; change `EM_HTTP_ADDR` if you prefer 8080.

## Five-minute demo

```powershell
$api = 'http://127.0.0.1:8090/api/v1'
$key = (scripts\demo-key.ps1 | Select-Object -Last 1).Trim()    # new API key, shown once

# 1. Send an event: returns 202 immediately, delivery happens asynchronously.
Invoke-RestMethod -Method Post "$api/events" -Headers @{ Authorization = "Bearer $key"; 'Idempotency-Key' = 'demo-1' } `
  -ContentType 'application/json' -Body '{"event_type":"order.created","payload":{"order_id":"o-1"}}'

# 2. Send the same idempotency key again: same event comes back (HTTP 200, Idempotent-Replayed: true), no new deliveries.
Invoke-WebRequest -Method Post "$api/events" -Headers @{ Authorization = "Bearer $key"; 'Idempotency-Key' = 'demo-1' } `
  -ContentType 'application/json' -Body '{"event_type":"order.created","payload":{"order_id":"o-1"}}' | Select-Object StatusCode

# 3. Watch what the receiver got (each request shows whether the signature verified).
Invoke-RestMethod http://127.0.0.1:9000/control/requests | Select-Object -Last 5 hook, attempt, status_returned, signature_verified

# 4. In the dashboard: Deliveries -> "Always failing (500)" -> open a dead-lettered delivery -> Replay.
# 5. Break and fix a receiver hook at run time:
Invoke-RestMethod -Method Put 'http://127.0.0.1:9000/control/switch/demo?mode=fail'
Invoke-RestMethod -Method Put 'http://127.0.0.1:9000/control/switch/demo?mode=ok'
```

Receiver hooks: `/hooks/ok`, `/hooks/fail`, `/hooks/client-error`, `/hooks/slow?ms=3000`, `/hooks/rate-limited`,
`/hooks/flaky?fail_first=2`, `/hooks/drop`, `/hooks/switch/<name>`.

## Tests

| Suite | Command |
| --- | --- |
| Go unit tests (`-race` if `gcc` is installed) | `scripts\test.ps1` |
| Go integration tests (real PostgreSQL + Redis; own database and Redis key prefix per run) | `scripts\test-integration.ps1` |
| Frontend unit tests | `cd frontend; npm test -- --run` |
| End to end (Playwright, Chromium, starts and stops the stack) | `scripts\test-e2e.ps1` |
| Lint | `scripts\lint.ps1` |
| Load test and experiments | `scripts\loadtest.ps1 -events 1000` / `scripts\run-experiments.ps1` |

Integration tests need `EM_TEST_DATABASE_URL` (a role with `CREATEDB`) and `EM_TEST_REDIS_URL`; they fail loudly if
either is missing.

## Configuration

Every setting is an `EM_*` environment variable, read once at startup into one typed struct
(`backend/internal/config`). [.env.example](.env.example) lists all of them with a comment each. With
`EM_ENV=production` the API refuses to start with the public development encryption key, a non-empty
`EM_SSRF_ALLOWED_HOSTS`, or the default database password.

## Docker (optional)

The native scripts above are the supported development path. To run the whole stack in containers instead:

```
docker compose -f docker-compose.app.yml up --build -d
```

That starts PostgreSQL (with pgvector), Redis, a one-shot migration, the API with the built UI on
<http://127.0.0.1:8090>, a worker and the demo receiver. Register an account in the UI, or load the demo data with
`docker compose -f docker-compose.app.yml --profile seed run --rm seed`. Stop and delete the data with
`docker compose -f docker-compose.app.yml down -v`. Development settings only (default passwords, public dev key).
`docker-compose.yml` is separate and only starts PostgreSQL and Redis for the native workflow.


## Self-hosting (public, free)

[docs/self-hosting.md](docs/self-hosting.md) puts the stack on the internet from a Linux PC with HTTPS, using a
Tailscale Funnel tunnel (no port forwarding). `docker-compose.prod.yml` adds production settings on top of
`docker-compose.app.yml`: random secrets in a git-ignored `.env.prod`, `EM_ENV=production`, closed registration,
trusted-proxy client IPs, read-only unprivileged containers, no demo receiver. `scripts/prod.sh` wraps init, start,
logs and backups; the CI `docker-prod` job runs it and `scripts/docker-prod-smoke.sh`.

## Observability

Structured JSON logs with a request ID (and a trace ID when tracing is on), Prometheus metrics on `:9090` (API) and
`:9091` (worker), and OpenTelemetry traces: set `EM_TRACE_EXPORTER=stdout` to print spans, or `otlp` to send them to
a collector at `EM_TRACE_OTLP_ENDPOINT`. One event is one trace across the API request, the queue and every delivery
attempt. Details: [docs/architecture.md](docs/architecture.md).

## Optional AI features (off by default)

Docs Q&A, dead-letter explanations and natural-language event search use the Gemini API. They need
`EM_AI_PROVIDER=gemini`, `EM_AI_API_KEY` and a per-feature `EM_FEATURE_AI_*` flag; with any of those missing the endpoints
answer `503` and the UI hides the controls. What each feature sends is in [docs/security.md](docs/security.md); setup
(including pgvector for docs Q&A) is in [docs/windows-setup.md](docs/windows-setup.md).

## Known limitations

No ordering guarantee between deliveries. Events, attempts and idempotency keys are kept forever (no retention job).
No circuit breaker for persistently failing endpoints. Memurai is a development server; CI runs against Linux Redis.
Application-level SSRF protection is not a substitute for network egress controls. `docker-compose.yml` is an optional
convenience (PostgreSQL and Redis only); nothing depends on Docker.
