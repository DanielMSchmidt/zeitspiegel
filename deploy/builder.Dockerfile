# The one container both halves of `make image` run in: the arm64 cross-build
# (`make pi-binary`) and the privileged bake (deploy/sd/bake.sh). It exists so
# that neither of them installs anything while a card is being made — build it
# once, and every bake after that needs no network at all.
#
#   make builder-image                             # once, needs a network
#   docker rmi zeitspiegel-builder:trixie-arm64    # to rebuild against newer packages
#
# Trixie because that is the current Pi OS userland; bookworm's 6.1 kernel
# headers are too old for go4vl. arm64 because the bake chroots into the image
# it is building — native, no qemu.
FROM golang:1.25-trixie

# Two toolsets, one image, because two containers is an implementation detail
# of the bake and not worth two Dockerfiles:
#   libsdl2*-dev  — what cmd/zeitspiegel links against under the `sdl` tag
#   the rest      — what bake.sh drives a loop-mounted image with (growpart is
#                   in cloud-guest-utils, resize2fs/e2fsck in e2fsprogs,
#                   losetup/partx in util-linux)
RUN apt-get update -qq \
 && apt-get install -y -qq --no-install-recommends \
      libsdl2-dev libsdl2-image-dev libsdl2-ttf-dev \
      xz-utils cloud-guest-utils e2fsprogs dosfstools util-linux parted \
 && rm -rf /var/lib/apt/lists/*
