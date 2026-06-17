# Delay Indicator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an always-on `MM:SS` delay badge in the top-right of the SDL display, white on opaque black, never mirror-flipped.

**Architecture:** All rendering stays inside `internal/screen` (hard rule 2). A pre-rendered PNG glyph atlas (digits + colon) is embedded via `go:embed` and loaded once into an SDL texture. `Screen` gains a `SetDelay(time.Duration)` method (backed by `atomic.Int64`) that the render loop calls each tick; `Render` draws the badge after `CopyEx` so it sits above the (possibly mirrored) camera frame and is itself never flipped. The `engine.Display` interface is unchanged.

**Tech Stack:** Go 1.22+, `go-sdl2` (existing approved dep), stdlib `image`/`image/png` (atlas generator), stdlib `embed`. No new modules.

**Spec:** `docs/superpowers/specs/2026-06-17-delay-indicator-design.md`

**Pre-flight check:** SDL2 + SDL2_image must be installed locally to run the `-tags sdl` tests. If you cannot run them locally, note that the `make test-hw` lane on Linux covers the same code path.

---

## Task 1: Documentation — add FR-13 and UT-11

**Files:**
- Modify: `docs/REQUIREMENTS.md`
- Modify: `docs/TESTPLAN.md`

- [ ] **Step 1: Add FR-13 to REQUIREMENTS.md**

Open `docs/REQUIREMENTS.md`. Find the FR table (rows `| FR-1 |` … `| FR-12 |`). Insert this row immediately after the FR-12 row, before the NFR section:

```
| FR-13 | On-screen delay indicator: top-right badge showing current delay as `MM:SS`, white on opaque black, always visible, not mirror-flipped | UT-11 + manual visual check |
```

- [ ] **Step 2: Add UT-11 to TESTPLAN.md**

Open `docs/TESTPLAN.md`. Find the UT-10 row (`| UT-10 | config | …`). Insert immediately after it:

```
| UT-11 | screen | `formatDelay` table: 0⇒"00:00", 30s⇒"00:30", 1m30s⇒"01:30", 61m⇒"61:00", 2h⇒"99:59" (clamp), -5s⇒"00:00" (clamp); plus sdl-tagged smoke that `Render` after `SetDelay` succeeds and the glyph texture loads (FR-13) |
```

Then find the build-order table row for stage 6 (the row that currently reads `| 6 | camera + screen adapters (thin), reconnect supervisor | IT-7; ST-1 |`). Replace the test column to include UT-11:

```
| 6 | camera + screen adapters (thin), reconnect supervisor | UT-11; IT-7; ST-1 |
```

- [ ] **Step 3: Verify docs build / parse (just `go vet ./...` and a quick grep)**

Run:
```bash
grep -n "FR-13" docs/REQUIREMENTS.md
grep -n "UT-11" docs/TESTPLAN.md
```
Expected: each grep prints exactly one line.

- [ ] **Step 4: Commit**

```bash
git add docs/REQUIREMENTS.md docs/TESTPLAN.md
git commit -m "docs: FR-13 on-screen delay indicator + UT-11"
```

---

## Task 2: `formatDelay` (UT-11 pure unit)

This file deliberately has **no build tag** so it compiles under plain `go test ./...` (no SDL needed). The `screen` package's other files are `//go:build sdl`-gated; that's fine — on headless builds the package will contain only this file plus its test, which is the desired behaviour.

**Files:**
- Create: `internal/screen/format.go`
- Create: `internal/screen/format_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/screen/format_test.go` with this exact content:

```go
package screen

import (
	"testing"
	"time"
)

// UT-11
func TestFormatDelay(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"zero", 0, "00:00"},
		{"thirty seconds", 30 * time.Second, "00:30"},
		{"one minute thirty", 90 * time.Second, "01:30"},
		{"sixty-one minutes", 61 * time.Minute, "61:00"},
		{"two hours clamps to max", 2 * time.Hour, "99:59"},
		{"negative clamps to zero", -5 * time.Second, "00:00"},
		{"sub-second rounds down", 999 * time.Millisecond, "00:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatDelay(tc.in)
			if got != tc.want {
				t.Fatalf("formatDelay(%v) = %q, want %q", tc.in, got, tc.want)
			}
			if len(got) != 5 {
				t.Fatalf("formatDelay(%v) = %q, want fixed-width 5 chars", tc.in, got)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
go test ./internal/screen/ -run TestFormatDelay -v
```
Expected: build failure — `formatDelay` undefined.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/screen/format.go` with this exact content:

```go
package screen

import (
	"fmt"
	"time"
)

// formatDelay renders d as fixed-width "MM:SS" for the on-screen badge.
// Clamps negatives to zero and anything above 99:59 to 99:59 — the buffer
// can never reach that duration in practice, but the display must stay
// five glyphs wide regardless of input.
func formatDelay(d time.Duration) string {
	const max = 99*time.Minute + 59*time.Second
	if d < 0 {
		d = 0
	}
	if d > max {
		d = max
	}
	totalSec := int(d / time.Second)
	return fmt.Sprintf("%02d:%02d", totalSec/60, totalSec%60)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run:
```bash
go test ./internal/screen/ -run TestFormatDelay -v -race
```
Expected: PASS for all seven sub-tests.

- [ ] **Step 5: `go vet` clean**

Run:
```bash
go vet ./internal/screen/
```
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add internal/screen/format.go internal/screen/format_test.go
git commit -m "screen: formatDelay MM:SS with clamps (UT-11)"
```

---

## Task 3: Glyph atlas generator + checked-in PNG

The generator is a separate `main` package under `internal/screen/fontgen` so it has no impact on the library build. It uses stdlib only (`image`, `image/png`, `os`). The atlas PNG is checked in; CI does not run the generator.

**Files:**
- Create: `internal/screen/fontgen/main.go`
- Create (via generator): `internal/screen/glyphs.png`

- [ ] **Step 1: Write the generator**

Create `internal/screen/fontgen/main.go` with this exact content:

```go
// Generator for internal/screen/glyphs.png. Stdlib only. Run with
//   go run ./internal/screen/fontgen
// from the repo root. The output PNG is checked in; CI does not run this.
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
)

// 7×12 source glyph cell. Each byte is one row, LSB = leftmost pixel.
// Cells are centred within the cell (col 0 and col 6 blank, rows 0/10/11
// blank). Order matches atlasOrder below.
var glyphs = map[rune][12]byte{
	'0': {0x00, 0x1C, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x1C, 0x00, 0x00},
	'1': {0x00, 0x08, 0x0C, 0x08, 0x08, 0x08, 0x08, 0x08, 0x08, 0x1C, 0x00, 0x00},
	'2': {0x00, 0x1C, 0x22, 0x20, 0x10, 0x08, 0x04, 0x02, 0x22, 0x3E, 0x00, 0x00},
	'3': {0x00, 0x1C, 0x22, 0x20, 0x10, 0x18, 0x10, 0x20, 0x22, 0x1C, 0x00, 0x00},
	'4': {0x00, 0x10, 0x18, 0x14, 0x12, 0x3E, 0x10, 0x10, 0x10, 0x10, 0x00, 0x00},
	'5': {0x00, 0x3E, 0x02, 0x02, 0x1E, 0x20, 0x20, 0x20, 0x22, 0x1C, 0x00, 0x00},
	'6': {0x00, 0x1C, 0x22, 0x02, 0x1E, 0x22, 0x22, 0x22, 0x22, 0x1C, 0x00, 0x00},
	'7': {0x00, 0x3E, 0x20, 0x10, 0x08, 0x08, 0x04, 0x04, 0x04, 0x04, 0x00, 0x00},
	'8': {0x00, 0x1C, 0x22, 0x22, 0x1C, 0x22, 0x22, 0x22, 0x22, 0x1C, 0x00, 0x00},
	'9': {0x00, 0x1C, 0x22, 0x22, 0x22, 0x3C, 0x20, 0x20, 0x22, 0x1C, 0x00, 0x00},
	':': {0x00, 0x00, 0x00, 0x08, 0x08, 0x00, 0x00, 0x08, 0x08, 0x00, 0x00, 0x00},
}

// Atlas layout: 11 glyphs, each 14×24 (source 7×12 scaled 2×), white on
// transparent. Matches the order screen/badge.go expects.
var atlasOrder = []rune{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', ':'}

const (
	cellW   = 14 // 7 source cols × 2
	cellH   = 24 // 12 source rows × 2
	atlasW  = cellW * 11
	atlasH  = cellH
	srcRows = 12
	srcCols = 7
)

func main() {
	img := image.NewNRGBA(image.Rect(0, 0, atlasW, atlasH))
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	for i, r := range atlasOrder {
		bm, ok := glyphs[r]
		if !ok {
			log.Fatalf("missing glyph for %q", r)
		}
		baseX := i * cellW
		for row := 0; row < srcRows; row++ {
			bits := bm[row]
			for col := 0; col < srcCols; col++ {
				if bits&(1<<col) == 0 {
					continue
				}
				// 2× scale: write a 2×2 block.
				px := baseX + col*2
				py := row * 2
				img.SetNRGBA(px, py, white)
				img.SetNRGBA(px+1, py, white)
				img.SetNRGBA(px, py+1, white)
				img.SetNRGBA(px+1, py+1, white)
			}
		}
	}
	f, err := os.Create("internal/screen/glyphs.png")
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatalf("encode: %v", err)
	}
	log.Printf("wrote internal/screen/glyphs.png (%dx%d, %d glyphs)", atlasW, atlasH, len(atlasOrder))
}
```

- [ ] **Step 2: Generate the atlas**

From the repo root:
```bash
go run ./internal/screen/fontgen
```
Expected: log line `wrote internal/screen/glyphs.png (154x24, 11 glyphs)` and file `internal/screen/glyphs.png` appears.

- [ ] **Step 3: Sanity-check the PNG**

Run:
```bash
file internal/screen/glyphs.png
```
Expected output contains: `PNG image data, 154 x 24, 8-bit/color RGBA, non-interlaced`.

- [ ] **Step 4: `go vet` the generator package**

Run:
```bash
go vet ./internal/screen/fontgen/
```
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add internal/screen/fontgen/main.go internal/screen/glyphs.png
git commit -m "screen: glyph atlas generator + checked-in glyphs.png"
```

---

## Task 4: Badge rendering inside `internal/screen` (sdl-tagged)

This task adds the SDL-side code. Tests run under `SDL_VIDEODRIVER=dummy` exactly like the existing `screen_test.go`. If SDL2 / SDL2_image are not installed locally, this task must be executed where they are (`make test-hw` lane on Linux is acceptable).

**Files:**
- Create: `internal/screen/badge.go`
- Modify: `internal/screen/screen.go`
- Modify: `internal/screen/screen_test.go`

- [ ] **Step 1: Extend the failing test**

Open `internal/screen/screen_test.go`. Add this test function at the end of the file (immediately after `TestProcessEventsQuietByDefault`):

```go
// UT-11 (sdl side): SetDelay before Render must not affect rendering and
// the glyph atlas must have loaded successfully in Open.
func TestRenderWithDelayBadge(t *testing.T) {
	s := openDummy(t)
	src := synth.NewSource(30, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	s.SetDelay(0)
	if err := s.Render(src.Next()); err != nil {
		t.Fatalf("render at delay 0: %v", err)
	}
	s.SetDelay(90 * time.Second)
	if err := s.Render(src.Next()); err != nil {
		t.Fatalf("render at delay 90s: %v", err)
	}
	s.SetDelay(-1 * time.Second) // clamp path
	if err := s.Render(src.Next()); err != nil {
		t.Fatalf("render at negative delay: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
SDL_VIDEODRIVER=dummy go test -tags sdl ./internal/screen/ -run TestRenderWithDelayBadge -v
```
Expected: build failure — `s.SetDelay` undefined.

- [ ] **Step 3: Create the badge file**

Create `internal/screen/badge.go` with this exact content:

```go
//go:build sdl

package screen

import (
	_ "embed"
	"fmt"
	"time"

	"github.com/veandco/go-sdl2/img"
	"github.com/veandco/go-sdl2/sdl"
)

//go:embed glyphs.png
var glyphsPNG []byte

// Atlas geometry — must match internal/screen/fontgen/main.go.
const (
	atlasOrder    = "0123456789:"
	glyphW        = 14
	glyphH        = 24
	badgePadEdge  = 16 // px from top + right edges of the screen
	badgePadInner = 4  // px between text and badge border, each side
)

// loadGlyphAtlas decodes the embedded PNG into an SDL texture.
func loadGlyphAtlas(ren *sdl.Renderer) (*sdl.Texture, error) {
	rw, err := sdl.RWFromMem(glyphsPNG)
	if err != nil {
		return nil, fmt.Errorf("screen: glyph rwops: %w", err)
	}
	surf, err := img.LoadRW(rw, true)
	if err != nil {
		return nil, fmt.Errorf("screen: decode glyph atlas: %w", err)
	}
	defer surf.Free()
	tex, err := ren.CreateTextureFromSurface(surf)
	if err != nil {
		return nil, fmt.Errorf("screen: glyph texture: %w", err)
	}
	return tex, nil
}

// drawBadge renders the 5-char MM:SS badge in the top-right corner.
// Must be called after CopyEx so it is never mirror-flipped.
func (s *Screen) drawBadge(d time.Duration) error {
	text := formatDelay(d)
	if len(text) != 5 {
		return fmt.Errorf("screen: badge text %q not 5 chars", text)
	}
	winW, _ := s.win.GetSize()
	innerW := int32(len(text)) * glyphW
	badgeW := innerW + 2*badgePadInner
	badgeH := int32(glyphH) + 2*badgePadInner
	bx := winW - badgePadEdge - badgeW
	by := int32(badgePadEdge)

	// Opaque black backdrop.
	if err := s.ren.SetDrawColor(0, 0, 0, 255); err != nil {
		return fmt.Errorf("screen: badge color: %w", err)
	}
	if err := s.ren.FillRect(&sdl.Rect{X: bx, Y: by, W: badgeW, H: badgeH}); err != nil {
		return fmt.Errorf("screen: badge rect: %w", err)
	}

	// Blit each glyph from the atlas.
	for i, r := range text {
		idx := int32(indexOf(atlasOrder, r))
		if idx < 0 {
			return fmt.Errorf("screen: badge has unknown rune %q", r)
		}
		src := sdl.Rect{X: idx * glyphW, Y: 0, W: glyphW, H: glyphH}
		dst := sdl.Rect{
			X: bx + badgePadInner + int32(i)*glyphW,
			Y: by + badgePadInner,
			W: glyphW,
			H: glyphH,
		}
		if err := s.ren.Copy(s.glyphTex, &src, &dst); err != nil {
			return fmt.Errorf("screen: badge glyph: %w", err)
		}
	}
	return nil
}

func indexOf(s string, r rune) int {
	for i, c := range s {
		if c == r {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 4: Modify `screen.go` to load the atlas, store delay, draw the badge**

Open `internal/screen/screen.go` and apply these three edits:

(a) Replace the `img.Init(img.INIT_JPG)` line so PNG decoding works too. Find:

```go
	if err := img.Init(img.INIT_JPG); err != nil {
```
Replace with:
```go
	if err := img.Init(img.INIT_JPG | img.INIT_PNG); err != nil {
```

(b) Add the new fields to `Screen` and load the atlas in `Open`. Find the `Screen` struct:

```go
type Screen struct {
	win    *sdl.Window
	ren    *sdl.Renderer
	mirror atomic.Bool
}
```

Replace with:

```go
type Screen struct {
	win      *sdl.Window
	ren      *sdl.Renderer
	mirror   atomic.Bool
	delayNS  atomic.Int64
	glyphTex *sdl.Texture
}

// SetDelay stores the delay shown by the on-screen badge. Called from the
// render loop each tick; no locking needed (single writer in practice).
func (s *Screen) SetDelay(d time.Duration) { s.delayNS.Store(int64(d)) }
```

Add the `time` import to the existing import block (`"time"`).

In `Open`, immediately before `return s, nil`, insert atlas loading and a teardown on failure:

```go
	tex, err := loadGlyphAtlas(ren)
	if err != nil {
		ren.Destroy()
		win.Destroy()
		img.Quit()
		sdl.Quit()
		return nil, err
	}
	s.glyphTex = tex
```

(c) Call `drawBadge` from `Render`. Find the existing tail of `Render`:

```go
	if err := s.ren.CopyEx(tex, nil, nil, 0, nil, flip); err != nil {
		return fmt.Errorf("screen: copy: %w", err)
	}
	s.ren.Present()
	return nil
}
```

Replace with:

```go
	if err := s.ren.CopyEx(tex, nil, nil, 0, nil, flip); err != nil {
		return fmt.Errorf("screen: copy: %w", err)
	}
	if err := s.drawBadge(time.Duration(s.delayNS.Load())); err != nil {
		return err
	}
	s.ren.Present()
	return nil
}
```

(d) Destroy the glyph texture in `Close`. Find:

```go
func (s *Screen) Close() error {
	s.ren.Destroy()
	s.win.Destroy()
	img.Quit()
	sdl.Quit()
	return nil
}
```

Replace with:

```go
func (s *Screen) Close() error {
	if s.glyphTex != nil {
		s.glyphTex.Destroy()
	}
	s.ren.Destroy()
	s.win.Destroy()
	img.Quit()
	sdl.Quit()
	return nil
}
```

- [ ] **Step 5: Run the new test under -race**

Run:
```bash
SDL_VIDEODRIVER=dummy go test -tags sdl ./internal/screen/ -run TestRenderWithDelayBadge -v -race
```
Expected: PASS.

- [ ] **Step 6: Run the full screen test suite to confirm nothing else broke**

Run:
```bash
SDL_VIDEODRIVER=dummy go test -tags sdl ./internal/screen/ -v -race
go vet -tags sdl ./internal/screen/
```
Expected: all four tests PASS; no vet output.

- [ ] **Step 7: Commit**

```bash
git add internal/screen/badge.go internal/screen/screen.go internal/screen/screen_test.go
git commit -m "screen: draw MM:SS delay badge in top-right (FR-13)"
```

---

## Task 5: Wire `SetDelay` into the render loop

The render loop must push the engine's current delay onto the screen each tick. We do this through a thin shim so the headless build (no sdl tag) compiles unchanged — mirroring the existing `displayMirrorFunc` / `displayEvents` pattern.

**Files:**
- Modify: `cmd/zeitspiegel/display_stub.go`
- Modify: `cmd/zeitspiegel/display_sdl.go`
- Modify: `cmd/zeitspiegel/main.go`

- [ ] **Step 1: Add a no-op shim in `display_stub.go`**

Open `cmd/zeitspiegel/display_stub.go`. Find:

```go
// displayEvents: no window, no events.
func displayEvents(engine.Display) func() bool { return nil }
```

Add immediately after that line:

```go

// displayDelayFunc: no badge in headless mode.
func displayDelayFunc(engine.Display) func(time.Duration) { return nil }
```

Add `"time"` to the imports if not already present.

- [ ] **Step 2: Add the real shim in `display_sdl.go`**

Open `cmd/zeitspiegel/display_sdl.go`. Find:

```go
// displayMirrorFunc exposes the runtime mirror toggle (PATCH /config).
func displayMirrorFunc(d engine.Display) func(bool) {
	if s, ok := d.(*screen.Screen); ok {
		return s.SetMirror
	}
	return nil
}
```

Add immediately after that function:

```go

// displayDelayFunc exposes the badge delay setter used by the render loop.
func displayDelayFunc(d engine.Display) func(time.Duration) {
	if s, ok := d.(*screen.Screen); ok {
		return s.SetDelay
	}
	return nil
}
```

Add `"time"` to the imports.

- [ ] **Step 3: Push delay in the render loop**

Open `cmd/zeitspiegel/main.go`. Find the render-loop block beginning at `pump := displayEvents(display)` (around line 172). Replace the entire loop body — from `pump := displayEvents(display)` down to and including the inner `}` that closes the `<-tick.C` case — so it reads:

```go
		pump := displayEvents(display)
		setDelay := displayDelayFunc(display)
		tick := time.NewTicker(time.Duration(float64(time.Second) / cfg.FPS()))
		defer tick.Stop()
	loop:
		for {
			select {
			case <-ctx.Done():
				break loop
			case err := <-errCh:
				runErr = err
				stop()
				break loop
			case <-tick.C:
				if pump != nil && pump() { // window closed (dev mode)
					stop()
					break loop
				}
				if setDelay != nil {
					setDelay(eng.Delay())
				}
				if sel := eng.Tick(time.Now()); sel.Render {
					if err := display.Render(sel.Frame); err != nil {
						logger.Error("render", "seq", sel.Frame.Seq, "err", err)
					}
				}
			}
		}
```

- [ ] **Step 4: Build both flavours**

Run:
```bash
go build ./...
go build -tags sdl ./...
```
Expected: both succeed with no output.

- [ ] **Step 5: Run the full headless test suite**

Run:
```bash
make test
```
Expected: PASS, `go vet` clean.

- [ ] **Step 6: Run the sdl-tagged tests**

If SDL2 is available locally:
```bash
SDL_VIDEODRIVER=dummy go test -tags sdl -race ./internal/screen/...
```
Expected: all PASS.

- [ ] **Step 7: Smoke-test the dev TV view**

If SDL2 is available locally:
```bash
make run-tv
```
Watch for the badge in the top-right showing `00:30` (the default delay from config). It must:
- Be readable on bright and dark backgrounds.
- Stay upright when the camera frame is mirrored.
- Update within one frame interval when `curl -X PUT http://localhost:8080/delay -d '{"delay_s": 5}'` is issued (replace port if different — check `cfg.Bind`).

Stop the app with Ctrl-C.

- [ ] **Step 8: Commit**

```bash
git add cmd/zeitspiegel/display_stub.go cmd/zeitspiegel/display_sdl.go cmd/zeitspiegel/main.go
git commit -m "wire: push engine delay to screen each tick (FR-13)"
```

---

## Final verification

- [ ] `make test` is green and `-race` clean.
- [ ] `go vet ./...` and `go vet -tags sdl ./...` are both clean.
- [ ] `git log --oneline` shows five focused commits since the spec commit (one per task).
- [ ] FR-13 in `docs/REQUIREMENTS.md` and UT-11 in `docs/TESTPLAN.md` are present.
- [ ] `internal/screen/glyphs.png` is checked in and is 154×24 RGBA.
- [ ] Manual: badge visible in `make run-tv`, upright, white-on-black, updates with delay changes.
