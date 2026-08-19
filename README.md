# gh-runnerd

Ephemeral **Ubuntu 24.04 VMs** that register as GitHub Actions self-hosted runners.

`runs-on` selects the gh-runnerd infrastructure. `container.image` selects the Linux environment for the job. Alpine is a first-class **job container** example, not the runner OS.

```text
Ubuntu host
└── gh-runnerd
    └── disposable Ubuntu 24.04 VM
        ├── GitHub Actions Runner (official)
        ├── Docker Engine + Compose
        └── job container (any image)   # only if the workflow sets container:
```

gh-runnerd does **not** reimplement GitHub's job container runtime. The official runner inside the VM runs `docker pull`, job containers, sibling container actions, and `services:`.

## New Ubuntu host → first job

Host must be **Ubuntu 24.04+** with `/dev/kvm`. The Go binary is not enough: QEMU still runs the disposable VMs, and you bake the Ubuntu runner disk **once**.

### 1. Packages

```bash
sudo apt update
sudo apt install -y qemu-system-x86 qemu-utils qemu-guest-agent iptables \
  cloud-image-utils curl git make golang-go ca-certificates
# ARM Ubuntu: qemu-system-arm instead of qemu-system-x86
ls -l /dev/kvm   # must exist
```

### 2. Binary

Grab the tar.gz for your architecture from [Releases](https://github.com/RefireLab/gh-runnerd/releases):

```bash
VERSION=0.1.0
ARCH=amd64        # x86_64 servers/PCs; use arm64 for Ampere/Raspberry/Graviton
curl -fL -o gh-runnerd.tar.gz \
  "https://github.com/RefireLab/gh-runnerd/releases/download/v${VERSION}/gh-runnerd_${VERSION}_linux_${ARCH}.tar.gz"
tar -xzf gh-runnerd.tar.gz
sudo install -m 0755 gh-runnerd gh-runnerd-guest /usr/local/bin/
gh-runnerd --version
```

One-liner that picks the arch and the latest release automatically:

```bash
curl -fsSL https://raw.githubusercontent.com/RefireLab/gh-runnerd/main/scripts/install-binary.sh | sudo bash
```

While this repository is private, anonymous downloads return 404 — use the authenticated GitHub CLI instead:

```bash
gh release download v0.1.0 --repo RefireLab/gh-runnerd \
  --pattern 'gh-runnerd_*_linux_amd64.tar.gz'
tar -xzf gh-runnerd_*_linux_amd64.tar.gz
sudo install -m 0755 gh-runnerd gh-runnerd-guest /usr/local/bin/
```

Or build from source (`git clone` → `make build` → `sudo install -m 0755 bin/gh-runnerd bin/gh-runnerd-guest /usr/local/bin/`).

### 3. GitHub token

Fine-grained PAT on the repo that will use the runner:

- **Administration**: read/write (creates JIT runners)
- **Actions**: read
- **Metadata**: read

```bash
export GH_RUNNERD_GITHUB_TOKEN=github_pat_...
```

A GitHub App is better for production: [docs/github-app.md](docs/github-app.md).

### 4. Init + bake the runner VM (once)

```bash
sudo gh-runnerd init --config /etc/gh-runnerd/config.toml \
  --data-dir /var/lib/gh-runnerd \
  --token "$GH_RUNNERD_GITHUB_TOKEN" \
  --owner YOUR_ORG --repo YOUR_REPO

# edit /etc/gh-runnerd/config.toml if needed, then bake (~several minutes, needs network)
cd /path/to/gh-runnerd
sudo make runner-image
sudo gh-runnerd runner-image import ./images/runner/ubuntu-24.04-amd64.qcow2 --name ubuntu-24.04-amd64
sudo gh-runnerd runner-image activate ubuntu-24.04-amd64
# arm64: ubuntu-24.04-arm64.qcow2
```

### 5. Run the daemon

```bash
sudo gh-runnerd doctor --config /etc/gh-runnerd/config.toml
sudo gh-runnerd serve --config /etc/gh-runnerd/config.toml
# or, after make deb / dpkg -i: sudo systemctl enable --now gh-runnerd
```

Poll mode works without a public webhook. For org-scale, expose `/webhook` and set `webhook.secret`.

### 6. Workflow in that repo

```yaml
jobs:
  test:
    runs-on: gh-runnerd
    steps:
      - uses: actions/checkout@v4
      - run: uname -a
```

Workflow:

```yaml
jobs:
  test:
    runs-on: gh-runnerd
    steps:
      - uses: actions/checkout@v4
      - run: uname -a
```

With a job container (any registry, including Alpine / Node / your own):

```yaml
jobs:
  test:
    runs-on: gh-runnerd
    container:
      image: alpine:3.22
    steps:
      - run: apk add --no-cache git curl
      - run: uname -a
```

More: [docs/install.md](docs/install.md), [docs/github-app.md](docs/github-app.md), [examples/workflows](examples/workflows).

## What this is not

- Not a clone of GitHub-hosted `ubuntu-latest` (no Node/Python/Java/browsers preinstalled in the VM).
- Not an Alpine-based runner OS. GitHub's self-hosted runner targets glibc distros; Alpine is for `container:`.
- Not Docker-on-the-host. Jobs never run on the Ubuntu host and never get the host `docker.sock`.

## Local images

```bash
./bin/gh-runnerd image import ./imports/my-ci.tar --name my-ci --tag 2026.08
```

```yaml
container:
  image: gh-runnerd.local/my-ci:2026.08
```

The VM's Docker daemon pulls from an embedded pull-only registry bound only to the isolated bridge (`10.87.0.1:443`). There is no public host port.

## License

MIT
