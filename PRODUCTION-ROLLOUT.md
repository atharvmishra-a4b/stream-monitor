# Production rollout — first Windows laptop

**Do the sandbox first.** Everything below is written to be run twice: once against sandbox to prove
the whole chain works, then against production. The only thing that changes between the two runs is
the host.

### The two environments (verified 2026-11-18)

| | Sandbox — **do this first** | Production |
|---|---|---|
| Prometheus | `https://monitor-sbox.a4b.io:9090` | `https://monitor.a4b.io:9090` |
| Grafana | `https://monitor-sbox.a4b.io` | `https://monitor.a4b.io` |
| Prometheus version | 2.54.1 | 2.50.1 |
| Grafana version | 11.2.2 | 11.4.0 |
| Scrape targets at risk | 112 | 234 |
| `web.enable-remote-write-receiver` | **false** — must be enabled | **false** — must be enabled |
| `web.enable-lifecycle` | **true** — config hot-reloads | false — rules need SIGHUP |
| Retention | 30d | 15d (default, unset) |
| Grafana datasource uid | *(not checked — needs sbox login)* | `rrgl-gJSk` |

Both Prometheus instances run as Docker Compose, config at `/etc/prometheus/prometheus.yml`, with
Alertmanager already wired up. Production's existing rule file is `/etc/prometheus/alert.rules`.

> **Use the `https://monitor*.a4b.io:9090` URLs, not the EC2 hostname.** `monitor.a4b.io:9090` and
> `http://ec2-65-1-25-0.ap-south-1.compute.amazonaws.com:9090` are the **same Prometheus** — verified
> identical version and target count — but the `monitor.a4b.io` path goes through the load balancer
> with TLS. That gives you encryption in transit for free, and a stable name that survives the EC2
> instance being replaced. There is no reason to point laptops at the raw instance.

> **Auth is deliberately deferred.** Per the infra decision the write endpoint goes up unauthenticated
> for now, with auth landing later across everything. Known consequences: anyone who can reach the
> endpoint can write arbitrary metrics into Prometheus. Port 9090 is already publicly reachable and
> unauthenticated **for reads** on both environments today, so enabling writes widens an existing
> exposure rather than creating a new one. Using the TLS URLs at least removes the plaintext problem.
> Revisit at the auth rollout.

---

## Part A — Prometheus changes (infra engineer)

Every command below uses `$PROM`. Set it once per environment, and run the whole of Part A against
sandbox before you touch production:

```bash
export PROM=https://monitor-sbox.a4b.io:9090    # sandbox — do this run first
# export PROM=https://monitor.a4b.io:9090       # production — only after sandbox passes
```

### A1. Enable the remote-write receiver

This is the blocker. Right now `POST /api/v1/write` returns **404** and no laptop can send anything.

In the Prometheus service definition in `docker-compose.yml`, add one flag:

```yaml
services:
  prometheus:
    # image: leave at whatever this environment already runs
    #   sandbox v2.54.1 / production v2.50.1 — both support everything here
    command:
      - --config.file=/etc/prometheus/prometheus.yml
      - --web.enable-remote-write-receiver     # <-- ADD THIS
      # ... keep every existing flag exactly as-is
```

**This one needs a real restart** — it is a process flag, not config. `SIGHUP` will not pick it up,
and neither will `/-/reload` on sandbox. Both environments currently have it `false`.

### A2. Allow backfill after a laptop is offline

Add this top-level block to `/etc/prometheus/prometheus.yml` (alongside `global:`, `alerting:`,
`rule_files:` — not nested inside any of them):

```yaml
storage:
  tsdb:
    out_of_order_time_window: 12h
```

**Why:** the agent buffers ~8 hours of readings when a temple's internet drops and replays them with
their original timestamps. With out-of-order ingestion disabled (the current state), Prometheus only
accepts samples landing in the active head block (~2h) and silently rejects the rest. Without this,
an overnight outage loses most of its history. 12h comfortably covers the agent's buffer.

### A3. Add the recording and alerting rules

**These are mandatory, not optional.** The dashboards query `stream:laptop_status` 25 times — without
the rules both dashboards render completely empty even when data is arriving.

1. Copy `stack/prometheus/rules.yml` from this repo to the host and mount it at
   `/etc/prometheus/stream-rules.yml` (same volume as any existing rule file).
2. Append it to `rule_files` in `prometheus.yml`, keeping whatever is already listed:

```yaml
rule_files:
- /etc/prometheus/alert.rules          # production's existing file — leave alone
- /etc/prometheus/stream-rules.yml     # <-- ADD THIS
```

Rule-file changes do **not** need a restart. On sandbox `web.enable-lifecycle` is `true`, so
`curl -X POST $PROM/-/reload` is enough. On production that endpoint is disabled, so use
`docker kill -s HUP <prometheus-container>`.

What it defines: 4 recording rules (`stream:window_active`, `stream:laptop_last_seen_seconds`,
`stream:laptop_problem`, `stream:laptop_status`) and 2 alerts (`StreamLaptopOffline`,
`StreamLaptopNeedsAttention`), both labelled `team: streaming`.

Validate before loading, if promtool is handy:
```bash
promtool check rules stack/prometheus/rules.yml
promtool test rules stack/prometheus/rules_test.yml
```

### A4. Alert routing

Route `team: streaming` to its own Slack channel in Alertmanager. **Do not route it to the on-call
paging rotation** — a laptop switched off at a temple is not a production incident.

**There is no stream schedule.** Laptops are treated as expected online at every hour, so
`StreamLaptopOffline` fires for *any* laptop quiet for 2 minutes — including one deliberately switched
off overnight. This is the right setting while validating (a dead agent can't hide in "standby"), but
it means a dedicated, non-paging Slack channel is not optional. Expect nightly noise once several
temples are live, and restore a schedule before the fleet grows.

#### Restoring a stream schedule later

`stream:window_active` in `rules.yml` is currently `vector(1)` (always on). To bring back time windows,
replace that expression with the original below. The numbers are minutes after midnight **IST** —
Prometheus works in UTC, so 330 is added:

| Window | Minutes | Meaning |
|---|---|---|
| 06:30–12:00 | 390–720 | stream 07:00–12:00, laptops on from 06:30 |
| 15:30–20:00 | 930–1200 | stream 16:00–20:00, laptops on from 15:30 |

```yaml
- record: stream:window_active
  expr: |
    (vector(1) and on() (((hour() * 60 + minute() + 330) % 1440) >= 390 and ((hour() * 60 + minute() + 330) % 1440) < 720))
    or
    (vector(1) and on() (((hour() * 60 + minute() + 330) % 1440) >= 930 and ((hour() * 60 + minute() + 330) % 1440) < 1200))
    or
    vector(0)
```

Restoring this makes the Standby status (3) reachable again. If you do, also revisit the fleet
dashboard — the Standby stat tile and the Hubs table's Standby column were removed when the schedule
was dropped. The `StreamLaptopOffline` description text also mentions that no schedule is configured.

### A5. Restart and verify

```bash
docker compose up -d prometheus      # restart picks up the new flag
```

Then, from anywhere:

```bash
# 1. receiver is live — expect 400 (not 404). 400 means "endpoint exists, bad body", which is correct.
curl -s -o /dev/null -w "%{http_code}\n" -X POST \
  $PROM/api/v1/write

# 2. flag is actually set — expect "true"
curl -s $PROM/api/v1/status/flags \
  | tr ',' '\n' | grep remote-write-receiver

# 3. rules loaded — expect the 4 stream: rules listed
curl -s $PROM/api/v1/rules \
  | tr ',' '\n' | grep 'stream:'

# 4. existing targets still scraping normally
curl -s "$PROM/api/v1/query?query=count(up)"
```

Step 4 matters. Baselines taken 2026-11-18, **before** any change:

| | Sandbox | Production |
|---|---|---|
| `count(up)` | 112 | 234 |

Production also had ~18 targets already failing (including the `prometheus` self-scrape, `bff`, `neo`,
`herald`, `hop`, `otel`). **Re-record the exact count immediately before restarting** so a pre-existing
failure isn't mistaken for fallout from this change.

### A6. Rollback

Remove the `--web.enable-remote-write-receiver` flag and restart. Nothing else is load-bearing —
the rules only create new `stream:*` series and the `storage:` block is inert without incoming data.
No existing scrape job, alert, or dashboard is touched by any of this.

---

## Part B — First Windows laptop

### What to copy over

From this repo, after running `agent/build.sh`:

- `agent/dist/stream-agent-windows.exe`
- `agent/install-windows.ps1`

### The URL to use

Test against sandbox first. The laptop steps are identical for both; only this URL changes.

```
https://monitor-sbox.a4b.io:9090/api/v1/write     # sandbox — start here
https://monitor.a4b.io:9090/api/v1/write          # production — after sandbox passes
```

Note it is **`https`** on port **`9090`**, and there is **no** `-Username` / `-Password` until auth
lands. Omit those parameters entirely — the installer only adds the flags if a username is supplied.

Moving a laptop from sandbox to production later just means re-running `install-windows.ps1` with the
production URL; it unregisters and re-registers the scheduled task. Note the readings already sent to
sandbox stay there — history does not follow the laptop across environments.

### Every option, both ways

The installer builds the exe's command line and hands it to Task Scheduler, so the two take the same
settings under different naming conventions. Use the **exe** to test by hand, the **installer** to
make it permanent.

| Installer (`.ps1`) | Agent (`.exe`) | Default | What it does |
|---|---|---|---|
| `-RemoteWrite` | `--remote-write` | — | Prometheus write URL; omit to take it from the shared config |
| `-Hub` | `--hub` | — | hub name (required) |
| `-Temple` | `--temple` | — | temple name (required) |
| `-Name` | `--name` | computer name | laptop name |
| `-Interval` | `--interval` | `15s` | how often to measure |
| `-WifiInterval` | `--wifi-interval` | `30s` | Wi-Fi link rate; `0` off |
| `-SpeedtestInterval` | `--speedtest-interval` | `0` (off) | measured download speed, e.g. `1m` |
| `-SpeedtestBytes` | `--speedtest-bytes` | `10` | MB per speed test |
| `-ConfigUrl` | `--config-url` | — | shared JSON config; everything below can come from it |
| `-LogUrl` | `--log-url` | — | endpoint receiving OBS's log |
| `-LogToken` | `--log-token` | — | sent as `x-api-key` |
| `-LogAppName` | `--log-app-name` | `daily_darshan` | sent as `x-afb-app-name` |
| `-ObsPassword` | `--obs-password` | — | enables OBS metrics |
| `-ObsUrl` | `--obs-url` | `ws://127.0.0.1:4455` | OBS WebSocket address |
| `-Username` / `-Password` | `--username` / `--password` | — | not needed until auth lands |
| — | `--programs-interval` | `1m` | top programs; `0` off |
| — | `--dry-run` | — | print readings, send nothing (exe only) |

Only the exe has `--dry-run`. Only the installer registers a scheduled task.

| | Running the exe | Running the installer |
|---|---|---|
| Survives a reboot | **no** | yes (SYSTEM task, at startup) |
| Restarts after a crash | no | yes, 99 retries |
| Where errors appear | on screen | Task Scheduler |
| Needs an elevated shell | no | **yes** |

### Step 1 — Put the files in place

```powershell
New-Item -Path "C:\StreamMonitor" -ItemType Directory -Force

Copy-Item -Path "$env:USERPROFILE\Downloads\stream-agent-windows.exe", `
                "$env:USERPROFILE\Downloads\install-windows.ps1" `
          -Destination "C:\StreamMonitor"

cd C:\StreamMonitor

# Windows tags downloaded files and will refuse to run the script otherwise
Unblock-File .\install-windows.ps1
Unblock-File .\stream-agent-windows.exe
```

If the script still refuses to run, allow it for this session only:
```powershell
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass
```

### Step 2 — Check the clock

Readings with a skewed clock get rejected or render wrong. Settings → Time & language → **Sync now**.

```powershell
w32tm /resync
Get-Date   # sanity-check against your phone
```

### Step 3 — Dry run (sends nothing)

Confirms the binary runs on this hardware before any network is involved:

```powershell
.\stream-agent-windows.exe --dry-run
```

Expect a line every 15s: `cpu 12%  memory 48%  up 30 kbps  down 110 kbps  errors 0.0/min`, a `wifi:`
line, and the top programs once a minute. `Ctrl+C` to stop.

If you intend to enable the speed test, prove it here before committing to the data it costs:

```powershell
.\stream-agent-windows.exe --dry-run --speedtest-interval 30s --speedtest-bytes 10
```

Within about 40 seconds you want `speed test: 142.3 Mbit/s  (10 MB in 0.9s, 0 failures)`.
`no successful measurement yet` with a rising failure count means the endpoint is unreachable from
this network — worth knowing now rather than from inside a scheduled task.

SmartScreen may block it here — the exe is unsigned. Allow it via **More info → Run anyway**, or
sign it if your policy requires.

### Step 4 — Foreground run (the real test)

Run it by hand first, so you see errors directly instead of hunting through Task Scheduler:

```powershell
.\stream-agent-windows.exe `
  --remote-write "$PROM/api/v1/write" `
  --hub "Ujjain" `
  --temple "Khedapati Hanuman temple" `
  --name "Main hall 1" `
  --interval 5s `
  --speedtest-interval 1m --speedtest-bytes 10
```

Drop the last line to leave the speed test off. The hub, temple and name strings must match
**exactly** what you intend to keep: they become Prometheus labels, and a changed capitalisation
creates a second, permanently-offline laptop.

Expect one line and then silence:
```
sending to $PROM/api/v1/write as Ujjain / Mahakal temple / Main hall 1
```

**Silence is success.** The agent only logs again on failure. If you see repeated errors:

| Message | Cause |
|---|---|
| `server said 404` | A1 not done, or Prometheus not restarted |
| `server said 400 ... out of bounds` | Clock skew (step 2), or A2 not done |
| connection timeout | Temple firewall blocking outbound to port 9090 |

Leave it running for the next step.

### Step 5 — Confirm the data arrived

From your Mac, or any browser:

```bash
curl -s '$PROM/api/v1/query' \
  --data-urlencode 'query=stream_laptop_cpu_percent'
```

You should see your `hub`, `temple` and `device` labels. Then check the derived status is computing —
this proves A3 worked:

```bash
curl -s '$PROM/api/v1/query' \
  --data-urlencode 'query=stream:laptop_status'
```

Expect `1` (healthy) — at any hour, since no stream schedule is configured. If the agent has been
stopped for more than 90s you'll get `0` (offline) instead.

Now `Ctrl+C` the foreground agent.

### Step 6 — Install as a scheduled task

Only once steps 4 and 5 passed. In an **elevated** PowerShell (Run as Administrator):

```powershell
cd C:\StreamMonitor

.\install-windows.ps1 `
  -RemoteWrite "$PROM/api/v1/write" `
  -Hub "Ujjain" `
  -Temple "Khedapati Hanuman temple" `
  -Name "Main hall 1" `
  -Interval 5s `
  -SpeedtestInterval 1m -SpeedtestBytes 10
```

The same settings as step 4, in the installer's naming. Drop the last line to leave the speed test
off; `-SpeedtestInterval 1m` costs about **14 GB per laptop per day**, so enable it on one laptop
first and check the usage before rolling it out.

This copies the exe to `C:\ProgramData\StreamMonitor\`, registers the `StreamMonitorAgent` task to run
as SYSTEM at startup, restarts it on failure (99 retries, 1 min apart), and starts it immediately.

`-Interval` defaults to `15s` if omitted. A shorter interval does **not** shorten the offline buffer:
the agent sizes its queue from the interval so it always holds ~8 hours of readings.

> This installer has **never been run on a real Windows machine**. Expect to debug it on this first
> laptop — that is the point of doing one before the rest.

Verify:
```powershell
Get-ScheduledTask -TaskName StreamMonitorAgent
Get-ScheduledTaskInfo -TaskName StreamMonitorAgent    # LastTaskResult 0 = running fine
Get-Process stream-agent -ErrorAction SilentlyContinue
```

### Step 7 — Reboot test

The whole point is surviving an unattended restart at a temple:

```powershell
Restart-Computer
```

After it comes back, **without logging in if possible**, re-run the step 5 query and confirm fresh
data. The timestamp should be within ~30s.

### Removal

```powershell
Unregister-ScheduledTask -TaskName StreamMonitorAgent -Confirm:$false
Remove-Item -Recurse C:\ProgramData\StreamMonitor
```

---

## Part C — Grafana

**Production is already done.** A `Streaming` folder was created and both dashboards imported on
2026-11-17:

- https://monitor.a4b.io/d/stream-fleet — Streaming laptops (fleet)
- https://monitor.a4b.io/d/stream-device — Streaming laptop (detail)

They point at the `Prometheus` datasource (uid `rrgl-gJSk`) and will read "No data" until A1 and A3
are done. Nothing further is needed on production Grafana.

**Sandbox still needs importing.** On https://monitor-sbox.a4b.io:

1. Dashboards → New → Import → upload `stack/grafana/dashboards/streaming-laptops.json`, then
   `streaming-laptop.json`
2. At the datasource prompt pick the sandbox Prometheus (its uid was not checked — the sandbox
   Grafana API requires a login, unlike production's)
3. Put them in a new `Streaming` folder

The dashboards use a `${datasource}` template variable, so no JSON editing is needed for either
environment. The uids `stream-fleet` and `stream-device` are hardcoded, so the sandbox URLs will be
the same paths under `monitor-sbox.a4b.io`.

---

## Naming — decide before laptop #2

`hub` + `temple` + `name` become Prometheus labels, and the combination **must be unique per laptop**.
Two laptops sharing all three will silently overwrite each other's series, which looks like one
flickering machine rather than an error.

Agree the convention now (e.g. `Hub = city`, `Temple = official temple name`, `Name = physical position`)
and keep a list. Renaming later means re-running the installer, and the old name lingers in history
until it ages out — currently ~15 days, since retention is at the default.

## Scale

~30 series per laptop (7 readings + up to 20 top-programs + 1 info). 15 laptops ≈ 450 series against
an instance already handling 237 scrape targets. Negligible.

## Changed in this repo

`agent/install-windows.ps1` — renamed the internal `$args` variable to `$agentArgs`. `$args` is a
PowerShell automatic variable; assigning to it is legal but shadows the builtin. Untested locally
(no PowerShell on macOS), so this removes one avoidable unknown from tomorrow.
