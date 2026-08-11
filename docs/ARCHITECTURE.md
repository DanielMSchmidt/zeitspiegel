# Architecture

## 1. Context

One Go process on a Raspberry Pi 5 turns a UVC webcam + HDMI display into a
time-delayed mirror, controlled over HTTP. Reference camera: a wide-angle
(~100-110° diagonal) fixed-focus UVC webcam with native MJPEG at 1080p30 and
no in-camera AI processing — see E-9 and docs/HARDWARE.md for the selection
criteria and the shortlist, which stays provisional until spike S-2 measures a
real device. (The Razer Kiyo was the original reference; it is discontinued and
its successor is an auto-framing camera, which a mirror cannot use.) Prior-art
research and rejected approaches are summarized inline with each decision.

## 2. Design decisions

### D1 — Own ring buffer, no framework delay
GStreamer's `queue min-threshold-time` stalls/black-screens beyond ~1 s
(GStreamer GitLab #2673 et al.), and buffering decoded raw video costs
~93 MB/s at 1080p30. Instead: one time-indexed ring buffer of compressed
frames `(seq, capture_ts, jpeg_bytes)`. Delay, export and preview are three
read strategies on the same structure — the core becomes pure, hardware-free,
TDD-friendly code.

### D2 — Intra-only codec (MJPEG) in the buffer
Picamera2's `CircularOutput` issues (#226/#323/#815) show the GOP problem:
clips must start at keyframes, raw H.264 needs muxing, frame counts drift.
MJPEG makes every frame independently decodable → frame-accurate delay AND
export with zero special cases, no live encoder (the camera emits MJPEG
natively over UVC). Cost: ~5× RAM vs H.264 (~5 MB/s at 720p60; MJPEG
bitrate is scene-dependent — bright/high-motion scenes spike it, watch
`zeitspiegel_buffer.bytes_per_s`). Production runs 60 s / 1 GiB
(deploy/config.toml): the delay slider stays ≤ 30 s in practice and a
longer buffer only inflates heap and GC pause times. A second GOP-aligned
H.264 ring for very long history is a possible v2, not v1.

### D3 — Native full-screen display, not a browser
SDL2 via KMSDRM renders directly to HDMI without X11. A browser display would
add network jitter and uncontrolled latency; the web UI is control-only
(plus an optional throttled MJPEG preview).

### D4 — Export via ffmpeg subprocess, streamed as fragmented MP4
JPEG frames of the requested window are piped to ffmpeg. Default output is
H.264 (`libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p
-vf scale='min(1280,iw)':-2`) because MJPEG-in-MP4 is effectively unplayable
on phones/browsers and the use case is "watch on the phone, share in a
chat". `?format=mjpeg` remains as a CPU-free stream-copy option (same
container, compatibility unchanged-poor — VLC-class players). Note: Pi 5
has NO hardware H.264 encoder; x264 ultrafast on its A76 cores keeps the
export comfortably faster than realtime (benchmark in M3). The 720p long-
edge cap halves x264 work on 1080p sources at no visible cost for phone
playback. ffmpeg muxes **fragmented MP4 to stdout**
(`-movflags empty_moov+default_base_moof -frag_duration 500000`) and the
HTTP handler streams it chunked as it is produced: the download starts
within ~1 s of the request while the encode runs behind it, instead of
after the whole encode. fMP4 needs no trailing (or rewritten) moov at all —
this supersedes the earlier "+faststart intentionally omitted" note — and
clips never touch storage. 0.5 s fragments rather than `frag_keyframe`
because x264's default 250-frame GOP would hold the first fragment for
~8 s of media. Subprocess isolation > libav bindings (crash isolation, no
binding maintenance).

### D5 — REST API + thin static frontend
Versioned HTTP API is the testable contract; the UI is one embedded
HTML/JS page with no build tooling. The page is a list of cards, one per
mirror on the network and this one included: identical markup and identical
controls (delay, preview, clip), differing only in the base URL they call —
`""` for this unit, the host-issued address for another. Cards rather than
tabs: with two or three mirrors in a room the delays have to be comparable
at a glance, and a mirror hidden behind a tab is one nobody notices has gone
Offline. Each card is built once and then only updated, which is what lets a
slider survive the 1 Hz poll and keep its own hold-off state. Node and
Playwright appear only under `web/uitest` (UI-1..11); the shipped page
depends on neither.

### D6 — Testability as architecture
Injected `Clock` and `FrameSource` interfaces. `synth.Source` emits frames at
an exact rate with seq/timestamp encoded in a JPEG APP4 segment + pixel
pattern; `FakeDisplay` records which frame rendered when. The system's core
property — displayed frame = now − d — is automatically, frame-accurately
testable without hardware.

### D7 — Appliance model
Pi OS Lite, systemd (`Restart=always`), read-only root via overlayfs, clips
streamed straight to the client (never written to storage), volatile journal
logs → pulling the plug is the supported off switch. Discovery via Avahi/mDNS (`zeitspiegel.local`). Details in
docs/DEPLOYMENT.md.

### D8 — Elected roles, not baked ones (multi-unit)
Two or three appliances in one room have to share a network, and per-card
configuration is what makes an installation tedious to operate. So every card
carries the identical image and the role is decided at runtime: a unit scans
for the shared SSID and joins it, or hosts it if nobody does (E-8). There is
only ever one SSID and NetworkManager's shared mode always takes `10.42.0.1`,
so the poster and `zeitspiegel.local` survive a failover.

The shape of the solution is forced by one hardware fact: **a Wi-Fi radio in AP
mode cannot scan.** A host can therefore never observe a second host beaconing
the same SSID, so a split brain is undetectable by observation. Two mechanisms
cover it instead — a short id-derived stagger that makes a simultaneous claim
unlikely, and a blind self-heal. A refused scan is never read as "nobody is
out there" — that is exactly how a split brain gets made.

What a host CAN see, cheaply and even in AP mode, is **who is associated to
it**: `iw dev wlan0 station dump` enumerates every client — member units and
guests' phones alike (standard nl80211; verified for the Pi's brcmfmac, with
the caveat that frequent dumps have been reported to cause momentary drops in
noisy RF, so the supervisor polls it only while the registry is empty and at
most every 10 s, and a failed query keeps the last known count rather than
inventing an empty room). That observation replaces any notion of a
configured fleet size and yields the central rule:

**A host serving anybody holds its network unconditionally. Only a host
serving nobody probes** — after 90 s of empty audience it drops its AP,
scans, joins what it finds, otherwise reclaims. With zero stations there is
nobody to kick, so probing is free by construction; each fruitless probe
doubles the next wait (capped at 8×, offset by the unit's stagger slot), so
a genuinely lonely unit converges to a quiet cadence. Any station or member
appearing resets both the timer and the backoff.

The comparison with lease systems is worth writing down: membership *is*
leased (members renew every 10 s, entries expire after 30 s), but leadership
cannot be, because there is no arbiter — nothing can fence a stale host,
which is why recovery has to be a *self*-heal. The audience rule makes the
convergence audit short: a 0+0 split (cold-boot collision) heals when the
first prober sees the other's beacon; a 1+0 split heals because only the
empty side probes; a solo host with a phone attached never probes at all —
so a single station running for a whole afternoon never once takes the
Wi-Fi away from the dancer using it. The accepted residual is a phone+phone
split: both sides hold, each fully functional for its own users, and any
power cycle resolves it.

Promotion generalises "the second one takes over" to "the lowest surviving id
goes first, at position × PromoteStep (10 s)", so the fleet cannot deadlock
waiting for a designated successor that is also dead — after a host's power
is cut the network is typically back in 15–20 s and phones rejoin the same
open SSID on their own. A member waiting its promotion turn that sees the
SSID reappear joins immediately (rejoining can never create a second AP), so
a transient Wi-Fi blip costs seconds, not a promotion slot. A returning
ex-host never preempts: a handback would cost the room a second outage for
nothing.

The decision logic is pure (`internal/netrole`: injected clock, no I/O) and the
radio sits behind an interface (`internal/fleet`), so cold start, failover and
split-brain recovery are all testable with no radios and no root. The nmcli
adapter is a subprocess for the same reasons ffmpeg is (hard rule 7). Both
NetworkManager profiles are baked with `autoconnect=false` so the binary, not
NetworkManager, decides which is up — and nothing has to be written to the
read-only root at runtime.

Membership (`internal/peers`) lives only in RAM on the hosting unit. Members
announce to their default gateway — the host, by construction — carrying no
address of their own; the host fills it in from the connection. That is what
lets a member ship with nothing about the network configured. The combined page
then calls each unit directly rather than being proxied through the host, which
keeps a clip download off a second wireless hop.

## 3. Components

```
Camera ──MJPEG/V4L2──► capture worker ──► ring buffer (RAM)
                                            │        │      │
                              reader t−d ───┘        │      │
                          display renderer     clip exporter  preview
                          SDL2/KMSDRM decode   ffmpeg → MP4   MJPEG stream
                          + hflip
                                 web server: REST API + static UI ◄── browser
```

- **Capture worker** (sole buffer writer): reads V4L2 MJPEG, stamps frames
  with the monotonic clock, pushes; counts drops. A reconnect supervisor
  reopens the device with backoff on USB errors.
- **Ring buffer** (`internal/ringbuf`): slice-based deque, `sync.RWMutex`
  (1 writer @60 Hz, few readers — contention irrelevant). `At(t)` = newest
  frame with `ts ≤ t`, by binary search. Eviction in `Push`: pop-front while
  duration > max OR bytes > cap. Frames are immutable; readers get shared
  slices; GC reclaims evicted frames once no export holds them → a running
  export "pins" its frames for free (this is how FR-6 is satisfied).
- **Engine** (`internal/engine`): pure frame-selection logic — which frame
  belongs to tick t; delay-change semantics (hard cut: increase replays the
  past once, decrease jumps forward); warm-up (delay > buffered ⇒ show oldest,
  report `warming_up`).
- **Display renderer** (`internal/screen`): per tick `buf.At(t − delay)`
  where t is the ticker's fire time; if the selected frame is unchanged
  (identity = seq + capture timestamp; seq alone restarts at 0 on a source
  reconnect) → no-op; else decode (SDL2_image/libjpeg-turbo) into a
  persistent `SDL_TEXTUREACCESS_STREAMING` texture (recreated only on a
  dimension/format change — per-frame texture create/destroy causes
  periodic driver hitches on KMSDRM/GLES), render with
  `RenderCopyEx(..., FLIP_HORIZONTAL)` into an aspect-preserving destination
  rect — the frame is fitted and centred, not stretched to fill, and the
  remainder is cleared to black (FR-16); a mirror used to judge body line must
  not distort proportion. The negotiated renderer (name,
  software fallback, display refresh rate) is logged at startup and
  published via expvar. Budget @60 fps = 16.7 ms; expected on Pi 5: 720p
  decode 4–8 ms + present 2–4 ms (validate in spike S-1). Fallbacks:
  decode in worker goroutine (+1 tick latency, irrelevant for a mirror) or
  a 30 fps profile.
- **Exporter** (`internal/window` + `internal/export`): window [t−n, t] →
  ffmpeg stdin → fragmented MP4 on stdout → streamed into the chunked HTTP
  response (headers, including `X-Clip-Duration`, go out first — the
  duration is known from the window cut before ffmpeg starts). One export
  slot in production (semaphore), then 503 + Retry-After; the slot is held
  for the whole download, and a stalled client is cut by a rolling 30 s
  per-write deadline so it cannot pin the slot and its frames. The ffmpeg
  child is reniced (+10) so x264 on the Pi's four cores cannot starve the
  render loop's tick budget.
- **HTTP layer** (`internal/httpapi`): stdlib ServeMux patterns; handlers
  depend on small interfaces (StatusProvider, DelaySetter, ClipExporter,
  PeerStore).
- **Fleet** (`internal/netrole` + `internal/fleet` + `internal/peers` +
  `internal/identity`, D8): the election state machine, the supervisor that
  drives it against a radio, the in-RAM membership list, and the unit's
  identity (CPU serial for the id, boot-partition file for the name). None of
  it touches the display path — a unit that cannot sort out its place on the
  network still mirrors perfectly, since `zeitspiegel.service` is ordered
  after `local-fs.target` only and never waits on the network.

## 4. Concurrency model

- main goroutine: SDL render loop (`runtime.LockOSThread` — SDL needs the
  main thread); everything else started alongside under an errgroup + ctx.
- capture goroutine → `Buffer.Push` (single writer).
- delay value: `atomic.Int64` nanoseconds; written by HTTP handler, read by
  render loop each tick → "effective ≤ 1 tick" by construction (FR-3).
- source channel capacity 4; on overflow drop oldest + increment
  `dropped_frames`; capture never blocks.
- render loop ticks at a fixed 60 Hz (the highest nominal profile rate):
  the ticker is created once but the profile can change at runtime; extra
  ticks are nearly free because the engine renders only when the selected
  frame changed. The exporter reads the nominal fps from the runtime
  profile per export for the same reason. Frame selection uses each tick's
  fire time (the value delivered on the ticker channel), not the wall
  clock at processing time, so a render that overruns does not also skew
  which frame the next tick picks; tick overruns, over-budget renders,
  selection misses, and held-frame streaks are counted into
  `zeitspiegel_render` (expvar), capture-timeline holes into
  `zeitspiegel_capture` (a hole replays on screen `delay` seconds later).
- shutdown: `signal.NotifyContext`; clean close matters for dev/tests (in
  production the plug is pulled, which NFR-9 makes safe).

## 5. Tech stack

Go ≥ 1.24. External modules (closed list, see CLAUDE.md):
`vladimirvivien/go4vl` (V4L2 MJPEG capture + controls),
`veandco/go-sdl2` + `img` (KMSDRM display, JPEG decode via libjpeg-turbo),
`BurntSushi/toml` (config), `pgregory.net/rapid` (property tests).
Stdlib for HTTP (`net/http` + `httptest`), logging (`slog`), metrics
(`expvar`), embedding (`embed`). ffmpeg/ffprobe as subprocesses.
cgo note: go4vl and go-sdl2 are cgo → Pi binary is built on-device or via
zig-cc/Docker arm64; core tests never need cgo.
Dev fallback source: without the v4l2 tag, `--source camera` captures via an
ffmpeg subprocess (internal/ffcam — avfoundation on macOS, v4l2 demuxer on
Linux; pure Go). Camera controls (FR-9) apply only on the go4vl path; the
appliance always builds with the tags. `device = "auto"` (default) picks the
first node that actually streams (the Kiyo also enumerates a metadata node).

## 6. Latency floor

Exposure + USB + decode + render + vsync ≈ 60–120 ms. `delay = 0` means
"minimum system latency"; reported as `min_latency_ms` in /status.

## 7. Measured numbers (filled by spikes)

| Measurement | Spike | Value |
|---|---|---|
| Camera MJPEG bitrate @1080p30, **well-lit** studio | S-2 | _tbd_ |
| Camera MJPEG bitrate @1080p30, **dim** studio (high gain ⇒ noise ⇒ bitrate; this is what sizes `buffer_max_bytes`) | S-2 | _tbd_ |
| Camera controls actually implemented (`v4l2-ctl --list-ctrls`); focus controls absent on a fixed-focus device is the expected result (E-9) | S-2 | _tbd_ |
| Discrete MJPEG modes offered (`--list-formats-ext`), and the frame rate of the largest — guards the known `probeMaxMJPEG` area-vs-rate issue | S-2 | _tbd_ |
| 720p JPEG decode+render per frame (Pi 5) | S-1 | _tbd_ |
| x264 ultrafast export speed, 30 s clip (Pi 5) — encode wall time (`zeitspiegel_export_seconds` to a fast client) + time to first byte (`zeitspiegel_export_ttfb_seconds`) | M3 | _tbd_ |
| Bright-scene MJPEG bitrate @1080p30 (`zeitspiegel_buffer.bytes_per_s` peak) | prod | _tbd_ |
| Render over-budget tick ratio under bright-scene stress (`zeitspiegel_render`) | prod | _tbd_ |