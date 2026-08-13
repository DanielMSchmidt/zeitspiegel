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
# 2.4 GHz channel 6 by default for maximum device compatibility. With several
# units in one room, band=a on a non-DFS channel (36) gives clip downloads a
# lot more headroom -- worth measuring on site before switching.
AP_BAND="${AP_BAND:-bg}"
AP_CHANNEL="${AP_CHANNEL:-6}"
GROW_MB="${GROW_MB:-1500}"
SRC_XZ=/work/raspios.img.xz
# SEAL=0 bakes a development card: everything identical except that the
# first-boot seal never runs, so the root stays writable and the persistent
# journal at /var/log/journal actually persists across reboots. A sealed card
# sends every write after the seal to tmpfs, which is why a field unit can only
# ever hand back its first boot (NFR-8/NFR-9). OUT_NAME keeps the two images
# apart so a dev bake never gets flashed in place of a production one.
SEAL="${SEAL:-1}"
OUT="/work/${OUT_NAME:-zeitspiegel-appliance.img}"
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
    network-manager dnsmasq-base iptables rfkill iw avahi-utils >/dev/null
# dnsmasq-base + iptables: required by NM's `ipv4.method=shared` AP profile
# (DHCP to clients + NAT rules). rfkill + iw: lightweight tools for in-place
# debugging when the appliance won't broadcast, and `iw station dump` is how
# a host counts its audience (D8). avahi-utils: avahi-set-host-name is the
# only way to rename a running Avahi -- the daemon does not follow kernel
# hostname changes, and without the rename zeitspiegel.local would not move
# to the new host after a failover.

echo "==> install zeitspiegel binary / config / unit"
install -D -m0755 "$PAYLOAD/zeitspiegel"          "$ROOT/usr/local/bin/zeitspiegel"
install -D -m0644 "$PAYLOAD/config.toml"          "$ROOT/etc/zeitspiegel/config.toml"
install -D -m0644 "$PAYLOAD/zeitspiegel.service"  "$ROOT/etc/systemd/system/zeitspiegel.service"
install -D -m0755 "$PAYLOAD/seal.sh"              "$ROOT/usr/local/sbin/zeitspiegel-seal"
install -D -m0644 "$PAYLOAD/zeitspiegel-seal.service" "$ROOT/etc/systemd/system/zeitspiegel-seal.service"
chroot "$ROOT" systemctl enable NetworkManager  >/dev/null 2>&1 || true
chroot "$ROOT" systemctl enable zeitspiegel.service      >/dev/null
if [[ "$SEAL" == 1 ]]; then
    chroot "$ROOT" systemctl enable zeitspiegel-seal.service >/dev/null
else
    # The unit and seal.sh stay installed either way: a dev card can be sealed
    # by hand later (systemctl enable zeitspiegel-seal && reboot) without
    # rebuilding it.
    chroot "$ROOT" systemctl disable zeitspiegel-seal.service >/dev/null 2>&1 || true
    echo "==> DEV IMAGE: first-boot seal NOT enabled — root stays writable, journal persists"
fi

echo "==> install + enable boot-time profile capture (→ /boot/firmware/zeitspiegel-boot-profile.log)"
install -D -m0755 "$PAYLOAD/zeitspiegel-boot-profile.sh" \
    "$ROOT/usr/local/sbin/zeitspiegel-boot-profile"
install -D -m0644 "$PAYLOAD/zeitspiegel-boot-profile.service" \
    "$ROOT/etc/systemd/system/zeitspiegel-boot-profile.service"
install -D -m0644 "$PAYLOAD/zeitspiegel-boot-profile.timer" \
    "$ROOT/etc/systemd/system/zeitspiegel-boot-profile.timer"
# Enable the timer, not the service — the service is not in any
# target's wants graph (would deadlock multi-user.target). The timer
# fires it once, 30s after boot, by which point FinishTimestamp is set.
chroot "$ROOT" systemctl enable zeitspiegel-boot-profile.timer >/dev/null
# Belt-and-suspenders: if a previous bake left the .service enabled,
# disable it so we don't double-fire.
chroot "$ROOT" systemctl disable zeitspiegel-boot-profile.service >/dev/null 2>&1 || true

echo "==> install boot-time diagnostic capture (3 stages, on-demand → /boot/firmware/zeitspiegel-debug.log)"
# The rfkill investigation is closed: AP bring-up verified 2026-04-21,
# root cause fixed deterministically at bake (NetworkManager.state +
# rfkill-unblock oneshot below). All three debug stages stay installed
# for field debugging but are NOT enabled — pre/post-rfkill cost ≈150 ms
# plus two synced FAT32 writes on every boot, and -late adds 20 s via
# its ExecStartPre=/bin/sleep 20. Run on demand (SSH escape hatch):
# `systemctl start zeitspiegel-debug-<stage>.service`.
install -D -m0755 "$PAYLOAD/zeitspiegel-debug.sh" "$ROOT/usr/local/sbin/zeitspiegel-debug"
for u in zeitspiegel-debug-pre-rfkill zeitspiegel-debug-post-rfkill zeitspiegel-debug-late; do
    install -D -m0644 "$PAYLOAD/${u}.service" "$ROOT/etc/systemd/system/${u}.service"
    chroot "$ROOT" systemctl disable "${u}.service" >/dev/null 2>&1 || true
done

echo "==> hostname + mDNS (zeitspiegel.local)"
# The baked name is what a lone appliance keeps, and what every unit answers
# to for the few seconds before it knows its role. Once elected, the unit
# hosting the network keeps "zeitspiegel" and the others rename themselves to
# zeitspiegel-<unit id> so mDNS does not collide -- transiently, because
# /etc/hostname is read-only once the overlay is sealed.
echo zeitspiegel > "$ROOT/etc/hostname"
sed -i 's/127\.0\.1\.1.*/127.0.1.1\tzeitspiegel/' "$ROOT/etc/hosts" 2>/dev/null \
    || printf '127.0.1.1\tzeitspiegel\n' >> "$ROOT/etc/hosts"

echo "==> admin user + ssh (Pi OS userconf mechanism)"
# userconf.txt is the supported headless way to create the first user on
# first boot; it also satisfies the Bookworm/Trixie first-run user gate.
printf 'zeitspiegel:%s\n' "$ADMIN_HASH" > "$ROOT/boot/firmware/userconf.txt"
# SSH is not enabled: this appliance is read by pulling the SD card, not
# by ssh'ing in (E-7 + user preference 2026-04-21). authorized_keys is
# still staged so an emergency `touch /boot/firmware/ssh` on the FAT32
# partition turns ssh back on in one boot without rebuilding the image.
if [[ -f "$PAYLOAD/authorized_keys" ]]; then
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

echo "==> NetworkManager: trim for an AP-only appliance (no internet, no DNS)"
# - connectivity check pings http://connectivity-check.ubuntu.com to
#   distinguish "connected" from "captive portal". We have no upstream;
#   the check times out and wastes time. Disable it.
# - We don't run a DNS resolver on the device. NM defaults to managing
#   /etc/resolv.conf via systemd-resolved or a similar plugin — `none`
#   skips that whole code path.
# - plugins=keyfile only. We ship one .nmconnection in
#   /etc/NetworkManager/system-connections; no ifupdown plugin needed.
install -d -m0755 "$ROOT/etc/NetworkManager/conf.d"
cat > "$ROOT/etc/NetworkManager/conf.d/00-zeitspiegel.conf" <<'NMCONF'
[main]
plugins=keyfile
dns=none
no-auto-default=*

[connectivity]
enabled=false
NMCONF
chmod 0644 "$ROOT/etc/NetworkManager/conf.d/00-zeitspiegel.conf"

echo "==> Wi-Fi profiles: host the network, or join it (open, NetworkManager keyfiles)"
# Both profiles have autoconnect=false on purpose. The appliance elects its
# own role at boot -- host the shared network or join it (E-8, FR-15) -- and
# activates whichever profile it decided on. Letting NetworkManager
# autoconnect as well would race that decision, and a unit that lost the race
# would come up beaconing a network somebody else is already hosting.
install -d -m0700 "$ROOT/etc/NetworkManager/system-connections"
cat > "$ROOT/etc/NetworkManager/system-connections/zeitspiegel-ap.nmconnection" <<EOF
[connection]
id=zeitspiegel-ap
type=wifi
interface-name=wlan0
autoconnect=false

[wifi]
mode=ap
band=${AP_BAND}
channel=${AP_CHANNEL}
ssid=${AP_SSID}

[ipv4]
method=shared

[ipv6]
method=disabled
EOF
chmod 600 "$ROOT/etc/NetworkManager/system-connections/zeitspiegel-ap.nmconnection"

# Station profile: join the network somebody else is already hosting.
# An OPEN network gets no [wifi-security] section at all -- that is what
# nmcli writes for one. key-mgmt=none is the ambiguous spelling
# NetworkManager documents as "WEP or no password protection", and using it
# here makes the association fail in a way that is tedious to diagnose.
cat > "$ROOT/etc/NetworkManager/system-connections/zeitspiegel-sta.nmconnection" <<EOF
[connection]
id=zeitspiegel-sta
type=wifi
interface-name=wlan0
autoconnect=false

[wifi]
mode=infrastructure
ssid=${AP_SSID}
# 2 = disable Wi-Fi powersave. Pi OS leaves it on by default, and a member
# unit is an HTTP server on this link (1 Hz status polls from every open
# page, whole clip downloads) -- the documented failure mode is added
# latency and the link dropping offline after hours. Wall power, nothing
# to save.
powersave=2

[ipv4]
method=auto

[ipv6]
method=disabled
EOF
chmod 600 "$ROOT/etc/NetworkManager/system-connections/zeitspiegel-sta.nmconnection"

# Primary evidence from the previous bake's zeitspiegel-debug.log: Pi OS
# Lite Trixie on Pi 5 has TWO independent gates blocking the AP, both
# need addressing:
#   (a) kernel/driver brings up the WiFi radio soft-blocked at every boot
#       (/sys/class/rfkill/rfkill1/soft = 1 captured at uptime 3.32s
#       before any userland could have intervened) and nothing in the
#       default boot path clears it. → install + enable a oneshot that
#       runs `rfkill unblock all` Before=NetworkManager.service.
#   (b) NM's own enable gate /var/lib/NetworkManager/NetworkManager.state
#       ships with `WirelessEnabled=false` (Pi OS expects raspi-config or
#       Imager to flip this; we don't run either). → overwrite at bake.
# Also mask systemd-rfkill so the kernel state we set doesn't get
# re-saved/re-restored across reboots, and wipe its stale cache.
echo "==> wifi: clear stale rfkill saved state + mask systemd-rfkill"
rm -rf "$ROOT/var/lib/systemd/rfkill"
install -d -m0755 "$ROOT/var/lib/systemd/rfkill"
chroot "$ROOT" systemctl mask systemd-rfkill.service systemd-rfkill.socket \
    >/dev/null 2>&1 || true

echo "==> wifi: NetworkManager.state — flip WirelessEnabled=false → true"
install -d -m0700 "$ROOT/var/lib/NetworkManager"
cat > "$ROOT/var/lib/NetworkManager/NetworkManager.state" <<'NMS'
[main]
NetworkingEnabled=true
WirelessEnabled=true
WWANEnabled=true
NMS
chmod 0600 "$ROOT/var/lib/NetworkManager/NetworkManager.state"

echo "==> wifi: install + enable rfkill-unblock oneshot (runs before NM)"
install -D -m0644 "$PAYLOAD/zeitspiegel-rfkill-unblock.service" \
    "$ROOT/etc/systemd/system/zeitspiegel-rfkill-unblock.service"
chroot "$ROOT" systemctl enable zeitspiegel-rfkill-unblock.service >/dev/null

echo "==> persistent journal (post-mortem debug across reboots)"
# /var/log/journal existing flips systemd-journald from volatile to
# persistent storage. NFR-8 traded the "tiny disk writes" story for being
# able to debug a no-AP appliance without attaching a screen — this whole
# script is the receipt for that trade.
install -d -m2755 "$ROOT/var/log/journal"

echo "==> Wi-Fi regulatory domain (${WIFI_COUNTRY}) via kernel cmdline"
CMD="$ROOT/boot/firmware/cmdline.txt"
grep -q ieee80211_regdom "$CMD" || sed -i "1 s|\$| cfg80211.ieee80211_regdom=${WIFI_COUNTRY}|" "$CMD"

echo "==> trim: mask services this appliance doesn't use (boot time + RAM)"
# Sealed single-purpose appliance: open Wi-Fi AP, no Bluetooth, no
# keyboard, no swap, no apt updates (read-only root), no upstream NTP.
# Masking — not just disabling — also blocks units pulled in as deps,
# which is what eats seconds during boot. systemctl mask on an
# already-absent unit just creates a /dev/null symlink, so this list is
# safe to apply to a stock Pi OS Lite image without probing first.
for u in \
    bluetooth.service hciuart.service \
    ModemManager.service \
    triggerhappy.service triggerhappy.socket \
    keyboard-setup.service console-setup.service \
    apt-daily.timer apt-daily-upgrade.timer \
    apt-daily.service apt-daily-upgrade.service \
    man-db.timer man-db.service \
    dphys-swapfile.service \
    systemd-timesyncd.service \
    rpi-eeprom-update.service \
    e2scrub_all.timer e2scrub_reap.service \
    cloud-init.service cloud-init-local.service \
    cloud-init-main.service cloud-init-network.service \
    cloud-config.service cloud-final.service \
    alsa-restore.service alsa-state.service \
    systemd-zram-setup@zram0.service \
    rpi-resize-swap-file.service rpi-setup-loop@var-swap.service \
    ssh.service sshswitch.service \
    NetworkManager-dispatcher.service NetworkManager-wait-online.service \
; do
    chroot "$ROOT" systemctl mask "$u" >/dev/null 2>&1 || true
done

echo "==> kiosk: silent boot, no login prompt (FR-12)"
# No getty login prompt on the HDMI console — the mirror is the only thing shown.
chroot "$ROOT" systemctl mask getty@tty1.service >/dev/null 2>&1 || true
chroot "$ROOT" systemctl disable getty@tty1.service >/dev/null 2>&1 || true
# Quiet the boot text and hide the console cursor (idempotent, single line).
# loglevel=0: with the old loglevel=3, err-level kernel chatter (brcmfmac,
# vc4) still flashed on the HDMI console during boot. The persistent
# journal keeps every message (journald reads /dev/kmsg regardless of the
# console loglevel), so nothing is lost for post-mortems.
# systemd.show_status=0: `quiet` alone leaves show-status on "auto",
# which still paints [FAILED]/degraded lines on the console.
read -r KLINE < "$CMD"
KLINE="${KLINE// loglevel=3/}"
for t in quiet loglevel=0 systemd.show_status=0 udev.log_level=3 logo.nologo vt.global_cursor_default=0 consoleblank=0 fastboot; do
    case " $KLINE " in *" $t "*) ;; *) KLINE="$KLINE $t" ;; esac
done
printf '%s\n' "$KLINE" > "$CMD"
CFG="$ROOT/boot/firmware/config.txt"
# Disable the rainbow splash screen.
grep -q '^disable_splash=1' "$CFG" 2>/dev/null || echo 'disable_splash=1' >> "$CFG"
# Skip firmware probing for hardware this appliance never has: CSI
# cameras (ours is USB), DSI displays (ours is HDMI), analog audio.
sed -i 's/^camera_auto_detect=1/camera_auto_detect=0/; s/^display_auto_detect=1/display_auto_detect=0/; s/^dtparam=audio=on/dtparam=audio=off/' "$CFG"
# Drop the Bluetooth node from the device tree entirely — bluetooth.service
# and hciuart are already masked, but without the overlay the kernel still
# initializes the radio during boot.
grep -q '^dtoverlay=disable-bt' "$CFG" 2>/dev/null || echo 'dtoverlay=disable-bt' >> "$CFG"

echo "==> reclaim space + restore resolv.conf"
chroot "$ROOT" apt-get clean
rm -f "$ROOT/etc/resolv.conf"
[[ "$HADRES" == yes ]] && mv "$ROOT/etc/resolv.conf.zsbak" "$ROOT/etc/resolv.conf"

sync
echo "==> baked: $OUT"
