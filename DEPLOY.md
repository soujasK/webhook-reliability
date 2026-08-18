# Deploying to Railway

This deploys the engine as a **single process** (`--mode=all` — api + dispatcher + scheduler +
workers together), the same mode the "no Docker" local dev instructions in the README use.
Railway is Docker-native, so it builds straight from the repo's existing `Dockerfile` — no
Railway-specific config file is needed.

Two code changes were made on this branch to make that possible:

- `internal/queue/redis.go` now dials Redis via a full `redis://` URL (`redis.DialURL`) instead
  of a bare `host:port`, since hosted Redis requires a password and the old code had no way to
  send one.
- `cmd/server/main.go` gained an optional `SEED_URL` env var: if set, the `api`/`all` modes
  register that URL as a demo endpoint on boot, so a fresh deploy has something to actually
  deliver webhooks to without a manual `--mode=seed` step.

## 1. Get the code onto GitHub

Railway's "Deploy from GitHub repo" flow needs the branch pushed and (for the first deploy)
merged or selectable in the dashboard:

```bash
git push -u origin deploy/railway
```

Then either merge `deploy/railway` into `main` via a PR, or point Railway at the `deploy/railway`
branch directly (Railway lets you pick any branch per service).

## 2. Create the Railway project

1. [railway.app](https://railway.app) → **New Project** → **Deploy from GitHub repo** → select
   `webhook-engine`. Railway detects the `Dockerfile` automatically.
2. In the same project, click **New → Database → PostgreSQL**.
3. Click **New → Database → Redis**.

## 3. Configure the main service (api + workers)

Rename the auto-created service to `api` (Settings → General). Under **Variables**, add:

| Variable | Value |
|---|---|
| `DATABASE_URL` | `${{Postgres.DATABASE_URL}}` (reference the Postgres plugin) |
| `REDIS_URL` | `${{Redis.REDIS_URL}}` (reference the Redis plugin — check the Redis plugin's own Variables tab for its exact public variable name if this doesn't resolve) |
| `SEED_URL` | `http://mock-receiver.railway.internal:9090/receive` (set after step 4 below; omit if you skip the mock receiver) |

Leave `PORT` alone — Railway injects it and the app already reads `PORT` from the environment
([config.go](internal/config/config.go)).

Under **Settings → Networking**, click **Generate Domain** to get a public URL. Under
**Settings → Deploy**, set **Healthcheck Path** to `/healthz`.

## 4. (Recommended) Deploy the mock receiver so there's something to demo

Without a registered endpoint, `POST /events` still works but there's nothing to show retries,
backoff, dead-lettering, or replay against. Add a second service from the same repo:

1. **New → GitHub Repo** → same `webhook-engine` repo again.
2. Rename it to `mock-receiver` (the `SEED_URL` above depends on this exact name for Railway's
   internal DNS: `mock-receiver.railway.internal`).
3. Under **Settings → Deploy**, set a **Custom Start Command** of `/mock-receiver` — this
   overrides the Dockerfile's `ENTRYPOINT ["/server"]` for this service only.
4. Variables: `PORT=9090`, `FAIL_COUNT=2` (fails the first 2 hits per delivery so you can watch a
   retry-then-succeed cycle; set it above `MAX_ATTEMPTS` if you want to watch a dead-letter
   instead).
5. Don't generate a public domain for this one — it only needs to be reachable over Railway's
   private network from the `api` service, same as a real customer's server would be reachable
   only over the internet from yours.

Redeploy `api` after setting `SEED_URL` so it picks up the new variable and seeds on boot.

## 5. Verify

```bash
curl https://<your-api-domain>.up.railway.app/healthz

curl -X POST https://<your-api-domain>.up.railway.app/events \
  -H "Content-Type: application/json" \
  -d '{"source":"demo","event_type":"payment.succeeded","payload":{"amount":499},"idempotency_key":"evt-001"}'

# use the returned event_id-adjacent delivery flow: check logs on the api service to find the
# delivery_id, or query it via GET /deliveries/{id} once you have one
```

Watch the `api` service's logs — you should see the ingest, a failed attempt or two against
`mock-receiver`, a scheduled retry, then either `delivered` or `dead-lettered` depending on how
`FAIL_COUNT` compares to `MAX_ATTEMPTS`.

## Troubleshooting

- **`pq: SSL is not enabled on the server`**: Railway's internal Postgres doesn't speak TLS on
  the private network. Override the variable to
  `${{Postgres.DATABASE_URL}}?sslmode=disable`.
- **Redis connection refused / auth errors**: double-check you used the Redis plugin's
  *connection URL* variable (starts with `redis://default:...@...`), not a bare host:port — the
  code now requires a full URL.
- **`SEED_URL` didn't seed anything**: it only runs from the `api`/`all` modes, and only logs a
  failure rather than crashing the process — check the `api` service logs for `seed on boot
  failed`.

## Cost note

Railway's Hobby plan is usage-based against a small monthly credit. Two lightweight services
(api, mock-receiver) plus Postgres and Redis idling most of the time should stay within that for
a resume/portfolio demo, but sustained traffic or leaving it running indefinitely will eventually
bill past the credit — stop the services from the dashboard when you're not actively showing it
off if that's a concern.
