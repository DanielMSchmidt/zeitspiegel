#!/bin/bash
# Real provisioning, run as root by zeitspiegel-firstboot.service on the
# first boot that has network. Exits non-zero (and retries next boot) when
# the internet is not reachable yet; on success it seals the appliance and
# reboots into the finished mirror.
set -euo pipefail

CARD=/boot/firmware/zeitspiegel

# apt needs internet once: wait up to ~1 min for a default route.
for _ in $(seq 1 30); do
    ip route 2>/dev/null | grep -q '^default' && break
    sleep 2
done
if ! ip route 2>/dev/null | grep -q '^default'; then
    echo "zeitspiegel-firstboot: no network — plug in ethernet and power-cycle" >&2
    exit 1
fi

# SSH key for the admin user, if one was staged.
if [[ -f "$CARD/authorized_keys" ]]; then
    USER_HOME=$(getent passwd zeitspiegel | cut -d: -f6 || true)
    if [[ -n "$USER_HOME" ]]; then
        install -d -m 0700 -o zeitspiegel -g zeitspiegel "$USER_HOME/.ssh"
        install -m 0600 -o zeitspiegel -g zeitspiegel "$CARD/authorized_keys" "$USER_HOME/.ssh/authorized_keys"
    fi
fi

# Full appliance setup from the staged payload (binary, config, unit,
# access point) + seal with the read-only overlay (NFR-9).
export AP_SSID AP_PASS WIFI_COUNTRY
AP_SSID=$(cat "$CARD/ap-ssid" 2>/dev/null || echo zeitspiegel)
AP_PASS=$(cat "$CARD/ap-password.txt")
WIFI_COUNTRY=$(cat "$CARD/wifi-country" 2>/dev/null || echo DE)
bash "$CARD/setup.sh" --seal

mkdir -p /etc/zeitspiegel
touch /etc/zeitspiegel/.provisioned # written before the overlay takes effect (reboot)
systemctl disable zeitspiegel-firstboot.service

echo "zeitspiegel-firstboot: done — rebooting into the sealed appliance"
reboot
