# Troubleshooting

Run this first:

```bash
gh-runnerd doctor
```

| Symptom | Likely cause |
| --- | --- |
| `only runs on Ubuntu` | Host is not Ubuntu 24.04+. |
| `/dev/kvm missing` | Install qemu, enable VT-x/AMD-V, add user/device permissions. |
| `qemu-system-* not in PATH` | `apt install qemu-system-x86` (or `qemu-system-arm`). |
| `no Ubuntu 24.04 runner image` | `sudo gh-runnerd runner-image bake` (builds, imports, and activates it). |
| `github-auth` error | Set `github.token` or App id/key/installation. |
| Job sits queued | Labels don't include `gh-runnerd`; webhook not reaching host and poll repo not set; pool at `max_concurrent`; JIT failed (check daemon logs). |
| `guest agent did not connect` | VM didn't boot in `vm.boot_timeout`; golden image missing guest unit; bridge/DHCP broken. |
| `docker pull` 429 | Docker Hub rate limit. Set `registry.dockerhub_username/token`, `image pull` the image once, or use `gh-runnerd.local/...`. |
| Alpine job fails on bash | Container default shell is `sh`. See [containers.md](containers.md). |
| Digest mismatch | Re-import the tar; `image inspect` shows `Digest OK: false`. |
| Registry connection refused from VM | Serve isn't running, or registry didn't bind `10.87.0.1:443` (bridge setup failed — needs root). |

Daemon logs go to stderr / journald:

```bash
journalctl -u gh-runnerd -f
```

`gh-runnerd status` reads `state/status.json` written while `serve` is running.
