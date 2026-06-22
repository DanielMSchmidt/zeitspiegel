# docs/DEPLOYMENT.md

# Deployment — Appliance Model

Target: Raspberry Pi 5, Raspberry Pi OS **Lite** 64-bit (Bookworm+), no
desktop. Operating model: plug in power + HDMI + USB camera → full-screen
mirror in ≤ 25 s. Power off = pull the plug (safe by design, NFR-9).

## Hardware checklist

- Raspberry Pi 5, **8 GB** (required for the default 12 min / 5 GiB 1080p
  ring buffer; the 4 GB variant cannot hold it); official 5 V/5 A PSU
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
| `setup.sh` | idempotent on fresh Pi OS Lite: install ffmpeg + SDL2/libjpeg runtime, copy binary/unit/config, hostname `zeitspiegel`, create the open Wi-Fi AP (`AP_SSID`/`WIFI_COUNTRY`), enable service, enable read-only overlayfs (`raspi-config nonint enable_overlayfs`) **last** |
| `sd/bake.sh` | runs in a privileged linux/arm64 container (`make image`): loop-mounts a stock Pi OS image, grows the root, chroots in to `apt install` ffmpeg + SDL2 + NetworkManager + dnsmasq-base/iptables (needed by `ipv4.method=shared`) + rfkill/iw (for in-place debug), writes the binary, AP keyfile, user, regdomain, NOPASSWD sudo for the admin, persistent journal, and clears the stock rfkill soft-block — produces a finished, network-free image |
| `sd/seal.sh` + `zeitspiegel-seal.service` | one-time first-boot finisher baked into the image: stages `authorized_keys` for the SSH escape hatch, enables the read-only overlay, reboots; self-disables (offline). SSH itself stays masked. |
| `PROVISIONING.md` | plug-and-play path: `make sd` (bakes the image + writes the card on macOS) → boot once, no network → done |

## Network & discovery (E-7: the appliance is its own network)

- The Pi hosts an **open** (passwordless) access point via NetworkManager
  (`zeitspiegel-ap` profile, `ipv4.method shared` → built-in DHCP, gateway
  `10.42.0.1`). Phones/laptops just pick SSID `zeitspiegel` and connect — no
  password, no venue Wi-Fi, no router, no client-isolation surprises. The AP
  is an isolated, internet-less LAN serving only the LAN-only control UI
  (NFR-6, no auth in v1).
- mDNS via Avahi (preinstalled): `http://zeitspiegel.local`; fallback
  `http://10.42.0.1` always works.
- The Pi never needs internet: packages are baked into the image at build
  time (`make image`, on your computer). Clients on the AP get no internet
  either; phones may warn about it ("stay connected" once).
- Radio: 2.4 GHz (band bg, channel 6) for maximum device compatibility; the
  regulatory domain must be set (default DE, `WIFI_COUNTRY=`) — and the
  stock Pi OS image's saved rfkill state must be cleared at bake time
  (bake.sh does this), or the radio stays soft-blocked even with the
  regdom set, and NM logs `Wi-Fi disabled by radio killswitch; disabled
  by state file` while wlan0 stays in `unavailable`.
- The join-venue-Wi-Fi variant is preserved on the `wifi-client` branch.

## Operations

- Logs: `journalctl -u zeitspiegel` — persistent across reboots (NFR-8),
  bake.sh creates `/var/log/journal/` so post-mortem debug survives without
  needing a screen attached. Metrics: `GET /debug/vars`.
- Admin: SSH is **off** by default (bake.sh masks `ssh.service`); the
  supported admin path is re-flashing the card. The bake-time random
  password (saved in `build/credentials.txt`) is for the local HDMI +
  keyboard console; `sudo` is passwordless.
- SSH escape hatch (post-mortem / on-site debug only): mount the SD's
  FAT32 `bootfs` on any computer and `touch ssh` — Pi OS unmasks
  `ssh.service` on next boot. The `authorized_keys` from your
  `~/.ssh/*.pub` was staged at bake time, so the key already works.
- Config change / update: re-flash via `make sd`. If you must edit
  in place, unseal first (`raspi-config nonint disable_overlayfs` +
  reboot), apply, re-enable + reboot — full procedure in PROVISIONING.md §5.
- RAM budget: buffer cap 5 GiB default (12 min × ~6 MB/s 1080p30 MJPEG ≈
  4.3 GiB + spike headroom). Requires the 8 GiB Pi 5 variant; the 4 GiB
  variant cannot hold the default buffer. To shrink, lower `buffer_max_s`
  (and optionally `buffer_max_bytes`) in config.toml; the delay slider cap
  is independent (`delay_max_s`, default 120 s).
