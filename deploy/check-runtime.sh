#!/usr/bin/env bash
# Assert the appliance's dlopen-time dependencies exist in a root.
#
#   deploy/check-runtime.sh              # this machine (on the unit)
#   deploy/check-runtime.sh /mnt/zsroot  # an image being baked, before it boots
#
# The binary links against almost nothing interesting: SDL loads the EGL and
# GBM stack at runtime, so a missing library produces no build error, no link
# error and no failing test — only a black screen in a venue, which is exactly
# how libEGL.so.1 stayed absent from every baked card for two months. This is
# the check that makes that absence loud, and it runs at bake time, at install
# time and at boot.
#
# The list lives in deploy/runtime-libs.txt so there is one answer to "what
# does a unit need", shared by every path that installs anything.
set -euo pipefail

ROOT="${1:-/}"
LIBS_FILE="${LIBS_FILE:-$(cd "$(dirname "$0")" && pwd)/runtime-libs.txt}"

[[ -f "$LIBS_FILE" ]] || { echo "check-runtime: no library list at $LIBS_FILE" >&2; exit 2; }
[[ -d "$ROOT" ]] || { echo "check-runtime: no such root: $ROOT" >&2; exit 2; }

missing=0
checked=0
while read -r soname package _rest; do
    case "${soname:-}" in ''|'#'*) continue ;; esac
    checked=$((checked + 1))
    # Search the library directories rather than asking the dynamic loader:
    # the root being checked is usually an image that is not running, and on
    # the unit itself ldconfig's cache can lag a fresh install.
    case "$soname" in
        /*)
            # An absolute path is a data file the runtime loads by name — a
            # font, say. Nothing links it, so nothing else would notice.
            [[ -e "$ROOT$soname" ]] && continue
            ;;
        *)
            if find "$ROOT/usr/lib" "$ROOT/lib" -name "$soname" -print -quit 2>/dev/null | grep -q .; then
                continue
            fi
            ;;
    esac
    missing=$((missing + 1))
    echo "check-runtime: MISSING $soname — install $package" >&2
done < "$LIBS_FILE"

if [[ "$missing" -gt 0 ]]; then
    echo "check-runtime: $missing of $checked runtime libraries missing from $ROOT." >&2
    echo "  These are dlopened, not linked, so nothing else will tell you: the image" >&2
    echo "  will build and boot and the mirror will never open a screen." >&2
    exit 1
fi

echo "check-runtime: all $checked runtime libraries present in $ROOT"
