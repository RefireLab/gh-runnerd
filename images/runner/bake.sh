#!/usr/bin/env bash
# Bake a golden Ubuntu 24.04 runner qcow2 for gh-runnerd.
# Requires: Ubuntu 24.04 host, KVM, qemu-system, qemu-img, cloud-image-utils, curl.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  QEMU_BIN="${QEMU_BIN:-qemu-system-x86_64}"; IMG_ARCH=amd64; RUNNER_ARCH=x64; CLOUD_NAME="noble-server-cloudimg-amd64.img" ;;
  aarch64) QEMU_BIN="${QEMU_BIN:-qemu-system-aarch64}"; IMG_ARCH=arm64; RUNNER_ARCH=arm64; CLOUD_NAME="noble-server-cloudimg-arm64.img" ;;
  *) echo "unsupported host arch $ARCH" >&2; exit 1 ;;
esac

RUNNER_VERSION="${RUNNER_VERSION:-2.336.0}"
OUT_DIR="${OUT_DIR:-$ROOT/images/runner}"
WORK="${WORK:-/tmp/gh-runnerd-bake-$$}"
NAME="ubuntu-24.04-${IMG_ARCH}"
OUT_QCOW="${OUT_DIR}/${NAME}.qcow2"
GUEST_BIN="${GUEST_BIN:-$ROOT/bin/gh-runnerd-guest}"
CLOUD_URL="https://cloud-images.ubuntu.com/noble/current/${CLOUD_NAME}"

if [[ ! -e /dev/kvm ]]; then
  echo "error: /dev/kvm is required to bake the runner image" >&2
  exit 1
fi
for bin in "$QEMU_BIN" qemu-img curl cloud-localds; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "error: missing $bin (apt install qemu-system-x86 qemu-utils cloud-image-utils curl)" >&2
    exit 1
  fi
done

if [[ ! -x "$GUEST_BIN" ]]; then
  echo "building gh-runnerd-guest..."
  make -C "$ROOT" guest
fi

mkdir -p "$WORK/seed" "$OUT_DIR"
trap 'rm -rf "$WORK"' EXIT

echo ">> downloading Ubuntu 24.04 cloud image"
curl -fL --retry 3 -o "$WORK/cloud.img" "$CLOUD_URL"
qemu-img convert -O qcow2 "$WORK/cloud.img" "$WORK/base.qcow2"
qemu-img resize "$WORK/base.qcow2" 40G

install -m 0755 "$GUEST_BIN" "$WORK/seed/gh-runnerd-guest"
install -m 0644 "$ROOT/images/runner/guest/gh-runnerd-guest.service" "$WORK/seed/gh-runnerd-guest.service"
install -m 0644 "$ROOT/images/runner/guest/hosts" "$WORK/seed/hosts"
install -m 0644 "$ROOT/images/runner/guest/docker-daemon.json" "$WORK/seed/daemon.json"
install -m 0644 "$ROOT/images/runner/guest/hosts.toml.docker" "$WORK/seed/hosts.toml.docker"
install -m 0644 "$ROOT/images/runner/guest/hosts.toml.ghcr" "$WORK/seed/hosts.toml.ghcr"
install -m 0644 "$ROOT/images/runner/guest/hosts.toml.quay" "$WORK/seed/hosts.toml.quay"

CA_SRC="${GH_RUNNERD_CA:-}"
if [[ -z "$CA_SRC" ]]; then
  for cand in \
    "$ROOT/gh-runnerd-data/state/ca/ca.crt" \
    /var/lib/gh-runnerd/state/ca/ca.crt; do
    if [[ -f "$cand" ]]; then CA_SRC="$cand"; break; fi
  done
fi
if [[ -n "$CA_SRC" && -f "$CA_SRC" ]]; then
  install -m 0644 "$CA_SRC" "$WORK/seed/gh-runnerd-ca.crt"
else
  printf '' > "$WORK/seed/gh-runnerd-ca.crt"
fi

cat > "$WORK/seed/install.sh" <<INSTALL
#!/bin/bash
set -euo pipefail
SEED=/mnt/gh-runnerd-seed
install -m 0755 "\$SEED/gh-runnerd-guest" /usr/local/bin/gh-runnerd-guest
install -m 0644 "\$SEED/gh-runnerd-guest.service" /etc/systemd/system/gh-runnerd-guest.service
if [[ -s "\$SEED/gh-runnerd-ca.crt" ]]; then
  install -m 0644 "\$SEED/gh-runnerd-ca.crt" /usr/local/share/ca-certificates/gh-runnerd.crt
  update-ca-certificates || true
fi
cat "\$SEED/hosts" >> /etc/hosts
install -d /etc/docker /etc/docker/certs.d/docker.io /etc/docker/certs.d/ghcr.io /etc/docker/certs.d/quay.io
install -m 0644 "\$SEED/daemon.json" /etc/docker/daemon.json
install -m 0644 "\$SEED/hosts.toml.docker" /etc/docker/certs.d/docker.io/hosts.toml
install -m 0644 "\$SEED/hosts.toml.ghcr" /etc/docker/certs.d/ghcr.io/hosts.toml
install -m 0644 "\$SEED/hosts.toml.quay" /etc/docker/certs.d/quay.io/hosts.toml

curl -fsSL https://get.docker.com | sh
apt-get install -y docker-compose-plugin || true
usermod -aG docker ubuntu || true

install -d -o ubuntu -g ubuntu /opt/actions-runner
cd /opt/actions-runner
curl -fL -o actions-runner.tgz \\
  "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-${RUNNER_ARCH}-${RUNNER_VERSION}.tar.gz"
tar xzf actions-runner.tgz
rm -f actions-runner.tgz
chown -R ubuntu:ubuntu /opt/actions-runner
./bin/installdependencies.sh || apt-get install -y libicu74 libssl3t64 libkrb5-3 zlib1g || true

systemctl enable qemu-guest-agent.service || true
systemctl enable gh-runnerd-guest.service
systemctl disable cloud-init.service cloud-init-local.service cloud-config.service cloud-final.service || true
touch /etc/cloud/cloud-init.disabled

if [[ -f "\$SEED/runnerfile-runs.sh" ]]; then
  bash "\$SEED/runnerfile-runs.sh"
fi
INSTALL
chmod 0755 "$WORK/seed/install.sh"

if [[ -n "${GH_RUNNERD_RUNNERFILE_RUNS:-}" ]]; then
  printf '#!/bin/bash\nset -euo pipefail\n%s\n' "$GH_RUNNERD_RUNNERFILE_RUNS" > "$WORK/seed/runnerfile-runs.sh"
  chmod 0755 "$WORK/seed/runnerfile-runs.sh"
fi

cat > "$WORK/user-data" <<'EOF'
#cloud-config
hostname: gh-runnerd-golden
manage_etc_hosts: false
package_update: true
packages:
  - qemu-guest-agent
  - git
  - curl
  - jq
  - tar
  - gzip
  - unzip
  - ca-certificates
  - apt-transport-https
  - gnupg
runcmd:
  - mkdir -p /mnt/gh-runnerd-seed
  - mount -t 9p -o trans=virtio,version=9p2000.L seed /mnt/gh-runnerd-seed
  - bash /mnt/gh-runnerd-seed/install.sh
power_state:
  mode: poweroff
  timeout: 900
  condition: true
EOF

cat > "$WORK/meta-data" <<EOF
instance-id: gh-runnerd-bake
local-hostname: gh-runnerd-golden
EOF

cloud-localds "$WORK/seed.iso" "$WORK/user-data" "$WORK/meta-data"

echo ">> booting bake VM (several minutes, needs network)"
"$QEMU_BIN" \
  -name gh-runnerd-bake \
  -machine q35,accel=kvm \
  -cpu host \
  -smp 2 -m 4096 \
  -nographic \
  -drive if=virtio,format=qcow2,file="$WORK/base.qcow2" \
  -drive if=virtio,format=raw,file="$WORK/seed.iso" \
  -netdev user,id=net0 \
  -device virtio-net-pci,netdev=net0 \
  -device virtio-rng-pci \
  -fsdev local,id=seed,path="$WORK/seed",security_model=mapped,readonly=on \
  -device virtio-9p-pci,fsdev=seed,mount_tag=seed

qemu-img convert -c -O qcow2 "$WORK/base.qcow2" "$OUT_QCOW"
sha256sum "$OUT_QCOW" | awk '{print $1}' > "${OUT_QCOW}.sha256"
echo ">> baked $OUT_QCOW"
echo ">> next: gh-runnerd runner-image import $OUT_QCOW --name $NAME"
echo ">>        gh-runnerd runner-image activate $NAME"
