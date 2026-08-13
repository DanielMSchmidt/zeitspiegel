#!/bin/bash
# Dumps boot evidence to the FAT32 boot partition so the SD card carries it
# off the device — readable on any host without attaching a screen. Sibling of
# zeitspiegel-debug.sh; same on-disk location pattern (/boot/firmware/*.log)
# for the same reason.
#
# Fired by zeitspiegel-boot-profile.timer (OnBootSec=30s). The timer isn't in
# multi-user.target's dependency graph, so there's no cycle blocking systemd
# from setting FinishTimestampMonotonic. The poll below is a safety net for
# the rare case where a slow unit hasn't completed by the 30 s mark.
#
# This started as boot *timing* only, and a unit that came back from a venue
# with a black screen proved that too narrow: it grepped for five success
# lines, so a failing unit produced an empty section and the card said nothing
# about why. What a dark unit needs is the opposite — the unit's own words,
# its failure count, and whether the hardware it needs even showed up. The
# journal cannot be relied on for this: a sealed card sends journald's writes
# to tmpfs, so this file is the only durable record there is.
#
# Output: /boot/firmware/zeitspiegel-boot-profile.log (overwritten each boot —
# the latest is what matters).
#
# OUT and PROFILE_WAIT are overridable so the capture can be run by hand, or
# off the unit entirely: every command's failure is captured in place of its
# output, so this produces the same shape of file on a machine with no systemd.
set -u
OUT="${OUT:-/boot/firmware/zeitspiegel-boot-profile.log}"
PROFILE_WAIT="${PROFILE_WAIT:-60}"

# Poll until the manager records a finished boot, capped so we always write
# *something* even if a unit hangs. `systemctl show` returns
# `FinishTimestampMonotonic=0` while bootup is still in progress and a real
# monotonic-microsecond value once it's done.
if [ "$PROFILE_WAIT" -gt 0 ]; then
    for _ in $(seq 1 "$PROFILE_WAIT"); do
        ts=$(systemctl show -p FinishTimestampMonotonic --value 2>/dev/null || echo 0)
        [ "${ts:-0}" != "0" ] && break
        sleep 1
    done
fi

# say <heading> <command...> — run a command under its own heading and keep
# whatever it produces, including its failure. A section that is simply absent
# on a broken unit is how the first black screen cost a round trip.
say() {
    local heading="$1"; shift
    echo
    echo "-- $heading --"
    "$@" 2>&1 || echo "(the command above failed: exit $?)"
}

{
    printf '==========================================\n'
    printf 'zeitspiegel boot profile   uptime %ss   wall-clock %s\n' \
        "$(awk '{print $1}' /proc/uptime 2>/dev/null)" \
        "$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null)"
    printf '==========================================\n'
    printf 'The unit has no battery-backed RTC and no upstream NTP, so the\n'
    printf 'wall-clock above is whatever systemd restored at boot. Uptime is\n'
    printf 'the number to trust.\n'

    # --- did it work, and if not, what did it say ---------------------------
    # First, because on a unit that is showing nothing this is the whole
    # question and everything below is context for it.
    say "systemctl status zeitspiegel (state, restarts, last exit)" \
        systemctl status zeitspiegel.service --no-pager --lines=0
    say "systemctl show zeitspiegel (restart count + last exit status)" \
        systemctl show zeitspiegel.service \
        -p NRestarts -p Result -p ExecMainStatus -p ExecMainCode -p ActiveState -p SubState

    echo
    echo "-- journalctl -u zeitspiegel (this boot, last 200 lines) --"
    # Everything the unit said, not just the milestones: a unit that fails
    # before its first success line used to leave this section empty, which
    # reads exactly like a unit that said nothing at all.
    if journalctl -u zeitspiegel.service -b --no-pager --output=short-monotonic 2>&1 \
        | tail -200; then :; else echo "(journalctl failed: exit $?)"; fi

    say "zeitspiegel app milestones (the timing line-up)" \
        sh -c 'journalctl -u zeitspiegel.service -b --no-pager --output=short-monotonic 2>&1 |
               grep -E "display opened|source opened|display loop starting|http listening|first frame presented" |
               head -20 || echo "(none of the startup milestones were logged this boot)"'

    # --- why the card may carry no journal ----------------------------------
    # A sealed root sends journald's writes to tmpfs; an unsealed one should
    # persist. When a card comes back with an empty /var/log/journal, this is
    # what says which of the two happened.
    say "journald storage (why the card does or does not carry a journal)" \
        sh -c 'systemctl show systemd-journald -p Result -p ActiveState 2>&1;
               echo "--- /var/log/journal ---"; ls -la /var/log/journal 2>&1;
               echo "--- disk usage ---"; journalctl --disk-usage 2>&1;
               echo "--- Storage= as configured ---";
               grep -rhs "^ *Storage" /etc/systemd/journald.conf /etc/systemd/journald.conf.d/ 2>&1 || echo "(unset — journald default: auto)";
               echo "--- machine id ---"; cat /etc/machine-id 2>&1;
               echo "--- root mount ---"; findmnt -no SOURCE,FSTYPE,OPTIONS / 2>&1'

    # --- the hardware the mirror needs before it can show anything ----------
    say "/dev/dri (KMSDRM needs a card node before SDL can open)" \
        ls -la /dev/dri
    say "drm connectors (is a screen plugged in, and does the kernel see it)" \
        sh -c 'for c in /sys/class/drm/card*/status; do
                   [ -e "$c" ] || continue
                   printf "%s: %s\n" "${c%/status}" "$(cat "$c" 2>/dev/null)"
               done; [ -e /sys/class/drm ] || echo "(no /sys/class/drm on this machine)"'
    say "/dev/video (did the camera enumerate)" \
        ls -la /dev/video0 /dev/video1 /dev/video2
    say "dmesg: vc4 / drm / hdmi / usb video" \
        sh -c 'dmesg 2>&1 | grep -iE "vc4|drm|hdmi|uvcvideo|cma" | tail -40'

    # --- boot timing, which is what this file was originally for ------------
    say "systemd-analyze (firmware/loader/kernel/userspace totals)" systemd-analyze
    say "systemd-analyze blame (per-unit, slowest first)" systemd-analyze blame --no-pager
    say "systemd-analyze critical-chain (path to multi-user.target)" \
        systemd-analyze critical-chain --no-pager
    say "systemd-analyze critical-chain zeitspiegel.service" \
        systemd-analyze critical-chain --no-pager zeitspiegel.service
    say "systemctl --failed (anything else that gave up)" \
        systemctl --failed --no-pager --no-legend
    say "systemctl list-unit-files --state=masked (sanity: what we disabled)" \
        systemctl list-unit-files --state=masked --no-legend --no-pager

    say "/proc/uptime (idle/total)" cat /proc/uptime
} > "$OUT" 2>&1

# FAT32 loses unflushed writes on a power cut, and surviving one is the entire
# point of writing here.
sync
