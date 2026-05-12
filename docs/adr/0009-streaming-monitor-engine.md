# ADR 0009: Streaming Monitor Engine

## Status

Accepted for merge candidacy on `feature/streaming-monitor-engine` after
internal-only capacity validation through 2 million active sites.

## Context

The legacy-compatible v2 scheduler still behaves like a round/page system: query
due rows, dispatch a page, collect results, then write freshness and history for
every completed probe. Batched writes and indexed `next_check_at` made that
model viable for the current test sizes, but the shape does not scale cleanly to
the next target: hundreds of thousands to one million sites on five-minute
intervals.

At one million sites on five-minute intervals, the monitor must sustain roughly
3,333 checks per second all day, every day. A design that writes healthy
freshness and raw timing rows for every probe turns the database into the hot
path even when every customer site is healthy.

## Decision

Add a v2-native streaming scheduler behind `SCHEDULER_ENGINE=streaming`.

The streaming engine:

- loads active site config from `jetpack_monitor_sites`, which remains the
  migration-time source of truth for site configuration and legacy status;
- assigns each site a stable phase inside its configured check interval so work
  is naturally spread over time instead of lumped into round boundaries;
- keeps due scheduling in memory and reschedules each target as results return;
- auto-sizes the checker pool from required check rate and observed latency,
  using `NUM_WORKERS` as a floor rather than a throughput ceiling;
- avoids per-success writes to `last_checked_at` and `jetmon_check_history`;
- writes failure history, event transitions, recoveries, SSL/TLS event changes,
  verifier state changes, audit entries, and WPCOM notifications through the
  existing v2 incident path;
- batches coarse legacy freshness projection every
  `STREAMING_LEGACY_PROJECTION_INTERVAL_MIN` minutes so rollback to the legacy
  scheduler has bounded freshness loss.

Add `jetmon_check_targets` as the durable home for v2-native scheduling state.
The first prototype still reloads from `jetpack_monitor_sites` directly, but the
new table is intentionally additive so later iterations can move derived
scheduling state out of the legacy table without breaking rollback.

## Compatibility

`jetpack_monitor_sites` remains the source of truth during v1/v2 migration.
Event state remains authoritative in `jetmon_events` and
`jetmon_event_transitions`, with legacy `site_status` projection maintained by
the same eventstore paths already used by the legacy-compatible v2 scheduler.

The deliberate compatibility tradeoff is freshness precision:
`last_checked_at` and `next_check_at` are no longer updated after every healthy
probe in streaming mode. Operators accepted a 5-15 minute worst-case rollback
freshness loss window; the default projection interval is 15 minutes.

## Capacity Validation

The branch was capacity-tested with uptime-bench internal-only HTTP/DNS targets
so the monitor service, not external internet reachability, was the primary
bottleneck under test.

The 2026-05-12 runs validated the streaming engine through 2 million active
sites on five-minute check intervals. At 1.5 million and 2 million active sites,
Jetmon v2 reached 100% observed target coverage, reported no never-seen or
stale targets, and passed replay detection for down and recovery scenarios. The
2 million run sustained roughly 6,765 completed checks per second, kept p95
target age around 270 seconds, kept max target age below 285 seconds, and kept
process RSS around 6.3 GB peak on the test host.

The same test shape failed at 4 million active sites. The engine initially
reached the required throughput, then collapsed into a timeout/backlog failure:
pending work grew into the millions, queue depth hit its cap, HTTP timeout
counts spiked, and target-observer coverage stopped at roughly 88%. That run
defines the current single-host ceiling signal, not an accepted production
capacity. The follow-up work is latency/error-aware concurrency control,
worker-scaler hardening, and bracket tests around 2.5-3 million sites before
attempting another larger jump.

## Consequences

The streaming engine should dramatically reduce write pressure in healthy
steady state. Database writes become mostly eventful writes instead of a
constant function of fleet size.

The first version still performs periodic full active-site reloads. That is
simpler and safer for the prototype, but a later iteration should use
`jetmon_check_targets` plus change detection to avoid broad reload reads at very
large fleet sizes. Until that target-table sync exists, the scheduler
automatically stretches periodic full reload cadence for large fleets so broad
legacy-table scans do not compete with the check loop during normal steady
state.

The new engine needs uptime-bench coverage that validates freshness, incident
correctness, recovery correctness, verifier promotion, rollback projection
staleness, and long-running steady-state resource use. A high score on one
benchmark is not the goal; the engine should remain robust across broad failure
modes while scaling well.
