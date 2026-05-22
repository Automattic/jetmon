# Jetmon v3 Probe-Agent Architecture Options

## Status

Planning note. This is not an accepted architecture decision and should not
block the [v2 production migration](v1-to-v2-migration.md).

Recommended order:

```text
v1 production
  -> v2 compatibility rewrite
  -> v2 production hardening and measurement
  -> v3 probe-agent shadow mode
  -> v3 gradual production cutover
```

Do not choose a v3 architecture until v2 has production operating data.

## Why Revisit After v2?

V2 intentionally keeps Jetmon close to the existing model: Monitor servers own
bucketed primary checks, and Verifliers provide independent confirmation before
a site moves from `Seems Down` to `Down`.

That is the right migration target because it limits product and operational
change while the Go rewrite, eventstore, API, alerting, delivery workers, and
Verifliers stabilize.

After v2 is stable, the core question is whether Jetmon should keep separate
Monitor and Veriflier roles or evolve into a probe platform where regional
agents execute routine and confirmation checks while a central decision layer
owns incident state.

## Data Needed From v2

The v3 decision should be based on production evidence:

- time from first local failure to `Seems Down`
- time from `Seems Down` to confirmed `Down`
- false-alarm rate by failure class
- Veriflier agreement/disagreement rates
- Veriflier latency and timeout rates by region/provider
- incidents where local failure was not remotely confirmed
- incidents with mixed remote confirmation by region
- monitor-side failures that should be `Unknown`
- cost and capacity profile for primary checks versus confirmation checks
- operator pain points when explaining why an incident was or was not confirmed
- WPCOM notification parity against the legacy path

V2 already emits useful evidence through StatsD:

- `detection.*` timing metrics for local failure through lifecycle state
- class-specific `detection.*.<failure-class>.count` counters for confirmed,
  false-alarm, and probe-cleared outcomes
- `verifier.host.<host>.*` counters for RPC health and confirm/disagree votes
- `wpcom.notification.*` counters for attempts, deliveries, retries, errors,
  final failures, and status-specific splits

## Current v2 Baseline

V2 lifecycle:

```text
Up
  -> Seems Down     local probe failed, retry/confirmation in progress
  -> Down           enough independent Verifliers confirmed
  -> Resolved       local or confirmed recovery
```

V2 deployment shape:

- `jetmon2` Monitors claim buckets and perform primary checks.
- Failed local checks open or update eventstore incidents.
- After enough local failures, the orchestrator asks Verifliers to confirm.
- Veriflier agreement promotes the same event from `Seems Down` to `Down`.
- Veriflier disagreement closes the event as a false alarm.
- Legacy WPCOM notification behavior is preserved around confirmed `Down` and
  recovery transitions.

This remains the correct v2 production target.

## Internal State Question

The external lifecycle can remain `Seems Down -> Down -> Resolved`, but v3 may
need richer internal decision states:

| Internal state | Meaning |
| --- | --- |
| `Suspected` | First failure observed, not enough evidence yet |
| `Confirming` | Confirmation probes are in flight |
| `ConfirmedGlobalDown` | Enough independent regions agree the site is down |
| `RegionalFailure` | Some regions fail while others succeed |
| `Unknown` | Monitor/probe infrastructure cannot produce trustworthy evidence |
| `FalseAlarm` | The original failure was not confirmed |

Projection can stay compatible:

```text
Suspected / Confirming -> Seems Down
ConfirmedGlobalDown    -> Down
RegionalFailure        -> Degraded or Regional Failure
Unknown                -> Unknown, not downtime
FalseAlarm             -> Resolved with reason=false_alarm
```

## Architecture Options

### Option 1: v2 Plus Stronger Probe Metadata

Keep the Monitor/Veriflier structure, but store richer evidence for every vote:
probe identity, region, provider, timing, failure class, and decision inputs.

Pros:

- Lowest risk after v2.
- Improves support and operator explainability quickly.
- Produces better data for later architecture decisions.
- Minimal deployment change.

Cons:

- Keeps the Monitor/Veriflier split.
- Remote perspective is still mostly gathered after suspicion.
- Does not fully support regional baselines or synthetic-check expansion.

Choose when v2 works well and the main pain is missing evidence.

### Option 2: Peer Probe Mesh

Every Monitor can perform primary and confirmation probes. A bucket owner asks
peer Monitors in other regions/providers for confirmation.

Pros:

- Removes a separate Veriflier fleet.
- Uses Monitor capacity more evenly.
- Simpler than a durable scheduler/job bus.

Cons:

- Couples Monitor hosts more tightly.
- A Monitor incident can affect both primary and confirmation capacity.
- Anti-correlation rules depend on rigorous host metadata.
- Decisions still center on the bucket owner.

Choose when the Veriflier fleet is operationally awkward but a full scheduler is
too large a step.

### Option 3: Central Scheduler Plus Regional Probe Agents

This is the leading v3 candidate.

A scheduler/decision service owns check plans and durable jobs. Regional probe
agents claim jobs, execute checks, and write results. The decision layer
evaluates evidence and writes eventstore transitions.

Pros:

- Best long-term separation of concerns.
- Durable jobs replace in-memory confirmation state.
- Probe agents are simple and horizontally scalable.
- Primary and confirmation checks use the same execution path.
- Supports regional status, confidence scoring, per-vantage SLA, synthetic
  checks, richer diagnostics, and new probe types.

Cons:

- Largest implementation effort.
- Requires durable job claiming and result deduplication.
- Requires careful shadow-mode comparison before becoming authoritative.
- Adds operational components relative to the v2 single-binary shape.

Choose when v2 data shows confirmation latency, regional ambiguity, operator
explainability gaps, or demand for regional SLA/synthetic checks.

### Option 4: Always-On Multi-Region Quorum

Every site is checked from multiple regions continuously or near-continuously.
Incidents are classified from live quorum instead of second-stage confirmation.

Pros:

- Fastest confirmation.
- Best regional visibility.
- Strong latency and SLA data by vantage point.
- Removes most retry-then-confirm delay.

Cons:

- Much higher check volume and customer-site load.
- Higher cost.
- Needs careful aggregation to avoid noisy partial failures.
- Likely needs tiers or sampling.

Choose only if product requirements demand regional SLA visibility or very fast
confirmation and the cost profile is acceptable.

### Option 5: External Probes Plus Site/WPCOM Signals

Combine external probes with Jetpack/WPCOM/site-side signals such as Jetpack
heartbeat, wp-admin reachability, cron heartbeat, or WPCOM-side activity.

Pros:

- Better distinction between site downtime, regional network issues, and
  monitor-side failures.
- Better support diagnostics.
- Can reduce false positives.
- Complements any probe-agent architecture.

Cons:

- Depends on signal quality outside Jetmon.
- Heartbeats can be delayed for reasons other than downtime.
- Requires additional data contracts with Jetpack/WPCOM/site systems.
- Not a replacement for external probing.

Choose when v2 data shows false positives that probes alone cannot classify
confidently, or support needs better causal diagnostics.

## Recommendation

Do not change the v2 production target.

Recommended path:

1. Finish and deploy v2 with the current Monitor-plus-Veriflier shape.
2. Stabilize v2 in production.
3. Gather the evidence listed above.
4. Improve v2 metadata first if operators lack enough explanation detail.
5. Revisit these candidates with real data.
6. If evidence supports it, evolve toward Option 3.

Option 3 is the current best long-term architecture because it turns Jetmon into
a durable probe platform instead of a Monitor-plus-confirmers system. It offers
the best path to regional status, richer classification, synthetic checks, and
predictable scaling.

Option 1 is the likely first step regardless of the final v3 choice because
better probe metadata makes every other path easier to evaluate.

## Option 3 Migration Sketch

After v2 stabilizes:

1. Add probe metadata to v2 results: region, provider, probe identity, timing,
   failure class, and vote details.
2. Introduce durable confirmation jobs while keeping primary checks in v2.
3. Generalize Veriflier into probe-agent so confirmation is an execution mode,
   not a special-purpose service.
4. Run primary probe jobs in shadow mode for a small cohort.
5. Compare v2 and v3 decisions: detection latency, confirmation latency, false
   positives, missed incidents, regional disagreement, and WPCOM parity.
6. Cut over confirmation decisions after the job-based path matches or beats v2.
7. Cut over primary checks by bucket range or site cohort.
8. Retire the Monitor/Veriflier distinction once the decision layer owns
   scheduling and state and probe agents execute all supported check types.

## Non-Goals Until v2 Is Stable

- Do not skip directly from v1 to v3.
- Do not change customer-visible notification semantics during the v2 cutover.
- Do not replace eventstore as the source of truth.
- Do not require a new queueing system before MySQL-backed job claiming has
  been evaluated.
- Do not make regional classifications customer-visible until taxonomy and
  support language are ready.
