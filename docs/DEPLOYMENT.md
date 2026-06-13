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
| `setup.sh` | idempotent on fresh Pi OS Lite: install ffmpeg + SDL2/libjpeg runtime, copy binary/unit/config, hostname `zeitspiegel`, create the Wi-Fi AP (`AP_SSID`/`AP_PASS`/`WIFI_COUNTRY`), enable service, enable read-only overlayfs (`raspi-config nonint enable_overlayfs`) **last** |
| `sd/` | first-boot machinery staged onto the card by `make sd`: `firstrun.sh` (offline basics via the Imager's cmdline hook), `zeitspiegel-firstboot.service` + `firstboot.sh` (full provisioning on the first boot with network, retries each boot until it succeeds, then seals + disables itself) |
| `PROVISIONING.md` | plug-and-play path: `make sd` (flashes + stages the card on macOS) → boot once with ethernet → done; manual Imager path as fallback |

## Network & discovery (E-7: the appliance is its own network)

- The Pi hosts a WPA2 access point via NetworkManager (`zeitspiegel-ap`
  profile, `ipv4.method shared` → built-in DHCP, gateway `10.42.0.1`).
  Phones/laptops join SSID `zeitspiegel` directly — no venue Wi-Fi, no
  router, no client-isolation surprises.
- mDNS via Avahi (preinstalled): `http://zeitspiegel.local`; fallback
  `http://10.42.0.1` always works.
- Internet is needed exactly once (first-boot apt install) — plug in
  ethernet for provisioning, unplug afterwards. Clients on the AP get no
  internet; phones may warn about it ("stay connected" once).
- Radio: 2.4 GHz (band bg, channel 6) for maximum device compatibility; the
  regulatory domain must be set (default DE, `WIFI_COUNTRY=`) or the radio
  stays rfkill-blocked.
- The join-venue-Wi-Fi variant is preserved on the `wifi-client` branch.

## Operations

- Logs: `journalctl -u zeitspiegel` (volatile). Metrics: `GET /debug/vars`.
- Config change / update: temporarily disable overlay
  (`raspi-config nonint disable_overlayfs` + reboot), apply, re-enable +
  reboot. Two-command procedure in PROVISIONING.md.
- RAM budget: buffer cap 1.5 GB default; 720p60 MJPEG ≈ 5 MB/s ⇒ 120 s ≈
  600 MB (1080p30 ≈ 6 MB/s ⇒ 720 MB).
