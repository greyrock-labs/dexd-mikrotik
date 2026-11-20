# AGENTS.md

## Purpose

`dexd-mikrotik` watches Docker containers labelled `dexd.enabled=true`, reads
their Traefik `Host()` rules, and reconciles A/CNAME records into MikroTik
RouterOS 7 static DNS over the REST API (`www-ssl`). Ownership is recorded in
companion TXT entries so it can share a router with external-dns.

Go module: `github.com/doonga/dexd-mikrotik`. Image: `ghcr.io/doonga/dexd-mikrotik`.

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

`.forgejo/workflows/release.yaml` publishes the image to GHCR. Pushes to `main`
publish `ghcr.io/doonga/dexd-mikrotik:main` and `:sha-<short>`. A hand-cut
signed tag publishes `X.Y.Z`, `X.Y` and `latest`:

```bash
git tag -s vX.Y.Z -m "vX.Y.Z" && git push origin vX.Y.Z
```

Only `v*` tags produce versioned tags and `latest`; `main` never does. A
pre-release tag such as `vX.Y.Z-rc.1` publishes only `X.Y.Z-rc.1` (no `X.Y`, no
`latest`). Images carry an SBOM but no provenance attestation. Cut
`v0.1.0` only after the live RouterOS test passes. The
workflow needs the repo secret `GHCR_TOKEN`, a GitHub classic PAT with
`write:packages`. Pull requests run tests and lint via
`.forgejo/workflows/test.yaml` and `lint.yaml`.

## Safety invariants

- Never modify or delete a DNS entry that lacks our ownership TXT record. Unrelated entries are never touched.
- Conflicts are skipped, not resolved. For a hostname that is desired or owned, a conflict is more than one A/CNAME entry at the hostname, or another writer's ownership TXT at the `a-`/`cname-` key. For a desired hostname, it is also an A/CNAME without our TXT, or containers asking for both an A and a CNAME. They are warned about each reconcile and counted in `dexd_mikrotik_plan_changes{operation="conflict"}`.
- `comment=dexd-mikrotik` on entries is informational; ownership comes only from the TXT record.
- Startup exits 1 if `GET /rest/system/resource` fails.
- No internal hostnames or IPs in committed files; use `192.0.2.x` and `example.com`.
