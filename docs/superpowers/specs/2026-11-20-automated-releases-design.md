# Automated releases via a release pull request

Date: 2026-11-20. This records the design as it was agreed on that date. The
workflows and `AGENTS.md` are the current source of truth.

## Intent

Releasing dexd-mikrotik should need one human decision: merging a release pull
request that says "it is time to release everything since last time". The
version number, tag, release notes, image build, signing and publishing all
follow from commit messages. People pulling the image with Renovate should get
immutable version tags, honest semver, and release notes in their Renovate
PRs.

Development happens on Forgejo. GitHub is the public mirror and hosts the
images on GHCR.

Before this design, releases were cut by hand from signed `v*` tags, and
pushes to `main` also published moving `:main` and `:sha-*` image tags. The
last hand-cut tag is `v1.0.1`, so the first automated release is `1.0.2`.

## Goals

- A bot keeps one open release PR on Forgejo whenever there are releasable
  commits since the last version tag. It shows the next version, the new
  `CHANGELOG.md` section, and the release notes as its description.
- Merging it tests, builds, pushes and signs the image, creates the `vX.Y.Z`
  tag, and publishes the release with notes on Forgejo and on the GitHub
  mirror.
- Image tags: exactly `X.Y.Z` and `latest`. Nothing else.
- Renovate's PRs in this repo produce a release only when they change what
  ships in the image.

## Non-goals

- Moving development to GitHub, or depending on release-please, which only
  supports GitHub.
- Releasing without a human merge.

## Anti-goals

- Any image tag that moves other than `latest`: no `:main`, `:sha-*`, `X.Y`
  or `rolling`.
- Publishing anything from a commit whose tests fail.
- Hand-maintained version numbers anywhere in the repo.

## Approaches considered

1. **Release PR on Forgejo, built with git-cliff (chosen).** A workflow in this
   repo does what release-please does on GitHub. It costs about 100 lines of
   workflow plus a Forgejo bot account, and keeps development on Forgejo with
   a merge as the release decision.
2. **Move development to GitHub and use release-please with a GitHub App.**
   This is the common setup for GitHub-hosted projects and needs the least
   custom code. Rejected because development stays on Forgejo.
3. **Fully automatic release on every releasable push.** No human checkpoint.
   Rejected in favour of choosing when to release.

## Version rules

Conventional commits since the last `v*` tag reachable from `main`:

| Commit                                                             | Release |
| ------------------------------------------------------------------ | ------- |
| breaking change (`!` or `BREAKING CHANGE:`)                        | major   |
| `feat:`                                                            | minor   |
| `fix:`, `perf:`, `fix(deps):`                                      | patch   |
| `chore:`, `ci:`, `docs:`, `test:`, `build:`, `refactor:`, `style:` | none    |

## Workflows

- `test.yaml` and `lint.yaml`: unchanged. Test runs on PRs and pushes to
  `main`, so the release PR gets the same checks as any PR.
- `release-pr.yaml` (new), on push to `main` and `workflow_dispatch`:
  - Computes the next version with git-cliff. If nothing releasable is pending
    and the run wasn't dispatched, it closes any open release PR and stops.
  - Otherwise it regenerates `CHANGELOG.md` and commits it to the
    `release/next` branch as `chore(release): vX.Y.Z`. The branch is rebuilt
    from `main` each time and force-pushed. It then opens or updates the PR
    through the Forgejo API with the bot token. The PR title is
    `chore(release): vX.Y.Z` and its body is the release notes.
  - `workflow_dispatch` forces at least a patch bump. This covers releasing
    when only non-releasable commits are pending, as with the first automated
    release.
- `release.yaml` (rewritten), on push to `main`:
  - Reads the top version from `CHANGELOG.md`. If tag `vX.Y.Z` already exists,
    it stops. This works whatever merge style the PR used.
  - Runs the tests, then builds `linux/amd64,linux/arm64` and pushes `X.Y.Z`
    and `latest` with the OCI labels, annotations and SBOM.
  - Signs the pushed image by digest with cosign (see Image signing).
  - Creates the annotated `vX.Y.Z` tag on the merge commit with the bot token,
    and a Forgejo release with the notes.
  - Creates the GitHub release on the mirror with the same notes, once the
    mirror carries the tag.
  - There is no `v*` tag trigger, because tags come from CI.

## Renovate (this repo)

`.renovaterc.json5` extends the maintainer's shared Renovate preset, like the
maintainer's other Forgejo repos. That preset sets commit types by update size
(major → `feat!`, minor → `feat`, patch → `fix`), which would turn dependency
bumps into minor and major releases. This repo overrides it, and its rules win
because repo rules apply after presets:

- Go modules and the Go toolchain group (the `gomod` `go` directive plus the
  mise `go` tool, which sets the builder image) are always `fix(deps)`, which
  means a patch release.
- Everything else keeps the preset's non-releasing types (`ci`, `chore`).
- No Renovate commit carries a breaking `!`. A dependency's major bump doesn't
  break people running dexd-mikrotik. Only human `feat!:` commits make a major
  release.
- Repo-specific settings stay: Forgejo workflow paths for the
  `github-actions` manager, the Go toolchain group, and `gomodTidy`.

## Bot account and secrets

- A Forgejo bot user with write access to the repo. Its token, with
  repository and issue write scopes, is the repo secret `RELEASE_BOT_TOKEN`.
  Forgejo doesn't start workflows from actions taken with the built-in token,
  so the PR branch and the tag come from this account.
- `GH_RELEASE_TOKEN`: a GitHub token that can create releases on the mirror.
- `GHCR_TOKEN`: a GitHub token with `write:packages` for pushing images.

## Image signing

- Key-based cosign, because keyless signing needs an OIDC issuer that
  sigstore trusts, and self-hosted Forgejo isn't one.
- The maintainer generates the key pair locally, with cosign pinned in mise.
  The private key and its password are the Forgejo secrets
  `COSIGN_PRIVATE_KEY` and `COSIGN_PASSWORD`. The public key is committed as
  `cosign.pub`.
- Each release signs `ghcr.io/greyrock-labs/dexd-mikrotik@<digest>`. GHCR shows
  a `sha256-<digest>.sig` entry next to the version tag, which Renovate
  ignores.
- The README documents `cosign verify --key <raw cosign.pub URL> <image>:X.Y.Z`.
- If the key leaks: generate a new pair, replace the secrets and
  `cosign.pub`, and say so in the next release notes.

## Decisions

- Git tags keep the `v` prefix and image tags drop it.
- `CHANGELOG.md` starts at `1.0.2`. Earlier history isn't backfilled.
- `AGENTS.md`'s Release process describes this flow.
- When it is first enabled, the GHCR package has to be set public and linked
  to the repository once in the GitHub UI.

## Left to the builder

- The exact git-cliff config (`cliff.toml`) and release-notes template.
- How the GitHub release step waits for the mirror: poll with a timeout,
  then fall back to `--target <sha>`.
- Pinning git-cliff and cosign in `.mise/config.toml`. Actions stay SHA-pinned
  per the existing convention.
