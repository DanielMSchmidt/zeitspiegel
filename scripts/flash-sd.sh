#!/usr/bin/env bash
# Write the baked appliance image to an SD card (macOS) and name the unit.
# Build the image first with scripts/build-image.sh; `make sd` does both.
#
#   make sd NAME="Long Side"                # auto-detect the card, confirm, write
#   make sd NAME=auto                       # ship the card deliberately unnamed
#   DISK=/dev/disk4 make sd NAME="Long Side" # skip auto-detection
#
# Auto-detection finds cards in a built-in reader as well as USB readers and
# sticks. `diskutil list` shows every disk if you need to pick one by hand.
#
# NAME is the label this mirror shows in the UI. It is staged onto the FAT32
# bootfs partition after the write, so the image itself stays byte-identical
# across every card (E-8).
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "error: $*" >&2; exit 1; }

[[ "$(uname)" == "Darwin" ]] || die "flash-sd.sh targets macOS; on Linux use: sudo dd if=build/zeitspiegel-appliance.img of=/dev/sdX bs=4M conv=fsync, then scripts/stage-name.sh \"\$NAME\" /media/\$USER/bootfs"
IMG=build/zeitspiegel-appliance.img
[[ -f "$IMG" ]] || die "$IMG missing — run 'make image' first"

# The label is validated before anything is erased, so a typo costs a second
# rather than a rewritten card.
[[ -n "${NAME:-}" ]] || die "NAME is required — e.g. make sd NAME=\"Long Side\" (NAME=auto for a card that names itself)"
NAME_CLEAN=$(./scripts/stage-name.sh "$NAME")

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
if [[ "$NAME_CLEAN" == "auto" ]]; then
    echo "  Mirror name:            (unnamed — it will call itself Zeitspiegel <ID>)"
else
    echo "  Mirror name:            $NAME_CLEAN"
fi
echo
read -r -p "Type 'erase' to continue: " answer
[[ "$answer" == "erase" ]] || die "aborted"

RDISK="${DISK/\/dev\/disk//dev/rdisk}"
echo "==> unmounting $DISK"
diskutil unmountDisk force "$DISK"
echo "==> writing $IMG to $RDISK (sudo will prompt; a few minutes)"
sudo dd if="$IMG" of="$RDISK" bs=4m
sync

# --- name the card ---------------------------------------------------------
# The label is the only per-card difference, and it goes onto the FAT32 bootfs
# partition (partition 1) rather than into the image. macOS mounts it a moment
# after the write finishes, hence the short wait.
echo "==> naming the card"
diskutil mountDisk "$DISK" >/dev/null
BOOTFS=""
for _ in 1 2 3 4 5; do
    BOOTFS=$(diskutil info "${DISK}s1" 2>/dev/null | awk -F': +' '/Mount Point/ {print $2}')
    [[ -n "$BOOTFS" && -d "$BOOTFS" ]] && break
    sleep 1
done
[[ -n "$BOOTFS" && -d "$BOOTFS" ]] || die "bootfs partition of $DISK did not mount — name the card by hand: scripts/stage-name.sh \"$NAME\" /Volumes/bootfs"
./scripts/stage-name.sh "$NAME" "$BOOTFS" >/dev/null
sync
diskutil eject "$DISK" >/dev/null

echo
if [[ "$NAME_CLEAN" == "auto" ]]; then
    echo "Card ready (unnamed — the unit calls itself Zeitspiegel <ID>) — plug-and-play:"
else
    echo "Card ready — \"$NAME_CLEAN\" — plug-and-play:"
fi
echo "  1. Put the card in the Pi, attach HDMI + camera + the 5 V/5 A PSU, power on."
echo "  2. First boot finishes itself offline (resize, create user, seal) and"
echo "     reboots a couple of times — allow ~3 minutes. No network needed, ever."
echo "  3. Join the Wi-Fi below and open the mirror UI."
echo
cat build/credentials.txt 2>/dev/null || true
