#!/usr/bin/env bash
# Drain benchmark for docs/performance.md ("Finding the delivery ceiling").
#
# scripts/loadtest measures ingestion and delivery together, which hides the worker's own limit behind
# the API's. This harness separates them: it ingests every event with NO worker running, then starts
# the worker and times how long the backlog takes to drain, using the timestamps PostgreSQL recorded.
#
#   bench/drain.sh <label> <workers> <endpoints> <events> [VAR=value ...]     (run from backend/, Git Bash)
#
# Extra VAR=value pairs are set for the API and worker (for example EM_DISPATCHER_COUNT=4).
# Uses its own database (eventmesh_bench) and Redis key prefix; the development data is not touched.
set -euo pipefail
label=$1; workers=$2; endpoints=$3; events=$4; shift 4
PSQL=${PSQL:-psql}
DB=${BENCH_DB:-postgres://eventmesh:eventmesh@127.0.0.1:5432/eventmesh_bench?sslmode=disable}
BIN=../bin/bench
export EM_DATABASE_URL="$DB${PG_PARAMS:-}" EM_REDIS_KEY_PREFIX="drain$$" EM_HTTP_ADDR=127.0.0.1:8290 EM_METRICS_PORT=9290 \
  EM_WORKER_METRICS_PORT=9291 EM_RECEIVER_ADDR=127.0.0.1:9200 EM_SSRF_ALLOWED_HOSTS=127.0.0.1 \
  EM_INGEST_RATE_LIMIT=100000 EM_INGEST_RATE_BURST=100000 EM_API_RATE_LIMIT=100000 EM_API_RATE_BURST=100000 \
  EM_AUTH_RATE_LIMIT_PER_MINUTE=1000 EM_RECONCILER_GRACE=10m EM_STATS_CACHE_TTL=0 EM_WORKER_COUNT="$workers"
for kv in "$@"; do export "$kv"; done

"$PSQL" "$DB" -qAt -c "TRUNCATE users CASCADE" >/dev/null 2>&1
pids=()
cleanup() { for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done; wait 2>/dev/null || true; }
trap cleanup EXIT
$BIN/receiver.exe >/dev/null 2>&1 & pids+=($!)
$BIN/api.exe >/dev/null 2>&1 & pids+=($!)
for i in $(seq 1 50); do curl -s -m 1 http://127.0.0.1:8290/readyz >/dev/null && break; sleep 0.2; done

$BIN/loadtest.exe -api http://127.0.0.1:8290 -receiver http://127.0.0.1:9200 -events "$events" -healthy "$endpoints" \
  -clients 20 -label "$label" >/dev/null 2>&1 & lt=$!
want=$((events * endpoints))
for i in $(seq 1 600); do
  [ "$("$PSQL" "$DB" -qAt -c "SELECT count(*) FROM deliveries")" = "$want" ] && break; sleep 0.2
done
sleep 0.5   # let the API finish its enqueues
cpu() { powershell -NoProfile -Command "(Get-Process $1 -ErrorAction SilentlyContinue | Measure-Object -Property CPU -Sum).Sum" 2>/dev/null | tr -d ''; }
[ -n "${CPU_REPORT:-}" ] && { pg0=$(cpu postgres); rd0=$(cpu memurai); }
$BIN/worker.exe >/dev/null 2>&1 & pids+=($!)
wait $lt || true
if [ -n "${CPU_REPORT:-}" ]; then
  # CPU seconds used while the backlog drained (worker and receiver: since they started; PostgreSQL and Memurai: the difference).
  echo "cpu_seconds worker=$(cpu worker) receiver=$(cpu receiver) postgres=$(awk "BEGIN{print $(cpu postgres)-$pg0}") memurai=$(awk "BEGIN{print $(cpu memurai)-$rd0}") logical_cpus=$(nproc)"
fi
limited=$(curl -s http://127.0.0.1:9291/metrics | sed -n 's/^eventmesh_rate_limited_total{scope="endpoint"} //p')
"$PSQL" "$DB" -qAt -F ' ' -c "SELECT count(*) FILTER (WHERE status = 'delivered'), count(*),
  round(extract(epoch FROM max(updated_at) - min(last_attempt_at))::numeric, 3),
  round(count(*) / extract(epoch FROM max(updated_at) - min(last_attempt_at))::numeric)
  FROM deliveries" | { read -r ok total secs rate; echo "$label workers=$workers endpoints=$endpoints deliveries=$ok/$total drain_s=$secs per_s=$rate rate_limited=${limited:-0} $*"; }
