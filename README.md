# Jetmon 2

Jetmon 2 is the Go rewrite of Jetpack's uptime monitor: the same production
contract v1 consumers depend on, with a cleaner runtime, an event-sourced health
model, richer diagnostics, and API-first automation.

The core detection story stays familiar:

```text
local checks -> local retries -> geo Veriflier confirmation -> notify
```

The first difference is correctness: v2 checks sites with `GET`, not the
`HEAD`-only probes that made v1 disagree with real visitor behavior on too many
VIP and Agency sites. Around that more realistic probe, Jetmon 2 records what
it saw, why it believed a site was down, which Verifliers agreed, which
notifications were sent, and how every incident changed over time. It turns "up
or down" into an auditable health platform.

## Why This Matters

| Audience | What Gets Better |
|---|---|
| Systems | Static Go binaries, no `npm`, `node-gyp`, Qt, or worker process tree. Bucket ownership is coordinated in MySQL, hosts drain cleanly, and memory pressure is handled inside the goroutine pool. |
| VIP and Agency | GET-based checks that match customer-visible behavior better than v1's HEAD probes, plus fewer noisy pages and fewer missed incidents through local retries, Veriflier quorum, maintenance windows, keyword checks, redirect policy, SSL/TLS checks, and clearer failure classes. |
| Leadership | A foundation for differentiated uptime monitoring: internal API, webhooks, managed alert contacts, tenant-aware gateway paths, and future Jetpack/WPCOM integrations. |
| Happiness Engineers | Incident answers with evidence: audit logs, event transitions, check timing, Veriflier votes, WPCOM payloads, and suppression reasons are all queryable. |
| Jetpack | A monitor that can grow into a product surface, not just a backend notification hook. |

## What Changed

| Area | Jetmon 1 | Jetmon 2 |
|---|---|---|
| Runtime | Node master, Node workers, C++ native addon, Qt Veriflier | Go monitor, Go Veriflier, optional Go deliverer |
| Probe method | `HEAD` requests that could disagree with real page loads | `GET` requests for local checks and Veriflier checks |
| State | Mutable `site_status` projection | `jetmon_events` plus append-only `jetmon_event_transitions` |
| Detection | Binary status changes | `Seems Down`, `Down`, recovery, false-alarm, and severity transitions |
| Evidence | Basic logs | Audit log, check history, timing breakdown, verifier outcomes, API request logs |
| Integrations | WPCOM notification path | WPCOM, REST API, HMAC webhooks, email, PagerDuty, Slack, Teams |
| Operations | Static bucket config and process recycling | Dynamic bucket ownership, graceful drain, hot reload, dashboard, pprof |

Jetmon 2 keeps the compatibility surfaces that matter during rollout:

- MySQL changes are additive.
- WPCOM notification payloads stay compatible.
- StatsD metric naming remains `com.jetpack.jetmon.<hostname>`.
- Legacy log and stats file paths remain available.
- `jetpack_monitor_sites.site_status` can be projected from v2 events during
  the [v1-to-v2 migration](docs/v1-to-v2-migration.md).

## How Incidents Flow

1. The monitor checks active sites with a bounded Go worker pool.
2. A first local failure opens a `Seems Down` event so the incident start time is
   honest.
3. Local retries absorb one-off network blips before customer notification.
4. Geo-distributed Verifliers confirm or reject the outage.
5. Confirmed outages become `Down`; rejected outages close as false alarms.
6. WPCOM, webhooks, alert contacts, the dashboard, and the API all read from the
   same event and transition history.

That model gives operators and support teams the part v1 could not: a coherent
timeline for every incident, not just the final status bit.

## Try It Locally

Docker Compose is the fastest path for local development:

```bash
cd docker
cp .env-sample .env
docker compose up --build -d
```

Build and test from the repository root:

```bash
make all
make test
make test-race
```

The API CLI can exercise the internal REST API and local failure fixture:

```bash
make build
make api-cli-token-create

export JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090}
export JETMON_API_TOKEN=jm_replace_with_the_printed_token

./bin/jetmon2 api health --pretty
./bin/jetmon2 api commands --output table
make api-cli-smoke
```

See [docs/getting-started.md](docs/getting-started.md) for the full local loop.

## Documentation

| Document | Start Here For |
|---|---|
| [docs/project.md](docs/project.md) | Full product and implementation specification |
| [docs/internal-api-reference.md](docs/internal-api-reference.md) | Internal REST API reference |
| [docs/events.md](docs/events.md) | Event lifecycle and transition semantics |
| [docs/taxonomy.md](docs/taxonomy.md) | Severity, state, cause, and rollup taxonomy |
| [docs/getting-started.md](docs/getting-started.md) | Docker setup, builds, tests, API CLI smoke runs |
| [docs/operations-guide.md](docs/operations-guide.md) | Production config, rollout, delivery workers, metrics, debugging |
| [docs/data-model.md](docs/data-model.md) | Tables, migrations, event projection, tenant mapping |
| [docs/support-guide.md](docs/support-guide.md) | HE workflows for explaining alerts and missed alerts |
| [docs/api-cli-guide.md](docs/api-cli-guide.md) | API CLI examples and automation patterns |
| [docs/v1-to-v2-migration.md](docs/v1-to-v2-migration.md) | Full v1-to-v2 production migration and rollback runbook |
| [docs/jetmon-deliverer-rollout.md](docs/jetmon-deliverer-rollout.md) | Moving outbound delivery to `jetmon-deliverer` |
| [docs/roadmap.md](docs/roadmap.md) | Broader v2 and v3 planning |

Longer design decisions live in [docs/adr/](docs/adr/).

## Production Posture

Jetmon 2 is designed for a cautious host-by-host rollout. The complete process
is in [docs/v1-to-v2-migration.md](docs/v1-to-v2-migration.md):

- Run `./jetmon2 migrate` before first start. Migrations are embedded and
  additive.
- Run `./jetmon2 validate-config` before deploy to check config shape,
  database connectivity, email transport mode, verifier config, and rollout
  safety commands.
- Use pinned bucket mode for the first v1-to-v2 migration so one v1 host can be
  replaced by one v2 host with the same bucket range.
- Use `rollout static-plan-check`, `rollout pinned-check`,
  `rollout activity-check`, `rollout rollback-check`, and
  `rollout projection-drift` from the migration runbook before changing the
  next host.
- Keep `LEGACY_STATUS_PROJECTION_ENABLE` on until legacy readers have moved to
  the v2 API or event tables.
- Use `SIGINT` or `./jetmon2 drain` for graceful shutdown.
- Use `SIGHUP` or `./jetmon2 reload` for config reload without restart.

After the fleet is fully on v2, dynamic bucket ownership lets surviving hosts
absorb work during rolling updates.

## Main Binaries

| Binary | Purpose |
|---|---|
| `bin/jetmon2` | Monitor, orchestrator, REST API, dashboard, embedded delivery workers |
| `bin/veriflier2` | Remote confirmation worker used by the monitor |
| `bin/jetmon-deliverer` | Standalone webhook and alert-contact delivery worker |

## Development Commands

```bash
make all              # Build jetmon2, jetmon-deliverer, and veriflier2
make build            # Build only jetmon2
make build-deliverer  # Build only jetmon-deliverer
make build-veriflier  # Build only veriflier2
make test             # Run the Go test suite
make test-race        # Run tests with the race detector
make lint             # Run lint checks
```

`make generate` is intentionally separate. It requires `protoc` and Go protobuf
plugins, and the generated stubs are not part of the production JSON-over-HTTP
Veriflier transport.
