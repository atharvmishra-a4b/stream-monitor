# Stream monitor

CPU, memory and network health of every live-streaming laptop, shown in Grafana by hub, temple and laptop. No custom app to run.

```
laptop: stream-agent ──HTTPS (remote-write)──▶ Prometheus ──▶ Grafana
                                                   │             (dashboards)
                                              rules + alerts
                                          (status per laptop, Slack)
```

| Folder | What |
|---|---|
| `agent/` | One small program per laptop. Measures CPU, memory and network every 15s and pushes it to Prometheus. |
| `stack/prometheus/` | `rules.yml`: turns raw readings into one status per laptop, with alerts. `rules_test.yml`: tests for them. |
| `stack/grafana/` | Two dashboards (fleet overview, single laptop) and the data source setup. |
| `docker-compose.yml` | Prometheus + Grafana to try it on your own machine. Nothing to build. |
| `alternative-custom-dashboard/` | An earlier version with its own backend, database and web app. Not needed for this setup. |

## What you see in Grafana

- **Streaming laptops:** counts of healthy / needs attention / offline / standby, a table of hubs, and a table of laptops. Click a hub to filter, click a laptop to open it.
- **Streaming laptop:** live figures, history charts with the alert limits drawn on them, the **top 5 programs by CPU and by memory** (so a red laptop shows why, for example OBS or a browser using it all), and hardware details. Pick hub, temple and laptop at the top.

A laptop's **status** comes from `stack/prometheus/rules.yml`:

| Status | Meaning |
|---|---|
| Healthy | Reporting, nothing wrong |
| Needs attention | Reporting, but CPU over 90%, memory over 93%, over 10 network errors or drops a minute, or reports taking over 1.5 seconds |
| Offline | Nothing received for 90 seconds |
| Standby | Not currently used — see below |

**No stream schedule is configured.** Laptops are treated as expected online at all hours, so a laptop that
stops reporting is Offline whatever the time, including overnight and when switched off deliberately.
Standby is never produced, and its dashboard count stays at zero. This is deliberate while the stream times
are unsettled: a wrong schedule would park genuinely dead laptops in Standby and hide them.
To restore a schedule, `stream:window_active` in `rules.yml` carries the original expression in a comment.

Alerts fire when a laptop is offline for 2 minutes, or has a problem for 5 minutes, so short spikes are ignored.

## Try it on a Mac

You need Docker Desktop (and Go for the agent: `brew install go`).

```bash
# 1. Prometheus and Grafana (only pulls images)
docker compose up

# 2. In another terminal, run the agent on this Mac
cd agent
go run . --remote-write http://localhost:9090/api/v1/write --hub Ujjain --temple "Test temple" --name "My Mac"

# 3. Open http://localhost:3000   (the Mac appears after about 30 seconds)
```

To fill the dashboards with fake laptops as well: `cd agent && go run ./cmd/fleet-sim`.
(Fake laptops start with 45 minutes of history, so the charts are not empty.)

Check the rules at any time with `promtool test rules stack/prometheus/rules_test.yml` (promtool comes with Prometheus).

## Putting it on your Prometheus and Grafana

1. **Let laptops push to Prometheus.** Prometheus must be started with `--web.enable-remote-write-receiver`. The laptops are outside your network,
   so put an HTTPS address with a username and password in front of **only** `/api/v1/write` (reverse proxy or load balancer). Do not expose Prometheus itself.
2. **Add the rules.** Add `stack/prometheus/rules.yml` to Prometheus's `rule_files`, and route the two alerts (label `team: streaming`) to a Slack channel of their own.
   Do not send them to the on-call paging system: a laptop that is off at a temple is not a production outage.
3. **Add the dashboards.** In Grafana, Dashboards, New, Import, upload both files from `stack/grafana/dashboards/`, and choose your Prometheus data source at the top of each.
4. **Install the agent** on each laptop (see `agent/README.md`).

Size: about 30 series per laptop (7 for the readings, up to 20 for the top programs), so 15 laptops is roughly 450 series. That is tiny next to a normal service.

Things to know:
- The top-programs table sends **program names only** (such as `obs64.exe` or `chrome.exe`), never window titles, files or command lines. It is updated once a minute, and turned off with `--programs-interval 0`.
- Keep each laptop's clock correct. Readings with a wrong time are rejected or look odd.
- After a long outage (more than about an hour) Prometheus may refuse the oldest buffered readings; the agent drops those and carries on.
  Your Prometheus admin can widen this with `out_of_order_time_window`.
- Names are labels, so renaming or moving a laptop means running the agent again with the new values. The old name stays in history until it ages out.
