#!/usr/bin/env bash
# deploy/setup.sh — idempotent provisioning for a fresh Pi OS Lite 64-bit.
#
# Usage:  sudo ./setup.sh [--seal]
#
# Expects the built arm64 binary `zeitspiegel` next to this script (built
# elsewhere via `make pi-binary`, or on-device — see the dev-packages block
# below). Safe to re-run: an edited /etc/zeitspiegel/config.toml is never
# clobbered.
#
# Environment:
#   AP_SSID       Wi-Fi network the appliance hosts (default: zeitspiegel)
#   WIFI_COUNTRY  regulatory domain for the radio (default: DE)
# The AP is open (no password) — see E-7 / NFR-6.
#
# --seal   enable the read-only overlayfs at the end (NFR-9). Without the
#          flag you are prompted; answer no on a dev box you still edit.

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
    echo "error: run as root (sudo ./setup.sh)" >&2
    exit 1
fi

SEAL=no
[[ "${1:-}" == "--seal" ]] && SEAL=yes

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --- runtime dependencies (libs only; the binary links SDL2 dynamically) ---
apt-get update
apt-get install -y ffmpeg libsdl2-2.0-0 libsdl2-image-2.0-0

# For an on-device build instead, install the dev toolchain and run
# `make build-pi` in the repo:
#   apt-get install -y golang libsdl2-dev libsdl2-image-dev
# Then copy the resulting binary next to this script before re-running it.

# --- binary, config, unit ---
install -D -m 0755 "$DIR/zeitspiegel" /usr/local/bin/zeitspiegel

# Config only if absent — never overwrite an edited appliance config.
if [[ ! -f /etc/zeitspiegel/config.toml ]]; then
    install -D -m 0644 "$DIR/config.toml" /etc/zeitspiegel/config.toml
    echo "installed /etc/zeitspiegel/config.toml"
else
    echo "kept existing /etc/zeitspiegel/config.toml"
fi

install -D -m 0644 "$DIR/zeitspiegel.service" /etc/systemd/system/zeitspiegel.service
systemctl daemon-reload
systemctl enable --now zeitspiegel.service

# --- mDNS discovery as zeitspiegel.local via the preinstalled Avahi (NFR-10) ---
hostnamectl set-hostname zeitspiegel

# --- kiosk: silent boot, no login prompt (FR-12) ---------------------------
# The mirror must be the only thing on the HDMI output. Remove the console
# login, quiet the boot text, and disable the splash.
systemctl mask getty@tty1.service 2>/dev/null || true
CMD=/boot/firmware/cmdline.txt; [[ -f "$CMD" ]] || CMD=/boot/cmdline.txt
if [[ -f "$CMD" ]]; then
    read -r KLINE < "$CMD"
    for t in quiet loglevel=3 logo.nologo vt.global_cursor_default=0 consoleblank=0; do
        case " $KLINE " in *" $t "*) ;; *) KLINE="$KLINE $t" ;; esac
    done
    printf '%s\n' "$KLINE" > "$CMD"
fi
CFG=/boot/firmware/config.txt; [[ -f "$CFG" ]] || CFG=/boot/config.txt
[[ -f "$CFG" ]] && { grep -q '^disable_splash=1' "$CFG" || echo 'disable_splash=1' >> "$CFG"; }

# --- Wi-Fi access point (E-7): the appliance hosts its own OPEN network -----
# No password: guests just pick "$AP_SSID" and connect. The AP is an isolated,
# internet-less LAN (NFR-6, no auth in v1). NetworkManager's ipv4.method=shared
# runs DHCP for clients (gateway 10.42.0.1) and mDNS works with no router in
# between. The connection profile persists and autoconnects on boot.
AP_SSID="${AP_SSID:-zeitspiegel}"
WIFI_COUNTRY="${WIFI_COUNTRY:-DE}"
# Unblock the radio: a regulatory domain must be set before AP mode works.
raspi-config nonint do_wifi_country "$WIFI_COUNTRY" || true
rfkill unblock wifi || true
# Recreate the profile so an SSID change on re-run takes effect.
nmcli connection delete zeitspiegel-ap >/dev/null 2>&1 || true
nmcli connection add type wifi ifname wlan0 con-name zeitspiegel-ap \
    autoconnect yes connection.autoconnect-priority 100 \
    ssid "$AP_SSID" \
    802-11-wireless.mode ap 802-11-wireless.band bg 802-11-wireless.channel 6 \
    ipv4.method shared ipv6.method disabled
nmcli connection up zeitspiegel-ap || \
    echo "note: AP not up yet (radio may need a reboot) — autoconnect will bring it up"

# --- LAST step: seal the appliance — read-only overlayfs (NFR-9) ---------
# Must come last: after this, nothing above persists across reboots until
# the overlay is disabled again (see PROVISIONING.md, Operations).
if [[ "$SEAL" == "no" ]]; then
    read -r -p "Enable read-only overlayfs now (seal the appliance)? [y/N] " answer
    [[ "$answer" =~ ^[Yy]$ ]] && SEAL=yes
fi
if [[ "$SEAL" == "yes" ]]; then
    raspi-config nonint enable_overlayfs
    echo "overlayfs enabled — REBOOT REQUIRED for the read-only root to take effect."
else
    echo "overlayfs NOT enabled (dev mode). Seal later with: sudo raspi-config nonint enable_overlayfs && sudo reboot"
fi

echo
echo "Done: binary -> /usr/local/bin/zeitspiegel, config -> /etc/zeitspiegel/,"
echo "unit enabled and started, hostname -> zeitspiegel."
echo "Wi-Fi:     join \"$AP_SSID\" (open network, no password)"
echo "Mirror UI: http://zeitspiegel.local  (or http://10.42.0.1)"
