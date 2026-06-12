# docs/DEPLOYMENT.md

# Deployment — Appliance Model

Target: Raspberry Pi 5, Raspberry Pi OS **Lite** 64-bit (Bookworm+), no
desktop. Operating model: plug in power + HDMI + USB camera → full-screen
mirror in ≤ 25 s. Power off = pull the plug (safe by design, NFR-9).

## Hardware checklist

- Raspberry Pi 5, 4 GB (8 GB for long 1080p buffers); official 5 V/5 A PSU
  (required — Kiyo is USB-powered and x264 exports load all cores); active
  cooler; micro-HDMI → HDMI cable
- Razer Kiyo (USB). Ring light is hardware-controlled via its bezel.
  Autofocus must be pinned in config (`focus_automatic_continuous=0`,
  `focus_absolute` from spike S-2) to prevent focus hunting during movement.

## Artifacts (deploy/)

| File | Content |
|---|---|
| `zeitspiegel.service` | `Restart=always`, `RuntimeDirectory=zeitspiegel` (tmpfs for clips), journal logging; ordered after `network-online.target` but NOT requiring it — the mirror must work with Wi-Fi down; the web UI appears when the network does |
| `config.toml` | profile=720p60, buffer 120 s / 1.5 GB cap, mirror_flip=true, focus pinning, bind `:80` |
| `setup.sh` | idempotent on fresh Pi OS Lite: install ffmpeg + SDL2/libjpeg runtime, copy binary/unit/config, hostname `zeitspiegel`, enable service, enable read-only overlayfs (`raspi-config nonint enable_overlayfs`) **last** |
| `PROVISIONING.md` | Raspberry Pi Imager (OS Lite + member-Wi-Fi credentials + SSH key in the Imager dialog) → boot → `ssh zeitspiegel.local` → `setup.sh` → power-cycle test |

## Network & discovery

- mDNS via Avahi (preinstalled): `http://zeitspiegel.local`. Works because
  clients and the Pi share the LAN; no router support needed beyond passing
  multicast.
- Behind a Fritzbox additionally `http://zeitspiegel.fritz.box` via the
  router's local DNS.
- **Must join the regular member Wi-Fi.** Guest networks block
  client-to-client traffic → no discovery, no web UI. A Pi-hosted access
  point is an explicit non-goal for v1 (documented fallback if a venue only
  offers guest Wi-Fi).

## Operations

- Logs: `journalctl -u zeitspiegel` (volatile). Metrics: `GET /debug/vars`.
- Config change / update: temporarily disable overlay
  (`raspi-config nonint disable_overlayfs` + reboot), apply, re-enable +
  reboot. Two-command procedure in PROVISIONING.md.
- RAM budget: buffer cap 1.5 GB default; 720p60 MJPEG ≈ 5 MB/s ⇒ 120 s ≈
  600 MB (1080p30 ≈ 6 MB/s ⇒ 720 MB).
