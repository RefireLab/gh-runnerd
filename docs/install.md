# Install

gh-runnerd runs **only on Ubuntu 24.04 or newer**. Other host OS images are rejected at `doctor` / `serve`.

## Packages

```bash
sudo apt update
sudo apt install -y \
  qemu-system-x86 \
  qemu-utils \
  qemu-guest-agent \
  iptables \
  cloud-image-utils \
  curl \
  git \
  make \
  golang-go
```

On ARM Ubuntu hosts install `qemu-system-arm` instead of `qemu-system-x86`.

Confirm KVM:

```bash
ls -l /dev/kvm
```

If the node is a VM itself, enable nested virtualization in the hypervisor **or** run gh-runnerd on bare metal. Nested virt is **not** required for job *containers* (they use the VM kernel). Nested virt is only needed if you try to run VMs inside the runner VM, which gh-runnerd does not do.

## Build from source

```bash
make build
sudo install -m 0755 bin/gh-runnerd bin/gh-runnerd-guest /usr/bin/
```

## Debian package

```bash
make build
./packaging/deb/build.sh
sudo dpkg -i dist/gh-runnerd_*.deb
```

The unit is `gh-runnerd.service` (`DeviceAllow=/dev/kvm`, `CAP_NET_ADMIN` for the isolated bridge).

## First boot

```bash
sudo gh-runnerd init --config /etc/gh-runnerd/config.toml \
  --token "$GITHUB_TOKEN" --owner ORG --repo REPO
```

Edit `/etc/gh-runnerd/config.toml`. Prefer a GitHub App ([github-app.md](github-app.md)).

Bake the Ubuntu 24.04 runner template (once per machine, several minutes, needs outbound HTTPS):

```bash
sudo make runner-image
sudo gh-runnerd runner-image import ./images/runner/ubuntu-24.04-amd64.qcow2 --name ubuntu-24.04-amd64
sudo gh-runnerd runner-image activate ubuntu-24.04-amd64
sudo gh-runnerd doctor
sudo systemctl enable --now gh-runnerd
```

On arm64 the qcow2 is `ubuntu-24.04-arm64.qcow2`.

## Permissions

`serve` needs root (or equivalent) for `/dev/kvm`, TAP devices, the bridge, and iptables NAT. Do not run jobs as that host user — jobs run inside disposable VMs.

## Data directory

systemd: `/var/lib/gh-runnerd`

dev: `./gh-runnerd-data` (override with `--data-dir` or `GH_RUNNERD_DATA_DIR`)
