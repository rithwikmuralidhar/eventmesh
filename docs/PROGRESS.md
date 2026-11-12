# EventMesh progress

The specification is [PROMPT.md](PROMPT.md) (replaced 2026-11-12 with the Windows-native / Memurai / Tailwind v4 /
milestone-11 version). The project root is `C:\Users\Testing\Desktop\eventmesh\eventmesh` (a nested folder).

## Status (checkpoint 2026-11-12, second session)

Machine: Windows 11, Go 1.27.4, Node 26.13.1, Git 2.56, Memurai (Redis 7.2.5), PostgreSQL 18.7 (under `E:\Program Files\PostgreSQL\18`).
Role/database `eventmesh` created with `scripts\init-db.ps1` (dev password `eventmesh`). **No `gcc`, so `-race` has not been run.**

| # | Milestone | Gate |
| --- | --- | --- |
| 1 | Skeleton | **passed**: `scripts\check-prereqs.ps1` all PASS; `scripts\migrate.ps1`, `-Down`, again: ok; Lua EVAL with `redis.call`+`TIME` ok on Memurai. Still missing: `.github/workflows/ci.yml`, `.golangci.yml` |
| 2 | Accounts | **passed** (integration): sessions/CSRF/logout, same error for unknown email vs wrong password, API key valid/missing/malformed/wrong scheme/revoked |
| 3 | Endpoints + ingestion | **passed** (integration): idempotency table, 50 concurrent requests -> 1 event, non-matching event -> 0 deliveries, private/metadata destinations rejected |
| 4 | Delivery engine | **passed live**: seed + `scripts\start-stack.ps1 -NoFrontend`, `Invoke-RestMethod` event -> 3 deliveries; receiver verified 237/237 signatures; flaky hook failed twice then 200 |
| 5 | Reliability | **passed except `-race`**: retries -> dead letter -> replay (attempts kept, trigger=replay), 4xx straight to dead letter, 4 workers x 150 deliveries each sent exactly once, crashed worker recovered after lease, full Redis key loss recovered by reconciler, enqueue failure recovered, rate-limited deferral records no attempt, disabled endpoint -> cancelled, goleak after graceful stop |
| 6 | Read API + SSE | routes and hub exist; user-B-gets-404 test passes for endpoints/events/deliveries/api-keys; **SSE live check, OpenAPI/Swagger, audit-log authorization test not done** |
| 7-10 | Frontend, e2e, metrics/loadtest, docs | not started (`cmd/loadtest`, `frontend/`, `docs/` other than this file and `design-decisions.md`, README) |
| 11 | AI features | not started (optional) |

Commands that produced results:

- `go build ./...`, `go vet ./...` (backend): clean. `go test -count=1 ./internal/...`: pass.
- `$env:EM_TEST_DATABASE_URL='postgres://eventmesh:eventmesh@127.0.0.1:5432/postgres?sslmode=disable'; $env:EM_TEST_REDIS_URL='redis://127.0.0.1:6379/1'; go test -count=1 -v ./tests` (in `backend`): all 25 tests pass (~12 s).
- `scripts\stop-stack.ps1` after the live demo: no `api`/`worker`/`receiver` processes left, `.run\` empty.

## Next action

Milestone 6 remainder: add an SSE live-update check, an audit-log authorization test, OpenAPI (`docs/openapi.yaml`) with embedded Swagger UI at `/docs`;
then start the frontend (milestone 7: Vite + Tailwind v4 `@tailwindcss/vite`, all pages in section 10).

## Open problems

- `-race` not run (install MSYS2/TDM-GCC; `scripts\test.ps1` and `test-integration.ps1` pick up `gcc` automatically).
- `start-stack.ps1` run from the assistant's tool blocks until the child processes exit (inherited output handles); it works fine from a normal terminal - not yet verified there.
- Not written: `cmd/loadtest`, `.github/workflows/ci.yml`, `.golangci.yml`, `docker-compose.yml`, README, docs (windows-setup, architecture, database, reliability, security, performance, interview-guide, openapi), frontend, Playwright.
- `internal/database/db/*.go` is the sqlc output kept in the repo; `sqlc` is not installed here, so `scripts\sqlc.ps1` / CI diff check unverified.
- Port 8080 is taken by an unrelated `httpd`; dev API port is 8090 (ADR-003). Seed uses `EM_RECEIVER_BASE_URL` default `http://127.0.0.1:9000`.

## Decisions made this session

ADR-001..005 in `docs/design-decisions.md`. New this session: `endpoints.Service.CreateWithSecret` (seed only, not routed) so demo endpoints share
`EM_RECEIVER_SECRET` with the receiver; test harness builds `httpapi.New` directly with an optional deps hook to inject a failing queue.

## Files that matter now

- `backend/internal/httpapi/router.go`, `backend/internal/workers/process.go`, `backend/internal/realtime/realtime.go`
- `backend/tests/stack_test.go` (harness), `backend/tests/reliability_test.go`, `backend/tests/delivery_test.go`, `backend/tests/queue_test.go`
- `backend/cmd/{api,worker,receiver,seed}/main.go`, `scripts/*.ps1`, `.env.example`
- `docs/PROMPT.md` sections 10 (frontend), 6 (API), 13-15 for what's next
