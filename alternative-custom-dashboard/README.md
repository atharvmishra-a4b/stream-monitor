> **This is the earlier custom version** (own backend, Postgres and web app). The agent still supports it with `--server`. The main project is the Prometheus + Grafana setup one folder up.

# Stream monitor

CPU, memory and network health of every live-streaming laptop, by hub and temple.

```
laptop: stream-agent ──HTTPS──▶ backend (Go) ──▶ Postgres
                                   │   ▲
                       dashboard ◀─┘   └── hubs and temples (read-only) from kratos
```

Four pieces, nothing else to run:

| Folder | What |
|---|---|
| `agent/` | One small program per laptop. Measures CPU, memory and network every 15s and reports it. |
| `backend/` | Go API. Stores readings in Postgres, reads hubs and temples from kratos, applies the health rules. |
| `web/` | Next.js dashboard: hubs, then temples, then a device with live figures and history. |
| `tools/fleet-sim/` | Fake laptops, to try the dashboard without hardware. |

- **Kratos is not changed.** Hubs and temples are read from it. Which laptop belongs to which temple is saved in this project's own Postgres (`devices.temple_id`).
- Streams run on a schedule (default 7:00-12:00 and 16:00-20:00 IST, the same for every temple). A laptop that is not reporting is **offline** (a problem) only during those hours, and from 30 minutes before each stream starts. Outside them it shows as **standby**, which is normal. Change it with `STREAM_WINDOWS`, `TIMEZONE`, `EARLY_MINUTES`.
- "Not reporting" means nothing has arrived for 90 seconds.
- A device **needs attention** when CPU is over 90%, memory over 93%, the network reports over 10 errors/drops per minute, or its reports take over 1.5 seconds to arrive. The rules are in `backend/health.go`.
- Readings older than 7 days are deleted automatically (`RETENTION_DAYS`).

## Try it on a Mac

You need Docker Desktop. For a clean start (if you ran the earlier version): `docker compose down -v`.

```bash
# 1. Postgres, backend and dashboard
docker compose up --build

# 2. See the temple ids a laptop can belong to
curl localhost:8080/api/temples

# 3. In another terminal, run the agent on this Mac (needs Go: brew install go)
cd agent
go run . --server http://localhost:8080 --temple aaaaaaa1-0000-4000-8000-000000000001 --name "My Mac"

# 4. Open http://localhost:3000   (the Mac appears within about 15 seconds)
```

To fill the dashboard with fake laptops as well: `go run ./tools/fleet-sim` from the repo root.

## Use the real hubs and temples from kratos

```bash
CATALOG_SOURCE=kratos \
KRATOS_BASE_URL=https://api-sbox.a4b.io/gw2 \
KRATOS_AUTH_HEADER="x-fe-server: 3" \
docker compose up --build
```

## Hosting it

The backend and dashboard are each one container (`backend/Dockerfile`, `web/Dockerfile`) and the backend needs a Postgres.

| Setting (backend) | |
|---|---|
| `DATABASE_URL` | Postgres connection string |
| `AGENT_TOKEN` | Shared secret the agents send. **Set this** for anything reachable from the internet. |
| `CATALOG_SOURCE`, `KRATOS_BASE_URL`, `KRATOS_AUTH_HEADER` | Where hubs and temples come from |
| `RETENTION_DAYS` | How long to keep readings (default 7) |
| `STREAM_WINDOWS`, `TIMEZONE`, `EARLY_MINUTES` | When laptops are expected to be online (see above) |

| Setting (dashboard) | |
|---|---|
| `BACKEND_URL` | Where the backend is, e.g. `http://backend:8080` |

Laptops need to reach the backend over HTTPS (put it behind a load balancer or reverse proxy that does TLS).
The dashboard has **no login yet**, so put it behind your SSO or VPN.
Size: 50 laptops send about 290,000 readings a day, roughly 2 million rows kept for a week. A small Postgres is plenty.

## Putting the agent on a laptop

See `agent/README.md`. On Windows it is one PowerShell command that installs the agent so it starts with Windows.
