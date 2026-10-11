# dexd

External DNS, but for Docker!

`dexd` is a lightweight Go agent that watches your Docker daemon and
automatically creates/removes static DNS records on a **MikroTik RouterOS v7**
router (`/ip dns static`, via the REST API) for services exposed via Traefik.

It uses TXT ownership records so it can safely coexist with other DNS
automation (and hand-made entries) on the same router.

## How it works

1. Scans running containers opted in with `dexd.enabled=true`.
2. Extracts hostnames from `traefik.http.routers.<name>.rule: Host(\`...\`)` labels.
3. Creates an **A** or **CNAME** record and a companion **TXT ownership record**
   on the router for each hostname.
4. Listens for Docker lifecycle events (start/stop/remove) and reconciles on a
   periodic interval — records are removed when containers disappear and new
   ones appear within seconds.

### Ownership safety

For every managed record at `foo.example.com`, a TXT record is created at
`{record_type}-foo.example.com` with value:

```text
heritage=dexd,dexd/owner=<TXT_OWNER>,dexd/resource=docker/<container-name>
```

Records **without** a matching ownership TXT are never touched — records
created outside dexd are safe.

## Deployment

Images are published to `ghcr.io/greyrock-labs/dexd-mikrotik` for
`linux/amd64` and `linux/arm64`, tagged `X.Y.Z`, `X.Y`, `X`, `latest`, and
`sha-<commit>`. Pin a version in production.

Copy `.env.example` to `.env` next to [`compose.yaml`](compose.yaml) and fill
it in:

```bash
# .env
MIKROTIK_BASEURL=https://192.168.88.1
MIKROTIK_USERNAME=dexd
MIKROTIK_PASSWORD=<password>
DEFAULT_TARGET=192.0.2.10
```

```bash
docker compose up -d
```

dexd needs read-only access to the Docker socket, which `compose.yaml`
mounts. Start with `DRY_RUN=true` and check the planned changes in
`docker compose logs dexd` before letting it write to the router.

## Configuration

| Environment variable       | Default      | Description                                                                                      |
| -------------------------- | ------------ | ------------------------------------------------------------------------------------------------ |
| `MIKROTIK_BASEURL`         | **required** | RouterOS web service URL, e.g. `https://192.168.88.1` (the REST API lives under `/rest`)         |
| `MIKROTIK_USERNAME`        | **required** | RouterOS user name                                                                               |
| `MIKROTIK_PASSWORD`        | **required** | RouterOS user password                                                                           |
| `DEFAULT_TARGET`           | **required** | Default target. IPv4 → A record, hostname → CNAME                                                |
| `MIKROTIK_SKIP_TLS_VERIFY` | `false`      | Skip TLS verification (e.g. the router's self-signed certificate)                                |
| `MIKROTIK_CA_CERT`         | empty        | Path to a PEM CA certificate to trust in addition to the system pool                             |
| `TXT_OWNER`                | `dexd`       | Scopes TXT ownership; change if running multiple instances                                       |
| `TXT_PREFIX`               | empty        | Optional prefix for ownership TXT record names                                                   |
| `POLICY`                   | `sync`       | Change policy: `sync`, `upsert-only`, or `create-only`                                           |
| `DEFAULT_TTL`              | `auto`       | TTL in seconds for created/updated records. `auto` omits it so RouterOS uses its default (`1d`). |
| `RECONCILE_INTERVAL`       | `5m`         | How often to run a full reconcile                                                                |
| `LOG_LEVEL`                | `info`       | `debug`, `info`, `warn`, or `error`                                                              |
| `LOG_FORMAT`               | `text`       | `text` or `json`                                                                                 |
| `DRY_RUN`                  | `false`      | List current router records and log planned changes without mutating them                        |
| `METRICS_ADDR`             | `:8080`      | Address for the Prometheus metrics HTTP server. Empty disables metrics.                          |

### Policy

`POLICY` controls which planned changes are applied:

| Policy        | Creates | Updates | A/CNAME replacements | Deletes                               |
| ------------- | ------- | ------- | -------------------- | ------------------------------------- |
| `sync`        | yes     | yes     | yes                  | yes                                   |
| `upsert-only` | yes     | yes     | yes                  | no stale-record or orphan-TXT cleanup |
| `create-only` | yes     | no      | no                   | no                                    |

`upsert-only` allows A/CNAME replacements because they are updates by intent. The old owned record is deleted before the replacement record with the same hostname is created.

## Metrics

Prometheus metrics are exposed at `/metrics` on `METRICS_ADDR`. The example
Docker Compose file publishes the default metrics listener on host port `8080`.

Useful alerting metrics:

```promql
time() - dexd_reconcile_last_success_timestamp_seconds > 900
increase(dexd_reconcile_total{result="error"}[10m]) > 0
increase(dexd_changes_total{result="error"}[10m]) > 0
increase(dexd_provider_errors_total[10m]) > 0
```

`deploy/prometheusrule.yaml` includes alerts for stale successful reconciles, source/reconcile/change/provider errors, and slow reconciles. Apply it in clusters using the Prometheus Operator.

Import [`deploy/grafana-dashboard.json`](deploy/grafana-dashboard.json) into Grafana to monitor reconciliation health, managed DNS records, plans and applied DNS changes, Docker events, MikroTik provider requests/latency, errors, build information, and the Go process runtime. The dashboard includes datasource, `job`, and `instance` variables so it works with any Prometheus scrape configuration. **Managed MikroTik Records** counts the desired A/CNAME records and their companion TXT ownership records, broken down by `record_type`; it does not expose the complete RouterOS static-DNS inventory.

Useful dashboard metrics:

- `dexd_reconcile_total`
- `dexd_reconcile_duration_seconds`
- `dexd_reconcile_last_success_timestamp_seconds`
- `dexd_plan_desired_records{record_type}`
- `dexd_plan_changes`
- `dexd_changes_total`
- `dexd_provider_requests_total`
- `dexd_provider_request_duration_seconds`
- `dexd_provider_errors_total`
- `dexd_docker_events_total`
- `process_cpu_seconds_total`
- `process_resident_memory_bytes`
- `go_memstats_heap_alloc_bytes`
- `go_goroutines`
- `go_threads`

## Container labels

Add `dexd.enabled=true` to any service you want managed:

```yaml
labels:
  dexd.enabled: "true"
  traefik.http.routers.myapp.rule: Host(`myapp.example.com`)
  # ... other traefik labels
```

Multiple hosts in a single rule are all created:

```yaml
traefik.http.routers.myapp.rule: Host(`foo.example.com`) || Host(`bar.example.com`)
```

`HostRegexp(...)` entries are skipped — they cannot be materialized into DNS records.

### Record type detection

Record type is inferred automatically from the target value: an **IPv4 address** produces an **A record**, and a **hostname** produces a **CNAME**. There is no `record-type` label — the target string is the single source of truth.

A container-level target override applies to every router from that container:

```yaml
labels:
  dexd.enabled: "true"
  dexd.target: "traefik.example.com" # hostname → CNAME for all routers
```

Per-router target overrides take precedence, and each router's type is detected independently:

```yaml
labels:
  dexd.enabled: "true"
  traefik.http.routers.s3.rule: Host(`bucket.example.com`)
  traefik.http.routers.console.rule: Host(`console.example.com`)
  dexd.routers.console.target: "traefik.example.com"
  # bucket.example.com → A (uses DEFAULT_TARGET, an IP)
  # console.example.com → CNAME (hostname override)
```

Per-router hostname overrides can replace or extend hostnames parsed from
Traefik `Host(...)` rules:

```yaml
labels:
  dexd.enabled: "true"
  traefik.http.routers.rustfs.rule: Host(`${S3_HOST}`) || HostRegexp(`^.+\.${S3_HOST}$`)
  dexd.routers.rustfs.extra-hostnames: "*.${S3_HOST}"
```

Use `dexd.routers.<name>.hostnames` to replace parsed hostnames entirely, or
`dexd.routers.<name>.extra-hostnames` to append comma-separated names. Router
`skip=true` wins over hostnames, extra hostnames, and target overrides.

Standalone host blocks create records that are not tied to Traefik router
labels:

```yaml
labels:
  dexd.enabled: "true"
  dexd.hosts.dashboard.hostnames: "traefik.${DOMAIN}"
  dexd.hosts.dashboard.target: "192.0.2.10"
```

`dexd.hosts.<name>.target` is optional and falls back to container-level
`dexd.target`, then `DEFAULT_TARGET`. `dexd.hosts.<name>.skip=true` skips the
whole standalone block, including all configured hostnames.

### Wildcards

RouterOS static DNS has no wildcard names, so a hostname such as
`*.example.com` is written as a **regexp** entry, `^.+\.example\.com$`, with
no `name`. Both A and CNAME wildcards are supported. The ownership TXT record
replaces the `*` label with `wildcard-dexd`, e.g. `a-wildcard-dexd.example.com`.
Only a single leading `*.` label is supported; other wildcard forms are logged,
counted as `dexd_provider_errors_total{type="unsupported"}`, and skipped.

> **Caveat:** RouterOS checks regexp entries **before** plain entries. A
> wildcard therefore shadows more specific static entries beneath it: with
> `*.example.com` present, a static entry for `foo.example.com` on the router is
> not used. This differs from normal DNS wildcard semantics. Prefer wildcards
> on dedicated subdomains, such as `*.s3.example.com`.

dexd only manages regexp entries in exactly the form it writes. Hand-made
regexp entries (ad-blocking lists, for example) and `FWD`/`NXDOMAIN`/other
entry types are ignored.

### Native ownership and labels

`dexd` accepts only `dexd.*` labels and writes native `dexd` ownership TXT
records. Records created by versions before this breaking change are not
considered owned and must be removed or migrated manually before using
`POLICY=sync`.

## Preparing the router

dexd uses the RouterOS v7 REST API, which is served by the `www-ssl` (or
`www`) IP service. Enable HTTPS and create a dedicated user. A group with
`read`, `write`, `api` and `rest-api` is enough (drop `rest-api` on older
RouterOS 7 releases that do not have that policy):

```routeros
/certificate add name=local-root-cert common-name=local-cert key-usage=key-cert-sign,crl-sign
/certificate sign local-root-cert
/certificate add name=webfig common-name=192.168.88.1
/certificate sign webfig ca=local-root-cert
/ip service set www-ssl certificate=webfig disabled=no

/user group add name=dexd policy=read,write,api,rest-api
/user add name=dexd group=dexd password=<password>
```

With a self-signed certificate, set `MIKROTIK_SKIP_TLS_VERIFY=true` or export
the CA and point `MIKROTIK_CA_CERT` at it. Plain HTTP (`http://`, `www`
service) also works but sends credentials in cleartext.

Records written by dexd look like this on the router:

```routeros
/ip dns static print where name~"example.com" or regexp~"example"
```

TXT ownership values are stored unquoted, e.g.
`heritage=dexd,dexd/owner=dexd,dexd/resource=docker/myapp`. Values wrapped in
double quotes are also recognized.

## Development

[mise](https://mise.jdx.dev) pins the toolchain (Go, golangci-lint, oxfmt) and
defines the tasks CI runs. `mise tasks` lists them.

```bash
mise run test          # unit + integration tests, race detector
mise run lint          # golangci-lint
mise run dev           # run against your router, loading .env
mise run docker-build  # docker buildx bake -> dexd-mikrotik:local
```

## Releases

CI (`.forgejo/workflows/ci.yaml`) runs formatting, tidiness, vet, lint, build,
and tests on every push to `main` and on pull requests.

Releases are cut by pushing an annotated `v*` tag. `.forgejo/workflows/release.yaml`
runs the tests, then builds the multi-arch image with `docker-bake.hcl` and
pushes it to GHCR. The tag is the only version input; see
[`.agents/skills/cutting-a-release`](.agents/skills/cutting-a-release/SKILL.md).

The Forgejo repository needs `GHCR_USERNAME` and `GHCR_TOKEN` secrets (a token
with `write:packages` on the `greyrock-labs` organization).

## Credits

Derived from [ishioni/dexd](https://github.com/ishioni/dexd). The RouterOS
provider was informed by
[external-dns-provider-mikrotik](https://github.com/mirceanton/external-dns-provider-mikrotik).
See [`NOTICE`](NOTICE).
