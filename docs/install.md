# Install

gh-runnerd runs **only on Ubuntu 24.04 or newer**. Other host OS images are rejected at `doctor` / `serve`.

## The short version

```bash
mkdir gh-runnerd && cd gh-runnerd
gh release download --repo RefireLab/gh-runnerd --pattern "gh-runnerd_*_linux_$(dpkg --print-architecture).tar.gz"
tar -xzf gh-runnerd_*.tar.gz
sudo ./gh-runnerd init
```

No `gh` CLI? One line, installs into `/usr/local/bin`:

```bash
curl -fsSL https://raw.githubusercontent.com/RefireLab/gh-runnerd/main/scripts/install-binary.sh | sudo bash
```

`init` is an interactive wizard. It checks the host, installs the QEMU packages via apt, asks for your GitHub token (and verifies it against the API), asks which software the runner VMs should carry ([flavors](runner-images.md)), builds the VM image, and can register a systemd service that starts on boot. Everything below is detail for people who want to do it by hand.

## Requirements

- Ubuntu 24.04+ (amd64 or arm64)
- KVM: `ls -l /dev/kvm` must exist. On a VPS this needs nested virtualization; on bare metal enable VT-x/AMD-V in BIOS.
- Free disk: ~10 GB for the default `minimal` image; ~30 GB for `essential`; ~130 GB for `full` (the bake checks before starting).
- Outbound HTTPS.

Nested virt is **not** required for job *containers* (they use the VM kernel). It is only the host that needs KVM.

## Packages (the wizard installs these for you)

```bash
sudo apt-get install -y qemu-system-x86 qemu-utils cloud-image-utils iptables
# arm64 hosts: qemu-system-arm and qemu-efi-aarch64 instead of qemu-system-x86
```

## Binaries

The daemon is two static Linux binaries: `gh-runnerd` (host) and `gh-runnerd-guest` (baked into the VM). Keep them in the same directory.

From [GitHub Releases](https://github.com/RefireLab/gh-runnerd/releases) (replace the version with the [latest](https://github.com/RefireLab/gh-runnerd/releases/latest)):

```bash
VERSION=0.4.0
ARCH=amd64   # or arm64
curl -fL -o gh-runnerd.tar.gz \
  "https://github.com/RefireLab/gh-runnerd/releases/download/v${VERSION}/gh-runnerd_${VERSION}_linux_${ARCH}.tar.gz"
tar -xzf gh-runnerd.tar.gz
```

Verify against `gh-runnerd_${VERSION}_checksums.txt` from the same release:

```bash
sha256sum -c --ignore-missing gh-runnerd_${VERSION}_checksums.txt
```

Or with the GitHub CLI:

```bash
gh release download --repo RefireLab/gh-runnerd \
  --pattern 'gh-runnerd_*_linux_amd64.tar.gz'   # or _arm64
```

## GitHub token permissions

Fine-grained token ([create one](https://github.com/settings/personal-access-tokens/new)):

| Runners for | Required permissions |
|---|---|
| one repository | Repository: **Actions: Read-only**, **Administration: Read and write** (Metadata: Read-only is added automatically) |
| an organization | Resource owner = **the organization**; Organization: **Self-hosted runners: Read and write**; Repository: **Actions: Read-only** on the repos gh-runnerd watches for jobs |

Classic tokens: `repo` scope; org runners also need `admin:org`. The `init` wizard validates the token and the runner access live and tells you what is missing.

## Build from source

```bash
make build
sudo install -m 0755 bin/gh-runnerd bin/gh-runnerd-guest /usr/local/bin/
```

## First boot

```bash
sudo ./gh-runnerd init
```

The wizard also picks a private VM network (default `10.87.0.1/16`) that provably does not overlap your LAN/Docker/VPN, and a free local port for the embedded registry (default `42443`) — VMs still reach it on 443 via an iptables redirect scoped to the bridge, so the host's real 443 stays untouched.

The wizard offers two layouts:

- **System service** (root + systemd): config in `/etc/gh-runnerd/config.toml`, data in `/var/lib/gh-runnerd`, binaries in `/usr/local/bin`, a `gh-runnerd.service` unit, optional autostart.
- **Portable**: everything stays in the current folder (`./gh-runnerd.toml`, `./gh-runnerd-data/`). Start it with `sudo ./gh-runnerd serve`.

Scripted setups can skip the questions:

```bash
sudo gh-runnerd init --non-interactive \
  --config /etc/gh-runnerd/config.toml \
  --token "$GITHUB_TOKEN" --owner ORG --repo REPO
sudo gh-runnerd runner-image bake
sudo gh-runnerd doctor
sudo gh-runnerd serve --config /etc/gh-runnerd/config.toml
```

The VM image bake is built into the binary (`runner-image bake`): it downloads the Ubuntu cloud image, installs Docker + the GitHub Actions runner + the guest agent inside a throwaway VM, and activates the result. Once per machine, 3-5 minutes. Add `--flavor essential` (or `full`) to also run GitHub's own [actions/runner-images](https://github.com/actions/runner-images) build scripts so the VM carries the `ubuntu-latest` toolset — see [runner-images.md](runner-images.md).

Prefer a GitHub App for production credentials ([github-app.md](github-app.md)).

## Releasing (maintainers)

Releases are tag-driven and built by GoReleaser in CI ([.github/workflows/release.yml](../.github/workflows/release.yml), config in [.goreleaser.yaml](../.goreleaser.yaml)):

```bash
git tag v0.4.0
git push origin v0.4.0
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

## Permissions

`serve` needs root (or equivalent) for `/dev/kvm`, TAP devices, the bridge, and iptables NAT. Do not run jobs as that host user — jobs run inside disposable VMs.

## Data directory

- system service: `/var/lib/gh-runnerd`
- portable: `./gh-runnerd-data` next to the config (a relative `data_dir` resolves against the config file's folder)
- override with `--data-dir` or `GH_RUNNERD_DATA_DIR`
