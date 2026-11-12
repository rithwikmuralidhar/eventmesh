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
- **Scaling is near-linear then flattens**: 1 -> 5 workers gives 3.3x; 5 -> 10 gives 1.15x; 10 -> 25 gives 1.12x. By 10
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
