# Architecture

## 1. Context

One Go process on a Raspberry Pi 5 turns a UVC webcam + HDMI display into a
time-delayed mirror, controlled over HTTP. Reference camera: Razer Kiyo
(native MJPEG, 720p@60 / 1080p@30, hardware ring light). Prior-art research
and rejected approaches are summarized inline with each decision.

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
natively over UVC). Cost: ~5× RAM vs H.264 (~5 MB/s at 720p60; 120 s ≈
600 MB) — acceptable. A second GOP-aligned H.264 ring for very long history
is a possible v2, not v1.

### D3 — Native full-screen display, not a browser
SDL2 via KMSDRM renders directly to HDMI without X11. A browser display would
add network jitter and uncontrolled latency; the web UI is control-only
(plus an optional throttled MJPEG preview).

### D4 — Export via ffmpeg subprocess
JPEG frames of the requested window are piped to ffmpeg. Default output is
H.264 (`libx264 -preset ultrafast -crf 23 -pix_fmt yuv420p
-vf scale='min(1280,iw)':-2`) because MJPEG-in-MP4 is effectively unplayable
on phones/browsers and the use case is "watch on the phone, share in a
chat". `?format=mjpeg` remains as a CPU-free stream-copy option. Note: Pi 5
has NO hardware H.264 encoder; x264 ultrafast on its A76 cores keeps the
export comfortably faster than realtime (benchmark in M3). The 720p long-
edge cap halves x264 work on 1080p sources at no visible cost for phone
playback. `+faststart` is intentionally omitted: clips are downloaded then
played locally, so the second-pass moov rewrite is wasted work. Subprocess
isolation > libav bindings (crash isolation, no binding maintenance).

### D5 — REST API + thin static frontend
Versioned HTTP API is the testable contract; the UI is one embedded
HTML/JS page with no build tooling.

### D6 — Testability as architecture
Injected `Clock` and `FrameSource` interfaces. `synth.Source` emits frames at
an exact rate with seq/timestamp encoded in a JPEG APP4 segment + pixel
pattern; `FakeDisplay` records which frame rendered when. The system's core
property — displayed frame = now − d — is automatically, frame-accurately
testable without hardware.

### D7 — Appliance model
Pi OS Lite, systemd (`Restart=always`), read-only root via overlayfs, clips
on tmpfs, volatile journal logs → pulling the plug is the supported off
switch. Discovery via Avahi/mDNS (`zeitspiegel.local`). Details in
docs/DEPLOYMENT.md.

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
- **Display renderer** (`internal/screen`): per tick `buf.At(now − delay)`;
  if `Seq` unchanged → no-op; else decode (SDL2_image/libjpeg-turbo), render
  with `RenderCopyEx(..., FLIP_HORIZONTAL)`. Budget @60 fps = 16.7 ms;
  expected on Pi 5: 720p decode 4–8 ms + present 2–4 ms (validate in spike
  S-1). Fallbacks: decode in worker goroutine (+1 tick latency, irrelevant
  for a mirror) or a 30 fps profile.
- **Exporter** (`internal/window` + `internal/export`): window [t−n, t] →
  ffmpeg stdin → tmpfs file → `http.ServeFile` → cleanup. Max 3 concurrent
  exports (semaphore), then 503 + Retry-After.
- **HTTP layer** (`internal/httpapi`): stdlib ServeMux patterns; handlers
  depend on small interfaces (StatusProvider, DelaySetter, ClipExporter).

## 4. Concurrency model

- main goroutine: SDL render loop (`runtime.LockOSThread` — SDL needs the
  main thread); everything else started alongside under an errgroup + ctx.
- capture goroutine → `Buffer.Push` (single writer).
- delay value: `atomic.Int64` nanoseconds; written by HTTP handler, read by
  render loop each tick → "effective ≤ 1 tick" by construction (FR-3).
- source channel capacity 4; on overflow drop oldest + increment
  `dropped_frames`; capture never blocks.
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
| Kiyo MJPEG bitrate @720p60 / @1080p30 | S-2 | _tbd_ |
| 720p JPEG decode+render per frame (Pi 5) | S-1 | _tbd_ |
| x264 ultrafast export speed, 30 s clip (Pi 5) | M3 | _tbd_ |