# deploy/PROVISIONING.md

# Provisioning — blank micro-SD to running mirror (M4)

Target: Raspberry Pi 5, Pi OS **Lite** 64-bit. See docs/DEPLOYMENT.md for the
appliance model and hardware checklist.

## 1. Flash the SD card

Raspberry Pi Imager → Raspberry Pi OS Lite (64-bit). In the Imager's
customization dialog:

- hostname: `zeitspiegel`
- enable SSH, public-key auth (paste your key)
- Wi-Fi: the **regular member network**, not a guest network. Guest networks
  block client-to-client traffic → no `zeitspiegel.local`, no web UI (E-6,
  docs/DEPLOYMENT.md §Network).

## 2. Install

Boot the Pi (HDMI + Kiyo + official 5 V/5 A PSU), then from your machine:

```
make build-pi                                   # arm64 binary with v4l2+sdl tags
scp zeitspiegel deploy/* zeitspiegel.local:~/   # binary + unit + config + setup.sh
ssh zeitspiegel.local
sudo ./setup.sh          # add --seal to enable the read-only overlay non-interactively
sudo reboot              # required if you sealed
```

`setup.sh` is idempotent: re-running updates the binary and unit but never
overwrites an edited `/etc/zeitspiegel/config.toml`.

## 3. Power-cycle test

Pull the plug mid-operation. Plug back in. The full-screen mirror must be
back in ≤ 25 s with no interaction (FR-12); the buffer starts empty, the
web UI reappears once Wi-Fi is up. If anything required fsck or manual
recovery, the overlay is not enabled — check `sudo raspi-config nonint
get_overlay_now` (0 = enabled).

## 4. Config changes / updates on the sealed appliance

The root is read-only (NFR-9); writes vanish on reboot. Two-command unseal:

```
sudo raspi-config nonint disable_overlayfs && sudo reboot
# ...edit /etc/zeitspiegel/config.toml, or re-run setup.sh for a new binary...
sudo raspi-config nonint enable_overlayfs && sudo reboot
```

## 5. Troubleshooting

- Logs: `journalctl -u zeitspiegel` (volatile, lost on power-off)
- Metrics: `GET http://zeitspiegel.local/debug/vars` (expvar)
- Behind a Fritzbox also: `http://zeitspiegel.fritz.box`
- No `zeitspiegel.local` at all → almost always guest Wi-Fi / client
  isolation; move the Pi (and your client) to the member network (E-6).
