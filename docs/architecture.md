Jetmon 2 — Architecture Overview
==================================

This document describes the internal architecture of Jetmon 2 and the complete
call flow used to determine and report site status.


System Overview
---------------

Jetmon 2 runs as a Go monitor binary (`jetmon2`). Multiple monitor instances can
run on different hosts, each owning a non-overlapping range of site buckets
claimed from MySQL. Outbound webhooks and alert contacts can still run embedded
inside one API-enabled `jetmon2` process, or through the standalone
`jetmon-deliverer` binary as the first step toward the post-v2 process split.

```
                          ┌─────────────────────────────────────────┐
                          │                 jetmon2                 │
                          │                                         │
  ┌──────────┐  sites     │  ┌─────────────┐    ┌─────────────────┐ │
  │  MySQL   │──────────► │  │ Orchestrator│───►│  Checker Pool   │ │
  │ (bucket) │◄────────── │  │  (1 gorout.)│◄───│  (N goroutines) │ │
  └──────────┘  updates   │  └──────┬──────┘    └─────────────────┘ │
                          │         │                               │
  ┌──────────┐  confirm?  │         │ escalate                      │
  │Veriflier │◄───────────│─────────┘                               │
  │ (remote) │──────────► │                                         │
  └──────────┘  result    │  ┌───────────┐  ┌──────────┐            │
                          │  │   WPCOM   │  │Dashboard │            │
  ┌──────────┐  notify    │  │  Client   │  │  (SSE)   │            │
  │  WPCOM   │◄───────────│  │(circuit-  │  │          │            │
  │   API    │            │  │ breaker)  │  │          │            │
  └──────────┘            │  └───────────┘  └──────────┘            │
                          └─────────────────────────────────────────┘
```

Multiple jetmon2 instances coordinate through MySQL bucket leases:

```
  Host A  ──────  buckets 0–499
  Host B  ──────  buckets 500–999
  Host C  ──────  (takes over Host B's range if B goes offline)
```

Shadow-v2-state migration model:

- `jetmon_events` and `jetmon_event_transitions` are the authoritative incident
  state for Jetmon v2.
- `jetpack_monitor_sites` remains the legacy site/config table during migration.
- While `LEGACY_STATUS_PROJECTION_ENABLE` is true, every v2 incident mutation
  also projects the v1-compatible `site_status` / `last_status_change` fields
  back to `jetpack_monitor_sites` in the same transaction.
- Once legacy readers have moved to the v2 API/event tables, disable
  `LEGACY_STATUS_PROJECTION_ENABLE`; v2 incident state continues to be written
  to the event tables.


Package Map
-----------

```
jetmon/
├── cmd/jetmon2/          Entry point, CLI subcommands, signal handling
├── cmd/jetmon-deliverer/ Standalone outbound delivery worker
├── internal/
│   ├── orchestrator/     Round loop, bucket coordination, retry queue,
│   │                     failure escalation, status notifications
│   ├── checker/
│   │   ├── checker.go    HTTP check logic (httptrace, SSL, keyword, redirect)
│   │   └── pool.go       Auto-scaling goroutine pool
│   ├── db/               MySQL queries and schema migrations
│   ├── config/           Config loading, validation, hot reload
│   ├── veriflier/        Veriflier client (JSON-over-HTTP) and server
│   ├── wpcom/            WPCOM notification client with circuit breaker
│   ├── audit/            Structured audit log (read + write)
│   ├── eventstore/       Authoritative incident event + transition writer
│   ├── api/              Internal REST API, auth, rate limits, idempotency
│   ├── deliverer/        Shared webhook + alert-contact worker wiring
│   ├── webhooks/         Webhook registry + HMAC-signed delivery worker
│   ├── alerting/         Managed alert-contact registry + delivery worker
│   ├── metrics/          StatsD UDP client, stats file writer
│   └── dashboard/        HTTP + SSE operator dashboard
└── veriflier2/cmd/       Standalone veriflier binary
```


Site Status Call Flow
----------------------

This is the end-to-end path from database query to WPCOM notification.

```
┌──────────────────────────────────────────────────────────────────────┐
│ PHASE 1 — Fetch                                                      │
│                                                                      │
│  orchestrator.runRound()                                             │
│    dbHeartbeat()          ── UPDATE jetmon_hosts SET last_heartbeat  │
│    ClaimBuckets()         ── rebalance bucket ranges (each round)    │
│    dbGetSitesForBucket()  ── SELECT due sites in DATASET_SIZE pages  │
│                              ORDER BY sidecar next/last checked time  │
└──────────────────────────────────────────────────────────────────────┘
                  │  []db.Site
                  ▼
┌──────────────────────────────────────────────────────────────────────┐
│ PHASE 2 — Check (parallel)                                           │
│                                                                      │
│  for each site:                                                      │
│    pool.Submit(checker.Request)                                      │
│         │                                                            │
│         ▼   (goroutine worker)                                       │
│    checker.Check(ctx, req)                                           │
│      • HTTP HEAD or GET with httptrace timing (DNS/TCP/TLS/TTFB)     │
│      • Keyword match in full GET profile (reads up to 1 MB of body)  │
│      • Redirect policy in full profile (follow / alert / fail)       │
│      • SSL expiry extraction from peer certificate                   │
│      • Error classification → ErrorCode (8 codes)                    │
│      • Success = HTTPCode in [1, 399]                                │
│         │                                                            │
│         ▼                                                            │
│    checker.Result  ──►  pool.results channel                         │
└──────────────────────────────────────────────────────────────────────┘
                  │  map[blogID]Result
                  ▼
┌──────────────────────────────────────────────────────────────────────┐
│ PHASE 3 — Collect (deadline: NetCommsTimeout + 5 s)                  │
│                                                                      │
│  Drain pool.Results() until all dispatched results arrive or         │
│  deadline fires (partial results processed, remaining work stays      │
│  visible through outstanding/due-remaining scheduler metrics)         │
└──────────────────────────────────────────────────────────────────────┘
                  │
          ┌───────┴───────┐
          │               │
    !IsFailure()      IsFailure()
          │               │
          ▼               ▼
┌─────────────┐   ┌─────────────────────────────────────────────────┐
│  RECOVERY   │   │ PHASE 4 — Failure Escalation                    │
│             │   │                                                 │
│ retries     │   │ Stage 1 — Local retry                           │
│  .clear()   │   │   retries.record(res) → failCount++             │
│             │   │   if failCount < NumOfChecks (default 3):       │
│ if site was │   │     auditLog("retry_dispatched")                │
│ previously  │   │     ← return; retry next round                  │
│ down:       │   │                                                 │
│  dbUpdate   │   │ Stage 2 — Veriflier escalation                  │
│  Status()   │   │   if failCount >= NumOfChecks:                  │
│  Notify()   │   │     escalateToVerifliers()                      │
│             │   │       ← see Veriflier Quorum section            │
└─────────────┘   │                                                 │
                  │ Stage 3 — Confirm down                          │
                  │   confirmDown(site, entry, vResults)            │
                  │     if LEGACY_STATUS_PROJECTION_ENABLE:         │
                  │       project site_status(→ confirmed_down)     │
                  │     if inMaintenance(): suppress + audit        │
                  │     else if !isAlertSuppressed(): Notify()      │
                  │     retries.clear(blogID)                       │
                  └─────────────────────────────────────────────────┘
```


Failure Escalation Detail
--------------------------

```
  Local check fails (N times)
          │
          │  failCount < NumOfChecks?
          ├──────────────────────────► queue in retryQueue, retry next round
          │
          │  failCount >= NumOfChecks
          ▼
  escalateToVerifliers()
          │
          │  No verifliers configured?
          ├──────────────────────────► confirmDown() immediately
          │
          │  Verifliers available
          ▼
  Dispatch in parallel to all verifliers
          │
          ├── veriflier-1:  same policy  ──►  {success: false, http: 500}
          ├── veriflier-2:  same policy  ──►  {success: false, http: 500}
          └── veriflier-N:  same policy  ──►  {success: true,  http: 200}
                                                    ↑ false positive

  healthyUniqueVantages = count of unique healthy verifier vote identities
  minHealthyFloor = 2 for multi-verifier fleets unless PeerOfflineLimit=1
  Quorum = max(min(healthyUniqueVantages, PeerOfflineLimit), minHealthyFloor)
  confirmations = count of unique verifier vantages reporting !success

  confirmations >= quorum?
      YES  ──►  confirmDown() → WPCOM notified
      NO   ──►  recordFalsePositive() + retries.clear()
```


Orchestrator Round Loop
------------------------

```
orchestrator.Run()
    │
    └── loop (until ctx.Done()):
          │
          ├─ config.Get()                    // fresh config snapshot each round
          ├─ pool.SetMaxSize(cfg.NumWorkers)  // apply hot-reloaded worker limit
          ├─ refreshVeriflierClients(cfg)     // rebuild list only on change
          │
          ├─ runRound()
          │     │
          │     ├─ dbHeartbeat()
          │     ├─ ClaimBuckets()             // rebalance every round
          │     ├─ dbGetSitesForBucket()      // fetch due work in DATASET_SIZE pages
          │     │
          │     ├─ for each scheduler page:
          │     │     pool.Submit(checker.Request)  // waits/collects on backpressure
          │     │
          │     ├─ collect results (deadline-bounded)
          │     │
          │     ├─ processResults()
          │     │     ├─ dbMarkSitesChecked()       // jetmon_site_runtime freshness
          │     │     ├─ dbRecordCheckHistories()   // method + RTT + DNS/TCP/TLS/TTFB
          │     │     ├─ dbUpdateSSLExpiries() + checkSSLAlerts()
          │     │     └─ handleRecovery(), handleFailure(),
          │     │        or maintenance-swallow the failure
          │     │
          │     ├─ emit StatsD metrics
          │     └─ applyMemoryPressure()       // drain workers if Go runtime memory > limit
          │
          └─ sleep to enforce fixed cadence or short variable-interval poll
```


Checker Pool — Auto-Scaling
----------------------------

The pool maintains a live set of worker goroutines bounded by `[minSize, maxSize]`.

```
  NewPool(initial=30, min=1, max=60)
    │
    ├─ work channel  (cap = max×2 = 120)
    ├─ results channel (cap = max×2 = 120)
    ├─ retire channel  (cap = max  =  60)
    └─ autoScale() goroutine (every 5 s)

  autoScale() logic:
    ┌─────────────────────────────────────────────────────┐
    │  current = WorkerCount()                            │
    │  queue   = QueueDepth()                             │
    │                                                     │
    │  Scale UP:   queue > current && current < maxSize   │
    │    spawn min(queue-current, maxSize-current) workers│
    │                                                     │
    │  Scale DOWN: current > maxSize                      │
    │    retire (current - maxSize) workers immediately   │
    │                                                     │
    │  Scale DOWN: queue == 0 && current > minSize        │
    │    retire 1 worker (gradual idle drain)             │
    └─────────────────────────────────────────────────────┘

  Worker lifecycle:
    spawnWorker() → goroutine:
      loop:
        select:
          <-ctx.Done()  → exit (pool shutdown)
          <-retire      → exit (graceful scale-down)
          req := <-work → execute checker.Check(), push to results

  Graceful shutdown:
    Drain() → CompareAndSwap(closed, false→true)
            → close(work)
            → wg.Wait()  ← blocks until last check completes
            → cancel ctx
```


WPCOM Circuit Breaker
----------------------

```
         ┌─────────────────────────────────────────────┐
         │              Closed (normal)                │
         │  • notifications sent immediately           │
         │  • failure counter tracks HTTP errors       │
         └──────────────────┬──────────────────────────┘
                            │ failures >= 5
                            ▼
         ┌─────────────────────────────────────────────┐
         │               Open (tripped)                │
         │  • new notifications queued (max 1000)      │
         │  • oldest dropped when queue full           │
         │  • circuitOpenAt recorded                   │
         └──────────────────┬──────────────────────────┘
                            │ time.Since(circuitOpenAt) > 60 s
                            ▼
         ┌─────────────────────────────────────────────┐
         │            Resetting (half-open)            │
         │  • failures reset to 0                      │
         │  • circuitOpen = false                      │
         │  • queued notifications flushed             │
         │  • next failure reopens circuit             │
         └─────────────────────────────────────────────┘

  Notify() call path:
    if circuit open AND timeout not elapsed → enqueue, return error
    if circuit open AND timeout elapsed     → reset + flush queue + send
    if circuit closed                       → send()
      send() error → failures++
                     failures >= 5 → open circuit
      send() ok    → failures = 0
```


Veriflier Transport
--------------------

```
  Monitor (orchestrator)              Veriflier (remote)
  ──────────────────────              ──────────────────
  veriflier.VeriflierClient           veriflier.Server

  Preferred v2 contract:

  CheckBatch(ctx, []CheckRequest)
    POST /v2/check
    Authorization: Bearer <token>
    Content-Type: application/json
    Body: {
      "batch_id": "...",
      "deadline_ms": 12000,
      "requests": [{
        "request_id": "...",
        "blog_id": 123,
        "url": "https://example.com",
        "timeout_ms": 10000,
        "method": "GET",
        "detection_profile": "full",
        "headers": {},
        "body_rules": {"required": ["needle"], "forbidden": ["bad"]},
        "redirect_policy": "follow"
      }]
    }
                        ─────────────────────────────►
                                                        bounded admission queue
                                                        concurrent HEAD/GET probes
                                                        typed outcomes
                        ◄─────────────────────────────
    Body: {
      "batch_id": "...",
      "vantage": {"id": "us-east-1", "region": "us-east", "provider": "..."},
      "agent": {"id": "host-a", "host": "host-a", "version": "..."},
      "results": [{
        "request_id": "...",
        "blog_id": 123,
        "url": "https://example.com",
        "vantage_id": "us-east-1",
        "agent_id": "host-a",
        "outcome": "down",
        "success": false,
        "http_code": 500,
        "error_code": 0,
        "rtt_ms": 214,
        "timings_ms": {"dns": 4, "tcp": 18, "tls": 36, "ttfb": 190}
      }]
    }

  Status(ctx)
    GET /v2/status
    ◄── {
          "status": "OK",
          "version": "1.2.3",
          "protocols": ["v2-json-http"],
          "vantage": {...},
          "agent": {...},
          "capacity": {"max_concurrency": 512, "queue_depth": 0, ...}
        }

  Optional legacy-compatible HTTP contract
  (only when VERIFLIER_ENABLE_LEGACY_HTTP=true):

    POST /check
    Authorization: Bearer <token>
    Content-Type: application/json
    Body: {"sites": [{blog_id, url, timeout, ...}, ...]}
                        ─────────────────────────────►
                                                        for each site:
                                                          checkFn(req)
                                                          res.Host = hostname
                        ◄─────────────────────────────
    Body: {"results": [{blog_id, host, success, http_code, ...}, ...]}

  Ping(ctx)
    GET /status
    ◄── {"status":"OK","version":"1.2.3"}
```

Veriflier discovery is staged through `VERIFLIER_DISCOVERY_MODE`:
`static` uses the configured `VERIFIERS` list, `shadow` reads the DB registry
and reports drift without changing traffic, and `active` uses enabled usable
rows from `jetmon_veriflier_vantages` with fallback to static config if the
registry is unavailable or empty. Monitors poll Veriflier `/v2/status` and write
`jetmon_veriflier_agents` capacity/liveness telemetry; Veriflier hosts do not
need DB access. Agent telemetry never creates trusted quorum votes by itself;
operators must pre-approve each enabled vantage.

The transport is JSON-over-HTTP for v2 production. `proto/veriflier.proto`
remains as a schema reference for a possible future transport, but generated
gRPC stubs are not required to build or deploy v2.

The monitor prefers `/v2/check` and falls back to `/check` only for
`veriflier2`'s legacy-compatible HTTP endpoint when v2 is unavailable. The
preferred rollout deploys a fresh v2 Veriflier fleet first and points v2
Monitors only at that fleet; original v1 Verifliers use the old TLS/custom
transport and are not v2 Monitor fallback targets.

This transport fallback is distinct from site probe policy. `HEAD` + `legacy`
checks are v1-compatible probe semantics carried in the versioned `/v2/check`
payload, not a reason to enable the legacy-compatible `/check` transport.

`vantage.id` is the quorum identity. Horizontal replicas behind the same
regional or provider endpoint must report the same `vantage.id`; `agent.id`
identifies only the process that handled the request and is diagnostic metadata,
not an extra vote. This keeps vertical and horizontal Veriflier scaling from
changing the meaning of `PEER_OFFLINE_LIMIT`.

Duplicate `vantage.id` values across monitor-configured Veriflier entries are
treated as one vote. The duplicate replies are still written to audit metadata
for debugging, but they do not increase quorum. In multi-Veriflier fleets the
effective quorum has a two-healthy-vantage floor unless operators intentionally
configure `PEER_OFFLINE_LIMIT=1`; this prevents a degraded verifier set from
collapsing to one confirming vote.

The v2 server executes accepted batches through a bounded concurrent executor.
If the local queue is full, it rejects the whole batch with HTTP 503. The
monitor treats that endpoint as unhealthy/no-vote rather than interpreting
overload as customer-site downtime.

`body_rules.required` is an array for future extensibility, but the current
checker supports zero or one required keyword. `body_rules.forbidden` supports
multiple forbidden keywords.


Bucket Distribution — Multi-Host Scaling
-----------------------------------------

Each round, all active monitors re-negotiate bucket ownership via a locked
MySQL transaction. Expired hosts (heartbeat missed by `BucketHeartbeatGraceSec`)
are removed and their ranges redistributed.

```
  jetmon_hosts (3 active hosts, BucketTotal=1000, BucketTarget=500):

  Hosts sorted by host_id: [host-a, host-b, host-c]
  assignBucketRanges() water-fill:
    host-a → buckets   0– 499  (capped at BucketTarget=500)
    host-b → buckets 500– 749  (250 remaining, 2 hosts left)
    host-c → buckets 750– 999

  host-b goes offline (heartbeat expires):
    host-a → buckets   0– 499
    host-c → buckets 500– 999  ← automatically absorbs host-b's range
```

`SELECT ... FOR UPDATE` prevents two hosts from claiming overlapping ranges.


Signal Handling
----------------

```
  SIGHUP  ──►  config.Reload()
                  └─ re-reads JSON file under RWMutex
                  └─ next round: pool.SetMaxSize(), refreshVeriflierClients()
                  └─ zero downtime, current round unaffected

  SIGINT  ──►  orchestrator.Stop()   (also sent by: jetmon2 drain)
  SIGTERM      └─ cancel context
               └─ current round completes
               └─ dbMarkHostDraining()
               └─ pool.Drain()       ← waits for in-flight checks
               └─ dbReleaseHost()
               └─ exit 0
               └─ hard kill after 30 s if drain stalls
```


Database Tables
----------------

```
  jetpack_monitor_sites   V1-shaped legacy site table plus compatibility projection
    blog_id               WordPress site identifier
    bucket_no             Determines which monitor instance owns this site
    monitor_url           URL to check
    monitor_active        Whether the site is active
    check_interval        V1-owned per-site cadence
    site_status           Legacy v1 projection; derived from v2 events
    last_status_change    Legacy v1 projection; derived from v2 transitions

  jetmon_site_check_config V2-only per-site probe config
    request_method        HEAD / GET rollout policy override
    detection_profile     legacy / simple_http / full detection profile
    check_keyword         Optional body text to require
    forbidden_keyword     Optional body text that must not appear
    forbidden_keywords    JSON array of body text that must not appear
    maintenance_start/end Suppress alerts during scheduled maintenance
    custom_headers        JSON blob of extra HTTP headers
    timeout_seconds       Per-site timeout override
    redirect_policy       follow / alert / fail
    alert_cooldown_minutes Per-site override for notification cooldown

  jetmon_site_runtime     V2-only runtime/freshness projection
    last_checked_at       Last completed local check timestamp
    next_check_at         Materialized variable-interval due time
    ssl_expiry_date       Updated after HTTPS checks
    last_alert_sent_at    Tracks cooldown window

  jetmon_hosts            Active monitor instances and bucket leases
    host_id               System hostname (PRIMARY KEY)
    bucket_min/max        Owned bucket range
    last_heartbeat        Updated every round; expiry triggers rebalance
    status                active / draining

  jetmon_process_health   Durable process heartbeat snapshots for dashboards
    process_id            Stable key such as <host>:monitor or <host>:deliverer
    host_id/process_type  Fleet grouping dimensions
    state/updated_at      Lifecycle state and freshness marker
    health_status         Green/amber/red process health rollup
    go_sys_mem_mb         Go runtime system memory in MB
    rss_mem_mb            Operating-system resident set size in MB
    dependency_health     JSON dependency health summary

  jetmon_events           Authoritative v2 incident current state
    id                    Incident identifier
    blog_id               Site identifier
    check_type            Probe family (http, tls_expiry, ...)
    severity/state        Current incident projection
    started_at/ended_at   Incident window
    resolution_reason     Required close reason

  jetmon_event_transitions Append-only mutation history for jetmon_events
    event_id              Incident row being mutated
    severity/state before/after
    reason/source         Why and who caused the mutation
    changed_at            Transition time

  jetmon_audit_log        Operational trail for compliance/debugging
    event_type            check | wpcom_sent | wpcom_retry |
                          retry_dispatched | veriflier_sent |
                          veriflier_result | maintenance_active |
                          alert_suppressed | api_access | config_reload
    blog_id, source, http_code, error_code, rtt_ms

  jetmon_check_history    Per-check method and timing samples
    request_method, rtt_ms, dns_ms, tcp_ms, tls_ms, ttfb_ms

  jetmon_false_positives  Checks local failed but verifliers passed
    blog_id, http_code, error_code, rtt_ms

  jetmon_veriflier_vantages Trusted Veriflier quorum identities
    vantage_id, region/provider, endpoint_host/port, auth_token, enabled

  jetmon_veriflier_agents Concrete Veriflier process telemetry
    agent_id, vantage_id, version, protocols, capacity, last_seen

  jetmon_api_keys         Internal API Bearer-token registry
    key_hash, consumer_name, scope, rate_limit_per_minute

  jetmon_webhooks         Registered webhook receivers and filters
  jetmon_webhook_deliveries
                           Per-transition webhook delivery attempts
  jetmon_webhook_dispatch_progress
                           Webhook worker transition high-water marks

  jetmon_alert_contacts   Managed notification destinations
  jetmon_alert_deliveries Per-transition alert delivery attempts
  jetmon_alert_dispatch_progress
                           Alert worker transition high-water marks

  jetmon_schema_migrations  Idempotent migration tracking
```


Key Concurrency Patterns
-------------------------

```
  Component            Primitive          Usage
  ─────────────────    ───────────────    ────────────────────────────────
  config.current       sync.RWMutex       RLock on every Get(); Lock on Reload()
  orchestrator         stdctx.Context     Cancel propagates stop to all goroutines
  veriflierClients     sync.RWMutex       RLock for snapshot; Lock on rebuild
  retryQueue.entries   sync.Mutex         Lock on record/clear/get/size
  wpcom state          sync.Mutex         Never held during HTTP send()
  pool.size/active     sync/atomic.Int64  Hot-path counters, no lock needed
  pool.closed          sync/atomic.Bool   CAS for idempotent Drain()
  pool.workMu          sync.RWMutex       RLock on Submit; Lock on close(work)
  pool.wg              sync.WaitGroup     Drain() blocks until wg reaches 0
  pool.work            chan Request        cap = maxSize×2; scheduler waits when full
  pool.retire          chan struct{}       Signals individual workers to exit
  dashboard.sseClients sync.RWMutex       One channel per connected SSE client
```


Error Codes (checker.ErrorCode)
--------------------------------

```
  ErrorNone          0   Success, no error
  ErrorTimeout       1   Context deadline exceeded
  ErrorConnect       2   TCP connection refused or DNS failure
  ErrorSSL           3   TLS handshake error (invalid cert, mismatch)
  ErrorRedirect      4   Redirect when RedirectPolicy=fail
  ErrorKeyword       5   Required keyword missing or forbidden keyword present
  ErrorTLSExpired    6   Certificate has passed NotAfter date
  ErrorTLSDeprecated 7   TLS 1.0 or 1.1 detected (advisory only, not a failure)
  ErrorBodyRead      8   GET response body closed early or could not be read
```

`IsFailure()` returns true for all codes except `ErrorNone` and
`ErrorTLSDeprecated`. `StatusType()` maps codes to the string values
expected by the WPCOM API (e.g. "https", "intermittent", "redirect").
Body integrity reads are capped to a bounded prefix so Jetmon can catch
truncated successful GET responses without buffering unbounded response
bodies. Keyword checks retain their larger bounded body window.
Deprecated TLS opens a separate `tls_deprecated` warning event and does not
project the legacy site status down.
