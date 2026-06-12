GO ?= go
BIN := bin/zeitspiegel

.PHONY: test test-integration test-hw build build-pi run-synth manual-test vet clean

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

run-synth: build
	./$(BIN) --source synth

# Build + boot synth mode + open the web UI; see docs/MANUAL_TESTING.md.
manual-test:
	./scripts/manual-test.sh

clean:
	rm -rf bin
