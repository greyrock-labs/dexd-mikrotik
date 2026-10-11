# Repo workflow

## Commits and pushes

- Commit straight to `main` as linear commits; no feature branches or pull
  requests unless asked.
- One focused commit per concern, Conventional Commits style:
  `fix(provider): ...`, `feat(source): ...`, `docs: ...`, `ci: ...`. Use two
  commits when a change has two kinds of content, for example code and an
  unrelated doc fix.
- Push without asking when the change is low-risk (docs, agent instructions,
  routine fixes). Ask first when a push or release could break a running
  deployment.
- Read prose end to end before the first commit. If a pushed commit needs
  fixing, amend or squash it while it is unreleased rather than stacking
  follow-up commits.

## Keep the documentation current

Every change updates the documentation it affects, in the same commit when
it is part of that change:

- `README.md` and `.env.example` for anything a user configures, sees or
  troubleshoots. `compose.yaml` stays in sync with both.
- `.agents/` for how to work on the repository: behaviour, design decisions,
  layout, commands, test conventions, and anything learned the hard way.

A change is not done until its documentation matches the code. When you find
documentation that is already wrong, fix it.

## Checks

`mise run <task>` runs the same checks CI does: `fmt-check`, `oxfmt-check`,
`tidy-check`, `vet`, `lint`, `build` and `test`. Run them all before pushing.
Run `mise run oxfmt` to format Markdown, YAML and JSON.

## Releases

Every commit that changes code gets a release; a docs-only change does not.
Follow `.agents/skills/cutting-a-release/SKILL.md`: signed annotated tag on
`origin/main`, wait for CI to pass first, then the release workflow publishes
`ghcr.io/greyrock-labs/dexd-mikrotik`.

## What docs, comments and commit messages may say

- Describe this repository only. No internal hostnames, IP addresses,
  usernames or 1Password item names, and no deployment details from other
  repositories. Use `example.com` and `192.0.2.x` in examples.
- No backstory: do not mention deleted repositories, abandoned approaches or
  earlier attempts. State the current behaviour and why.
- Grep the diff for internal details before the first push. Amending and
  force-pushing does not remove a leaked commit from the remotes.

## Remotes and tools

- `origin` is the Forgejo repository (see `git remote -v`), push-mirrored to
  GitHub (`greyrock-labs/dexd-mikrotik`).
- Use `tea` for Forgejo issues and pull requests; it is installed and
  authenticated. `gh` works for GitHub and GHCR (deleting package versions
  needs the `delete:packages` scope).
- Forgejo's web pages sit behind a bot check, so `curl` cannot read job logs;
  use the API (`/api/v1/repos/<owner>/<repo>/actions/tasks`) for run status,
  and a browser for logs.

## Renovate

- Renovate extends `local>todd/renovate-config` and names its branches
  `renovate-...`.
- Renovate only prunes branches with its configured prefix; it never removes
  `renovate/configure` after its onboarding pull request is closed by hand.
- The Renovate Dependency Dashboard issue is meant to stay open.
