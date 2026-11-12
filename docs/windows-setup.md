# Windows setup

Everything runs natively on Windows 10/11 with PowerShell. No WSL, no Docker, no `make`.

## 1. Install the tools

| Tool | Notes |
| --- | --- |
| Git for Windows | |
| Go (latest stable) | `go version` |
| Node.js LTS | includes npm |
| PostgreSQL 16 or newer | Install as a Windows service on port 5432. Remember the `postgres` superuser password. The installer may put it on a non-C drive (for example `E:\Program Files\PostgreSQL\18`); `psql.exe` is in its `bin` folder |
| Memurai Developer | A Windows-native Redis-compatible server, installed as a service on 6379. `memurai-cli ping` should answer `PONG` |
| C toolchain (optional, for `go test -race`) | MSYS2 (`pacman -S mingw-w64-ucrt-x86_64-gcc`, then add `C:\msys64\ucrt64\bin` to PATH) or TDM-GCC. The race detector needs CGO. Without it the scripts print "-race NOT RUN" and run without it |

EventMesh uses only Redis lists, sorted sets, pub/sub and Lua scripts with `redis.call` and `TIME`. It does not use
modules, Streams, Functions or RESP3. `go run ./cmd/redischeck` (run from `backend`) verifies the Lua features on
your server.

## 2. Configure

```powershell
scripts\setup.ps1          # go mod download, installs sqlc, npm ci, Playwright Chromium, creates .env
scripts\init-db.ps1        # creates role "eventmesh" (CREATEDB) and database "eventmesh"; psql asks for the postgres password
scripts\check-prereqs.ps1
scripts\migrate.ps1
```

`init-db.ps1` finds `psql.exe` from your PostgreSQL service; pass `-PsqlPath` if it cannot. The role's development
password is `eventmesh` - change it for anything shared and update `EM_DATABASE_URL` / `EM_TEST_DATABASE_URL`.

## 3. Firewall prompts

The first time the API, worker and receiver listen, Windows Defender Firewall asks for permission. They bind to
`127.0.0.1` only (metrics too), so they are not reachable from the network either way; choose "Cancel" or
private-only.

## 4. Common problems

| Symptom | Fix |
| --- | --- |
| `password authentication failed for user "eventmesh"` | Run `scripts\init-db.ps1`, or fix the URL in `.env` |
| `check-prereqs` says a port is in use | It names the process. Port 8080 is often Apache/IIS; the default API port here is 8090. Change `EM_HTTP_ADDR` |
| `bind: An attempt was made to access a socket in a way forbidden` | The port is in a Windows excluded range: `netsh interface ipv4 show excludedportrange protocol=tcp`; pick another |
| `-race NOT RUN` | Install a C toolchain as above |
| `localhost` resolves to `::1` and a service is not listening there | Use `127.0.0.1`, which is what `.env.example` does |
| Load test reports connection errors after a few thousand requests | New TCP connection per request exhausts Windows ephemeral ports; `loadtest` reuses connections, other clients must too |
| `start-stack.ps1` appears to hang when started from an automation tool | The child processes inherit the tool's output handles; run it from a normal terminal |

## 5. pgvector (milestone 11 only)

Not installed: the AI features (milestone 11) are not built, so nothing in the core system needs pgvector.
