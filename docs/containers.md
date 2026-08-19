# Job containers

The official GitHub runner inside the disposable VM implements `jobs.<job_id>.container`. gh-runnerd does not patch that path.

## Any image, one runner label

```yaml
jobs:
  test:
    runs-on: gh-runnerd
    container:
      image: alpine:3.22
    steps:
      - run: apk add --no-cache git curl
```

```yaml
jobs:
  test:
    runs-on: gh-runnerd
    container:
      image: node:22-bookworm
    steps:
      - uses: actions/checkout@v4
      - run: npm ci
      - run: npm test
```

Private GHCR:

```yaml
permissions:
  contents: read
  packages: read
jobs:
  build:
    runs-on: gh-runnerd
    container:
      image: ghcr.io/my-company/private-ci:2026.08
      credentials:
        username: ${{ github.actor }}
        password: ${{ secrets.GITHUB_TOKEN }}
    steps:
      - run: ./build.sh
```

Digest pin:

```yaml
container:
  image: ghcr.io/my-company/private-ci@sha256:abc123...
```

Local import:

```yaml
container:
  image: gh-runnerd.local/my-ci:2026.08
```

## Default shell is `sh` inside containers

GitHub runs `run:` with `sh` in a job container, not `bash`. Alpine often has no bash. Either write POSIX scripts or set:

```yaml
defaults:
  run:
    shell: bash
```

and use an image that actually contains bash (`node:22-bookworm`, `ubuntu:24.04`, not a tiny Alpine).

## Alpine is fine as a container, not as the runner OS

Good: small shell jobs, static Go builds, Docker actions.
Bad: expecting glibc binaries, CodeQL, or a universal `ubuntu-latest` replacement.

## `docker/build-push-action`

That action talks to Docker on the **runner machine**. Use it **without** `container:`:

```yaml
jobs:
  build:
    runs-on: gh-runnerd
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/build-push-action@v6
        with:
          context: .
          load: true
```

Inside a job container there is no Docker socket unless you explicitly mount one (not recommended).

## `services:`

Postgres, Redis, and friends work the same as on GitHub-hosted runners: the official runner creates sibling containers on a Docker network inside the VM.
