# Runner VM images

Shipped templates are **Ubuntu 24.04 only** (amd64 and arm64). Debian/Fedora templates are not included.

The golden image is a minimal Ubuntu cloud disk with:

- official GitHub Actions Runner (pinned at bake time, default v2.336.0)
- Docker Engine + Compose plugin
- git, bash, sh, curl, tar, gzip, unzip, jq, ca-certificates, coreutils
- qemu-guest-agent
- gh-runnerd-guest
- internal CA + registry `hosts.toml`

It is **not** GitHub's `ubuntu-latest` kitchen sink.

```bash
sudo gh-runnerd runner-image bake        # build + import + activate in one go
sudo gh-runnerd runner-image list
sudo gh-runnerd runner-image import ./ubuntu-24.04-amd64.qcow2   # name defaults to the file name
sudo gh-runnerd runner-image validate ubuntu-24.04-amd64
sudo gh-runnerd runner-image activate ubuntu-24.04-amd64
sudo gh-runnerd runner-image update      # re-bake from the newest Ubuntu cloud image
```

`bake` is built into the binary: it downloads the official Ubuntu 24.04 cloud image, boots it once under QEMU/KVM to install Docker, the pinned GitHub Actions runner, and `gh-runnerd-guest`, verifies the install completed, compresses the qcow2, and activates it. It needs `/dev/kvm`, `qemu-system-*`, `qemu-utils`, and `cloud-image-utils` (the `init` wizard installs these).

`validate` checks the file exists, SHA-256 matches the catalog MANIFEST, and `qemu-img info` recognizes a disk. Live boot validation is the bake process itself.

## Runnerfile

```dockerfile
FROM gh-runnerd/ubuntu-24.04
RUN apt-get update && apt-get install -y ffmpeg imagemagick
PRELOAD ghcr.io/company/ci:2026.08
```

```bash
./bin/gh-runnerd runner-image build ./Runnerfile --name company-runner
```

- `FROM` must be the shipped Ubuntu 24.04 base (Alpine/Debian/Fedora are rejected).
- `RUN` lines execute inside the VM at the end of the bake.
- `PRELOAD` pulls into the **host** registry so every VM can `docker pull` it instantly.

## Updating the runner binary

Disposable overlays cannot keep GitHub's runner auto-update. When GitHub requires a newer runner:

```bash
./bin/gh-runnerd doctor          # warns if the template is missing
./bin/gh-runnerd runner-image update
```
