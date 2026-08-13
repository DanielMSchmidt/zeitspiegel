#!/usr/bin/env bash
# Write the baked appliance image to an SD card (macOS) and name the unit.
# Build the image first with scripts/build-image.sh; `make sd` does both.
#
#   make sd NAME="Long Side"                # auto-detect the card, confirm, write
#   make sd NAME=auto                       # ship the card deliberately unnamed
#   DISK=/dev/disk4 make sd NAME="Long Side" # skip auto-detection
#
# The confirmation prompt shows how big a card this image needs next to what
# the card actually has; a card that is too small is refused there, before
# anything is erased. To ask that on its own, without touching the card:
#
#   scripts/flash-sd.sh --size-check build/zeitspiegel-appliance.img /dev/disk4
#
# Auto-detection finds cards in a built-in reader as well as USB readers and
# sticks. `diskutil list` shows every disk if you need to pick one by hand.
#
# NAME is the label this mirror shows in the UI. It is written into the image's
# FAT32 boot partition just before the card is, so the card is named the moment
# it exists and never has to be mounted afterwards. The bake stays label-free:
# this is the one per-card file, not a per-card build (E-8).
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "error: $*" >&2; exit 1; }

# --- how big a card does this image need? ----------------------------------
# The confirmation prompt is the last moment before a card is erased, and the
# question actually being asked at it is "is this card big enough?". Answer it
# there, in the same units the disk is described in.
human_size() {
    # Powers of ten, because that is what card packaging and `diskutil` mean
    # by GB — comparing a 32 GB card against a GiB figure invites the wrong
    # answer at exactly the wrong moment.
    awk -v b="$1" 'BEGIN {
        split("B KB MB GB TB", u, " ")
        i = 1
        while (b >= 1000 && i < 5) { b /= 1000; i++ }
        printf (i <= 2 || b >= 100) ? "%.0f %s\n" : "%.1f %s\n", b, u[i]
    }'
}

file_bytes() { stat -f%z "$1" 2>/dev/null || stat -c%s "$1" 2>/dev/null; }

# The byte count out of `diskutil info`, which prints it in parentheses next
# to the human figure: "Disk Size: 31.9 GB (31914983424 Bytes) (exactly ...)".
disk_bytes() {
    diskutil info "$1" 2>/dev/null \
        | awk -F'[()]' '/Disk Size/ {print $2; exit}' \
        | awk '{print $1}'
}

# size_verdict <image> <disk-bytes> — the two lines the prompt shows, plus the
# exit status that decides whether erasing this card could work at all.
size_verdict() {
    local img="$1" have="$2" need
    [[ -f "$img" ]] || die "$img missing — run 'make image' first"
    need=$(file_bytes "$img")
    printf '  Image size:             %s — the card must be at least this big\n' "$(human_size "$need")"
    if [[ -z "$have" || "$have" == 0 ]]; then
        printf '  Card capacity:          unknown (diskutil did not report a size)\n'
        return 0
    fi
    if [[ "$have" -lt "$need" ]]; then
        printf '  Card capacity:          %s — TOO SMALL for this image by %s\n' \
            "$(human_size "$have")" "$(human_size $((need - have)))"
        return 1
    fi
    printf '  Card capacity:          %s — fits, %s to spare\n' \
        "$(human_size "$have")" "$(human_size $((have - need)))"
    return 0
}

# Standalone form: answer the question without touching anything.
#   scripts/flash-sd.sh --size-check build/zeitspiegel-appliance.img /dev/disk4
#   scripts/flash-sd.sh --size-check <image> <byte count>
if [[ "${1:-}" == "--size-check" ]]; then
    [[ $# -eq 3 ]] || die "usage: flash-sd.sh --size-check <image> <disk|bytes>"
    case "$3" in
        /dev/*) size_verdict "$2" "$(disk_bytes "$3")" ;;
        *)      size_verdict "$2" "$3" ;;
    esac
    exit $?
fi

[[ "$(uname)" == "Darwin" ]] || die "flash-sd.sh targets macOS; on Linux name the image first (L=\$(sudo losetup -Pf --show build/zeitspiegel-appliance.img); sudo mount \"\${L}p1\" /mnt; scripts/stage-name.sh \"\$NAME\" /mnt; sudo umount /mnt; sudo losetup -d \"\$L\"), then: sudo dd if=build/zeitspiegel-appliance.img of=/dev/sdX bs=4M conv=fsync"
# IMG picks which bake gets written — `make sd-dev` points it at the unsealed
# development image, which is a separate file so the two can never be confused
# at the card writer.
IMG="${IMG:-build/zeitspiegel-appliance.img}"
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
echo "  Image:                  $IMG"
FITS=0
size_verdict "$IMG" "$(disk_bytes "$DISK")" || FITS=1
if [[ "$NAME_CLEAN" == "auto" ]]; then
    echo "  Mirror name:            (unnamed — it will call itself Zeitspiegel <ID>)"
else
    echo "  Mirror name:            $NAME_CLEAN"
fi
# Refuse here rather than at the prompt: dd would erase the card and then run
# out of room partway, which costs whatever was on it and produces a card that
# boots nowhere.
[[ "$FITS" == 0 ]] || die "this card cannot hold $IMG — use a bigger one. Nothing has been erased."
echo
read -r -p "Type 'erase' to continue: " answer
[[ "$answer" == "erase" ]] || die "aborted"

# --- name the image, then write it -----------------------------------------
# The label goes into the image's own boot partition before a single byte
# reaches the card. Naming the card afterwards is what the earlier version did,
# and it is a coin flip: macOS auto-mounts the partitions the moment dd closes
# the device, and that mount regularly comes up read-only — DiskArbitration
# attaches to a filesystem whose bytes changed underneath it. An image file is
# subject to none of that, and a card that has just been written is never
# touched again.
#
# The bake stays label-free, so this is still the one per-card file rather than
# a per-card build (E-8); the image is re-labelled in place for the next card.
echo "==> naming the image"
IMG_DEV=$(hdiutil attach -imagekey diskimage-class=CRawDiskImage -nomount "$IMG" | awk 'NR==1 {print $1}') \
    || die "could not attach $IMG — 'hdiutil info' lists what is already attached"
[[ "$IMG_DEV" =~ ^/dev/disk[0-9]+$ ]] || die "attaching $IMG gave no disk device (got \"$IMG_DEV\")"
detach_img() { hdiutil detach "$IMG_DEV" >/dev/null 2>&1 || true; }
trap detach_img EXIT

# An already-mounted partition makes `diskutil mount` fail; the mount point is
# the thing that actually matters, so test for that rather than for the exit.
diskutil mount "${IMG_DEV}s1" >/dev/null 2>&1 || true
IMG_BOOTFS=$(diskutil info "${IMG_DEV}s1" 2>/dev/null | awk -F': +' '/Mount Point/ {print $2}')
[[ -n "$IMG_BOOTFS" && -d "$IMG_BOOTFS" ]] || die "could not mount the boot partition of $IMG"
# `auto` clears the label instead of writing one — the image is reused card
# after card, so a previous card's name must not leak into this one.
./scripts/stage-name.sh "$NAME" "$IMG_BOOTFS" >/dev/null
diskutil unmount "${IMG_DEV}s1" >/dev/null
detach_img
trap - EXIT

RDISK="${DISK/\/dev\/disk//dev/rdisk}"
echo "==> unmounting $DISK"
diskutil unmountDisk force "$DISK"
echo "==> writing $IMG to $RDISK (sudo will prompt; a few minutes)"
sudo dd if="$IMG" of="$RDISK" bs=4m
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
