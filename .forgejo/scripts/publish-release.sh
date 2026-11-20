#!/usr/bin/env bash
# publish-release.sh vX.Y.Z tags the current commit and publishes the release
# notes from CHANGELOG.md on Forgejo and on the GitHub mirror. Each step skips
# what already exists, so a failed run can be re-run.
#
# Needs FORGEJO_SERVER_URL, FORGEJO_REPOSITORY, RELEASE_BOT_TOKEN,
# GITHUB_MIRROR (owner/repo) and GH_TOKEN.
set -euo pipefail

version="${1:?usage: $0 vX.Y.Z}"
: "${FORGEJO_SERVER_URL:?}" "${FORGEJO_REPOSITORY:?}" "${RELEASE_BOT_TOKEN:?}"
: "${GITHUB_MIRROR:?}" "${GH_TOKEN:?}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_api="$FORGEJO_SERVER_URL/api/v1/repos/$FORGEJO_REPOSITORY"
sha="$(git rev-parse HEAD)"
notes_file="$(mktemp)"
"$here/release-version.sh" notes >"$notes_file"

status() {
  curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: token $RELEASE_BOT_TOKEN" "$1"
}

call() {
  local method="$1" url="$2"
  shift 2
  curl -sS --fail-with-body -X "$method" -H "Authorization: token $RELEASE_BOT_TOKEN" \
    -H "Content-Type: application/json" "$url" "$@" \
    || { echo "Forgejo API call failed: $method $url" >&2; return 1; }
}

if [[ "$(status "$repo_api/tags/$version")" == "404" ]]; then
  call POST "$repo_api/tags" \
    -d "$(jq -n --arg t "$version" --arg s "$sha" '{tag_name: $t, target: $s, message: $t}')" >/dev/null
  echo "Tagged $sha as $version."
else
  echo "Tag $version already exists."
fi

if [[ "$(status "$repo_api/releases/tags/$version")" == "404" ]]; then
  call POST "$repo_api/releases" \
    -d "$(jq -n --arg t "$version" --rawfile b "$notes_file" '{tag_name: $t, name: $t, body: $b}')" >/dev/null
  echo "Published the Forgejo release $version."
else
  echo "Forgejo release $version already exists."
fi

if gh release view "$version" -R "$GITHUB_MIRROR" >/dev/null 2>&1; then
  echo "GitHub release $version already exists."
  exit 0
fi

# The push mirror carries the tag to GitHub shortly after it is created.
target=()
for _ in $(seq 1 20); do
  if gh api "repos/$GITHUB_MIRROR/git/ref/tags/$version" >/dev/null 2>&1; then
    break
  fi
  sleep 15
done
if ! gh api "repos/$GITHUB_MIRROR/git/ref/tags/$version" >/dev/null 2>&1; then
  echo "The mirror has not carried $version after 5 minutes; creating it on GitHub at $sha."
  target=(--target "$sha")
fi

gh release create "$version" -R "$GITHUB_MIRROR" --title "$version" \
  --notes-file "$notes_file" "${target[@]}"
echo "Published the GitHub release $version."
