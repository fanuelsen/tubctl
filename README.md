# tubctl

LAN-only control for Bestway Airjet hot tubs over the Gizwits GAgent protocol. No cloud, no Bestway account.

A small Go server + CLI that speaks directly to the WiFi module in your tub.

## Why

The official Bestway Smart Hub app is the only first-party way to control these tubs from a phone, tubctl uses direct LAN connection: your browser talks to a service on your network, the service talks to the tub over plain TCP. Nothing leaves your house, nothing depends on Bestway's backend staying up (if using the cloud version).

## What it does

- **Web UI**: current/target temperature, toggles for heater/filter/bubbles/lock, schedule heat and filter cycles by start/stop time.
- **CLI**: read state, set attributes, stream live changes from a terminal.
- **HTTP API**: `GET /api/state`, `POST /api/set`.
- Single ~8 MB static binary, ~5 MB RAM at idle.
- Every change is logged with a before/after diff, so you can `docker logs -f tubctl` and audit what's happening on the tub.

## Compatibility

Verified on the **2022 Bestway Airjet** (controller model `S100201`, Gizwits product key `fbc861733103419b9c2a09c71584cfe5`). Should work on other Bestway tubs that expose the Gizwits LAN protocol on TCP 12416 — many of the Airjet/Hydrojet line do — but the datapoint layout in `data/datapoint.json` is specific to this product key. Different firmware may have different byte offsets.

The tub itself must be paired to your WiFi already (see **Pairing** below).

## Quick start

### Docker

```bash
docker run -d --restart unless-stopped \
  --name tubctl \
  -p 3000:3000 \
  -e TUB_HOST=192.168.1.50 \
  fanuelsen/tubctl:latest

# open http://localhost:3000
```

Or with the included compose file:

```bash
git clone https://forgejo.sublog.org/sublog.org/tubctl.git
cd tubctl
TUB_HOST=192.168.1.50 docker compose up -d
```

### Standalone binary

Download from the [Forgejo releases page](https://forgejo.sublog.org/sublog.org/tubctl/releases) (linux/amd64), or build from source:

```bash
go build -o tubctl ./cmd/tubctl
TUB_HOST=192.168.1.50 ./tubctl serve
```

## Configuration

All configuration is via environment variables:

| variable | default | meaning |
|---|---|---|
| `TUB_HOST` | `172.31.0.105` | tub IP on the LAN |
| `TUB_PORT` | `12416` | Gizwits TCP control port |
| `PORT` | `3000` | HTTP server port (serve mode) |
| `TIME_FORMAT` | `24` | UI clock format: `24` or `12` |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

## CLI

The Docker image's default command is `serve`. The binary itself has more subcommands:

```bash
tubctl state                                # read and print current state
tubctl set heat_power=1 temp_set=38         # write attributes, verify each
tubctl set locked=0
tubctl watch                                # poll every 2s, print diffs
tubctl watch -interval 500ms
tubctl serve                                # what Docker runs by default
tubctl help
tubctl version
```

From inside the running container:

```bash
docker exec -it tubctl /tubctl state
docker exec -it tubctl /tubctl set wave_power=1
```

**Writable attributes**: `power`, `heat_power`, `filter_power`, `wave_power`, `locked`, `earth`, `temp_set_unit` (0=°F, 1=°C), `temp_set` (20-40 °C), `heat_appm_min`, `heat_timer_min`, `filter_appm_min`, `filter_timer_min`, `wave_appm_min`, `wave_timer_min`. The `*_appm_min` fields are countdown-to-start timers; the `*_timer_min` fields are duration-while-running timers (both in minutes, uint16).

## HTTP API

| method | path | body | returns |
|---|---|---|---|
| `GET`  | `/api/state`  | — | full current state, JSON |
| `POST` | `/api/set`    | `{"heat_power":1,"temp_set":38}` | new state after the write |
| `GET`  | `/api/health` | — | `{"ok":true,"connected":true}` |
| `GET`  | `/api/config` | — | UI config (e.g. `timeFormat`) |

State JSON shape matches the writable attribute names plus `temp_now` (read-only current water temp), `temp_unit` (`"C"`/`"F"` friendly alias), `heat_temp_reach` (water reached set temp), and `errors` (string list of any active fault flags).

## Pairing

tubctl assumes the tub is already on your home WiFi. Initial pairing still requires the official Bestway iOS/Android app: hold the tub's WiFi button for 10 seconds, then walk through the app's pairing flow — it joins the tub's temporary SoftAP and pushes your home WiFi credentials. tubctl could implement this (the Gizwits protocol command for it is documented) but it would require either monitor-mode WiFi capture to confirm the packet format, or a fake-AP setup; the value-to-effort ratio is low for something used once per device lifetime.

## Protocol notes

tubctl speaks the **Gizwits GAgent LAN protocol** on TCP 12416 (and UDP 12414 for discovery). Plaintext binary, well documented in:

- [Apollon77/node-ph803w PROTOCOL.md](https://github.com/Apollon77/node-ph803w/blob/main/PROTOCOL.md) — protocol reference for a different Gizwits device, same wire format.
- The Gizwits SoC SDK headers, e.g. [xuhongv/StudyInEsp8266/...gizwits_protocol.h](https://github.com/xuhongv/StudyInEsp8266/blob/master/Gizkit_soc_pet/app/Gizwits/gizwits_protocol.h).

A few gotchas worth recording for anyone implementing this from scratch:

- The `attrFlags` field in cmd `0x0093` (control device) is byte-order-exchanged by the device on receipt. Multi-byte attrFlags **must be sent big-endian on the wire** even though the C bitfield struct on the device expects little-endian after reversal. Sending little-endian silently fails — the device acks but ignores the write.
- Status reports use magic byte `0x03` (reply to read) or `0x04` (auto-pushed state-change report). Same payload layout; the parser must accept both.
- The product-specific datapoint layout is fetchable, no auth required:
  ```bash
  curl -H "X-Gizwits-Application-Id: 98754e684ec045528b073876c34c7348" \
    "http://usapi.gizwits.com/app/datapoint?product_key=<your_product_key>"
  ```
  Discover your product key via UDP broadcast on 12414 (see `internal/tub/client.go`).

## Building

```bash
go build -o tubctl ./cmd/tubctl

# static linux build for distribution
CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -trimpath -o tubctl ./cmd/tubctl
```

The static webapp files are embedded into the binary via `embed.FS`, so deployment is just the binary — no separate `public/` to ship.

## Project layout

```
cmd/tubctl/      subcommand dispatch + serve/state/set/watch entrypoints
internal/tub/    Gizwits LAN client (framing, UDP discover, TCP handshake, read/write)
internal/web/    HTTP handlers + embedded webapp (public/index.html)
data/            cached Gizwits datapoint spec for the verified tub
```

## Acknowledgements

- [Apollon77/node-ph803w](https://github.com/Apollon77/node-ph803w) — Gizwits protocol reverse-engineering notes that made this possible.
- [cdpuk/ha-bestway](https://github.com/cdpuk/ha-bestway) — Home Assistant integration that documents the Bestway-flavored attribute names. (Different approach: cdpuk uses the Bestway cloud API and requires a Bestway account; tubctl is LAN-only.)
- [visualapproach/WiFi-remote-for-Bestway-Lay-Z-SPA](https://github.com/visualapproach/WiFi-remote-for-Bestway-Lay-Z-SPA) — documents the underlying tub-controller serial protocol (the hardware-mod alternative to this software-only approach).
