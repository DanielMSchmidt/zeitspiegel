#!/usr/bin/env bash
# Build a finished, network-free Zeitspiegel appliance image (no SD card
# needed — that's flash-sd.sh). Downloads Pi OS Lite (cached), then bakes in
# the binary, packages, Wi-Fi access point and admin user via a privileged
# linux/arm64 container running deploy/sd/bake.sh.
#
#   make image                       # uses defaults / cached downloads
#   SSID=studio-mirror make image    # rename the open Wi-Fi network
#   SEAL=0 make image                # development image: no read-only overlay
#   make warm-cache                  # bake once with a network, then:
#   OFFLINE=1 make image             # ...bake with none, from the caches alone
#   ./scripts/build-image.sh --check-caches   # could I bake offline right now?
#
# Env: AP_SSID ADMIN_PASS WIFI_COUNTRY SSH_PUBKEY IMG_URL IMG_SHA256 SEAL
#      CACHE_DIR OFFLINE BUILDER_IMAGE   (Wi-Fi is open)
# Output: build/zeitspiegel-appliance.img  +  build/credentials.txt
#         (SEAL=0: build/zeitspiegel-appliance-dev.img + credentials-dev.txt)
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "error: $*" >&2; exit 1; }

# Everything a second bake would otherwise download again. CACHE_DIR is
# overridable so two checkouts can share one cache — and so the preflight can
# be pointed at a fixture (UT-42) instead of the real thing.
CACHE_DIR="${CACHE_DIR:-$PWD/build/cache}"
IMG_XZ="$CACHE_DIR/raspios-lite-arm64.img.xz"
APT_CACHE="$CACHE_DIR/apt"
GOMOD_CACHE="$CACHE_DIR/gomod"
OFFLINE="${OFFLINE:-0}"
BUILDER_IMAGE="${BUILDER_IMAGE:-zeitspiegel-builder:trixie-arm64}"

sha256_of() {
    if command -v shasum >/dev/null; then shasum -a 256 "$1" | awk '{print $1}'
    else sha256sum "$1" | awk '{print $1}'; fi
}

# check_caches reports, one line per cache, whether a card could be made right
# now with the network off — and names every cold one in a single pass, since
# the answer is wanted before the trip and nobody should have to run this four
# times to collect the whole list. Non-zero if anything is cold.
check_caches() {
    local cold=0
    echo "offline caches under $CACHE_DIR"
    if [[ -s "$IMG_XZ" ]]; then
        echo "  warm  raspios-lite-arm64.img.xz  ($(du -h "$IMG_XZ" | awk '{print $1}'), the base OS)"
    else
        echo "  COLD  raspios-lite-arm64.img.xz — the base OS download"; cold=1
    fi
    if compgen -G "$APT_CACHE/archives/*.deb" >/dev/null; then
        echo "  warm  apt/archives  ($(compgen -G "$APT_CACHE/archives/*.deb" | wc -l | tr -d ' ') packages the card gets)"
    else
        echo "  COLD  apt/archives — the runtime packages the chroot installs"; cold=1
    fi
    if [[ -n "$(ls -A "$APT_CACHE/lists" 2>/dev/null)" ]]; then
        echo "  warm  apt/lists"
    else
        echo "  COLD  apt/lists — without these apt cannot resolve a dependency offline"; cold=1
    fi
    if [[ -d "$GOMOD_CACHE/cache/download" ]]; then
        echo "  warm  gomod  (the modules the cross-build needs)"
    else
        echo "  COLD  gomod — the Go module cache"; cold=1
    fi
    if (( cold )); then
        echo
        echo "one 'make warm-cache' with a network fills all of it; after that a card can be made without one." >&2
    fi
    return $cold
}

# Argument handling comes before anything with a side effect: this script's
# main path bakes a 4.8 GB image and mints a new admin password, and an
# argument it merely ignored would do both when all that was asked was a
# question.
case "${1:-}" in
    --check-caches) if check_caches; then exit 0; fi; exit 1 ;;
    "") ;;
    *) die "unknown argument: $1 (only --check-caches)" ;;
esac
[[ $# -le 1 ]] || die "unexpected arguments: ${*:2}"
randpw() {
    local s=""
    while [ ${#s} -lt 12 ]; do s="$s$(head -c 64 /dev/urandom | LC_ALL=C tr -dc 'a-z0-9')"; done
    printf '%s' "${s:0:12}"
}

command -v docker >/dev/null || die "docker not found"
[[ -f bin/zeitspiegel-pi ]] || die "bin/zeitspiegel-pi missing — run 'make pi-binary' (make image does this for you)"
docker image inspect "$BUILDER_IMAGE" >/dev/null 2>&1 \
    || die "builder image $BUILDER_IMAGE is missing — run 'make builder-image' (make image does this for you)"

# Offline is a promise the caller made; check it can be kept before spending
# five minutes finding out it cannot.
if [[ "$OFFLINE" == 1 ]]; then
    check_caches || die "OFFLINE=1 but a cache is cold (above) — this bake would need a network"
fi

# SSID is the documented spelling; AP_SSID is kept for older scripts.
AP_SSID="${SSID:-${AP_SSID:-zeitspiegel}}" # open Wi-Fi network (no password, E-7)
ADMIN_PASS="${ADMIN_PASS:-$(randpw)}"      # local-console login (SSH is off by default)
WIFI_COUNTRY="${WIFI_COUNTRY:-DE}"
# Radio band/channel for the AP. bg/6 is the compatibility default; with
# several units in one room, AP_BAND=a AP_CHANNEL=36 (non-DFS 5 GHz) gives
# clip downloads far more headroom.
AP_BAND="${AP_BAND:-bg}"
AP_CHANNEL="${AP_CHANNEL:-6}"
# SEAL=0 leaves the root writable so the journal survives reboots — the whole
# point of a dev card. It gets its own file names: a dev image that overwrote
# the production one would be flashed later believing it was sealed.
SEAL="${SEAL:-1}"
# Normally handed down by the Makefile so the image and the binary in it
# carry the same string; computed here too, for a direct call.
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo unknown)}"
if [[ "$SEAL" == 1 ]]; then
    OUT_NAME=zeitspiegel-appliance.img
    CRED=build/credentials.txt
else
    OUT_NAME=zeitspiegel-appliance-dev.img
    CRED=build/credentials-dev.txt
fi
IMG_URL="${IMG_URL:-https://downloads.raspberrypi.com/raspios_lite_arm64_latest}"

mkdir -p "$CACHE_DIR" "$APT_CACHE/archives" "$APT_CACHE/lists" build/payload
# What the cached base image is and where it came from. IMG_URL says "latest",
# so without this a deleted cache silently swaps the OS underneath the
# appliance; with it, every bake states which download it is building on and
# says so if the bytes ever change. Pin a known one with IMG_SHA256=...
PROVENANCE="$IMG_XZ.provenance"
if [[ ! -s "$IMG_XZ" ]]; then
    [[ "$OFFLINE" != 1 ]] || die "no cached base image and OFFLINE=1"
    echo "==> downloading Raspberry Pi OS Lite (arm64) ..."
    curl -fL --progress-bar -o "$IMG_XZ.tmp" "$IMG_URL"
    mv "$IMG_XZ.tmp" "$IMG_XZ"
    printf 'url=%s\nsha256=%s\nfetched=%s\n' \
        "$IMG_URL" "$(sha256_of "$IMG_XZ")" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$PROVENANCE"
fi
IMG_HAVE=$(sha256_of "$IMG_XZ")
if [[ -s "$PROVENANCE" ]]; then
    IMG_WANT=$(sed -n 's/^sha256=//p' "$PROVENANCE")
    [[ "$IMG_HAVE" == "$IMG_WANT" ]] \
        || die "cached base image no longer matches $PROVENANCE (corrupt or replaced) — delete both and retry"
else
    # A cache from before this file existed: adopt it rather than throw away a
    # 577 MB download, but record what is actually there from now on.
    printf 'url=%s\nsha256=%s\nadopted=%s\n' \
        "unknown (cache predates provenance)" "$IMG_HAVE" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$PROVENANCE"
fi
[[ -z "${IMG_SHA256:-}" || "$IMG_SHA256" == "$IMG_HAVE" ]] \
    || die "base image is $IMG_HAVE, IMG_SHA256 asks for $IMG_SHA256"
echo "==> base image $(basename "$IMG_XZ")  sha256 ${IMG_HAVE:0:12}…"

echo "==> staging payload"
cp bin/zeitspiegel-pi                       build/payload/zeitspiegel
cp deploy/config.toml                       build/payload/config.toml
cp deploy/zeitspiegel.service               build/payload/zeitspiegel.service
cp deploy/sd/seal.sh                        build/payload/seal.sh
cp deploy/sd/zeitspiegel-seal.service       build/payload/zeitspiegel-seal.service
cp deploy/sd/zeitspiegel-debug.sh           build/payload/zeitspiegel-debug.sh
cp deploy/sd/zeitspiegel-debug-pre-rfkill.service  build/payload/zeitspiegel-debug-pre-rfkill.service
cp deploy/sd/zeitspiegel-debug-post-rfkill.service build/payload/zeitspiegel-debug-post-rfkill.service
cp deploy/sd/zeitspiegel-debug-late.service        build/payload/zeitspiegel-debug-late.service
cp deploy/sd/zeitspiegel-rfkill-unblock.service    build/payload/zeitspiegel-rfkill-unblock.service
cp deploy/check-runtime.sh                         build/payload/check-runtime.sh
cp deploy/runtime-libs.txt                         build/payload/runtime-libs.txt
cp deploy/zeitspiegel-boot-profile.sh              build/payload/zeitspiegel-boot-profile.sh
cp deploy/zeitspiegel-boot-profile.service         build/payload/zeitspiegel-boot-profile.service
cp deploy/zeitspiegel-boot-profile.timer           build/payload/zeitspiegel-boot-profile.timer
rm -f build/payload/authorized_keys
if [[ -z "${SSH_PUBKEY:-}" ]]; then
    for k in ~/.ssh/id_ed25519.pub ~/.ssh/id_rsa.pub; do [[ -f "$k" ]] && SSH_PUBKEY="$k" && break; done
fi
# An `&&` here would end the script under `set -e` on any machine with no SSH
# key at all — a bake that stops with no output and no image.
if [[ -n "${SSH_PUBKEY:-}" && -f "${SSH_PUBKEY:-}" ]]; then
    cp "$SSH_PUBKEY" build/payload/authorized_keys
fi

# OFFLINE is not a preference the containers are asked to respect — it is taken
# away from them. A bake that still reaches for the network fails here rather
# than working on the machine that has one and nowhere else.
NET=()
if [[ "$OFFLINE" == 1 ]]; then NET=(--network none); fi

# sha512-crypt the admin password (consumed by Pi OS userconf.txt). Hashing a
# string needs no network under any circumstances.
ADMIN_HASH=$(docker run --rm --network none "$BUILDER_IMAGE" openssl passwd -6 "$ADMIN_PASS")

echo "==> baking image (privileged linux/arm64 container) ..."
# The cache is mounted rather than copied: the base image is read straight out
# of it (a 577 MB copy per bake, otherwise) and the .debs apt downloads are
# written back into it, which is what makes the next bake offline.
docker run --rm --privileged --platform linux/arm64 ${NET[@]+"${NET[@]}"} \
    -v "$PWD/build":/work -v "$PWD/deploy":/deploy:ro -v "$CACHE_DIR":/cache \
    -e SRC_XZ="/cache/$(basename "$IMG_XZ")" -e APT_CACHE=/cache/apt -e OFFLINE="$OFFLINE" \
    -e AP_SSID="$AP_SSID" -e AP_BAND="$AP_BAND" -e AP_CHANNEL="$AP_CHANNEL" \
    -e ADMIN_HASH="$ADMIN_HASH" -e WIFI_COUNTRY="$WIFI_COUNTRY" \
    -e SEAL="$SEAL" -e OUT_NAME="$OUT_NAME" -e VERSION="$VERSION" \
    "$BUILDER_IMAGE" bash /deploy/sd/bake.sh

cat > "$CRED" <<EOF
Zeitspiegel appliance credentials
  Wi-Fi SSID:    $AP_SSID   (open network, no password)
  Mirror UI:     http://zeitspiegel.local   (or http://10.42.0.1)
  More mirrors:  write this SAME image to every card, now or years from
                 now — whichever unit is on first hosts the network,
                 the rest join it, and if the host is unplugged another
                 takes over by itself. Each card gets its own label from
                 'make sd NAME=...'; rename later with
                 scripts/stage-name.sh "New name" /Volumes/bootfs.
  Console login: zeitspiegel / $ADMIN_PASS
                 (HDMI + keyboard only — SSH is off by default.
                  sudo is passwordless. Escape hatch: touch ssh on
                  the SD's bootfs partition to enable SSH for one
                  boot; the authorized_keys baked from your
                  ~/.ssh/*.pub will then work.)
EOF
if [[ "$SEAL" != 1 ]]; then
    cat >> "$CRED" <<'EOF'
  DEVELOPMENT CARD: the read-only overlay is NOT enabled. The root stays
                 writable, so /var/log/journal survives reboots and
                 'make sd-logs' can read every boot off the card — not just
                 the first. The trade is the one NFR-9 buys back: an
                 unsealed card can be corrupted by pulling the plug, so
                 this belongs on a bench, not in a venue. Seal it later
                 with: sudo systemctl enable zeitspiegel-seal && sudo reboot
EOF
fi

echo
echo "Image ready: build/$OUT_NAME   (version $VERSION)"
cat "$CRED"
echo
echo "Write it to a card with:  make sd NAME=\"Long Side\""
