#!/usr/bin/env bash
# Build a finished, network-free Zeitspiegel appliance image (no SD card
# needed — that's flash-sd.sh). Downloads Pi OS Lite (cached), then bakes in
# the binary, packages, Wi-Fi access point and admin user via a privileged
# linux/arm64 container running deploy/sd/bake.sh.
#
#   make image                       # uses defaults / cached download
#   SSID=studio-mirror make image    # rename the open Wi-Fi network
#
# Env: AP_SSID ADMIN_PASS WIFI_COUNTRY SSH_PUBKEY IMG_URL  (Wi-Fi is open)
# Output: build/zeitspiegel-appliance.img  +  build/credentials.txt
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

AP_SSID="${AP_SSID:-zeitspiegel}"          # open Wi-Fi network (no password, E-7)
ADMIN_PASS="${ADMIN_PASS:-$(randpw)}"      # local-console login (SSH is off by default)
WIFI_COUNTRY="${WIFI_COUNTRY:-DE}"
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
    -e AP_SSID="$AP_SSID" \
    -e ADMIN_HASH="$ADMIN_HASH" -e WIFI_COUNTRY="$WIFI_COUNTRY" \
    golang:1.25-trixie bash /deploy/sd/bake.sh

cat > build/credentials.txt <<EOF
Zeitspiegel appliance credentials
  Wi-Fi SSID:    $AP_SSID   (open network, no password)
  Mirror UI:     http://zeitspiegel.local   (or http://10.42.0.1)
  Console login: zeitspiegel / $ADMIN_PASS
                 (HDMI + keyboard only — SSH is off by default.
                  sudo is passwordless. Escape hatch: touch ssh on
                  the SD's bootfs partition to enable SSH for one
                  boot; the authorized_keys baked from your
                  ~/.ssh/*.pub will then work.)
EOF

rm -f build/raspios.img.xz
echo
echo "Image ready: build/zeitspiegel-appliance.img"
cat build/credentials.txt
echo
echo "Write it to a card with:  make sd"
