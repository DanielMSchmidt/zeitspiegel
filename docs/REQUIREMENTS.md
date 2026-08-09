# Requirements & API Contract

## 1. Functional requirements

| ID | Requirement | Acceptance criterion |
|---|---|---|
| FR-1 | Delayed full-screen display | Displayed frame capture time = `now − delay ± 1 frame interval` |
| FR-2 | Horizontal mirroring (default on, configurable) | Pixel comparison flipped/unflipped |
| FR-3 | Delay changeable at runtime, 0…`capacity_s`, resolution ≥ 0.1 s. Boot value from `default_delay_s` (default 15 s) | Effective ≤ 1 frame interval after the 200 response |
| FR-4 | Delay change = hard cut | Increase ⇒ past replays once; decrease ⇒ forward jump, no double display |
| FR-5 | Download last *n* seconds as MP4 (H.264 default, MJPEG copy option) | ffprobe-valid, duration = min(n, buffered) ± 1 frame, `X-Clip-Duration` correct. Clips are fragmented MP4: frame count probes via `-count_packets`/`nb_read_packets` (`empty_moov` carries no `nb_frames` sample table) |
| FR-6 | Export never blocks capture or display | Drop counter does not increase during export |
| FR-7 | Web UI: delay slider, full-buffer download button (fixed length = `capacity_s`; the API keeps free-form `seconds`), status | Manual + API contract test |
| FR-8 | Status endpoint (see §3) | Schema test |
| FR-9 | Config file for boot defaults incl. camera controls (focus pinning, exposure); runtime changes via API | Restart test; control test vs v4l2loopback/device |
| FR-10 | Warm-up: `delay > filled_s` ⇒ show oldest frame, status `warming_up` | FakeClock test |
| FR-11 | Invalid input ⇒ 422 problem+json with limits | Table-driven API tests |
| FR-12 | Boot to full-screen mirror ≤ 25 s, no login/desktop: Pi OS Lite (no desktop), SDL/KMSDRM fullscreen, getty@tty1 masked, quiet boot (`quiet logo.nologo vt.global_cursor_default=0`, `disable_splash`), unit starts without waiting on the network | Boot timing on target (M4) |
| FR-13 | On-screen delay indicator: top-right badge showing current delay as `Ns delay` (whole seconds), white on opaque black, always visible, not mirror-flipped | UT-11 + manual visual check |
| FR-14 | Several appliances on one network are controlled from one page, each with its own independently settable delay; changing one must not affect another. A lone appliance's page is unchanged | IT-10, ST-8; ST-7 for the listing |
| FR-15 | Role election: on boot a unit looks for the shared `zeitspiegel` network and joins it, or hosts it if nobody does. If the host disappears exactly one survivor takes over; a returning ex-host rejoins as an ordinary member without preempting. Every card runs the identical image (E-8) | UT-19, IT-11, ST-7/9/10/11 |

## 2. Non-functional requirements

| ID | Requirement |
|---|---|
| NFR-1 | 720p@60 sustained (default; 1080p@30 alternative); drop rate < 0.1 % over 24 h |
| NFR-2 | RAM ≤ byte budget + 200 MB overhead; no growth over 24 h soak |
| NFR-3 | Display jitter < 1 frame interval (p99) |
| NFR-4 | API < 50 ms (except /clip); /clip streams — first response byte < 1 s, encode of a 30 s clip completes < 5 s on Pi 5 (total download time is client-bound) |
| NFR-5 | systemd service, auto-restart, camera reconnect on USB loss |
| NFR-6 | LAN-only, no auth in v1 (documented); bind address configurable; the appliance hosts its own **open** Wi-Fi access point (SSID `zeitspiegel`, no password) — an isolated, internet-less LAN, no venue network involved (E-7). With several units, exactly one hosts that network and the rest join it (E-8). API responses carry `Access-Control-Allow-Origin: *` and answer `OPTIONS` preflight, so the combined page can call every unit directly instead of being proxied through the host; this widens nothing, since the LAN is already unauthenticated and has no route to the internet in either direction |
| NFR-7 | Core logic 100 % testable without hardware |
| NFR-8 | Structured logs (persistent journal at `/var/log/journal`, so a no-AP / no-screen field appliance can still be post-mortem-debugged after a power cycle) + expvar metrics (drops, fill, export duration [stream-complete, download-inclusive] + export time-to-first-byte, render duration / over-budget ticks, tick overruns, buffer misses, held-frame streaks, capture gaps, frame-size / bitrate, renderer + display info) |
| NFR-9 | Unplug tolerance: read-only root (overlayfs); clips are streamed to the client and never written to storage. Persistent journal at `/var/log/journal` is the one allowed write path (NFR-8); ext4 journaling keeps it crash-consistent on power loss |
| NFR-10 | Discoverable as `zeitspiegel.local` (mDNS) on the appliance's own AP; fallback address `http://10.42.0.1` (the AP gateway). Whichever unit is hosting answers to `zeitspiegel.local` and takes `10.42.0.1`, so both stay valid across a failover; the others answer to `zeitspiegel-<unit id>.local`. Hostnames are set transiently (`hostnamectl --transient`) because `/etc/hostname` is read-only once the overlay is sealed |

## 3. API contract (v1)

Errors: RFC-9457 `application/problem+json`. Config has single-writer
semantics; capture/display read atomic snapshots.

| Method & path | Purpose | Responses |
|---|---|---|
| `GET /api/v1/status` | `delay_s, fps, resolution, buffer{capacity_s, filled_s, bytes}, dropped_frames, min_latency_ms, warming_up, uptime_s, unit_id, name, role(primary\|member\|unknown), fleet_size` | 200 |
| `PUT /api/v1/delay` | body `{"seconds": 4.0}`, valid 0…capacity_s | 200 · 422 (limits in body) |
| `GET /api/v1/clip?seconds=n&format=mp4|mjpeg` | last n seconds; clamped if under-buffered, actual length in `X-Clip-Duration` (sent before the body). Response streams while encoding: chunked, no `Content-Length`; a mid-stream failure truncates the download (no second status). `Retry-After: 2` is optimistic for slow clients — the slot is held for the whole download | 200 video/mp4 + Content-Disposition · 422 (n≤0 or >capacity) · 503 + Retry-After (empty buffer / export slots busy) |
| `GET/PATCH /api/v1/config` | `mirror_flip, profile(auto|720p60|1080p30), buffer_max_s (0 < s ≤ 86400), focus_auto, focus_absolute, exposure_*`; profile change ⇒ pipeline restart + buffer cleared (signalled) | 200 · 422 |
| `GET /api/v1/preview?view=live|delayed` | MJPEG preview, throttled ~10 fps; `live` (default) = newest frame, `delayed` = the frame the mirror shows (now − delay, warm-up shows oldest) | 200 multipart/x-mixed-replace · 422 (unknown view) |
| `POST /api/v1/peers` | member heartbeat, body `{"id","name","port"}`. Carries **no address**: the host derives `base_url` from the connection's remote address plus the announced port, so a member never has to discover its own DHCP lease. Replies `{"id","base_url","roster","position"}` — the roster and position are what give promotion its order (FR-15). Registering with the host's own id is rejected | 200 · 422 |
| `GET /api/v1/peers` | `{"peers":[{"id","name","base_url","age_s"}]}`, sorted by id, entries expiring after 3 missed heartbeats | 200 |
| `GET /healthz` | liveness | 200/503 |
| `GET /` | web UI | 200 |

Both `/api/v1/peers` routes exist only on a unit that keeps a membership list
(`fleet_size > 1`); a lone appliance returns 404, which is how the page knows
to stop asking. All API responses carry `Access-Control-Allow-Origin: *` and
`OPTIONS` is answered with 204 (NFR-6).

## 4. Decision log

| ID | Decision |
|---|---|
| E-1 | Export: H.264 transcode default, `?format=mjpeg` copy option |
| E-2 | *Revised.* Default profile `auto`: the camera adapter probes for its highest discrete MJPEG mode, capped at 1080p (`config.MaxAuto{Width,Height}`) so software decode stays within the Pi 5 budget; nominal pipeline rate 30 fps. Owner chose spatial sharpness (dancers read the screen from across a room) over the original 720p60 temporal-resolution preference. `720p60`/`1080p30` remain selectable; the engine selects by capture timestamp, so a camera whose real rate differs from the 30 fps nominal stays correct |
| E-3 | Delay change = hard cut (ramp = v2 idea) |
| E-4 | Audio out of scope v1 (architecture admits a second ring later) |
| E-5 | Appliance: Pi OS Lite, KMSDRM, systemd, read-only overlay, Avahi; clips stream to the client (no clip storage, tmpfs or otherwise) |
| E-6 | *Superseded by E-7.* (Was: regular member Wi-Fi; no Pi-hosted AP) |
| E-8 | **Self-electing fleet**, supplementing E-7 rather than superseding it — a lone unit still behaves exactly as E-7 describes. Every card carries the identical image; the role is elected at boot instead of baked, because per-card configuration is what makes an installation tedious to operate. A unit looks for the shared SSID and joins it, or hosts it if nobody does. There is only ever **one** SSID, so the printed poster, the QR codes and `zeitspiegel.local` stay valid whichever unit is hosting. The whole design is shaped by one hardware fact: a radio in AP mode cannot scan, so a host can never observe a second host on the same SSID. Two mechanisms cover that blind spot — an id-derived stagger before claiming, and a host that stays short of `fleet_size` demoting to look around, with exponential backoff so a switched-off peer costs a few brief interruptions rather than one every 90 s forever. The combined page talks to each unit directly (NFR-6 CORS) rather than being proxied, keeping clip downloads off a second wireless hop. `fleet_size` is the one configured value and is the same on every card; the display name comes from a file on the FAT32 boot partition, which stays writable when the root overlay is sealed |
| E-7 | Appliance hosts its own **open** (passwordless) Wi-Fi AP (NetworkManager hotspot, `ipv4.method shared`): guests just pick the SSID and connect — no password to print or type. No venue Wi-Fi, no client-isolation issues, mDNS works with no router in between; the Pi never needs internet (packages baked into the image at build time, `make image`). Clients get no internet while connected, and the open AP carries only the no-auth LAN-only control UI (NFR-6) on an isolated network — acceptable for a single-purpose appliance. The join-venue-Wi-Fi variant lives on branch `wifi-client` |
