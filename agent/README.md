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

## Wi-Fi link quality

The network figures above measure bytes actually moved. They cannot tell a healthy idle link from a
crippled one: a laptop sending 24 Mbit/s looks the same whether the Wi-Fi has 2400 Mbps of headroom
or 43. The agent therefore also reads the **negotiated link rate and signal strength** every 30s
(`--wifi-interval`, `0` turns it off). It costs no network traffic and never competes with a stream.

`stream_laptop_wifi_tx_rate_mbps`, `..._rx_rate_mbps`, `..._signal_percent` (Windows),
`..._signal_dbm` (macOS), plus `stream_laptop_wifi_info` carrying `ssid`, `radio`, `band` and
`channel` labels, so a drop to 2.4 GHz or a roam to a different access point is visible.

Fields a platform does not report are simply absent rather than reported as zero. On Windows this
reads `netsh wlan show interfaces`, whose labels are **localised**: on a non-English Windows the
metrics will be missing. Check with `netsh wlan show interfaces`.

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
