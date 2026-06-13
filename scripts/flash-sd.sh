#!/usr/bin/env bash
# Write the baked appliance image to an SD card (macOS). Build it first with
# scripts/build-image.sh; `make sd` does both.
#
#   make sd                  # auto-detect the card, confirm, write
#   DISK=/dev/disk4 make sd  # skip auto-detection
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "error: $*" >&2; exit 1; }

[[ "$(uname)" == "Darwin" ]] || die "flash-sd.sh targets macOS; on Linux use: sudo dd if=build/zeitspiegel-appliance.img of=/dev/sdX bs=4M conv=fsync"
IMG=build/zeitspiegel-appliance.img
[[ -f "$IMG" ]] || die "$IMG missing — run 'make image' first"

# --- pick the target disk, with guard rails (bash 3.2 compatible) ----------
if [[ -z "${DISK:-}" ]]; then
    EXTERNAL=$(diskutil list external physical | awk '/^\/dev\/disk/ {print $1}')
    set -- $EXTERNAL
    [[ $# -gt 0 ]] || die "no external disk found — insert the SD card"
    [[ $# -eq 1 ]] || die "several external disks ($*) — set DISK="
    DISK="$1"
fi
[[ "$DISK" =~ ^/dev/disk[0-9]+$ ]] || die "DISK must look like /dev/diskN (whole disk)"
diskutil info "$DISK" >/dev/null || die "no such disk: $DISK"
diskutil info "$DISK" | grep -q "Internal: *Yes" && die "refusing to write to internal disk $DISK"

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
