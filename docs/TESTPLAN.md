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
| UT-26 | camera | `plannedControls` table per config (pinned focus / auto focus / everything pinned); an absolute value of 0 means "not measured" and is never sent — the shipped config carries 0 until S-2 measures one, and a device whose focus_absolute range starts at 1 refuses it, aborting the open and costing the mirror its video while the screen still shows its delay badge; a measured value is sent, and a device rejecting *that* stays fatal. A device implementing no focus controls still opens, both are reported by `SkippedControls()`, and the controls it *does* implement are still applied; a genuine failure (I/O error, out-of-range value) still aborts and names the control (FR-9, E-9). Tag-free: the cgo `SetControlValue` stays behind the `v4l2` tag, the skip decision does not; the picture controls (saturation, contrast, gamma) follow the same rule — unset leaves the camera's own default alone, a chosen value is sent — which is the lever for a camera that returns a technically perfect frame that still looks like nothing |
| UT-27 | screen | `fitRect` table: same aspect ⇒ full bleed; 4:3 into 16:9 ⇒ pillarbox; 16:9 into 4:3 ⇒ letterbox; square and 1×1 sources; non-positive source or destination ⇒ fill (never divide by zero, never blank the screen). Plus a sweep asserting the rect never escapes the destination and never inverts the source aspect (FR-16) |
| UT-28 | camera | `selectMode` table (E-2, NFR-1): 1080p15 + 720p30 ⇒ 720p30 (rate is the constraint); 1080p30 + 720p60 ⇒ 1080p30 (area wins once the floor is cleared); equal area ⇒ faster wins; the 25 fps floor — 29.97 and 1080p@25 clear it (keeping the resolution over a 720p@30), 1080p@24 does not; modes above `MaxAuto{Width,Height}` filtered; nothing clearing the floor ⇒ fastest available, never an error; a device enumerating sizes but not intervals still selectable; empty/degenerate ⇒ error; selection stable across every rotation of the input |
| UT-29 | cmd | `modeStore`: status/gap/export read the mode the source actually opened, and fall back to the profile nominal when it reports none (synth) or its rate is unknown; `Clear` stops a reopen inheriting the previous camera's mode; `/status` carries the live geometry and rate — a 60 fps capture must not reach the exporter as the nominal 30 (FR-5 half-speed clips) |
| UT-30 | poster | The generated guest poster (python, `make poster-test`, run by the CI `poster` job — not part of `go test`): every string exists in both languages and is actually drawn; a single-language variant carries only its own; no line runs past the margins and no two columns on one baseline touch (German is ~20 % longer than its English twin); the content clears the footer rule, with a long SSID/URL too; both QR codes' emitted rects, sampled back into a module matrix, still equal what segno encodes for the Wi-Fi join string and the controls URL |
| UT-31 | identity | Provisioning (`scripts/stage-name.sh`, the label `make sd NAME=…` stages into the image's bootfs before the card is written): a staged label is read back verbatim by `identity.Resolve` — trimmed, umlauts intact, at the length limit; validate-only mode (no target directory) so a bad label is refused before the image bake; empty / whitespace / multi-line refused loudly with nothing written; over-length written but warned about, matching the unit's truncation; `auto` stages nothing and the unit falls back to `Zeitspiegel <ID>`, and clears a previous card's label from the reused image rather than letting it leak; re-naming replaces the file rather than appending; a bootfs that cannot be written to (macOS mounts a freshly written FAT32 read-only) is diagnosed by name with nothing left behind, not reported as a shell redirect failure — skipped when the tests run as root, which cannot be locked out of a directory |
| UT-32 | support | Field log collection (`scripts/collect-sd-logs.sh`, `make sd-logs` — the card comes back from a venue and has to explain itself): both halves of a card land in one zip — the run's only output, no scratch directory beside it — the FAT32 captures (`zeitspiegel-debug.log`, boot profile, `cmdline.txt`) and the ext4 root's persistent journal, which is copied verbatim so `journalctl -D` can still render it; a card that never got far enough to write a debug log, or whose root cannot be read on this machine (macOS without e2fsprogs), still produces a report naming what is missing instead of an empty file; the AP pre-shared key and the admin password hash are redacted while the keys stay visible, because the bundle travels; a volume that is not one of ours is refused by name with nothing written; a card whose ext4 root cannot be read without the reader installed refuses to run at all (naming e2fsprogs and the `--boot-only` override) rather than hand back a bundle with no journal, and `--boot-only` records the deliberate gap in the report; both spellings of the overlay seal (`overlayroot=`, `boot=overlay`) are recognised, with the warning that a sealed card's journal ends at the seal; a file the run cannot read — the card's 0600-root Wi-Fi profiles, when an extraction under sudo left the card's own ownership on the copy — is dropped and named in the report rather than carried unredacted or silently missing, and one locked file never costs the rest of the card; an empty `/var/log/journal` is stated as a finding rather than printed as an empty list; redaction keeps the file's mtime, because on a unit with no battery-backed clock the timestamps are half the evidence; the build the card carries (`zeitspiegel-version.txt`, stamped onto the boot partition by the bake) leads the report, and a card baked before stamping existed says `unknown` rather than showing a blank field; the boot partition's journal snapshot is decompressed into the report and carried in the bundle, since on a first boot or a sealed card it is the only journal there is; the settings the unit was left set to (FR-18) come back with it, because “the mirror came back unflipped” and “somebody unflipped it” are different faults and that file is what tells them apart |
| UT-33 | support | The flashing script's size verdict (`scripts/flash-sd.sh --size-check`, the lines above the "type erase" prompt): the image's required size is stated in the same power-of-ten units the card is sold and `diskutil` reports in; a card larger than the image fits, one exactly its size fits, one byte short does not; a card that cannot hold the image is refused before anything is erased rather than failing partway through `dd`; a missing image is reported as a missing image, not as a card problem. Plus the write-protect verdict (`--media-check`, against captured `diskutil info` output): read-only media is refused before the erase, in both field spellings, naming the disk and the lock switch — a read-only *volume* on writable media is not refused, being a mount option rather than the card |
| UT-34 | support | The on-unit boot capture (`deploy/zeitspiegel-boot-profile.sh`, the only durable record a sealed or journal-less card has — it writes to the FAT32 boot partition): run with no systemd at all, every section is still present and each command's failure is captured in place of its output rather than aborting the capture or leaving a section silently empty; the sections a dark unit needs are there — `systemctl status` and the restart count, the unit's own journal lines rather than only its success milestones, journald's storage state (why a card carries no journal), `/dev/dri` and the DRM connector status (whether SDL had a screen to open), `/dev/video`, and what the camera says about itself (`v4l2-ctl --all`, its formats and its controls) — where a picture that arrives sharp but grey gets answered without anybody standing at the unit; the whole boot's journal — every unit, not only ours — is snapshotted gzipped beside the profile on the same FAT32 partition, which is what makes a single boot enough: a first boot keeps its journal in RAM and a sealed card keeps every boot in tmpfs, so without the snapshot the boot the card was pulled for is gone; the timer keeps firing and the capture decides whether a firing rewrites anything — once per boot by default, since a venue appliance should not write to its card every few minutes, and on every firing when the `zeitspiegel-capture-live` marker is on the boot partition, which is what makes a unit that fails after hours explainable |
| UT-35 | cmd | `buildVersion`: the build stamp (`-ldflags -X main.version`, from `git describe`) is returned verbatim — release tag, describe output with distance and dirt — and trimmed, since it reaches log lines and filenames; an unstamped build falls back to the toolchain's own VCS stamp and, failing that, says so, because an empty version reads as a collection bug and a made-up one is worse than an admitted unknown |
| UT-36 | support | The unit file pins `SDL_VIDEODRIVER=kmsdrm` with `XDG_RUNTIME_DIR` pointing at the tmpfs `RuntimeDirectory` creates, so SDL stops probing wayland/x11 and burying the real error; frame dumping and debug logging are development-only by construction, not by convention — the shipped `deploy/config.toml` leaves both keys commented out and `bake.sh` writes values for them only inside its `SEAL != 1` branch, so a production card cannot quietly start writing pictures of the room to storage |
| UT-37 | support | One answer to "what does a unit need": both install paths (`deploy/sd/bake.sh`, `deploy/setup.sh`) install from `deploy/runtime-packages.txt` and verify the result with `deploy/check-runtime.sh`, which asserts every entry in `deploy/runtime-libs.txt` exists in a given root — sonames searched under the library directories, absolute paths checked as files, which is how the badge's typeface (loaded by name, linked by nothing) gets noticed when it is missing — an image being baked, or the unit itself at boot. A root missing `libEGL.so.1` fails, naming both the library and the package that provides it; a complete one passes. These are dlopened rather than linked, so no linker, build or test on any other tier can see them missing — which is how every baked card shipped for two months with a binary that could not open a screen |
| UT-38 | support | The card writer will not write an unverified image: the bake records its runtime-check verdict on the FAT partition (`runtime_check=` in `zeitspiegel-version.txt`, readable without an ext4 reader) and `flash-sd.sh` reads it back before the erase prompt. An image carrying a passing verdict flashes and the verdict is shown; one baked before the check existed is refused by name, pointing at `make image` and at `ALLOW_UNVERIFIED_IMAGE=1` for when writing an older image is the point |
| UT-39 | camera | `summarizeProbeErrors`: a Pi 5 enumerates nineteen `/dev/video*` nodes and seventeen are codec/ISP endpoints that could never be a camera, so a failed auto-probe groups failures by reason, reports the rarest first — the node that failed differently from the crowd is the camera — and accounts for every node by name or by count. Six lines at most, because the capture supervisor logs this on every retry, several times a second; a single node is reported plainly with no counting or grouping |
| UT-40 | screen | `badgeLayout`: the delay badge is a fraction of the screen (~4 % of its height), not a fixed pixel count — it was sized for one monitor and reads as a postage stamp on a studio TV. Table across 720p/1080p/1440p/4K: the type stays within a legible band, never stretches (the atlas aspect is preserved), and the box stays inset inside the top-right corner; the scale is a whole multiple of the atlas cell, since a bitmap font blown up fractionally comes out smeared; a degenerate screen size falls back to the unscaled cell with a positive rectangle rather than a negative one SDL would refuse. `badgeFontPx` sizes real type to the same fraction without cell rounding (720p/1080p/1440p/4K, with a floor since SDL will not open a font at zero pixels), and `badgeLayoutText` boxes text the font has measured, padding growing with the type and the box staying on screen even when the text is wider than it |
| UT-41 | framedump | Development cards write the frames they are showing where `make sd-logs` collects them, because a unit in a studio has no network to curl and no keyboard to type on: one file per sampling tick named by sequence number; bounded to the newest N, since this runs on a card; a frame already written is not written again, so a stalled capture does not fill the card with copies of the moment it stopped; an empty buffer and an unwritable directory are both stepped over, because a debugging aid must never take the mirror down. The ticker is injected (hard rule 6) — the package reads no clock |
| UT-42 | support | A card can be made with no internet, and whether it can be is knowable before the trip rather than at the venue: the cache preflight (`scripts/build-image.sh --check-caches`, `make check-caches`) names every cold cache in one pass — base image, `.deb`s, apt lists, Go modules — and points at `make warm-cache`, while a warm one passes; the bake path installs nothing of its own, both containers coming from `deploy/builder.Dockerfile` with the SDL2 dev libraries and the image tools already in them; the cross-build's module and build caches are mounted from the host, so a bake does not re-download four modules and recompile the arm64 standard library into a container it throws away; and the chroot install has an offline branch that uses `apt-get --no-download` and never `apt-get update`, releasing the host's `.deb` cache before `apt-get clean` — which empties the directory that cache is mounted on, and would otherwise wipe what the bake just filled |
| UT-43 | cmd | `displayAcquirer` (FR-17): the display is opened by the render loop, not before it, so a unit with no HDMI connected keeps its API, its capture and its role election. A screen already plugged in is picked up on the first tick with no interval waited out; a failing open is retried on a bounded interval and never once per 60 Hz tick; the cable arriving ends the retries; the same failure is logged once and a changed one again, because the retry runs for as long as the unit is powered; `status()` carries open/attempts/last_error into expvar (NFR-8); a build with no display support at all stops asking rather than retrying something that can never succeed. Plus `attachMirror`: a display that arrives late takes the mirror flip **in force**, not the one the config file booted with — the control page has been answering since boot, so the value may already have moved |
| UT-44 | cmd | `renderLoop` overlay (FR-10/FR-13): warm-up remaining is derived from the tick's own fire time and the oldest buffered frame, zero once the delay is fully buffered and zero on an empty buffer (that stretch is the splash's). A held tick repaints when the badge text or the countdown second changed — a delay change during warm-up, or the countdown ticking over — and is left alone when neither did, since the frame-skip rule is what keeps the tick budget. A repaint never decodes again |
| UT-45 | screen | `formatWarmup` table: whole seconds rounding **up** (a countdown reading 0s while the picture is still frozen is the lie the line exists to stop telling), empty at zero and below, clamped like `formatDelay`. Every rune the badge can emit has a cell in `atlasOrder` — the atlas is drawn by a generator holding its own copy of that string, the bitmap fallback errors on an unknown rune, and a unit with no typeface installed draws every line from it. `stackUnder` puts the countdown below the badge, right-aligned to the same edge, on screen even when the renderer reports a size nothing fits in. sdl-tagged: `Repaint` re-presents without re-uploading the frame, is a no-op before any frame rather than blanking a live mirror, both lines draw through the typeface and the atlas paths, and the two lines cache their type in separate slots — they alternate within one frame, so a single slot would re-rasterise both on every tick |
| UT-46 | cmd | `/status` carries the runtime config (REQUIREMENTS §3): the same snapshot the buffer capacity and profile in the same body came from — two reads would describe a moment that never existed — and it is a nested `config` object, so a page watching a unit polls once instead of twice (UI-14) |
| UT-47 | config | Settings outlive the process they were changed in (FR-18): a saved patch round-trips; the file carries **only** the keys somebody set, so the boot config still governs the rest and a card can still be re-defaulted by editing it; a missing file is an ordinary first boot rather than a fault; no path configured means persistence is off both ways (the laptop case); a corrupt or hand-edited file is an error naming itself, so the caller can boot on the config file instead of not at all; the write is atomic and leaves no scratch file on the partition the Pi boots from (NFR-9 — FAT has no journal and the plug is the off switch), and an unwritable location is reported rather than swallowed. `Patch.Merge` accumulates, so a later profile change does not drop an earlier flip, and `Config.WithRuntime` folds the effective runtime back into the boot config without touching a key outside that subset |
| UT-48 | cmd | The runtime store hands every accepted patch to be written down (FR-18), accumulated rather than replaced; a patch that changes nothing writes nothing — a page re-sending the value it just read must not touch the card — and a rejected patch is not stored at all, because the unit is not running under it |
| UT-49 | screen | FR-2's acceptance criterion, finally performed: a frame that is one colour on the left and another on the right is read back off the renderer and the halves are **actually swapped** when the flip is on. Covers the runtime toggle on a freshly decoded frame and on `Repaint`, since warm-up holds one frame for the whole window and a flip that waited for the next decode would sit there doing nothing. Every other sdl test asserts only that a flipped render does not error, which a renderer ignoring the flip would also satisfy |
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
| UI-13 | A warming mirror's card counts down (`Ready in Ns`, from `delay_s − buffer.filled_s`), shrinking as the buffer fills and clearing when it is deep enough; the slider tints the part of its range the buffer cannot serve yet, since a delay past what is buffered is accepted and warmed into rather than refused; a mirror that stopped answering is Offline on that same badge instead, because a countdown for a unit nobody can reach is a guess presented as fact (FR-10) |
| UI-14 | The settings page's mirror toggle is shared state, not this page's: it follows a flip made from another phone within a poll, and its next click is computed from the unit's value rather than the one the page loaded with. A read taken before that click cannot undo it by landing after it — the toggle's version of UI-4's hold-off, on a control with no drag to recognise. The setting rides on the `/status` poll, so watching a unit is one request per second and not two |
| UI-15 | The mirror flip is per mirror, on that mirror's card (FR-2, FR-14): a card's flip PATCHes **that** unit's own `base_url` and no other origin is touched, the other cards are left as they were, and this mirror's card posts to the page's origin. It is the unit's state and not the card's, so it follows a flip made elsewhere within a poll and computes its next click from the unit's value — UI-14's stale-read guard, per card. Living only on the settings page is what let a guest flip one unit's TV while standing in front of another and read that one as broken. The settings page keeps its own switch and now names the mirror it is about, since a page that names no unit is one you can act on by mistake |

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
can be built whenever a card first needs to be debugged after the fact. UT-42
is outside it for the same reason from the other end — it covers how a card is
made (`make warm-cache`, `OFFLINE=1 make sd`), not what is on one.

UT-43/44/45 and UI-13 sit outside it from a third direction: they are the
first field test's answers, hardening steps 6–8 after the fact rather than
building anything new. All three came off one session with two units and one
display moved between them — the unit without the cable crash-looped in
silence for as long as it had no screen (FR-17), and when it did come up its
buffer was empty, so it held a still frame through its warm-up window while
the delay slider appeared to do nothing (FR-10/FR-13). Neither is a fault in
the delay pipeline; both are the appliance failing to say what it was doing.

UT-47/48/49, UI-15 and ST-14 are the second field test's answers, and they sit
outside the build order for the same reason. One session with two units: the
flip was toggled on the page the host was serving while its owner watched the
*other* unit's TV, so a working mirror read as a broken one (UI-15) — and
whatever was set that evening was gone the next time the plug went in (FR-18,
UT-47/48, ST-14). UT-49 came out of the same hunt: FR-2 has always specified a
pixel comparison as its acceptance criterion and nothing had ever performed
one, so the first question — "does the flip reach the glass at all?" — had no
test to answer it.

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
| ST-14 | A setting made from the control page is still in force after the unit has been switched off and on again (FR-18), through the real binary: the flip is patched, the process is **killed** rather than asked to exit — that is what the switch on the wall does — and a unit restarted on the same config comes back flipped; deleting the state file puts it back to its config file, which is the only reset an operator with a card reader has |

Milestones: **M1** core (steps 1–3) · **M2** API+export (4–5) · **M3**
hardware (6 + spikes + x264 benchmark + manual glass-to-glass measurement:
film a millisecond stopwatch, measured delay = configured + min_latency_ms
± 1 frame) · **M4** appliance (7 + provisioning walkthrough from blank SD).
