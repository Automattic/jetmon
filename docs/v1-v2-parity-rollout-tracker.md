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
8. Full codebase simplification and legacy-surface review

## 1. WPCOM Notification Endpoint/Auth Parity

Status: implemented with `WPCOM_NOTIFY_MODE`, pending WPCOM contract
confirmation before using modern mode in production

Key difference:

- v1 sends a client-certificate HTTPS `GET` to
  `jetpack.wordpress.com/jetmon/?data=...`, with the auth token embedded in the
  JSON payload.
- v2 modern mode sends bearer-token JSON `POST` requests to
  `public-api.wordpress.com/wpcom/v2/jetpack-monitor/status-change`.
- v2 legacy mode sends the v1-compatible `GET` request and is the production
  default.

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

V2 modern mode:

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

- Use `WPCOM_NOTIFY_MODE=legacy` for the initial production rollout. Keep
  `WPCOM_NOTIFY_MODE=modern` available for local, staging, and WPCOM contract
  testing until the WPCOM owner explicitly signs off on the new endpoint/auth
  contract.

Implementation notes:

- The default is `legacy`.
- Legacy mode sends the JSON payload as the `data` query parameter, includes
  the configured `AUTH_TOKEN` as `token`, omits modern-only fields such as
  `status_type`, and requires the configured client certificate/key when the
  endpoint is HTTPS.
- Modern mode keeps the bearer-token JSON `POST` path for future testing and
  development.
- `jetmon2 validate-config` reports the selected mode. With notifications
  enabled, it warns when modern mode is selected and warns if legacy
  certificate/key files are not readable.

## 2. StatsD Hostname And Metric Path Compatibility

Status: implemented in this branch with explicit `STATSD_HOST_PATH` metric
identity.

Key difference:

- v1 builds metric paths as `com.jetpack.jetmon.<dc>.<node>...` by taking the
  first two hostname labels and reversing them.
- v2 now uses explicit `STATSD_HOST_PATH` when set, then falls back to the
  resolved process hostname for local/dev compatibility.

Why this is an issue:

- Existing Graphite/Grafana dashboards may depend on the v1 hierarchy. In
  containers, the runtime hostname may be a container ID or service name rather
  than the production host identity.

V1 way:

- Pros: existing dashboards and alerts already know this path shape.
- Cons: hostname parsing is implicit and brittle; it assumes production naming
  conventions.
- Risks: malformed or changed hostnames produce confusing metric paths.

Initial v2 way:

- Pros: simple, deterministic, and avoids hidden datacenter parsing logic.
- Cons: likely changes existing metric series names.
- Risks: dashboards can go dark even though metrics are being emitted.

Resolution options:

- Add `HOSTNAME` / `JETMON_HOSTNAME` to explicitly set the process and metric
  host segment.
- Add `STATSD_HOST_PATH` to explicitly set only the metric host segment while
  leaving `HOSTNAME` as process identity.
- Preserve the v1 hostname transform by default and allow override.
- Keep current v2 behavior and migrate dashboards.

Recommended solution:

- Add `STATSD_HOST_PATH` and use it in TeamCity production config. Keep
  `HOSTNAME` / `JETMON_HOSTNAME` as stable process identity. This avoids
  brittle hostname inference while preserving production dashboards.

Implementation notes:

- `STATSD_ADDR` in the loaded config selects the UDP endpoint.
- `STATSD_HOST_PATH` selects the Graphite path identity used in
  `com.jetpack.jetmon.<hostname>`.
- `HOSTNAME` / `JETMON_HOSTNAME` selects process identity for bucket ownership,
  process health rows, delivery ownership, and source/audit labels.
- Production Monitor config should set `STATSD_HOST_PATH` to
  `<datacenter>.<node>`, for example `dfw1.jetmon-prod-1` for
  `jetmon-prod-1.dfw1.example.com`.
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

Status: resolved; v1 runtime log-file compatibility is intentionally retired

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
  through `jetpack_monitor_events`, `jetpack_monitor_event_transitions`, `jetpack_monitor_audit_log`, the
  API, dashboard, StatsD, and the v1-style stats API.

Recommended solution:

- Runtime log files are not part of v2. The implementation now removes Docker
  log mounts, logrotate packaging, and docs that promised v1 log-file
  compatibility. Routine scheduler summaries and state-change chatter are
  debug-only so production stdout/stderr stays focused on startup/shutdown,
  warnings, and operational failures.

## 5. Legacy Monitor Health/Status Endpoint Replacement

Status: resolved; use v2 health endpoints and leave `/get/status` out unless a
consumer need is later confirmed

Key difference:

- v1 has an HTTPS listener on port `7800` with `/get/status` returning `OK`,
  and `/put/host-status` for asynchronous Veriflier replies.
- v2 uses JSON-over-HTTP request/response for Verifliers and exposes health via
  the API/dashboard endpoints.

Why this is an issue:

- The Veriflier reply endpoint is intentionally obsolete, but external health
  checks may still call `/get/status`.

Evidence:

- The v2 internal REST API route table currently registers only `/api/v1/...`
  endpoints. No `/api/v2/...` endpoints exist yet, and there is no v2 Monitor
  `/get/status` route.
- The operator dashboard has non-versioned dashboard-only routes:
  `/api/state`, `/api/health`, `/api/host`, and `/api/fleet`.
- `veriflier2` exposes `/v2/check` and `/v2/status` by default. It can expose
  legacy-compatible `/check` and `/status` only when
  `VERIFLIER_ENABLE_LEGACY_HTTP=true`; it does not expose `/get/status`.
- In the v1 branch, `/get/status` is implemented in `lib/server.js` on the
  legacy Monitor HTTPS listener and returns plain `OK`. The v1 Veriflier also
  recognizes `GET /get/status` and replies with service OK.
- Repository evidence for `/get/status` consumers is limited to v1 lab/operator
  helper docs and the v1 Monitor/Veriflier internal protocol code. No in-repo
  production monitoring consumer was found, so the remaining risk is external
  Systems or monitoring configuration outside this repository.

API versioning note:

- The Monitor's `/api/v1/...` surface is the internal operator/product REST API
  and is versioned from its first public contract.
- The Veriflier's `/v2/...` surface is not the Monitor REST API; it is the
  Monitor-to-Veriflier transport contract. It is called `v2` because it
  replaces the original v1 Veriflier TLS/custom protocol and optional
  `/check`/`/status` compatibility endpoints.
- This is visually inconsistent, but changing it before rollout would add
  churn to the already-tested Veriflier contract. Keep the paths as-is and
  document the distinction. If a future Veriflier API grows beyond this narrow
  transport contract, revisit an `/api/vN/...` shape then.

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

- Update docker-deploy/monitoring to the v2 health endpoint. Do not add a
  compatibility `OK` endpoint unless Systems later confirms a consumer that
  cannot be changed before rollout.

## 6. DB Server-Map Parser Behavior And Documentation

Status: implemented; production validation recommended

Key difference:

- v1 parses only the `misc` dataset, uses `WRITE_MASTER` rows for writes, and
  builds local/failover read pools from datacenter matching. It also uses the
  `INTERNET_URI` field for host/port in the inspected code.
- v2 now supports the explicit `DB_*` startup path for local/dev and a
  production `DB_SERVER_MAP_PATH` path that parses the `misc` dataset into
  separate read and write pools.

Why this is an issue:

- Production rollout must configure the server-map path and datacenter
  explicitly so v2 selects the same write master and local/failover read
  targets operators expect from v1.
- Database credential rotation should not require a Monitor recreate shortly
  after rollout.

V1 way:

- Pros: supports live DB map refresh and read/write pool separation.
- Cons: parser is brittle, tied to hostname-derived datacenter, and reloads
  pools lazily.
- Risks: hostname/datacenter assumptions can select unexpected pools.

Current v2 way:

- Pros: explicit local/dev path remains simple; production path separates
  read/write pools, validates changed maps before publication, and hot-reloads
  connection creation without printing secrets.
- Cons: log-only reload visibility for now; dashboard/API reload status is a
  useful follow-up.
- Risks: if `DB_SERVER_MAP_DATACENTER` is omitted in containers, v2 falls back
  to the v1 hostname heuristic and may not prefer the intended local reads.

Resolution options:

- Use explicit `DB_*` for local, smoke, and one-off read-only tests.
- Use `DB_SERVER_MAP_PATH` plus `DB_SERVER_MAP_DATACENTER` for production
  Monitor/Deliverer containers.
- Add dashboard/API visibility for DB reload health after the first rollout.

Recommended solution:

- Use the new server-map manager for production rollout and keep the
  config-sync sidecar as the only component that knows SVN credentials.

## 7. Deprecated V1 Config Key Handling

Status: implemented with startup and `validate-config` warnings

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

Implementation notes:

- Startup, SIGHUP reload, and `jetmon2 validate-config` report
  `WARN config_key=...` lines for deprecated aliases, ignored v1-only keys,
  unknown top-level keys, the old `VERIFIERS` spelling, and `grpc_port`
  Veriflier entry aliases.
- Ignored v1-only keys include `WORKER_MAX_CHECKS` and
  `TIMEOUT_FOR_REQUESTS_SEC`.
- Accepted compatibility keys that warn because their v1 tuning meaning no
  longer applies include `NUM_TO_PROCESS`, `BATCH_SIZE`,
  `VERIFLIER_BATCH_SIZE`, `SQL_UPDATE_BATCH`, `TIME_BETWEEN_CHECKS_SEC`, and
  `TIME_BETWEEN_NOTICES_MIN`.
- Deprecated aliases with real behavior still load but warn:
  `DB_UPDATES_ENABLE`, `BUCKET_NO_MIN/MAX`, `VERIFIERS`, and `grpc_port`
  Veriflier entry aliases.

## 8. Full Codebase Simplification And Legacy-Surface Review

Status: initial cleanup implemented; keep as an ongoing post-rollout review

Goal:

- Review the full codebase for features, compatibility surfaces, deployment
  paths, docs, and helper tools that may no longer be necessary after the
  production rollout plan settles.

Review areas:

- v1 legacy features that no longer have a confirmed consumer and may increase
  risk, maintenance cost, or runtime overhead.
- v2 features added during early design or testing that are no longer needed,
  including deployment paths such as systemd integration if TeamCity/Docker
  becomes the only production Monitor path.
- Compatibility code that can be removed after migration windows close.
- Docs that duplicate, contradict, or over-explain superseded workflows.
- Test/lab tooling that is useful for rollout but should be archived, renamed,
  or isolated after production cutover.
- Runtime and configuration knobs whose existence may confuse operators or
  imply unsupported behavior.

Recommended solution:

- Do this as a separate cleanup branch after the rollout-blocking parity items
  are resolved. Treat each removal as evidence-driven: keep anything with a
  known production consumer, deprecate uncertain surfaces first, and remove
  clearly unused code/docs in small commits with focused tests.

Implementation notes:

- Stale Claude helper docs were the largest low-risk legacy surface found in
  the first pass. They referenced `master`, C++/Node worker guidance, gRPC
  Veriflier paths, MySQL 8.0, Go 1.22, and systemd watchdog behavior that no
  longer matches the current v2 branch. They now point at `AGENTS.md`, target
  `v2`, describe the JSON-over-HTTP Veriflier contract, and prefer internal
  `api-fixture` targets for local Docker tests.
- `docs/project.md` now describes process supervision generically instead of
  presenting systemd as the production Monitor deployment layer. It also
  removes the inaccurate `sd_notify` watchdog claim; the shipped unit is
  currently `Type=simple`.
- `docs/changelog.md` now names `VERIFLIERS[].port` as the current Veriflier
  config key and labels `grpc_port` as a warning compatibility alias.
- No runtime code removal was made in this pass. Systemd units, lab tooling,
  optional `veriflier2` legacy-compatible HTTP endpoints, stats files, pinned
  bucket mode, and deprecated config aliases still have rollout, lab, or
  compatibility value and should be removed only after the relevant production
  migration gates close.
