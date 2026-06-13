# deploy/PROVISIONING.md

# Provisioning — blank micro-SD to running mirror (M4)

Target: Raspberry Pi 5, Pi OS **Lite** 64-bit. See docs/DEPLOYMENT.md for the
appliance model and hardware checklist. The appliance hosts its own Wi-Fi
(E-7) — internet is needed exactly once, during step 2.

## 1. Make the card (plug and play, macOS)

Insert the micro-SD into your computer, then:

```
make sd                      # auto-detects the card, asks before erasing
AP_PASS=studiopw make sd     # choose the Wi-Fi passphrase yourself
```

This downloads Pi OS Lite (cached under `build/cache/`), cross-builds the
Pi binary in Docker, flashes the card, and stages everything the Pi needs
to finish setting itself up: enable SSH (your `~/.ssh/*.pub` is authorized
automatically), create the `zeitspiegel` admin user, and install the
first-boot provisioning hook. The Wi-Fi and admin passwords are printed at
the end **and stored on the card** in the `zeitspiegel/` folder — re-insert
the card any time to read them.

> Not on macOS? Flash Pi OS Lite with the Raspberry Pi Imager (hostname
> `zeitspiegel`, enable SSH), then copy `deploy/`, `deploy/sd/` and the
> `make pi-binary` output onto the Pi and run `sudo ./setup.sh --seal`.

## 2. First boot (once, with ethernet)

Put the card in the Pi, connect **ethernet to any router**, attach HDMI +
Kiyo + the official 5 V/5 A PSU, power on, and wait (~10 minutes: the Pi
resizes its filesystem, installs packages, creates its access point, seals
the read-only overlay, and reboots twice — no interaction).

Done when the Wi-Fi `zeitspiegel` appears. Unplug ethernet. If the AP never
appears, the Pi likely had no internet: check the ethernet link and
power-cycle — provisioning retries on every boot until it succeeds.

## 3. Use it

- Join Wi-Fi `zeitspiegel` (passphrase from step 1)
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

`ssh zeitspiegel@zeitspiegel.local` (key or the admin password from the
card). The root is read-only (NFR-9); writes vanish on reboot. Two-command
unseal:

```
sudo raspi-config nonint disable_overlayfs && sudo reboot
# ...edit /etc/zeitspiegel/config.toml, or re-run setup.sh for a new binary...
sudo raspi-config nonint enable_overlayfs && sudo reboot
```

To change the Wi-Fi passphrase: unseal, `AP_PASS=newpw sudo -E ./setup.sh`,
re-seal.

## 6. Troubleshooting

- Logs: `journalctl -u zeitspiegel` (volatile, lost on power-off);
  provisioning: `journalctl -u zeitspiegel-firstboot`
- Metrics: `GET http://zeitspiegel.local/debug/vars` (expvar)
- No `zeitspiegel` Wi-Fi → first boot has not finished (or had no
  internet); plug in ethernet and power-cycle. Radio blocked? The Wi-Fi
  country must be set (`WIFI_COUNTRY=`, default DE).
- `zeitspiegel.local` not resolving but Wi-Fi joined → use
  `http://10.42.0.1`.
