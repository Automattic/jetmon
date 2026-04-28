# Jetmon Docs

This directory holds longer-form design material that does not belong in the
main README.

## Architecture Decisions

Accepted decisions live in [`adr/`](adr/). These records are append-only and
capture load-bearing choices that the current v2 implementation depends on.

Start with [`adr/README.md`](adr/README.md) for the ADR format and index.

## Planning Notes

Planning notes capture future options and open design threads. They are not
accepted architecture decisions.

| Document | Purpose |
|---|---|
| [`jetmon-deliverer-rollout.md`](jetmon-deliverer-rollout.md) | Operational rollout policy for moving outbound dispatch from embedded `jetmon2` workers to standalone `jetmon-deliverer`. |
| [`outbound-credential-encryption-plan.md`](outbound-credential-encryption-plan.md) | Migration plan for encrypting webhook secrets and alert-contact destination credentials after the current plaintext v2 model. |
| [`public-api-gateway-tenant-contract.md`](public-api-gateway-tenant-contract.md) | Gateway boundary contract, implemented Jetmon-side tenant ownership checks, and remaining public-exposure prerequisites. |
| [`v1-to-v2-pinned-rollout.md`](v1-to-v2-pinned-rollout.md) | Initial production migration plan for replacing v1 static-bucket hosts with v2 hosts pinned to the same ranges before enabling dynamic ownership. |
| [`v3-probe-agent-architecture-options.md`](v3-probe-agent-architecture-options.md) | Post-v2 architecture options for evolving from main servers plus Verifliers toward a probe-agent architecture. |
