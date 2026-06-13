#!/bin/bash
# Runs ONCE on the very first boot, before networking, via the
# systemd.run= hook that scripts/make-sd.sh appends to cmdline.txt
# (the same mechanism the Raspberry Pi Imager uses). It only does the
# offline basics and installs the real provisioning service, which runs
# on the next boot with network. Must never fail the boot: best effort.
set +e

CARD=/boot/firmware/zeitspiegel
CUSTOM=/usr/lib/raspberrypi-sys-mods/imager_custom

# hostname → zeitspiegel (mDNS name, NFR-10)
if [ -x "$CUSTOM" ]; then
    "$CUSTOM" set_hostname zeitspiegel
else
    echo zeitspiegel > /etc/hostname
    sed -i 's/127\.0\.1\.1.*/127.0.1.1\tzeitspiegel/' /etc/hosts
fi

# Wi-Fi regulatory domain — required before the radio may run an AP.
COUNTRY=$(cat "$CARD/wifi-country" 2>/dev/null || echo DE)
if [ -x "$CUSTOM" ]; then
    "$CUSTOM" set_wlan_country "$COUNTRY"
else
    raspi-config nonint do_wifi_country "$COUNTRY"
fi

# The real provisioning (apt, binary, AP, seal) needs network — install it
# as a service that runs on the following boots until it succeeds.
install -m 0644 "$CARD/zeitspiegel-firstboot.service" /etc/systemd/system/
systemctl enable zeitspiegel-firstboot.service

# Remove this hook so it never runs again.
sed -i 's| systemd\.run[^ ]*||g; s| systemd\.unit=kernel-command-line\.target||g' /boot/firmware/cmdline.txt
rm -f /boot/firmware/firstrun.sh
exit 0
