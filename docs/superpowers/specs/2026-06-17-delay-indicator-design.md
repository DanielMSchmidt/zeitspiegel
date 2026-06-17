# Delay Indicator Overlay — Design

Date: 2026-06-17
Status: approved for implementation
Owner: Daniel

## Problem

The display shows a delayed video feed but gives no on-screen hint of *how
much* the delay currently is. A viewer walking up to the mirror has to open
the web UI to find out. We want a small, always-on badge in the top-right of
the display that shows the current delay as `MM:SS`, readable on any
background.

## Constraints

- Pulled directly from `CLAUDE.md`:
  - cgo only in `internal/camera` and `internal/screen` (hard rule 2). All
    SDL calls stay in `internal/screen`.
  - `Frame.JPEG` is immutable; we render the badge on top of the SDL
    framebuffer, never by modifying frames.
  - Dependency policy: no new external modules. The atlas must be built
    from stdlib only (`image`, `image/png`).
  - No wall-clock in core packages. The badge uses the delay value the
    engine already owns; it does not read a clock.
- The image is mirror-flipped by default (FR-2). The badge must **not** be
  flipped — text has to stay readable.
- 720p@60 is the production target (NFR-1). The badge is sized for 720p and
  scales proportionally fine to 1080p.

## Decisions

1. **Always on.** No fade, no toggle, no config flag. Simpler, predictable.
2. **Format: `MM:SS`, fixed five characters.** Examples: `00:30`, `01:30`,
   `99:59`. Delays above 99:59 (we never buffer that long) are clamped for
   display.
3. **White text on opaque black rectangle.** 16 px padding from top and
   right edges. Badge ≈ 78 × 32 px at 720p (5 glyphs × 14 px + 8 px
   internal padding).
4. **Pre-rendered PNG glyph atlas, embedded via `go:embed`.** Hand-coded
   7×12 bitmap font, scaled 2× to 14 × 24 per glyph, baked into
   `internal/screen/glyphs.png`.
5. **Drawn after `CopyEx`.** Mirror flip applies only to the camera frame;
   the badge is drawn straight onto the renderer in screen-space and is
   never flipped.
6. **`engine.Display` interface is unchanged.** The delay is pushed to the
   screen via a screen-specific `SetDelay` method called from the render
   loop. Engine and synth stay unaware of the badge.

## Architecture

### `internal/screen` changes

- New file `internal/screen/badge.go` (sdl build tag) holds:
  - The embedded `glyphs.png` atlas and the loaded `*sdl.Texture`.
  - `formatDelay(d time.Duration) string` returning a fixed 5-char `MM:SS`.
    Clamps `d < 0` to `0` and `d > 99m59s` to `99:59`.
  - `(*Screen).drawBadge(delay time.Duration)` that draws the rectangle
    and blits five glyphs from the atlas.
- `Screen` gains:
  - `delayNS atomic.Int64`
  - `glyphTex *sdl.Texture` (loaded in `Open`, destroyed in `Close`)
  - `SetDelay(d time.Duration)` — stores to the atomic.
- `Screen.Render` calls `s.drawBadge(time.Duration(s.delayNS.Load()))`
  after `s.ren.CopyEx(...)` and before `s.ren.Present()`.

### `cmd/zeitspiegel/main.go` changes

- The render loop calls `display.SetDelay(eng.Delay())` immediately before
  `display.Render(sel.Frame)`. Type assert to a `interface{ SetDelay(time.Duration) }`
  so the headless path (display == nil) still works and the engine's
  `Display` interface stays minimal.

### Glyph atlas generator

- `internal/screen/fontgen/main.go` — stdlib only.
- Defines `var glyphs = map[rune][12]byte{...}` where each `byte` is the
  bit pattern for one 7-px row (LSB = leftmost pixel).
- Writes `internal/screen/glyphs.png`: 154 × 24 RGBA, white-on-transparent,
  fixed 14 × 24 cells, in atlas order `0123456789:`.
- Invoked manually (`go run ./internal/screen/fontgen`); the resulting PNG
  is checked in. CI does not run the generator.

## Documentation updates (same PR)

- `docs/REQUIREMENTS.md` — add FR-13:
  > Display overlays the current delay as `MM:SS` in the top-right corner,
  > white text on opaque black, always on, not mirror-flipped.
  > Verification: UT-11 + visual smoke (manual M5).
- `docs/TESTPLAN.md` — add:
  - UT-11 (screen): `formatDelay` table-driven — `0 → "00:00"`,
    `30s → "00:30"`, `1m30s → "01:30"`, `61m → "61:00"`,
    `2h → "99:59"`, `-5s → "00:00"`.
  - Extend the `screen` adapter test (sdl-tagged) to assert that
    `Render` succeeds after `SetDelay` under the dummy SDL driver and
    that the glyph texture is non-nil.

## Test plan (per CLAUDE.md hard rule 1)

Each commit lands a failing test first, referencing its plan ID in a
comment.

1. UT-11 first: write `formatDelay_test.go` with the table. Implement
   `formatDelay` until green.
2. Add `Screen.SetDelay`/`Screen.glyphTex`/`drawBadge` behind the sdl
   tag, with the extended `screen_test.go` assertion as the failing
   gate. Implement until green under `SDL_VIDEODRIVER=dummy`.
3. Generator commit: `internal/screen/fontgen/main.go` + checked-in
   `glyphs.png`. No test needed — the screen tests already depend on a
   loadable atlas.
4. Wire-up commit: `main.go` calls `SetDelay` before `Render`.
   Verified by the existing `make test-hw` smoke run and a manual M5
   pass.

## Non-goals

- No fade-in / fade-out animation.
- No toggle via config, query string, or API. Always on.
- No warm-up indicator in the badge — the web UI already shows it.
- No 1080p-specific sizing; constant pixels at all resolutions.
- No font fallback beyond the 11 glyphs we ship (`0–9`, `:`).
