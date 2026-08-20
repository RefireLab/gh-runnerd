# Architecture

Product rule: **`runs-on` selects gh-runnerd infrastructure. `container.image` selects the user Linux environment.**

```text
Ubuntu host
└── gh-runnerd
    ├── isolated bridge br-ghrunnerd (10.87.0.0/16, configurable at init)
    │     ├── pull-only OCI registry on 10.87.0.1:42443
    │     │     (VMs dial :443 → iptables REDIRECT on the bridge)
    │     ├── guest control TCP 10.87.0.1:5099 (vsock when available)
    │     └── tiny DHCP
    └── disposable Ubuntu VM (overlay qcow2; 24.04 / 26.04 / 22.04 base)
          ├── official GitHub Actions Runner (JIT, one job)
          ├── Docker Engine
          ├── optional ubuntu-latest tooling (essential/full flavor)
          └── optional job container
```

## Golden image

`runner-image bake` boots the official Ubuntu cloud image once under
QEMU/KVM, installs Docker, the pinned GitHub Actions runner, and the
guest agent, then compresses and activates the qcow2. With the
`essential` or `full` flavor it first executes the build scripts from a
pinned release of [actions/runner-images](https://github.com/actions/runner-images)
— parsed straight out of GitHub's own Packer template, no Packer or
Azure involved — so the VM carries the `ubuntu-latest` toolset. Each
runtime VM is a copy-on-write overlay of that image, never smaller than
the image's virtual size, destroyed after one job.
([runner-images.md](runner-images.md))

## Startup self-test

Before booting any runner VM, `serve` cleans up orphans from a previous
run (QEMU processes, `tap-ghrd*` devices, overlay disks) and probes the VM
datapath through a network namespace attached to the real bridge: DHCP
offer, guest control port, DNS via `1.1.1.1`, and TCP 443 egress. Failures
are logged with a concrete fix and pause runner creation — otherwise every
boot would mint an Offline JIT runner in GitHub. The probe re-runs each
poll interval until it passes; `gh-runnerd status` reports `network_egress`.

## Job path

1. GitHub App webhook `workflow_job` / `queued` (or poll fallback).
2. If the job requests a configured label (`gh-runnerd`), generate a JIT config.
3. Create a copy-on-write overlay from the golden qcow2, attach a TAP, boot QEMU (`q35,accel=kvm`).
4. Guest agent connects, receives the JIT blob over the isolated channel, starts `./run.sh --jitconfig`.
5. Official runner takes the job. If `container:` is set it `docker pull`s (up to 3 times) and runs steps in that container. Otherwise steps run on the Ubuntu VM.
6. Job ends, overlay is destroyed.

## Isolation

- Jobs never run on the host.
- Host `docker.sock` is never mounted.
- Registry does not listen on a public NIC.
- VM-to-VM traffic on the bridge is dropped.
- VMs may NAT to the internet for GitHub and uncached pulls.
- Authenticated registry pulls are proxied and **not** written to the shared cache (so job A cannot leak a private image to job B).

## Images

| Layer | What | How you choose it |
| --- | --- | --- |
| Host OS | Ubuntu 24.04+ | install gh-runnerd here |
| Runner VM | Ubuntu 22.04/24.04/26.04 + runner + Docker (+ `ubuntu-latest` tools) | `runner-image bake --image ... --flavor ...` / Runnerfile |
| Job container | anything Docker can pull | `jobs.<id>.container.image` |
