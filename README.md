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

## Five minutes to a green check

Host: Ubuntu 24.04+ with KVM.

```bash
sudo apt install -y qemu-system-x86 qemu-utils qemu-guest-agent iptables cloud-image-utils curl git make golang-go genisoimage
git clone https://github.com/RefireLab/gh-runnerd
cd gh-runnerd
make build

# data dir + internal CA + starter config
./bin/gh-runnerd init --token "$GITHUB_TOKEN" --owner YOUR_ORG --repo YOUR_REPO --with-examples

# bake the Ubuntu 24.04 runner VM (needs KVM and several minutes)
make runner-image
./bin/gh-runnerd runner-image import ./images/runner/ubuntu-24.04-amd64.qcow2 --name ubuntu-24.04-amd64
./bin/gh-runnerd runner-image activate ubuntu-24.04-amd64

./bin/gh-runnerd doctor
./bin/gh-runnerd serve --config ./gh-runnerd.toml
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
