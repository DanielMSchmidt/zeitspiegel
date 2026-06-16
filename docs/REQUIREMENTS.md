# Requirements & API Contract

## 1. Functional requirements

| ID | Requirement | Acceptance criterion |
|---|---|---|
| FR-1 | Delayed full-screen display | Displayed frame capture time = `now − delay ± 1 frame interval` |
| FR-2 | Horizontal mirroring (default on, configurable) | Pixel comparison flipped/unflipped |
| FR-3 | Delay changeable at runtime, 0…`capacity_s`, resolution ≥ 0.1 s. Boot value from `default_delay_s` (default 30 s) | Effective ≤ 1 frame interval after the 200 response |
| FR-4 | Delay change = hard cut | Increase ⇒ past replays once; decrease ⇒ forward jump, no double display |
| FR-5 | Download last *n* seconds as MP4 (H.264 default, MJPEG copy option) | ffprobe-valid, duration = min(n, buffered) ± 1 frame, `X-Clip-Duration` correct |
| FR-6 | Export never blocks capture or display | Drop counter does not increase during export |
| FR-7 | Web UI: delay slider, download input+button, status | Manual + API contract test |
| FR-8 | Status endpoint (see §3) | Schema test |
| FR-9 | Config file for boot defaults incl. camera controls (focus pinning, exposure); runtime changes via API | Restart test; control test vs v4l2loopback/device |
| FR-10 | Warm-up: `delay > filled_s` ⇒ show oldest frame, status `warming_up` | FakeClock test |
| FR-11 | Invalid input ⇒ 422 problem+json with limits | Table-driven API tests |
| FR-12 | Boot to full-screen mirror ≤ 25 s, no login/desktop: Pi OS Lite (no desktop), SDL/KMSDRM fullscreen, getty@tty1 masked, quiet boot (`quiet logo.nologo vt.global_cursor_default=0`, `disable_splash`), unit starts without waiting on the network | Boot timing on target (M4) |

## 2. Non-functional requirements

| ID | Requirement |
|---|---|
| NFR-1 | 720p@60 sustained (default; 1080p@30 alternative); drop rate < 0.1 % over 24 h |
| NFR-2 | RAM ≤ byte budget + 200 MB overhead; no growth over 24 h soak |
| NFR-3 | Display jitter < 1 frame interval (p99) |
| NFR-4 | API < 50 ms (except /clip); 30 s clip export < 5 s on Pi 5 |
| NFR-5 | systemd service, auto-restart, camera reconnect on USB loss |
| NFR-6 | LAN-only, no auth in v1 (documented); bind address configurable; the appliance hosts its own **open** Wi-Fi access point (SSID `zeitspiegel`, no password) — an isolated, internet-less LAN, no venue network involved (E-7) |
| NFR-7 | Core logic 100 % testable without hardware |
| NFR-8 | Structured logs (persistent journal at `/var/log/journal`, so a no-AP / no-screen field appliance can still be post-mortem-debugged after a power cycle) + expvar metrics (drops, fill, export duration) |
| NFR-9 | Unplug tolerance: read-only root (overlayfs), clips on tmpfs. Persistent journal at `/var/log/journal` is the one allowed write path (NFR-8); ext4 journaling keeps it crash-consistent on power loss |
| NFR-10 | Discoverable as `zeitspiegel.local` (mDNS) on the appliance's own AP; fallback address `http://10.42.0.1` (the AP gateway) |

## 3. API contract (v1)

Errors: RFC-9457 `application/problem+json`. Config has single-writer
semantics; capture/display read atomic snapshots.

| Method & path | Purpose | Responses |
|---|---|---|
| `GET /api/v1/status` | `delay_s, fps, resolution, buffer{capacity_s, filled_s, bytes}, dropped_frames, min_latency_ms, warming_up, uptime_s` | 200 |
| `PUT /api/v1/delay` | body `{"seconds": 4.0}`, valid 0…capacity_s | 200 · 422 (limits in body) |
| `GET /api/v1/clip?seconds=n&format=mp4|mjpeg` | last n seconds; clamped if under-buffered, actual length in `X-Clip-Duration` | 200 video/mp4 + Content-Disposition · 422 (n≤0 or >capacity) · 503 + Retry-After (empty buffer / export slots busy) |
| `GET/PATCH /api/v1/config` | `mirror_flip, profile(auto|720p60|1080p30), buffer_max_s, focus_auto, focus_absolute, exposure_*`; profile change ⇒ pipeline restart + buffer cleared (signalled) | 200 · 422 |
| `GET /api/v1/preview?view=live|delayed` | MJPEG preview, throttled ~10 fps; `live` (default) = newest frame, `delayed` = the frame the mirror shows (now − delay, warm-up shows oldest) | 200 multipart/x-mixed-replace · 422 (unknown view) |
| `GET /healthz` | liveness | 200/503 |
| `GET /` | web UI | 200 |

## 4. Decision log

| ID | Decision |
|---|---|
| E-1 | Export: H.264 transcode default, `?format=mjpeg` copy option |
| E-2 | *Revised.* Default profile `auto`: the camera adapter probes for its highest discrete MJPEG mode, capped at 1080p (`config.MaxAuto{Width,Height}`) so software decode stays within the Pi 5 budget; nominal pipeline rate 30 fps. Owner chose spatial sharpness (dancers read the screen from across a room) over the original 720p60 temporal-resolution preference. `720p60`/`1080p30` remain selectable; the engine selects by capture timestamp, so a camera whose real rate differs from the 30 fps nominal stays correct |
| E-3 | Delay change = hard cut (ramp = v2 idea) |
| E-4 | Audio out of scope v1 (architecture admits a second ring later) |
| E-5 | Appliance: Pi OS Lite, KMSDRM, systemd, read-only overlay, tmpfs, Avahi |
| E-6 | *Superseded by E-7.* (Was: regular member Wi-Fi; no Pi-hosted AP) |
| E-7 | Appliance hosts its own **open** (passwordless) Wi-Fi AP (NetworkManager hotspot, `ipv4.method shared`): guests just pick the SSID and connect — no password to print or type. No venue Wi-Fi, no client-isolation issues, mDNS works with no router in between; the Pi never needs internet (packages baked into the image at build time, `make image`). Clients get no internet while connected, and the open AP carries only the no-auth LAN-only control UI (NFR-6) on an isolated network — acceptable for a single-purpose appliance. The join-venue-Wi-Fi variant lives on branch `wifi-client` |
