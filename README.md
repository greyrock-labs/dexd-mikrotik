# dexd-mikrotik

External DNS, but for Docker and MikroTik.

`dexd-mikrotik` is a lightweight Go agent that watches your Docker daemon and
automatically creates and removes static DNS records on a **MikroTik RouterOS 7**
router for services exposed via Traefik. It talks to the router over the
RouterOS REST API.

It uses TXT ownership records so it can safely coexist with other DNS
automation, such as external-dns, pointing at the same router.

## How it works

1. Scans running containers opted in with `dexd.enabled=true`.
2. Extracts hostnames from `traefik.http.routers.<name>.rule: Host(\`...\`)` labels.
3. Creates an **A** or **CNAME** static DNS entry and a companion **TXT
   ownership entry** on the router for each hostname.
4. Listens for Docker lifecycle events (start/stop/remove) and reconciles on a
   periodic interval. Entries are removed when containers disappear and new
   ones appear within seconds.

At startup it calls `GET /rest/system/resource` and exits with status 1 if the
router is unreachable or the credentials are wrong.

### Ownership safety

For every managed entry at `foo.example.com`, a TXT entry is created at
`<type>-foo.example.com` (for example `a-foo.example.com` or
`cname-foo.example.com`) with an unquoted value:

```text
heritage=dexd-mikrotik,dexd-mikrotik/owner=<TXT_OWNER>,dexd-mikrotik/resource=docker/<container-name>
```

Entries created by `dexd-mikrotik` also carry `comment=dexd-mikrotik`. The
comment is informational only; ownership is decided by the TXT record.

Entries without a matching ownership TXT are never touched, and unrelated
entries are never touched.

### Conflicts and coexistence with external-dns

Because ownership is recorded per hostname, `dexd-mikrotik` can share a router
with external-dns or another `dexd-mikrotik` instance with a different
`TXT_OWNER`. When a hostname that is desired (or already owned) is in any of
these states, it is a **conflict**:

- more than one A/CNAME entry exists for the hostname (desired or owned);
- another writer's ownership TXT (external-dns, or another `dexd-mikrotik`
  owner) exists at the `a-` / `cname-` key (desired or owned);
- an A/CNAME entry exists without our ownership TXT (desired hostnames only);
- containers ask for the hostname as both an A and a CNAME (desired hostnames
  only).

Conflicting hostnames are skipped, a warning is logged on every reconcile, and
they are counted in `dexd_mikrotik_plan_changes{operation="conflict"}`.
Resolve the conflict on the router (or give each writer distinct hostnames) and
the next reconcile picks the hostname up.

## RouterOS setup

`dexd-mikrotik` requires the REST API, which RouterOS serves over `www-ssl`.
Create a dedicated user and enable the service:

```routeros
/user group add name=dexd-mikrotik policy=read,write,web,api,rest-api
/user add name=dexd-mikrotik group=dexd-mikrotik password=<password>
/ip service set www-ssl disabled=no certificate=<your-cert>
```

Set `MIKROTIK_BASEURL` to the `https://` address of the router. If the
certificate is self-signed, either trust it in the container or set
`MIKROTIK_SKIP_TLS_VERIFY=true`.

## Deployment

```bash
# .env
MIKROTIK_BASEURL=https://192.0.2.1
MIKROTIK_USERNAME=dexd-mikrotik
MIKROTIK_PASSWORD=<password>
DEFAULT_TARGET=192.0.2.10
```

```bash
docker compose up -d
```

The image is `ghcr.io/doonga/dexd-mikrotik:latest`. The Compose file mounts the
Docker socket read-only.

## Configuration

| Environment variable       | Default         | Description                                                                                  |
| -------------------------- | --------------- | -------------------------------------------------------------------------------------------- |
| `MIKROTIK_BASEURL`         | **required**    | RouterOS REST base URL, e.g. `https://192.0.2.1`                                             |
| `MIKROTIK_USERNAME`        | **required**    | RouterOS user with `read,write,api,rest-api` policies                                        |
| `MIKROTIK_PASSWORD`        | **required**    | Password for that user                                                                       |
| `MIKROTIK_SKIP_TLS_VERIFY` | `false`         | Skip TLS verification (self-signed certificates)                                             |
| `DEFAULT_TARGET`           | **required**    | Default target. IPv4 → A record, hostname → CNAME                                            |
| `TXT_OWNER`                | `dexd-mikrotik` | Scopes TXT ownership; change if running multiple instances                                   |
| `TXT_PREFIX`               | empty           | Optional prefix for ownership TXT record names                                               |
| `POLICY`                   | `sync`          | Change policy: `sync`, `upsert-only`, or `create-only`                                       |
| `DEFAULT_TTL`              | `auto`          | TTL for created A/CNAME records. Use `auto` to let the router choose, or a positive integer. |
| `RECONCILE_INTERVAL`       | `5m`            | How often to run a full reconcile                                                            |
| `LOG_LEVEL`                | `info`          | `debug`, `info`, `warn`, or `error`                                                          |
| `LOG_FORMAT`               | `text`          | `text` or `json`                                                                             |
| `DRY_RUN`                  | `false`         | List current records and log planned changes without mutating the router                     |
| `METRICS_ADDR`             | `:8080`         | Address for the Prometheus metrics HTTP server. Empty disables metrics.                      |

### Policy

`POLICY` controls which planned changes are applied:

| Policy        | Creates | Updates | A/CNAME replacements | Deletes                               |
| ------------- | ------- | ------- | -------------------- | ------------------------------------- |
| `sync`        | yes     | yes     | yes                  | yes                                   |
| `upsert-only` | yes     | yes     | yes                  | no stale-record or orphan-TXT cleanup |
| `create-only` | yes     | no      | no                   | no                                    |

`upsert-only` allows A/CNAME replacements because they are updates by intent.

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

`HostRegexp(...)` entries are skipped, since they cannot be materialized into DNS records.

### Record type detection

Record type is inferred from the target value: an **IPv4 address** produces an
**A record**, and a **hostname** produces a **CNAME**. There is no `record-type`
label.

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
  traefik.http.routers.rustfs.rule: Host(`s3.example.com`)
  dexd.routers.rustfs.extra-hostnames: "bucket.s3.example.com"
```

Use `dexd.routers.<name>.hostnames` to replace parsed hostnames entirely, or
`dexd.routers.<name>.extra-hostnames` to append comma-separated names. Router
`skip=true` wins over hostnames, extra hostnames, and target overrides.
dexd-mikrotik does not support wildcard hostnames: names such as
`*.s3.example.com` are skipped with a warning.

Standalone host blocks create records that are not tied to Traefik router
labels:

```yaml
labels:
  dexd.enabled: "true"
  dexd.hosts.dashboard.hostnames: "traefik.example.com"
  dexd.hosts.dashboard.target: "192.0.2.10"
```

`dexd.hosts.<name>.target` is optional and falls back to container-level
`dexd.target`, then `DEFAULT_TARGET`. `dexd.hosts.<name>.skip=true` skips the
whole standalone block, including all configured hostnames.

## Metrics

Prometheus metrics are exposed at `/metrics` on `METRICS_ADDR`. The example
Docker Compose file publishes the default metrics listener on host port `8080`.
The `dexd_mikrotik_build_info` metric carries `version`, `gitsha`, `policy` and
`provider` (`mikrotik`) labels.

Useful alerting metrics:

```promql
time() - dexd_mikrotik_reconcile_last_success_timestamp_seconds > 900
increase(dexd_mikrotik_reconcile_total{result="error"}[10m]) > 0
increase(dexd_mikrotik_changes_total{result="error"}[10m]) > 0
increase(dexd_mikrotik_provider_errors_total[10m]) > 0
dexd_mikrotik_plan_changes{operation="conflict"} > 0
```

`deploy/prometheusrule.yaml` includes alerts for stale successful reconciles,
source/reconcile/change/provider errors, slow reconciles, and conflicts. Apply
it in clusters using the Prometheus Operator.

Import [`deploy/grafana-dashboard.json`](deploy/grafana-dashboard.json) into
Grafana to monitor reconciliation health, managed DNS records, conflicts, plans
and applied DNS changes, Docker events, RouterOS provider requests/latency,
errors, build information, and the Go process runtime. The dashboard includes
datasource, `job`, and `instance` variables so it works with any Prometheus
scrape configuration.

Useful dashboard metrics:

- `dexd_mikrotik_reconcile_total`
- `dexd_mikrotik_reconcile_duration_seconds`
- `dexd_mikrotik_reconcile_last_success_timestamp_seconds`
- `dexd_mikrotik_plan_desired_records{record_type}`
- `dexd_mikrotik_plan_changes{operation}` (including `operation="conflict"`)
- `dexd_mikrotik_changes_total`
- `dexd_mikrotik_provider_requests_total`
- `dexd_mikrotik_provider_request_duration_seconds`
- `dexd_mikrotik_provider_errors_total`
- `dexd_mikrotik_docker_events_total`
- `process_cpu_seconds_total`
- `process_resident_memory_bytes`
- `go_memstats_heap_alloc_bytes`
- `go_goroutines`
- `go_threads`

## Building

```bash
go build ./cmd/dexd-mikrotik
```

or with Docker, using the Go version pinned by mise:

```bash
mise run docker-build
```
