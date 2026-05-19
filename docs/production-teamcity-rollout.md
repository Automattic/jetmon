# Production TeamCity Rollout Plan

This plan covers production Monitor container rollout through TeamCity and
docker-deploy, with database server-map refresh kept inside the Docker service.
It is intentionally secret-free: SVN credentials, database credentials, WPCOM
tokens, and generated production config files stay out of this repo and out of
Docker images.

## Inputs

For database server-map refresh, the recommended sidecar deployment has one
external secret input and one runtime-generated private file. Standard Monitor
runtime config, including the StatsD endpoint, is handled separately by the
deployment role:

| Input | Where It Lives | Notes |
|---|---|---|
| `config-sync.env` | External to the image; mounted or injected into the config-sync sidecar | SVN credentials and sync paths for `scripts/jetmon-config-update.sh`. This is the only external config dependency for DB server-map refresh. |
| `db-servers.php` | Generated inside the Docker service on a shared runtime path | Synced from SVN by the sidecar. The Monitor reads this file; it is not committed, baked into an image, or written to TeamCity logs. |
| `config/config.json` or equivalent env | TeamCity secure parameters, docker-deploy role config, or another Systems-managed secret source | Non-image operational config for the Monitor. |
| `STATSD_ADDR` | docker-deploy role config or TeamCity parameter | UDP StatsD endpoint for Monitor and Deliverer. Production Monitor hosts already run local StatsD proxies, so set this to the host-local proxy endpoint that is reachable from inside the container. |
| `STATSD_HOSTNAME` | docker-deploy role config or TeamCity parameter | Optional Graphite identity for the StatsD prefix. Recommended format for Monitor production is the v1-compatible `<datacenter>.<node>` path segment, for example `dfw1.jetmon-prod-1`, so metrics land under the expected dashboard hierarchy instead of a Docker container hostname. |
| Docker image tag | TeamCity build output | Use immutable Git SHA tags for rollout. |

Use [../config/jetmon-config-sync-sample.env](../config/jetmon-config-sync-sample.env)
as the template for `config-sync.env`. Do not copy the real file into the repo.
The same one-shot sync script can be used by the sidecar or by the host-side
fallback service/timer.

Local development and smoke testing continue to use explicit `DB_HOST`,
`DB_PORT`, `DB_USER`, `DB_PASSWORD`, and `DB_NAME` values. The production
config-sync sidecar is not part of the default local Docker Compose stack.

## TeamCity And Frontity Reference Findings

The saved Frontity TeamCity configuration and build log show the current
docker-deploy pattern:

- TeamCity has one build configuration with separate Docker build and push
  steps per image, followed by a final deploy command.
- Each image is tagged as both `latest` and the Git SHA.
- The final deploy step calls
  `deploy-to-servers-by-role.sh docker-frontity-web frontity-web/<git-sha>`.
- That script issues a verified HTTPS request to docker-deploy at
  `/deploy/frontity-web/<git-sha>` and waits for a `200` response.
- The Frontity repo's `docker-compose.yml` is build-oriented: it has service
  `build:` entries and no registry `image:` pins. TeamCity provides the
  immutable image tags separately, so the docker-deploy role must contain or
  derive the mapping from the deployed service name and Git SHA to the registry
  images.
- Systems explicitly noted that docker-deploy supports Docker Compose, and that
  self-contained images are preferred over mounting application source from the
  host.

For Jetmon Monitors, this points to a TeamCity job that builds and pushes one
Monitor image plus one config-sync sidecar image, then calls docker-deploy with
a Jetmon service group and SHA tag. Production Monitor hosts already run local
StatsD proxies, so the Monitor docker-deploy role should not start a
StatsD/Graphite container. The remaining Systems-specific item is the
docker-deploy role definition: it must define the target hosts, image names,
secret injection/mounting for `config-sync.env`, the host-local StatsD endpoint
reachable from the container, and whether a shared runtime volume/tmpfs between
sidecar and Monitor is allowed.

## What v1 Does

The v1 flow is split between a shell updater and the Node database library:

- `jetmon-config-update.sh` sources `/usr/local/etc/config`, runs an SVN
  checkout of the private `.config` tree, and changes ownership to the `jetmon`
  user.
- `lib/dbpools.js` schedules `configuration.update()` using
  `DB_CONFIG_UPDATES_MIN`.
- When a changed config is detected, v1 rebuilds its MySQL pool cluster lazily
  before the next query/update.
- v1 reads only the `misc` dataset from `db-servers.php`.
- Rows with `WRITE_MASTER == 1` become the write pool.
- Rows in the local datacenter with read priority become preferred read pools.
- Non-`bak` rows outside the local datacenter become failover read pools.
- v1 sent StatsD UDP metrics to `127.0.0.1:8125` in production and switched to
  the Docker service name `statsd:8125` only for the local Docker hostname.
  That implies production had a local StatsD/proxy endpoint on each Monitor
  host or container namespace. The public v1 repo does not contain a remote
  Graphite relay target; it only shows local Docker using
  `graphiteapp/graphite-statsd`. Jetmon itself does not send directly to
  Grafana; Grafana reads from Graphite after StatsD/proxy ingestion.
- v1 shaped the metric hostname as `<datacenter>.<node>` from the production
  hostname. v2 supports `STATSD_HOSTNAME` so TeamCity/docker-deploy can set the
  same Graphite path explicitly instead of relying on the container runtime
  hostname.

One local-v1 caveat needs Systems confirmation: the checked-out updater script
prints `OK` on success, while the checked-out Node refresh path appears to treat
non-empty stdout as "not updated." Production may have a wrapper or older script
variant. The v2 rollout should copy the operational intent, not that stdout
quirk.

The PHP row shape used by v1 is:

```text
datacenter, read_priority, write_master, internet_host_port,
internal_host_port, database, user, password, ...
```

The comments in the production file describe `R` as read attempt order and `W`
as master/write eligibility. For blog datasets, database and user fields may be
ignored; for Jetmon's `misc` dataset they are the connection credentials.

## Current v2 Database Behavior

Jetmon v2 supports two database configuration modes:

1. Explicit local/test DSN: `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, and
   `DB_NAME`.
2. Production server map: `DB_SERVER_MAP_PATH` points at the synced
   `db-servers.php` file and v2 reads the `misc` dataset directly.

When `DB_SERVER_MAP_PATH` is set, v2 builds separate read and write pools from
the server map:

- Writes and transactions use the first `misc` row marked as write master.
- Reads use read-enabled non-`bak` rows, preferring rows whose datacenter
  matches `DB_SERVER_MAP_DATACENTER` and keeping the remaining non-`bak` read
  rows as connection-time failover targets.
- If a map has no separate read rows, reads use the same endpoint as writes.
- `DB_SERVER_MAP_ADDRESS=internet` matches v1's effective behavior. Use
  `internal` only when the container network can reach the internal addresses.

The Monitor and standalone deliverer re-parse `db-servers.php` on the
`DB_CONFIG_UPDATES_MIN` cadence with per-host jitter. Changed endpoint maps are
validated with read and write ping checks before publication. On success, the
stable read/write `*sql.DB` pools keep their identity while their connectors
start creating new connections from the new server-map details; idle
connections are flushed so credential rotations do not require a process
restart. On parse or connection failure, the process keeps the previous working
pools and logs the reload failure without printing passwords.

Operators can confirm reload state without reading container files by checking
the host dashboard `db-config` dependency or
`GET /api/v1/monitor/db-config`. Both surfaces report the next scheduled
server-map check, the last changed map observed, the last successful hot reload,
and the active endpoint fingerprint. They expose endpoint labels only, never
passwords or full DSNs.

Set `DB_SERVER_MAP_DATACENTER` explicitly for container deployments. The v1
hostname-derived datacenter heuristic is retained only as a fallback and may not
work with container hostnames.

## Recommended First Rollout Shape

Use TeamCity as the image builder and docker-deploy as the service rollout
entry point. Run the SVN refresh in a config-sync sidecar rather than on the
host. Monitor production hosts already run local StatsD proxies, so the
Monitor stack should contain two Jetmon-managed containers on each host:
Monitor and config-sync.

1. Build and publish immutable Monitor and config-sync sidecar images. Keep
   Veriflier as a separate image/deployment stream.
2. Tag each image as both `latest` and the Git SHA, matching the Frontity
   TeamCity pattern.
3. Configure a docker-deploy role such as `docker-jetmon-monitor` with the
   approved registry image names and the secret source for `config-sync.env`.
4. Configure `STATSD_ADDR` for the Monitor container to reach the host-local
   StatsD proxy. If docker-deploy uses host networking, this can be
   `127.0.0.1:8125`. If docker-deploy uses bridge networking, do not use
   `127.0.0.1`; use the approved host-gateway or bridge endpoint instead.
5. Start the config-sync sidecar first. It runs
   [../scripts/jetmon-config-sync-loop.sh](../scripts/jetmon-config-sync-loop.sh),
   which calls [../scripts/jetmon-config-update.sh](../scripts/jetmon-config-update.sh)
   on a jittered cadence.
6. Share only the generated config-source path with the Monitor, mounted
   read-only from the Monitor's perspective.
7. Stage the Monitor runtime config/env through docker-deploy role config or
   TeamCity secure parameters, not through image layers.
   Use `/api/v1/monitor/stats` or StatsD for external stats consumers rather
   than host filesystem reads unless Systems explicitly approves host bind
   mounts for the legacy `stats/` directory.
8. Drain the existing Monitor.
9. Pull and start/recreate the new Monitor service with the sidecar.
10. Run `./jetmon2 migrate`, `./jetmon2 status`, and the rollout gates from the
   existing migration runbooks.
11. Move to the next host only after the current host is healthy.

The rolling update path is still useful for image/config changes, but database
credential or endpoint rotations in `db-servers.php` should hot-reload in place
after the sidecar syncs the new file.

## Safe TeamCity Test Rollout

We know enough to create a limited TeamCity/docker-deploy smoke rollout, but it
must not run the normal Monitor entrypoint against production-like services.
The default `docker/Dockerfile_jetmon` entrypoint runs `./jetmon2 migrate` and
then starts `./jetmon2`; that path writes migrations, bucket ownership,
process-health rows, runtime/check-history/event state, and can perform checks.

For a safe first docker-deploy test, use a dedicated test role or test service
name that overrides the entrypoint/command and only runs read-only checks:

```sh
./jetmon2 version
./jetmon2 validate-config
```

`validate-config` parses config, connects to MySQL, and probes configured
Veriflier status endpoints. It does not run migrations, start the scheduler,
claim buckets, write process health, enqueue deliveries, or send WPCOM
notifications. Use a read-only database user for this test so accidental writes
fail closed.

Recommended safe-test config:

- `WPCOM_NOTIFY_ENABLE=false`
- `EMAIL_TRANSPORT=stub`
- `API_PORT=0`
- `DASHBOARD_PORT=0`
- `DEBUG_PORT=0`
- `STATSD_ADDR=` if the test role should not emit metrics, or
  the production host-local StatsD endpoint when the smoke test should validate
  metrics reachability
- `DELIVERY_OWNER_HOST` set to a non-matching sentinel value such as
  `disabled-for-teamcity-smoke`
- `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, and `DB_NAME` set explicitly
  to the read-only test database credentials, or `DB_SERVER_MAP_PATH` pointed at
  a redacted/internal-only test server map whose `misc` write target is safe
- Veriflier entries pointed at internal test Verifliers, or omitted if the goal
  is only image/deploy/DB-connect validation

The safe test should answer these questions only:

- Can TeamCity build and push the intended image tags?
- Can docker-deploy resolve the role and deploy the selected Git SHA?
- Can the service receive non-image config/secrets without leaking them?
- Can the container reach the intended database and Veriflier endpoints?
- Can the deployed container stay healthy without starting Monitor work?

Do not use the default Monitor service command for this test. Do not run
`migrate`, `status` against a real running Monitor, `rollout cutover-check`, or
the bare `jetmon2` server process until the target database is intentionally
writable and the rollout is allowed to perform Monitor work.

## StatsD Deployment Model

Jetmon v2 keeps the v1-compatible metric prefix and StatsD transport. Monitor,
Deliverer, and Veriflier all support `STATSD_ADDR`:

- Monitor and Deliverer default to `statsd:8125`, matching Docker Compose
  service discovery.
- Veriflier leaves StatsD disabled unless `STATSD_ADDR` is set, which keeps
  standalone Veriflier runs simple.
- Setting `STATSD_ADDR` explicitly empty disables StatsD for safe smoke tests.

Production roles should also set `STATSD_HOSTNAME` to the v1-compatible
Graphite identity. The recommended Monitor format is `<datacenter>.<node>`,
matching v1's hostname transform. For example, a v1 host named
`jetmon-prod-1.dfw1.example.com` should use:

```text
STATSD_HOSTNAME=dfw1.jetmon-prod-1
```

This controls only the metric prefix:

```text
com.jetpack.jetmon.<STATSD_HOSTNAME>.<metric>
```

Leaving `STATSD_HOSTNAME` unset falls back to the runtime hostname, which is
acceptable for local Docker runs but may become a container ID or service name
under docker-deploy.

Keep this value stable and low-cardinality. Do not include container IDs,
release SHAs, process IDs, ports, or random suffixes. For Verifliers, use a
stable region/vantage identity that matches the intended Grafana grouping.

Production Monitor docker-deploy should use the existing local StatsD proxy on
the host and should not add a StatsD/Graphite container to the Monitor stack.
The exact `STATSD_ADDR` depends on the docker-deploy network mode:

- Host-networked container: `STATSD_ADDR=127.0.0.1:8125` preserves v1's local
  proxy behavior.
- Bridge-networked container: `127.0.0.1` points inside the container, not at
  the host. Use the approved host-gateway address, bridge gateway address, or a
  docker-deploy-provided alias that reaches the host's StatsD proxy.

Local development still uses `docker/docker-compose.yml` with
`graphiteapp/graphite-statsd` so developers get a local StatsD receiver and
Graphite UI without production infrastructure.

Production Veriflier VPS deployments are different: those hosts do not have a
pre-existing local StatsD proxy, so the Veriflier Compose stack should include
StatsD and Graphite. Use
[../docker/docker-compose.veriflier-prod.yml](../docker/docker-compose.veriflier-prod.yml)
as the starting point. It keeps StatsD internal to the Docker network, points
Veriflier at `statsd:8125`, persists Graphite data, and publishes the Graphite
HTTP port only on the configured `GRAPHITE_BIND_ADDR` so central Grafana can
query it over a private/VPN/firewalled path.

## Periodic Config Refresh

The one-shot sync script exits `0` for both unchanged and updated files and
prints only `unchanged <path>` or `updated <path>`. It does not print SVN
credentials. The sidecar loop wraps that script, logs a UTC timestamp, retries
on failure by default, and adds jitter to avoid fleet-wide refresh bursts.

The sidecar loop defaults to ten minutes plus up to two minutes of jitter. That
is intentionally close to the legacy `DB_CONFIG_UPDATES_MIN` default while
avoiding a simultaneous fleet-wide SVN refresh.

Recommended refresh behavior:

- Add jitter so all Monitor hosts do not refresh at the same second.
- Run the sidecar as the unprivileged `jetmon` user and skip `chown`; the
  generated file is written directly into the shared runtime path.
- Keep the SVN working copy outside the Monitor-readable destination.
- Mount only the generated destination into the Monitor, read-only.
- Let the Monitor/Deliverer hot-reload selected `misc` endpoint changes in
  process. A full container restart is still appropriate for image, config, or
  environment changes, but not required for DB credential rotation in the
  synced server map.

If docker-deploy cannot support the required sidecar secret mount and shared
runtime path, the included `jetmon-config-sync.service` /
`jetmon-config-sync.timer` files are the host-side fallback. The timer uses the
same ten-minute cadence with two minutes of randomized delay.

## Container Shape Options

### Option A: Monitor Plus Config-Sync Sidecar

Run the Monitor and an SVN config-sync sidecar in the same docker-deploy Compose
service. The sidecar owns `db-servers.php` refresh and writes to a shared
runtime path. The Monitor reads that path read-only.

Pros:

- Matches the Frontity/docker-deploy direction toward Compose-managed,
  self-contained service deployments.
- Keeps SVN tooling and credentials out of the Monitor image.
- Avoids host cron/systemd drift for database server-map refresh.
- Keeps failure domains clearer: a sync failure is visible on the sidecar
  container without restarting the Monitor immediately.

Cons:

- Needs Systems confirmation that docker-deploy role config can provide the
  secret `config-sync.env` and a shared runtime path between the sidecar and
  Monitor containers.
- Needs `DB_SERVER_MAP_PATH` and `DB_SERVER_MAP_DATACENTER` configured for the
  Monitor so local read replica preference does not depend on container
  hostname shape.

Recommendation: use this for the first containerized production rollout if the
docker-deploy role can support the shared runtime path.

### Option B: Monolithic Monitor Image

Install SVN tooling and the sync loop into the Monitor image, then run both the
Monitor and the periodic sync process inside one container.

Pros:

- Works even if docker-deploy cannot support shared runtime paths.
- Gives the operator one service container per Monitor host.
- Satisfies the "single external config file" goal.

Cons:

- Puts SVN client code and SVN credentials in the Monitor container's runtime
  environment.
- Requires process supervision inside one container or custom entrypoint logic.
- Makes debugging sync failures less clean because they share the Monitor
  process namespace and logs.

Recommendation: use only if the sidecar shape is blocked by docker-deploy role
limitations.

### Option C: Host-Side Sync

Run the sync script from systemd/cron on each host and mount the generated file
into the Monitor.

Pros:

- Closest to the existing v1 operational model.
- Works with simple single-container Monitor deployment.
- Easy to reason about with standard host tooling.

Cons:

- More host-local state to manage and audit.
- Less aligned with the Frontity docker-deploy model.
- Secrets and SVN working copy live directly on the host rather than in a
  narrowly scoped sidecar.

Recommendation: keep this as the fallback path, not the primary plan.

## TeamCity Deployment Options

### Option 1: TeamCity Web UI Deployment Job

Create a deployment build configuration in the TeamCity UI with secure
parameters for SVN credentials, registry credentials, target role, image names,
and any per-environment paths. Follow the Frontity pattern: Docker build steps,
Docker push steps, then one final `deploy-to-servers-by-role.sh` call.

Pros:

- Fastest path to production readiness.
- Secrets stay in TeamCity secure parameters.
- Easy for Systems to audit and adjust without app code changes.
- Matches the current operational model: one host at a time, drain, replace,
  validate.

Cons:

- Deployment logic is less visible in Git.
- Manual UI drift is possible unless Systems exports or documents the job.

Recommendation: use this for the first production rollout.

### Option 2: TeamCity Kotlin DSL

Add versioned TeamCity build/deploy definitions to the repo. Store only
credential IDs and parameter names in Git; actual secrets remain in TeamCity.

Pros:

- Reviewable deployment workflow.
- Reproducible job definitions.
- Easier to keep build, publish, and deploy steps aligned with code changes.

Cons:

- Requires TeamCity project setup and credential IDs before the DSL can be
  useful.
- More upfront coordination with Systems.

Recommendation: good follow-up once the first Systems-managed deployment shape
is accepted.

### Option 3: `jetmon2`-Managed Deployment

Teach the `jetmon2` binary to call TeamCity APIs or manage remote Docker
services directly.

Pros:

- Could bundle Jetmon-specific rollout validation and orchestration.
- Might reduce operator command variance after the model is mature.

Cons:

- Couples the application binary to deployment infrastructure.
- Requires the app or operator host to hold TeamCity/API credentials.
- Expands the security surface of a binary that already handles production
  monitoring and database access.

Recommendation: do not use for the first rollout. Keep deployment orchestration
in TeamCity and keep `jetmon2` focused on validation, drain, status, and
rollout gates.

### Option 4: Host-Side Config Sync Timer

Install the SVN sync script plus `jetmon-config-sync.service` /
`jetmon-config-sync.timer` or an equivalent cron job on each host. TeamCity is
responsible for installing/updating the script and env file, while the host
keeps `db-servers.php` fresh.

Pros:

- Closest to v1's periodic refresh behavior.
- Decouples database server-map refresh from image deployments.
- Keeps secrets out of the container image.

Cons:

- Reintroduces host-level timer/service state that the sidecar shape avoids.
- Needs jitter and observability so a bad SVN/config issue is visible.

Recommendation: keep this ready as the fallback if docker-deploy cannot support
the sidecar secret mount and shared runtime config path.

## Database Config Manager Follow-Ups

The v2 database config manager now handles the production-critical parts of v1
parity: it parses `db-servers.php`, separates read/write endpoints, validates a
changed map before publication, hot-reloads connection creation, and keeps the
previous working config if the new map is bad.

Two follow-ups remain useful after the first production rollout:

- Add dashboard/API visibility for the last DB reload status, active endpoint
  fingerprint, and last reload time. Current visibility is log-based and avoids
  printing secrets.
- Continue moving low-risk read-only API/dashboard queries from the write pool
  to `db.ReadDB()` as those surfaces are reviewed. Core scheduler reads and
  rollout audit reads already use the read pool; writes and transactions stay
  on the write pool.

## TeamCity Job Skeleton

The exact TeamCity fields should be confirmed with Systems, but the deployment
steps should mirror the Frontity build:

1. Docker build `jetmon2` from [../docker/Dockerfile_jetmon](../docker/Dockerfile_jetmon)
   and tag it as `registry.a8c.com/<jetmon-image>:latest` plus
   `registry.a8c.com/<jetmon-image>:<git-sha>`.
2. Docker push both Monitor tags.
3. Docker build the config-sync sidecar from
   [../docker/Dockerfile_config_sync](../docker/Dockerfile_config_sync) and tag
   it as `latest` plus `<git-sha>`.
4. Docker push both sidecar tags.
5. Set `STATSD_ADDR` in the docker-deploy role to the host-local StatsD proxy
   endpoint reachable from inside the Monitor container, and set
   `STATSD_HOSTNAME` to the v1-compatible Graphite identity for that host.
6. Call docker-deploy, for example:
   `deploy-to-servers-by-role.sh docker-jetmon-monitor jetmon-monitor/<git-sha>`.
7. Let the docker-deploy role roll hosts one at a time:
   - provide `config-sync.env` to the sidecar as a secret mount or equivalent
     secure injection readable by the sidecar's `jetmon` user;
   - set `STATSD_ADDR` for the Monitor and any co-located Deliverer process to
     the host-local StatsD proxy endpoint;
   - set `STATSD_HOSTNAME` for the Monitor and any co-located Deliverer process
     to the v1-compatible Graphite path segment for the host;
   - start the sidecar and wait for `db-servers.php` to appear;
   - start/recreate the Monitor with read-only access to the generated file and
     `DB_SERVER_MAP_PATH` set to that path;
   - set `DB_SERVER_MAP_DATACENTER` explicitly for the host and leave
     `DB_SERVER_MAP_ADDRESS=internet` unless Systems confirms internal DB
     hostnames are reachable from inside the container;
   - drain the existing Monitor before replacement;
   - run `./jetmon2 migrate`, `./jetmon2 status`, and rollout validation gates;
   - continue only if the host is healthy.

Use TeamCity secure/hidden parameters for:

- SVN username and password.
- Docker registry credentials.
- WPCOM/API tokens.
- Any database credentials not solely sourced from `db-servers.php`.

Do not store those values in repo files, TeamCity plain text parameters, Docker
image layers, or command logs.

## Notes From TeamCity Reference Review

The saved TeamCity pages and downloaded Frontity build log provide enough
context for the Jetmon deployment shape, but not the server-side docker-deploy
role definition. Before production rollout, ask Systems for the Jetmon
docker-deploy role or a comparable non-secret role example that shows:

- how a Compose service maps `jetmon-monitor/<git-sha>` to one or more registry
  image names;
- how secret files or environment variables are injected into containers, and
  whether mounted secret files can be made readable by the sidecar's unprivileged
  user;
- whether shared runtime volumes/tmpfs mounts are allowed between sidecars and
  app containers;
- how docker-deploy performs per-host health checks and rollback on failure.
