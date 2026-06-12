#!/usr/bin/env bash
# Manual end-to-end test harness: build, boot in synth mode, pre-set a 2 s
# delay, open the web UI. Walk docs/MANUAL_TESTING.md from there.
#
#   ./scripts/manual-test.sh               # build + run + open browser
#   SOURCE=camera ./scripts/manual-test.sh # your real webcam (macOS: grant
#                                          # camera permission on first run)
#   TV=1 ./scripts/manual-test.sh          # ALSO open the real display path
#                                          # (SDL) in a desktop window — what
#                                          # the connected TV would show
#   PORT=9090 ./scripts/manual-test.sh     # different port
#   NO_BROWSER=1 ./scripts/manual-test.sh
#
# Ctrl-C stops the binary (clean SIGINT shutdown path).
set -euo pipefail
cd "$(dirname "$0")/.."

PORT="${PORT:-8080}"
SOURCE="${SOURCE:-synth}"
ADDR="127.0.0.1:${PORT}"
URL="http://${ADDR}"

command -v go >/dev/null || { echo "error: go not found" >&2; exit 1; }
if ! command -v ffmpeg >/dev/null || ! command -v ffprobe >/dev/null; then
  echo "warning: ffmpeg/ffprobe not found — clip download (MT-3..MT-5) will fail" >&2
fi

BIN=./bin/zeitspiegel
ARGS=()
if [ -n "${TV:-}" ]; then
  echo "==> building TV view (sdl tag; needs SDL2 + SDL2_image + pkg-config)"
  make --silent build-tv
  BIN=./bin/zeitspiegel-tv
  ARGS+=(--windowed)
else
  echo "==> building"
  make --silent build
fi

echo "==> starting zeitspiegel (${SOURCE} source) on ${URL}"
"${BIN}" --source "${SOURCE}" --bind "${ADDR}" ${ARGS[@]+"${ARGS[@]}"} &
PID=$!
trap 'echo; echo "==> stopping"; kill "${PID}" 2>/dev/null; wait "${PID}" 2>/dev/null || true' EXIT INT TERM

[ "${SOURCE}" = "camera" ] && [ "$(uname)" = "Darwin" ] && \
  echo "    (macOS: if the buffer stays empty, grant your terminal Camera access in System Settings → Privacy)"

for _ in $(seq 1 50); do
  curl -sf "${URL}/healthz" >/dev/null 2>&1 && break
  kill -0 "${PID}" 2>/dev/null || { echo "error: binary exited early" >&2; exit 1; }
  sleep 0.2
done
curl -sf "${URL}/healthz" >/dev/null || { echo "error: not healthy after 10s" >&2; exit 1; }

# Pre-set a 2 s delay so the delayed view differs visibly from live right away.
curl -sf -X PUT -d '{"seconds": 2.0}' "${URL}/api/v1/delay" >/dev/null

cat <<EOF

  Zeitspiegel is up:   ${URL}
  Checklist:           docs/MANUAL_TESTING.md  (MT-1 .. MT-7)

  Quick start: press "Start preview", switch the view to "delayed mirror" —
  the moving bar lags 2 s behind the "live camera" view.

EOF

if [ -z "${NO_BROWSER:-}" ]; then
  case "$(uname)" in
    Darwin) open "${URL}" ;;
    Linux)  xdg-open "${URL}" >/dev/null 2>&1 || true ;;
  esac
fi

echo "==> running (Ctrl-C to stop); binary log follows"
wait "${PID}"
