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
| `zeitspiegel.service` | `Restart=always` with `StartLimitIntervalSec=0` (an appliance never stops retrying), `RestartSec=1`, `RuntimeDirectory=zeitspiegel` (tmpfs scratch dir; clips are streamed to the client and never written to disk), journal logging; ordered after `local-fs.target` only — no network ordering, the mirror must work with Wi-Fi down and the web UI appears when the network does. An `ExecStartPre` waits ≤5 s for `/dev/dri/card*` so early boot doesn't race udev's DRM cold-plug |
| `config.toml` | profile=auto (E-2: highest MJPEG mode capped at 1080p), buffer 60 s / 1 GiB cap, mirror_flip=true, focus pinning, bind `:80` |
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

## Several units in one room (E-8)

Every card carries the **identical image**; the role is elected at boot, not
baked. Bake once with `FLEET_SIZE=3 make sd` and write that same image to all
three cards.

- On boot a unit looks for the `zeitspiegel` network. If it is on the air, the
  unit joins it and registers with whoever is hosting; if not, the unit hosts
  it. Power-on order does not matter — a unit that boots late just joins.
- There is only ever **one** SSID, and whoever hosts it takes `10.42.0.1` and
  the name `zeitspiegel.local`. The printed poster and the QR codes therefore
  stay valid no matter which box is hosting. The others answer to
  `zeitspiegel-<unit id>.local`.
- Open `http://zeitspiegel.local` and you get a card per mirror, each with its
  own delay slider and download button. Delays are entirely independent; the
  page talks to each unit directly, so a clip download never relays through
  the hosting unit.
- **Pull the host's plug** and one survivor takes over within roughly 20–45 s
  (measure on site and record it here). Every mirror keeps running throughout
  — `zeitspiegel.service` never waits on the network. Plug the old one back in
  and it rejoins as an ordinary member; it does not take the network back,
  because a handback would cost the room a second outage for nothing.
- To name a unit, plug its SD card into any computer and write one line into
  `zeitspiegel-name.txt` on the FAT32 `bootfs` partition (e.g. `Barre`). The
  image itself stays identical. Unnamed units call themselves
  `Zeitspiegel <ID>` after their CPU serial.
- **The one thing to check on site first:** a phone and a member, both joined
  to the hosting unit's AP, must be able to reach each other
  (`curl http://10.42.0.x/healthz` from a laptop on the network).
  NetworkManager exposes no client-isolation knob and does not enable one, so
  this should work — but the whole combined page depends on it.
- Radio: all units share one channel. Control traffic is negligible, but a
  full-buffer clip from a member relays member→host→phone on 2.4 GHz. With
  every unit in one room, `AP_BAND=a AP_CHANNEL=36` (non-DFS) gives a lot more
  headroom; `bg`/6 remains the compatibility default.
- Split brain: two units that come up in the same stagger slot can both host
  the same SSID, and neither can detect it — a radio in AP mode cannot scan.
  A host that stays short of `fleet_size` for ~90 s drops its network, looks
  around and joins the other one, so the fleet recovers on its own at the cost
  of a brief interruption. Each fruitless attempt backs the next one off, so a
  unit that is simply switched off does not cost an outage every 90 s.
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
- RAM budget: buffer cap 1 GiB (deploy/config.toml); typical 1080p30 MJPEG
  ≈ 6 MB/s ⇒ 60 s ≈ 360 MB, bright/high-motion scenes can spike toward the
  cap. The unit sets `GOMEMLIMIT=1400MiB`: buffer cap + one pinned export
  (a running clip holds its frames past eviction, hard rule 4; clips
  stream, so the pin lasts for the whole download — bounded by the single
  export slot and the handler's rolling 30 s write deadline) + ~200 MB
  process overhead. Without the limit, GOGC=100 lets the heap grow toward
  2× live between collections — long GC marks whose assist pauses hit the
  render loop as visible stutter. This accounting intentionally reads
  NFR-2's "byte budget + 200 MB" as excluding the export pin, which is
  bounded by the single export slot.
- Watching stutter diagnostics: `curl -s zeitspiegel.local/debug/vars`.
  Key fields — `zeitspiegel_render` (`tick_overruns`, `render_over_budget`,
  `render_max_us`, `held_streak_max` ≥ 3 = visible judder, `miss_*` ≠ 0
  after warm-up = delay outran the buffer), `zeitspiegel_capture` (`gaps`
  — a capture hole replays on screen `delay` seconds later,
  `max_frame_bytes`), `zeitspiegel_buffer` (`bytes_per_s` — live MJPEG
  bitrate, the bright-scene signal), `zeitspiegel_display`
  (`software` must be false, `refresh_hz` should be 60,
  `texture_recreates` steady at 1), and the stdlib `memstats`
  (`NumGC`, `PauseNs`, `HeapAlloc`). The startup journal logs the
  negotiated renderer: `software=true` there means SDL silently fell back
  to software rendering, which cannot hold the budget at 1080p.
