# Architecture Decisions

Accepted decisions. Reopen one by editing this file with what changed, the old
decision, the new decision, and migration consequences.

| ID | Decision | Why It Matters |
| --- | --- | --- |
| 0001 | Site state is event-sourced in `jetpack_monitor_events` and `jetpack_monitor_event_transitions`; `internal/eventstore` is the sole writer. | Gives dashboards, support, webhooks, and SLA jobs a reliable incident timeline. |
| 0002 | Jetmon exposes an internal API behind a gateway, not a direct customer API. | Customer auth, tenant isolation, public errors, and plan gates belong outside Jetmon. |
| 0003 | Outbound credentials are stored in recoverable form. | Webhook and alert delivery need raw secrets; hashing would make dispatch impossible. |
| 0004 | Webhooks use Stripe-style `t=<unix>,v1=<hmac>` signatures. | Simple replay-resistant signing with room for algorithm rotation. |
| 0005 | Delivery workers poll event transitions and enqueue delivery rows. | Keeps MySQL as the rollout-safe bus without adding pub/sub before it is needed. |
| 0006 | Webhooks and alert contacts stay in separate packages. | Raw system payloads and managed human notifications have different behavior and credentials. |
| 0007 | Delivery uses transactional row claims, not soft locks. | Multiple workers can run without duplicate claiming; crashes do not strand process-local locks. |
| 0008 | Rollout uses shadow v2 state plus legacy projection. | V2 can be authoritative while v1 readers and rollback paths keep working. |
| 0009 | Scheduler is streaming rather than round-sized stop/start batches. | Improves freshness and reduces round-edge artifacts at scale. |
| 0010 | Veriflier health comes from trusted discovery/telemetry where available. | Quorum decisions should use healthy usable vantages, with a configured floor. |
| 0011 | V2 tables use the `jetpack_monitor_` prefix. | Matches production DB grouping, permissions, audits, and operator queries. |

Consequences to preserve:

- Every event mutation writes one transition in the same transaction.
- Event close requires a resolution reason.
- API keys identify internal systems, not users.
- Webhook timestamp and HMAC checks are both required.
- Delivery payloads are frozen when queued.
- `DELIVERY_OWNER_HOST` is a rollout guard, not a correctness requirement.
- `LEGACY_STATUS_PROJECTION_ENABLE` stays on until legacy readers move.
- A single healthy Veriflier must not be able to confirm downtime alone.
