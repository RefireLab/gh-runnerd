#!/usr/bin/env bash
# Install gh-runnerd + gh-runnerd-guest from the latest GitHub Release tar.gz.
# This is only the Go binaries. You still need Ubuntu 24.04, KVM/QEMU,
# and a baked ubuntu-24.04-*.qcow2 runner image.
set -euo pipefail

REPO="${REPO:-RefireLab/gh-runnerd}"
DEST="${DEST:-/usr/local/bin}"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  SUFFIX=linux_amd64 ;;
  aarch64) SUFFIX=linux_arm64 ;;
  *) echo "unsupported arch $ARCH" >&2; exit 1 ;;
esac

if [[ "$(id -u)" -ne 0 ]]; then
  echo "run as root: sudo $0" >&2
  exit 1
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

if ! curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" -o "$tmpdir/release.json"; then
  echo "no GitHub Release found for ${REPO}." >&2
  echo "fallback: clone the repo, run make build, then:" >&2
  echo "  sudo install -m 0755 bin/gh-runnerd bin/gh-runnerd-guest ${DEST}" >&2
  exit 1
fi

URL="$(python3 -c '
import json, sys
data = json.load(open(sys.argv[1]))
suffix = sys.argv[2]
for a in data.get("assets", []):
    name = a.get("name", "")
    if name.startswith("gh-runnerd_") and name.endswith(f"_{suffix}.tar.gz"):
        print(a["browser_download_url"])
        sys.exit(0)
sys.exit(1)
' "$tmpdir/release.json" "$SUFFIX")" || {
  echo "no gh-runnerd_*_${SUFFIX}.tar.gz asset in the latest release" >&2
  exit 1
}

curl -fL --retry 3 -o "$tmpdir/gh-runnerd.tar.gz" "$URL"
tar -xzf "$tmpdir/gh-runnerd.tar.gz" -C "$tmpdir"

for bin in gh-runnerd gh-runnerd-guest; do
  if [[ ! -f "$tmpdir/$bin" ]]; then
    echo "archive is missing $bin" >&2
    exit 1
  fi
done

mkdir -p "$DEST"
install -m 0755 "$tmpdir/gh-runnerd" "$tmpdir/gh-runnerd-guest" "$DEST/"
echo "installed $DEST/gh-runnerd and $DEST/gh-runnerd-guest ($("$DEST/gh-runnerd" --version))"
echo "still required: qemu-system, /dev/kvm, and a baked Ubuntu runner qcow2 (see README)"
