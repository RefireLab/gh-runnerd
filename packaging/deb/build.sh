#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)}"
STAGE="${STAGE:-/tmp/gh-runnerd-deb-root}"
ARCH="$(dpkg --print-architecture 2>/dev/null || echo amd64)"
OUT="${OUT:-$ROOT/dist/gh-runnerd_${VERSION}_${ARCH}.deb}"

rm -rf "$STAGE"
mkdir -p "$STAGE/usr/bin" \
  "$STAGE/lib/systemd/system" \
  "$STAGE/etc/gh-runnerd" \
  "$STAGE/usr/share/doc/gh-runnerd" \
  "$STAGE/DEBIAN"

install -m 0755 "$ROOT/bin/gh-runnerd" "$STAGE/usr/bin/gh-runnerd"
install -m 0755 "$ROOT/bin/gh-runnerd-guest" "$STAGE/usr/bin/gh-runnerd-guest"
install -m 0644 "$ROOT/packaging/deb/gh-runnerd.service" "$STAGE/lib/systemd/system/gh-runnerd.service"
install -m 0644 "$ROOT/packaging/deb/config.toml.example" "$STAGE/etc/gh-runnerd/config.toml.example"
install -m 0644 "$ROOT/README.md" "$STAGE/usr/share/doc/gh-runnerd/README.md"

SIZE="$(du -sk "$STAGE" | awk '{print $1}')"
cat > "$STAGE/DEBIAN/control" <<EOF
Package: gh-runnerd
Version: ${VERSION}
Section: admin
Priority: optional
Architecture: ${ARCH}
Maintainer: RefireLab <dev@refirelab.com>
Depends: qemu-system-x86 | qemu-system-arm, qemu-utils, qemu-guest-agent, iptables, cloud-image-utils, curl, ca-certificates
Installed-Size: ${SIZE}
Description: Ephemeral Ubuntu VM GitHub Actions runners
 gh-runnerd runs disposable Ubuntu 24.04 VMs as GitHub Actions self-hosted
 runners. Job containers are any Docker image via jobs.<id>.container.image.
EOF

cat > "$STAGE/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
mkdir -p /var/lib/gh-runnerd /etc/gh-runnerd
if [ ! -f /etc/gh-runnerd/config.toml ] && [ -f /etc/gh-runnerd/config.toml.example ]; then
  cp /etc/gh-runnerd/config.toml.example /etc/gh-runnerd/config.toml
  chmod 600 /etc/gh-runnerd/config.toml
fi
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
echo "Run the setup wizard: sudo gh-runnerd init"
EOF
chmod 0755 "$STAGE/DEBIAN/postinst"

mkdir -p "$(dirname "$OUT")"
dpkg-deb --build "$STAGE" "$OUT"
echo "built $OUT"
