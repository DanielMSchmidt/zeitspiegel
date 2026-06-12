# Zeitspiegel

A time-delayed video mirror appliance. A camera + display show you the camera
image with a configurable delay (0–n seconds) — perform a movement, watch it a
few seconds later without looking at the screen during execution. Built for
movement training (dance, strength training, technique work).

## What it does

- Full-screen delayed mirror display on an HDMI screen (horizontally flipped,
  like a real mirror)
- Delay adjustable at runtime from any phone/laptop via a web UI
- Download the last *n* seconds as an MP4 from the web UI

## How it works (one paragraph)

A single Go process reads MJPEG frames from a UVC webcam via V4L2 and pushes
them into a time-indexed in-RAM ring buffer. The display renderer reads the
frame at `now − delay` each tick, decodes that one JPEG, and renders it
full-screen via SDL2/KMSDRM (no X11). The clip exporter reads a time window
from the same buffer and pipes it through ffmpeg to MP4. A stdlib HTTP server
exposes the control API and a static web UI. Because the buffer stores
intra-only MJPEG, every frame is independently decodable — variable delay and
frame-accurate export are trivial, and no live encoder is needed.

## Target hardware

Raspberry Pi 5 (4/8 GB) + Razer Kiyo (USB, native MJPEG, 720p60 default) +
any HDMI display. Runs as an unattended appliance: power on → mirror in ~20 s;
control via `http://zeitspiegel.local`. Power-off = pull the plug (root FS is
read-only).

## Repository layout

```
cmd/zeitspiegel/     main: wiring, lifecycle

internal/frame/      Frame type, JPEG APP segment helpers      (pure Go)

internal/ringbuf/    time-indexed ring buffer                  (pure Go)

internal/engine/     delay scheduler, frame selection          (pure Go)

internal/window/     export windowing [t−n, t]                 (pure Go)

internal/export/     ffmpeg invocation, temp files

internal/httpapi/    REST handlers, validation, MJPEG preview

internal/config/     TOML config

internal/synth/      synthetic source, fake clock/display (test infra + demo mode)

internal/camera/     V4L2 source via go4vl        [build tag: v4l2]

internal/screen/     SDL2/KMSDRM display          [build tag: sdl]

web/                 static UI (embedded)

deploy/              systemd unit, config, setup script, provisioning guide

docs/                ARCHITECTURE.md, REQUIREMENTS.md, TESTPLAN.md, DEPLOYMENT.md
```

# Documentation

| Doc | Read it for |
|---|---|
| [CLAUDE.md](CLAUDE.md) | Working conventions, commands, hard rules (start here if you're an LLM) |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Design decisions D1–D7, components, concurrency model |
| [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md) | FR/NFR list, API contract |
| [docs/TESTPLAN.md](docs/TESTPLAN.md) | Test tiers UT/IT/ST, TDD build order, milestones |
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | Appliance model, provisioning, network |
