# gh-runnerd

Your own GitHub Actions runners on one Ubuntu machine.

Every job runs in a **fresh disposable virtual machine** with Docker inside. When the job ends, the VM is destroyed. No leftovers, no security surprises, no per-minute bills.

## What you need

1. A machine with **Ubuntu 24.04 or newer** — a home server, an old PC, or a VPS with nested virtualization.
   Quick check: `ls /dev/kvm` — if that file exists, you are good.
2. About **10 GB of free disk** and a normal internet connection.
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
- builds the runner VM image (one time, ~600 MB download, 10-20 minutes),
- and asks one important question: **"Install as a system service?"**
  - **Yes** (recommended): it puts everything in the proper system places, registers a `systemd` service, and starts it on every boot. Set up and forget.
  - **No**: everything stays in the current folder (`gh-runnerd.toml`, `gh-runnerd-data/`). Start it yourself with `sudo ./gh-runnerd serve`.

Just press **Enter** at every question to accept the defaults.

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

## Everyday commands

| I want to... | Run |
|---|---|
| Check that everything is healthy | `gh-runnerd doctor` |
| See the service | `systemctl status gh-runnerd` |
| See live logs | `journalctl -u gh-runnerd -f` |
| Rebuild the VM image (newest Ubuntu + runner) | `sudo gh-runnerd runner-image update` |
| Get GitHub's `ubuntu-latest` tools in the VM | `sudo gh-runnerd runner-image bake --image ubuntu-24.04 --flavor essential` |
| See which GitHub images can be mirrored | `gh-runnerd runner-image available` |
| Add a VM image from a file | `sudo gh-runnerd runner-image import` (it's a wizard too) |
| Re-run the whole setup | `sudo gh-runnerd init` |

## If something goes wrong

1. `gh-runnerd doctor` — tells you what is missing and how to fix it.
2. `journalctl -u gh-runnerd -e` — the daemon's own words.
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
    └── disposable Ubuntu 24.04 VM   ← created per job, destroyed after
        ├── GitHub Actions Runner (official)
        ├── Docker Engine + Compose
        └── job container (any image)   # only if the workflow sets container:
```

- `runs-on: gh-runnerd` picks your infrastructure; `container.image` picks the job's Linux environment.
- Jobs **never** touch the host: no host shell, no host `docker.sock`.
- The default VM is minimal Ubuntu + Docker + the official runner. Want it to look like GitHub's `ubuntu-latest`? Bake it with the same scripts GitHub uses ([actions/runner-images](https://github.com/actions/runner-images)): `sudo gh-runnerd runner-image bake --image ubuntu-24.04 --flavor essential` (everyday tools: git, gh, node, python, cmake...) or `--flavor full` (everything, ~80 GB). Details: [docs/runner-images.md](docs/runner-images.md).
- An embedded pull-only registry on an isolated bridge caches container images, so repeated `docker pull`s are instant and local images work: `gh-runnerd image import ./my-ci.tar --name my-ci --tag 1.0`, then `container: image: gh-runnerd.local/my-ci:1.0`.

More detail: [docs/install.md](docs/install.md) · [docs/architecture.md](docs/architecture.md) · [docs/github-app.md](docs/github-app.md) · [examples/workflows](examples/workflows)

## License

MIT

Crafted by [Refirelab](https://refirelab.com/), co-piloted by a few AI agents.
