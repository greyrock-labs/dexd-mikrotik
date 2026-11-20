#!/usr/bin/env bash
# Tests for next-version.sh and release-version.sh against throwaway repos.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../.." && pwd)"
next="$here/next-version.sh"
release="$here/release-version.sh"
failures=0

fail() {
  echo "FAIL: $1" >&2
  failures=$((failures + 1))
}

# new_repo creates a repo with one commit tagged v1.0.1 and cds into it.
new_repo() {
  local dir
  dir="$(mktemp -d)"
  cd "$dir"
  git init -q -b main
  git config user.name test
  git config user.email test@example.com
  git config commit.gpgsign false
  git config tag.gpgsign false
  cp "$root/cliff.toml" .
  git add cliff.toml
  git commit -q -m "chore: initial"
  git tag -a v1.0.1 -m v1.0.1
}

commit() {
  git commit -q --allow-empty -m "$1"
}

expect_next() {
  local name="$1" want="$2"
  shift 2
  local got
  got="$("$next" "$@")"
  [[ "$got" == "$want" ]] || fail "$name: next-version.sh $* printed '$got', want '$want'"
}

new_repo
commit "ci: tweak workflow"
commit "docs: fix typo"
commit "chore(deps): update action actions/checkout (v7.0.1 ➔ v7.1.0)"
expect_next "nothing releasable" ""
expect_next "nothing releasable, forced" "v1.0.2" --force

new_repo
commit "fix(deps): update module example.com/x (v1.2.0 ➔ v1.3.0)"
expect_next "dependency fix" "v1.0.2"

new_repo
commit "feat: add y"
expect_next "feature" "v1.1.0"

new_repo
commit "feat!: drop z"
expect_next "breaking" "v2.0.0"

new_repo
commit "chore(release): v1.0.2"
expect_next "release commit alone" ""

new_repo
printf '# Changelog\n\n## [1.0.2] - 2026-11-20\n\n### Bug Fixes\n\n- thing\n\n## [1.0.1] - 2026-11-19\n' > CHANGELOG.md
got="$("$release" top)"
[[ "$got" == "v1.0.2" ]] || fail "top: printed '$got', want 'v1.0.2'"
"$release" pending || fail "pending: want exit 0 when v1.0.2 is untagged"
git add CHANGELOG.md
commit "chore(release): v1.0.2"
git tag -a v1.0.2 -m v1.0.2
if "$release" pending; then fail "pending: want exit 1 when v1.0.2 is tagged"; fi

new_repo
"$here/update-changelog.sh" v1.0.2
grep -qx '# Changelog' CHANGELOG.md || fail "update-changelog: header missing"
grep -q 'Maintenance release with no user-facing changes.' CHANGELOG.md \
  || fail "update-changelog: empty release has no maintenance note"
git add CHANGELOG.md
commit "chore(release): v1.0.2"
git tag -a v1.0.2 -m v1.0.2
commit "fix: handle empty label"
commit "feat(source): support x"
"$here/update-changelog.sh" "$("$next")"
[[ "$(grep -c '^# Changelog$' CHANGELOG.md)" == "1" ]] || fail "update-changelog: header duplicated"
[[ "$("$release" top)" == "v1.1.0" ]] || fail "update-changelog: top is '$("$release" top)', want 'v1.1.0'"
grep -q '^## \[1.0.2\]' CHANGELOG.md || fail "update-changelog: earlier release dropped"
grep -q -- '- \*\*source:\*\* Support x' CHANGELOG.md || fail "update-changelog: feature missing"
"$release" pending || fail "pending: want exit 0 after update-changelog adds v1.1.0"

new_repo
got="$("$release" top)"
[[ -z "$got" ]] || fail "top without CHANGELOG.md: printed '$got', want nothing"
if "$release" pending; then fail "pending without CHANGELOG.md: want exit 1"; fi

if ((failures > 0)); then
  echo "$failures failure(s)" >&2
  exit 1
fi
echo "release scripts: all tests passed"
