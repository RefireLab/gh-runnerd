# Local registry and cache

`images/` is owned by gh-runnerd. Drop tarballs in `imports/` and run CLI commands.

```bash
./bin/gh-runnerd image list
./bin/gh-runnerd image pull alpine:3.22
./bin/gh-runnerd image import ./imports/my-ci.tar --name my-ci --tag 2026.08
./bin/gh-runnerd image add alpine alpine:3.22
./bin/gh-runnerd image inspect my-ci:2026.08
./bin/gh-runnerd image remove my-ci:2026.08
./bin/gh-runnerd image prune --dry-run
```

Import accepts `docker save` archives and OCI layout tarballs. Result:

```
Imported image

Name:       my-ci
Tag:        2026.08
Digest:     sha256:...
Reference:  gh-runnerd.local/my-ci:2026.08
```

## How `docker pull` hits the cache

Each VM has `/etc/hosts` entries for `gh-runnerd.local`, `dockerhub.gh-runnerd.local`, `ghcr.gh-runnerd.local`, and `quay.gh-runnerd.local` pointing at `10.87.0.1`. Docker's containerd `hosts.toml` redirects those registries to the embedded HTTPS endpoint.

- `alpine:3.22` → pull-through cache for Docker Hub
- `ghcr.io/org/img:tag` → pull-through cache for GHCR
- `gh-runnerd.local/my-ci:1` → pinned/imported store only (404 if missing)

The registry:

- listens only on the isolated bridge
- rejects push/PUT/POST/PATCH/DELETE from VMs
- verifies digests
- applies separate disk quotas for pinned images and cache
- does not let a job replace a pinned tag
- does not cache authenticated (private) pulls into the shared store
- garbage-collects unreferenced cache blobs via `image prune`

Optional Docker Hub username/token in config raises host-side rate limits. GitHub-hosted runners are exempt from Hub limits; **self-hosted runners are not**.

`init --with-examples` prints pull commands for `alpine:3.22`, `ubuntu:24.04`, `node:22-bookworm`, and `python:3.13-slim`.
