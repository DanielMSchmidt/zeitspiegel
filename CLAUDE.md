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
make test              # pure unit tests, -race, runs anywhere   (every change)
make test-integration  # adds -tags integration (needs ffmpeg + ffprobe)
make test-hw           # -tags "v4l2 sdl" build + v4l2loopback tests (Linux)
make build-pi          # arm64 binary with v4l2+sdl tags
make run-synth         # run binary with --source synth (no camera needed)
```

`go vet` and `-race` are part of `make test`; both must be clean.

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
- Adding a router/web framework — stdlib `ServeMux` patterns are sufficient.
- Copying frame slices out of the buffer "for safety" — see hard rule 4.