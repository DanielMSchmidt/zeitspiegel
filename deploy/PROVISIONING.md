# deploy/PROVISIONING.md

# Provisioning — blank micro-SD to running mirror (M4)

Target: Raspberry Pi 5, Pi OS **Lite** 64-bit. See docs/DEPLOYMENT.md for the
appliance model and hardware checklist. The appliance hosts its own Wi-Fi
(E-7) — internet is needed exactly once, during step 2.

## 1. Make the card (plug and play, macOS)

Insert the micro-SD into your computer, then:

```
make sd                      # auto-detects the card, asks before erasing
SSID=studio-mirror make sd   # choose the Wi-Fi network name
```

This downloads Pi OS Lite (cached under `build/cache/`), cross-builds the Pi
binary, and **bakes a finished image** — ffmpeg/SDL2 packages, the binary,
the open Wi-Fi access point and the `zeitspiegel` admin user are all
installed into the image inside a Docker container, so the card needs **no
network, ever**. The Wi-Fi is open (no password). Your `~/.ssh/*.pub` is
authorized automatically; `sudo` is passwordless for the admin user (E-7 /
NFR-6 — the appliance is LAN-only and key-gated, so a sudo password adds
no defense). A random admin password is still generated for the local
console and saved to `build/credentials.txt`; if you lose it, re-baking
will print a new one.

`make image` bakes the image without touching a card (useful to inspect it
first); `make sd` runs that, then writes the card.

> Not on macOS? `make image` still works (it's all Docker). To write the
> card: `sudo dd if=build/zeitspiegel-appliance.img of=/dev/sdX bs=4M
> conv=fsync`. Or flash stock Pi OS Lite with the Imager and run
> `sudo ./setup.sh --seal` on the Pi (needs internet once).

## 2. First boot (no network needed)

Put the card in the Pi, attach HDMI + Kiyo + the official 5 V/5 A PSU, power
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

## 5. Config changes / updates on the sealed appliance

`ssh zeitspiegel@zeitspiegel.local` (key only — `sudo` is passwordless).
The root is read-only (NFR-9); writes outside `/var/log/journal` (NFR-8)
vanish on reboot. Two-command unseal:

```
sudo raspi-config nonint disable_overlayfs && sudo reboot
# ...edit /etc/zeitspiegel/config.toml, or re-run setup.sh for a new binary...
sudo raspi-config nonint enable_overlayfs && sudo reboot
```

To rename the Wi-Fi: unseal, `SSID=new-name sudo -E ./setup.sh`, re-seal.

## 6. Troubleshooting

- Logs: `journalctl -u zeitspiegel` (persistent across reboots — NFR-8,
  so a no-AP / no-screen failure can still be diagnosed after a power
  cycle); the one-time seal: `journalctl -u zeitspiegel-seal`
- Metrics: `GET http://zeitspiegel.local/debug/vars` (expvar)
- No `zeitspiegel` Wi-Fi after a few minutes → check the seal log on the
  HDMI console. The regulatory domain is baked into `cmdline.txt`
  (`cfg80211.ieee80211_regdom=`, default DE, `WIFI_COUNTRY=` at build time).
- `zeitspiegel.local` not resolving but Wi-Fi joined → use
  `http://10.42.0.1`.
