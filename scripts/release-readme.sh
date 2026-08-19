#!/usr/bin/env bash
# Generate .release/README.md: the repo README with the release version
# stamped under the title. Included in release archives by .goreleaser.yaml.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  VERSION="$(git describe --tags --exact-match 2>/dev/null || git describe --tags --always 2>/dev/null || echo dev)"
fi
VERSION="${VERSION#v}"

mkdir -p .release
{
  head -n 1 README.md
  echo
  echo "**Version ${VERSION}** · by [RefireLab](https://refirelab.com/) · [github.com/RefireLab/gh-runnerd](https://github.com/RefireLab/gh-runnerd)"
  tail -n +2 README.md
} > .release/README.md

echo "generated .release/README.md (version ${VERSION})"
