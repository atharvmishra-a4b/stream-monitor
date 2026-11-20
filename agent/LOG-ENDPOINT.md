# OBS log shipping — what the agent will send

The monitoring agent on each streaming laptop tails OBS Studio's own log file and posts new lines to
an HTTP endpoint. This is the contract it expects.

## Request

```
POST https://api.a4b.io/gw4/log/v1/ingest
Content-Type: application/json
x-afb-app-name: daily_darshan
x-api-key: <token>
```

Both headers are sent in lowercase exactly as written above. Header names are case-insensitive by
spec, so matching case-insensitively is safest, but `x-api-key` is what will arrive.

Body is **newline-delimited JSON** (GELF 1.1) — one object per log line, many per request:

```json
{"version":"1.1","host":"Main hall 1","short_message":"[rtmp stream: 'multi-output'] Interface: Intel(R) Wi-Fi 6E AX211 160MHz (802.11, 2402↓/2402↑ mbps)","timestamp":1795063505.644,"level":6,"_hub":"Ujjain","_temple":"Khedapati Hanuman temple","_device":"Main hall 1","_source":"obs"}
{"version":"1.1","host":"Main hall 1","short_message":"[obs-ffmpeg mpegts muxer / libsrt]: Connection was broken","timestamp":1795063509.368,"level":3,"_hub":"Ujjain","_temple":"Khedapati Hanuman temple","_device":"Main hall 1","_source":"obs"}
{"version":"1.1","host":"Main hall 1","short_message":"Output 'adv_stream': Total frames output: 143043","timestamp":1795063509.368,"level":6,"_hub":"Ujjain","_temple":"Khedapati Hanuman temple","_device":"Main hall 1","_source":"obs"}
```

## Fields

| Field | Type | Meaning |
|---|---|---|
| `version` | string | always `"1.1"` |
| `host` | string | laptop name |
| `short_message` | string | the OBS log line, with its timestamp prefix removed |
| `timestamp` | number | **when the line was written**, unix seconds with milliseconds. Not ingest time. |
| `level` | number | syslog level: `3` error, `4` warning (reconnects), `6` informational |
| `_hub` | string | e.g. `Ujjain` |
| `_temple` | string | e.g. `Khedapati Hanuman temple` |
| `_device` | string | laptop name, same as `host` |
| `_source` | string | always `"obs"` |

`_hub`, `_temple` and `_device` are the important ones: they let a search here line up with the
same laptop's dashboard in Grafana.

**Please index on `timestamp`, not arrival time.** Logs may arrive minutes late after a connection
drop, and the whole point is correlating them with what the dashboards show.

## Response

- **2xx** — accepted. The agent discards the batch and moves on.
- **anything else, or no response** — the agent keeps the lines and retries on its next cycle.

A plain `202 Accepted` with an empty body is fine. The agent does not read the response body.

## Volume and behaviour

- One request every **2 minutes** per laptop (configurable).
- Up to **2000 lines** per request; a quiet period may send a handful.
- About **1 MB per laptop per day**, rising during an incident — that is when the detail is wanted.
- Currently **4 laptops**, expected to grow.
- The agent buffers up to 20,000 lines if the endpoint is unreachable, then drops oldest first.
  Nothing is lost for a short outage.
- Retries are on the next 2-minute cycle, not immediate, so a failing endpoint will not be hammered.

## Auth

Every request carries `x-api-key`. Give us the token and we will configure it; it can be rotated
centrally without touching the laptops. If you need a different scheme as well (bearer, basic), say
so and we will add it.

## Example as curl

```bash
curl -X POST "https://api.a4b.io/gw4/log/v1/ingest" \
  -H "Content-Type: application/json" \
  -H "x-afb-app-name: daily_darshan" \
  -H "x-api-key: <token>" \
  --data-binary $'{"version":"1.1","host":"Main hall 1","short_message":"Output \'adv_stream\': stopping","timestamp":1795063509.368,"level":6,"_hub":"Ujjain","_temple":"Khedapati Hanuman temple","_device":"Main hall 1","_source":"obs"}\n{"version":"1.1","host":"Main hall 1","short_message":"Connection was broken","timestamp":1795063509.368,"level":3,"_hub":"Ujjain","_temple":"Khedapati Hanuman temple","_device":"Main hall 1","_source":"obs"}'
```
