# dexd-mikrotik design

Date: 2026-11-19. This records the design as it was agreed on that date. The
code and `AGENTS.md` are the current source of truth.

## Intent

Containers on a Docker host that are routed by Traefik need DNS records on the
network's MikroTik router. dexd-mikrotik watches Docker, reads hostnames from
Traefik router rules, and keeps matching A/CNAME records in RouterOS static
DNS, so nobody has to add them by hand.

## Goals

- Watch Docker for containers labelled `dexd.enabled=true`, read hostnames from
  Traefik `Host()` router rules, and reconcile A/CNAME records on Docker events
  and on a timer.
- Write to RouterOS 7 static DNS (`/ip/dns/static`) through the RouterOS REST
  API.
- Track ownership with companion TXT records, the way external-dns and its
  MikroTik provider do, so records dexd-mikrotik didn't create are never
  touched.
- Run CI/CD on Forgejo Actions and publish multi-arch OCI images to
  `ghcr.io/greyrock-labs/dexd-mikrotik`. The source is hosted on Forgejo and
  push-mirrored to `github.com/greyrock-labs/dexd-mikrotik`.

## Non-goals

- Any DNS backend other than RouterOS.
- Kubernetes or Helm packaging. Deployment is Docker Compose.
- Record types beyond A, CNAME and the TXT ownership records. No AAAA, MX, SRV
  or regexp records.

## Anti-goals (failure even if it "works")

- Modifying or deleting any RouterOS DNS entry that has no matching
  dexd-mikrotik ownership TXT record, such as hand-made entries, regexp
  entries, or entries other tools create.
- Breaking DNS resolution for clients during reconciles, for example with
  delete-then-create windows when an update would do.

## Constraints

- external-dns-provider-mikrotik is reference only. No code is copied from it.
  The RouterOS client is written fresh against RouterOS's REST API.
- RouterOS REST is served over `www-ssl`. The router's certificate may not
  cover every address it is reached on, so TLS verification can be turned off
  explicitly.
- A dedicated RouterOS user, in a group with `read,write,web,api,rest-api`,
  runs dexd-mikrotik. It must not share a user with external-dns.
- **external-dns may share the router.** It manages its own static entries and
  its own TXT ownership records. dexd-mikrotik's records must be
  distinguishable from them, and the two must coexist without interfering.
- Hand-made static entries on the router must never be touched.
- GHCR push credentials: a GitHub token with `write:packages`, stored as a
  Forgejo repository secret.

## Approaches considered (ownership tracking)

1. **Companion TXT records (chosen).** For each owned `foo.example.com`
   A/CNAME, a TXT record holds
   `heritage=dexd-mikrotik,dexd-mikrotik/owner=<TXT_OWNER>,dexd-mikrotik/resource=docker/<container>`.
   This is external-dns's TXT registry model, which its MikroTik provider
   relies on. Cost: two static entries per hostname.
2. **RouterOS `comment` field.** Ownership goes in the record's own comment:
   one entry per hostname, visible in WinBox. Rejected in favour of matching
   external-dns. Records still get a comment as a human-readable label (see
   below), but it isn't used for ownership.

## Design

### Code layout

```text
cmd/dexd-mikrotik/main.go         entry point, wiring
internal/config/                  env config
internal/source/                  Docker + Traefik label parsing
internal/plan/                    desired vs current diff
internal/registry/                TXT ownership encode/decode
internal/controller/              reconcile loop; Provider interface uses a neutral record type
internal/dns/                     neutral Record type shared by all packages
internal/provider/mikrotik/       RouterOS REST client + provider
internal/metrics/                 Prometheus metrics
deploy/                           Grafana dashboard + PrometheusRule
```

- Go module: `github.com/greyrock-labs/dexd-mikrotik`, matching the public
  mirror and image location so `go install` and Renovate references resolve.
- Binary and image name: `dexd-mikrotik`.

### Docker labels

`dexd.enabled` opts a container in. The `dexd.*` target, type and skip labels
override the defaults per container.

### Ownership TXT records

- Name: `{record_type}-{TXT_PREFIX}{hostname}`.
- Value:
  `heritage=dexd-mikrotik,dexd-mikrotik/owner=<TXT_OWNER>,dexd-mikrotik/resource=docker/<container>`.
- **Coexistence with external-dns.** external-dns names its TXT records with
  the same `<type>-<hostname>` pattern, and RouterOS allows several static
  entries with the same name. Registry lookups therefore consider every TXT at
  a name, and only accept one whose heritage is `dexd-mikrotik` with a matching
  owner. A TXT belonging to someone else is ignored, never updated or deleted.
  If a hostname is claimed by both tools (an A/CNAME exists with no
  dexd-mikrotik ownership TXT), dexd-mikrotik logs a conflict, reports it in a
  metric, and leaves it alone.
- **Duplicate A/CNAME entries at one name.** If a name has more than one
  A/CNAME entry, that is a conflict: log it, count it, and don't touch any
  entry at that name until only one is left. This never guesses which `.id` is
  ours, which is the only rule that can't end up modifying an entry
  dexd-mikrotik didn't create.
- TXT values are written unquoted, and the decoder accepts quoted and
  unquoted values. A live test confirmed that unquoted values round-trip
  byte-identical through REST and DNS.

### RouterOS provider

Talks to `/rest/ip/dns/static` using HTTP basic auth.

| Operation | Request                                                                                           |
| --------- | ------------------------------------------------------------------------------------------------- |
| List      | `GET /rest/ip/dns/static` (ignore entries with `regexp` or an unsupported `type`)                 |
| Create    | `PUT /rest/ip/dns/static` with `name`, `type`, and `address` / `cname` / `text`, `ttl`, `comment` |
| Update    | `PATCH /rest/ip/dns/static/{.id}`                                                                 |
| Delete    | `DELETE /rest/ip/dns/static/{.id}`                                                                |

- A records: `type` may be omitted or `A`. CNAME: `type=CNAME`, `cname=`.
  TXT: `type=TXT`, `text=`.
- TTL: RouterOS uses duration strings (`1d`, `5m`). `DEFAULT_TTL=auto` omits
  `ttl`, so RouterOS's default applies. An integer number of seconds is sent as
  `<n>s`.
- Comment: managed entries get `comment=dexd-mikrotik` so they stand apart
  from hand-made and external-dns entries in WinBox. It is informational only
  and never read for ownership.
- Errors: RouterOS returns JSON `{error, message, detail}` with 4xx/5xx. These
  map to typed errors, so the controller can tell not-found from other
  failures.
- `.id` values go into the path raw (`*B7`). RouterOS rejects a
  percent-encoded `.id` (`%2AB7`) with a 400.
- Startup checks the connection with `GET /rest/system/resource`, logs the
  RouterOS version and board, and exits 1 on auth or connectivity errors.
- An update to an existing `.id` uses `PATCH`, never delete-then-create, so
  records stay resolvable. Changing a record between A and CNAME still needs
  delete plus create.

### Configuration

The naming follows the external-dns MikroTik provider, so the variables look
familiar.

| Variable                                                                                                                       | Default    | Notes                                                                             |
| ------------------------------------------------------------------------------------------------------------------------------ | ---------- | --------------------------------------------------------------------------------- |
| `MIKROTIK_BASEURL`                                                                                                             | required   | e.g. `https://192.0.2.1`                                                          |
| `MIKROTIK_USERNAME`                                                                                                            | required   |                                                                                   |
| `MIKROTIK_PASSWORD`                                                                                                            | required   |                                                                                   |
| `MIKROTIK_SKIP_TLS_VERIFY`                                                                                                     | `false`    | verify by default; `true` is for addresses the router's certificate doesn't cover |
| `DEFAULT_TARGET`                                                                                                               | required   |                                                                                   |
| `TXT_OWNER`, `TXT_PREFIX`, `POLICY`, `DEFAULT_TTL`, `RECONCILE_INTERVAL`, `LOG_LEVEL`, `LOG_FORMAT`, `DRY_RUN`, `METRICS_ADDR` | see README |                                                                                   |

Metrics use the `dexd_mikrotik_` prefix. The Grafana dashboard and
PrometheusRule in `deploy/` match.

### Testing

- Unit tests for config, source, plan, registry and controller.
- An in-process RouterOS REST simulator covering list, create, patch and
  delete, error bodies, TTL formatting, and TXT values containing commas and
  quotes.
- An optional live integration test against a real router, gated by
  `MIKROTIK_INTEGRATION=1` plus the connection env vars. It creates and
  removes records under a throwaway test name and never runs in CI.
- `go test -race ./...` must pass, and `golangci-lint` stays configured.

### Tooling

- mise is the toolchain source of truth (`.mise/config.toml`), alongside the
  golangci-lint config, lefthook, editorconfig and markdown/oxfmt formatting.
- Renovate covers Go modules, the mise tools, the Dockerfile base images and
  the Forgejo workflow actions.
- `AGENTS.md` describes the project for coding agents, with no internal
  hostnames.

### CI/CD

Forgejo Actions in `.forgejo/workflows/`. `ubuntu-latest` runners handle test
and lint jobs. `docker` runners provide a privileged Docker-in-Docker sidecar
for image builds. Actions are pinned by SHA, with `https://github.com/...` for
anything not mirrored to the Forgejo default action host.

| Workflow    | Trigger                                       | Does                                                                                      |
| ----------- | --------------------------------------------- | ----------------------------------------------------------------------------------------- |
| `test.yaml` | PRs, pushes to `main`                         | `go vet`, `go test -race`, `golangci-lint`, fmt and tidy checks, `docker build` (no push) |
| `lint.yaml` | PRs                                           | actionlint on workflows, oxfmt check                                                      |
| release     | see `2026-11-20-automated-releases-design.md` |                                                                                           |

- The OCI `org.opencontainers.image.source` label and annotation link the GHCR
  package to the GitHub mirror.

### Dockerfile

A Go builder stage that cross-compiles for the target platform, then `scratch`
with CA certificates and a nobody user, and `ENTRYPOINT ["/dexd-mikrotik"]`.
Version and git SHA are injected through ldflags.

## Left to the builder

- Simulator design for the RouterOS REST contract tests.
- README content: setup, RouterOS user and permission setup, configuration
  table, labels, metrics.
