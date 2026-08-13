# docs/TESTPLAN.md

# Test Plan & Build Order

Test infrastructure: injected `Clock`/`FrameSource`; `synth.Source` encodes
seq + timestamp into a JPEG APP4 segment and pixel pattern; `FakeDisplay`
records (frame, render time). All timing behavior is deterministic without
hardware. CI matrix: `go test -race ./...` (anywhere) · `-tags integration`
(ffmpeg/ffprobe) · `-tags e2e` (`make test-e2e`: three real binaries electing
a role over a virtual airspace — no ffmpeg, no radios, no root) ·
`-tags "v4l2 sdl"` build + v4l2loopback (Linux) · arm64 cross build ·
`make test-ui` (the control page in headless Chromium, API stubbed).

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
| UT-19 | netrole | Election table under FakeClock, no fleet size anywhere: SSID present ⇒ join; short bounded stagger (4 fixed slots; production worst case ≤ 15 s so a solo Wi-Fi is up in seconds) then claim; a **refused scan never claims the SSID** (a beaconing radio cannot scan — that is how a split brain would be made); join timeout ⇒ back to searching; promotion at position × PromoteStep; a member waiting its promotion slot **joins the moment the SSID reappears**; a host serving **anybody** (peers or stations — a phone counts) never probes, however long; a host serving nobody probes after HealAfter, each fruitless probe backing the next off, capped; any audience resets timer and backoff; a probing host joins what it finds |
| UT-20 | config | `network_manage`, `name_file` parse + defaults; a legacy `fleet_size` key from the previous release is ignored, never a startup error; `deploy/config.toml` manages the radio |
| UT-21 | identity | Unit id from a `/proc/cpuinfo` fixture, deterministic; MAC fallback; random fallback reported **unstable**; name file missing / blank / whitespace / CRLF / second line / over-length; slug validation; explicit overrides |
| UT-22 | peers | Register/List; re-register replaces and refreshes; TTL expiry under FakeClock; sorted List; self-id and malformed input ⇒ `ErrInvalid`; over-long name truncated not refused; roster includes self; Position; `-race` on concurrent read/write |
| UT-23 | httpapi | `POST /api/v1/peers` derives `base_url` from the connection; roster + position in the reply; 422 table (bad id, blank name, self id, port range, malformed JSON); `GET /api/v1/peers` shape, `{"peers":[]}` on an empty registry (the API is always on); status carries `unit_id`/`name`/`role` |
| UT-24 | httpapi | CORS headers on API responses; `OPTIONS` preflight ⇒ 204 with the PUT method and Content-Type header allowed; a real PUT still works through the wrapper |
| UT-25 | peers | Announcer posts to the gateway with no address of its own; missing gateway and non-200 surface as errors; `Run` announces immediately and keeps announcing after failures |
| UT-26 | camera | `plannedControls` table per config (pinned focus / auto focus / everything pinned); a device implementing no focus controls still opens, both are reported by `SkippedControls()`, and the controls it *does* implement are still applied; a genuine failure (I/O error, out-of-range value) still aborts and names the control (FR-9, E-9). Tag-free: the cgo `SetControlValue` stays behind the `v4l2` tag, the skip decision does not |
| UT-27 | screen | `fitRect` table: same aspect ⇒ full bleed; 4:3 into 16:9 ⇒ pillarbox; 16:9 into 4:3 ⇒ letterbox; square and 1×1 sources; non-positive source or destination ⇒ fill (never divide by zero, never blank the screen). Plus a sweep asserting the rect never escapes the destination and never inverts the source aspect (FR-16) |
| UT-28 | camera | `selectMode` table (E-2, NFR-1): 1080p15 + 720p30 ⇒ 720p30 (rate is the constraint); 1080p30 + 720p60 ⇒ 1080p30 (area wins once the floor is cleared); equal area ⇒ faster wins; the 25 fps floor — 29.97 and 1080p@25 clear it (keeping the resolution over a 720p@30), 1080p@24 does not; modes above `MaxAuto{Width,Height}` filtered; nothing clearing the floor ⇒ fastest available, never an error; a device enumerating sizes but not intervals still selectable; empty/degenerate ⇒ error; selection stable across every rotation of the input |
| UT-29 | cmd | `modeStore`: status/gap/export read the mode the source actually opened, and fall back to the profile nominal when it reports none (synth) or its rate is unknown; `Clear` stops a reopen inheriting the previous camera's mode; `/status` carries the live geometry and rate — a 60 fps capture must not reach the exporter as the nominal 30 (FR-5 half-speed clips) |
| UT-30 | poster | The generated guest poster (python, `make poster-test`, run by the CI `poster` job — not part of `go test`): every string exists in both languages and is actually drawn; a single-language variant carries only its own; no line runs past the margins and no two columns on one baseline touch (German is ~20 % longer than its English twin); the content clears the footer rule, with a long SSID/URL too; both QR codes' emitted rects, sampled back into a module matrix, still equal what segno encodes for the Wi-Fi join string and the controls URL |
| UT-31 | identity | Provisioning (`scripts/stage-name.sh`, the label `make sd NAME=…` stages into the image's bootfs before the card is written): a staged label is read back verbatim by `identity.Resolve` — trimmed, umlauts intact, at the length limit; validate-only mode (no target directory) so a bad label is refused before the image bake; empty / whitespace / multi-line refused loudly with nothing written; over-length written but warned about, matching the unit's truncation; `auto` stages nothing and the unit falls back to `Zeitspiegel <ID>`, and clears a previous card's label from the reused image rather than letting it leak; re-naming replaces the file rather than appending; a bootfs that cannot be written to (macOS mounts a freshly written FAT32 read-only) is diagnosed by name with nothing left behind, not reported as a shell redirect failure — skipped when the tests run as root, which cannot be locked out of a directory |
| UT-32 | support | Field log collection (`scripts/collect-sd-logs.sh`, `make sd-logs` — the card comes back from a venue and has to explain itself): both halves of a card land in one zip — the run's only output, no scratch directory beside it — the FAT32 captures (`zeitspiegel-debug.log`, boot profile, `cmdline.txt`) and the ext4 root's persistent journal, which is copied verbatim so `journalctl -D` can still render it; a card that never got far enough to write a debug log, or whose root cannot be read on this machine (macOS without e2fsprogs), still produces a report naming what is missing instead of an empty file; the AP pre-shared key and the admin password hash are redacted while the keys stay visible, because the bundle travels; a volume that is not one of ours is refused by name with nothing written; a card whose ext4 root cannot be read without the reader installed refuses to run at all (naming e2fsprogs and the `--boot-only` override) rather than hand back a bundle with no journal, and `--boot-only` records the deliberate gap in the report; both spellings of the overlay seal (`overlayroot=`, `boot=overlay`) are recognised, with the warning that a sealed card's journal ends at the seal |
| UT-33 | support | The flashing script's size verdict (`scripts/flash-sd.sh --size-check`, the lines above the "type erase" prompt): the image's required size is stated in the same power-of-ten units the card is sold and `diskutil` reports in; a card larger than the image fits, one exactly its size fits, one byte short does not; a card that cannot hold the image is refused before anything is erased rather than failing partway through `dd`; a missing image is reported as a missing image, not as a card problem |
| UT-18 | httpapi | Streaming clip handler: headers (`X-Clip-Duration`, no `Content-Length`) precede the chunked body; pre-flight busy/empty still 503; exporter failing before the first body byte ⇒ 500 problem+json; failure mid-stream ⇒ truncated body, no second response; the stream is Closed exactly once |

## 1.1 Tier 1b — UI unit (headless browser, no server, `make test-ui`)

The control page is the one part of the system no Go test can see. These run
it in headless Chromium (Playwright) with every `/api/v1` call answered by a
stub fleet inside the browser's network layer — no binary, no camera, no
ffmpeg — so they assert the page's own behaviour and nothing about the API.
What the server does with those calls is UT-23/24, IT-10 and ST-8/12. The
page still ships with no build tooling and no dependencies (§D5); the test
harness under `web/uitest/` is the only thing that needs Node.

| ID | Case |
|---|---|
| UI-1 | A lone mirror renders exactly one card — its own — carrying delay, preview and clip controls; no fleet hint |
| UI-2 | One card per mirror with this one first, names from the roster; a mirror leaving loses its card, and a returning one (new address) gets a rebuilt card with its own slider |
| UI-3 | FR-14 as the page sees it: a card's slider PUTs `{"seconds"}` to **that** mirror's own `base_url` and no other origin is touched; this mirror's card posts to the page's origin |
| UI-4 | A slider being dragged is not overwritten by the 1 Hz poll (hold-off), and follows the mirror again once released |
| UI-5 | A mirror that stops answering is marked Offline and stays visible; it clears itself when it answers again — including the mirror serving the page, whose card is the only offline indicator now |
| UI-6 | Preview is per card: starting one streams from that mirror only, the view select swaps the running stream, stopping drops `src` rather than hiding it |
| UI-7 | Download asks that mirror directly (no second hop), for that mirror's own buffer length, in that card's format — one card's format select never changes another's |
| UI-8 | Three cards on a 390 px phone: no sideways scroll, one column, every control ≥ 44 px tall and on-screen |
| UI-9 | Each card's slider range follows that mirror's own `buffer.capacity_s` |
| UI-10 | A rejected delay surfaces the server's problem+json `detail` in the toast (FR-11) |
| UI-11 | Cards are as tall as their own contents: a running preview must not stretch the cards beside it into empty panel |
| UI-12 | The line introducing the cards stands clear of the first one, rather than reading as that card's own label |

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
| IT-11 | Units against a fake radio whose airspace is faithful in the ways that matter (a beaconing unit cannot scan; two can beacon at once; a station stays on the access point it joined; an AP can count its clients, phones modelled as phantom stations): cold start ⇒ exactly one host; **units added one at a time simply join**; failover; losing two of three; an ex-host rejoining without the AP count ever leaving 1; an empty 0+0 split collapsing; a host cut off without restarting working out from its emptied audience that it is the stale one while the replacement serving a member holds; **a solo host with a phone attached never dropping its network over hours**; an idle solo host probing rarely and always returning; a phone arriving stopping the probing (FR-15) |

## 3. TDD build order (follow strictly)

Spikes (time-boxed throwaway, as soon as hardware exists, parallel to M1):
S-1 = SDL2/KMSDRM decode+render benchmark on Pi (720p60, 1080p30);
S-2 = the candidate camera (docs/HARDWARE.md §6 — the shortlist is provisional
until this runs): `v4l2-ctl --list-formats-ext` to validate the native-MJPEG
assumption and record **every mode with its frame rate** — that whole list is
what `selectMode` consumes (UT-28), so the interesting result is which mode it
picks and whether it agrees with the `source opened` log line, not just whether
1920×1080@30 exists; `v4l2-ctl --list-ctrls` to
record which controls exist (a fixed-focus device has no focus controls — that
is expected and UT-26 makes it survivable) and the achievable `focus_absolute`
if it has them; then minimal go4vl capture to measure MJPEG bitrate **under
real studio lighting**, since sensor noise in a dim room inflates it and
`buffer_max_bytes` depends on that number. Record results in ARCHITECTURE.md
§7 before the BOM stops being provisional.

| Step | Content | Tests |
|---|---|---|
| 1 | frame + ringbuf | UT-1..5 |
| 2 | synth (source/clock/display) — test infra, itself tested | — |
| 3 | engine: tick logic, hard-cut semantics, warm-up | UT-6,7; IT-1, IT-6 |
| 4 | window + export vs real ffmpeg (`integration` tag) | UT-8; IT-3,4,9 |
| 5 | httpapi + config | UT-9,10,18; IT-2,5,8 |
| 6 | camera + screen adapters (thin), reconnect supervisor | UT-11,26,27,28,29; IT-7; ST-1 |
| 7 | wiring, web UI, deploy artifacts, soak | ST-2..6 |
| 8 | observability + stutter hardening (render metrics, capture gaps, streaming texture, export nice, 60 s capacity) | UT-12..17; ST-4 |
| 9 | multi-unit: dynamic election, membership, combined page, one image (E-8) | UT-19..25,31; UI-1..12; IT-10,11; ST-7..13 |

Step 9 in order: netrole (UT-19) → config + identity (UT-20, UT-21; UT-31
covers the provisioning script that writes what UT-21 reads) → peers
(UT-22, UT-25) → httpapi (UT-23, UT-24) → the fleet supervisor and IT-11 →
IT-10 → cmd wiring and the radio adapters → the combined page (UI-1..12) →
ST-7..12.
The election is built before anything can call it, and the E2E lane last,
because it exercises the whole thing through real processes.

UT-32 sits outside the build order: it covers field-support tooling on the
operator's laptop (`make sd-logs`), not anything that ships on the unit, so it
can be built whenever a card first needs to be debugged after the fact.

## 4. Tier 3 — system/E2E (real binary, nightly) & milestones

| ID | Case |
|---|---|
| ST-1 | API contract suite vs running process with v4l2loopback (CI, no camera) |
| ST-2 | UI smoke (Playwright) **against the real binary**: slider ⇒ PUT /delay; download ⇒ a file ffprobe accepts as MP4. **Not implemented** — the repo has no such lane. UI-1..12 now cover the page's behaviour with the API stubbed, so what is still missing here is only the page against a live server and a real encode |
| ST-3 | 24 h soak (synth): RSS growth < 5 %, drops < 0.1 % (NFR-1/2) |
| ST-4 | Load: export loop + preview client ⇒ NFR-3/4 held: `zeitspiegel_render.render_over_budget/ticks < 1 %`, `tick_overruns ≈ 0`, `miss_too_early = miss_empty = 0`, `held_streak_max ≤ 2` |
| ST-5 | systemd kill -9 ⇒ restart, /healthz green < 10 s |
| ST-6 | Power cycle mid-operation ⇒ clean boot to mirror, FS intact (NFR-9, FR-12) |
| ST-7 | Three **real binaries** cold-start over a virtual airspace ⇒ exactly one reports `role=primary`, the other two register, and the host lists both with the right names and connection-derived addresses. Also: a **lone** unit brings its own network up within seconds and serves `{"peers":[]}` — both Wi-Fi profiles ship with `autoconnect=false`, so the binary is the only thing that ever hosts a network |
| ST-8 | Three independent delays end to end: each unit's slider driven through the address the host handed out, and moving one leaves the others alone (FR-14) |
| ST-9 | SIGKILL the host process (its beacon stops being refreshed, exactly as pulling a plug) ⇒ one survivor promotes and the other rejoins it |
| ST-10 | Restart the ex-host ⇒ it rejoins as a member; the fleet is sampled throughout, so a moment with two hosts fails the test even if the end state looks right |
| ST-11 | Two units on separate stretches of air both host the same SSID, blind to each other; merging the air ⇒ they collapse back to one network unaided |
| ST-12 | The clip endpoint answers on a member's own address (validation asserted unconditionally; the encode half skips without ffmpeg — IT-3 owns MP4 validity), and every unit answers cross-origin calls and their preflight |
| ST-13 | **The studio day**, real binaries end to end: one station powers on and hosts within seconds; a second and third are added later and join; three independent delays driven through host-issued addresses; the host's power is cut mid-use ⇒ a survivor hosts and the members' delays are untouched; the ex-host restarts and rejoins as a member without the network changing hands |

Milestones: **M1** core (steps 1–3) · **M2** API+export (4–5) · **M3**
hardware (6 + spikes + x264 benchmark + manual glass-to-glass measurement:
film a millisecond stopwatch, measured delay = configured + min_latency_ms
± 1 frame) · **M4** appliance (7 + provisioning walkthrough from blank SD).
