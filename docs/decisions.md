# Architecture Decisions

This file is the consolidated ADR record. These decisions are accepted unless a
new change explicitly reopens them.

## 0001 Event-Sourced State Model

Site state is event-sourced in `jetpack_monitor_events` and
`jetpack_monitor_event_transitions`. The current event row and append-only
transition log are updated in the same transaction. `internal/eventstore` is
the sole writer. The legacy site row stores a projection for compatibility, not
the source of truth.

Why: v1's mutable status bit could not explain how an incident evolved. The
transition log gives dashboards, support, webhooks, and SLA jobs a coherent
timeline.

Consequences:

- every mutation writes a transition;
- event close requires a resolution reason;
- severity and state remain separate;
- projection drift is detectable and rebuildable.

## 0002 Internal API Behind A Gateway

Jetmon exposes an internal API only. A separate gateway owns customer-facing
authentication, authorization, tenant isolation, public rate limits, public
error vocabulary, and plan gating.

Why: Jetmon's internal model is intentionally rich and operational. Direct
public exposure would require a separate threat model and compatibility promise.

Consequences:

- API keys identify internal systems, not end users;
- tenant headers are accepted only from the `gateway` consumer;
- the internal API can include operator detail that a public API would redact.

## 0003 Plaintext Outbound Credentials

Webhook secrets and alert-contact destinations are stored in recoverable form
because outbound delivery needs the raw value on every send. Hashing would make
dispatch impossible.

Why: the useful mitigation is database/storage encryption and access control,
not one-way hashing.

Consequences:

- API reads return previews, not raw credentials;
- creation/rotation returns secrets once;
- application-level encryption with a master key remains future hardening.

## 0004 Stripe-Style Webhook Signatures

Webhook deliveries use:

```text
X-Jetmon-Signature: t=<unix>,v1=<hmac_sha256(t.body)>
```

Consumers verify header shape, timestamp age/skew, and HMAC with constant-time
comparison.

Why: the format is simple, language-portable, replay-resistant, and leaves room
for algorithm rotation.

Consequences:

- consumers should reject deliveries older than 5 minutes or more than 1 minute
  in the future;
- immediate secret rotation is implemented;
- grace-period dual signing is deferred.

## 0005 Pull-Only Delivery Via Event Transitions

Webhook and alert-contact workers poll `jetpack_monitor_event_transitions` with
a high-water mark, then create delivery rows.

Why: event confirmation already has seconds of domain latency, so a 1s poll is
operationally invisible and keeps the database as the rollout-safe bus.

Consequences:

- workers can run embedded or standalone;
- delivery payloads are frozen at enqueue time;
- transactional row claims handle multiple workers safely.

## 0006 Separate Alerting And Webhooks Packages

`internal/webhooks` and `internal/alerting` remain separate packages even though
their worker shape is similar.

Why: webhooks deliver raw signed event streams for systems; alert contacts
render managed human notifications through owned transports. Their product
behavior and credential shapes differ enough that a shared abstraction would
hide important differences.

Consequences:

- duplicated worker concepts are acceptable;
- shared helper extraction is allowed only when it stays obvious;
- the future deliverer binary can host both packages.

## 0007 Transactional Row Claim Over Soft Locks

Delivery workers claim rows transactionally instead of relying on soft locks or
best-effort process-local coordination.

Why: soft locks fail under crashes, clock skew, and multiple active workers.
Database row claims are the smallest reliable coordination mechanism available
in the current architecture.

Consequences:

- multiple workers may be active without double-claiming pending rows;
- `DELIVERY_OWNER_HOST` is a rollout guard, not a correctness requirement;
- delivery tables need claim-friendly indexes.

## 0008 Shadow V2 State Migration

During rollout, v2 state is authoritative in v2 tables while
`jetpack_monitor_sites` keeps the v1-compatible projection fields current.

Why: v1 readers and rollback paths need the legacy projection while v2 proves
out the event model.

Consequences:

- `LEGACY_STATUS_PROJECTION_ENABLE` stays on until legacy readers move;
- projection updates happen in the same transaction as event mutations;
- projection drift checks are a rollout gate.

## 0009 Streaming Monitor Engine

The monitor keeps an in-memory active target set, reloads it periodically, and
schedules checks continuously instead of doing round-sized stop/start batches.

Why: streaming scheduling improves freshness and reduces round-edge artifacts at
large site counts.

Consequences:

- target reload cadence and runtime write cadence become tuning levers;
- check-history mode matters for DB write pressure;
- sustained memory pressure should be investigated with pprof instead of hiding
  it behind a scheduler drain cap.

## 0010 Trusted Veriflier Discovery

Monitor-collected telemetry can define usable Veriflier vantages and health
rather than relying only on static config.

Why: production needs to know which vantages are actually healthy before using
their votes for downtime confirmation.

Consequences:

- quorum reports distinguish enabled, usable, healthy, and active-agent counts;
- a configured quorum floor prevents one healthy Veriflier from confirming
  downtime alone;
- static endpoints remain a fallback.

## 0011 Table Naming

V2 tables use the `jetpack_monitor_` prefix.

Why: production DBs already group Jetmon data under that prefix, and a mixed
prefix scheme would make permissions, audits, and operator queries harder.

Consequences:

- new tables follow `jetpack_monitor_*`;
- docs and migrations should not introduce a second v2-specific prefix.

## Reopening A Decision

To reopen a decision, update this file with:

- what changed;
- the old decision being superseded;
- the new decision;
- rollout and migration consequences.

Do not add a new standalone ADR unless the decision truly needs a long-form
record that would make this file unwieldy.
