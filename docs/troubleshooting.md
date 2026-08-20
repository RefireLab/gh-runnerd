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
| `guest agent did not connect` | VM didn't boot in `vm.boot_timeout`; golden image missing guest unit; bridge/DHCP broken; host has no `/dev/vsock` (guest then hangs on vsock and never reaches TCP `host_ip:5099`). |
| `docker pull` 429 | Docker Hub rate limit. Set `registry.dockerhub_username/token`, `image pull` the image once, or use `gh-runnerd.local/...`. |
| Alpine job fails on bash | Container default shell is `sh`. See [containers.md](containers.md). |
| Digest mismatch | Re-import the tar; `image inspect` shows `Digest OK: false`. |
| Registry connection refused from VM | Serve isn't running, or the registry didn't bind (bridge setup failed — needs root). Check `gh-runnerd doctor`. |
| `registry listen ...: address already in use` | Another service owns that port. Re-run `sudo gh-runnerd init` (it picks a free port) or set `registry.listen = "10.87.0.1:42443"` in the config. |
| VM network overlaps LAN/Docker/VPN | `gh-runnerd doctor` warns about it; re-run `sudo gh-runnerd init` and pick the suggested free subnet, then `sudo gh-runnerd runner-image update`. |
| JIT runners stay Offline, `MASQUERADE` counter stays 0 | Docker/ufw set `FORWARD` policy `DROP`. `serve` inserts ACCEPT rules so VMs can NAT. Restart `serve` (v0.2.9+) or add them by hand: `iptables -I FORWARD -i br-ghrunnerd ! -o br-ghrunnerd -j ACCEPT`. |
| Runner Offline although `status` shows `idle` | The VM has no IP. Images baked before v0.3.0 pinned netplan to the bake-time NIC (different PCI slot and MAC than at runtime), so networking never came up. Upgrade, then rebuild the image: `sudo gh-runnerd runner-image update`. |
| `selftest ... FAILED` in the serve log | `serve` probes the VM datapath (DHCP, control port, DNS, TCP 443) from the bridge before booting runners. Each failing line names the broken hop and the fix. Runner creation pauses and resumes automatically once a re-probe passes; `gh-runnerd status` shows `network_egress`. |
| `selftest dhcp FAILED` while dns/tcp443 pass | Either another process owns UDP :67 (the `dhcp server down` log line names it — commonly dnsmasq; scope it away with `except-interface=br-ghrunnerd`), or the host drops locally-generated broadcast toward bridge ports. The server replies both broadcast and (dnsmasq-style) L2-unicast via a pinned neighbor entry, and the probe verifies the unicast path too, so only a genuinely dead DHCP fails this step. |
| Pool fills to `max` with `busy` and no jobs | Pre-0.2.9: guest `job_started` made warm VMs look busy, so `MaintainIdle` kept booting more. Upgrade. |
| `Device or resource busy` on tap after restart | Pre-0.3.0 leftovers: killing `serve` orphaned QEMU processes holding the TAPs. `serve` now destroys its VMs on shutdown and removes orphans (matching `qemu-system.* -name <prefix>-N-N`), stale `tap-ghrd*`, and old overlays at startup. |

Daemon logs go to stderr / journald:

```bash
journalctl -u gh-runnerd -f
```

`gh-runnerd status` reads `state/status.json` written while `serve` is running.
