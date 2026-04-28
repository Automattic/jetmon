# Jetmon v3 Probe-Agent Architecture Options

## Status

Planning note. This is not an accepted architecture decision and should not
block the v2 production migration.

The intended migration order is:

```text
v1 production
  -> v2 compatibility rewrite
  -> v2 production hardening and measurement
  -> v3 probe-agent architecture in shadow mode
  -> v3 gradual production cutover
```

The v3 architecture should be revisited only after v2 has been deployed to
production and has enough operating data to make the tradeoffs concrete.

## Why Revisit This After v2?

The currently implemented v2 shape keeps Jetmon close to the existing mental
model: main monitor servers own bucketed primary checks, and Verifliers provide
independent confirmation before a site moves from `Seems Down` to `Down`.

That is the right near-term migration target because it limits product and
operational change while the Go rewrite, eventstore, API, alerting, and
delivery workers stabilize.

After v2 is stable, the main question is whether Jetmon should keep the
separate "main monitor" and "Veriflier" roles or evolve into a more general
probe platform where regional agents execute both routine checks and
confirmation jobs while a central decision layer owns incident state.

## Data To Gather During v2

The v3 decision should be based on production data from v2, especially:

- Time from first local failure to `Seems Down`.
- Time from `Seems Down` to confirmed `Down`.
- False alarm rate by failure class.
- Veriflier agreement and disagreement rates.
- Veriflier latency and timeout rates by region/provider.
- Number of incidents where local failure was not confirmed remotely.
- Number of incidents where remote confirmation was mixed by region.
- Number of monitor-side failures that should be modeled as `Unknown`.
- Cost and capacity profile for primary checks versus confirmation checks.
- Operator pain points around explaining why an incident was or was not
  confirmed.
- Customer-impacting notification parity against the legacy WPCOM path.

Without this data, v3 risks optimizing for hypothetical problems instead of
the production failure modes that actually matter.

## Current v2 Baseline

The v2 flow is:

```text
Up
  -> Seems Down     local probe failed, retry/confirmation in progress
  -> Down           enough independent Verifliers confirmed
  -> Resolved       local or confirmed recovery
```

The v2 deployment shape is:

- Main `jetmon2` servers claim site buckets and perform primary checks.
- Failed local checks open or update eventstore incidents.
- After enough local failures, the orchestrator asks Verifliers to confirm.
- Veriflier agreement promotes the same event from `Seems Down` to `Down`.
- Veriflier disagreement closes the event as a false alarm.
- Legacy WPCOM notification behavior remains preserved around the confirmed
  `Down` and recovery transitions.

This is intentionally conservative and remains the correct v2 production
target.

## Question 1: Is There A Better Flow Than Seems Down To Confirmed Down?

Externally, the `Seems Down -> Down -> Resolved` lifecycle is still a good
operator and customer-facing model. It is simple, useful, and maps well to the
current false-positive reduction goal.

Internally, v3 may need a richer decision model:

| Internal state | Meaning |
|---|---|
| `Suspected` | First failure observed, not enough evidence yet |
| `Confirming` | Confirmation probes are in flight |
| `ConfirmedGlobalDown` | Enough independent regions agree the site is down |
| `RegionalFailure` | Some regions fail while others succeed |
| `Unknown` | Monitor/probe infrastructure cannot produce trustworthy evidence |
| `FalseAlarm` | The original failure was not confirmed |

Those internal states do not need to leak directly to every consumer. They can
still project to the v2 public states where compatibility matters:

```text
Suspected / Confirming -> Seems Down
ConfirmedGlobalDown    -> Down
RegionalFailure        -> Degraded or Regional Failure, depending on taxonomy
Unknown                -> Unknown, not downtime
FalseAlarm             -> Resolved with reason=false_alarm
```

## Question 2: Should Main Servers And Verifliers Remain Separate?

For v2, yes. It keeps the migration safe.

For v3, probably not as a permanent distinction. A better long-term shape is
likely:

- **Decision layer:** owns scheduling, quorum rules, eventstore writes, and
  notification decisions.
- **Probe agents:** execute check jobs from one or more regions/providers.
- **Durable job bus:** stores check jobs, claims, results, retries, and agent
  heartbeats.

In that model, "primary check" and "confirmation check" are job types, not
separate binary roles.

## Question 3: What Does The Current Shape Leave On The Table?

Compared with a probe-agent architecture, the current v2 shape gives up or
delays:

- Continuous regional baseline data.
- First-class regional or partial-outage classification.
- Durable confirmation jobs independent of orchestrator memory.
- Cleaner backpressure and retry accounting for probe work.
- Easier addition of new probe types, such as synthetic flows or TCP checks.
- Per-vantage-point latency and SLA reporting.
- Better explanations for mixed outcomes.
- More flexible capacity planning, because every probe agent can execute any
  supported check job.

These are good v3 motivations, but they should not be bundled into the v2
production cutover.

## Candidate Architectures To Revisit

### Candidate 1: v2 Plus Stronger Probe Metadata

Keep the main-server-plus-Veriflier structure, but record richer evidence for
every vote: probe identity, region, provider, timing, failure class, and
decision inputs.

Flow:

```text
main check fails -> Seems Down
local retries fail -> Veriflier confirmation
event transition stores each vote and decision input
quorum -> Down, disagreement -> false_alarm
```

Pros:

- Lowest risk after v2.
- Improves support and operator explainability quickly.
- Produces better data for future v3 decisions.
- Minimal deployment changes.

Cons:

- Keeps the main/Veriflier split.
- Remote perspective is still mostly gathered after suspicion.
- Does not fully support regional baseline or synthetic-check expansion.

When to choose:

- v2 works well, but operators mainly need better evidence and dashboards.

### Candidate 2: Peer Probe Mesh

Every monitor host can perform both primary and confirmation probes. A host
that detects a failure asks peer monitor hosts in other regions/providers for
confirmation.

Flow:

```text
bucket owner detects failure
bucket owner requests peer probes
peer votes return directly to owner
owner writes event transition and notifications
```

Pros:

- Removes a separate Veriflier fleet.
- Uses monitor capacity more evenly.
- Simpler than introducing a full scheduler and job bus.
- Can become region-aware if monitor hosts are deployed across regions.

Cons:

- Monitor hosts become more coupled.
- A monitor-host incident can affect both primary and confirmation capacity.
- Harder to enforce anti-correlation rules unless host metadata is rigorous.
- Still centers decisions on the bucket owner.

When to choose:

- The Veriflier fleet is operationally awkward, but a full scheduler is too
  large a step.

### Candidate 3: Central Scheduler Plus Regional Probe Agents

This is the leading v3 candidate.

A scheduler/decision service owns check plans and durable jobs. Regional probe
agents claim jobs, execute checks, and write results. The decision layer
evaluates evidence and writes eventstore transitions.

Flow:

```text
scheduler creates routine probe jobs
regional probe agents claim and execute jobs
decision layer evaluates results
first failure opens Suspected/Seems Down
confirmation jobs are scheduled to independent agents
quorum/classifier promotes to Down, RegionalFailure, Unknown, or false_alarm
eventstore writes remain the source of truth
delivery workers notify from event transitions
```

Pros:

- Best long-term separation of concerns.
- Durable jobs replace in-memory confirmation state.
- Probe agents are simple and horizontally scalable.
- Primary and confirmation checks use the same execution path.
- Supports regional status, confidence scoring, per-vantage SLA, synthetic
  checks, and richer diagnostics.
- Lets Jetmon add new probe types without reshaping the decision layer.

Cons:

- Largest implementation effort.
- Requires durable job claiming and result deduplication.
- Requires careful shadow-mode comparison before becoming authoritative.
- More operational components than the v2 single-binary shape.

When to choose:

- v2 production data shows confirmation latency, regional ambiguity, or
  operator explainability are material problems.
- Jetmon needs regional SLAs, synthetic checks, or more probe types.
- The team is ready to invest in a platform-shaped monitoring architecture.

### Candidate 4: Always-On Multi-Region Quorum

Every monitored site is checked from multiple regions continuously or
near-continuously. Incidents are classified from live quorum rather than a
second-stage confirmation request.

Flow:

```text
regional agents check every site on schedule
decision layer continuously evaluates current regional evidence
multi-region failure -> Down
single-region failure -> RegionalFailure or Degraded
probe infrastructure failure -> Unknown
```

Pros:

- Fastest confirmation.
- Best regional visibility.
- Strong latency and SLA data by vantage point.
- Removes most of the "wait for retries, then confirm" gap.

Cons:

- Much higher check volume.
- More customer-site load.
- Higher cost.
- Needs careful aggregation to avoid noisy partial failures.
- Probably too expensive for every site unless tiers or sampling are added.

When to choose:

- Product requirements demand regional SLA visibility or very fast
  confirmation, and the cost profile is acceptable.

### Candidate 5: External Probes Plus Site/WPCOM Signals

Combine external probe evidence with internal or site-side signals such as
Jetpack heartbeat, wp-admin reachability, cron heartbeat, or WPCOM-side
activity.

Flow:

```text
external probe failure opens Suspected/Seems Down
decision layer checks corroborating Jetpack/WPCOM/site signals
external + internal evidence agree -> Down
external failure only -> Confirming, RegionalFailure, or Unknown
internal signal missing only -> agent/heartbeat problem, not customer downtime
```

Pros:

- Better distinction between site downtime, regional network issues, and
  monitor-side failures.
- Better support diagnostics.
- Can reduce false positives.
- Complements any probe-agent architecture.

Cons:

- Depends on signal quality from Jetpack/WPCOM/site-side systems.
- Heartbeats can be delayed for reasons other than downtime.
- More data contracts outside Jetmon.
- Not a replacement for external probing.

When to choose:

- v2 data shows many false positives that external probes alone cannot
  classify confidently, or support needs better causal diagnostics.

## Current Recommendation

Do not change the v2 production target.

The recommended path is:

1. Finish and deploy v2 with the current main-server-plus-Veriflier shape.
2. Stabilize v2 in production.
3. Gather the data listed above.
4. Revisit these candidates with real evidence.
5. If the evidence supports it, evolve toward Candidate 3.

Candidate 3 is the current best long-term option because it turns Jetmon into a
durable probe platform instead of a monitor-plus-confirmers system. It offers
the best path to regional status, richer classification, synthetic checks, and
more predictable scaling.

Candidate 1 is the likely first step regardless of final v3 choice because
better probe metadata makes every other option easier to evaluate.

## Candidate 3 Migration Sketch After v2 Stabilizes

The v2-to-v3 migration should be incremental:

1. **Add probe metadata to v2 results.**
   Record region, provider, probe identity, timing, failure class, and vote
   details for local and Veriflier checks.

2. **Introduce durable confirmation jobs.**
   Keep primary checks in v2, but replace direct Veriflier fanout with jobs in
   MySQL. Existing Verifliers or new probe agents claim jobs and write results.

3. **Generalize Veriflier into probe-agent.**
   Make confirmation an execution mode of a generic agent rather than a
   special-purpose service.

4. **Run primary probe jobs in shadow mode.**
   Schedule routine check jobs for a small cohort but do not let them affect
   customer-visible state.

5. **Compare v2 decisions to v3 decisions.**
   Measure detection latency, confirmation latency, false positives, missed
   incidents, regional disagreement, and WPCOM notification parity.

6. **Cut over confirmation decisions.**
   Let the job-based confirmation path become authoritative for
   `Seems Down -> Down` after it matches or beats v2 behavior in shadow mode.

7. **Cut over primary checks gradually.**
   Move bucket ranges or site cohorts from direct v2 primary checks to scheduled
   probe jobs.

8. **Retire the main/Veriflier distinction.**
   The central decision layer owns scheduling and state; probe agents execute
   jobs from any supported check type.

## Non-Goals Until After v2 Is Stable

- Do not skip directly from v1 to v3.
- Do not change customer-visible notification semantics during the v2 cutover.
- Do not replace eventstore as the source of truth.
- Do not require a new queueing system before MySQL-backed job claiming has
  been evaluated.
- Do not make regional classifications customer-visible until the taxonomy and
  support story are ready.
