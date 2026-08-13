#!/usr/bin/env bash
# Pull everything a Zeitspiegel card knows about its own last boots into one
# zip you can attach to a bug report. Call it with the card in any reader and
# it does the rest: finds the volume, reads both partitions, writes a single
# zeitspiegel-logs-<mirror>-<timestamp>.zip and nothing else.
#
#   scripts/collect-sd-logs.sh                      # find the card, collect
#   scripts/collect-sd-logs.sh --list               # just say which disk it found
#   DISK=/dev/disk4 scripts/collect-sd-logs.sh      # skip auto-detection
#   scripts/collect-sd-logs.sh --image build/zeitspiegel-appliance.img
#   scripts/collect-sd-logs.sh --bootfs /Volumes/bootfs [--rootfs /mnt/root]
#   scripts/collect-sd-logs.sh --boot-only          # skip the ext4 half
#
# A card has two halves and they fail differently:
#
#   bootfs (FAT32, partition 1) — zeitspiegel-debug.log (the three-stage
#     rfkill/NetworkManager capture) and zeitspiegel-boot-profile.log, written
#     there precisely so they survive a sealed overlay root and can be read by
#     pulling the card into any OS. Plus cmdline.txt/config.txt: the
#     regulatory domain, the overlay flag, what the kernel was told.
#   rootfs (ext4, partition 2) — the persistent journal at /var/log/journal
#     (NFR-8: a no-AP, no-screen appliance is post-mortem-debuggable after a
#     power cycle) plus the config the unit actually ran and NetworkManager's
#     own state files.
#
# macOS cannot mount ext4, so the second half is read with debugfs from
# e2fsprogs (`brew install e2fsprogs`) straight off the raw partition —
# read-only, nothing is mounted, nothing is written to the card. Without the
# reader this refuses to run at all: a bundle missing the journal is a bundle
# missing the failure, and finding that out later costs a round trip with the
# card already back in the unit. --boot-only takes the FAT32 half deliberately
# and says so in the report.
#
# A sealed card can only ever hand back its first boot — the overlay root
# sends every later write, journald included, to tmpfs. `make sd-dev` bakes an
# unsealed bench card whose journal survives reboots.
#
# The bundle is meant to travel, so the AP pre-shared key and the admin
# password hash are redacted out of it (--keep-secrets opts out).
set -euo pipefail

OUT_BASE=""
BOOTFS=""
ROOTFS=""
IMAGE=""
LIST_ONLY=0
REDACT=1
RENDER_DOCKER=0
BOOT_ONLY=0
ROOTPART_ARG=""
MOUNTED=()      # things this script mounted and must unmount
ATTACHED=()     # disk images this script attached
TMPDIRS=()

die() { echo "collect-sd-logs: $*" >&2; exit 1; }
note() { echo "==> $*"; }

usage() {
    sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'
    exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --out)      OUT_BASE="${2:-}"; shift 2 ;;
        --bootfs)   BOOTFS="${2:-}"; shift 2 ;;
        --rootfs)   ROOTFS="${2:-}"; shift 2 ;;
        --rootpart) ROOTPART_ARG="${2:-}"; shift 2 ;;
        --boot-only) BOOT_ONLY=1; shift ;;
        --image)    IMAGE="${2:-}"; shift 2 ;;
        --disk)     DISK="${2:-}"; shift 2 ;;
        --list)     LIST_ONLY=1; shift ;;
        --keep-secrets) REDACT=0; shift ;;
        --render-journal) RENDER_DOCKER=1; shift ;;
        -h|--help)  usage 0 ;;
        *)          echo "collect-sd-logs: unknown argument: $1" >&2; usage 1 ;;
    esac
done

# --- cleanup ---------------------------------------------------------------
# Whatever happens, the card is left the way it was found: unmounted from the
# temporary mount points, images detached, scratch directories gone. A support
# script that leaves a half-mounted card behind creates the next support case.
cleanup() {
    local m
    for m in "${MOUNTED[@]:-}"; do
        [[ -n "$m" ]] || continue
        if [[ "$(uname)" == "Darwin" ]]; then
            diskutil unmount "$m" >/dev/null 2>&1 || true
        else
            umount "$m" >/dev/null 2>&1 || sudo umount "$m" >/dev/null 2>&1 || true
        fi
    done
    for m in "${ATTACHED[@]:-}"; do
        [[ -n "$m" ]] || continue
        hdiutil detach "$m" >/dev/null 2>&1 || true
        losetup -d "$m" >/dev/null 2>&1 || sudo losetup -d "$m" >/dev/null 2>&1 || true
    done
    for m in "${TMPDIRS[@]:-}"; do
        [[ -n "$m" && "$m" == *zeitspiegel-collect.* ]] && rm -rf "$m" 2>/dev/null
    done
    return 0
}
trap cleanup EXIT

mktempdir() {
    local d
    d=$(mktemp -d "${TMPDIR:-/tmp}/zeitspiegel-collect.XXXXXX")
    TMPDIRS+=("$d")
    printf '%s\n' "$d"
}

# --- is this actually one of ours? ----------------------------------------
# The whole point of auto-detection is that it runs against whatever is in the
# reader, so the answer has to be checkable rather than assumed. Any of the
# per-card files the bake or `make sd` writes is proof; a stock Pi OS card is
# recognised by its cmdline carrying the regulatory domain we bake in.
is_zeitspiegel_bootfs() {
    local d="$1"
    [[ -d "$d" ]] || return 1
    local f
    for f in zeitspiegel-name.txt zeitspiegel-debug.log zeitspiegel-boot-profile.log \
             zeitspiegel-version.txt zeitspiegel-authorized_keys zeitspiegel-seal-done; do
        [[ -e "$d/$f" ]] && return 0
    done
    grep -q 'ieee80211_regdom' "$d/cmdline.txt" 2>/dev/null && return 0
    grep -q 'disable-bt' "$d/config.txt" 2>/dev/null && return 0
    return 1
}

# --- finding the card ------------------------------------------------------
# Cards live in USB readers and in built-in readers, and macOS reports the two
# differently: a card in a Mac's own slot is *internal* media that happens to
# be removable, so `diskutil list external` never shows it. Removability is
# what matters here — and unlike flash-sd.sh this script only ever reads, so
# the guard rails exist to avoid collecting a stranger's photos, not to avoid
# erasing them.
mac_removable() {
    diskutil info "$1" 2>/dev/null | grep -qE \
        '^ *(Removable Media: *Removable|Ejectable: *Yes|Media Removable: *Yes|Device Location: *External|Internal: *No)'
}

mac_mount_point() {
    diskutil info "$1" 2>/dev/null | awk -F': +' '/Mount Point/ {print $2; exit}'
}

# Mount a partition read-only if it is not mounted already, and echo where.
mac_mount_ro() {
    local part="$1" mp
    mp=$(mac_mount_point "$part")
    if [[ -z "$mp" || ! -d "$mp" ]]; then
        diskutil mount readOnly "$part" >/dev/null 2>&1 || return 1
        mp=$(mac_mount_point "$part")
        [[ -n "$mp" && -d "$mp" ]] || return 1
        MOUNTED+=("$mp")
    fi
    printf '%s\n' "$mp"
}

linux_mount_ro() {
    local part="$1" mp
    mp=$(lsblk -nro MOUNTPOINT "$part" 2>/dev/null | head -1)
    if [[ -n "$mp" && -d "$mp" ]]; then
        printf '%s\n' "$mp"; return 0
    fi
    mp=$(mktempdir)
    # noload: never replay an ext4 journal on someone's evidence. A card pulled
    # mid-write has a dirty journal, and replaying it is a write to the very
    # filesystem being collected.
    if mount -o ro,noload "$part" "$mp" 2>/dev/null || sudo mount -o ro,noload "$part" "$mp" 2>/dev/null \
       || mount -o ro "$part" "$mp" 2>/dev/null || sudo mount -o ro "$part" "$mp" 2>/dev/null; then
        MOUNTED+=("$mp")
        printf '%s\n' "$mp"; return 0
    fi
    return 1
}

# Locate the card and set BOOTFS / ROOTFS / ROOTPART. ROOTFS may stay empty
# (macOS without an ext4 reader); ROOTPART is then the raw device debugfs
# reads instead.
ROOTPART=""
SOURCE_DESC=""

detect_macos() {
    local disks=() d parts mp
    if [[ -n "${DISK:-}" ]]; then
        disks=("$DISK")
    else
        # A card that is already mounted is the cheapest hit: /Volumes/bootfs
        # is where macOS puts it the moment it is inserted.
        for mp in /Volumes/*; do
            if is_zeitspiegel_bootfs "$mp"; then
                BOOTFS="$mp"
                d=$(diskutil info "$mp" 2>/dev/null | awk -F': +' '/Device Identifier/ {print $2; exit}')
                [[ -n "$d" ]] && DISK="/dev/${d%s*}"
                break
            fi
        done
        if [[ -z "$BOOTFS" ]]; then
            for d in $(diskutil list physical 2>/dev/null | awk '/^\/dev\/disk/ {print $1}'); do
                mac_removable "$d" && disks+=("$d")
            done
        fi
    fi

    if [[ -z "$BOOTFS" ]]; then
        for d in "${disks[@]:-}"; do
            [[ -n "$d" ]] || continue
            mp=$(mac_mount_ro "${d}s1" 2>/dev/null) || continue
            if is_zeitspiegel_bootfs "$mp"; then
                BOOTFS="$mp"; DISK="$d"; break
            fi
        done
    fi
    [[ -n "$BOOTFS" ]] || die "no Zeitspiegel card found — insert it, or point at it with --disk /dev/diskN (\`diskutil list\` shows every disk). A card that is in the reader but not recognised here is not one of ours: the boot partition carries no zeitspiegel-* file."
    [[ -n "${DISK:-}" ]] && ROOTPART="${DISK/\/dev\/disk//dev/rdisk}s2"
    SOURCE_DESC="${DISK:-unknown disk} (macOS reader)"
}

detect_linux() {
    local disks=() d p mp
    if [[ -n "${DISK:-}" ]]; then
        disks=("$DISK")
    else
        while read -r name rm type; do
            [[ "$type" == "disk" ]] || continue
            [[ "$rm" == "1" ]] || continue
            disks+=("/dev/$name")
        done < <(lsblk -dnro NAME,RM,TYPE 2>/dev/null)
    fi
    for d in "${disks[@]:-}"; do
        [[ -n "$d" ]] || continue
        # mmcblk0p1 vs sdb1 — the kernel names partitions both ways.
        p="${d}1"; [[ -b "$p" ]] || p="${d}p1"
        [[ -b "$p" ]] || continue
        mp=$(linux_mount_ro "$p") || continue
        if is_zeitspiegel_bootfs "$mp"; then
            BOOTFS="$mp"; DISK="$d"
            p="${d}2"; [[ -b "$p" ]] || p="${d}p2"
            [[ -b "$p" ]] && ROOTPART="$p"
            break
        fi
    done
    [[ -n "$BOOTFS" ]] || die "no Zeitspiegel card found — insert it, or point at it with --disk /dev/sdX (\`lsblk\` shows every disk)."
    SOURCE_DESC="${DISK:-unknown disk} (Linux)"
    [[ -n "$ROOTPART" ]] && ROOTFS=$(linux_mount_ro "$ROOTPART" || true)
}

attach_image() {
    [[ -f "$IMAGE" ]] || die "no such image: $IMAGE"
    SOURCE_DESC="$IMAGE (image file)"
    if [[ "$(uname)" == "Darwin" ]]; then
        local dev
        dev=$(hdiutil attach -imagekey diskimage-class=CRawDiskImage -nomount "$IMAGE" | awk 'NR==1 {print $1}')
        [[ "$dev" =~ ^/dev/disk[0-9]+$ ]] || die "attaching $IMAGE gave no disk device (got \"$dev\")"
        ATTACHED+=("$dev")
        BOOTFS=$(mac_mount_ro "${dev}s1") || die "could not mount the boot partition of $IMAGE"
        ROOTPART="${dev/\/dev\/disk//dev/rdisk}s2"
    else
        local loop
        loop=$(losetup -Pf --show "$IMAGE" 2>/dev/null || sudo losetup -Pf --show "$IMAGE") \
            || die "could not attach $IMAGE"
        ATTACHED+=("$loop")
        BOOTFS=$(linux_mount_ro "${loop}p1") || die "could not mount the boot partition of $IMAGE"
        ROOTPART="${loop}p2"
        ROOTFS=$(linux_mount_ro "$ROOTPART" || true)
    fi
}

# --- reading the ext4 half without mounting it -----------------------------
# debugfs walks the filesystem read-only from userland, which is the only way
# to read the journal on macOS and a perfectly good way on a Linux box where
# mounting would need root. Missing paths are not errors: a card that never
# sealed has no NetworkManager state, and that absence is itself evidence.
find_debugfs() {
    local c
    # DEBUGFS= points at the binary when it lives somewhere unusual; setting
    # it to a path that is not there is also how the tests run as if the
    # reader were missing.
    if [[ -n "${DEBUGFS:-}" ]]; then
        [[ -x "$DEBUGFS" ]] && { printf '%s\n' "$DEBUGFS"; return 0; }
        return 1
    fi
    for c in debugfs /opt/homebrew/opt/e2fsprogs/sbin/debugfs /usr/local/opt/e2fsprogs/sbin/debugfs \
             /opt/homebrew/sbin/debugfs /sbin/debugfs /usr/sbin/debugfs; do
        command -v "$c" >/dev/null 2>&1 && { printf '%s\n' "$c"; return 0; }
        [[ -x "$c" ]] && { printf '%s\n' "$c"; return 0; }
    done
    return 1
}

# ext4_extract <device> <dest> — pull the interesting subtrees into dest so the
# rest of the script sees a plain directory, exactly like a mounted rootfs.
ext4_extract() {
    local dev="$1" dest="$2" dbg path parent
    dbg=$(find_debugfs) || return 1
    # Raw device reads need root. An unquoted empty variable expands to no
    # argument at all, which is what an empty array would do too — except that
    # empty arrays are an unbound-variable error under `set -u` in the bash 3.2
    # macOS still ships, and this script has to run there.
    local SUDO=""
    [[ -r "$dev" ]] || SUDO=sudo
    for path in "${ROOT_PULL[@]}"; do
        parent="$dest$(dirname "$path")"
        mkdir -p "$parent"
        # rdump copies a directory into a destination directory; dump copies a
        # single file. Try the directory form first and fall back, because the
        # card decides which of the two a path is.
        $SUDO "$dbg" -R "rdump \"$path\" \"$parent\"" "$dev" >/dev/null 2>&1 \
            || $SUDO "$dbg" -R "dump \"$path\" \"$dest$path\"" "$dev" >/dev/null 2>&1 \
            || true
    done
    # rdump reproduces the card's ownership and modes: the Wi-Fi profiles come
    # out 0600 root, the journal directories root:systemd-journal. Extracted
    # under sudo, that leaves a tree this script — running as you — can neither
    # redact nor zip. Take ownership of the copy; the card is untouched.
    if [[ -n "$SUDO" ]]; then
        $SUDO chown -R "$(id -u):$(id -g)" "$dest" 2>/dev/null || true
    fi
    chmod -R u+rwX "$dest" 2>/dev/null || true
    [[ -n "$(ls -A "$dest" 2>/dev/null)" ]]
}

# What is worth having off the root filesystem. Everything else on a sealed
# appliance is stock Pi OS.
ROOT_PULL=(
    /var/log
    /etc/zeitspiegel
    /etc/NetworkManager/system-connections
    /var/lib/NetworkManager
    /var/lib/systemd/rfkill
    /etc/os-release
    /etc/hostname
    /etc/machine-id
    /etc/fstab
)

# --- the report ------------------------------------------------------------
MAX_LINES=20000   # a runaway log must not turn the bundle into a download

section() { printf '\n\n===== %s =====\n' "$1" >> "$REPORT"; }

# emit <title> <file> [origin] — one text file into the report, or the fact
# that it is not there. "not present" is a finding: no zeitspiegel-debug.log
# means the debug units were never enabled on this card. The file read is the
# bundle's redacted copy; the path printed is where it lived on the card,
# because that is the path the reader will go looking for.
emit() {
    local title="$1" f="$2" origin="${3:-$2}"
    section "$title"
    if [[ ! -e "$f" ]]; then
        printf '(not present on this card)\n' >> "$REPORT"
        return
    fi
    printf '# %s   (%s bytes, mtime %s)\n\n' "$origin" \
        "$(wc -c < "$f" | tr -d ' ')" "$(file_mtime "$f")" >> "$REPORT"
    head -n "$MAX_LINES" "$f" >> "$REPORT" 2>/dev/null || true
    if [[ "$(wc -l < "$f" | tr -d ' ')" -gt "$MAX_LINES" ]]; then
        printf '\n[truncated at %s lines — the full file is in the bundle]\n' "$MAX_LINES" >> "$REPORT"
    fi
}

file_mtime() {
    if [[ "$(uname)" == "Darwin" ]]; then
        stat -f '%Sm' -t '%Y-%m-%dT%H:%M:%SZ' "$1" 2>/dev/null
    else
        date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null
    fi
}

# Files dropped because this run could not read them — reported, never
# silently missing.
UNREADABLE=""

# redact <file> — the bundle travels; the AP key and the admin hash do not.
# The keys stay, so a reader can still see that a profile had a psk at all.
#
# A file that cannot be read cannot be redacted either, and carrying it
# unread is how the pre-shared key this function exists to strip would ship
# anyway. Drop it and say so.
redact() {
    local f="$1"
    [[ "$REDACT" == 1 && -f "$f" ]] || return 0
    if [[ ! -r "$f" ]]; then
        unreadable "$f"
        rm -f "$f" 2>/dev/null || true
        return 0
    fi
    local tmp="$f.redacting"
    if LC_ALL=C sed -E \
        -e 's/^([[:space:]]*(psk|password|wep-key[0-9]*|passwd|leap-password|private-key-password)[[:space:]]*=).*/\1<REDACTED>/I' \
        -e 's/(\$[0-9y][a-z]*\$[^:[:space:]]+)/<REDACTED-PASSWORD-HASH>/g' \
        "$f" > "$tmp" 2>/dev/null
    then
        # Keep the file's timestamp: on a unit with no battery-backed clock,
        # mtimes are among the few things that order events, and the step that
        # makes the bundle safe to send must not destroy the evidence in it.
        touch -r "$f" "$tmp" 2>/dev/null || true
        mv "$tmp" "$f"
    else
        rm -f "$tmp" 2>/dev/null || true
        unreadable "$f"
        rm -f "$f" 2>/dev/null || true
    fi
}

# unreadable <path> — record a file the card had and this bundle does not.
unreadable() {
    local p="$1"
    p="${p#"$ROOTFS"}"; p="${p#"$BOOTFS"}"; p="${p#"$BUNDLE/"}"
    UNREADABLE="$UNREADABLE
  $p"
}

# copy_into <src> <dest-dir> — verbatim copy of a subtree into the bundle, so
# the binary journals can be rendered later with journalctl -D.
#
# cp gives up on individual files it cannot read (0600 root, when this is not
# root) while still returning a tree that looks complete. Compare afterwards
# rather than trusting the exit status: a file the card has and the bundle
# does not must be named, not quietly absent.
copy_into() {
    local src="$1" dest="$2" f rel
    [[ -e "$src" ]] || return 0
    mkdir -p "$(dirname "$dest")"
    # -p: keep timestamps. A unit with no RTC leaves mtimes as the only
    # ordering evidence there is, and a copy stamped "now" erases it. Setting
    # ownership will fail when this is not root; the comparison below is what
    # catches anything that actually failed to copy.
    cp -Rp "$src" "$dest" 2>/dev/null || true
    if [[ -d "$src" ]]; then
        while IFS= read -r f; do
            rel="${f#"$src"}"
            [[ -e "$dest$rel" ]] || unreadable "$f"
        done < <(find "$src" -type f 2>/dev/null)
    else
        [[ -e "$dest" ]] || unreadable "$src"
    fi
}

# --- go --------------------------------------------------------------------
if [[ -n "$IMAGE" ]]; then
    attach_image
elif [[ -z "$BOOTFS" ]]; then
    case "$(uname)" in
        Darwin) detect_macos ;;
        Linux)  detect_linux ;;
        *)      die "unsupported platform $(uname) — mount the card yourself and pass --bootfs/--rootfs" ;;
    esac
else
    SOURCE_DESC="$BOOTFS${ROOTFS:+ + $ROOTFS} (given on the command line)"
fi

is_zeitspiegel_bootfs "$BOOTFS" \
    || die "$BOOTFS is not a Zeitspiegel boot partition — no zeitspiegel-* file and no baked cmdline. Refusing to collect a volume that belongs to something else."

# An explicit root partition overrides what detection found — useful when the
# card enumerates oddly, and the seam the tests drive.
[[ -n "$ROOTPART_ARG" ]] && ROOTPART="$ROOTPART_ARG"
[[ "$BOOT_ONLY" == 1 ]] && ROOTPART=""

# The unit's own logs live on the ext4 root, and a bundle without them is
# usually a bundle without the failure. Refuse before any work rather than
# hand over a half-collected card that reads as a quiet one.
if [[ -z "$ROOTFS" && -n "$ROOTPART" ]] && ! find_debugfs >/dev/null; then
    die "the root filesystem ($ROOTPART) needs an ext4 reader this machine does not have,
  and without it the bundle carries no journal — the unit's own logs, which is
  where a black screen or a missing AP is explained. Install it:

      brew install e2fsprogs        # macOS
      sudo apt install e2fsprogs    # Debian/Ubuntu

  then run this again. To collect the boot partition alone anyway — the debug
  and boot-profile captures, which survive a sealed overlay — pass --boot-only.
  DEBUGFS=/path/to/debugfs points at the reader if it is installed somewhere
  unusual."
fi

if [[ "$LIST_ONLY" == 1 ]]; then
    echo "card:   ${SOURCE_DESC:-${DISK:-$BOOTFS}}"
    echo "bootfs: $BOOTFS"
    echo "rootfs: ${ROOTFS:-${ROOTPART:-(none found)}}"
    [[ -f "$BOOTFS/zeitspiegel-name.txt" ]] && echo "name:   $(head -1 "$BOOTFS/zeitspiegel-name.txt")"
    [[ -f "$BOOTFS/zeitspiegel-version.txt" ]] \
        && echo "build:  $(grep '^version=' "$BOOTFS/zeitspiegel-version.txt" | cut -d= -f2-)"
    exit 0
fi

# Name the bundle after the mirror, so three cards from one evening do not all
# land on top of each other.
NAME="unnamed"
[[ -f "$BOOTFS/zeitspiegel-name.txt" ]] && NAME=$(head -1 "$BOOTFS/zeitspiegel-name.txt" | tr -d '\r')

# Which build this card carries. The bake stamps it onto the boot partition
# precisely so the units that cannot boot can still be identified.
BUILD="unknown (this card predates version stamping, or was not built by the bake)"
if [[ -f "$BOOTFS/zeitspiegel-version.txt" ]]; then
    BUILD=$(tr -d '\r' < "$BOOTFS/zeitspiegel-version.txt" | sed '/^[[:space:]]*$/d' | paste -sd'   ' -)
fi
SLUG=$(printf '%s' "${NAME:-unnamed}" | LC_ALL=C tr '[:upper:]' '[:lower:]' | LC_ALL=C tr -c 'a-z0-9' '-' | sed -E 's/-+/-/g; s/^-|-$//g')
[[ -n "$SLUG" ]] || SLUG=unnamed
STAMP=$(date -u +%Y%m%dT%H%M%SZ)

if [[ -z "$OUT_BASE" ]]; then
    OUT_BASE="$(cd "$(dirname "$0")/.." && pwd)/build"
fi
mkdir -p "$OUT_BASE"
# One call, one file: everything is assembled in scratch space and only the
# zip is ever written next to the operator. A directory left beside it is one
# more thing to explain when the bug report is filed.
STAGING=$(mktempdir)
BUNDLE="$STAGING/zeitspiegel-logs-$SLUG-$STAMP"
mkdir -p "$BUNDLE"
REPORT="$BUNDLE/report.txt"

# The ext4 half, if it is not already a directory: extract it beside the report
# so both mounted and debugfs cards look the same from here on.
ROOTFS_NOTE=""
if [[ "$BOOT_ONLY" == 1 ]]; then
    ROOTFS_NOTE="not collected — --boot-only was passed, so the ext4 root was skipped
  deliberately. The unit's own logs (the journal) are therefore absent by
  choice, not because the unit was quiet."
fi
if [[ -z "$ROOTFS" && -n "$ROOTPART" ]]; then
    note "reading the root filesystem from $ROOTPART"
    EXTRACTED="$BUNDLE/rootfs"
    mkdir -p "$EXTRACTED"
    if ext4_extract "$ROOTPART" "$EXTRACTED"; then
        ROOTFS="$EXTRACTED"
        ROOTFS_NOTE="extracted read-only from $ROOTPART with debugfs"
    else
        rmdir "$EXTRACTED" 2>/dev/null || true
        ROOTFS_NOTE="NOT COLLECTED — the root filesystem ($ROOTPART) could not be read on this machine.
  macOS cannot mount ext4; install the reader and run this again:
      brew install e2fsprogs
  Everything below comes from the FAT32 boot partition only, which means the
  persistent journal (/var/log/journal — the unit's own logs) is missing."
    fi
fi

{
    printf 'Zeitspiegel field log bundle\n'
    printf '============================\n\n'
    printf 'mirror name:   %s\n' "$NAME"
    printf 'build:         %s\n' "$BUILD"
    printf 'collected:     %s (UTC)\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'collected on:  %s %s\n' "$(uname -s)" "$(uname -r)"
    printf 'source:        %s\n' "$SOURCE_DESC"
    printf 'bootfs:        %s\n' "$BOOTFS"
    printf 'root filesystem: %s\n' "${ROOTFS:-none}"
    [[ -n "$ROOTFS_NOTE" ]] && printf '  %s\n' "$ROOTFS_NOTE"
    printf 'secrets:       %s\n' "$([[ "$REDACT" == 1 ]] && echo 'redacted (psk, password hashes)' || echo 'NOT redacted (--keep-secrets)')"
    printf '\nThe unit has no battery-backed clock, so on-card timestamps before the\n'
    printf 'first network sync are relative to boot, not wall-clock.\n'
} > "$REPORT"

# --- boot partition --------------------------------------------------------
note "collecting the boot partition"
mkdir -p "$BUNDLE/bootfs"
for f in zeitspiegel-debug.log zeitspiegel-boot-profile.log zeitspiegel-journal.log.gz \
         zeitspiegel-name.txt zeitspiegel-version.txt cmdline.txt config.txt ssh \
         userconf.txt zeitspiegel-authorized_keys; do
    copy_into "$BOOTFS/$f" "$BUNDLE/bootfs/$f"
    redact "$BUNDLE/bootfs/$f"
done

# The boot partition is mostly stock Pi firmware — forty .dtb files nobody
# will read. List what is ours, and count the rest so a missing kernel is
# still visible.
section "bootfs listing (zeitspiegel + boot config; stock firmware counted only)"
ls -la "$BOOTFS" 2>/dev/null | grep -E 'zeitspiegel|cmdline\.txt|config\.txt|userconf|(^| )ssh$|issue\.txt' >> "$REPORT" || true
printf '\n%s files on the boot partition in total.\n' \
    "$(ls -1 "$BOOTFS" 2>/dev/null | wc -l | tr -d ' ')" >> "$REPORT"

emit "bootfs: zeitspiegel-debug.log (rfkill / NetworkManager, three boot stages)" "$BUNDLE/bootfs/zeitspiegel-debug.log" "/boot/firmware/zeitspiegel-debug.log"
emit "bootfs: zeitspiegel-boot-profile.log (boot timing, first frame, HTTP listen)" "$BUNDLE/bootfs/zeitspiegel-boot-profile.log" "/boot/firmware/zeitspiegel-boot-profile.log"
# The snapshot the on-unit capture leaves on the FAT partition. On a first
# boot or a sealed card this is the only journal there is, so it is read out in
# full rather than merely carried.
section "bootfs: journal snapshot for the last boot (every unit, not just ours)"
if [[ -f "$BUNDLE/bootfs/zeitspiegel-journal.log.gz" ]]; then
    printf '# /boot/firmware/zeitspiegel-journal.log.gz\n\n' >> "$REPORT"
    gunzip -c "$BUNDLE/bootfs/zeitspiegel-journal.log.gz" 2>/dev/null \
        | head -n "$MAX_LINES" >> "$REPORT" \
        || printf '(could not decompress it; the file itself is in the bundle)\n' >> "$REPORT"
else
    printf '(not present — this card predates the snapshot, or never booted far\n' >> "$REPORT"
    printf 'enough for the 30 s capture to run)\n' >> "$REPORT"
fi

emit "bootfs: cmdline.txt (regdomain, overlay root, quiet boot)" "$BUNDLE/bootfs/cmdline.txt" "/boot/firmware/cmdline.txt"
emit "bootfs: config.txt" "$BUNDLE/bootfs/config.txt" "/boot/firmware/config.txt"
emit "bootfs: zeitspiegel-version.txt (which build this card carries)" "$BUNDLE/bootfs/zeitspiegel-version.txt" "/boot/firmware/zeitspiegel-version.txt"
emit "bootfs: zeitspiegel-name.txt (the label this mirror shows)" "$BUNDLE/bootfs/zeitspiegel-name.txt" "/boot/firmware/zeitspiegel-name.txt"

# raspi-config's enable_overlayfs spells the seal `overlayroot=tmpfs` on
# current Pi OS and `boot=overlay` on older releases. Reading only the old
# spelling reports a sealed appliance as writable, which also means reporting
# a journal that stops at the seal as if it ran to the last boot.
SEALED=0
grep -qE 'boot=overlay|overlayroot=' "$BOOTFS/cmdline.txt" 2>/dev/null && SEALED=1

section "bootfs: seal / access markers"
{
    printf 'overlay root (cmdline.txt):                 %s\n' \
        "$([[ "$SEALED" == 1 ]] && echo 'yes — root is sealed read-only' || echo 'no — root is writable')"
    printf 'ssh marker file present:                    %s\n' "$([[ -e "$BOOTFS/ssh" ]] && echo yes || echo no)"
    printf 'staged authorized_keys present:             %s\n' "$([[ -e "$BOOTFS/zeitspiegel-authorized_keys" ]] && echo yes || echo no)"
    printf 'userconf.txt present:                       %s (hash redacted)\n' "$([[ -e "$BOOTFS/userconf.txt" ]] && echo yes || echo no)"
} >> "$REPORT"

# --- root filesystem -------------------------------------------------------
if [[ -n "$ROOTFS" ]]; then
    note "collecting the root filesystem"
    if [[ "$ROOTFS" != "$BUNDLE/rootfs" ]]; then
        for p in "${ROOT_PULL[@]}"; do
            copy_into "$ROOTFS$p" "$BUNDLE/rootfs$p"
        done
    fi
    # Redact every text file we copied off the root before anything is read
    # back out of it into the report.
    if [[ "$REDACT" == 1 ]]; then
        while IFS= read -r f; do redact "$f"; done < <(find "$BUNDLE/rootfs" -type f \
            \( -name '*.nmconnection' -o -name '*.conf' -o -name '*.state' -o -name 'shadow' -o -name 'userconf*' \) 2>/dev/null)
    fi

    emit "rootfs: /etc/os-release" "$BUNDLE/rootfs/etc/os-release" "/etc/os-release"
    emit "rootfs: /etc/hostname" "$BUNDLE/rootfs/etc/hostname" "/etc/hostname"
    emit "rootfs: /etc/zeitspiegel/config.toml (the config the unit ran)" "$BUNDLE/rootfs/etc/zeitspiegel/config.toml" "/etc/zeitspiegel/config.toml"
    emit "rootfs: /var/lib/NetworkManager/NetworkManager.state (NM's enable gate)" "$BUNDLE/rootfs/var/lib/NetworkManager/NetworkManager.state" "/var/lib/NetworkManager/NetworkManager.state"

    section "rootfs: NetworkManager profiles (/etc/NetworkManager/system-connections)"
    if [[ -d "$BUNDLE/rootfs/etc/NetworkManager/system-connections" ]]; then
        for f in "$BUNDLE/rootfs/etc/NetworkManager/system-connections"/*; do
            [[ -f "$f" ]] || continue
            printf '\n--- %s ---\n' "$(basename "$f")" >> "$REPORT"
            cat "$f" >> "$REPORT" 2>/dev/null || true
        done
        # autoconnect=false on both profiles is deliberate (E-8): an
        # autoconnecting radio races the role election.
        printf '\nautoconnect settings (both profiles must be false — E-8):\n' >> "$REPORT"
        grep -H 'autoconnect' "$BUNDLE/rootfs/etc/NetworkManager/system-connections"/* >> "$REPORT" 2>/dev/null \
            || printf '  (no autoconnect key — NetworkManager defaults to true)\n' >> "$REPORT"
    else
        printf '(not present on this card)\n' >> "$REPORT"
    fi

    section "rootfs: /var/lib/systemd/rfkill (what systemd-rfkill persisted)"
    if [[ -d "$BUNDLE/rootfs/var/lib/systemd/rfkill" ]]; then
        for f in "$BUNDLE/rootfs/var/lib/systemd/rfkill"/*; do
            [[ -f "$f" ]] || continue
            printf '  %s: %s\n' "$(basename "$f")" "$(cat "$f" 2>/dev/null)" >> "$REPORT"
        done
    else
        printf '(not present on this card)\n' >> "$REPORT"
    fi

    # Plain-text logs (rsyslog leftovers, wtmp-adjacent text) before the
    # journal, because they need no tooling to read.
    section "rootfs: /var/log text logs"
    if [[ -d "$BUNDLE/rootfs/var/log" ]]; then
        ls -la "$BUNDLE/rootfs/var/log" >> "$REPORT" 2>&1 || true
        while IFS= read -r f; do
            printf '\n--- %s ---\n' "${f#"$BUNDLE/rootfs"}" >> "$REPORT"
            tail -n 2000 "$f" >> "$REPORT" 2>/dev/null || true
        done < <(find "$BUNDLE/rootfs/var/log" -maxdepth 1 -type f \
                 \( -name '*.log' -o -name 'syslog*' -o -name 'messages*' \) 2>/dev/null | sort)
    else
        printf '(not present on this card)\n' >> "$REPORT"
    fi

    # --- the journal -------------------------------------------------------
    section "rootfs: persistent journal (/var/log/journal)"
    JDIR="$BUNDLE/rootfs/var/log/journal"
    if [[ -d "$JDIR" ]]; then
        JCOUNT=$(find "$JDIR" -type f -name '*.journal*' 2>/dev/null | wc -l | tr -d ' ')
        if [[ "$JCOUNT" == 0 ]]; then
            # The directory without the files is its own finding: the bake
            # creates /var/log/journal, so an empty one means journald never
            # wrote persistently — not that the unit had nothing to say.
            printf 'The directory exists but holds no journal files.\n\n' >> "$REPORT"
            printf 'The bake creates /var/log/journal, so journald had somewhere to write and\n' >> "$REPORT"
            printf 'did not use it. On a first boot that is expected — the machine id is\n' >> "$REPORT"
            printf 'generated during it, and journald keeps that boot in RAM. A card that has\n' >> "$REPORT"
            printf 'booted twice and still shows this has a real problem.\n' >> "$REPORT"
            printf '\nWhat the unit said on its last boot is in the boot partition captures\n' >> "$REPORT"
            printf 'above — the journal snapshot section carries it in full.\n' >> "$REPORT"
        else
        printf 'journal files carried in this bundle:\n' >> "$REPORT"
        find "$JDIR" -type f -name '*.journal*' -exec ls -la {} \; 2>/dev/null \
            | sed "s|$BUNDLE/rootfs||" >> "$REPORT" || true
        fi
        # An overlay root sends every write after the seal to tmpfs, journald
        # included. What is on the card is then the pre-seal boots only —
        # believing otherwise turns "the journal says nothing" into a wrong
        # conclusion about the boot that actually failed.
        if [[ "$SEALED" == 1 ]]; then
            printf '\nNOTE: this card is sealed (overlay root), so what is here ends at the seal —\n' >> "$REPORT"
            printf 'the first boot. Every boot since wrote its journal to tmpfs and lost it at\n' >> "$REPORT"
            printf 'power-off. For logs from a later boot, unseal the unit\n' >> "$REPORT"
            printf '(sudo raspi-config nonint disable_overlayfs && sudo reboot), reproduce, and\n' >> "$REPORT"
            printf 'collect again — or read what the boot partition captured, which survives\n' >> "$REPORT"
            printf 'the overlay because it is a separate FAT32 mount.\n' >> "$REPORT"
        fi
    else
        printf '(no persistent journal on this card — either it never booted far enough\n' >> "$REPORT"
        printf 'to write one, or the root filesystem could not be read here)\n' >> "$REPORT"
    fi

    if [[ -d "$JDIR" && "${JCOUNT:-0}" != 0 ]]; then
        RENDERED="$BUNDLE/journal.txt"
        if command -v journalctl >/dev/null 2>&1; then
            note "rendering the journal with journalctl"
            journalctl -D "$JDIR" --no-pager -o short-iso > "$RENDERED" 2>/dev/null || true
            journalctl -D "$JDIR" --no-pager -o short-iso -u zeitspiegel.service > "$BUNDLE/journal-zeitspiegel.txt" 2>/dev/null || true
            journalctl -D "$JDIR" --no-pager --list-boots >> "$REPORT" 2>/dev/null || true
        elif [[ "$RENDER_DOCKER" == 1 ]] && command -v docker >/dev/null 2>&1; then
            note "rendering the journal in a container (no journalctl on this machine)"
            docker run --rm -v "$JDIR":/j:ro debian:bookworm-slim sh -c \
                'apt-get update -qq >/dev/null 2>&1 && apt-get install -y -qq systemd >/dev/null 2>&1 && journalctl -D /j --no-pager -o short-iso' \
                > "$RENDERED" 2>/dev/null || true
        fi
        if [[ -s "$RENDERED" ]]; then
            emit "journal (rendered): the unit's own log, all boots" "$RENDERED" "journal.txt (rendered from /var/log/journal)"
            # The unit's own lines are what a Zeitspiegel bug is usually about.
            section "journal: zeitspiegel.service, role transitions, errors"
            grep -aE 'zeitspiegel|role|peer|capture|export|render|panic|error|fail' "$RENDERED" \
                | tail -n 4000 >> "$REPORT" 2>/dev/null || true
        else
            printf '\nThe journal is binary and this machine has no journalctl, so it is\n' >> "$REPORT"
            printf 'carried verbatim in the bundle instead. Render it on any Linux box:\n' >> "$REPORT"
            printf '    journalctl -D <bundle>/rootfs/var/log/journal --no-pager\n' >> "$REPORT"
            printf 'or re-run this script with --render-journal (uses Docker).\n' >> "$REPORT"
        fi
    fi
else
    section "rootfs: not collected"
    if [[ -n "$ROOTFS_NOTE" ]]; then
        printf '%s\n' "$ROOTFS_NOTE" >> "$REPORT"
    else
        printf 'No root filesystem was reachable, so the persistent journal\n' >> "$REPORT"
        printf '(/var/log/journal) — the logs the unit itself wrote — is not in this\n' >> "$REPORT"
        printf 'bundle. On macOS install the ext4 reader and run this again:\n' >> "$REPORT"
        printf '    brew install e2fsprogs\n' >> "$REPORT"
        printf 'On Linux, mount partition 2 read-only and pass --rootfs.\n' >> "$REPORT"
    fi
    if [[ "$SEALED" == 1 ]]; then
        printf '\nThis card is sealed (overlay root), so even once it can be read, the\n' >> "$REPORT"
        printf 'journal on it ends at the seal — the first boot. Later boots wrote to\n' >> "$REPORT"
        printf 'tmpfs and lost it at power-off.\n' >> "$REPORT"
    fi
fi

# --- one file to hand over -------------------------------------------------
# zip rather than tar.gz: it is the format that opens with a double click on
# every machine this bundle might be forwarded to. Minimal Linux images ship
# without the zip binary, so fall back rather than fail at the last step —
# still exactly one file either way.
# Whatever the card had and this bundle does not, said once, where a reader
# will see it before concluding the unit was quiet about something.
if [[ -n "$UNREADABLE" ]]; then
    section "files on the card that this bundle does NOT contain"
    printf 'These could not be read by the account that ran the collection, so
' >> "$REPORT"
    printf 'they were dropped rather than carried unredacted:
%s
' "$UNREADABLE" >> "$REPORT"
    printf '
On a card read with debugfs this should not happen — if it did, re-run
' >> "$REPORT"
    printf 'the collection and let the sudo prompt through.
' >> "$REPORT"
fi

ARTIFACT="$OUT_BASE/zeitspiegel-logs-$SLUG-$STAMP.zip"
if command -v zip >/dev/null 2>&1; then
    # A zip that cannot read part of its input exits 18 with two words of
    # explanation. Say which file and what to do about it.
    ( cd "$STAGING" && zip -qr "$ARTIFACT" "$(basename "$BUNDLE")" ) || {
        rm -f "$ARTIFACT"
        die "could not pack the bundle — something under $BUNDLE is unreadable.
  This is usually a card extracted under sudo whose files kept the card's own
  ownership. Re-run the collection; if it persists, run it with sudo."
    }
else
    ARTIFACT="$OUT_BASE/zeitspiegel-logs-$SLUG-$STAMP.tar.gz"
    tar -czf "$ARTIFACT" -C "$STAGING" "$(basename "$BUNDLE")"
    note "no zip on this machine — wrote a tar.gz instead"
fi
rm -rf "$STAGING"

# journald sizes the persistent journal against the card, so a unit that has
# been running for months can carry a lot of it. Say how much rather than
# letting someone discover it when the upload fails.
echo
echo "$ARTIFACT   ($(du -h "$ARTIFACT" | cut -f1 | tr -d ' '))"
echo
echo "report.txt inside it reads as plain text, with the raw logs beside it."
if [[ -n "$ROOTFS" ]]; then
    echo "Both halves of the card are in there, journal included."
else
    echo "Boot partition only — the unit's own journal is NOT in this bundle."
fi
