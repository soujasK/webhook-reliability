# Webhook Reliability & Replay Engine

## Requirements

This project uses Go and Docker; no Python requirements file is required for the app runtime.

If you want a simple environment file for tooling or scripts, you may add packages here as needed.

Reliable event delivery to customer HTTP endpoints: durable ingestion, per-endpoint ordering,
exponential-backoff retries, rate limiting, dead-lettering, and manual replay.

Stack: Go 1.22 (stdlib `net/http`, `database/sql`) · PostgreSQL (source of truth) · Redis (queues,
locks, retry timing, rate limiting) · Docker Compose.

This has been run end-to-end against real Postgres + Redis instances — ingestion, idempotent
resubmission, retry/backoff, dead-lettering after max attempts, and replay recovery all verified
manually before this was handed over. It has **not** been load-tested or hardened for production
(see Known Limitations below).

## Architecture

```
Source → Ingest API → Postgres (event + delivery rows, one transaction)
                          │
                    Dispatcher (sweeps pending → Redis)
                          │
                    Redis Queue (FIFO list per endpoint)
                          │
                    Worker Pool (goroutines, one lock per endpoint)
                          │
                    Customer Endpoint
                    ↙            ↘
              success        failure → retry (backoff+jitter) → back to Redis Queue
                                  ↓ (max attempts exceeded)
                             dead_letter → POST /replay/{id} → back to Redis Queue
```

Postgres is the durability guarantee — nothing is queued until it's committed. Redis is fast and
disposable — if it loses state, the dispatcher's next sweep re-feeds anything still `pending`.

## Project layout

```
cmd/
  server/main.go        — entrypoint, --mode picks which loop(s) run
  mock-receiver/main.go — tiny HTTP server for local testing, can simulate failures
internal/
  api/handlers.go        — POST /events, POST /replay/{id}, GET /deliveries/{id}
  config/config.go        — env var loading
  models/models.go        — shared structs
  store/                  — Postgres: connection, embedded migrations, all queries
  queue/                  — Redis: pool, queue ops, locks, token-bucket rate limiter,
                             dispatcher loop, retry scheduler loop
  worker/                 — worker pool + the actual delivery logic (HTTP POST, signing,
                             retry scheduling)
```

## Run locally (no Docker)

Needs Go 1.22+, Postgres, and Redis reachable locally.

```bash
export DATABASE_URL="postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable"
export REDIS_ADDR="localhost:6379"

go build -o bin/server ./cmd/server
go build -o bin/mock-receiver ./cmd/mock-receiver

# terminal 1 — a receiver that fails its first 2 requests, then succeeds
FAIL_COUNT=2 PORT=9090 ./bin/mock-receiver

# terminal 2 — everything in one process (api + dispatcher + scheduler + workers)
./bin/server --mode=all

# terminal 3 — register a demo endpoint subscribed to payment.succeeded, then fire an event
./bin/server --mode=seed --seed-url=http://localhost:9090/receive
curl -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '{"source":"test","event_type":"payment.succeeded","payload":{"amount":499},"idempotency_key":"evt-001"}'
```

Migrations run automatically on startup (`CREATE TABLE IF NOT EXISTS`, safe to re-run).

## Run with Docker Compose

```bash
docker compose up --build
docker compose up --build --scale worker=3   # to run 3 worker replicas
```

This starts Postgres, Redis, the API, and separate `dispatcher` / `worker` / `scheduler`
containers all from the same binary (`--mode` differs per container) — plus a `mock-receiver` on
`:9090` for testing without a real customer endpoint.

## API

| Endpoint | What it does |
|---|---|
| `POST /events` | Ingest an event. Body: `{source, event_type, payload, idempotency_key}`. Returns `202` (accepted) or `200 {"status":"duplicate"}` if the idempotency key was already seen. |
| `POST /replay/{delivery_id}` | Reset a delivery to `pending` and re-queue it — same durability path as first ingestion. |
| `GET /deliveries/{delivery_id}` | Current `status` and `attempt_count`. |
| `GET /healthz` | Liveness check. |

## Config (env vars)

| Var | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/webhooks?sslmode=disable` | Postgres DSN |
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `PORT` | `8080` | API listen port |
| `WORKER_COUNT` | `4` | Goroutines per worker process |
| `MAX_ATTEMPTS` | `5` | Failures before dead-lettering |
| `DISPATCH_INTERVAL_MS` | `500` | Dispatcher sweep frequency |
| `RETRY_INTERVAL_MS` | `1000` | Retry scheduler poll frequency |

## Known limitations (read before calling this "production-ready")

- **Redis-restart gap**: a delivery marked `queued` in Postgres has no live queue entry if Redis
  restarts before a worker picks it up. Fix: have the dispatcher also re-sweep `queued` rows older
  than a staleness threshold, not just `pending` ones.
- **No inbound auth**: `POST /events` doesn't verify the caller. Add an API key or HMAC check
  before exposing this beyond localhost.
- **No circuit breaker**: a permanently-dead endpoint still gets retried up to `MAX_ATTEMPTS`
  times per event rather than being short-circuited after repeated failures.
- **No observability**: no metrics/tracing. `delivery_attempts` gives you an audit trail via SQL,
  but there's no Prometheus/Grafana wiring.
- **No test suite yet** — verified manually (see top of this file), not via `go test`.

## Suggested next steps

1. Add `go test` coverage for `store` (using a test DB) and `worker` (using `httptest.Server`).
2. Add HMAC-based auth on `POST /events`.
3. Add per-endpoint circuit breaking (skip straight to backoff-without-attempt after N consecutive failures).
4. Add Prometheus metrics: deliveries in flight, retry queue depth, dead-letter rate.
5. Swap the dispatcher's poll loop for Postgres `LISTEN/NOTIFY` to cut ingestion-to-delivery latency.
