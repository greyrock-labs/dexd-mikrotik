#!/usr/bin/env bash
# update-changelog.sh vX.Y.Z adds the unreleased commits to CHANGELOG.md as
# that version. The first run writes the file with its header; later runs
# prepend below the header, which only matches a file git-cliff wrote itself.
set -euo pipefail

tag="${1:?usage: $0 vX.Y.Z}"

if [[ -f CHANGELOG.md ]]; then
  git cliff --unreleased --tag "$tag" --prepend CHANGELOG.md 2>/dev/null
else
  git cliff --unreleased --tag "$tag" --output CHANGELOG.md 2>/dev/null
fi
