# docs/TESTPLAN.md

# Test Plan & Build Order

Test infrastructure: injected `Clock`/`FrameSource`; `synth.Source` encodes
seq + timestamp into a JPEG APP4 segment and pixel pattern; `FakeDisplay`
records (frame, render time). All timing behavior is deterministic without
hardware. CI matrix: `go test -race ./...` (anywhere) · `-tags integration`
(ffmpeg/ffprobe) · `-tags e2e` (`make test-e2e`: three real binaries electing
a role over a virtual airspace — no ffmpeg, no radios, no root) ·
`-tags "v4l2 sdl"` build + v4l2loopback (Linux) · arm64 cross build.

The multi-unit lane fakes only the radio, and fakes it faithfully in the ways
that decide the design (§D8): a beaconing unit cannot scan, two units can
beacon the same SSID while blind to each other, and a beacon that stops being
refreshed goes off the air — which is what makes SIGKILL there behave like
pulling a plug. Election timings are injected and compressed via `--net-scale`,
so a full failover takes seconds.

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
| UT-11 | screen | `formatDelay` table: 0⇒"0s delay", 2s⇒"2s delay", 30s⇒"30s delay", 90s⇒"90s delay", 999ms⇒"0s delay" (truncate), -5s⇒"0s delay" (clamp), 4h⇒"9999s delay" (clamp); plus sdl-tagged smoke that `Render` after `SetDelay` succeeds and the glyph texture loads (FR-13) |
| UT-12 | engine | `Selection.Miss`: `MissEmpty` on empty buffer; `MissTooEarly` when the target precedes the oldest frame (oldest still shown, `WarmingUp` unchanged); `MissNone` on a hit; `Err` stays nil for both miss kinds |
| UT-13 | cmd | `renderLoop.step`: the tick timestamp (not wall clock) reaches `Engine.Tick`; tick overrun counted when the tick-to-tick delta exceeds 1.5× budget; over-budget render counted; miss kinds and held-frame streaks counted (NFR-3 observability) |
| UT-14 | capture | CaptureTS delta > injected `GapThreshold` ⇒ `Gaps()` increments and `OnGap` fires; no gap counted across a source reopen; `MaxFrameBytes()` tracks the largest payload |
| UT-15 | screen | sdl-tagged: streaming frame texture reused across same-size frames (`TextureRecreates()` stays 1), recreated on dimension change (⇒ 2); `Info()` reports a non-empty renderer name (`make test-hw` lane only) |
| UT-16 | export | `applyNice` lowers a child process's priority (getpriority == nice); `Exporter.Nice` is applied to the ffmpeg child in `Stream.WriteTo` |
| UT-17 | config | `deploy/config.toml` loads cleanly via `config.Load`; `buffer_max_s == 60` (production capacity guard) |
| UT-19 | netrole | Election table under FakeClock: lone unit ⇒ AP; SSID present ⇒ join; two ids land in different stagger slots; claim only after the stagger expires; a **refused scan never claims the SSID** (a beaconing radio cannot scan — that is how a split brain would be made); join timeout ⇒ back to searching; promotion at position × PromoteStep; healthy host never demotes; `fleet_size = 1` never demotes; short-fleet host demotes; a returning peer resets the timer; each fruitless heal backs the next one off, capped |
| UT-20 | config | `fleet_size`, `network_manage`, `name_file` parse + defaults; out-of-range `fleet_size` ⇒ clear startup error; `deploy/config.toml` manages the radio |
| UT-21 | identity | Unit id from a `/proc/cpuinfo` fixture, deterministic; MAC fallback; random fallback reported **unstable**; name file missing / blank / whitespace / CRLF / second line / over-length; slug validation; explicit overrides |
| UT-22 | peers | Register/List; re-register replaces and refreshes; TTL expiry under FakeClock; sorted List; self-id and malformed input ⇒ `ErrInvalid`; over-long name truncated not refused; roster includes self; Position; `-race` on concurrent read/write |
| UT-23 | httpapi | `POST /api/v1/peers` derives `base_url` from the connection; roster + position in the reply; 422 table (bad id, blank name, self id, port range, malformed JSON); `GET /api/v1/peers` shape; routes absent without a registry; status carries `unit_id`/`name`/`role`/`fleet_size` |
| UT-24 | httpapi | CORS headers on API responses; `OPTIONS` preflight ⇒ 204 with the PUT method and Content-Type header allowed; a real PUT still works through the wrapper |
| UT-25 | peers | Announcer posts to the gateway with no address of its own; missing gateway and non-200 surface as errors; `Run` announces immediately and keeps announcing after failures |
| UT-18 | httpapi | Streaming clip handler: headers (`X-Clip-Duration`, no `Content-Length`) precede the chunked body; pre-flight busy/empty still 503; exporter failing before the first body byte ⇒ 500 problem+json; failure mid-stream ⇒ truncated body, no second response; the stream is Closed exactly once |

## 2. Tier 2 — integration (SyntheticSource, seconds, every PR)

| ID | Case |
|---|---|
| IT-1 | Core property: @60 fps, delay 2.0 s ⇒ every rendered frame has `capture_ts = render_ts − 2.0 s ± 17 ms` (FR-1) |
| IT-2 | Delay change via real HTTP (httptest) effective ≤ 1 frame interval (FR-3) |
| IT-3 | `/clip?seconds=10` ⇒ save the streamed body, then ffprobe: fragmented mp4, duration 10 s ± 1 frame, 600 ± 1 packets via `-count_packets`/`nb_read_packets` (`empty_moov` files carry no `nb_frames` sample table) (FR-5) |
| IT-4 | Clip first/last frames carry expected seq numbers (window frame-accurate) |
| IT-5 | Export during display ⇒ drop counter stays 0 (FR-6) |
| IT-6 | Warm-up: delay 10 s, 3 s buffered ⇒ oldest frame + `warming_up` (FR-10) |
| IT-7 | Source error ⇒ reconnect with backoff, status degraded, no crash (NFR-5) |
| IT-8 | 3 parallel clips all valid, no interleaving; 4th ⇒ 503 + Retry-After (slots are held for the whole download, so this also proves 3 concurrent streamed downloads) |
| IT-9 | Clip body streams: first container bytes readable while the encode is still running; client disconnect kills ffmpeg and frees the slot promptly |
| IT-10 | Two units over real HTTP (httptest): the member announces carrying no address, the host lists it, and the member's slider — driven through the address the host handed out — changes only that unit's delay (FR-14) |
| IT-11 | Three units against a fake radio whose airspace is faithful in the ways that matter (a beaconing unit cannot scan; two can beacon at once): cold start ⇒ exactly one host; failover; losing two of three; an ex-host rejoining as a member with the AP count never leaving 1; a split brain collapsing back to one network; a fleet of one never dropping its network (FR-15) |

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
| 4 | window + export vs real ffmpeg (`integration` tag) | UT-8; IT-3,4,9 |
| 5 | httpapi + config | UT-9,10,18; IT-2,5,8 |
| 6 | camera + screen adapters (thin), reconnect supervisor | UT-11; IT-7; ST-1 |
| 7 | wiring, web UI, deploy artifacts, soak | ST-2..6 |
| 8 | observability + stutter hardening (render metrics, capture gaps, streaming texture, export nice, 60 s capacity) | UT-12..17; ST-4 |
| 9 | multi-unit: election, membership, combined page, one image (E-8) | UT-19..25; IT-10,11; ST-7..12 |

Step 9 in order: netrole (UT-19) → config + identity (UT-20, UT-21) → peers
(UT-22, UT-25) → httpapi (UT-23, UT-24) → the fleet supervisor and IT-11 →
IT-10 → cmd wiring and the radio adapters → the combined page → ST-7..12.
The election is built before anything can call it, and the E2E lane last,
because it exercises the whole thing through real processes.

## 4. Tier 3 — system/E2E (real binary, nightly) & milestones

| ID | Case |
|---|---|
| ST-1 | API contract suite vs running process with v4l2loopback (CI, no camera) |
| ST-2 | UI smoke (Playwright): slider ⇒ PUT /delay; download ⇒ MP4 |
| ST-3 | 24 h soak (synth): RSS growth < 5 %, drops < 0.1 % (NFR-1/2) |
| ST-4 | Load: export loop + preview client ⇒ NFR-3/4 held: `zeitspiegel_render.render_over_budget/ticks < 1 %`, `tick_overruns ≈ 0`, `miss_too_early = miss_empty = 0`, `held_streak_max ≤ 2` |
| ST-5 | systemd kill -9 ⇒ restart, /healthz green < 10 s |
| ST-6 | Power cycle mid-operation ⇒ clean boot to mirror, FS intact (NFR-9, FR-12) |
| ST-7 | Three **real binaries** cold-start over a virtual airspace ⇒ exactly one reports `role=primary`, the other two register, and the host lists both with the right names and connection-derived addresses |
| ST-8 | Three independent delays end to end: each unit's slider driven through the address the host handed out, and moving one leaves the others alone (FR-14) |
| ST-9 | SIGKILL the host process (its beacon stops being refreshed, exactly as pulling a plug) ⇒ one survivor promotes and the other rejoins it |
| ST-10 | Restart the ex-host ⇒ it rejoins as a member; the fleet is sampled throughout, so a moment with two hosts fails the test even if the end state looks right |
| ST-11 | Two units on separate stretches of air both host the same SSID, blind to each other; merging the air ⇒ they collapse back to one network unaided |
| ST-12 | The clip endpoint answers on a member's own address (validation asserted unconditionally; the encode half skips without ffmpeg — IT-3 owns MP4 validity), and every unit answers cross-origin calls and their preflight |

Milestones: **M1** core (steps 1–3) · **M2** API+export (4–5) · **M3**
hardware (6 + spikes + x264 benchmark + manual glass-to-glass measurement:
film a millisecond stopwatch, measured delay = configured + min_latency_ms
± 1 frame) · **M4** appliance (7 + provisioning walkthrough from blank SD).
