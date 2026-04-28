# 0006 — Separate `internal/alerting` and `internal/webhooks` packages

**Status:** Accepted (2026-04-25)

## Context

Phase 3 shipped `internal/webhooks` — a webhook registry, delivery
worker, and HMAC signing flow. Phase 3.x then needed to ship alert
contacts: managed channels (email, PagerDuty, Slack, Teams) for
human destinations, with site-filter + severity-gate filtering and a
per-hour rate cap.

The two are noticeably similar at the operational level. Both:

- Poll `jetmon_event_transitions` on a high-water mark (per ADR-0005).
- Match new transitions against an active registry.
- Enqueue per-(subscriber, transition) deliveries with INSERT IGNORE
  on a UNIQUE KEY.
- Have a deliver loop with a per-subscriber in-flight cap and a
  shared retry ladder (1m / 5m / 30m / 1h / 6h).
- Surface delivery list / manual-retry endpoints through the API.

The natural temptation was to extend the webhook worker to handle
both — define a `Dispatcher` interface, two concrete implementations
(HMAC-POST for webhooks, transport-rendered for alert contacts), and
share the loop / retry / claim plumbing.

## Decision

We will keep `internal/alerting` and `internal/webhooks` as
**separate packages with parallel-but-duplicated structure**, at
least until the deliverer-binary extraction (`ROADMAP.md`).

The webhook worker keeps its existing shape; the alerting worker is
copy-paste-and-adapt with the alerting-specific concerns layered on
(severity gate, rate cap, transport map, Notification rendering).

This is a deliberate choice to defer abstraction. Webhooks shipped
first; alerting hadn't been built. We didn't yet know what shape
alerting would actually take — fan-out, escalation, digest mode,
on-call routing are all real possibilities for future alert-contact
features that webhooks doesn't have. Building a shared abstraction
against one known concrete user (webhooks) and one guessed-at user
(alerting) was likely to produce an abstraction that fits neither
well.

## Consequences

**Wins:**
- Each package can evolve independently. Webhooks growing a v2
  signature scheme doesn't risk regressing alerting; alerting
  growing per-contact escalation doesn't risk regressing the webhook
  flow.
- Webhooks went to production first (verified end-to-end before
  alerting was started). Coupling them to greenfield code would
  have added production risk to a working feature.
- Reading either package is easy: it's all the relevant code in one
  spot, no "is this branch reached for webhooks too?" cognitive
  load.

**Costs:**
- ~300 lines of duplicated code: retry schedule constants, in-flight
  cap, claim-and-soft-lock pattern (ADR-0007), polling loop shape,
  abandon semantics. Bug fixes have to land twice (the soft-lock
  claim fix did exactly that).
- Two metrics namespaces (`webhook_*` vs `alert_*`). Operators have
  to remember which is which.
- Drift risk — improvements in one package don't automatically reach
  the other.

These costs are bounded and acceptable in exchange for the
flexibility, but they accrue every time we touch the workers. The
soft-lock fix is the canary: if every fix is two-pass, the unification
is overdue.

## Future revisit

The deliverer-binary extraction is the natural moment to revisit
this. By then we'll have:

- Two concrete dispatch workers in production with known operational
  profiles.
- A clear picture of what alerting actually grew into vs. what
  webhooks actually needed.
- WPCOM legacy notifications queued to migrate behind the same
  abstraction, providing a third concrete user.

At that point, factor a `Dispatcher` interface against three known
implementations, not one known plus one guess. The unification work
is documented in `ROADMAP.md` "Multi-repo / multi-binary split →
Revisit point: unify `internal/alerting/` and `internal/webhooks/`."

## Related

- ROADMAP.md "Multi-repo / multi-binary split"
- `internal/webhooks/worker.go` and `internal/alerting/worker.go` —
  the parallel implementations.
- ADR-0005 (Pull-only delivery) — the shared shape both workers
  follow.
- ADR-0007 (Soft-lock claim) — a fix that had to land in both
  packages, illustrating the duplication cost.
