#!/usr/bin/env bash
# Prints the next release tag (vX.Y.Z) if anything releasable has landed since
# the last version tag, and nothing otherwise. With --force it always prints a
# version, bumping at least the patch.
set -euo pipefail

force=false
if [[ "${1:-}" == "--force" ]]; then
  force=true
fi

# cliff.toml skips every commit that shouldn't release, so whatever is left
# in the unreleased section is releasable.
count="$(git cliff --unreleased --strip all --body '{{ commits | length }}' 2>/dev/null | tr -d '[:space:]')"

if [[ "${count:-0}" == "0" ]] && [[ "$force" != true ]]; then
  exit 0
fi

current="$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' 2>/dev/null || echo v0.0.0)"
next="$(git cliff --bumped-version 2>/dev/null)"

if [[ "$next" == "$current" ]]; then
  IFS=. read -r major minor patch <<<"${current#v}"
  next="v${major}.${minor}.$((patch + 1))"
fi

echo "$next"
