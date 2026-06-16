#!/bin/bash
# Captures rfkill / NetworkManager / kernel evidence at named stages of
# boot. Writes to /boot/firmware/zeitspiegel-debug.log so the result is
# on the FAT32 boot partition — readable by pulling the SD card into
# any OS, and immune to whatever the rootfs overlay is doing. The Pi 5
# has no battery-backed RTC, so timestamps are uptime-relative (the
# wall-clock is unreliable on a freshly-baked appliance).
#
# Three stages are wired by separate systemd units (deploy/sd/*.service):
#   pre-rfkill   — After=sys-subsystem-rfkill-devices.device,
#                  Before=systemd-rfkill.service.  Captures the KERNEL
#                  rfkill state independent of any userland gate.
#   post-rfkill  — After=systemd-rfkill.service,
#                  Before=NetworkManager.service. Captures what
#                  systemd-rfkill set + the saved state files +
#                  NetworkManager.state BEFORE NM has touched it.
#   late         — After=multi-user.target with a delay.  Captures the
#                  final NM/iw/nmcli state + journal excerpts.
set -u
STAGE="${1:-unknown}"
OUT=/boot/firmware/zeitspiegel-debug.log

[ "$STAGE" = "pre-rfkill" ] && : > "$OUT"   # truncate at start of each boot

{
    printf '\n==========================================\n'
    printf 'STAGE: %s   uptime %ss   wall-clock %s\n' \
        "$STAGE" \
        "$(awk '{print $1}' /proc/uptime)" \
        "$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null)"
    printf '==========================================\n'

    echo
    echo "-- /sys/class/rfkill/* (KERNEL view, can't lie about driver state) --"
    for f in /sys/class/rfkill/rfkill*; do
        [ -d "$f" ] || continue
        printf '%s:\n' "$f"
        for x in name type soft hard state persistent; do
            printf '    %-12s %s\n' "$x" "$(cat $f/$x 2>/dev/null)"
        done
    done

    echo
    echo "-- /var/lib/systemd/rfkill/ (what systemd-rfkill is persisting) --"
    ls -la /var/lib/systemd/rfkill/ 2>/dev/null
    for f in /var/lib/systemd/rfkill/*; do
        [ -f "$f" ] || continue
        printf '    %s: %s\n' "$(basename "$f")" "$(cat "$f" 2>/dev/null)"
    done

    echo
    echo "-- /var/lib/NetworkManager/NetworkManager.state (NM's own enable gate) --"
    cat /var/lib/NetworkManager/NetworkManager.state 2>/dev/null || echo "(not present yet)"

    if command -v iw >/dev/null 2>&1; then
        echo
        echo "-- iw reg get --"
        iw reg get 2>&1
    fi

    echo
    echo "-- ip link show wlan0 --"
    ip link show wlan0 2>&1

    if [ "$STAGE" = "late" ]; then
        echo
        echo "-- nmcli con --"
        nmcli -f NAME,DEVICE,STATE,TYPE,AUTOCONNECT con 2>&1

        echo
        echo "-- nmcli dev --"
        nmcli -f DEVICE,TYPE,STATE,CONNECTION dev 2>&1

        if command -v iw >/dev/null 2>&1; then
            echo
            echo "-- iw dev --"
            iw dev 2>&1
        fi

        echo
        echo "-- journalctl -u systemd-rfkill -b --no-pager --"
        journalctl -u systemd-rfkill -b --no-pager 2>&1

        echo
        echo "-- journalctl -u NetworkManager -b --no-pager (last 200) --"
        journalctl -u NetworkManager -b --no-pager 2>&1 | tail -200

        echo
        echo "-- dmesg | grep rfkill|brcm|cfg80211|wlan|nft --"
        dmesg 2>&1 | grep -iE 'rfkill|brcm|cfg80211|wlan|nft|firmware' | head -80
    fi
} >> "$OUT" 2>&1

# Be defensive about flushing to FAT32 — SD card filesystems can lose
# unflushed writes on power cuts and the whole point of this file is
# surviving a power cut.
sync
