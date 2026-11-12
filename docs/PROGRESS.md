# EventMesh progress

The specification is [PROMPT.md](PROMPT.md) (replaced 2026-11-12 with the Windows-native / Memurai / Tailwind v4 /
milestone-11 version). The project root is `C:\Users\Testing\Desktop\eventmesh\eventmesh` (a nested folder).

## Status (checkpoint 2026-11-12, third session)

Machine: Windows 11, Go 1.27.4, Node 26.13.1, Git 2.56, Memurai (Redis 7.2.5), PostgreSQL 18.7 (under `E:\Program Files\PostgreSQL\18`).
Role/database `eventmesh` created with `scripts\init-db.ps1` (dev password `eventmesh`). **No `gcc`, so `-race` has not been run.**

| # | Milestone | Gate |
| --- | --- | --- |
| 1 | Skeleton | **passed**: `scripts\check-prereqs.ps1` all PASS; `scripts\migrate.ps1`, `-Down`, again: ok; Lua EVAL with `redis.call`+`TIME` ok on Memurai. Still missing: `.github/workflows/ci.yml`, `.golangci.yml` |
| 2 | Accounts | **passed** (integration): sessions/CSRF/logout, same error for unknown email vs wrong password, API key valid/missing/malformed/wrong scheme/revoked |
| 3 | Endpoints + ingestion | **passed** (integration): idempotency table, 50 concurrent requests -> 1 event, non-matching event -> 0 deliveries, private/metadata destinations rejected |
| 4 | Delivery engine | **passed live**: seed + `scripts\start-stack.ps1 -NoFrontend`, `Invoke-RestMethod` event -> 3 deliveries; receiver verified 237/237 signatures; flaky hook failed twice then 200 |
| 5 | Reliability | **passed except `-race`**: retries -> dead letter -> replay (attempts kept, trigger=replay), 4xx straight to dead letter, 4 workers x 150 deliveries each sent exactly once, crashed worker recovered after lease, full Redis key loss recovered by reconciler, enqueue failure recovered, rate-limited deferral records no attempt, disabled endpoint -> cancelled, goleak after graceful stop |
| 6 | Read API + SSE | **passed**: SSE test (owner receives `event.created` and `delivered`, other user receives nothing), audit-log scoping, 401 on every management route, user-B 404 matrix, same-origin CSRF fix; OpenAPI (`docs/openapi.yaml`, copy embedded and checked by a unit test) with Swagger UI at `/docs` (HTTP 200 verified) |
| 7 | Frontend | **passed**: `npm run build` (tsc + Vite; built CSS 14 KB contains only used utilities), `npx tsc --noEmit` clean, `npx eslint .` clean, `npx vitest run` 10/10; manual walk against the running stack through the API-served build: login, Overview (real data, Live indicator), Endpoints, Events, Deliveries (+filter in URL), delivery detail (explanation, headers, attempt timeline, Replay), API keys, Activity, Settings. Not manually exercised: create/rotate/delete flows, light theme, mobile width |
| 8 | End to end | `start-stack.ps1`/`stop-stack.ps1` work (no orphans); **Playwright flow, `playwright.config.ts`, CI not written** |
| 9 | Observability + performance | metrics endpoints exist; **loadtest CLI, logging-audit test, experiments not done** |
| 10 | Docs + final verification | not started |
| 11 | AI features | not started (optional) |

Commands that produced results:

- `go build ./...`, `go vet ./...` (backend): clean. `go test -count=1 ./internal/...`: pass.
- `$env:EM_TEST_DATABASE_URL='postgres://eventmesh:eventmesh@127.0.0.1:5432/postgres?sslmode=disable'; $env:EM_TEST_REDIS_URL='redis://127.0.0.1:6379/1'; go test -count=1 -v ./tests` (in `backend`): all 25 tests pass (~12 s).
- `scripts\stop-stack.ps1` after the live demo: no `api`/`worker`/`receiver` processes left, `.run\` empty.

## Next action

Milestone 8: `frontend/playwright.config.ts` + `frontend/e2e/flow.spec.ts` (the 10-step flow in PROMPT.md section 12; receiver hook `switch/e2e` needs the endpoint secret registered via `PUT /control/secret/:name` - not implemented yet, or set `EM_RECEIVER_SECRET` per hook), `scripts/test-e2e.ps1`, `.github/workflows/ci.yml`, `.golangci.yml`, then milestones 9-10.

## Open problems

- `-race` not run (install MSYS2/TDM-GCC; `scripts\test.ps1` and `test-integration.ps1` pick up `gcc` automatically).
- `start-stack.ps1` run from the assistant's tool blocks until the child processes exit (inherited output handles); it works fine from a normal terminal - not yet verified there.
- Not written: `cmd/loadtest`, `.github/workflows/ci.yml`, `.golangci.yml`, `docker-compose.yml`, README, docs (windows-setup, architecture, database, reliability, security, performance, interview-guide), Playwright.
- `docs/openapi.yaml` query param for event ID search is documented as `q` but the API uses `id`: fix the spec and re-copy to `backend/internal/httpapi/swagger/openapi.yaml`.
- Receiver signature check uses one default secret; an e2e test creating its own endpoint (random secret) needs a way to register that secret with the receiver.
- `internal/database/db/*.go` is the sqlc output kept in the repo; `sqlc` is not installed here, so `scripts\sqlc.ps1` / CI diff check unverified.
- Port 8080 is taken by an unrelated `httpd`; dev API port is 8090 (ADR-003). Seed uses `EM_RECEIVER_BASE_URL` default `http://127.0.0.1:9000`.

## Decisions made this session

ADR-001..005 in `docs/design-decisions.md`. New this session: `endpoints.Service.CreateWithSecret` (seed only, not routed) so demo endpoints share
`EM_RECEIVER_SECRET` with the receiver; test harness builds `httpapi.New` directly with an optional deps hook to inject a failing queue.

## Fixed this session (found by the browser walk)

- CSRF origin check rejected the API's own origin when it serves the frontend; `middleware.sameOrigin` now allows it (integration test `TestCSRFOriginCheckAllowsSameOriginAndRejectsOthers`).

## Files that matter now

- `backend/internal/httpapi/router.go`, `backend/internal/workers/process.go`, `backend/internal/realtime/realtime.go`
- `backend/tests/stack_test.go` (harness), `backend/tests/reliability_test.go`, `backend/tests/delivery_test.go`, `backend/tests/queue_test.go`
- `backend/cmd/{api,worker,receiver,seed}/main.go`, `scripts/*.ps1`, `.env.example`
- `docs/PROMPT.md` sections 10 (frontend), 6 (API), 13-15 for what's next
