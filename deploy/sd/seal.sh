#!/bin/bash
# One-time first-boot finisher, run by zeitspiegel-seal.service. Offline: no
# network needed. Places the admin SSH key (the user exists only after the
# first-boot user creation), then enables the read-only overlay (NFR-9) and
# reboots into the sealed appliance. Guarded so it runs exactly once.
set -e

[ -f /etc/zeitspiegel/.sealed ] && exit 0

KEY=/boot/firmware/zeitspiegel-authorized_keys
if [ -f "$KEY" ] && id zeitspiegel >/dev/null 2>&1; then
    install -d -m700 -o zeitspiegel -g zeitspiegel /home/zeitspiegel/.ssh
    install -m600 -o zeitspiegel -g zeitspiegel "$KEY" /home/zeitspiegel/.ssh/authorized_keys
fi

mkdir -p /etc/zeitspiegel
touch /etc/zeitspiegel/.sealed   # written before the overlay engages (persists)
systemctl disable zeitspiegel-seal.service || true

# Enable the read-only overlay (rebuilds initramfs on the Pi — the right
# place for it, not the offline build host). Takes effect after the reboot.
raspi-config nonint enable_overlayfs
reboot
