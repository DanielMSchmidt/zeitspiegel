GO ?= go
GOFMT ?= gofmt
BIN := bin/zeitspiegel

# The build's identity. Every card ships the byte-identical image (E-8), so a
# unit cannot be asked which build it runs — the binary has to carry it, and
# the bake stamps the same value onto the boot partition where a pulled card
# can be read without booting it. Override for a reproducible build:
# VERSION=v1.4.2 make image
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo unknown)
LDFLAGS := -X main.version=$(VERSION)

# The container both halves of `make image` run in — the cross-build and the
# bake. Built once by `make builder-image` (see deploy/builder.Dockerfile);
# after that nothing in the card path installs a package.
BUILDER_IMAGE ?= zeitspiegel-builder:trixie-arm64

# Everything the bake downloads is kept here: the Pi OS base image, the .debs
# the chroot installs, and the Go module and build caches. Warm it once with
# `make warm-cache`, then a card can be made with the network off:
#
#   OFFLINE=1 make sd NAME="Long Side"
#
# OFFLINE=1 does not merely avoid the network — it refuses to reach for it, so
# a cold cache is a refusal at the start rather than a stall in the middle.
CACHE_DIR ?= $(CURDIR)/build/cache
OFFLINE ?= 0

.PHONY: test test-integration test-e2e test-hw test-ui build build-pi pi-binary builder-image warm-cache check-caches image image-dev sd sd-dev sd-logs check-name build-tv run-synth run-tv manual-test vet fmt-check clean poster poster-test poster-check

test: vet
	$(GO) test -race ./...

test-integration:
	$(GO) vet -tags integration ./...
	$(GO) test -race -tags integration ./...

# Multi-unit end-to-end (ST-7..ST-12): three real zeitspiegel processes elect
# a role among themselves over real HTTP, with only the radio faked. Needs no
# ffmpeg, no radios and no root, so it runs on a laptop.
test-e2e:
	$(GO) vet -tags e2e ./...
	$(GO) test -race -tags e2e -run 'TestFleet|TestLoneUnit' ./cmd/zeitspiegel/

# UI unit tests (UI-1..UI-10): the control page in headless Chromium with the
# API stubbed in the browser's network layer — no binary, no camera, no ffmpeg.
# Needs Node and Playwright: `cd web/uitest && npm install && npx playwright
# install chromium`, or a global playwright with NODE_PATH set. The page ships
# with no build tooling; this is test-only (ARCHITECTURE §D5).
test-ui:
	cd web/uitest && node --test

# Linux only: needs SDL2/SDL2_image headers, V4L2 kernel headers, v4l2loopback for ST-1.
test-hw:
	$(GO) vet -tags "v4l2 sdl" ./...
	$(GO) build -tags "v4l2 sdl" -o $(BIN) ./cmd/zeitspiegel
	$(GO) test -race -tags "v4l2 sdl" ./...

vet: fmt-check
	$(GO) vet ./...

# go vet does not check formatting, so drift sat unnoticed until something
# else touched the file — longest in the files behind build tags, which most
# tooling never compiles. gofmt reads every .go file regardless of its tags,
# which is exactly why the check belongs here rather than in `go vet ./...`.
fmt-check:
	@drift=$$($(GOFMT) -l ./cmd ./internal ./web); \
	if [ -n "$$drift" ]; then \
	  echo "gofmt: these files are not formatted:" >&2; \
	  echo "$$drift" | sed 's/^/  /' >&2; \
	  echo "fix with: $(GOFMT) -w ./cmd ./internal ./web" >&2; \
	  exit 1; \
	fi

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/zeitspiegel

# Native build on the Pi (or cross with CC=<aarch64 cc> set, e.g. zig cc target).
build-pi:
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 $(GO) build -tags "v4l2 sdl" -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/zeitspiegel

# Build the container the card path runs in. Idempotent and cheap once it
# exists — every target that needs it depends on this, so nobody has to
# remember to run it. It is the only step in the whole path that downloads
# anything a second bake would download again.
builder-image:
	@docker image inspect $(BUILDER_IMAGE) >/dev/null 2>&1 || { \
	  [ "$(OFFLINE)" != 1 ] || { \
	    echo 'error: builder image $(BUILDER_IMAGE) is missing, and OFFLINE=1 forbids building it' >&2; \
	    echo '       run `make warm-cache` once with a network' >&2; exit 1; }; \
	  echo "==> building $(BUILDER_IMAGE) (once)"; \
	  docker build --platform linux/arm64 -t $(BUILDER_IMAGE) - < deploy/builder.Dockerfile; }

# Pi binary cross-built in Docker against Debian trixie (= current Pi OS
# userland; bookworm's 6.1 kernel headers are too old for go4vl), arm64 —
# runs natively on Apple Silicon.
#
# The module and build caches are mounted from the host: without them every
# bake re-downloads the four modules and recompiles the arm64 standard library
# inside a container it then throws away.
pi-binary: builder-image
	@mkdir -p "$(CACHE_DIR)/gomod" "$(CACHE_DIR)/gobuild"
	@[ "$(OFFLINE)" != 1 ] || [ -d "$(CACHE_DIR)/gomod/cache/download" ] || { \
	  echo 'error: the Go module cache is cold — run `make warm-cache` once with a network' >&2; exit 1; }
	docker run --rm --platform linux/arm64 -v "$(CURDIR)":/src -w /src \
	  -v "$(CACHE_DIR)/gomod":/go/pkg/mod -v "$(CACHE_DIR)/gobuild":/root/.cache/go-build \
	  -e GOFLAGS=-buildvcs=false $(if $(filter 1,$(OFFLINE)),--network none -e GOPROXY=off,) \
	  $(BUILDER_IMAGE) \
	  go build -tags 'v4l2 sdl' -ldflags '$(LDFLAGS)' -o bin/zeitspiegel-pi ./cmd/zeitspiegel

# Fill every cache the card path reads, by walking that path once with a
# network. After this, making a card needs none.
warm-cache: image
	@echo
	@$(MAKE) --no-print-directory check-caches
	@echo
	@echo 'caches are warm: a card can now be made offline —'
	@echo '  OFFLINE=1 make sd NAME="Long Side"'

# Say whether a card could be made right now with the network off. Cheap, and
# the answer belongs before the trip rather than at the venue.
check-caches:
	@cold=0; \
	if docker image inspect $(BUILDER_IMAGE) >/dev/null 2>&1; then \
	  echo "  warm  builder image $(BUILDER_IMAGE)"; \
	else \
	  echo "  COLD  builder image $(BUILDER_IMAGE) — the container the bake runs in"; cold=1; \
	fi; \
	CACHE_DIR="$(CACHE_DIR)" ./scripts/build-image.sh --check-caches || cold=1; \
	exit $$cold

# Bake a finished, network-free appliance image (no SD card needed).
image: pi-binary
	VERSION="$(VERSION)" CACHE_DIR="$(CACHE_DIR)" OFFLINE="$(OFFLINE)" BUILDER_IMAGE="$(BUILDER_IMAGE)" ./scripts/build-image.sh

# Write the baked image to an SD card (macOS) and name that unit. Plug-and-play,
# no ethernet. NAME is the label the mirror shows in the UI; it is staged onto
# the card's boot partition afterwards, so the image stays byte-identical (E-8).
#
#   make sd NAME="Long Side"       # named card
#   make sd NAME=auto          # deliberately unnamed: it calls itself Zeitspiegel <ID>
sd: check-name image
	NAME="$(NAME)" ./scripts/flash-sd.sh

# A bench card: same image, minus the first-boot seal. The root stays
# writable, so the persistent journal at /var/log/journal survives reboots and
# `make sd-logs` reads every boot off it — a sealed card can only ever hand
# back its first, because the overlay sends later writes to tmpfs (NFR-8 vs
# NFR-9). The trade is that pulling the plug can corrupt the card, so this is
# for a desk, not a venue. Its own image and credentials file, so it can never
# be flashed in place of a production card.
#
#   make sd-dev NAME="Bench"
sd-dev: check-name image-dev
	IMG=build/zeitspiegel-appliance-dev.img NAME="$(NAME)" ./scripts/flash-sd.sh

image-dev: pi-binary
	SEAL=0 VERSION="$(VERSION)" CACHE_DIR="$(CACHE_DIR)" OFFLINE="$(OFFLINE)" BUILDER_IMAGE="$(BUILDER_IMAGE)" ./scripts/build-image.sh

# Reject a missing or unusable label before the (slow) image bake, not after.
check-name:
	@[ -n "$(NAME)" ] || { \
	  echo 'error: NAME is required — e.g. make sd NAME="Long Side"  (NAME=auto for a card that names itself)' >&2; \
	  exit 1; }
	@./scripts/stage-name.sh "$(NAME)" >/dev/null

# Pull a unit's own logs off its card into one attachable zip: the FAT32
# boot-partition captures plus the persistent journal from the ext4 root.
# One call, one file — read-only, it never writes to the card.
#
#   make sd-logs                       # find the card in any reader
#   make sd-logs ARGS="--list"         # just say which disk it found
#   make sd-logs ARGS="--disk /dev/disk4"
sd-logs:
	./scripts/collect-sd-logs.sh $(ARGS)

run-synth: build
	./$(BIN) --source synth

# Build + boot synth mode + open the web UI; see docs/MANUAL_TESTING.md.
manual-test:
	./scripts/manual-test.sh

# Dev TV view: the real SDL display path in a desktop window.
# macOS: brew install sdl2 sdl2_image sdl2_ttf pkgconf.
# Linux: libsdl2-dev libsdl2-image-dev libsdl2-ttf-dev.
build-tv:
	$(GO) build -tags sdl -ldflags "$(LDFLAGS)" -o $(BIN)-tv ./cmd/zeitspiegel

run-tv: build-tv
	./$(BIN)-tv --source synth --windowed

clean:
	rm -rf bin

# Regenerate the guest posters: the bilingual one plus the German-only and
# English-only variants. The script writes both the print masters
# (deploy/poster/zeitspiegel-poster*.svg) and the site copies (site/poster*.svg);
# don't hand-edit any of the SVGs.
#
# Override PYTHON to use a different interpreter, e.g.
#   make poster PYTHON=python3   # if segno is on the system path
PYTHON ?= deploy/poster/.venv/bin/python
poster:
	$(PYTHON) deploy/poster/make-poster.py

# UT-30: translations complete, nothing overflowing the page or the margins,
# both QR codes still encoding what their captions claim.
poster-test:
	$(PYTHON) -m unittest discover -s deploy/poster

# CI guard: regenerate and fail if any checked-in copy drifted.
poster-check: poster-test
	$(PYTHON) deploy/poster/make-poster.py
	git diff --exit-code -- 'deploy/poster/*.svg' 'site/poster*.svg'
