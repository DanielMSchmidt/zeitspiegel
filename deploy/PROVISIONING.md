# deploy/PROVISIONING.md

# Provisioning — blank micro-SD to running mirror (M4)

Target: Raspberry Pi 5, Pi OS **Lite** 64-bit. See docs/DEPLOYMENT.md for the
appliance model and hardware checklist. The appliance hosts its own Wi-Fi
(E-7) — internet is needed exactly once, during step 2.

## 1. Make the card (plug and play, macOS)

Insert the micro-SD into your computer, then:

```
make sd NAME="Long Side"                    # auto-detects the card, asks before erasing
SSID=studio-mirror make sd NAME="Long Side" # choose the Wi-Fi network name too
DISK=/dev/disk4 make sd NAME="Long Side"    # pick the card yourself (see `diskutil list`)
```

Auto-detection accepts any removable disk, whether the card sits in a built-in
reader or a USB one. It refuses to write to fixed disks and to the disk your
Mac booted from; if it finds more than one candidate it stops and asks you to
name the card with `DISK=`.

`NAME` is required: it is the label this mirror shows in the UI, so the
combined page says "Long Side" and "Window seat" instead of two hex ids. It is
written into the image's boot partition *just before* the card is written, so
the card is named the moment it exists and never has to be mounted afterwards.
The bake itself stays label-free — the label is the one per-card file, not a
per-card build. Up to 32 characters; longer labels are truncated by the unit
and `make sd` warns before it writes anything. To ship a card that names
itself, pass `NAME=auto`.

**Several mirrors?** Nothing changes: write that same image to every card —
including cards you buy years later. The role is elected at boot and the
fleet's size is discovered, not configured, so there is no per-card build, no
"first" card, and no count to keep in sync. Whichever unit is on first hosts
the network; the rest join it; if the host is unplugged another takes over by
itself. Power-on order does not matter.

Give each card its own `NAME` as you write it. To rename a unit later, or to
name a card that was written with `NAME=auto`, plug it into any computer and
stage the label onto the FAT32 `bootfs` partition — no re-flash:

```
scripts/stage-name.sh "Long Side" /Volumes/bootfs   # or: echo "Long Side" > /Volumes/bootfs/zeitspiegel-name.txt
```

Unnamed units call themselves `Zeitspiegel <ID>` after their CPU serial. The
image stays byte-identical either way.

This downloads Pi OS Lite (cached under `build/cache/`), cross-builds the Pi
binary, and **bakes a finished image** — ffmpeg/SDL2 packages, the binary,
the open Wi-Fi access point and the `zeitspiegel` admin user are all
installed into the image inside a Docker container, so the card needs **no
network, ever**. The Wi-Fi is open (no password). `sudo` is passwordless
for the admin user (E-7 / NFR-6 — the appliance is LAN-only, so a sudo
password adds no defense). SSH is **off** by default: the appliance is
re-imaged, not logged into. A random admin password is still generated
for the local (HDMI + keyboard) console and saved to
`build/credentials.txt`; if you lose it, re-baking prints a new one. Your
`~/.ssh/*.pub` is staged into the image as `authorized_keys` so the SSH
escape hatch below works without rebuilding.

`make image` bakes the image without touching a card (useful to inspect it
first); `make sd` runs that, then writes the card.

> Not on macOS? `make image` still works (it's all Docker). To write the
> card: `sudo dd if=build/zeitspiegel-appliance.img of=/dev/sdX bs=4M
> conv=fsync`. Or flash stock Pi OS Lite with the Imager and run
> `sudo ./setup.sh --seal` on the Pi (needs internet once).

## 2. First boot (no network needed)

Put the card in the Pi, attach HDMI + the camera + the official 5 V/5 A PSU, power
on, and wait (~3 minutes: the Pi resizes its filesystem, creates the user,
brings up its access point, seals the read-only overlay, and reboots a
couple of times — no interaction, no cable).

Done when the Wi-Fi `zeitspiegel` appears.

## 3. Use it

- Join Wi-Fi `zeitspiegel` (open — no password)
- Open `http://zeitspiegel.local` — fallback `http://10.42.0.1`
- Phones may warn "no internet on this network" — stay connected; the
  appliance is intentionally offline (E-7).

## 4. Power-cycle test

Pull the plug mid-operation. Plug back in. The full-screen mirror must be
back in ≤ 25 s with no interaction (FR-12); the buffer starts empty, the AP
and web UI reappear. If anything required fsck or manual recovery, the
overlay is not enabled — check `sudo raspi-config nonint get_overlay_now`
(0 = enabled).

## 5. Config changes / updates

Re-flash the card. SSH is off by default and the root is read-only
(NFR-9), so the supported update path is to edit
`deploy/config.toml` (or whatever else) in your local checkout,
re-run `make sd NAME="…"`, and swap the card. Boot is fast enough that this
is genuinely simpler than logging in.

### Emergency SSH escape hatch

When you absolutely need to poke at a field appliance without re-imaging
(post-mortem, on-site debug), enable SSH for one boot by mounting the
SD's FAT32 `bootfs` partition on any computer and `touch ssh` on it.
On next boot, Pi OS sees that file and unmasks `ssh.service`. The
`authorized_keys` baked in from your `~/.ssh/*.pub` at image time is
used; `sudo` is passwordless.

To then make persistent changes the overlay has to come off — two-command
unseal:

```
sudo raspi-config nonint disable_overlayfs && sudo reboot
# ...edit /etc/zeitspiegel/config.toml, or re-run setup.sh for a new binary...
sudo raspi-config nonint enable_overlayfs && sudo reboot
```

To rename the Wi-Fi: unseal, `SSID=new-name sudo -E ./setup.sh`, re-seal.
Easier: just re-bake the card with `SSID=new-name make sd NAME="…"`.

Renaming a *mirror* needs none of that: the display name is read from
`zeitspiegel-name.txt` on the FAT32 `bootfs` partition, which stays writable
while the root overlay is sealed. Pull the card, run
`scripts/stage-name.sh "New name" /Volumes/bootfs` on any computer, put it
back.

## 6. Troubleshooting

- `make sd` and `make sd-dev` both re-bake, and the bake fails if the image is
  missing a library SDL loads at runtime. Flashing checks again — the bake
  records its verdict on the boot partition, and the card writer refuses an
  image that never passed, so an old image in `build/` cannot quietly become a
  black-screened unit (`ALLOW_UNVERIFIED_IMAGE=1` if that is the point).
- "The picture looked wrong": a development card writes a frame every 5 s to
  `/var/log/zeitspiegel/frames`, keeping the newest 30, and `make sd-logs`
  carries them in the bundle — so a colour cast or a framing problem arrives as
  JPEGs somebody can measure rather than as a description. Dev cards also log
  at debug level, and the boot capture asks the camera what it can do
  (`v4l2-ctl --all`, formats, controls), so nothing needs running on the unit.
- Delay badge looks blocky: the typeface (`fonts-liberation2`) is missing and
  the badge fell back to its built-in bitmap glyphs. `zeitspiegel-check-runtime /`
  says so by name — nothing links a font, so nothing else would.
- Black screen with the unit otherwise alive: check the runtime libraries
  first — `sudo zeitspiegel-check-runtime /` on the unit, or the "runtime
  libraries" section of the boot capture on a pulled card. SDL loads the EGL
  stack with `dlopen`, so a missing library is invisible to the build and
  shows up only as `EGL not initialized` at startup. `deploy/runtime-libs.txt`
  is the list; `deploy/runtime-packages.txt` is what installs it, shared by
  the image bake and `setup.sh` so the two cannot drift.

- Which build is this? `cat /boot/firmware/zeitspiegel-version.txt` on the
  card, `zeitspiegel --version` on the unit, `zeitspiegel_version` at
  `/debug/vars`, or the first line of the unit's log. `make sd-logs` puts it
  at the top of the report. The bake stamps the version onto the FAT32
  partition on purpose: the units that need identifying are the ones that
  will not boot. Override it for a reproducible build with
  `VERSION=v1.4.2 make image`.

- A unit that came back from a venue broken, with no screen and no Wi-Fi to
  log into: pull its card, put it in any reader and run `make sd-logs`. It
  finds the card (USB or built-in reader), reads it without writing to it, and
  collects the boot partition's `zeitspiegel-debug.log` / boot profile — which
  carries the unit's own journal lines, its restart count, journald's storage
  state and whether DRM and the camera showed up, so a dark unit explains
  itself even when nothing persisted — plus
  the persistent journal off the ext4 root into a single
  `zeitspiegel-logs-<mirror>-<timestamp>.zip` (a plain-text `report.txt`
  inside it, raw logs beside that, nothing else left on disk).
  On macOS the ext4 half needs `brew install e2fsprogs` — without it
  the report says so rather than looking like a quiet card. The AP key and the
  admin password hash are redacted, so the bundle can be attached to an issue.
  Two things it will not do quietly: without the ext4 reader it refuses to run
  at all rather than hand back a bundle with no journal in it (`--boot-only`
  overrides), and it reports whether the card is sealed — because a sealed
  card's journal ends at the seal.
- One boot is enough. The 30 s capture snapshots that boot's entire journal to
  `/boot/firmware/zeitspiegel-journal.log.gz`, which survives both cases where
  the ext4 journal is empty — a first boot (journald keeps it in RAM until the
  machine id is committed) and any boot of a sealed unit (the overlay sends it
  to tmpfs). `make sd-logs` reads it out into the report.
- A unit that fails *after running a while* needs the capture to keep up: it
  writes once per boot by default, so a card pulled at hour six otherwise
  carries hour zero. Drop an empty file named `zeitspiegel-capture-live` on the
  boot partition — from any laptop, card in a reader — and every timer firing
  (5 min) refreshes the profile and the journal snapshot instead. Dev cards
  ship with it; venue cards do not, because the persistent journal is meant to
  be the one write path (NFR-9). `make sd-logs` reports which mode the card
  was in.
- A unit you are actively debugging belongs on a **development card**:
  `make sd-dev NAME="Bench"` bakes and flashes the same image with the
  first-boot seal left off. The root stays writable, so `/var/log/journal`
  survives reboots and `make sd-logs` reads *every* boot off it instead of
  only the first. It writes its own `build/zeitspiegel-appliance-dev.img` and
  `build/credentials-dev.txt`, so a bench card can never be flashed in place
  of a production one. Dev images also ship a pre-seeded machine id, so
  journald writes to `/var/log/journal` from the very first boot; production
  images deliberately do not, because every card ships the same image (E-8)
  and a fleet sharing a machine id shares everything derived from it.
  The trade is the one the overlay buys back (NFR-9): an
  unsealed card can be corrupted by a pulled plug, so it belongs on a desk,
  not in a venue. Seal it later with
  `sudo systemctl enable zeitspiegel-seal && sudo reboot`.
- Logs: `journalctl -u zeitspiegel` (persistent across reboots — NFR-8,
  so a no-AP / no-screen failure can still be diagnosed after a power
  cycle); the one-time seal: `journalctl -u zeitspiegel-seal`
- Metrics: `GET http://zeitspiegel.local/debug/vars` (expvar)
- No `zeitspiegel` Wi-Fi after a few minutes → check the seal log on the
  HDMI console. The regulatory domain is baked into `cmdline.txt`
  (`cfg80211.ieee80211_regdom=`, default DE, `WIFI_COUNTRY=` at build time).
- `zeitspiegel.local` not resolving but Wi-Fi joined → use
  `http://10.42.0.1`.
- Renaming a card by hand says the bootfs is mounted read-only → macOS
  sometimes mounts a FAT32 that way, most often right after a write. Eject the
  card and put it back in; the fresh mount is writable. (`make sd` itself is
  not affected: it labels the image before the card, and never mounts the
  card.)
