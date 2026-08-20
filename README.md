# gh-runnerd

[![CI](https://github.com/RefireLab/gh-runnerd/actions/workflows/ci.yml/badge.svg)](https://github.com/RefireLab/gh-runnerd/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/RefireLab/gh-runnerd)](https://github.com/RefireLab/gh-runnerd/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Your own GitHub Actions runners on one Ubuntu machine.

Every job runs in a **fresh disposable virtual machine** with Docker inside. When the job ends, the VM is destroyed. No leftovers, no security surprises, no per-minute bills.

## Why gh-runnerd

- **A clean VM per job.** Runners register with GitHub's just-in-time (JIT) API, take exactly one job, and are wiped. Workflow code never touches the host — no host shell, no host `docker.sock`.
- **GitHub's own images, on your hardware.** The bake can run the build scripts from [actions/runner-images](https://github.com/actions/runner-images) — the very repository GitHub builds `ubuntu-latest` from — so your VMs carry the same git, gh, node, python, cmake, docker tooling. Pick `ubuntu-24.04`, `ubuntu-26.04`, or `ubuntu-22.04` and how much of it you want.
- **Container images pulled once, reused forever.** An embedded pull-only registry on an isolated bridge caches Docker Hub / GHCR / Quay pulls, so a `docker pull` your workflows do on every run is instant and rate-limit-proof. Local `docker save` tarballs work too: `gh-runnerd.local/my-ci:1.0`.
- **It diagnoses itself.** `doctor` checks the host; `serve` probes the whole VM datapath (DHCP, control channel, DNS, TCP 443) before booting a single runner, logs a concrete fix when something is broken, and pauses runner creation instead of minting Offline runners in GitHub.
- **Warm pool.** Keep 0..N VMs booted and waiting so jobs start in seconds; everything else boots on demand.
- **Webhook or polling.** A GitHub App webhook when your host is reachable, plain polling when it is behind NAT. Both work out of the box.

## What you need

1. A machine with **Ubuntu 24.04 or newer** — a home server, an old PC, or a VPS with nested virtualization.
   Quick check: `ls /dev/kvm` — if that file exists, you are good.
2. **10 GB of free disk** for the default image (more if you mirror GitHub's images — see below) and a normal internet connection.
3. A **GitHub token** (fine-grained). The setup wizard walks you through creating it and checks it live. The permissions it needs:
   - runners for **one repository** — Repository permissions: `Actions: Read-only`, `Administration: Read and write`
   - runners for an **organization** — Resource owner: the organization; Organization permissions: `Self-hosted runners: Read and write`; plus `Actions: Read-only` on the repos it watches for jobs

## Set it up (one command)

Download the two files into a folder and run the wizard:

```bash
mkdir gh-runnerd && cd gh-runnerd
gh release download --repo RefireLab/gh-runnerd --pattern "gh-runnerd_*_linux_$(dpkg --print-architecture).tar.gz"
tar -xzf gh-runnerd_*.tar.gz
sudo ./gh-runnerd init
```

### Without the GitHub CLI

```bash
curl -fsSL https://raw.githubusercontent.com/RefireLab/gh-runnerd/main/scripts/install-binary.sh | sudo bash
```

This installs the binaries into `/usr/local/bin`. Then run `sudo gh-runnerd init`.

That's it. `init` is a wizard that walks you through everything:

- checks your machine (Ubuntu, virtualization),
- installs the required packages for you (QEMU etc.),
- asks for your GitHub token — with step-by-step instructions on where to click — and **verifies it live** against GitHub,
- asks which repository (`owner/repo`) or organization should get the runners and verifies the access too,
- asks **which software the runner VMs should carry** (see the flavors below) and builds the VM image,
- and asks one important question: **"Install as a system service?"**
  - **Yes** (recommended): it puts everything in the proper system places, registers a `systemd` service, and starts it on every boot. Set up and forget.
  - **No**: everything stays in the current folder (`gh-runnerd.toml`, `gh-runnerd-data/`). Start it yourself with `sudo ./gh-runnerd serve`.

Just press **Enter** at every question to accept the defaults.

## Pick what's inside the VM

The default VM is minimal Ubuntu + Docker + the runner. If your workflows expect `ubuntu-latest` tools (node, gh, cmake, ...), bake them in — gh-runnerd runs GitHub's own image build scripts, no Packer needed:

| Flavor | What's inside | Image | Bake time |
|---|---|---|---|
| `minimal` (default) | Docker + runner + git/curl/jq | ~2 GB | 10-20 min |
| `essential` | + everyday tools from GitHub's images: git/git-lfs/gh, node + nvm, python + pipx, cmake, ninja, gcc, zstd, yq, pwsh, docker plugins | ~10 GB | ~1-2 h |
| `full` | everything `ubuntu-latest` ships: browsers, JDKs, Android SDK, CodeQL, toolcache... | ~60-80 GB | hours, ~130 GB free disk |

```bash
gh-runnerd runner-image available    # which GitHub images can I mirror?
sudo gh-runnerd runner-image bake --image ubuntu-24.04 --flavor essential
```

Details, pinning, and per-script control: [docs/runner-images.md](docs/runner-images.md).

## Use it

In any repository you connected, create `.github/workflows/ci.yml`:

```yaml
jobs:
  build:
    runs-on: gh-runnerd
    steps:
      - uses: actions/checkout@v4
      - run: echo it works
```

Push. The job runs on your machine in a clean Ubuntu VM. Done.

Want the job itself inside a container (Alpine, Node, your own image)? Same as on GitHub-hosted runners:

```yaml
jobs:
  build:
    runs-on: gh-runnerd
    container:
      image: alpine:3.22
    steps:
      - run: apk add --no-cache git
      - run: uname -a
```

`services:` (Postgres, Redis), private GHCR images, digest pins, and `docker/build-push-action` all work the way you expect — see [docs/containers.md](docs/containers.md) and [examples/workflows](examples/workflows).

## Everyday commands

| I want to... | Run |
|---|---|
| Check that everything is healthy | `gh-runnerd doctor` |
| See the pool and network state | `gh-runnerd status` |
| See the service | `systemctl status gh-runnerd` |
| See live logs | `journalctl -u gh-runnerd -f` |
| Rebuild the VM image (newest Ubuntu + runner + tools) | `sudo gh-runnerd runner-image update` |
| Get GitHub's `ubuntu-latest` tools in the VM | `sudo gh-runnerd runner-image bake --image ubuntu-24.04 --flavor essential` |
| See which GitHub images can be mirrored | `gh-runnerd runner-image available` |
| Add a VM image from a file | `sudo gh-runnerd runner-image import` (it's a wizard too) |
| Cache a container image for instant pulls | `gh-runnerd image pull node:22-bookworm` |
| Use a local tarball as a job container | `gh-runnerd image import ./my-ci.tar --name my-ci --tag 1.0` |
| Re-run the whole setup | `sudo gh-runnerd init` |

## If something goes wrong

1. `gh-runnerd doctor` — tells you what is missing and how to fix it.
2. `journalctl -u gh-runnerd -e` — the daemon's own words; network problems come with the exact fix and runner creation pauses until it passes.
3. Re-run `sudo gh-runnerd init` — it is safe to run again; it keeps your config if you want.
4. Still stuck? [docs/troubleshooting.md](docs/troubleshooting.md).

## Remove it

```bash
sudo systemctl disable --now gh-runnerd
sudo rm -f /etc/systemd/system/gh-runnerd.service /usr/local/bin/gh-runnerd /usr/local/bin/gh-runnerd-guest
sudo rm -rf /etc/gh-runnerd /var/lib/gh-runnerd
```

(Portable mode: just delete the folder.)

## How it works

```text
Ubuntu host
└── gh-runnerd (daemon)
    ├── isolated bridge: DHCP, guest control, pull-through registry cache
    └── disposable Ubuntu VM   ← created per job, destroyed after
        ├── GitHub Actions Runner (official, JIT, one job)
        ├── Docker Engine + Compose
        ├── ubuntu-latest tooling            # if baked with essential/full
        └── job container (any image)        # only if the workflow sets container:
```

- `runs-on: gh-runnerd` picks your infrastructure; `container.image` picks the job's Linux environment.
- Jobs **never** touch the host: no host shell, no host `docker.sock`.
- VMs cannot talk to each other; private registry pulls are never cached into the shared store.

More detail: [install](docs/install.md) · [runner images](docs/runner-images.md) · [job containers](docs/containers.md) · [registry & cache](docs/local-registry.md) · [architecture](docs/architecture.md) · [security](docs/security.md) · [GitHub App](docs/github-app.md) · [troubleshooting](docs/troubleshooting.md) · [examples](examples/workflows)

## License

MIT

Crafted by [Refirelab](https://refirelab.com/), co-piloted by a few AI agents.
