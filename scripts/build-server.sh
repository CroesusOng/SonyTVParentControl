#!/bin/bash
# Cross-build the backend for a Raspberry Pi 5 from a Mac, and optionally ship it over.
#
#   ./scripts/build-server.sh                 # just build into server/dist/
#   ./scripts/build-server.sh pi@pi5.local    # build, copy, and print what to run there
#
# Building here rather than on the Pi keeps the Pi from needing a Go toolchain, and a
# cross-build of a CGO-free binary is byte-for-byte as good as a native one.
set -euo pipefail

cd "$(dirname "$0")/.."
OUT=server/dist
mkdir -p "$OUT"

echo "==> building linux/arm64"
(cd server && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags="-s -w" -o "../$OUT/braviaguardd" ./cmd/braviaguardd)

ls -lh "$OUT/braviaguardd"
file "$OUT/braviaguardd" 2>/dev/null || true

if [ $# -ge 1 ]; then
  TARGET="$1"
  echo "==> copying to $TARGET"
  scp "$OUT/braviaguardd" "$TARGET:/tmp/braviaguardd"
  scp server/deploy/braviaguard.service "$TARGET:/tmp/braviaguard.service"
  scp server/deploy/avahi-braviaguard.service "$TARGET:/tmp/avahi-braviaguard.service"
  cat <<'NEXT'

==> now, on the Pi:

  sudo install -m755 /tmp/braviaguardd /usr/local/bin/braviaguardd
  sudo install -m644 /tmp/braviaguard.service /etc/systemd/system/
  sudo install -m644 /tmp/avahi-braviaguard.service /etc/avahi/services/braviaguard.service
  sudoedit /etc/systemd/system/braviaguard.service     # set BG_ORIGINS and BG_TZ
  sudo systemctl daemon-reload && sudo systemctl enable --now braviaguard
  sudo systemctl reload avahi-daemon
  journalctl -u braviaguard -n 40                      # read the SETUP CODE from here
NEXT
fi
