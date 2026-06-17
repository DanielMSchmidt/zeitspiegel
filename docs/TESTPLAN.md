# docs/TESTPLAN.md

# Test Plan & Build Order

Test infrastructure: injected `Clock`/`FrameSource`; `synth.Source` encodes
seq + timestamp into a JPEG APP4 segment and pixel pattern; `FakeDisplay`
records (frame, render time). All timing behavior is deterministic without
hardware. CI matrix: `go test -race ./...` (anywhere) · `-tags integration`
(ffmpeg/ffprobe) · `-tags "v4l2 sdl"` build + v4l2loopback (Linux) · arm64
cross build.

## 1. Tier 1 — unit (pure, < 1 s, every commit)

| ID | Component | Case |
|---|---|---|
| UT-1 | ringbuf | Eviction by duration: oldest leaves, order monotonic |
| UT-2 | ringbuf | Eviction by byte budget before duration limit |
| UT-3 | ringbuf | `At`: exact hit, in-between, before-first, after-last, empty |
| UT-4 | ringbuf | rapid property test: any insert sequence ⇒ sorted, budgets never exceeded |
| UT-5 | ringbuf | 1 writer + 3 readers under `-race` |
| UT-6 | engine | Target time with FakeClock; delay change effective next tick |
| UT-7 | engine | FR-4: increase ⇒ selection runs backwards exactly once; decrease ⇒ jump, no double display |
| UT-8 | window | [t−n, t]: count = n·fps ± 1; clamp when under-buffered; empty ⇒ error |
| UT-9 | httpapi | Table-driven validation ⇒ 200/422 (FR-11) |
| UT-10 | config | Parse, defaults, invalid file ⇒ clear startup error |
| UT-11 | screen | `formatDelay` table: 0⇒"00:00", 30s⇒"00:30", 1m30s⇒"01:30", 61m⇒"61:00", 2h⇒"99:59" (clamp), -5s⇒"00:00" (clamp); plus sdl-tagged smoke that `Render` after `SetDelay` succeeds and the glyph texture loads (FR-13) |

## 2. Tier 2 — integration (SyntheticSource, seconds, every PR)

| ID | Case |
|---|---|
| IT-1 | Core property: @60 fps, delay 2.0 s ⇒ every rendered frame has `capture_ts = render_ts − 2.0 s ± 17 ms` (FR-1) |
| IT-2 | Delay change via real HTTP (httptest) effective ≤ 1 frame interval (FR-3) |
| IT-3 | `/clip?seconds=10` ⇒ ffprobe: mp4, duration 10 s ± 1 frame, 600 ± 1 frames (FR-5) |
| IT-4 | Clip first/last frames carry expected seq numbers (window frame-accurate) |
| IT-5 | Export during display ⇒ drop counter stays 0 (FR-6) |
| IT-6 | Warm-up: delay 10 s, 3 s buffered ⇒ oldest frame + `warming_up` (FR-10) |
| IT-7 | Source error ⇒ reconnect with backoff, status degraded, no crash (NFR-5) |
| IT-8 | 3 parallel clips all valid, no interleaving; 4th ⇒ 503 + Retry-After |

## 3. TDD build order (follow strictly)

Spikes (time-boxed throwaway, as soon as hardware exists, parallel to M1):
S-1 = SDL2/KMSDRM decode+render benchmark on Pi (720p60, 1080p30);
S-2 = Kiyo `v4l2-ctl --list-formats-ext` + minimal go4vl capture (validates
MJPEG assumption, documents controls, measures bitrates). Record results in
ARCHITECTURE.md §7.

| Step | Content | Tests |
|---|---|---|
| 1 | frame + ringbuf | UT-1..5 |
| 2 | synth (source/clock/display) — test infra, itself tested | — |
| 3 | engine: tick logic, hard-cut semantics, warm-up | UT-6,7; IT-1, IT-6 |
| 4 | window + export vs real ffmpeg (`integration` tag) | UT-8; IT-3,4 |
| 5 | httpapi + config | UT-9,10; IT-2,5,8 |
| 6 | camera + screen adapters (thin), reconnect supervisor | UT-11; IT-7; ST-1 |
| 7 | wiring, web UI, deploy artifacts, soak | ST-2..6 |

## 4. Tier 3 — system/E2E (real binary, nightly) & milestones

| ID | Case |
|---|---|
| ST-1 | API contract suite vs running process with v4l2loopback (CI, no camera) |
| ST-2 | UI smoke (Playwright): slider ⇒ PUT /delay; download ⇒ MP4 |
| ST-3 | 24 h soak (synth): RSS growth < 5 %, drops < 0.1 % (NFR-1/2) |
| ST-4 | Load: export loop + preview client ⇒ NFR-3/4 held (metrics assertions) |
| ST-5 | systemd kill -9 ⇒ restart, /healthz green < 10 s |
| ST-6 | Power cycle mid-operation ⇒ clean boot to mirror, FS intact (NFR-9, FR-12) |

Milestones: **M1** core (steps 1–3) · **M2** API+export (4–5) · **M3**
hardware (6 + spikes + x264 benchmark + manual glass-to-glass measurement:
film a millisecond stopwatch, measured delay = configured + min_latency_ms
± 1 frame) · **M4** appliance (7 + provisioning walkthrough from blank SD).
