# Working on Zeitspiegel

Read `docs/ARCHITECTURE.md` before touching code. Requirements (FR-x/NFR-x)
live in `docs/REQUIREMENTS.md`; test IDs (UT-x/IT-x/ST-x) in
`docs/TESTPLAN.md`. When code and docs disagree, stop and flag it — do not
silently pick one.

## Hard rules

1. **TDD, strictly.** Every change starts with a failing test referencing its
   test-plan ID (e.g. `// UT-3`). No production code without a failing test
   first. Follow the build order in docs/TESTPLAN.md §3 — do not skip ahead.
2. **cgo only in `internal/camera` and `internal/screen`**, guarded by build
   tags `v4l2` and `sdl`. Everything else must compile and pass tests with
   plain `go test ./...` on any machine, no SDL/kernel headers. Never import
   these two packages from core packages.
3. **Dependency policy:** exactly four external modules are approved —
   `go4vl`, `go-sdl2`, `BurntSushi/toml`, `pgregory.net/rapid`. Adding
   anything else requires explicit human approval. Prefer stdlib.
4. **`Frame.JPEG` is immutable after construction.** Never mutate it, never
   copy it defensively. The buffer hands out shared slices by design (this is
   how exports pin their frames — see ARCHITECTURE §D2/§5).
5. **Single-writer ownership.** Only the capture goroutine calls
   `Buffer.Push`. The delay value is an `atomic.Int64` written only by the
   HTTP handler. Do not add locks to "fix" races — fix ownership.
6. **No wall-clock time in core packages.** Inject `engine.Clock`. Tests use
   `synth.FakeClock`; any `time.Now()` outside `cmd/` and the hardware
   adapters is a bug.
7. **ffmpeg/ffprobe are subprocesses**, never linked libraries. Tests that
   need them carry the `integration` build tag.

## Commands

```
mise run deps          # install the C libraries the tagged lanes need (macOS)
mise run deps:check    # can this machine run them? reports, installs nothing
make test              # pure unit tests, -race, runs anywhere   (every change)
make test-ui           # control page in headless Chromium, API stubbed (needs Node)
make test-integration  # adds -tags integration (needs ffmpeg + ffprobe)
make test-e2e          # multi-unit: 3 real binaries elect a role (no ffmpeg/root)
make test-hw           # -tags "v4l2 sdl" build + v4l2loopback tests (Linux)
make build-pi          # arm64 binary with v4l2+sdl tags (on the Pi itself)
make pi-binary         # same, cross-built in Docker (bookworm arm64)
make sd NAME="Long Side"   # flash + name a self-provisioning SD card (macOS)
make warm-cache        # fill every cache the card path reads (needs a network, once)
make check-caches      # can a card be made right now with the network off?
OFFLINE=1 make sd NAME="Long Side"   # ...make one, no internet at all
make sd-dev NAME="Bench"   # same card, unsealed: writable root, journal persists
make sd-logs           # read a unit's logs off its card into one zip
make run-synth         # run binary with --source synth (no camera needed)
make run-tv            # real SDL display path in a desktop window (dev TV view)
make manual-test       # hands-on E2E: see docs/MANUAL_TESTING.md (TV=1, SOURCE=camera)
```

`gofmt`, `go vet` and `-race` are all part of `make test`; all three must be
clean. The formatting gate covers files behind build tags too (`gofmt` reads
them regardless), which is where drift otherwise hides.

The Go and Node versions come from `mise.toml` (Go's from `go.mod`, which
stays the one place it is written down). The SDL2 libraries cannot: Homebrew
is not a mise backend and these are headers and `.pc` files rather than
binaries, so `mise run deps` wraps `brew` and `mise run deps:check` asks
pkg-config the question that actually matters — the same check on macOS, Linux
and CI.

## Code conventions

- Standard Go style; no test framework — stdlib `testing`, table-driven tests,
  `httptest` for handlers, `rapid` only for UT-4 property tests.
- Errors: wrap with `%w`, sentinel errors in the package that owns the
  concept (`ringbuf.ErrEmpty`, not a shared errors package).
- HTTP errors are RFC-9457 `application/problem+json`; API shapes are pinned
  in docs/REQUIREMENTS.md §3 — changing a response shape means changing that
  doc in the same PR.
- Logging via `log/slog` with the logger passed in, not global. Metrics via
  `expvar`. No logging in `ringbuf`/`engine`/`window` (pure packages return
  values and errors).

## Definition of done (per step)

The step's mapped tests (see docs/TESTPLAN.md §3) are green under `-race`,
`go vet` is clean, no approved-dependency violations, docs updated if a
contract changed. Performance claims (decode budget, export speed) are never
assumed — they are measured by the spikes S-1/S-2 on real hardware and the
numbers recorded in docs/ARCHITECTURE.md §7.

## Things that look like improvements but aren't

- Replacing the MJPEG buffer with H.264 "to save RAM" — breaks frame-accurate
  delay/export; this trade-off is decision D2, made deliberately.
- Buffering decoded frames "to save decode time" — blows the memory budget
  (~30× larger); we decode exactly one frame per tick by design.
- Re-decoding when `Seq` hasn't changed between ticks — wasted work; the
  renderer must skip.
- Drawing the delay badge only when the frame is re-rendered ("same pixels,
  same badge") — the badge and the warm-up countdown change on their own
  schedule, and during warm-up the frame is held for the whole window. That
  coupling is what made a moved slider look like it did nothing on a unit that
  was working perfectly (FR-13, UT-44). A held tick whose overlay text changed
  repaints; one whose text did not is still skipped.
- Opening the display before the HTTP listener, or treating a failed open as
  fatal — KMSDRM needs a connected connector, so that turns a switched-off TV
  into a unit that crash-loops in silence with no API and no radio (FR-17).
- Clamping the delay slider to `filled_s` "so it can't ask for what isn't
  there" — asking for more than is buffered is legitimate; the mirror warms
  into it. The range is capacity, and the part that is not buffered yet is
  marked, not removed (UI-13).
- Keeping runtime settings anywhere but the boot partition — `/var/lib`, the
  working directory, `/etc` — the root is a read-only overlay, so the file
  lands in tmpfs and is gone at the one restart that actually happens: the
  plug (FR-18, D9). Storing a whole config snapshot instead of the patch is
  the other half of the same trap: it freezes every default at the first
  PATCH and makes editing `config.toml` on the card do nothing.
- Putting a per-unit control on the settings page. That page is about the unit
  serving it, and with three mirrors on one network a control there silently
  means "whichever one answered". That is how a working mirror got reported as
  broken: the flip was toggled on the host's page while its owner watched
  another unit's TV (FR-14, UI-15). Anything that changes what one mirror does
  belongs on that mirror's card.
- Adding a router/web framework — stdlib `ServeMux` patterns are sufficient.
- Copying frame slices out of the buffer "for safety" — see hard rule 4.
- Putting an `apt-get install` back into `bake.sh` or `pi-binary` "just for
  this one package" — the builder image is what lets a card be made with no
  internet. A package installed during a bake is downloaded during *every*
  bake; it belongs in `deploy/builder.Dockerfile` (UT-42).
- Letting NetworkManager autoconnect the Wi-Fi profiles "so the network comes
  up sooner" — it races the role election and a unit that loses the race
  beacons a network somebody else is already hosting. Both profiles are
  `autoconnect=false` on purpose (E-8).
- Treating a failed scan as "nobody is out there" — a radio in AP mode cannot
  scan, and reading that as an empty network is exactly how a split brain is
  made (ARCHITECTURE D8).
- Making a returning ex-host take the network back — a handback costs the room
  a second outage for no benefit. Promotion never preempts.
- Baking a per-card role, address, or fleet size. Every card ships the
  byte-identical image; how many units exist is discovered at runtime and
  the name comes off the boot partition.
- Making a host with an empty peer registry give up its network — guests'
  phones are stations too, and a solo mirror serving a dancer must hold its
  Wi-Fi unconditionally. Only a host serving nobody at all may probe (D8).