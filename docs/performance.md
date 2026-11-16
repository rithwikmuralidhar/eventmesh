# Performance

**These are single-machine numbers on a development Windows install, with the load generator, API, worker, receiver,
PostgreSQL and Memurai all on the same host. They are not production capacity.** Each configuration was run once
(no repeats), so differences of a few percent are noise.

## Environment

| | |
| --- | --- |
| CPU | 11th Gen Intel Core i7-11850H, 8 cores / 16 logical |
| RAM | 32 GB |
| Disk | Samsung NVMe SSD |
| OS | Windows 11 Pro 10.0.26200 |
| PostgreSQL | 18.7 (native service, default configuration) |
| Memurai | Developer edition, Redis 7.2.5 compatible |
| Go | 1.27.4 |
| EventMesh settings | defaults, except: ingestion/API rate limits raised to 100000, reconciler grace 5 s (so EventMesh's own protection does not throttle the generator); `EM_WORKER_COUNT` as listed |

## Method

`backend/cmd/loadtest` creates its own user, API key and endpoints pointing at the local receiver, sends events from
concurrent clients with **one shared `http.Transport`** (connection reuse: a new TCP connection per request would
exhaust the Windows ephemeral port range of roughly 16k ports, each held in TIME_WAIT for about two minutes, and measure
the OS instead), waits until every delivery is terminal, and reports throughput and percentiles. "End-to-end latency" is
event creation to the start of the last (successful) attempt, taken from the delivery list; because all events are
ingested in under a second, it is dominated by time spent waiting in the queue, not by the HTTP call.

Commands (from the repository root, in a normal terminal): `scripts\run-experiments.ps1 -Events 1000`, which for each row
below restarts the stack and runs `scripts\loadtest.ps1 -events 1000 -clients 20 ...`. Raw JSON: `loadtest-results\*.json`
(not committed).

## Experiment 1: vary `EM_WORKER_COUNT` (1000 events x 3 healthy endpoints = 3000 deliveries, receiver answers instantly)

| Workers | Ingestion events/s | Ingestion p50 / p95 / p99 ms | Deliveries/s | Total s | End-to-end p50 / p95 / p99 ms | Retries / dead letter |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | 2909 | 4.4 / 7.8 / 104.7 | 607 | 4.95 | 2523 / 4228 / 4362 | 0 / 0 |
| 5 | 2123 | 6.1 / 16.4 / 114.0 | 1992 | 1.51 | 714 / 986 / 1010 | 0 / 0 |
| 10 | 1967 | 6.7 / 14.8 / 133.6 | 2298 | 1.31 | 490 / 723 / 744 | 0 / 0 |
| 25 | 1612 | 6.7 / 16.9 / 237.5 | 2575 | 1.17 | 357 / 464 / 476 | 0 / 0 |

## Experiment 2: vary the receiver's response delay (3 slow endpoints, 10 workers, 3000 deliveries)

| Delay | Ingestion events/s | Deliveries/s | Total s | End-to-end p50 / p95 / p99 ms | Retries / dead letter |
| --- | --- | --- | --- | --- | --- |
| 0 ms | 2108 | 2364 | 1.27 | 487 / 767 / 782 | 0 / 0 |
| 100 ms | 2447 | 92 | 32.44 | 15861 / 30255 / 31521 | 0 / 0 |
| 500 ms | 2319 | 19 | 154.3 | 76130 / 145590 / 151793 | 0 / 0 |

## What the results show

- **One worker is the limit at 1 worker**: 607 deliveries/s is about 1.6 ms per delivery end to end (claim, row claim,
  rate-limit check, signed request, one transaction, ack, publish) done serially.
- *(Superseded for the cause of the ceiling by "Finding the delivery ceiling" below; the numbers stand.)* **Scaling is near-linear then flattens**: 1 -> 5 workers gives 3.3x; 5 -> 10 gives 1.15x; 10 -> 25 gives 1.12x. By 10
  workers something shared is saturated at roughly 2300-2600 deliveries/s. I did **not** isolate which one. The candidates
  are PostgreSQL commit latency (two statements in one transaction per delivery plus the row claim), Memurai command
  throughput (about five Redis round trips per delivery), the single dispatcher goroutine that claims jobs one at a time, the
  receiver, and the 16 logical cores shared with the load generator. Telling them apart needs experiments I did not run
  (for example PostgreSQL with `synchronous_commit=off`, a Redis-only microbenchmark, or running the receiver elsewhere), so
  treat the flat region as "shared-resource limited, cause untested".
- **Ingestion degrades slightly as workers grow** (2909 -> 1612 events/s from 1 to 25 workers): the API and workers compete
  for the same PostgreSQL and CPU. Ingestion p99 stays in the low hundreds of ms.
- **With a slow endpoint the bottleneck is worker concurrency, not the machine**: throughput is almost exactly
  `workers / response time` (10 / 0.1 s = 100/s measured 92/s; 10 / 0.5 s = 20/s measured 19/s). Slow endpoints cost
  worker slots, so more workers (or per-endpoint concurrency isolation, not built) is the lever.
- **End-to-end latency here is queue wait**, because 3000 deliveries are enqueued almost at once; it falls as workers grow.
- No retries or dead letters occurred (every endpoint answered 200), so these runs say nothing about the failure path's cost.

## Reproducing

```powershell
scripts\run-experiments.ps1 -Events 1000          # about 6 minutes, writes loadtest-results\*.json
scripts\loadtest.ps1 -events 1000 -clients 20 -healthy 3 -label mine -out loadtest-results\mine.json
```

Flags: `-healthy N`, `-failing N`, `-slow N -slow-ms MS`, `-endpoint-rate`, `-clients`, `-timeout`. Use short retry delays
(`EM_INITIAL_RETRY_DELAY=200ms`, `EM_MAX_RETRY_DELAY=1s`) if you include failing endpoints.

## Finding the delivery ceiling (2026-11-16)

The section above ends with "shared-resource limited, cause untested". This section runs the tests it asked for. Same
machine, same caveats; PostgreSQL 18.7 with default settings.

### Method: measure the worker alone

`scripts\loadtest.ps1` ingests and delivers at the same time, so its deliveries/s figure mixes two things: the API and the
workers compete for PostgreSQL and CPU for the first third of each run. `backend/bench/drain.sh` separates them. It ingests
every event with **no worker running**, then starts the worker and computes the drain rate from PostgreSQL's own timestamps
(`count / (max(updated_at) - min(last_attempt_at))`), so no polling error is involved. Each run: 1,000 events x 20
endpoints = 20,000 deliveries to a receiver that answers at once. Twenty endpoints, not three, so that the per-endpoint
rate limit (1,000/s each, the maximum the schema allows) can never be the cap; `eventmesh_rate_limited_total` was 0 in
every run. Its own database and Redis prefix; reconciler grace raised to 10 minutes so it cannot add duplicate queue entries.
Every configuration was run three times, in the order shown, one after another.

### Results (deliveries per second, three repetitions)

| Configuration | Workers | Rep 1 | Rep 2 | Rep 3 |
| --- | --- | --- | --- | --- |
| Baseline | 1 | 1013 | 992 | 915 |
| Baseline | 5 | 3116 | 3509 | 3530 |
| Baseline | 10 | 2002 * | 4494 | 4478 |
| Baseline | 25 | 2202 * | 4410 | 5062 |
| Baseline | 50 | 2102 * | 4529 | 4601 |
| 4 dispatchers (`EM_DISPATCHER_COUNT=4`) | 25 | 2148 * | 5408 | 5669 |
| 4 dispatchers | 50 | 2148 * | 5402 | 5498 |
| `synchronous_commit=off` | 25 | 5060 | 5194 | 5056 |
| `synchronous_commit=off` + 4 dispatchers | 25 | 6388 | 6161 | 6144 |
| `synchronous_commit=off` + 4 dispatchers | 50 | 5899 | 5682 | 5325 |

\* See "The anomaly in repetition 1" below. Raw lines: `loadtest-results\drain.txt` (not committed).

### What this shows

1. **The earlier "ceiling" of 2,300 to 2,600/s was mostly ingestion competing with delivery.** Draining a backlog with
   nothing else running, 10 workers reach about 4,500/s (a three-endpoint trial gave 4,074/s), against 2,298/s in the
   combined test. The old table measured the machine doing two jobs at once, not the worker's limit.
2. **Workers stop helping at about 10.** 1 -> 5 workers is 3.5x; 5 -> 10 is 1.3x; 10 -> 25 -> 50 is flat within the noise
   (4,400 to 5,100). So past 10 workers the limit is something the workers share.
3. **There is no single bottleneck. Two measured causes each account for part of it.**
   - *Waiting for the WAL flush.* Each delivery commits one transaction and PostgreSQL waits for the write-ahead log to
     reach disk. With `synchronous_commit=off` (commits return before the flush; a crash can lose the last few hundred
     milliseconds of *acknowledged* commits, though never corrupt data): about 5,100/s, roughly +10% over the baseline at 25
     workers. Group commit already batches concurrent flushes, which is why the gain is modest.
   - *The single dispatcher.* One goroutine claimed jobs from Redis one round trip at a time. With four: about 5,500/s,
     roughly +15 to 20%.
   - *Both together*: about 6,200/s, roughly +30%. The gains add, so they are separate limits.
4. **What remains at about 6,200/s was not identified.** Going from 25 to 50 workers in that configuration made it slower
   (5,300 to 5,900). The worker process used about 15 CPU-seconds per 20,000 deliveries, that is **0.75 ms of CPU per
   delivery and about 3.7 of 16 logical cores** during the drain, and the receiver under one core, so the machine was not
   out of CPU on their account. PostgreSQL and Memurai run as services under another account and their CPU time could not
   be read without elevation. The untested suspect is Memurai: every delivery costs four Redis round trips (claim,
   rate-limit script, ack, publish), and Redis executes commands on one thread. A Redis-only
   benchmark, or pipelining the ack and the publish, would test it. Neither was done.
5. **One worker costs about 1 ms per delivery end to end**, half of it CPU in the worker (0.5 ms: HMAC signing, AES-GCM
   decryption of the endpoint secret, JSON, the HTTP client) and the rest waiting on PostgreSQL, Redis and the receiver.

### The anomaly in repetition 1

Five consecutive runs in the first repetition (baseline at 10, 25 and 50 workers, and both four-dispatcher runs) drained at
2,000 to 2,200/s, about half the later figure, while the runs just before them (1 and 5 workers) and the
`synchronous_commit=off` runs just after them were normal. Every slow run waits for WAL flushes and every unaffected run
either does not, or is too slow for it to matter. That pattern fits a period of slow disk flushes (another process writing,
a checkpoint, or autovacuum on the 1.2-million-row plans database built earlier the same hour), but the cause was **not
investigated**, so it is reported, not explained. It is the most useful accident in the data: it shows delivery throughput
on this design is directly exposed to WAL flush latency, which is what a production deployment would tune first (faster
log device, or accepting `synchronous_commit=off` for the attempt-recording transaction only, since a lost attempt record
is recovered by the lease and re-sent, which at-least-once already allows).

### What was changed because of this

`EM_DISPATCHER_COUNT` exists (default 1, unchanged behaviour). It was not made the default: the +15 to 20% was measured at
25 workers, not at the default 10, and only on this machine. `synchronous_commit` was left at PostgreSQL's default.

## Dashboard statistics cache (2026-11-16)

`GET /dashboard/stats?range=30d` for one user owning 20,000 events and 60,000 deliveries in a 1.2-million-delivery
database, measured with `curl` against a locally running API, sequential requests:

| | Requests | Median | Range |
| --- | --- | --- | --- |
| Cache off (`EM_STATS_CACHE_TTL=0`, `X-Cache: BYPASS`) | 25 | 37.3 ms | 36.1 to 94.4 ms |
| Cache hit (TTL 5 s) | 24 | 1.5 ms | 1.3 to 2.1 ms |
| Cache miss (TTL 1 s, requests 1.2 s apart) | 6 | 74.6 ms | 36.5 to 170.8 ms |

A hit is about 25x faster and takes the three aggregate queries off PostgreSQL. A miss is no faster than no cache (the
miss sample is small and includes the first request after start-up, which is why its median is higher), so the cache helps
only when the same user asks again within the TTL: an open dashboard refetching on live updates, which is exactly the
case under load. The price is up to 5 s of staleness on the Overview tiles (ADR-028). For a user with a few hundred
deliveries the uncached query is already a few milliseconds and the cache changes little.
