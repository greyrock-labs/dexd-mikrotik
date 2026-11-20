#!/usr/bin/env bash
# release-version.sh top      prints the top CHANGELOG.md version as vX.Y.Z
# release-version.sh pending  exits 0 if that version has no tag yet
set -euo pipefail

top() {
  [[ -f CHANGELOG.md ]] || return 0
  grep -m1 -oE '^## \[[0-9]+\.[0-9]+\.[0-9]+\]' CHANGELOG.md | sed -E 's/^## \[(.*)\]$/v\1/' || true
}

case "${1:-}" in
  top)
    top
    ;;
  pending)
    v="$(top)"
    [[ -n "$v" ]] || exit 1
    if git rev-parse -q --verify "refs/tags/$v" >/dev/null; then
      exit 1
    fi
    ;;
  *)
    echo "usage: $0 top|pending" >&2
    exit 2
    ;;
esac
