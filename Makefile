GO ?= go
BIN := bin/zeitspiegel

.PHONY: test test-integration test-e2e test-hw build build-pi pi-binary image sd build-tv run-synth run-tv manual-test vet clean poster poster-test poster-check

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

# Linux only: needs SDL2/SDL2_image headers, V4L2 kernel headers, v4l2loopback for ST-1.
test-hw:
	$(GO) vet -tags "v4l2 sdl" ./...
	$(GO) build -tags "v4l2 sdl" -o $(BIN) ./cmd/zeitspiegel
	$(GO) test -race -tags "v4l2 sdl" ./...

vet:
	$(GO) vet ./...

build:
	$(GO) build -o $(BIN) ./cmd/zeitspiegel

# Native build on the Pi (or cross with CC=<aarch64 cc> set, e.g. zig cc target).
build-pi:
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 $(GO) build -tags "v4l2 sdl" -o $(BIN) ./cmd/zeitspiegel

# Pi binary cross-built in Docker against Debian trixie (= current Pi OS
# userland; bookworm's 6.1 kernel headers are too old for go4vl), arm64 —
# runs natively on Apple Silicon.
pi-binary:
	docker run --rm --platform linux/arm64 -v "$(CURDIR)":/src -w /src \
	  -e GOFLAGS=-buildvcs=false golang:1.25-trixie bash -c \
	  "apt-get update -qq >/dev/null && apt-get install -y -qq libsdl2-dev libsdl2-image-dev >/dev/null \
	   && go build -tags 'v4l2 sdl' -o bin/zeitspiegel-pi ./cmd/zeitspiegel"

# Bake a finished, network-free appliance image (no SD card needed).
image: pi-binary
	./scripts/build-image.sh

# Write the baked image to an SD card (macOS). Plug-and-play, no ethernet.
sd: image
	./scripts/flash-sd.sh

run-synth: build
	./$(BIN) --source synth

# Build + boot synth mode + open the web UI; see docs/MANUAL_TESTING.md.
manual-test:
	./scripts/manual-test.sh

# Dev TV view: the real SDL display path in a desktop window.
# macOS: brew install sdl2 sdl2_image pkgconf. Linux: libsdl2-dev libsdl2-image-dev.
build-tv:
	$(GO) build -tags sdl -o $(BIN)-tv ./cmd/zeitspiegel

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

# UT-28: translations complete, nothing overflowing the page or the margins,
# both QR codes still encoding what their captions claim.
poster-test:
	$(PYTHON) -m unittest discover -s deploy/poster

# CI guard: regenerate and fail if any checked-in copy drifted.
poster-check: poster-test
	$(PYTHON) deploy/poster/make-poster.py
	git diff --exit-code -- 'deploy/poster/*.svg' 'site/poster*.svg'
