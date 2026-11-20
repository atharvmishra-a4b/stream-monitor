# Agent

One small program per laptop (about 6 MB, nothing else to install). Every 15 seconds it measures **CPU, memory and network**
(upload, download, interface errors and dropped packets, and how long its report took to send) and pushes them to Prometheus.
Once a minute it also reports the **top 5 programs by CPU and by memory**, grouped by name, so Grafana can show what is using a struggling laptop.
Only program names are sent (for example `obs64.exe`), never window titles, files or command lines.
If the connection drops it keeps up to about 8 hours of readings and sends them when it is back.

The hub, temple and laptop name you give it become labels on every reading. Grafana uses them for its Hub, Temple and Laptop pickers.

## Run it (testing)

```bash
# no install needed if you have Go:
go run . --remote-write http://localhost:9090/api/v1/write --hub Ujjain --temple "Test temple" --name "My Mac"

# or use a prebuilt file from dist/ (see "Build"):
./dist/stream-agent-mac-arm64 --remote-write http://localhost:9090/api/v1/write --hub Ujjain --temple "Test temple" --name "My Mac"
```
Just want to see the numbers without sending anything? Use `--dry-run`.

## Measured download speed (optional)

`--speedtest-interval 1m` downloads 10 MB (`--speedtest-bytes`) over 4 parallel connections
(`--speedtest-parallel`) and
reports what the line actually delivers, whether or not anything else is using it. This exists for
one situation: a stream looks wrong, and the people on site report that the connection is fine.

`stream_laptop_speedtest_download_mbps`, `..._duration_seconds`, `..._age_seconds`, and
`..._failures_total` (a test too slow or too broken to finish is itself a symptom).

Two things to know before turning it on:

- **It costs data.** 10 MB a minute is about **14 GB per laptop per day**. Raise the interval or
  lower the size to cut it. Off by default.
- **On a fast line the figure understates.** A 10 MB download finishes before TCP reaches full
  speed, so a 165 Mbit/s line may read around 90. The error shrinks as the line gets worse, which is
  where accuracy matters: treat readings as a baseline to compare against, not an exact capacity.

Timing starts once a quarter of the bytes have arrived, so TCP's slow opening is excluded at any
speed.

**Point it at your own file instead of Cloudflare** with `--speedtest-url`. Any URL serving range
requests works (S3 and CloudFront both do), and it measures the path to your own infrastructure
rather than to somebody else's edge:

```bash
--speedtest-url "https://cdn.example.com/test/10mb.bin" --speedtest-parallel 1
```

A URL containing `%d` is handed the byte count, as Cloudflare's endpoint expects. Any other URL is
treated as a fixed file and fetched with a `Range` header, so several connections split one file
between them rather than downloading it once each.

`--speedtest-parallel` defaults to 4, because one connection cannot fill a line to a distant
endpoint. Against a nearby CDN that does not apply: measured against a Mumbai edge, one connection
reached 216 Mbit/s and four reached 201, so `1` is the better choice there and costs a quarter of
the data.

## Shared configuration

`--config-url` points the agent at a JSON file, fetched at startup and re-read every few minutes
(`--config-interval`), so the fleet can be retuned from one place instead of one laptop at a time.
Ours is at `https://srm-cdn.a4b.io/obs/monitoring_agent.json`:

```json
{
  "defaults": { "interval": "5s", "speedtest_interval": "1m", "speedtest_bytes": 10 },
  "temples":  { "Maa GajLaxmi temple": { "speedtest_bytes": 50 } }
}
```

Settable: `interval`, `wifi_interval`, `programs_interval`, `speedtest_interval`, `speedtest_bytes`,
`speedtest_parallel`, `speedtest_url`, `graylog_url`, `graylog_token`, `graylog_interval`,
`obs_log_dir`.
Per-temple entries override the defaults, because sites genuinely differ: a laptop on gigabit
Ethernet needs a larger speed-test sample than one on weak Wi-Fi.

Three rules stop a shared file becoming a single point of failure:

- **A flag given on the command line always wins**, so one laptop can be pinned without editing the
  file everyone reads.
- **A failed fetch is never fatal.** The last good document is cached on disk, so a laptop booting
  while its internet is down starts with the settings it had and keeps measuring.
- **Identity is never taken from the file.** `hub`, `temple` and `name` come from the install, so a
  mistake cannot rename the fleet.

Changes to the speed test, log shipping and the measurement interval apply on the next re-read; the
rest take effect at the next restart, which for these laptops is the same day.

## OBS logs to Graylog

`--log-url` ships OBS's own log, which is what explains an incident afterwards: the line that
accounted for the 19 November outage was an ordinary informational line reporting the Wi-Fi rate,
carrying no error at all. **Nothing is filtered** for that reason, and a full day is about a
megabyte per laptop.

Lines are batched and sent every `--log-interval` (default 2m) as newline-delimited GELF, each
carrying `_hub`, `_temple` and `_device` so a Graylog search lines up with the matching Grafana
panel. `--log-token` is sent as the `x-api-key` header and `--log-app-name` as `x-afb-app-name`.

> **A token in the shared config is readable by anyone who can read the config.** `graylog_token`
> works there and is the convenient choice for a file served privately. If the config is a public
> object, pass the token with `-LogToken` at install time or in `STREAM_LOG_TOKEN` instead,
> and leave the key out of the file. Progress is remembered in the state directory, so a restart does not resend.

The agent runs as SYSTEM while OBS runs as the logged-in user, so `%APPDATA%` inside the agent is
not where OBS writes. The user profiles are scanned instead; `--obs-log-dir` overrides that.

`stream_laptop_obslog_shipped_total`, `..._dropped_total` and `..._failures_total` make loss visible
rather than silent.

## How the laptop is connected

The network figures above measure bytes actually moved. They cannot tell a healthy idle link from a
crippled one: a laptop sending 24 Mbit/s looks the same whether the Wi-Fi has 2400 Mbps of headroom
or 43. The agent therefore also reads **how the laptop is attached and how fast that link is**, every
30s (`--wifi-interval`, `0` turns it off). It costs no network traffic and never competes with a stream.

`stream_laptop_wifi_info` describes the connection for **every** laptop, wired or wireless, told
apart by its `kind` label:

| `kind` | Labels | Numeric metrics |
|---|---|---|
| `wifi` | `adapter`, `ssid`, `radio`, `band`, `channel` | tx/rx rate, signal |
| `ethernet` | `adapter` | tx/rx rate (the link speed) |
| `unknown` | `reason` | none |

A temple on a cable and a temple on Wi-Fi therefore appear side by side. `kind="unknown"` exists so
that a laptop whose connection could not be read is never mistaken for one that is simply wired.

Numeric series: `stream_laptop_wifi_tx_rate_mbps`, `..._rx_rate_mbps`, `..._signal_percent`
(Windows), `..._signal_dbm` (macOS). Fields a platform or link type does not report are absent
rather than zero — a wired link has no signal strength, and reporting `0` would read as "no signal".

On Windows the Wi-Fi details come from `netsh wlan show interfaces`, whose labels are localised. If
that yields nothing the agent falls back to PowerShell's `Get-NetAdapter`, whose JSON property names
are English whatever the display language — so a non-English Windows still reports its link, just
without the Wi-Fi specifics.

## OBS metrics (optional)

With `--obs-password` the agent also reads OBS Studio over its built-in WebSocket server, which
answers what CPU and network cannot: **is the stream itself healthy?** Dropped frames, reconnects,
congestion and the real output bitrate.

Enable the server first in OBS: Tools > WebSocket Server Settings > Enable WebSocket server. Then:

```bash
go run . --remote-write ... --hub ... --temple ... --obs-password "<from that dialog>"
```

Metrics are `stream_laptop_obs_*`: `streaming`, `reconnecting`, `congestion`, `fps`,
`output_skipped_frames_total` (network could not keep up), `render_skipped_frames_total` (the laptop
could not keep up), `output_bytes_total`, `frame_render_time_ms`, `disk_free_gb`,
`stream_duration_seconds`.

The `_total` metrics are counters that reset when OBS restarts, so read them with `rate()`.
When OBS closes the series end immediately rather than freezing on their last value. Omit the flag
and nothing changes: no connection is attempted and no OBS metrics are sent.

Check it from the laptop without sending anything: `--dry-run --obs-password ...`.

Options: `--remote-write`, `--hub`, `--temple`, `--name` (default: the computer's name), `--username` and `--password`
(or `STREAM_RW_PASSWORD`) if the Prometheus address needs a login, `--token` for a bearer token, `--interval` (default 15s; the ~8h offline buffer resizes to match), `--programs-interval` (default 1m, `0` turns the top-programs report off).

Each laptop needs a **unique** hub + temple + name combination. Two laptops with the same labels would overwrite each other.
To move a laptop to another temple or rename it, run the agent again (or reinstall) with the new values.

macOS blocks downloaded programs it does not recognise. If it refuses to open, run
`xattr -d com.apple.quarantine dist/stream-agent-mac-arm64` once.

## Fake laptops for trying the dashboards

`go run ./cmd/fleet-sim` pushes seven pretend laptops (healthy, CPU maxed out, memory full, flaky network, switched off) to the same address.

## Build

`./build.sh` writes `dist/stream-agent-windows.exe`, `-mac-arm64`, `-mac-intel` and `-linux` (no dependencies).

## Install on a Windows laptop

Copy `stream-agent-windows.exe` and `install-windows.ps1` to the laptop and, in an elevated PowerShell:

```powershell
.\install-windows.ps1 -RemoteWrite https://<prometheus>/api/v1/write -Hub "Ujjain" -Temple "Temple A" -Name "Main hall 1" -Username <user> -Password <password>
```
It starts with Windows and restarts itself if it stops. The laptop only needs outbound HTTPS, and its clock must be correct.
