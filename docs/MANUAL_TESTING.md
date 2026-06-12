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
  preview stream — the settings toggle works (PATCH /config round-trips) but
  the flipped image is only observable on real display hardware.
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
