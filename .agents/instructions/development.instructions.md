# Development

dexd-mikrotik watches a Docker daemon and keeps static DNS entries on a
MikroTik RouterOS v7 router (`/ip dns static`, via the REST API) in step with
the hostnames its labelled containers expose. Supported record types are A
and CNAME, with native `dexd` TXT ownership records, the three change
policies, and Prometheus metrics on `/metrics`.

## Architecture

```
Docker daemon → label parser → plan → RouterOS /ip/dns/static
                                   ↕
                            TXT ownership registry
```

## Layout

| File                                               | Purpose                                                                        |
| -------------------------------------------------- | ------------------------------------------------------------------------------ |
| `cmd/dexd/main.go`                                 | Entry point, signal handling, wiring                                           |
| `internal/config/config.go`                        | Env-driven config with validation                                              |
| `internal/config/config_test.go`                   | Config validation tests, including policy, default TTL, and reconcile interval |
| `internal/source/endpoint.go`                      | Shared `Endpoint` type                                                         |
| `internal/source/traefik.go`                       | Extracts hostnames from Traefik labels                                         |
| `internal/source/docker.go`                        | Lists containers, streams Docker events                                        |
| `internal/provider/mikrotik/types.go`              | `DNSRecord` domain type, RouterOS wire type, wildcard ⇄ regexp mapping         |
| `internal/provider/mikrotik/client.go`             | RouterOS REST client for `/ip/dns/static` CRUD                                 |
| `internal/provider/mikrotik/errors.go`             | Typed MikroTik API/network/data errors                                         |
| `internal/provider/mikrotik/client_test.go`        | RouterOS wire-format tests via `httptest.NewServer`                            |
| `internal/registry/txt.go`                         | Native dexd TXT ownership encode/decode                                        |
| `internal/plan/plan.go`                            | Diffs desired vs current, produces Changes                                     |
| `internal/controller/controller.go`                | Reconcile loop with event debounce + periodic ticker                           |
| `internal/controller/types.go`                     | `Source` and `Provider` interfaces + domain `Event` type                       |
| `internal/controller/controller_test.go`           | Reconcile tests with in-process fakes, policy coverage, and debounce test      |
| `internal/controller/mikrotik_integration_test.go` | Fake source → real controller → real MikroTik client → fake RouterOS API tests |
| `internal/metrics/metrics.go`                      | Prometheus collectors and `/metrics` handler                                   |
| `internal/metrics/metrics_test.go`                 | Metrics handler smoke test                                                     |
| `Dockerfile`                                       | Cross-compiling multi-arch build → scratch final image; pins the Go builder    |
| `compose.yaml`                                     | Example deployment using the published GHCR image                              |
| `docker-bake.hcl`                                  | Image targets: `image-local` (default) and `image-all` (amd64+arm64, CI)       |
| `.env.example`                                     | Deployment env template; keep in sync with config and README                   |
| `deploy/prometheusrule.yaml`                       | Prometheus Operator alerts for reconciliation and provider health              |
| `deploy/grafana-dashboard.json`                    | Importable Grafana dashboard for dexd operational metrics                      |
| `internal/source/traefik_test.go`                  | Table tests for label → endpoint extraction                                    |
| `internal/source/docker_test.go`                   | Endpoints aggregation, name parsing, event filters                             |
| `internal/registry/txt_test.go`                    | TXT encode/decode round-trip + ownership tests                                 |
| `internal/plan/plan_test.go`                       | Diff engine: create/update/delete/replace + safety guards                      |
| `.mise/config.toml`                                | Pinned Go tooling and local/CI task definitions                                |
| `.forgejo/workflows/ci.yaml`                       | Forgejo CI: fmt/oxfmt/tidy/vet/lint and build/test via mise tasks              |
| `.forgejo/workflows/release.yaml`                  | Tag-driven (`v*`) release: tests, then bake-built image pushed to GHCR         |
| `.agents/skills/cutting-a-release/`                | How to cut a release: preflight, version choice, annotated tag notes           |

## Tooling and automation

- `mise` is the source of truth for the Go version and the build, test, formatting, lint, and module-tidiness tasks. Run `mise tasks` to discover them and `mise run <task>` locally. Node.js is pinned as the runtime required by oxfmt. CI runs the same tasks through `jdx/mise-action` with `--locked`, so `.mise/mise.lock` must stay current (`mise lock`) when tools change.
- The Dockerfile pins its `golang:<ver>-alpine` builder directly (no build arg). Renovate groups it with the `go.mod` directive and the mise Go tool as one `Go toolchain` update, so all three move together.
- Workflows run on the `docker` runner label and reference actions by full `https://github.com/...` URL pinned to a SHA.
- Releases: push a signed, annotated `v*` tag; the release workflow runs tests, derives the version from the tag, and bakes `image-all` to `ghcr.io/greyrock-labs/dexd-mikrotik` with semver/major.minor/major/sha tags (metadata-action adds `latest`). The runner has no QEMU; arm64 works because the Dockerfile cross-compiles and the runtime stage has no `RUN`. Requires `GHCR_USERNAME`/`GHCR_TOKEN` secrets. See `.agents/skills/cutting-a-release/SKILL.md`.
- The image `org.opencontainers.image.source` label and manifest annotation point at `https://github.com/greyrock-labs/dexd-mikrotik`; GHCR uses it to link the package to a repo.
- There is no Helm chart: dexd watches a Docker daemon through its socket, so it is deployed with Compose on the Docker host rather than on Kubernetes.
- Renovate extends `local>todd/renovate-config`.

## Key design decisions

- **Auth**: RouterOS REST basic auth. TLS verification is on by default; `MIKROTIK_SKIP_TLS_VERIFY` or `MIKROTIK_CA_CERT` handle self-signed router certificates.
- **Record type**: Inferred automatically from the resolved target — IPv4 → A, anything else → CNAME (AAAA explicitly out of scope). No `record-type` label exists; the target string is the single source of truth.
- **Opt-in labels**: Only `dexd.enabled=true` is required. Hostname extraction reads Traefik router rules, but `traefik.enable=true` is not a gate.
- **TXT ownership**: Optional global `TXT_PREFIX` env var (default `""`) is prepended to TXT record names. For example, with `TXT_PREFIX=talos.`, the companion TXT for `postgres.example.com` is `talos.a-postgres.example.com`; without a prefix it is `a-postgres.example.com`. `TXT_OWNER` defaults to `dexd` and scopes the records owned by an instance.
- **Policy**: `POLICY` defaults to `sync`. `sync` applies creates, updates, replacements, stale deletes, and orphan TXT cleanup. `upsert-only` applies creates, updates, and owned A/CNAME replacements, but skips stale deletes and orphan TXT cleanup. `create-only` applies only creates.
- **Metrics**: `METRICS_ADDR` defaults to `:8080` and exposes Prometheus metrics at `/metrics`. Set it to an empty string to disable the metrics HTTP server.
- **Ownership safety**: Records without matching owner TXT are never deleted.
- **Debounce**: Docker events are debounced 2s before triggering a reconcile to coalesce rapid restarts.
- **TXT values**: ownership TXT values are written unquoted, because RouterOS stores `text` literally; `DecodeTXT` still strips surrounding quotes.
- **TTL**: `DEFAULT_TTL` applies to every record dexd writes, TXT included, formatted as `<n>s`; `auto` omits `ttl` so RouterOS uses its default (`1d`).
- **Case**: hostnames are lower-cased in `internal/source`. RouterOS lower-cases queries, and regexp entries are case-sensitive.
- **Wildcard precedence**: RouterOS evaluates regexp entries before plain names, so a wildcard shadows more specific static entries beneath it.

## Label vocabulary

Container-level defaults apply to all routers from the container:

```yaml
dexd.enabled: "true"
dexd.target: "<ip-or-hostname>" # IPv4 → A, hostname → CNAME
```

Per-router overrides are matched by router name from `traefik.http.routers.<name>.rule`:

```yaml
dexd.routers.<name>.target: "<ip-or-hostname>"
dexd.routers.<name>.hostnames: "<hostname>[,<hostname>...]" # replaces parsed Host() names
dexd.routers.<name>.extra-hostnames: "<hostname>[,<hostname>...]" # appends to parsed Host() names
dexd.routers.<name>.skip: "true"
```

Standalone host blocks create records that are not tied to Traefik router labels:

```yaml
dexd.hosts.<name>.hostnames: "<hostname>[,<hostname>...]"
dexd.hosts.<name>.target: "<ip-or-hostname>"
dexd.hosts.<name>.skip: "true"
```

Precedence per router: `skip=true` → `hostnames` override or parsed `Host()` names + `extra-hostnames` → per-router target → container-level target → global default (`DEFAULT_TARGET`). Precedence per standalone block: `skip=true` → `hostnames` → block target → container-level target → global default. Record type is auto-detected from whichever target wins.

RouterOS has no wildcard names. `*.example.com` (A or CNAME) is written as a regexp entry `^.+\.example\.com$` and mapped back to `*.example.com` on list; only that exact generated form is recognised, so hand-written regexp entries are never managed. The TXT ownership key encodes `*` as `wildcard-dexd`. Non-leading wildcards are rejected by `ValidateRecord`; the controller logs a warning, increments `dexd_provider_errors_total{type="unsupported"}`, and continues.

## Wire format reference

RouterOS endpoint: `<MIKROTIK_BASEURL>/rest/ip/dns/static` — `GET` list, `PUT` create, `PATCH /<.id>` update, `DELETE /<.id>` delete.

```json
{ "name": "foo.example.com", "type": "A", "address": "192.0.2.10", "ttl": "300s" }
{ "name": "bar.example.com", "type": "CNAME", "cname": "foo.example.com" }
{ "name": "a-foo.example.com", "type": "TXT", "text": "heritage=dexd,..." }
{ "regexp": "^.+\\.foo\\.example\\.com$", "type": "A", "address": "192.0.2.10" }
```

RouterOS may omit `type` for A entries; the client infers A (or AAAA for IPv6 addresses).

TXT ownership value:

```
heritage=dexd,dexd/owner=dexd,dexd/resource=docker/myapp
```

The request shape is verified in `internal/provider/mikrotik/client_test.go` and `internal/controller/mikrotik_integration_test.go`.

## Test coverage

Run with `mise run test` or `go test -race ./...`. All tests use stdlib only (no testcontainers, no Docker daemon or real router needed).

| Package                      | What it covers                                                                                                                                                                                                                                                                                                              |
| ---------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/source`            | Label → endpoint extraction: `dexd.*` enable-flag gating and label vocabulary, standalone host blocks, single/multi `Host()`, `\|\|` joining, router hostname override/append labels, `HostRegexp` skip, unsubstituted `${VAR}` skip, multi-router merging, container/router target and record-type overrides, router skip. |
| `internal/registry`          | `TXTKey` and `ParseTXTKey` formatting/parsing, `EncodeTXT` never quoted, `DecodeTXT` round-trip + quote stripping, rejects non-heritage values, `IsOwnedBy` cross-owner matrix.                                                                                                                                             |
| `internal/plan`              | All Create/Update/Delete/Replace branches for A and CNAME plus the three safety rules: no update without our TXT, no update if TXT belongs to another owner, no delete without our TXT.                                                                                                                                     |
| `internal/provider/mikrotik` | RouterOS wire format: list mapping (missing `type`, regexp wildcards, foreign/unsupported entries skipped), basic auth, PUT/PATCH/DELETE URLs and bodies per type, `ttl` omitted for auto, typed API/network/data errors with RouterOS `detail`, dry-run makes no mutation calls, wildcard regexp round-trip.               |
| `internal/controller`        | Reconcile → apply flow: create/update/delete record+TXT pairs, CNAME pair creation, all three ownership safety rules, error-continues behaviour, debounce → reconcile event path, and integration tests through the real MikroTik client against a fake RouterOS API (including idempotency and foreign-entry safety).      |
| `internal/metrics`           | Prometheus handler and metrics for reconcile health, plan/change counts, source events, provider requests, provider errors, and build/config info.                                                                                                                                                                          |
| `internal/source` (docker)   | Endpoints aggregation across multiple containers, name slash-stripping, ID fallback, list error propagation, event filter correctness, channel pass-through.                                                                                                                                                                |

## Known gaps / future work

- [ ] **Real-router validation**: creating A/CNAME + TXT pairs and listing them back (including A entries returned without `type`) is confirmed against a real RouterOS v7 router, with no churn between reconciles. Still covered only by the in-process fakes: `PATCH`/`DELETE` on unescaped `.id` paths (e.g. `*1A`), and regexp wildcard entries resolving and round-tripping.
- [x] **Standalone non-Traefik hosts**: add explicit labels such as
      `dexd.hosts.<name>.*` for endpoints that do not have Traefik
      router rules.
- [x] **Prometheus metrics**: `/metrics` exports reconcile totals/duration/last success, reconcile errors, plan gauges, change counters, source errors/events, provider request metrics, provider errors, build info, and the standard Go/process runtime collectors. `dexd_plan_desired_records{record_type}` is the unique desired A/CNAME record count by type plus their companion TXT ownership records; the exporter intentionally does not expose the complete RouterOS static-DNS inventory.

## Dependency notes

- `github.com/docker/docker` — Docker SDK for Go; used for container listing and event streaming.
- `github.com/caarlos0/env/v11` — Declarative environment variable parsing for config.
- `github.com/prometheus/client_golang` — Prometheus collectors and HTTP handler.
- No external HTTP client lib — uses stdlib `net/http`.
- No external logging lib — uses stdlib `log/slog` (Go 1.21+).
- The Go version is pinned in `go.mod`, `.mise/config.toml` and the Dockerfile; Renovate moves them together.

## Maintenance notes

- Keep `.env.example`, `README.md`, and `compose.yaml` in sync whenever environment variables or deployment-facing label examples change. Users are expected to copy `.env.example` for real deployments.
