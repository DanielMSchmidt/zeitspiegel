GO ?= go
BIN := bin/zeitspiegel

.PHONY: test test-integration test-hw build build-pi pi-binary sd build-tv run-synth run-tv manual-test vet clean

test: vet
	$(GO) test -race ./...

test-integration:
	$(GO) vet -tags integration ./...
	$(GO) test -race -tags integration ./...

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

# Flash + stage a self-provisioning SD card (macOS). See scripts/make-sd.sh.
sd: pi-binary
	./scripts/make-sd.sh

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
