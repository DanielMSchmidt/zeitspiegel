#!/usr/bin/env bash
# Build a finished, network-free Zeitspiegel appliance image (no SD card
# needed — that's flash-sd.sh). Downloads Pi OS Lite (cached), then bakes in
# the binary, packages, Wi-Fi access point and admin user via a privileged
# linux/arm64 container running deploy/sd/bake.sh.
#
#   make image                       # uses defaults / cached download
#   SSID=studio-mirror make image    # rename the open Wi-Fi network
#   SEAL=0 make image                # development image: no read-only overlay
#
# Env: AP_SSID ADMIN_PASS WIFI_COUNTRY SSH_PUBKEY IMG_URL SEAL  (Wi-Fi is open)
# Output: build/zeitspiegel-appliance.img  +  build/credentials.txt
#         (SEAL=0: build/zeitspiegel-appliance-dev.img + credentials-dev.txt)
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "error: $*" >&2; exit 1; }
randpw() {
    local s=""
    while [ ${#s} -lt 12 ]; do s="$s$(head -c 64 /dev/urandom | LC_ALL=C tr -dc 'a-z0-9')"; done
    printf '%s' "${s:0:12}"
}

command -v docker >/dev/null || die "docker not found"
command -v xz >/dev/null || die "xz not found — brew install xz"
[[ -f bin/zeitspiegel-pi ]] || die "bin/zeitspiegel-pi missing — run 'make pi-binary' (make image does this for you)"

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

mkdir -p build/cache build/payload
IMG_XZ=build/cache/raspios-lite-arm64.img.xz
if [[ ! -s "$IMG_XZ" ]]; then
    echo "==> downloading Raspberry Pi OS Lite (arm64) ..."
    curl -fL --progress-bar -o "$IMG_XZ.tmp" "$IMG_URL"
    mv "$IMG_XZ.tmp" "$IMG_XZ"
else
    echo "==> using cached image $IMG_XZ (delete to re-download)"
fi
xz -t "$IMG_XZ" || die "cached image corrupt — delete $IMG_XZ and retry"
cp "$IMG_XZ" build/raspios.img.xz

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
[[ -n "${SSH_PUBKEY:-}" && -f "${SSH_PUBKEY:-}" ]] && cp "$SSH_PUBKEY" build/payload/authorized_keys

# sha512-crypt the admin password (consumed by Pi OS userconf.txt)
ADMIN_HASH=$(docker run --rm golang:1.25-trixie openssl passwd -6 "$ADMIN_PASS")

echo "==> baking image (privileged linux/arm64 container) ..."
docker run --rm --privileged --platform linux/arm64 \
    -v "$PWD/build":/work -v "$PWD/deploy":/deploy:ro \
    -e AP_SSID="$AP_SSID" -e AP_BAND="$AP_BAND" -e AP_CHANNEL="$AP_CHANNEL" \
    -e ADMIN_HASH="$ADMIN_HASH" -e WIFI_COUNTRY="$WIFI_COUNTRY" \
    -e SEAL="$SEAL" -e OUT_NAME="$OUT_NAME" -e VERSION="$VERSION" \
    golang:1.25-trixie bash /deploy/sd/bake.sh

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

rm -f build/raspios.img.xz
echo
echo "Image ready: build/$OUT_NAME   (version $VERSION)"
cat "$CRED"
echo
echo "Write it to a card with:  make sd NAME=\"Long Side\""
