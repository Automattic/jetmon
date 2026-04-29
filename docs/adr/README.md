# Architecture Decision Records

Short, immutable records of load-bearing decisions in Jetmon 2 — the kind
of "why is it like this" question that has been answered more than once
in code review, on Slack, or in a PR description.

## Format

Each ADR is a numbered Markdown file: `NNNN-short-slug.md`. Numbers are
allocated sequentially and never reused. The body has four sections:

- **Status** — Proposed / Accepted / Superseded by ADR-NNNN / Deprecated.
- **Context** — what problem we're solving and the constraints that
  shaped the choice. Capture the world as it was when the decision was
  made.
- **Decision** — what we chose, in active voice ("We will…").
- **Consequences** — what falls out of the decision, both the wins and
  the costs we accept. Future readers should be able to evaluate
  whether the consequences are still acceptable.

Optional fifth section: **Alternatives considered** when the rejected
options carry useful information for a future revisit.

## Conventions

- **ADRs are append-only.** Once accepted, the body is not edited.
  Status changes (e.g. "Superseded by ADR-NNNN") are added at the top
  with a date.
- **Each ADR captures one decision.** If a topic produces several
  decisions, write several ADRs that cross-reference.
- **Write what was true at the time.** If a column has been renamed
  since, the ADR keeps the old name with a footnote rather than being
  silently updated. Otherwise the historical thread is lost.
- **Cross-link generously.** ADRs frequently depend on each other;
  always link to the related decisions.
- **Don't backfill speculatively.** ADRs document decisions that have
  actually been made and shipped. Open questions belong in
  [`../roadmap.md`](../roadmap.md) until they're resolved.

## Index

| # | Title | Status |
|---|-------|--------|
| [0001](0001-event-sourced-state-model.md) | Event-sourced state model with dedicated transitions table | Accepted |
| [0002](0002-internal-only-api-behind-gateway.md) | Internal-only API behind a gateway | Accepted |
| [0003](0003-plaintext-credentials-for-outbound-dispatch.md) | Plaintext credential storage for outbound dispatch | Accepted |
| [0004](0004-stripe-style-hmac-webhook-signatures.md) | Stripe-style HMAC-SHA256 webhook signatures | Accepted |
| [0005](0005-pull-only-delivery-via-event-transitions.md) | Pull-only webhook and alerting delivery | Accepted |
| [0006](0006-separate-alerting-and-webhooks-packages.md) | Separate `internal/alerting` and `internal/webhooks` packages | Accepted |
| [0007](0007-soft-lock-vs-row-claim.md) | Soft-lock claim vs transactional row claim | Accepted |
| [0008](0008-shadow-v2-state-migration.md) | Shadow-v2-state migration with legacy status projection | Accepted |
