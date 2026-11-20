#!/usr/bin/env bash
# release-pr.sh [--force] keeps the release/next branch and its pull request in
# step with main. With nothing releasable it closes the PR. --force releases
# at least a patch even when nothing releasable is pending.
#
# Needs FORGEJO_SERVER_URL, FORGEJO_REPOSITORY and RELEASE_BOT_TOKEN. The token
# needs read:user so the release commit can carry the bot's own name and email.
set -euo pipefail

: "${FORGEJO_SERVER_URL:?}" "${FORGEJO_REPOSITORY:?}" "${RELEASE_BOT_TOKEN:?}"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
api="$FORGEJO_SERVER_URL/api/v1"
repo_api="$api/repos/$FORGEJO_REPOSITORY"
branch="release/next"

call() {
  local method="$1" url="$2"
  shift 2
  curl -sS --fail-with-body -X "$method" -H "Authorization: token $RELEASE_BOT_TOKEN" \
    -H "Content-Type: application/json" "$url" "$@" \
    || { echo "Forgejo API call failed: $method $url" >&2; return 1; }
}

# A merged release PR leaves an untagged version at the top of CHANGELOG.md
# until the Release workflow tags it. Opening a new PR now would repeat it.
if "$here/release-version.sh" pending; then
  echo "$("$here/release-version.sh" top) is merged and waiting for the Release workflow; nothing to do."
  exit 0
fi

version="$("$here/next-version.sh" "$@")"
pr="$(call GET "$repo_api/pulls?state=open&limit=50" \
  | jq -r --arg b "$branch" '[.[] | select(.head.ref == $b)][0].number // empty')"

if [[ -z "$version" ]]; then
  echo "Nothing releasable since the last version tag."
  if [[ -n "$pr" ]]; then
    call PATCH "$repo_api/pulls/$pr" -d '{"state":"closed"}' >/dev/null
    echo "Closed release PR #$pr."
  fi
  call DELETE "$repo_api/branches/$branch" >/dev/null 2>&1 || true
  exit 0
fi

bot="$(call GET "$api/user")"
bot_name="$(jq -r .login <<<"$bot")"
bot_email="$(jq -r .email <<<"$bot")"

git switch -q -C "$branch"
"$here/update-changelog.sh" "$version"
git add CHANGELOG.md
git -c user.name="$bot_name" -c user.email="$bot_email" -c commit.gpgsign=false \
  commit -q -m "chore(release): $version"

scheme="${FORGEJO_SERVER_URL%%://*}"
host="${FORGEJO_SERVER_URL#*://}"
git push -q --force "$scheme://$bot_name:$RELEASE_BOT_TOKEN@$host/$FORGEJO_REPOSITORY.git" \
  "HEAD:refs/heads/$branch"

notes="$("$here/release-version.sh" notes)"
title="chore(release): $version"

if [[ -n "$pr" ]]; then
  call PATCH "$repo_api/pulls/$pr" \
    -d "$(jq -n --arg t "$title" --arg b "$notes" '{title: $t, body: $b}')" >/dev/null
  echo "Updated release PR #$pr for $version."
else
  pr="$(call POST "$repo_api/pulls" \
    -d "$(jq -n --arg t "$title" --arg b "$notes" --arg h "$branch" \
      '{title: $t, body: $b, head: $h, base: "main"}')" | jq -r .number)"
  echo "Opened release PR #$pr for $version."
fi
