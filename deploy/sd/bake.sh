#!/usr/bin/env bash
# Runs INSIDE a privileged linux/arm64 container (see scripts/build-image.sh).
# Turns a stock Raspberry Pi OS Lite image into a finished, network-free
# Zeitspiegel appliance image: packages are installed here at build time, so
# the card never needs internet. Native arm64 chroot — no qemu.
#
# Inputs (env):  AP_SSID ADMIN_HASH WIFI_COUNTRY  [GROW_MB]   (open AP, no pass)
# Inputs (files under /work):  raspios.img.xz, payload/{zeitspiegel,config.toml,
#                              zeitspiegel.service,zeitspiegel-seal.service,seal.sh}
# Output:        /work/zeitspiegel-appliance.img
set -euo pipefail

: "${AP_SSID:?}" "${ADMIN_HASH:?}" "${WIFI_COUNTRY:?}"
GROW_MB="${GROW_MB:-1500}"
SRC_XZ=/work/raspios.img.xz
OUT=/work/zeitspiegel-appliance.img
PAYLOAD=/work/payload
ROOT=/mnt/zsroot

export DEBIAN_FRONTEND=noninteractive
echo "==> container tools"
apt-get update -qq
apt-get install -y -qq xz-utils cloud-guest-utils e2fsprogs dosfstools util-linux parted >/dev/null

echo "==> decompress image"
rm -f "$OUT"
xz -dc "$SRC_XZ" > "$OUT"
echo "==> grow image by ${GROW_MB} MiB (room for packages)"
truncate -s "+${GROW_MB}M" "$OUT"

BOOT_LOOP="" ROOT_LOOP=""
cleanup() {
    set +e
    for m in "$ROOT/boot/firmware" "$ROOT/dev/pts" "$ROOT/dev" "$ROOT/sys" "$ROOT/proc" "$ROOT"; do
        mountpoint -q "$m" && umount "$m"
    done
    [[ -n "$BOOT_LOOP" ]] && losetup -d "$BOOT_LOOP"
    [[ -n "$ROOT_LOOP" ]] && losetup -d "$ROOT_LOOP"
}
trap cleanup EXIT

echo "==> grow root partition + filesystem"
DISK_LOOP=$(losetup -f --show "$OUT")
growpart "$DISK_LOOP" 2
losetup -d "$DISK_LOOP"
# Docker Desktop's kernel creates no /dev/loopNpM partition nodes, so map each
# partition as its own offset loop device (sector size 512) instead.
secs() { partx -g -r -o "$2" --nr "$1" "$OUT" | tr -dc 0-9; }
P1_START=$(secs 1 START); P1_SECT=$(secs 1 SECTORS)
P2_START=$(secs 2 START); P2_SECT=$(secs 2 SECTORS)
BOOT_LOOP=$(losetup --show -f -o $((P1_START * 512)) --sizelimit $((P1_SECT * 512)) "$OUT")
ROOT_LOOP=$(losetup --show -f -o $((P2_START * 512)) --sizelimit $((P2_SECT * 512)) "$OUT")
e2fsck -fy "$ROOT_LOOP" || true
resize2fs "$ROOT_LOOP"

echo "==> mount root + boot"
mkdir -p "$ROOT"
mount "$ROOT_LOOP" "$ROOT"
mount "$BOOT_LOOP" "$ROOT/boot/firmware"
for d in proc sys dev dev/pts; do mount --bind "/$d" "$ROOT/$d"; done

# DNS for the chroot's apt; restore the image's original afterwards.
HADRES=no
if [[ -e "$ROOT/etc/resolv.conf" ]]; then cp -a "$ROOT/etc/resolv.conf" "$ROOT/etc/resolv.conf.zsbak"; HADRES=yes; fi
rm -f "$ROOT/etc/resolv.conf"; echo "nameserver 1.1.1.1" > "$ROOT/etc/resolv.conf"

echo "==> install runtime packages into the image"
chroot "$ROOT" apt-get update -qq
chroot "$ROOT" apt-get install -y -qq ffmpeg libsdl2-2.0-0 libsdl2-image-2.0-0 \
    network-manager dnsmasq-base iptables rfkill iw >/dev/null
# dnsmasq-base + iptables: required by NM's `ipv4.method=shared` AP profile
# (DHCP to clients + NAT rules). rfkill + iw: lightweight tools for in-place
# debugging when the appliance won't broadcast.

echo "==> install zeitspiegel binary / config / unit"
install -D -m0755 "$PAYLOAD/zeitspiegel"          "$ROOT/usr/local/bin/zeitspiegel"
install -D -m0644 "$PAYLOAD/config.toml"          "$ROOT/etc/zeitspiegel/config.toml"
install -D -m0644 "$PAYLOAD/zeitspiegel.service"  "$ROOT/etc/systemd/system/zeitspiegel.service"
install -D -m0755 "$PAYLOAD/seal.sh"              "$ROOT/usr/local/sbin/zeitspiegel-seal"
install -D -m0644 "$PAYLOAD/zeitspiegel-seal.service" "$ROOT/etc/systemd/system/zeitspiegel-seal.service"
chroot "$ROOT" systemctl enable NetworkManager  >/dev/null 2>&1 || true
chroot "$ROOT" systemctl enable ssh             >/dev/null 2>&1 || true
chroot "$ROOT" systemctl enable zeitspiegel.service      >/dev/null
chroot "$ROOT" systemctl enable zeitspiegel-seal.service >/dev/null

echo "==> install + enable boot-time diagnostic capture (3 stages → /boot/firmware/zeitspiegel-debug.log)"
# Until the AP is reliably coming up on first boot, every appliance image
# self-instruments and dumps rfkill / NM / kernel state to the FAT32 boot
# partition. The user pulls the SD card and reads the log directly.
install -D -m0755 "$PAYLOAD/zeitspiegel-debug.sh" "$ROOT/usr/local/sbin/zeitspiegel-debug"
for u in zeitspiegel-debug-pre-rfkill zeitspiegel-debug-post-rfkill zeitspiegel-debug-late; do
    install -D -m0644 "$PAYLOAD/${u}.service" "$ROOT/etc/systemd/system/${u}.service"
    chroot "$ROOT" systemctl enable "${u}.service" >/dev/null
done

echo "==> hostname + mDNS (zeitspiegel.local)"
echo zeitspiegel > "$ROOT/etc/hostname"
sed -i 's/127\.0\.1\.1.*/127.0.1.1\tzeitspiegel/' "$ROOT/etc/hosts" 2>/dev/null \
    || printf '127.0.1.1\tzeitspiegel\n' >> "$ROOT/etc/hosts"

echo "==> admin user + ssh (Pi OS userconf mechanism)"
# userconf.txt is the supported headless way to create the first user on
# first boot; it also satisfies the Bookworm/Trixie first-run user gate.
printf 'zeitspiegel:%s\n' "$ADMIN_HASH" > "$ROOT/boot/firmware/userconf.txt"
touch "$ROOT/boot/firmware/ssh"
if [[ -f "$PAYLOAD/authorized_keys" ]]; then
    # staged for the user systemd-firstboot will create on /home/zeitspiegel
    install -D -m0644 "$PAYLOAD/authorized_keys" "$ROOT/boot/firmware/zeitspiegel-authorized_keys"
fi

echo "==> passwordless sudo for the appliance admin (LAN-only + key-gated)"
# E-7/NFR-6: open Wi-Fi, LAN-only, no auth in v1. SSH is key-only. A sudo
# password on top adds no defense — anyone with the key + LAN can already
# fully own the device — but losing the bake-time random password turns a
# debug session into a re-flash. Trade the password for ergonomics.
install -d -m0755 "$ROOT/etc/sudoers.d"
printf 'zeitspiegel ALL=(ALL) NOPASSWD: ALL\n' \
    > "$ROOT/etc/sudoers.d/010-zeitspiegel-nopasswd"
chmod 0440 "$ROOT/etc/sudoers.d/010-zeitspiegel-nopasswd"

echo "==> Wi-Fi access point profile (open network, NetworkManager keyfile)"
install -d -m0700 "$ROOT/etc/NetworkManager/system-connections"
cat > "$ROOT/etc/NetworkManager/system-connections/zeitspiegel-ap.nmconnection" <<EOF
[connection]
id=zeitspiegel-ap
type=wifi
interface-name=wlan0
autoconnect=true
autoconnect-priority=100

[wifi]
mode=ap
band=bg
channel=6
ssid=${AP_SSID}

[ipv4]
method=shared

[ipv6]
method=disabled
EOF
chmod 600 "$ROOT/etc/NetworkManager/system-connections/zeitspiegel-ap.nmconnection"

# Stock Pi OS ships with the Wi-Fi radio soft-blocked via a saved rfkill
# state file ("disabled by state file" in NM's log). With our regdom set
# the radio is supposed to be free; clear the saved state so NM can bring
# the AP up at first boot instead of seeing the radio as killed.
echo "==> clear stock rfkill saved soft-block state"
rm -f "$ROOT/var/lib/systemd/rfkill/"*:wlan 2>/dev/null || true

echo "==> persistent journal (post-mortem debug across reboots)"
# /var/log/journal existing flips systemd-journald from volatile to
# persistent storage. NFR-8 traded the "tiny disk writes" story for being
# able to debug a no-AP appliance without attaching a screen — this whole
# script is the receipt for that trade.
install -d -m2755 "$ROOT/var/log/journal"

echo "==> Wi-Fi regulatory domain (${WIFI_COUNTRY}) via kernel cmdline"
CMD="$ROOT/boot/firmware/cmdline.txt"
grep -q ieee80211_regdom "$CMD" || sed -i "1 s|\$| cfg80211.ieee80211_regdom=${WIFI_COUNTRY}|" "$CMD"

echo "==> kiosk: silent boot, no login prompt (FR-12)"
# No getty login prompt on the HDMI console — the mirror is the only thing shown.
chroot "$ROOT" systemctl mask getty@tty1.service >/dev/null 2>&1 || true
chroot "$ROOT" systemctl disable getty@tty1.service >/dev/null 2>&1 || true
# Quiet the boot text and hide the console cursor (idempotent, single line).
read -r KLINE < "$CMD"
for t in quiet loglevel=3 logo.nologo vt.global_cursor_default=0 consoleblank=0; do
    case " $KLINE " in *" $t "*) ;; *) KLINE="$KLINE $t" ;; esac
done
printf '%s\n' "$KLINE" > "$CMD"
# Disable the rainbow splash screen.
CFG="$ROOT/boot/firmware/config.txt"
grep -q '^disable_splash=1' "$CFG" 2>/dev/null || echo 'disable_splash=1' >> "$CFG"

echo "==> reclaim space + restore resolv.conf"
chroot "$ROOT" apt-get clean
rm -f "$ROOT/etc/resolv.conf"
[[ "$HADRES" == yes ]] && mv "$ROOT/etc/resolv.conf.zsbak" "$ROOT/etc/resolv.conf"

sync
echo "==> baked: $OUT"
