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

## Binaries

The daemon is two static Linux binaries: `gh-runnerd` (host) and `gh-runnerd-guest` (baked into the VM). They do **not** replace QEMU/KVM or the Ubuntu qcow2.

Install from [GitHub Releases](https://github.com/RefireLab/gh-runnerd/releases):

```bash
VERSION=0.1.0
ARCH=amd64   # or arm64
curl -fL -o gh-runnerd.tar.gz \
  "https://github.com/RefireLab/gh-runnerd/releases/download/v${VERSION}/gh-runnerd_${VERSION}_linux_${ARCH}.tar.gz"
tar -xzf gh-runnerd.tar.gz
sudo install -m 0755 gh-runnerd gh-runnerd-guest /usr/local/bin/
```

Each archive contains `gh-runnerd`, `gh-runnerd-guest`, `LICENSE`, and `README.md`. Verify downloads against `gh-runnerd_${VERSION}_checksums.txt` from the same release:

```bash
sha256sum -c --ignore-missing gh-runnerd_${VERSION}_checksums.txt
```

Or use the helper that detects the arch and latest release:

```bash
sudo ./scripts/install-binary.sh
```

For a **private** repository the anonymous URLs above return 404. Download with the authenticated GitHub CLI:

```bash
gh release download v0.1.0 --repo RefireLab/gh-runnerd \
  --pattern 'gh-runnerd_*_linux_amd64.tar.gz'   # or _arm64
```

## Build from source

```bash
make build
sudo install -m 0755 bin/gh-runnerd bin/gh-runnerd-guest /usr/local/bin/
```

## Releasing (maintainers)

Releases are tag-driven and built by GoReleaser in CI ([.github/workflows/release.yml](../.github/workflows/release.yml), config in [.goreleaser.yaml](../.goreleaser.yaml)):

```bash
git tag v0.1.0
git push origin v0.1.0
```

The workflow cross-compiles linux amd64/arm64, packs `gh-runnerd_<version>_linux_<arch>.tar.gz`, generates the checksums file and changelog, and publishes the GitHub Release. Dry-run locally without publishing:

```bash
goreleaser release --snapshot --clean
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
