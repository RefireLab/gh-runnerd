#!/usr/bin/env bash
# Install gh-runnerd + gh-runnerd-guest from the latest GitHub Release.
# This is only the Go binaries. You still need Ubuntu 24.04, KVM/QEMU,
# and a baked ubuntu-24.04-*.qcow2 runner image.
set -euo pipefail

REPO="${REPO:-RefireLab/gh-runnerd}"
DEST="${DEST:-/usr/local/bin}"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  SUFFIX=linux-amd64 ;;
  aarch64) SUFFIX=linux-arm64 ;;
  *) echo "unsupported arch $ARCH" >&2; exit 1 ;;
esac

if [[ "$(id -u)" -ne 0 ]]; then
  echo "run as root: sudo $0" >&2
  exit 1
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
if ! curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" -o "$tmp"; then
  echo "no GitHub Release yet. Clone the repo and run: make build && sudo install -m 0755 bin/gh-runnerd bin/gh-runnerd-guest ${DEST}" >&2
  exit 1
fi

url_for() {
  python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); name=sys.argv[2];
print(next(a["browser_download_url"] for a in d.get("assets", []) if a["name"]==name))' "$tmp" "$1"
}

mkdir -p "$DEST"
curl -fL --retry 3 -o "$DEST/gh-runnerd" "$(url_for "gh-runnerd-${SUFFIX}")"
curl -fL --retry 3 -o "$DEST/gh-runnerd-guest" "$(url_for "gh-runnerd-guest-${SUFFIX}")"
chmod 0755 "$DEST/gh-runnerd" "$DEST/gh-runnerd-guest"
echo "installed $DEST/gh-runnerd and $DEST/gh-runnerd-guest"
echo "still required: qemu-system, /dev/kvm, and a baked Ubuntu runner qcow2 (see README)"
