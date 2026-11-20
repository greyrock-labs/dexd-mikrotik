# AGENTS.md

## Purpose

`dexd-mikrotik` watches Docker containers labelled `dexd.enabled=true`, reads
their Traefik `Host()` rules, and reconciles A/CNAME records into MikroTik
RouterOS 7 static DNS over the REST API (`www-ssl`). Ownership is recorded in
companion TXT entries so it can share a router with external-dns.

Go module: `github.com/greyrock-labs/dexd-mikrotik`. Image: `ghcr.io/greyrock-labs/dexd-mikrotik`.

## Layout

```text
cmd/dexd-mikrotik/main.go         entry point, wiring
internal/config/                  env config
internal/source/                  Docker + Traefik label parsing
internal/plan/                    desired vs current diff and conflict detection
internal/registry/                TXT ownership encode/decode
internal/controller/              reconcile loop; Provider interface uses a neutral record type
internal/dns/                     neutral Record type shared by all packages
internal/provider/mikrotik/       RouterOS REST client + provider (mikrotiktest/ holds a simulator)
internal/metrics/                 Prometheus metrics
deploy/                           Grafana dashboard + PrometheusRule
```

mise is the task runner (`mise tasks` lists them): `mise run dev`,
`mise run test`, `mise run lint`, `mise run oxfmt-check`.

## Testing

```bash
mise run test
```

Unit tests run against the in-process RouterOS simulator
(`internal/provider/mikrotik/mikrotiktest`).

The live test is `TestLive_RoundTrip` in
`internal/provider/mikrotik/live_test.go`. It is skipped unless
`MIKROTIK_INTEGRATION=1`, and reads `MIKROTIK_BASEURL`, `MIKROTIK_USERNAME`,
`MIKROTIK_PASSWORD`, `MIKROTIK_SKIP_TLS_VERIFY` and `MIKROTIK_TEST_NAME`
(default `dexd-mikrotik-livetest.invalid`). It creates, updates and deletes an
A record, an ownership TXT and a CNAME at `c.<MIKROTIK_TEST_NAME>` on a real
router and cleans up after itself. CI never runs it.

## Release process

Releases go through a release pull request, never through hand-made tags.

- `.forgejo/workflows/release-pr.yaml` ("Prepare release") runs on every push to `main`. If any
  releasable commits have landed since the last `v*` tag, the `keymaker` bot
  force-pushes `release/next` with an updated `CHANGELOG.md` and keeps one PR
  open, titled `chore(release): vX.Y.Z`, with the release notes as its body.
  If nothing is releasable, it closes that PR. Running the workflow by hand
  (`workflow_dispatch`) releases at least a patch even when nothing
  releasable is pending.
- Merging that PR is the decision to release. `.forgejo/workflows/release.yaml`
  ("Publish release") runs only when `CHANGELOG.md` changes on `main`, or by
  hand to retry a failed release. It sees an untagged version at the top of `CHANGELOG.md`, runs the tests, builds
  `linux/amd64,linux/arm64`, pushes exactly `X.Y.Z` and `latest` to
  `ghcr.io/greyrock-labs/dexd-mikrotik`, signs the digest with cosign, tags the
  commit `vX.Y.Z`, and publishes the notes as a Forgejo release and a GitHub
  release on the mirror. Every step can be re-run safely.
- Pushes to `main` publish no images. There are no `:main`, `:sha-*` or `X.Y`
  tags.

Versions come from conventional commits (git-cliff, `cliff.toml`):

| Commit                                                             | Release |
| ------------------------------------------------------------------ | ------- |
| breaking change (`feat!:` or `BREAKING CHANGE:`)                   | major   |
| `feat:`                                                            | minor   |
| `fix:`, `perf:`, `fix(deps):`                                      | patch   |
| `chore:`, `ci:`, `docs:`, `test:`, `build:`, `refactor:`, `style:` | none    |

Renovate extends the shared preset but overrides its commit types in
`.renovaterc.json5`. Go modules and the Go toolchain are `fix(deps)`, which
releases a patch. Everything else is `chore` and never carries `!`.

The scripts behind this live in `.forgejo/scripts/`, and
`mise run release-scripts-test` tests them. Repo secrets: `RELEASE_BOT_TOKEN`
(the `keymaker` Forgejo token, with repository and issue write),
`GHCR_TOKEN` (a GitHub classic PAT with `write:packages` and `public_repo`, used
for image pushes and GitHub releases), and `COSIGN_PRIVATE_KEY` plus
`COSIGN_PASSWORD` (signing key; the public half is `cosign.pub`). Repo
variables `RELEASE_BOT_NAME` and `RELEASE_BOT_EMAIL` set the release commit's
author. Images carry
an SBOM but no provenance attestation. Pull requests run tests and lint via
`.forgejo/workflows/test.yaml` and `lint.yaml`.

## Safety invariants

- Never modify or delete a DNS entry that lacks our ownership TXT record. Unrelated entries are never touched.
- Conflicts are skipped, not resolved. For a hostname that is desired or owned, a conflict is more than one A/CNAME entry at the hostname, or another writer's ownership TXT at the `a-`/`cname-` key. For a desired hostname, it is also an A/CNAME without our TXT, or containers asking for both an A and a CNAME. They are warned about each reconcile and counted in `dexd_mikrotik_plan_changes{operation="conflict"}`.
- `comment=dexd-mikrotik` on entries is informational; ownership comes only from the TXT record.
- Startup exits 1 if `GET /rest/system/resource` fails.
- No internal hostnames or IPs in committed files; use `192.0.2.x` and `example.com`.
