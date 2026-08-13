#!/usr/bin/env bash
# Write the baked appliance image to an SD card (macOS). Build it first with
# scripts/build-image.sh; `make sd` does both.
#
#   make sd                  # auto-detect the card, confirm, write
#   DISK=/dev/disk4 make sd  # skip auto-detection
#
# Auto-detection finds cards in a built-in reader as well as USB readers and
# sticks. `diskutil list` shows every disk if you need to pick one by hand.
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "error: $*" >&2; exit 1; }

[[ "$(uname)" == "Darwin" ]] || die "flash-sd.sh targets macOS; on Linux use: sudo dd if=build/zeitspiegel-appliance.img of=/dev/sdX bs=4M conv=fsync"
IMG=build/zeitspiegel-appliance.img
[[ -f "$IMG" ]] || die "$IMG missing — run 'make image' first"

# --- pick the target disk, with guard rails (bash 3.2 compatible) ----------
#
# A card in a Mac's built-in reader is NOT "external": macOS reports it as
# internal media that happens to be removable, so `diskutil list external`
# never shows it. Removability — not which bus the media hangs off — is what
# makes a disk safe to erase here, so both detection and the guard test that.
# Field names differ across macOS releases (older: "Internal:" / "Ejectable:",
# newer: "Device Location:" / "Removable Media:"), so accept every spelling;
# a disk that answers to none of them is treated as fixed and refused.
removable() {
    diskutil info "$1" | grep -qE \
        '^ *(Removable Media: *Removable|Ejectable: *Yes|Media Removable: *Yes)'
}
external() {
    diskutil info "$1" | grep -qE '^ *(Device Location: *External|Internal: *No)'
}
writable_target() { removable "$1" || external "$1"; }

# The disk backing / is never a target, whatever diskutil says about it.
BOOT_DISK=$(df / | awk 'NR==2 {sub(/s[0-9].*$/, "", $1); print $1}')

if [[ -z "${DISK:-}" ]]; then
    CANDIDATES=
    for d in $(diskutil list physical | awk '/^\/dev\/disk/ {print $1}'); do
        [[ "$d" == "$BOOT_DISK" ]] && continue
        writable_target "$d" && CANDIDATES="$CANDIDATES $d"
    done
    set -- $CANDIDATES
    [[ $# -gt 0 ]] || die "no removable disk found — insert the SD card (built-in readers count; if it is listed by 'diskutil list' but not picked up, set DISK=/dev/diskN)"
    [[ $# -eq 1 ]] || die "several removable disks ($*) — set DISK="
    DISK="$1"
fi
[[ "$DISK" =~ ^/dev/disk[0-9]+$ ]] || die "DISK must look like /dev/diskN (whole disk)"
diskutil info "$DISK" >/dev/null || die "no such disk: $DISK"
[[ "$DISK" != "$BOOT_DISK" ]] || die "refusing to write to the boot disk $DISK"
writable_target "$DISK" || die "refusing to write to $DISK — not removable or external media"

echo
echo "About to ERASE this disk and write the Zeitspiegel appliance image:"
diskutil info "$DISK" | grep -E "Device Identifier|Device / Media Name|Disk Size" | sed 's/^ */  /'
echo
read -r -p "Type 'erase' to continue: " answer
[[ "$answer" == "erase" ]] || die "aborted"

RDISK="${DISK/\/dev\/disk//dev/rdisk}"
echo "==> unmounting $DISK"
diskutil unmountDisk force "$DISK"
echo "==> writing $IMG to $RDISK (sudo will prompt; a few minutes)"
sudo dd if="$IMG" of="$RDISK" bs=4m
sync
diskutil eject "$DISK" >/dev/null

echo
echo "Card ready — plug-and-play:"
echo "  1. Put the card in the Pi, attach HDMI + camera + the 5 V/5 A PSU, power on."
echo "  2. First boot finishes itself offline (resize, create user, seal) and"
echo "     reboots a couple of times — allow ~3 minutes. No network needed, ever."
echo "  3. Join the Wi-Fi below and open the mirror UI."
echo
cat build/credentials.txt 2>/dev/null || true
