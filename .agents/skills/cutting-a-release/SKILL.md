---
name: cutting-a-release
description: Use when releasing this repo — "cut a release", "tag a release", "ship it", "send it", "publish", "bump the version", or any request to get merged work onto GHCR. Covers committing to main, choosing the version, writing the signed annotated tag, and pushing it to fire the Forgejo release workflow.
---

# Cutting a Release

## Core principle

**The git tag is the only version input.** Nothing in the tree carries a real
version. `docker/metadata-action` takes the image tags from it, and the
release job passes it into the build as `VERSION`, which is linked into the
binary as `main.Version` (logged at startup, exported as
`dexd_build_info`). There is nothing to bump before tagging — the tag _is_
the bump.

## Standing preferences

Following these is not a judgment call:

- **Commit straight to `main`.** No feature branch, no PR, no "should I?" The
  remote is Forgejo, so `gh` does not work here; use `tea` if a PR is asked for.
- **"Send it" / "ship it" means commit, push `main`, and tag a release.** All
  of it, in one go. Do not stop after the push and offer the tag as a next step.
- Do not open PRs on this remote unless asked.
- **Never hold a release back waiting for a fix to be confirmed by eye.**
  Deployments run the released image, so an untagged commit is invisible there.
  The release _is_ how the change becomes testable against the router --
  withholding the tag until it can be seen is circular. Get the preflight right
  and tag.

## Procedure

```bash
# 1. Preflight — every one of these must pass before tagging
git status --short                      # clean tree
git rev-parse HEAD origin/main          # identical; push main first if not
mise run fmt-check && mise run oxfmt-check && mise run tidy-check
mise run vet && mise run lint
mise run test                           # what the release job runs; fix before tagging
# ...and CI on origin/main must have passed for HEAD. Check with the Forgejo API:
#   /api/v1/repos/<owner>/<repo>/actions/tasks, filtered on head_sha

# 2. What is going into this release
git describe --tags --abbrev=0 --match 'v*'   # last release tag
git log --oneline "$(git describe --tags --abbrev=0 --match 'v*')"..main

# 3. Tag (signed and annotated, message from a heredoc — never -m, never lightweight)
git tag -s v0.5.1 -F - <<'EOF'
...message, see template below...
EOF

# 4. Fire the release
git push origin v0.5.1
```

Then say the workflow is building, with a link to the repository's Actions
page on Forgejo.

Pushing the tag publishes a multi-arch (amd64 + arm64) image to
`ghcr.io/greyrock-labs/dexd-mikrotik`, tagged `X.Y.Z`, `X.Y`, `X`,
`latest`, and `sha-<short>`. Published packages are not really retractable,
so get the preflight right rather than planning to fix it after.

Only `v*` tags are releases -- hence `--match 'v*'`.

## Choosing the version

| Bump  | When                                                                                                                                                                      |
| ----- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Patch | Fixes, docs-in-image, internal changes with no config or behaviour change                                                                                                 |
| Minor | Features, or dependency **majors** landing — even when behaviour is identical                                                                                             |
| Major | An existing deployment has to change something: env vars renamed/removed, label vocabulary broken, TXT ownership format changed so existing records stop being recognised |

**Every commit on `main` that the release builds from belongs to a release.**
A dependency bump that cannot change behaviour still gets a version -- it is a
build input, and the alternative is a deployed commit with no version naming
it. A commit that no build ever reads does not get one: `.agents/`, the
README, `AGENTS.md`, `deploy/` (dashboards and alert rules are copied by hand,
not published), CI config. The image is built from `cmd/`, `internal/`,
`go.mod`, `go.sum`, and the `Dockerfile`; nothing else can reach a published
artifact, so nothing else needs a version. Do not ask about this -- the rule
decides it. A `feat(...)!:` commit is a breaking _dependency_, not necessarily
a breaking _deployment_ — judge the bump by what an existing deployment has to
do, and say so in the notes.

Changes to TXT ownership encoding or regexp wildcard form deserve special
care: records written by the previous version that the new version no longer
recognises are orphaned -- never deleted, but never updated either. That is a
major bump with migration steps in `Upgrade notes`.

## Tag message template

Title is the bare version. Optional lead paragraph only when the bump level
needs defending. Then sections, in this order, omitting any that are empty:
`Fixed`, `Added`, `Dependencies`, `Documentation`, `Upgrade notes`.

```
v0.5.1

Minor rather than a patch because a dependency major landed. Nothing in the
environment variables, labels, or record format changed, so upgrading from
0.5.0 is still just an image bump.

Fixed

- <What a user would notice, then why it happened and what the fix actually
  does. One bullet per change, wrapped at 79 columns.>

Dependencies

- <name> <old> -> <new>. <What changed at the call site, and why the behaviour
  is unchanged.>

Upgrade notes

None. No environment variables, labels, or DNS record formats changed.
```

House style -- match it:

- Written for someone deciding whether to upgrade, not for a changelog robot.
  Explain the _why_, not just the what.
- Say what did **not** change. "The records written on the router are
  identical", "no new environment variables."
- `Upgrade notes` ends the message and is never omitted. When there is nothing
  to do, say so explicitly: "Upgrading from 0.5.0 needs only the image tag
  bumped in compose.yaml."
- Commits on `main` that net to nothing (a change and its revert) get a line
  saying so, not silence.
- Wrap at 79 columns.

## Never

- **Never hand-edit a version to cut a release.** `main.Version` defaults to
  `dev` and `docker-bake.hcl` defaults `VERSION` to `dev`; both are
  placeholders the release job overrides from the tag. If you catch yourself
  editing a version field, the tag is what you wanted.
- **Never use a lightweight or unsigned tag.** Every release tag is signed,
  annotated and carries the notes; `git tag v0.5.1` with no `-s` silently
  produces a release with no story.
- **Never tag a commit that is not on `origin/main`.** The workflow checks out
  the tag from the remote; a local-only commit builds nothing or builds wrong.
- Never re-point or force-push an existing tag. Cut the next patch instead.

## Common mistakes

| Mistake                               | What happens                                         |
| ------------------------------------- | ---------------------------------------------------- |
| Branch + PR for a one-line fix        | Wasted round trip; this repo commits to main         |
| Stopping after `git push origin main` | Half a release; "send it" included the tag           |
| `git tag -m "v0.5.1"`                 | Release with no notes, and the style is lost         |
| Unprefixed tag (`0.5.1`)              | Release workflow never fires -- it only matches `v*` |
| Tagging with a dirty tree             | The tag's tree is not what you tested                |
