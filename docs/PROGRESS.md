# EventMesh progress

Specification: [PROMPT.md](PROMPT.md). Project root: `C:\Users\Testing\Desktop\eventmesh\eventmesh` (nested folder).

## Status (checkpoint 2026-11-12, fourth session): milestones 1-10 done, 11 not built

Machine: Windows 11, i7-11850H, 32 GB, Go 1.27.4, Node 26.13.1, PostgreSQL 18.7, Memurai (Redis 7.2.5). DB role `eventmesh` created with `scripts\init-db.ps1`.

| # | Milestone | Gate result |
| --- | --- | --- |
| 1 | Skeleton | passed: `scripts\check-prereqs.ps1` all PASS; migrate up/down/up ok; Lua `redis.call`+`TIME` ok on Memurai (`go run ./cmd/redischeck`) |
| 2-3 | Accounts, endpoints, ingestion | passed (integration tests incl. 50 concurrent idempotent requests) |
| 4 | Delivery engine | passed live (237/237 signatures verified by the receiver) |
| 5 | Reliability | passed except **`-race` not run (no `gcc`)** |
| 6 | Read API, SSE, OpenAPI | passed (SSE isolation, audit scoping, 404 matrix, `/docs`) |
| 7 | Frontend | passed: build, tsc, eslint, 10 Vitest tests, manual walk of every page |
| 8 | End to end | passed: `scripts\test-e2e.ps1` -> Playwright 1 test passed (3.4 s); stack stops cleanly with no orphans. CI workflow written, **not executed** (no GitHub run) |
| 9 | Observability + performance | passed: metrics on :9090 and :9091 show real values; log-leak test passes; load test + 7 runs recorded in `docs/performance.md` |
| 10 | Docs + final verification | docs written; **clean clone** (`git clone` to a temp dir): `go build`, `go vet`, `go test ./internal/...`, `go test ./tests` (all pass), `npm ci`, `npm run build`, `vitest` (10/10), `eslint` clean |
| 11 | AI features | **not built** (optional); ADR-020 |

Commands that produced results:
- `cd backend; $env:EM_TEST_DATABASE_URL='postgres://eventmesh:eventmesh@127.0.0.1:5432/postgres?sslmode=disable'; $env:EM_TEST_REDIS_URL='redis://127.0.0.1:6379/1'; go test -count=1 ./...` -> all ok (integration ~16 s, 30+ tests).
- `scripts\test-e2e.ps1` (run in background from the assistant tool) -> `1 passed`.
- `scripts\run-experiments.ps1 -Events 1000` -> `loadtest-results\*.json`.

## Not verified / not done

- `go test -race` never run locally (no C compiler); `golangci-lint` not installed so `scripts\lint.ps1` ran only gofmt, `go vet`, tsc, eslint; CI would run both.
- `.github/workflows/ci.yml` and `sqlc generate` diff check never executed (no sqlc installed; `internal/database/db` was committed as generated before).
- `scripts\setup.ps1`, `scripts\test.ps1`, `scripts\lint.ps1`, `scripts\test-integration.ps1`, `scripts\sqlc.ps1` were written but not each executed end to end (their steps were run directly).
- `start-stack.ps1` blocks when started from an automation tool (inherited output handles); fine in a normal terminal, not verified there. README demo snippet `scripts\demo-key.ps1 | Select-Object -Last 1` was exercised via the same code path, not copied verbatim.
- Light theme, mobile width and create/rotate/delete UI flows were not exercised manually (Vitest covers create + one-time secret, API key reveal/revoke).
- Performance: single runs, no repeats; the bottleneck above ~10 workers is *not* isolated (see performance.md).
- Milestone 11 (Gemini RAG / explanations / NL search) and pgvector: not started.

## Next action

If continuing: install a C toolchain and `golangci-lint`, run `scripts\lint.ps1` and `scripts\test.ps1` (with `-race`); push to GitHub to run CI; then, optionally, milestone 11.

## Decisions

ADR-001..020 in `docs/design-decisions.md` (API dev port 8090 instead of 8080; `EM_` variables; Redis key prefix; same-origin CSRF rule; seed-only secret; receiver `PUT /control/secret/:name` and `PUT /control/switch/:name?mode=`).

## Files that matter

`backend/internal/{workers,queue,ratelimit,realtime,httpapi,events,deliveries,endpoints}`, `backend/tests/*`, `backend/cmd/*`, `frontend/src/pages/*`, `frontend/e2e/flow.spec.ts`, `scripts/*.ps1`, `.env.example`, `docs/*`.
