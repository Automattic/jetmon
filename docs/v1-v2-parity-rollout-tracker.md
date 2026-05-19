# V1/V2 Parity Rollout Tracker

This tracker captures v1 operational details that are not yet fully accounted
for in v2 or in the production rollout-planning branch. It is intentionally
secret-free and should be updated as each decision is resolved.

## Recommended Order

1. WPCOM notification endpoint/auth parity
2. StatsD hostname and metric path compatibility
3. Legacy `stats/` file format compatibility
4. Log file and status-change log compatibility
5. Legacy Monitor health/status endpoint replacement
6. DB server-map parser behavior and documentation
7. Deprecated v1 config key handling

## 1. WPCOM Notification Endpoint/Auth Parity

Status: resolved; v1 runtime log-file compatibility is intentionally retired

Key difference:

- v1 sends a client-certificate HTTPS `GET` to
  `jetpack.wordpress.com/jetmon/?data=...`, with the auth token embedded in the
  JSON payload.
- v2 sends bearer-token JSON `POST` requests to
  `public-api.wordpress.com/wpcom/v2/jetpack-monitor/status-change`.

Why this is an issue:

- This is a production contract change, not just an implementation detail. If
  WPCOM has not explicitly accepted the new endpoint/auth model, production v2
  notifications may fail or bypass legacy consumers.
- The rollout docs currently mention WPCOM tokens but do not explicitly state
  whether the v1 client certificate dependency has been retired.

V1 way:

- Pros: known production path; compatible with existing WPCOM handler; client
  certificate adds another access-control layer.
- Cons: query-string JSON is awkward, can hit URL-length/logging concerns, and
  uses `rejectUnauthorized=false`; client certificate provisioning is another
  production secret.
- Risks: retaining it carries legacy TLS behavior and cert-management burden.

Current v2 way:

- Pros: cleaner HTTP semantics, easier testing, smaller secret surface, better
  fit for modern API routing and observability.
- Cons: depends on WPCOM-side support for a new endpoint and bearer auth.
- Risks: silent mismatch with WPCOM production expectations until a real
  end-to-end notification test is run.

Resolution options:

- Keep v2 POST endpoint only and get explicit WPCOM sign-off plus an
  end-to-end staging/production-safe smoke test.
- Add a v1-compatible WPCOM client mode behind config and use it during initial
  rollout.
- Support both paths temporarily, with v2 POST as primary and v1 GET/cert as a
  fallback.

Recommended solution:

- Confirm the new WPCOM endpoint/auth contract with the WPCOM owner and document
  the decision. If confirmation is not available before rollout, add a
  config-gated v1-compatible client mode as the conservative fallback.

## 2. StatsD Hostname And Metric Path Compatibility

Status: implemented in this branch with `STATSD_HOSTNAME`

Key difference:

- v1 builds metric paths as `com.jetpack.jetmon.<dc>.<node>...` by taking the
  first two hostname labels and reversing them.
- v2 currently uses the full runtime hostname after replacing dots and dashes
  with underscores.

Why this is an issue:

- Existing Graphite/Grafana dashboards may depend on the v1 hierarchy. In
  containers, the runtime hostname may be a container ID or service name rather
  than the production host identity.

V1 way:

- Pros: existing dashboards and alerts already know this path shape.
- Cons: hostname parsing is implicit and brittle; it assumes production naming
  conventions.
- Risks: malformed or changed hostnames produce confusing metric paths.

Current v2 way:

- Pros: simple, deterministic, and avoids hidden datacenter parsing logic.
- Cons: likely changes existing metric series names.
- Risks: dashboards can go dark even though metrics are being emitted.

Resolution options:

- Add `STATSD_HOSTNAME` to explicitly set the metric host segment.
- Preserve the v1 hostname transform by default and allow override.
- Keep current v2 behavior and migrate dashboards.

Recommended solution:

- Add `STATSD_HOSTNAME` and use it in TeamCity production config. Keep the
  current sanitized hostname as the fallback for local/dev runs. This avoids
  brittle hostname inference while preserving production dashboards.

Implementation notes:

- `STATSD_ADDR` selects the UDP endpoint.
- `STATSD_HOSTNAME` selects the Graphite path identity used in
  `com.jetpack.jetmon.<hostname>`.
- Explicit `STATSD_HOSTNAME` values preserve dots so production can set
  `<datacenter>.<node>`, for example `dfw1.jetmon-prod-1` for
  `jetmon-prod-1.dfw1.example.com`. Unsafe characters are normalized to
  underscores.
- Keep the value stable and low-cardinality. Do not include container IDs,
  release SHAs, process IDs, ports, or random suffixes.

## 3. Legacy `stats/` File Format Compatibility

Status: implemented in this branch with strict v1 file shape

Key difference:

- v1 writes labeled files:
  `sites per second: N`, `sites in queue: N`, and multiline `totals`.
- v2 currently writes bare integers to those files.

Why this is an issue:

- The v2 docs claim `stats/` file outputs keep the same format. Any legacy
  scripts that parse the labels or multiline totals will break.

V1 way:

- Pros: compatible with existing file readers; useful for simple shell-based
  status checks.
- Cons: ad hoc text format and less useful than the dashboard/API.
- Risks: callers may parse whitespace-sensitive output.

Current v2 way:

- Pros: simpler implementation.
- Cons: not actually drop-in compatible.
- Risks: legacy monitoring reads misleading or unparseable content.

Resolution options:

- Restore v1 file formats exactly, plumbing the available success/error/offline
  counters into the writer.
- Restore labels for `sitespersec` and `sitesqueue` immediately, and write the
  best available multiline `totals` until richer counters are plumbed.
- Declare the files deprecated and update docs/monitoring.

Recommended solution:

- Restore v1-compatible file text. If some exact counters are not yet available
  in the streaming scheduler, add them rather than weakening the compatibility
  promise.

Implementation notes:

- The writer still updates only the existing three files:
  `stats/sitespersec`, `stats/sitesqueue`, and `stats/totals`.
- The output shape stays strict v1-compatible; no extra lines are added.
- Stats file writes stay on the existing scheduler/report cadence. No per-check
  file I/O or database work is added.
- `GET /api/v1/monitor/stats` exposes the same in-memory snapshot with `read`
  scope, and `?file=sitespersec|sitesqueue|totals` returns exact legacy file
  text for consumers migrating away from host filesystem reads.
- `working` maps to active check goroutines, `waiting` maps to live goroutines
  that are not actively checking, and `halting` remains `0` because v2 has no
  Node worker-process recycle state.

## 4. Log File And Status-Change Log Compatibility

Status: decision needed

Key difference:

- v1 writes rotating `logs/jetmon.log` and `logs/status-change.log`; status
  changes include `site_down:`, `status_change:`, and `still_down:` records.
- v2 logs to stdout/stderr and no longer creates v1 runtime log files.

Why this is an issue:

- The old project docs claimed same log paths and line format. If production has
  hidden file-based consumers, those consumers must migrate to container/service
  logs, the API, StatsD, or database-backed event/audit history before rollout.

V1 way:

- Pros: known paths and status-change line prefixes; file-based tooling keeps
  working.
- Cons: application-owned file rotation is less container-native and can create
  ever-growing files without a separate retention policy.
- Risks: duplicated log paths and container logs can diverge if both are used;
  bind mounts would be needed for external access in Docker deployments.

Current v2 way:

- Pros: container-native; easier for TeamCity/docker-deploy and centralized log
  collection; avoids extra bind mounts and runtime-owned log retention.
- Cons: not drop-in compatible with file readers.
- Risks: any undiscovered legacy file reader must be updated before rollout.

Resolution options:

- Reintroduce v1-compatible file logging and status-change logging in v2 only
  if a confirmed production consumer requires it.
- Keep stdout/stderr as the only runtime log stream and expose status/history
  through `jetmon_events`, `jetmon_event_transitions`, `jetmon_audit_log`, the
  API, dashboard, StatsD, and the v1-style stats API.

Recommended solution:

- Runtime log files are not part of v2. The implementation now removes Docker
  log mounts, logrotate packaging, and docs that promised v1 log-file
  compatibility. Routine scheduler summaries and state-change chatter are
  debug-only so production stdout/stderr stays focused on startup/shutdown,
  warnings, and operational failures.

## 5. Legacy Monitor Health/Status Endpoint Replacement

Status: rollout validation item recommended

Key difference:

- v1 has an HTTPS listener on port `7800` with `/get/status` returning `OK`,
  and `/put/host-status` for asynchronous Veriflier replies.
- v2 uses JSON-over-HTTP request/response for Verifliers and exposes health via
  the API/dashboard endpoints.

Why this is an issue:

- The Veriflier reply endpoint is intentionally obsolete, but external health
  checks may still call `/get/status`.

V1 way:

- Pros: simple health check and already deployed.
- Cons: tied to the legacy Veriflier callback server and self-signed certs.
- Risks: preserving it can expose a legacy surface that v2 no longer needs.

Current v2 way:

- Pros: health is richer and aligned with the v2 API/dashboard.
- Cons: changes endpoint and possibly port/protocol.
- Risks: docker-deploy or monitoring may mark healthy containers unhealthy if
  still configured for the v1 path.

Resolution options:

- Update Systems health checks to use `/api/v1/health` or dashboard health.
- Add a compatibility endpoint that returns plain `OK`.
- Add a small sidecar/proxy that maps old health checks to the new endpoint.

Recommended solution:

- Prefer updating docker-deploy/monitoring to the v2 health endpoint. Add a
  compatibility `OK` endpoint only if Systems cannot update health checks before
  rollout.

## 6. DB Server-Map Parser Behavior And Documentation

Status: docs update now; code manager later

Key difference:

- v1 parses only the `misc` dataset, uses `WRITE_MASTER` rows for writes, and
  builds local/failover read pools from datacenter matching. It also uses the
  `INTERNET_URI` field for host/port in the inspected code.
- v2 currently reads a single `DB_*` environment config at startup. This branch
  adds sync scaffolding but not an in-process DB pool manager.

Why this is an issue:

- The rollout plan must not imply v2 already hot-swaps DB pools or reads the
  full v1 server map in process.
- Documentation should mirror the real v1 parser behavior so TeamCity config
  generation selects the same endpoints.

V1 way:

- Pros: supports live DB map refresh and read/write pool separation.
- Cons: parser is brittle, tied to hostname-derived datacenter, and reloads
  pools lazily.
- Risks: hostname/datacenter assumptions can select unexpected pools.

Current v2 way:

- Pros: simpler and easier to reason about; no hidden pool swaps.
- Cons: endpoint changes require drain/recreate until a DB manager exists.
- Risks: stale DB endpoint if server map changes while container stays up.

Resolution options:

- Keep sidecar sync plus drain/recreate on material DB endpoint changes.
- Add a host/TeamCity render step that converts `db-servers.php` into `DB_*`.
- Build a first-class v2 DB config manager with read/write pools and hot reload.

Recommended solution:

- Short term: sidecar sync plus explicit drain/recreate on selected endpoint
  changes, with docs corrected to match v1 parser behavior. Long term: build
  the DB config manager only if production actually needs live pool reloads.

## 7. Deprecated V1 Config Key Handling

Status: docs/validation update recommended

Key difference:

- v1 includes keys such as `WORKER_MAX_CHECKS` and `TIMEOUT_FOR_REQUESTS_SEC`.
- v2 intentionally omits or changes some semantics because there are no worker
  processes and Veriflier escalation has a different transport model.

Why this is an issue:

- The docs say existing config keys are honored. Unknown or silently ignored
  keys can mislead operators during rollout.

V1 way:

- Pros: familiar knobs for existing operators.
- Cons: some knobs are artifacts of the Node worker-process design.
- Risks: carrying them forward can imply controls that no longer exist.

Current v2 way:

- Pros: cleaner config surface.
- Cons: incomplete compatibility story for copied v1 configs.
- Risks: operators may believe a copied key still changes behavior.

Resolution options:

- Accept deprecated keys and log clear no-op warnings.
- Reject deprecated keys with validation errors.
- Document deprecated keys only and leave parser behavior unchanged.

Recommended solution:

- Accept copied v1 configs but warn clearly for deprecated no-op keys during
  `validate-config` and startup. This keeps rollout forgiving without hiding
  semantic changes.
