# EventMesh progress

The specification is [PROMPT.md](PROMPT.md) (replaced 2026-11-12 with the Windows-native / Memurai / Tailwind v4 /
milestone-11 version). The project root is `C:\Users\Testing\Desktop\eventmesh\eventmesh` (a nested folder).

## Status (checkpoint 2026-11-12)

Machine: Windows 11, Go 1.27.4, Node 26.13.1, Git 2.56, Memurai (Redis 7.2.5) running, PostgreSQL 18 service running
(installed under `E:\Program Files\PostgreSQL\18`). **No C compiler (`gcc`) is installed, so `-race` has not been run.**

| # | Milestone | Code | Gate |
| --- | --- | --- | --- |
| 1 | Skeleton | done except CI file, `.golangci.yml`, `docs/windows-setup.md` | **partly passed**: Lua `EVAL` with `redis.call` + `TIME` passed on Memurai (`go run ./cmd/redischeck`); `scripts\check-prereqs.ps1` fails only on PostgreSQL (role/database missing); migrations up/down/up **not run** (needs PostgreSQL) |
| 2 | Accounts | code written earlier (auth, sessions, CSRF, API keys, audit), unit tests pass | integration tests **not written / not run** |
| 3 | Endpoints and ingestion | code written earlier (service, SSRF, idempotency) | integration tests **not written / not run** |
| 4 | Delivery engine | queue, worker pool, `process`/`record`, receiver written | end-to-end `Invoke-RestMethod` demo **not run** (needs PostgreSQL) |
| 5 | Reliability | scheduler, reaper, reconciler, rate limit, graceful drain written | Redis-only tests **passed** (below); PostgreSQL-backed tests (retry, dead letter, replay, crash recovery, Redis loss, no double processing) **not written / not run** |
| 6-10 | Read API/SSE, frontend, e2e, observability/load test, docs | routes + SSE hub exist; no frontend, seed, loadtest, OpenAPI, docs | not started |
| 11 | AI features | not started (optional) | not run |

Commands that produced results this session:

- `go build ./...` and `go vet ./...` in `backend`: clean.
- `go test -count=1 ./internal/...`: all packages pass (config, auth, apikeys, retry, ssrf, webhook, endpoints, events, deliveries).
- `EM_TEST_REDIS_URL=redis://127.0.0.1:6379/1 go test -count=1 ./tests/` on Memurai: pass (claim atomicity, 300 jobs / 16
  claimers each claimed once, delayed promotion, lease reaping, stale-delayed cleanup, rate limiter under 50 goroutines).
- `go run ./cmd/redischeck`: PONG + Lua OK on Memurai 7.2.5 (redis_version).
- `scripts\check-prereqs.ps1`: everything passes except PostgreSQL.

## Next action

Run `scripts\init-db.ps1` once (psql prompts for the `postgres` password; it creates role/database `eventmesh`),
then `scripts\migrate.ps1`, then continue with the PostgreSQL integration harness in `backend/tests`
(database-per-run, migrations up/down/up) and the milestone 3-5 integration tests. The previous session could not
connect to PostgreSQL because the `postgres` password is not available to the assistant.

## Open problems

- PostgreSQL role/database `eventmesh` does not exist: `cannot connect to EM_DATABASE_URL: failed to connect to user=eventmesh`.
- Nothing PostgreSQL-backed has run, so the sqlc-generated queries in `internal/database/db` (hand-maintained until
  `sqlc` is installed by `scripts\setup.ps1`), the migration `000001_init`, and every handler are unverified against a real database.
- `-race` not run (no `gcc`). Install MSYS2 or TDM-GCC, then `scripts\test.ps1` picks it up automatically.
- `cmd/seed`, `cmd/loadtest`, frontend, OpenAPI/Swagger, `.github/workflows/ci.yml`, `.golangci.yml`, `docker-compose.yml`,
  README and docs other than this file and `design-decisions.md` are not written.
- Port 8080 is taken by an unrelated `httpd`; dev API port is 8090 (ADR-003).

## Decisions made this session

See `docs/design-decisions.md` ADR-001 to ADR-005 (key prefix, `EM_` variables, port 8090, receiver switch modes, fencing order).
Also: a stray NUL byte in `internal/webhook/client.go` (`Excerpt`) was replaced by the `\x00` escape so the file compiles.

## Files that matter now

- `backend/internal/workers/pool.go`, `backend/internal/workers/process.go` (delivery engine)
- `backend/internal/queue/queue.go`, `backend/internal/ratelimit/ratelimit.go`
- `backend/internal/httpapi/router.go`, `backend/cmd/{api,worker,receiver,migrate,redischeck,dbcheck}/main.go`
- `backend/internal/config/config.go`, `.env.example`
- `backend/migrations/000001_init.up.sql`, `backend/sql/*.sql`, `backend/internal/database/db/*.go`
- `backend/tests/helpers_test.go`, `backend/tests/queue_test.go`
- `scripts/*.ps1`
