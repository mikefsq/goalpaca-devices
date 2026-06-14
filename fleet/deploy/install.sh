#!/usr/bin/env bash
# install.sh — install astrofleet as a systemd service on Linux (e.g. a CM5).
#
# Usage (run from the fleet/ directory, as root):
#   sudo deploy/install.sh [path-to-astrofleet-binary]
#
# Defaults to ./astrofleet. Build one first, e.g. for a 64-bit Pi:
#   CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o astrofleet .
set -euo pipefail

BIN_SRC="${1:-./astrofleet}"
BIN_DST=/usr/local/bin/astrofleet
CONF_DIR=/etc/astrofleet
CONF_DST="$CONF_DIR/fleet.json"
UNIT_DST=/etc/systemd/system/astrofleet.service
RULES_DST=/etc/udev/rules.d/99-astrofleet.rules
HERE="$(cd "$(dirname "$0")/.." && pwd)" # fleet/ root

if [[ $EUID -ne 0 ]]; then
	echo "error: run as root (sudo deploy/install.sh ...)" >&2
	exit 1
fi
if [[ ! -f "$BIN_SRC" ]]; then
	echo "error: binary '$BIN_SRC' not found — build it first (see header)" >&2
	exit 1
fi

echo "installing binary -> $BIN_DST"
install -m 0755 "$BIN_SRC" "$BIN_DST"

mkdir -p "$CONF_DIR"
if [[ -f "$CONF_DST" ]]; then
	echo "keeping existing config $CONF_DST"
else
	install -m 0644 "$HERE/config/fleet.example.json" "$CONF_DST"
	echo "installed starter config -> $CONF_DST   *** EDIT THIS for your hardware ***"
fi

echo "installing unit -> $UNIT_DST"
install -m 0644 "$HERE/deploy/astrofleet.service" "$UNIT_DST"

echo "installing udev rules -> $RULES_DST"
install -m 0644 "$HERE/deploy/99-astrofleet.rules" "$RULES_DST"
udevadm control --reload && udevadm trigger
echo "  (replug USB cameras/devices so the new permissions apply)"

systemctl daemon-reload
systemctl enable --now astrofleet.service
echo
systemctl --no-pager --full status astrofleet.service || true
echo
echo "done. edit $CONF_DST then: sudo systemctl restart astrofleet"
echo "logs: journalctl -u astrofleet -f"
