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
| [`outbound-credential-encryption-plan.md`](outbound-credential-encryption-plan.md) | Migration plan for encrypting webhook secrets and alert-contact destination credentials after the current plaintext v2 model. |
| [`v3-probe-agent-architecture-options.md`](v3-probe-agent-architecture-options.md) | Post-v2 architecture options for evolving from main servers plus Verifliers toward a probe-agent architecture. |
