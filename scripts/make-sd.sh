#!/usr/bin/env bash
# Flash and stage a fully self-provisioning Zeitspiegel SD card (macOS).
#
#   make sd                      # builds the Pi binary, then runs this
#   DISK=/dev/disk4 make sd      # skip auto-detection
#   AP_PASS=mystudiopw make sd   # choose the Wi-Fi passphrase
#
# What you get: insert the card into the Pi, connect ethernet to any router,
# power on, wait. The Pi provisions itself (packages, binary, its own Wi-Fi
# access point, read-only seal) and reboots into the mirror. Unplug ethernet,
# done — from then on it is fully offline plug-and-play.
#
# Environment:
#   DISK          target device (e.g. /dev/disk4); auto-detected when unset
#   AP_SSID       Wi-Fi name the appliance hosts        (default zeitspiegel)
#   AP_PASS       WPA2 passphrase, ≥ 8 chars            (default: random)
#   ADMIN_PASS    password of the `zeitspiegel` ssh/sudo user (default: random)
#   WIFI_COUNTRY  radio regulatory domain               (default DE)
#   SSH_PUBKEY    public key file to authorize          (default: auto-detect)
#   IMG_URL       Pi OS image override                  (default: latest Lite arm64)
set -euo pipefail
cd "$(dirname "$0")/.."

die() { echo "error: $*" >&2; exit 1; }

# random lowercase+digit secret; head-first ordering avoids SIGPIPE under pipefail
randpw() {
    local s=""
    while [ ${#s} -lt 12 ]; do
        s="$s$(head -c 64 /dev/urandom | LC_ALL=C tr -dc 'a-z0-9')"
    done
    printf '%s' "${s:0:12}"
}

[[ "$(uname)" == "Darwin" ]] || die "this script targets macOS (Linux: flash with rpi-imager, then copy the staging files per deploy/PROVISIONING.md)"
command -v xz >/dev/null || die "xz not found — brew install xz"
command -v docker >/dev/null || die "docker not found (needed to hash the admin password)"
[[ -f bin/zeitspiegel-pi ]] || die "bin/zeitspiegel-pi missing — run via 'make sd' (it builds it first)"

AP_SSID="${AP_SSID:-zeitspiegel}"
AP_PASS="${AP_PASS:-$(randpw)}"
ADMIN_PASS="${ADMIN_PASS:-$(randpw)}"
WIFI_COUNTRY="${WIFI_COUNTRY:-DE}"
IMG_URL="${IMG_URL:-https://downloads.raspberrypi.com/raspios_lite_arm64_latest}"
[[ ${#AP_PASS} -ge 8 ]] || die "AP_PASS must be at least 8 characters (WPA2)"

# --- pick the target disk, with guard rails --------------------------------
if [[ -z "${DISK:-}" ]]; then
    # no mapfile: macOS ships bash 3.2
    EXTERNAL=$(diskutil list external physical | awk '/^\/dev\/disk/ {print $1}')
    set -- $EXTERNAL
    [[ $# -gt 0 ]] || die "no external disk found — insert the SD card"
    [[ $# -eq 1 ]] || die "several external disks found ($*) — set DISK="
    DISK="$1"
fi
[[ "$DISK" =~ ^/dev/disk[0-9]+$ ]] || die "DISK must look like /dev/diskN (whole disk, not a slice)"
diskutil info "$DISK" >/dev/null || die "no such disk: $DISK"
diskutil info "$DISK" | grep -q "Internal: *Yes" && die "refusing to touch internal disk $DISK"

echo
echo "About to ERASE this disk and turn it into a Zeitspiegel appliance card:"
diskutil info "$DISK" | grep -E "Device Identifier|Device / Media Name|Disk Size" | sed 's/^ */  /'
echo
read -r -p "Type 'erase' to continue: " answer
[[ "$answer" == "erase" ]] || die "aborted"

# --- fetch the Pi OS Lite image (cached) ------------------------------------
CACHE=build/cache
mkdir -p "$CACHE"
IMG_XZ="$CACHE/raspios-lite-arm64.img.xz"
if [[ ! -s "$IMG_XZ" ]]; then
    echo "==> downloading Raspberry Pi OS Lite (arm64) ..."
    curl -fL --progress-bar -o "$IMG_XZ.tmp" "$IMG_URL"
    mv "$IMG_XZ.tmp" "$IMG_XZ"
else
    echo "==> using cached image $IMG_XZ (delete it to re-download)"
fi
xz -t "$IMG_XZ" || die "cached image is corrupt — delete $IMG_XZ and retry"

# --- hash the admin password (sha512-crypt, consumed by userconf.txt) ------
ADMIN_HASH=$(docker run --rm golang:1.25-trixie openssl passwd -6 "$ADMIN_PASS")

# --- flash -------------------------------------------------------------------
RDISK="${DISK/\/dev\/disk//dev/rdisk}"
echo "==> unmounting $DISK"
diskutil unmountDisk force "$DISK"
echo "==> writing image to $RDISK (sudo will prompt; takes a few minutes)"
xz -dc "$IMG_XZ" | sudo dd of="$RDISK" bs=4m
sync
echo "==> remounting boot partition"
diskutil mountDisk "$DISK" >/dev/null
BOOT=""
for _ in $(seq 1 30); do
    for cand in /Volumes/bootfs /Volumes/boot; do
        [[ -d "$cand" && -f "$cand/cmdline.txt" ]] && BOOT="$cand" && break 2
    done
    sleep 1
done
[[ -n "$BOOT" ]] || die "boot partition did not mount — re-insert the card and re-run"

# --- stage the self-provisioning payload ------------------------------------
echo "==> staging payload on $BOOT"
touch "$BOOT/ssh" # enable sshd (consumed by raspberrypi-sys-mods)
printf 'zeitspiegel:%s\n' "$ADMIN_HASH" > "$BOOT/userconf.txt"

CARD="$BOOT/zeitspiegel"
mkdir -p "$CARD"
cp bin/zeitspiegel-pi               "$CARD/zeitspiegel"
cp deploy/setup.sh                  "$CARD/setup.sh"
cp deploy/config.toml               "$CARD/config.toml"
cp deploy/zeitspiegel.service       "$CARD/zeitspiegel.service"
cp deploy/sd/firstboot.sh           "$CARD/firstboot.sh"
cp deploy/sd/zeitspiegel-firstboot.service "$CARD/zeitspiegel-firstboot.service"
printf '%s' "$AP_SSID"       > "$CARD/ap-ssid"
printf '%s' "$AP_PASS"       > "$CARD/ap-password.txt"
printf '%s' "$ADMIN_PASS"    > "$CARD/admin-password.txt"
printf '%s' "$WIFI_COUNTRY"  > "$CARD/wifi-country"

if [[ -z "${SSH_PUBKEY:-}" ]]; then
    for k in ~/.ssh/id_ed25519.pub ~/.ssh/id_rsa.pub; do
        [[ -f "$k" ]] && SSH_PUBKEY="$k" && break
    done
fi
if [[ -n "${SSH_PUBKEY:-}" && -f "$SSH_PUBKEY" ]]; then
    cp "$SSH_PUBKEY" "$CARD/authorized_keys"
    echo "    ssh key: $SSH_PUBKEY"
else
    echo "    no ssh public key found — ssh will use the admin password"
fi

# first-boot hook, exactly like the Raspberry Pi Imager wires it
cp deploy/sd/firstrun.sh "$BOOT/firstrun.sh"
CMDLINE=$(cat "$BOOT/cmdline.txt")
if [[ "$CMDLINE" != *firstrun.sh* ]]; then
    printf '%s systemd.run=/boot/firmware/firstrun.sh systemd.run_success_action=reboot systemd.unit=kernel-command-line.target\n' \
        "$(echo "$CMDLINE" | tr -d '\n')" > "$BOOT/cmdline.txt"
fi

sync
diskutil eject "$DISK" >/dev/null
echo
echo "Card ready. Next steps:"
echo "  1. Put the card in the Pi, connect ETHERNET to any router, power on."
echo "  2. Wait until the Wi-Fi \"$AP_SSID\" appears (first boot installs"
echo "     packages and reboots twice — allow ~10 minutes)."
echo "  3. Unplug ethernet. Done — plug-and-play from now on."
echo
echo "  Wi-Fi:     $AP_SSID   passphrase: $AP_PASS"
echo "  Mirror UI: http://zeitspiegel.local  (or http://10.42.0.1)"
echo "  ssh:       zeitspiegel@zeitspiegel.local   password: $ADMIN_PASS"
echo "  (all three are also stored on the card in zeitspiegel/)"
