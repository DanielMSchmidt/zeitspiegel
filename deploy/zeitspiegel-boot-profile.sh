#!/bin/bash
# Dumps boot timing evidence to the FAT32 boot partition so the SD card
# carries the numbers off the device — readable on any host without
# attaching a screen. Sibling of zeitspiegel-debug.sh; same on-disk
# location pattern (/boot/firmware/*.log) for the same reason.
#
# Fired by zeitspiegel-boot-profile.timer (OnBootSec=30s). The timer
# isn't in multi-user.target's dependency graph, so there's no cycle
# blocking systemd from setting FinishTimestampMonotonic. The poll
# below is a safety net for the rare case where a slow unit hasn't
# completed by the 30 s mark.
#
# Output: /boot/firmware/zeitspiegel-boot-profile.log (overwritten
# each boot — the latest is what matters; persistent journal keeps
# history).
set -u
OUT=/boot/firmware/zeitspiegel-boot-profile.log

# Poll until the manager records a finished boot, capped so we always
# write *something* even if a unit hangs. `systemctl show` returns
# `FinishTimestampMonotonic=0` while bootup is still in progress and a
# real monotonic-microsecond value once it's done.
for _ in $(seq 1 60); do
    ts=$(systemctl show -p FinishTimestampMonotonic --value 2>/dev/null || echo 0)
    [ "${ts:-0}" != "0" ] && break
    sleep 1
done

{
    printf '==========================================\n'
    printf 'zeitspiegel boot profile   uptime %ss   wall-clock %s\n' \
        "$(awk '{print $1}' /proc/uptime)" \
        "$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null)"
    printf '==========================================\n'

    echo
    echo "-- systemd-analyze (firmware/loader/kernel/userspace totals) --"
    systemd-analyze 2>&1

    echo
    echo "-- systemd-analyze blame (per-unit, slowest first) --"
    systemd-analyze blame --no-pager 2>&1

    echo
    echo "-- systemd-analyze critical-chain (path to multi-user.target) --"
    systemd-analyze critical-chain --no-pager 2>&1

    echo
    echo "-- systemd-analyze critical-chain zeitspiegel.service --"
    systemd-analyze critical-chain --no-pager zeitspiegel.service 2>&1

    echo
    echo "-- systemctl list-unit-files --state=masked (sanity: what we disabled) --"
    systemctl list-unit-files --state=masked --no-legend --no-pager 2>&1

    echo
    echo "-- zeitspiegel app: first-frame + http listening (from journal) --"
    # Persistent journal is enabled at bake; these lines are how we read
    # end-to-end timing off the SD card. The binary logs slog with key
    # `since_start` (time within the binary) plus `uptime` (kernel-
    # monotonic seconds since power-on).
    journalctl -u zeitspiegel.service -b --no-pager \
        --output=short-monotonic 2>/dev/null \
        | grep -E "display opened|source opened|display loop starting|http listening|first frame presented" \
        | head -20

    echo
    echo "-- /proc/uptime (idle/total) --"
    cat /proc/uptime 2>&1
} > "$OUT" 2>&1

sync
