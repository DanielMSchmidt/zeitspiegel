#!/usr/bin/env bash
# deploy/setup.sh — idempotent provisioning for a fresh Pi OS Lite 64-bit.
#
# Usage:  sudo ./setup.sh [--seal]
#
# Expects the built arm64 binary `zeitspiegel` next to this script (built
# elsewhere via `make build-pi`, or on-device — see the dev-packages block
# below). Safe to re-run: an edited /etc/zeitspiegel/config.toml is never
# clobbered.
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
echo "Mirror UI: http://zeitspiegel.local"
