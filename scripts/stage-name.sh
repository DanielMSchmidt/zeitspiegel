#!/usr/bin/env bash
# Stage a mirror's display name — its label in the UI — onto a card's FAT32
# bootfs partition. The appliance reads that file at boot (internal/identity),
# so every card still ships the byte-identical image (E-8): the label is the
# one per-card thing and it deliberately lives outside the image.
#
#   scripts/stage-name.sh "Long Side"                 # validate only, echo it back
#   scripts/stage-name.sh "Long Side" /Volumes/bootfs # validate and write
#   scripts/stage-name.sh auto  /Volumes/bootfs   # deliberately unnamed card
#
# `make sd NAME=...` calls this twice: once to reject a bad label in the first
# second, and once against the freshly written card.
set -euo pipefail

NAME_FILE=zeitspiegel-name.txt
MAX_LEN=32   # identity.MaxNameLen — a longer label is truncated by the unit

die() { echo "stage-name: $*" >&2; exit 1; }

[[ $# -ge 1 && $# -le 2 ]] || die "usage: stage-name.sh <name> [bootfs-dir]"
raw="$1"
bootfs="${2:-}"

# The unit reads the first line only, so a label with a newline in it would
# reach the screen silently shortened. Refuse it here, where it can still be
# retyped.
if [[ "$raw" == *$'\n'* || "$raw" == *$'\r'* ]]; then
    die "the name must be a single line"
fi

# Trim in bash rather than with sed: no locale surprises, and the unit trims
# the same way when it reads the file back.
name="${raw#"${raw%%[![:space:]]*}"}"
name="${name%"${name##*[![:space:]]}"}"
[[ -n "$name" ]] || die "the name is empty — pass a label, or 'auto' for a card that names itself"

if [[ -n "$bootfs" && ! -d "$bootfs" ]]; then
    die "no such directory: $bootfs"
fi

# `auto` is the documented opt-out: nothing is staged, and the unit falls back
# to "Zeitspiegel <ID>" after its CPU serial.
if [[ "$name" == "auto" ]]; then
    [[ -z "$bootfs" ]] || rm -f "$bootfs/$NAME_FILE"
    printf 'auto\n'
    exit 0
fi

# Count characters, not bytes: UTF-8 continuation bytes (0x80–0xBF) are the
# tail of a multi-byte rune, so what is left is one byte per code point.
len=$(( $(printf '%s' "$name" | LC_ALL=C tr -d '\200-\277' | wc -c) ))
if (( len > MAX_LEN )); then
    echo "stage-name: warning: $len characters — the unit truncates the label to $MAX_LEN" >&2
fi

if [[ -n "$bootfs" ]]; then
    # macOS mounts a freshly written FAT32 boot partition read-only often
    # enough that the bare redirect failure — a script line number and
    # "Read-only file system" — is a support question rather than an
    # instruction. Probe first, then say what to do about it.
    probe="$bootfs/.zeitspiegel-write-probe"
    # The group is what silences the shell's own redirect failure: on
    # `: >"$probe" 2>/dev/null` the redirections apply left to right, so the
    # message is already out before stderr is diverted.
    if ! { : > "$probe"; } 2>/dev/null; then
        rm -f "$probe" 2>/dev/null || true
        die "cannot write to $bootfs — the boot partition is mounted read-only.
  Eject and re-insert the card, then: scripts/stage-name.sh \"$name\" $bootfs"
    fi
    rm -f "$probe"
    # Truncate, never append: the unit reads the first line, so a leftover
    # from an earlier naming would outrank the new label forever.
    printf '%s\n' "$name" > "$bootfs/$NAME_FILE"
fi
printf '%s\n' "$name"
