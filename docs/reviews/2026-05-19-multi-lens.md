# Jetmon 2 — Multi-Lens Critical Review

**Date:** 2026-05-19
**Reviewer:** Claude (multi-agent review, 12 expert lenses + cross-cutting summary)
**Branch:** feature/low-overhead-runtime-metrics
**Scope:** Full repository (`~84K LOC`, 176 Go files, single `cmd/jetmon2` binary + standalone `veriflier2/`).
**Method:** Each lens dispatched as a parallel review agent with a defined reading list and category-tagged output. Findings below are paraphrased and consolidated; every concrete claim cites `file:line` so it can be verified independently.

> ⚠️ The reviewer did not run code, did not modify any project file other than this one, and did not execute migrations or tests. Findings are reading-based and should be confirmed before action.

---

## Executive summary

Jetmon 2 is a **well-architected production-ready Go service** with disciplined engineering: event-sourced state, transactional projection writes, ADR-driven decisions, exhaustive operator docs, sane shutdown/reload semantics. The team's biggest *engineering* risks are not in the code itself but at three boundaries:

1. **Secrets at rest** — webhook signing keys and alert-contact credentials are stored plaintext (`internal/webhooks/webhooks.go:30-34`, `internal/alerting/alerting.go:28-33`). A DB compromise = total downstream compromise. Roadmapped, but the most consequential finding by far.
2. **SSRF / DNS rebinding TOCTOU** — `internal/checker/checker.go:1108-1123` resolves DNS, validates, then dials. The dial happens later; a controlled DNS server can flip to RFC1918 between the two. Mitigate at the dialer (`net.Dialer.Control`).
3. **Multi-instance prerequisites are not built** — in-memory idempotency (`internal/api/idempotency.go:25-28`) and in-memory rate-limit (`internal/api/ratelimit.go:33-130`) do not survive the multi-binary split foreshadowed in `docs/roadmap.md`. Plan for Redis or DB-backed primitives before that split lands.

The **biggest organizational risk** (per the management lens) is unrelated to code: six WPCOM/Product sign-off items in `docs/jetmon-v2-prelaunch-readiness.md:47-61` are stalled and the legacy-consumer inventory is incomplete. Rollout could stall in the production window waiting for verbal approval.

The **biggest operational risk** is data growth: `jetmon_audit_log` and `jetmon_check_history` are append-only with no documented retention/partitioning, and the orchestrator's retry queue is in-memory only (lost on kill -9).

The **biggest user-facing risk** is documentation framing: the REST API doc is labelled "internal only" but is the document third-party integrators will read. A novice-facing quick start with a Hello-Webhook example is missing.

---

## Lens 1 — Go expert

### Happy paths
- Event-sourced state in `internal/eventstore/` is exemplary: single-writer pattern keeps projection and transition log in lockstep within one transaction; consistent `%w` wrapping; transition history is immutable by design.
- Goroutine lifecycle is disciplined: `sync.WaitGroup` use in `internal/checker/pool.go:118-120` and `internal/webhooks/worker.go:147-154`; `time.After` paired with context cancellation in `internal/orchestrator/orchestrator.go:407-410, 421-424`; pool drains by `close(p.work)` under mutex (`pool.go:109-111`).
- Connection pools tuned for scale: `MaxIdleConnsPerHost=2048`, `MaxIdleConns=8192` (`internal/checker/checker.go:393-394`); Veriflier client `MaxIdleConnsPerHost=1024` (`internal/veriflier/client.go:75`); DB pool caps at 256 with streaming multiplier (`internal/db/manager.go:76-94`).
- SSRF guards centralized in `internal/netguard/`; unsafe CIDR checks run before any probe.

### Concerns / vulnerabilities
- **Panic in production code path**: `internal/netguard/netguard.go:43` `mustParseCIDRs` panics on malformed CIDR. A hot-reload that introduces a bad CIDR will crash the process. Replace with error return.
- **Channel cancellation leak window** in `internal/orchestrator/streaming.go:688-694, 743-755` — background goroutines send to 1-buffered telemetry/reload channels; if the receiver exits first, sends could block. Low probability, but accumulates under rapid config reload.
- **Silent rollback errors** — repeated `defer func() { _ = tx.Rollback() }()` across `internal/orchestrator/orchestrator.go:2563+, 2603+`. No audit of rollback failure.

### Bugs / races
- **Dual-mutex on veriflier cooldowns** — `internal/orchestrator/orchestrator.go:272` map is protected by `veriflierCooldownMu` (lines 3388, 3424, 3444) but read elsewhere under `veriflierMu` (line 3236+). Unify under one lock or replace with `sync.Map`.
- **Allocates a no-op `eventstore.Store{db: nil}` on every call** when `o.events == nil` (`orchestrator.go:323-327`). Cache it or fail fast.
- WaitGroup Add/Done split across functions in `streaming.go:458-459, 522` is fragile.

### Confusing implementation
- Mutexes guarding scalar fields where atomics would suffice (`wpcomPermanentMu`, `orchestrator.go:2479`).
- Hot-reload reconfigures WPCOM client and dashboard source (`cmd/jetmon2/main.go:344-360`) but does **not** re-validate bucket ownership or stop in-flight webhook deliveries when webhooks are disabled. The hot-reload boundary is undocumented.
- Streaming scheduler reallocates `sideEffectProcessor` and maps on every 0↔1 active-site transition (`streaming.go:711-732`).

### Performance / allocation pressure
- Veriflier client `fmt.Sprintf` for per-request IDs allocates each call (counter suffix is per-request even though prefix is cached).
- Unbounded slice growth in `logMarkChecked` error accumulator (`orchestrator.go:1247`).
- Naked `go func` in `cmd/jetmon2/main.go:181, 191, 204, 321, 339`. Some have error logs, two do not.

### Missed opportunities / feature gaps
- No webhook secret rotation API (acknowledged in `internal/webhooks/webhooks.go:30-34`).
- No alert grouping for fleet-wide events.
- No distributed tracing for check → event → delivery causality.
- Untyped `interface{}` context-value extraction in `internal/checker/checker.go:84-90` — use a typed key package.

---

## Lens 2 — MySQL / MariaDB expert

### Critical / high
- **UTC consistency drift** — `jetmon_check_targets` uses `TIMESTAMP(3)` but `jetmon_site_runtime` uses bare `DATETIME` (`internal/db/migrations.go:487-489` vs `528-534`). `TIMESTAMP` stores UTC; `DATETIME` stores server-local. Scheduling and alert-cooldown comparisons can drift across hosts with different TZ. **Standardize on `TIMESTAMP(3)` for every temporal column.**
- **Plaintext webhook secrets** in `jetmon_webhooks.secret VARCHAR(80)` (`migrations.go:199`, comment at 179-185 acknowledges). Database dump = downstream compromise of every consumer.
- **Plaintext alert-contact credentials** in `jetmon_alert_contacts.destination` JSON (`migrations.go:280`).
- **Sub-second precision mismatch** — `jetmon_webhook_dispatch_progress.updated_at` is plain `TIMESTAMP` but transitions are `TIMESTAMP(3)` (`migrations.go:245-248`). Possible watermark sort races.

### Index coverage gaps
- `eventstore.FindActive` filters on `(blog_id, check_type, ended_at)` but the index is only `(blog_id, ended_at)` (`migrations.go:128`). Add `idx_blog_id_check_type_active`.
- `GetSitesForBucket` LEFT JOINs `jetmon_site_runtime` and `jetmon_site_check_config` on `blog_id` without a covering composite — add `(blog_id, next_check_at)` to runtime.
- Projection-drift query (`internal/db/queries.go:268-299`) full-scans via `OR endpoint_id IS NULL`. Slow at scale.

### Schema design
- **No FK constraints** anywhere — `event_transitions → events`, deliveries → webhooks/alert_contacts, etc. Soft-delete audit model is the rationale, but it relies on Go-code discipline to avoid orphans.
- Default `utf8mb4` but no explicit `COLLATE` on PKs / dedup keys; external tools using a different collation could create accidental case-insensitive collisions.
- No SQL injection risk observed — every query is parameterized.
- `jetmon_audit_log` is unbounded append; no partitioning / retention scheme (called out as future work but the operational risk is now).

### Transactions / concurrency
- Default REPEATABLE READ is appropriate.
- Eventstore "single-writer" pattern is **enforced in code only**, not schema. Acceptable but fragile.
- Delivery claim uses `FOR UPDATE SKIP LOCKED` with a 60s `next_attempt_at` lease — race-safe (`internal/webhooks/webhooks.go:127`).
- `jetmon_hosts` bucket coordination uses `SELECT ... FOR UPDATE` (`db/queries.go:233, 254`) — no schema-level dedup of bucket ranges; relies on transaction isolation.

### Write amplification
- Each event mutation = event row + transition row + (optional) v1 projection row = up to 3 writes (orchestrator.go:30-38, 220). Removable after v1 readers migrate.
- `UpdateSSLExpiry` unconditionally UPDATEs `jetmon_site_runtime` every HTTPS check (~166 writes/sec at 50k HTTPS sites). Add skip-if-unchanged.
- `jetmon_check_history` grows at ~333 rows/sec at 100k sites × 5-min cadence — no retention.

### Migrations
- 50 idempotent migrations, all wrapped in `IF NOT EXISTS`. No destructive operations. Safe.
- No explicit `ALGORITHM=INSTANT, LOCK=NONE` on online ALTERs — relies on MariaDB optimizer choice.

---

## Lens 3 — Docker expert

### Issues / vulnerabilities
- **No base-image digest pinning** — `debian:bookworm-slim`, `mariadb:11.4`, `axllent/mailpit:v1.29`, `ghcr.io/automattic/veriflier:latest` are all floating tags. Breaks reproducibility (`docker/docker-compose.yml`, `docker/docker-compose.veriflier-prod.yml:3`).
- **Secrets via env with hardcoded defaults** — `MYSQL_ROOT_PASSWORD:-123456` (`docker-compose.yml:6`), `WPCOM_AUTH_TOKEN:-change_me`, `VERIFLIER_AUTH_TOKEN:-veriflier_1_auth_token`. Prod compose correctly uses `${VAR:?...}` but the message ("set X") is operator-hostile.
- **`chmod 777 stats certs`** in `docker/Dockerfile_jetmon:27-29`. Should be 755.
- **`init-mysql-user.sh:60` grants ALL PRIVILEGES** to the app user — too broad. Restrict to SELECT/INSERT/UPDATE/DELETE/CREATE/ALTER/DROP.
- **`veriflier-prod.yml` defaults `VERIFLIER_BIND_ADDR` to `0.0.0.0`** — internet-facing if the host is. Should default to `127.0.0.1` and require explicit override.
- **No `cap_drop: [ALL]`**, no `read_only: true`, no `security_opt: [no-new-privileges:true]` anywhere.
- **No `logging:` driver config** — default `json-file` is unbounded. Add `max-size`/`max-file`.
- **No resource limits** — `deploy.resources.{limits,reservations}` missing. A memory leak takes the host.

### Happy paths
- `init: true` on jetmon and veriflier (PID 1 = tini, signals reaped).
- All Dockerfiles create non-root users with `-r --no-log-init`.
- All entrypoints `exec ./...` — no bash on PID 1 swallowing signals.
- Healthchecks present everywhere; `api-fixture` uses a dedicated `healthcheck` subcommand pattern (cleanest of the lot).
- Local-compose ports bound to `127.0.0.1`.
- `Dockerfile_api_fixture` builds from `scratch` — minimal surface.
- `mysql-user` one-shot uses `restart: "no"`.

### Build hygiene / performance
- `Dockerfile_jetmon:8` `COPY . .` instead of layered `go.mod`/`go.sum`-then-source — every code change invalidates the module-download cache.
- No `--mount=type=cache` for the Go module cache.
- `.dockerignore` adequate; could exclude `.github/` and large `testdata/`.
- No multi-arch (amd64 only); no buildx setup visible.

### Compose specifics
- Scale-lab: four jetmon instances share `../config` and `../stats` bind mounts (`docker-compose.scale-lab.yml`) → PID file contention.
- `jetmon` healthcheck: 12 × 10s = 120s before unhealthy is too generous.
- `depends_on: condition: service_healthy` does not guarantee app readiness (only that healthcheck passes). Migration-on-startup needs an additional probe gate.

---

## Lens 4 — Security expert (highest-stakes lens)

### Critical
- **Webhook secrets stored plaintext** (`internal/webhooks/webhooks.go:30-34`, `jetmon_webhooks.secret`). DB breach forges every HMAC.
- **Alert-contact credentials stored plaintext** (`internal/alerting/alerting.go:28-33`). PagerDuty/Slack/Teams/SMTP creds exfiltratable via DB dump.

### High
- **DNS rebinding TOCTOU** — `internal/checker/checker.go:1108-1123` validates once, dials later. Re-validate at the dial via `net.Dialer.Control`.
- **In-memory idempotency** — `internal/api/idempotency.go:25-28` is per-instance. Multi-instance future will allow duplicate execution of critical operations.
- **HMAC replay** — server includes a timestamp in the signature but does not enforce a max-age (Stripe-style offload). Should be made explicit in docs and ideally enforced server-side too.
- **In-memory rate limit** — `internal/api/ratelimit.go:33-130` per-instance, so N instances multiplies effective rate by N.

### Medium
- Custom outbound headers allow `User-Agent` override (`internal/checker/checker.go:916-920`).
- CSP allows `'unsafe-inline'` on dashboard; dashboard does correctly use `.textContent` so not directly exploitable, but DiD is weak.
- No CSRF token on dashboard mutations — mitigated by 127.0.0.1 binding only.
- No server-side scrub of Authorization headers in debug-level logs (`internal/api/middleware.go:105-112`).

### Strong / good (worth keeping)
- TLS verification enabled (`checker.go:359-360`, `InsecureSkipVerify: false`).
- All queries parameterized; no SQL injection observed.
- Redirect handler caps at 10 hops and re-validates each hop via `netguard.UnsafeHost()` (`checker.go:876-888`).
- `internal/netguard/netguard.go` blocks RFC1918, loopback, link-local, multicast, .local/.internal/.localhost, TEST-NET-*, CGNAT 100.64/10.
- Tiny dependency set: `go-sql-driver/mysql`, `go-sqlmock`, `edwards25519`. Minimal supply chain.
- pprof binds localhost-only (`cmd/jetmon2/main.go:189-194`).
- Veriflier auth token compared with `crypto/subtle.ConstantTimeCompare` (`internal/veriflier/server.go:362`).
- API keys SHA-256-hashed at rest.

---

## Lens 5 — Performance engineer

### Throughput
- `DisableKeepAlives: true` in the **HTTPS** site-check transport (`internal/checker/checker.go:372`) is justified for unique-host fleets but loses connection reuse for repeat-checks against the same host. The plain HTTP IP-pool transport (`checker.go:73`) keeps keepalives. Consider a hybrid: pool by host-frequency, with the high-frequency cohort keeping connections.
- Goroutine pool autoscale runs on a **5-second ticker** (`internal/checker/pool.go:150-159`). Under a flash of 10K pending checks, the pool waits up to 5s before scaling up. Reactive scale-up would help.
- Result channel cap of `maxSize*2` (`pool.go:33-34, 48-49`): result drain becomes a fan-in bottleneck. Decouple with a results multiplexer.

### p99 latency
- Timer allocation per page in orchestrator (`orchestrator.go:593, 810-817`) creates GC pressure at high page counts. Pool the timers.
- StatsD is fire-and-forget UDP with a `c.mu` lock per metric (`internal/metrics/metrics.go:103-121`). Batch into MTU-sized packets.
- `json.Marshal` in the Veriflier client allocates each batch (`internal/veriflier/client.go:426, 484`). Pool encoders / codegen marshal (easyjson) for a fixed schema.
- Event mutation retries have no jitter (`orchestrator.go:62-63`, `eventMutationRetryBaseDelay = 25ms`). Add jitter to break thundering-herd.

### DNS layer
- 15-min TTL with jitter (`checker.go:48-49`) is reasonable, 2M-entry cache adequate, but **no negative caching** for NXDOMAIN/SERVFAIL. Add 5–10s negative TTL.
- Cache uses `sync.RWMutex` on every lookup — contention under ≥1K concurrent DNS. Consider a lock-free LRU (ristretto / generation cache).
- Purge runs every 10K writes (`checker.go:276`). Bursty writes leave stale entries lingering.

### Big-picture wins / gaps
- ✅ Event sourcing, transactional projection, batch check-history inserts, per-Veriflier client pooling, custom DNS resolver.
- ❌ pprof always registered — no runtime toggle.
- ❌ No payload compression on the Veriflier transport. Gzip would cut bandwidth 10–50× for batch JSON.
- ❌ No binary protocol option (protobuf) for internal Veriflier traffic.
- ❌ No per-target rate limiter — fleet-wide flood against any single multi-tenant host (WordPress multisite) appears as DDoS to that host.
- ❌ No adaptive check intervals — healthy sites checked at the same rate as flaky ones.

---

## Lens 6 — QA / test engineer

### Strengths
- 83 test files, ~34.6K LOC of tests against the production code. Heavy concentration in the right places: orchestrator (100 tests), checker (71), api (25+ handler files), eventstore, webhooks, alerting, veriflier.
- Table-driven tests where it counts; 294 `httptest` uses — strong contract validation rather than mock-only.
- 34 files use `go-sqlmock`, with strict matcher (`QueryMatcherEqual`) — schema/query contract tested.
- Clock injection: `fakeClock` (`internal/api/ratelimit_test.go`), `nowFunc` override (orchestrator tests).
- Veriflier soak test (`veriflier2/.../soak_test.go`) simulates concurrent batches with partial failures and tracks peak concurrency atomically.
- HMAC contract tested via signature round-trip (`webhooks_test.go:46-85`).

### Concerns
- **No `t.Parallel()` anywhere across 83 test files.** Serial execution; entire suite slower than necessary.
- **Only 2 benchmarks** (both keyword-related). No baselines for pool throughput, eventstore writes, API latency.
- **No fuzz tests** despite Go 1.18+ availability — header parsing, JSON unmarshaling, custom-header validation are obvious targets.
- **`coverage.out` is checked in** at the repo root, 50KB, mode:set, 687 lines. Either it's stale or it's a manual snapshot — neither is a CI gate. Coverage is not enforced in pipeline.
- Hand-rolled `strings.Contains` error matching (e.g. `fleethealth/health_test.go:211`) instead of `errors.Is/As`. Fragile to message rewording.
- Comment "flaky on slow CI" in `ratelimit_test.go:10` indicates an unfixed flake.
- `t.Fatalf` dominates `t.Errorf` — stops on first failure; harder to see all problems in one run.
- API handler tests hardcode 600+ char SQL strings — hard to keep in sync with schema.

### Gaps
- SIGHUP hot-reload safety: `config_test.go` validates parsing; no orchestrator/checker restart safety tests.
- Memory-pressure drain (`WORKER_MAX_MEM_MB`) behavior not simulated.
- Bucket heartbeat reclaim race not exercised at the unit-test layer (only rollout tests).
- Delivery-claim transactional isolation tested via smoke script, not unit tests.
- Event-to-projection drift invariant not asserted by a test (AGENTS.md says "must not drift").
- No snapshot/golden tests for API response shapes.
- No CI run of `go test -race` visible (`.github/workflows/docker-publish.yml` is image build only).

---

## Lens 7 — Sysadmin (production 3am)

### Strong
- `systemd/jetmon2.service` is excellent: `Restart=on-failure`, `StartLimitBurst=5 / IntervalSec=60s` prevents thrash; `TimeoutStopSec=35s` with a hard `os.Exit(1)` floor at 30s; SIGINT/SIGTERM/SIGHUP all handled; `MemoryMax=512M`, `LimitNOFILE=65536`; `ProtectSystem=full`, `PrivateTmp=yes`, `NoNewPrivileges=yes`; `RuntimeDirectory=jetmon2` at mode 0750.
- SIGHUP hot reload is atomic across `config.Reload()`, DB config refetch, WPCOM client rebuild, dashboard reinit (`cmd/jetmon2/main.go:343-359`).
- `validate-config` subcommand dry-runs parse + DB + Veriflier reachability before deploy.
- Deprecated config aliases (`DB_UPDATES_ENABLE → LEGACY_STATUS_PROJECTION_ENABLE`, `BUCKET_NO_MIN/MAX → PINNED_BUCKET_MIN/MAX`) preserve v1 configs with warnings.
- `./jetmon2 keys {create,list,revoke,rotate}` and the API rollout subcommand (with dry-run) give operators direct control.

### Weak / risky
- **In-memory retry queue is not crash-durable**. AGENTS.md:311-313 warns about not flushing it at round start; on kill -9, the counter resets and a flaky site that was at 2 of NUM_OF_CHECKS failures starts over. Events remain durable but escalation cadence is lost.
- **PID file write does not halt startup on failure** (`cmd/jetmon2/main.go:148` logs a warning). Later `drain` / `reload` subcommands `log.Fatalf` if it's missing. Inconsistent.
- **`stats/` writability not pre-flighted** in the systemd path. Docker entrypoint warns; systemd does not.
- **Silent StatsD failure** — bad `STATSD_ADDR` logs a warning at startup, then no metrics are emitted; only visible by absence.
- **`WPCOM_NOTIFY_LEGACY_INSECURE_SKIP_VERIFY` defaults to `true`** (`config/config.readme:191`). v1 parity, but no startup warning.
- **`CHECK_DNS_RESOLVERS` is the only knob that requires a restart** on reload — surprising relative to the other hot-reloadable fields.
- **No Prometheus endpoint**. Modern SRE stacks expect `/metrics`; this requires statsd_exporter or a custom bridge.
- **No documented retention policy** for `jetmon_audit_log` and `jetmon_check_history`. Unbounded growth.
- **No logrotate snippet** for non-systemd / bare-metal deployers.
- **No correlation IDs** for log lines; cannot pivot from customer report to log lines.

---

## Lens 8 — Novice end-user (small WordPress agency dev)

### Documentation framing problem
- `docs/internal-api-reference.md` opens with "**Audience: internal systems only.**" — but it is the only API reference. Public-API consumers via the gateway will land here, read "internal only," and be confused.
- No quick-start aimed at the integrator: "1. Create a site. 2. Verify it's monitored. 3. Add a webhook. 4. See the first delivery." README jumps straight to architecture; `docs/getting-started.md` targets developers building/testing Jetmon itself.

### Conceptual confusion
- *Event vs. Transition vs. Alert vs. Webhook vs. Alert Contact* — five distinct concepts, no glossary. A novice reading the API reference encounters all of them within the first page.
- *Veriflier* — appears 20+ times without "this is a geo-distributed confirmation server that votes on whether a site is truly down."
- *Severity vs. State* — explained correctly in `internal-api-reference.md:397-399` but not surfaced anywhere a novice would see it. A `Warning`-severity event with `min_severity=Down` will silently not page; novice will think alerts are broken.

### Missing examples / artifacts
- No curl examples for webhooks or alert contacts (only `api-cli-guide.md` CLI commands).
- No PagerDuty walkthrough showing where `integration_key` comes from, what severity maps to PagerDuty `critical`.
- No webhook payload verification snippet (Go / PHP / Python). Consumers must reimplement HMAC verification themselves.
- No OpenAPI / Swagger spec.
- No Postman collection.
- No SDK in any popular language (Go/Python/PHP/JS).
- No Terraform provider.
- No UI for adding sites (API-only).

### Error-message gaps
- Authentication failure shapes are specified but not exemplified.
- Webhook delivery `last_response` is truncated — undocumented truncation length.
- Alert contact send-test surfaces transport errors directly — no documented example of a malformed-credential response.
- `EMAIL_TRANSPORT` typo behavior: "warns at startup" — unclear what "warns" means (log line? stderr? exit code?).

### Worth keeping
- `config/config.readme` is unusually complete.
- `api-cli-guide.md` is genuinely usable.
- Dashboard model (HTML + EventSource) is the right shape for non-engineer operators.
- Webhook signing scheme is documented in detail (Stripe-style `t=,v1=`).

---

## Lens 9 — Expert end-user (Automattic SRE)

### Strong (real power-user wins)
- **Event sourcing is genuinely load-bearing** — every state/severity change is a row with `severity_before/after`, `reason`, `source`, `metadata`. Reconstructable timelines.
- **Streaming scheduler is production-validated** with documented ceilings (2M targets/host stable, 4M past envelope) and clear hold gates (timeout pressure, queue depth, coverage loss).
- **Rollout flow is well-engineered** — API-driven with idempotency keys, dry-run/confirmation tokens, resume-from-interrupt, three paths (API-container preferred, host-based fallback, guided flow with transcript).
- **CLI-first operations** — `jetmon2` subcommands cover everything an operator needs to script.

### Pain points
- **No SLO/SLA endpoint.** Cannot ask "what's the 99.9% uptime for site X last month?" from Jetmon. Must build batch jobs over event tables.
- **Drain semantics opaque** — no `/api/v1/monitor/drain-status` showing pending checks, retry queue size, ETA. `systemctl stop` is fire-and-pray.
- **No bulk override endpoints** — no bulk-close, no bulk force-promote, no bulk detection-profile change. Manual loops only.
- **No force-promote (Seems Down → Down)** — if Veriflier consensus is genuinely broken, operator must close, manually notify WPCOM, reopen. Not atomic.
- **No replay/retrigger** — no `POST /api/v1/sites/{id}/trigger-check` to force out-of-cadence probe after a fix.
- **Veriflier quorum diagnostics missing** — no `/api/v1/verifliers/quorum-report` showing active vantages, last-seen, current votes on pending events.
- **Audit log not API-queryable** — `jetmon_audit_log` rows must be queried in MySQL directly.
- **Schema-version endpoint absent** — no `/api/v1/monitor/schema-version` to confirm a migration applied during rollout.
- **Static Veriflier trust** — `jetmon_veriflier_vantages` rows are manually inserted; no self-registration, no automatic fallback below floor.
- **Drain timeout not configurable** — no `DRAIN_TIMEOUT_SEC`.
- **WPCOM circuit breaker state not exposed** — no endpoint for queue depth, drop count, breaker state.
- **Feature flags are per-site config, not gradual ramp percentages.**

---

## Lens 10 — Engineering management

### Maturity signal — 8/10
- Active rollout convergence over the last ~50 commits (rollout scaffolding, Veriflier hardening, metrics).
- 72 markdown files, ~15K lines of docs. 10 ADRs with context/decision/consequences. AGENTS.md is 28KB of operational guidance.
- Static Go binary deployment removes Node.js / native-addon complexity.
- TODO/FIXME count is small (~11), most in test setup or operator prompts.
- GPL v2 license. Clean dependency footprint (3 direct deps).

### Organizational risk — 5/10 (where the actual rollout risk lives)
- **Six WPCOM/Product approval items in `docs/jetmon-v2-prelaunch-readiness.md:47-61` show no progress.** Not technical gates — sign-offs on launch posture. These can stall the rollout in the production window waiting for verbal approval.
- **Legacy consumer inventory incomplete** (`docs/roadmap.md:134-140`) — "hidden consumers" outside this checkout. Without confirmation, rolling out could break downstream integrations (XML-RPC, Activity Log, Elasticsearch).
- **Delivery-ownership transition window** is the most architecturally fragile period: embedded → standalone deliverer (`docs/roadmap.md`) is not yet built, and no end-to-end test rehearses it.
- **No on-call runbook** linked from README or operations-guide. New on-call engineers cannot triage 3am delivery backlog or Veriflier quorum loss from current docs alone.
- **Coverage gate** absent in CI — `coverage.out` checked in but not enforced.
- **Single load-bearing author** signal — AGENTS.md is one person's operational understanding crystallized. Bus-factor mitigation should include an explicit incident playbook.

### Hiring ramp
- Mid-level Go engineer reaches "fix simple bugs" in 3–5 days, "ship a feature" in 1–2 weeks. Healthy.

### Strategic
- Vendor lock-in is minimal (MariaDB schema-coupled but additive; StatsD names are vendor-agnostic; WPCOM coupled via a circuit-breaker).
- Multi-binary split deferred — correct call. Ship monolith, split after observational data exists.

---

## Lens 11 — Network engineer

### Strong
- HTTP/1.1 + intentional `DisableKeepAlives: true` for site-check transport against mostly-unique hosts; separate IP-pool transport with keepalives + 8K/2K idle conns for hostname reuse.
- HTTP/2 for Monitor → Veriflier with `ForceAttemptHTTP2: true`, `StrictMaxConcurrentRequests: true`, and a batching layer (light/full queues, 64/128 caps) that prevents head-of-line blocking under floods.
- 30s TCP `KeepAlive` on both checker and Veriflier dialers.
- Dual-stack IPv4/IPv6 with fallback (sequential, not RFC 8305 Happy Eyeballs; not critical for monitoring).
- DNS cache 2M entries / 15-min TTL / 3-min jitter; DNS lookup limiter at `GOMAXPROCS*128` capped 1024.
- HMAC-SHA256 webhook signing with retry ladder (1m, 5m, 30m, 1h, 6h); per-webhook in-flight caps + shared pool.
- SSL expiry tracking from the TLS handshake; deprecated TLS 1.0/1.1 flagged.
- `FOR UPDATE SKIP LOCKED` delivery claim — correct multi-worker pattern.

### Gaps
- **No `https://` for Monitor → Veriflier** — only plaintext HTTP. Bearer-token auth in clear. Acceptable inside a trusted DC; risky over WAN without VPN. No mTLS option.
- **Veriflier client respects `HTTPS_PROXY`** (`internal/veriflier/client.go:69`) — unintended if Verifliers are inside the network.
- **DNS cache TOCTOU window**: cache hit returns IP, validation runs after. Not exploitable in normal config (validation still runs) but worth noting for rebinding.
- **No per-target rate limit / 429/503 backoff** — fleet-wide checks against a single multi-tenant host (WordPress multisite) look like DDoS to that host.
- **Linux ephemeral port range / TIME_WAIT not tuned** — at >50K concurrent outbound, ephemeral exhaustion is possible without sysctl tuning.
- **No TCP_NODELAY** — Nagle can add ~40ms per direction on small request/response; not critical at monitor latencies but worth setting.
- **No fast-fail for a dead Veriflier** — Monitor will keep sending full batches until context deadline; no exponential backoff / circuit breaker per Veriflier.
- **No Happy Eyeballs**, deterministic IP ordering within family (no shuffle for large A/AAAA sets), no `prefer_ipv6` per-site.
- **Webhook delivery has no certificate pinning** for consumer endpoints.
- **Compose is IPv4-only** — no IPv6 networks defined. Production IPv6 site checks untested.
- **Idle-conn timeout mismatch**: Monitor 30s vs Veriflier 90s (`checker.go:395` vs `client.go:76`) — can produce "connection reset by peer" if Monitor reaps first.

---

## Lens 12 — DevOps engineer

### Pipeline gaps
- **Only one CI workflow** (`.github/workflows/docker-publish.yml`), and it only builds Docker images, only on push-to-`v2` or labeled PR. **No lint, no test, no `go test -race`, no SBOM, no container scan, no dependency audit.**
- `make rollout-docs-verify` exists but is not gated in CI — operators can skip it.
- Go module download not cached via BuildKit `--mount=type=cache`.

### Release / versioning
- **No `VERSION` file**, no semantic-version tags visible. Build embeds `git describe` via ldflags but most binaries will report "dev".
- Docker images tagged `latest` on `v2`, SHA-based on PRs — no semver tags. Operators cannot pin a specific release.
- Only `linux/amd64` images — no `linux/arm64`. Mac M-series users hit `--platform` workaround.
- No published binary releases (only container images).
- No automated changelog.

### Supply chain
- No Trivy / Nancy / Snyk in CI.
- No SBOM (CycloneDX/SPDX), no SLSA attestation, no image signing (Cosign).
- `go.sum` is small and clean (good) but never validated in CI.

### Migration / deploy
- Single migration file (`migrations/001_jetmon2.sql`); idempotent, additive. Good.
- **Migration runner is manual** — must be invoked before service start; healthcheck does not wait on migration completion.
- **No `/api/v1/ready` separate from `/api/v1/health`** — load balancers can't distinguish "starting up" from "ready to take traffic".
- **No IaC** — no Terraform/Ansible/Helm; deployment is rsync + systemctl.
- Rollout is excellent but **operator-driven** — every activation requires human confirmation. No GitOps.

### Strong
- Reproducible static binaries (`CGO_ENABLED=0`, ldflags-injected version).
- Tight direct-dependency set.
- Multi-stage Dockerfile, non-root runtime, minimal deps.
- Rollback is well-modeled (`stage-policy --mode=rollback-last-stage` + dual-write window via `LEGACY_STATUS_PROJECTION_ENABLE`).
- `init: true` on long-running services for proper signal forwarding.

---

## Cross-cutting themes

Patterns that emerged across multiple lenses:

| Theme | Lenses that hit it | Impact |
|------|--------------------|--------|
| **Plaintext secrets at rest** (webhook, alert-contact) | Security, MySQL, Management | Most consequential single finding. Roadmapped but uncapped blast radius until done. |
| **In-memory state that won't survive the multi-binary split** (idempotency, rate-limit, retry queue) | Security, Sysadmin, Expert UX | Builds in a hidden timer that goes off at the architectural transition. |
| **Append-only tables without retention policy** (`jetmon_audit_log`, `jetmon_check_history`) | MySQL, Sysadmin, Expert UX | Unbounded growth → degraded INSERTs at production scale. |
| **CI is image-only — no lint/test/race/scan gates** | QA, DevOps, Management | Bad PRs can merge; supply-chain regressions invisible. |
| **No Prometheus / OpenTelemetry surface** | Sysadmin, Performance, Network, DevOps | Modern SRE stacks have to bridge or scrape something custom. |
| **No correlation/trace IDs** | Sysadmin, Performance, Network, Expert UX | Operators can't pivot from customer report → log → event → delivery. |
| **No per-target rate-limit / backoff** | Performance, Network, Security | Fleet-wide checks against a single multi-tenant host look like a DDoS. |
| **Docs framing — "internal only" doc is the de-facto integration spec** | Novice UX, Expert UX, Management | First impression for anyone integrating through the gateway. |
| **Org gates (WPCOM/Product sign-off) ungated** | Management, Expert UX | Rollout is technically ready before it is organizationally ready. |
| **Base-image and `:latest` non-pinning** | Docker, DevOps, Security | Reproducibility + supply-chain surface. |

---

## Top-10 prioritized action list (reviewer's read)

In rough decreasing impact, mixing severity and feasibility:

1. **Encrypt webhook + alert-contact secrets at rest** (envelope encryption with a master key in env/secret store; one DB-side change + one rotation pass). Critical.
2. **Re-validate destination IPs at the dialer** (`net.Dialer.Control`) to close the DNS-rebinding TOCTOU. High.
3. **Add a real CI gate** (`go vet`, `go test`, `go test -race`, `golangci-lint`, dependency scan) on every PR, not only Docker builds. High.
4. **Retention/partition strategy** for `jetmon_audit_log` and `jetmon_check_history` — published policy + a `cleanup` subcommand. Medium-high.
5. **Standardize on `TIMESTAMP(3)` UTC** across `jetmon_site_runtime` and any other lingering `DATETIME` columns. Medium-high.
6. **Add `/api/v1/ready`** distinct from `/health` (covers migration done + bucket ownership + Veriflier discovery). Medium.
7. **Plan now for Redis-backed idempotency + rate-limit + retry queue** before the multi-binary split. Medium.
8. **Replace `panic` in `internal/netguard/netguard.go:43`** with error return; verify all `mustParse*` paths. Easy/medium.
9. **Operator-facing endpoints**: `/api/v1/monitor/drain-status`, `/verifliers/quorum-report`, `/audit-log` query API, `/sites/{id}/trigger-check`. Medium.
10. **Documentation re-framing**: extract a "Integrator quick-start" with a hello-webhook example, glossary, PagerDuty walk-through, and verification snippets in 3 languages. Medium.

**Bonus (organizational, not code):** drive the six stalled WPCOM/Product approvals in `docs/jetmon-v2-prelaunch-readiness.md:47-61` to closure before the production rollout window opens.

---

## What this review intentionally did not cover

- Actual code execution / test runs — purely reading-based.
- Performance under live load — only static analysis of hot loops.
- Veriflier-side internals beyond client/server contract (`veriflier2/` deserves its own pass).
- Front-end/dashboard JS in depth (`internal/dashboard/`) — only security and UX touched it.
- The `proto/veriflier.proto` (kept as future-transport schema reference per AGENTS.md, not on the v2 path).
- ADRs in `docs/adr/` were sampled but not individually reviewed for technical merit.
- The `.codex-*` directories were ignored as not part of the production codebase.

A follow-up pass should re-confirm any single finding before action; in particular, the multi-mutex Veriflier-cooldown claim, the rate-limit "in-memory" claim, and the precise HMAC server-side replay behavior would each benefit from a hands-on read of the cited lines.
