# Runner VM images

Templates are **Ubuntu only** (amd64 and arm64). Debian/Fedora templates are not included.

Every image contains the gh-runnerd base:

- official GitHub Actions Runner (pinned at bake time, default v2.336.0)
- Docker Engine + Compose plugin
- qemu-guest-agent, gh-runnerd-guest
- internal CA + registry `hosts.toml`

What else it contains is the **flavor**:

| Flavor | Contents | Image size | Bake time |
|---|---|---|---|
| `minimal` (default) | base + git, curl, jq, tar, unzip | ~2 GB | 10-20 min |
| `essential` | + the everyday tools from GitHub's own images: git/git-lfs/gh, node + nvm, python + pipx, cmake, ninja, gcc/g++, zstd, yq, PowerShell, Docker plugins (buildx, compose) | ~10 GB | ~1-2 h |
| `full` | every script GitHub's `ubuntu-latest` runs: browsers, JDKs, Android SDK, CodeQL, .NET, Go/Rust/Ruby/PHP, toolcache, clouds... | ~60-80 GB | many hours, needs ~130 GB free |

`essential` and `full` do not reimplement anything: the bake downloads a pinned **release of [actions/runner-images](https://github.com/actions/runner-images)** — the very repository GitHub builds its hosted VMs from — and executes the same build scripts inside the QEMU VM (no Packer, no Azure). Pick the upstream image and flavor:

```bash
sudo gh-runnerd runner-image available   # list upstream images + latest releases
sudo gh-runnerd runner-image bake --image ubuntu-24.04 --flavor essential
sudo gh-runnerd runner-image bake --image ubuntu-26.04 --flavor full
```

The choice is recorded in the config and reused by `runner-image update`:

```toml
[image]
flavor = "essential"          # minimal | essential | full
upstream = "ubuntu-24.04"     # any image from: runner-image available
# upstream_ref = "ubuntu24/20260810.271"   # optional pin; default: newest release
```

Useful knobs:

- `--upstream-ref ubuntu24/20260810.271` — pin an exact upstream release (or `main`).
- `--skip-scripts install-codeql-bundle.sh,install-android-sdk.sh` — drop scripts you do not need from any flavor.
- `--only-scripts ...` — run only the named scripts (debugging).
- `--timeout 6h` — override the flavor default (45 min / 3 h / 14 h).

If a non-critical upstream script fails (a mirror hiccup, a tool that will not install on your host), the bake **keeps going** and prints the failed script names at the end — the logs live at `/imagegeneration/logs/*.log` inside the image, and `/etc/gh-runnerd-image` records the source release, the flavor, and any failures. Critical failures (apt configuration, Docker) abort the bake.

All the everyday commands:

```bash
sudo gh-runnerd runner-image bake        # build + import + activate in one go
sudo gh-runnerd runner-image available   # what can I mirror?
sudo gh-runnerd runner-image list
sudo gh-runnerd runner-image import ./ubuntu-24.04-amd64.qcow2   # name defaults to the file name
sudo gh-runnerd runner-image validate ubuntu-24.04-amd64
sudo gh-runnerd runner-image activate ubuntu-24.04-amd64
sudo gh-runnerd runner-image update      # re-bake: newest cloud image + newest upstream release
```

`bake` is built into the binary: it downloads the official Ubuntu cloud image for the chosen version, boots it once under QEMU/KVM to install everything, verifies the install completed, compresses the qcow2, and activates it. It needs `/dev/kvm`, `qemu-system-*`, `qemu-utils`, and `cloud-image-utils` (the `init` wizard installs these).

`validate` checks the file exists, SHA-256 matches the catalog MANIFEST, and `qemu-img info` recognizes a disk. Live boot validation is the bake process itself.

## How mirroring GitHub's images works

There is no fork of upstream to maintain and nothing hardcoded per tool:

1. `bake` resolves the newest **stable release tag** of `actions/runner-images` for your image and architecture (e.g. `ubuntu24/20260810.271`; prereleases are skipped) and downloads that exact source snapshot.
2. It parses GitHub's own Packer template (`images/ubuntu/templates/build.ubuntu-24_04.pkr.hcl`) to get the ordered list of build scripts, their environment variables, and which ones run under PowerShell — so when upstream adds or reorders a tool, the next `runner-image update` simply follows.
3. Inside the bake VM it recreates the layout those scripts expect (`/imagegeneration` with helpers, installers, `toolset.json`, tests, post-generation hooks), shims the one Azure-ism (`/etc/waagent.conf`), and runs the scripts in order — each with its own log under `/imagegeneration/logs/`.
4. GitHub's *post-generation* scripts (the ones hosted VMs run at first boot) run once at the end, so `/etc/environment` carries `ImageOS`, `RUNNER_TOOL_CACHE=/opt/hostedtoolcache`, and the full `PATH` — `actions/setup-node`, `setup-python`, and friends find the toolcache exactly like on GitHub.
5. The gh-runnerd base install runs **after** the upstream scripts, so the guest agent, registry trust, and Docker mirror config always win.

The `essential` flavor is the same pipeline with a curated allowlist of upstream scripts; `full` runs them all.

## Runnerfile + flavors

The two compose: the flavor from the config is baked first, then `RUN` lines. So a `Runnerfile` on top of `[image] flavor = "essential"` starts from the full essential toolset.

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
