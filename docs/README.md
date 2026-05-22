# Jetmon Docs

This directory holds longer-form design material that does not belong in the
main README.

## Architecture Decisions

Accepted decisions live in [`adr/`](adr/). These records are append-only and
capture load-bearing choices that the current v2 implementation depends on.

Start with [`adr/README.md`](adr/README.md) for the ADR format and index,
including the Veriflier discovery trust decision in
[`adr/0010-trusted-veriflier-discovery.md`](adr/0010-trusted-veriflier-discovery.md).

## User Guides

| Document | Purpose |
|---|---|
| [`project.md`](project.md) | Full product and implementation specification for Jetmon 2. |
| [`architecture.md`](architecture.md) | High-level architecture, package responsibilities, and deployment shape. |
| [`internal-api-reference.md`](internal-api-reference.md) | Internal REST API reference and design notes. |
| [`events.md`](events.md) | Event lifecycle, transition semantics, and projection rules. |
| [`taxonomy.md`](taxonomy.md) | Severity, state, cause, rollup, and test taxonomy. |
| [`getting-started.md`](getting-started.md) | Local Docker setup, build/test commands, API CLI smoke runs, fixture failure simulation, and tenant import basics. |
| [`operations-guide.md`](operations-guide.md) | Production configuration, host setup, rollout modes, delivery workers, metrics, dashboard checks, and debugging. |
| [`rollout-quick-reference.md`](rollout-quick-reference.md) | One-page operator checklist for the v1-to-v2 rollout, linked back to the full migration runbook. |
| [`rollout-canaries.example.json`](rollout-canaries.example.json) | Template for operator-supplied synthetic canaries used by API rollout preflight and smoke gates. |
| [`rollout-vm-lab.md`](rollout-vm-lab.md) | KVM/libvirt lab harness for rehearsing rollout flows with DB, v1, and fresh v2 VMs plus snapshots. |
| [`scale-resilience-lab.md`](scale-resilience-lab.md) | Internal-only Docker lab for dynamic bucket ownership, Monitor failure takeover, Veriflier telemetry, and DB disruption recovery. |
| [`v2-soak-lab.md`](v2-soak-lab.md) | Internal-only Docker soak for sustained normal v2 operation with Monitors, Verifliers, fixture sites, and no outbound side effects. |
| [`production-rollout-lab.md`](production-rollout-lab.md) | Production-shaped rollout lab plan for validating DB server maps, read replicas, container networking, StatsD/Graphite, Verifliers, WPCOM simulation, API rollout flow, and rollback before production hardware is touched. |
| [`production-teamcity-rollout.md`](production-teamcity-rollout.md) | Production TeamCity rollout plan for Monitor containers, docker-deploy, config-sync sidecar, host-local StatsD proxy use, and secret-free database server map handling. |
| [`production-veriflier-compose.md`](production-veriflier-compose.md) | VPS Veriflier Docker Compose deployment with bundled StatsD/Graphite for central Grafana scraping. |
| [`jetmon-v2-prelaunch-readiness.md`](jetmon-v2-prelaunch-readiness.md) | Launch-readiness gates, canary checks, owner split, and service-side hardening map for the v2 drop-in rollout. |
| [`data-model.md`](data-model.md) | Legacy and v2 tables, additive migrations, event-sourced incident state, legacy projection, and tenant mapping. |
| [`support-guide.md`](support-guide.md) | Happiness Engineer workflows for explaining alerts, missed alerts, false positives, maintenance windows, and WPCOM payloads. |
| [`api-cli-guide.md`](api-cli-guide.md) | Feature guide and examples for using `jetmon2 api` against the internal REST API during local testing, rehearsals, and CI smoke runs. |
| [`v1-to-v2-migration.md`](v1-to-v2-migration.md) | Full production migration runbook from v1 to v2, including preparation, same-server replacement, fresh-server takeover, monitoring, revert paths, dynamic ownership cutover, and v1 teardown. |
| [`changelog.md`](changelog.md) | Release notes and implementation history. |

## Plans And Follow-Ups

These docs cover future options, open design threads, and repeatable test
plans. They are useful operating context, but they are not accepted
architecture decisions.

| Document | Purpose |
|---|---|
| [`roadmap.md`](roadmap.md) | Broader v2/v3 planning, deferred feature work, and public API prerequisites. |
| [`api-cli-roadmap.md`](api-cli-roadmap.md) | Completed implementation history for the local `jetmon2 api` helper CLI used during Docker and rollout testing. |
| [`jetmon-deliverer-rollout.md`](jetmon-deliverer-rollout.md) | Operational rollout policy for moving outbound dispatch from embedded `jetmon2` workers to standalone `jetmon-deliverer`. |
| [`outbound-credential-encryption-plan.md`](outbound-credential-encryption-plan.md) | Migration plan for encrypting webhook secrets and alert-contact destination credentials after the current plaintext v2 model. |
| [`public-api-gateway-tenant-contract.md`](public-api-gateway-tenant-contract.md) | Gateway boundary contract, implemented Jetmon-side tenant ownership checks, and remaining public-exposure prerequisites. |
| [`probe-safety-integration-test-plan.md`](probe-safety-integration-test-plan.md) | Integration test plan for DNS rebinding and TLS pathology probe-safety coverage. |
| [`jetmon-v2-scalability-test-plan.md`](jetmon-v2-scalability-test-plan.md) | Repeatable checklist for validating scheduler and check-path efficiency changes. Historical raw reports live in the sibling `uptime-bench` repo. |
| [`v3-probe-agent-architecture-options.md`](v3-probe-agent-architecture-options.md) | Post-v2 architecture options for evolving from main servers plus Verifliers toward a probe-agent architecture. |
