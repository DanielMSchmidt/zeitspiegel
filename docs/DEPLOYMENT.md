# docs/DEPLOYMENT.md

# Deployment — Appliance Model

Target: Raspberry Pi 5, Raspberry Pi OS **Lite** 64-bit (Bookworm+), no
desktop. Operating model: plug in power + HDMI + USB camera → full-screen
mirror in ≤ 25 s. Power off = pull the plug (safe by design, NFR-9).

## Hardware checklist

- Raspberry Pi 5, 4 GB (8 GB for long 1080p buffers); official 5 V/5 A PSU
  (required — the camera is USB-powered and x264 exports load all cores);
  active cooler; micro-HDMI → HDMI cable
- A **wide-angle (~100-110° diagonal), fixed-focus UVC webcam with native
  MJPEG** and no in-camera AI processing. Selection criteria, the field-of-view
  and depth-of-field numbers, why 4K and AI webcams are rejected, and the
  ranked shortlist are in **docs/HARDWARE.md** (provisional until spike S-2).
  Verify on arrival with `v4l2-ctl --list-formats-ext` — a camera without a
  discrete MJPEG mode at or below 1080p cannot be opened at all.
  - Fixed focus needs no pinning: the lens sits at its hyperfocal distance, so
    everything from ~0.3-0.4 m to infinity is sharp, and the focus controls
    simply do not exist (they are skipped at open, and logged — FR-9).
  - On an **autofocus** camera instead, pin it: `focus_auto = false` plus a
    `focus_absolute` measured in spike S-2, or it hunts during movement.

## Artifacts (deploy/)

| File | Content |
|---|---|
| `zeitspiegel.service` | `Restart=always` with `StartLimitIntervalSec=0` (an appliance never stops retrying), `RestartSec=1`, `RuntimeDirectory=zeitspiegel` (tmpfs scratch dir; clips are streamed to the client and never written to disk), journal logging; ordered after `local-fs.target` only — no network ordering, the mirror must work with Wi-Fi down and the web UI appears when the network does. An `ExecStartPre` waits ≤5 s for `/dev/dri/card*` so early boot doesn't race udev's DRM cold-plug |
| `config.toml` | profile=auto (E-2: highest MJPEG mode capped at 1080p), buffer 60 s / 1 GiB cap, mirror_flip=true, focus pinning, bind `:80` |
| `setup.sh` | idempotent on fresh Pi OS Lite: install ffmpeg + SDL2/libjpeg runtime, copy binary/unit/config, hostname `zeitspiegel`, create the open Wi-Fi AP (`AP_SSID`/`WIFI_COUNTRY`), enable service, enable read-only overlayfs (`raspi-config nonint enable_overlayfs`) **last** |
| `sd/bake.sh` | runs in a privileged linux/arm64 container (`make image`): loop-mounts a stock Pi OS image, grows the root, chroots in to `apt install` ffmpeg + SDL2 + NetworkManager + dnsmasq-base/iptables (needed by `ipv4.method=shared`) + rfkill/iw (for in-place debug), writes the binary, AP keyfile, user, regdomain, NOPASSWD sudo for the admin, persistent journal, and clears the stock rfkill soft-block — produces a finished, network-free image |
| `sd/seal.sh` + `zeitspiegel-seal.service` | one-time first-boot finisher baked into the image: stages `authorized_keys` for the SSH escape hatch, enables the read-only overlay, reboots; self-disables (offline). SSH itself stays masked. |
| `PROVISIONING.md` | plug-and-play path: `make sd NAME="Long Side"` (bakes the image + writes and names the card on macOS) → boot once, no network → done |

## Network & discovery (E-7: the appliance is its own network)

- The Pi hosts an **open** (passwordless) access point via NetworkManager
  (`zeitspiegel-ap` profile, `ipv4.method shared` → built-in DHCP, gateway
  `10.42.0.1`). Phones/laptops just pick SSID `zeitspiegel` and connect — no
  password, no venue Wi-Fi, no router, no client-isolation surprises. The AP
  is an isolated, internet-less LAN serving only the LAN-only control UI
  (NFR-6, no auth in v1).
- mDNS via Avahi (preinstalled): `http://zeitspiegel.local`; fallback
  `http://10.42.0.1` always works.

## Several units, added over time (E-8)

Every card carries the **identical image** and nothing about the fleet is
configured — not even how many units exist. `make sd NAME="…"` once per card,
for every card you own now and every card you buy later: the image is the same
one, only the label staged onto the boot partition differs.

- On boot a unit looks for the `zeitspiegel` network. On the air ⇒ join it
  and register with whoever is hosting; not ⇒ host it. Power-on order never
  matters, and units are stopped by cutting power — nothing depends on a
  clean shutdown anywhere.
- There is only ever **one** SSID; whoever hosts it takes `10.42.0.1` and
  answers to `zeitspiegel.local`, so the printed poster and QR codes stay
  valid across every failover. Members answer to `zeitspiegel-<unit id>.local`.
  The rename is done live via `avahi-set-host-name` on every role change —
  Avahi does not follow kernel hostname changes, so without it
  `zeitspiegel.local` would stay stuck on whichever unit won the boot-time
  name race and go dark when the host changes.
- **Client ceiling:** the Pi's built-in radio serves at most **~8 associated
  stations** in AP mode (a brcmfmac firmware memory limit — Pi 4-class
  hardware measures 7, and it is not tunable). Member units count against
  it, so a 3-mirror fleet leaves roughly **5 phone slots**. That fits the
  intended use — one person per mirror adjusting its delay — comfortably; a
  whole class connecting phones simultaneously will find later phones simply
  fail to associate, silently. If that day comes, the escalation paths are
  the `cyfmac43455-sdio-minimal.bin` firmware variant (reported ~19 clients,
  feature-stripped, unvalidated) or a dedicated router — measure before
  trusting either.
- `http://zeitspiegel.local` shows a card per mirror — its own delay slider
  and download button each. Delays are fully independent; the page talks to
  each unit directly, so a clip download never relays through the host.
- **A host serving anyone never takes the network down.** "Anyone" includes
  a dancer's phone: a single station used all afternoon holds its Wi-Fi
  unconditionally. Only a host with nobody attached occasionally drops its
  AP for a few seconds to check whether another network exists (that is how
  two units that cold-booted into separate networks find each other) — and
  since nobody is attached, nobody notices.
- **Pull the host's plug** and a survivor takes over in roughly 15–20 s
  (measure on site and record it here); phones rejoin the same open SSID by
  themselves. Every mirror keeps mirroring throughout — the display path
  never waits on the network. Plug the old unit back in and it joins as an
  ordinary member.
- Name a unit as you write its card: `make sd NAME="Long Side"`. The label lands
  in `zeitspiegel-name.txt` on the FAT32 `bootfs` partition, so renaming later
  is `scripts/stage-name.sh "Long Side" /Volumes/bootfs` on any computer — no
  re-flash. Cards written with `NAME=auto` call themselves `Zeitspiegel <ID>`
  after their CPU serial. The image stays identical either way.
- **First thing to check on site:** a phone and a member unit, both on the
  network, must reach each other (`curl http://10.42.0.x/healthz` from a
  laptop). The combined page rests on that.
- Radio: all units share one channel; `AP_BAND=a AP_CHANNEL=36 make sd NAME="…"`
  (non-DFS 5 GHz) gives clip downloads far more headroom in one room;
  `bg`/6 stays the compatibility default. The station profile ships with
  Wi-Fi powersave disabled (`powersave=2`) — Pi OS's default powersave is a
  documented source of latency and dropped links on units that serve HTTP,
  and these are on wall power.
- Election observability: `curl -s zeitspiegel.local/debug/vars` →
  `zeitspiegel_fleet` (`role`, `role_changes`, `heal_probes`,
  `announce_failures`, `peers`); role transitions are also in the journal.

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
- Config change / update: re-flash via `make sd NAME="…"`. If you must edit
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
