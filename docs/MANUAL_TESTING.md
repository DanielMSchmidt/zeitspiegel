# Manual E2E Testing

Automated coverage lives in docs/TESTPLAN.md (UT/IT/ST). This checklist is
for a human verifying the user-visible behavior. IDs are MT-x; each maps to
the requirement it exercises.

## Setup (no hardware needed)

```
./scripts/manual-test.sh        # or: make manual-test
```

Builds the binary, starts it with the synthetic source (a moving bar +
frame-number pattern at 60 fps), sets the delay to 2 s, and opens the web UI
(`PORT=…` and `NO_BROWSER=1` are honored). Everything below happens in the
browser. The synthetic source stands in for the camera; the *delayed mirror*
preview (`/api/v1/preview?view=delayed`) stands in for the HDMI display —
it shows exactly the frame the display renderer would show.

## Using your real webcam (dev machines)

```
SOURCE=camera ./scripts/manual-test.sh
```

Without the `v4l2` build tag the binary captures through an **ffmpeg
subprocess** (`internal/ffcam`): avfoundation on macOS, the v4l2 demuxer on
Linux. `device = "auto"` (the default) picks the default/first camera; set
`device` in a config file to choose another (macOS accepts an avfoundation
index like `"0"` or a name like `"FaceTime HD Camera"`).

- **macOS camera permission:** the first run triggers a system prompt for
  the terminal app running the script (ffmpeg inherits its permission). If
  the prompt never appeared and the buffer stays empty — capture errors in
  the log, `filled_s` stuck at 0 — grant access manually: System Settings →
  Privacy & Security → Camera → enable your terminal, then rerun.
- Input is captured at 30 fps (lowest common denominator) and resampled to
  the profile's nominal rate so clip timing stays correct.
- Camera *controls* (focus/exposure pinning, FR-9) only apply on the
  `-tags v4l2` go4vl path — the appliance build. The ffmpeg path is for
  development.

## Seeing what the TV would show

```
TV=1 ./scripts/manual-test.sh              # synth source + display window
TV=1 SOURCE=camera ./scripts/manual-test.sh
```

`TV=1` builds with the `sdl` tag and opens the **real display path** —
`internal/screen`, the same JPEG-decode → mirror-flip → vsync-present code
that drives the HDMI output on the Pi — in a desktop window (`--windowed`).
This is the closest possible answer to "what would the connected TV show",
including FR-2 mirror flip (toggle it in the web UI and watch the window)
and the warm-up / hard-cut behavior at native frame rate. Closing the
window shuts the binary down cleanly.

One-off without the script: `make run-tv`.

Prerequisites: macOS `brew install sdl2 sdl2_image pkgconf`; Linux
`apt install libsdl2-dev libsdl2-image-dev`. Only pixel timing differs from
the appliance (your desktop GPU vs. the Pi's KMSDRM) — measured budgets
still come from spike S-1 on real hardware.

## Checklist

| ID | Steps | Expect | Verifies |
|---|---|---|---|
| MT-1 | Start preview, switch view live ↔ delayed | Delayed bar lags the live bar by the configured delay (2 s after setup) | FR-1 |
| MT-2 | Drag the delay slider to 5 s, watch the delayed view | The view jumps *back* — recent past replays once; no freeze | FR-3, FR-4 |
| MT-3 | Drag the delay down to 1 s | The view jumps *forward*; nothing shows twice | FR-4 |
| MT-4 | Right after a fresh start: set delay 60 s | "warming up" badge in the status panel; delayed view shows the oldest frame and crawls forward | FR-10 |
| MT-5 | Download a 10 s clip (mp4) | File plays (QuickTime/browser/phone), ~10 s long, content matches what the delayed view showed | FR-5 |
| MT-6 | Download with seconds = 0 (type it manually) | Clean error from the UI, no download; API answers 422 problem+json | FR-11 |
| MT-7 | Start a 60 s clip download; while it runs, watch "dropped frames" in the status panel | Stays 0 during the export | FR-6 |

Notes:
- **Mirror flip (FR-2)** is applied by the SDL display renderer, *not* by the
  preview stream — observe it in the `TV=1` display window (or on the real
  HDMI output), not in the browser preview.
- Clean shutdown: Ctrl-C in the script terminal — the binary must exit by
  itself (no kill -9), which the e2e suite also checks.

## On the Pi (real hardware, milestone M3/M4)

Same checklist against `http://zeitspiegel.local` with `source = "camera"`,
plus:

- MT-8: film a millisecond stopwatch; measured glass-to-glass delay =
  configured delay + `min_latency_ms` ± 1 frame (TESTPLAN M3).
- MT-9: pull the plug mid-operation; power on → mirror back ≤ 25 s (FR-12,
  NFR-9).
- MT-10: unplug the camera USB mid-run → status degraded, /healthz 503;
  replug → picture returns without restart (NFR-5).
