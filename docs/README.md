# Jetmon Docs

This directory is for durable Jetmon 2 reference material. Keep short project
orientation in the root [`README.md`](../README.md), keep accepted design
decisions in [`adr/`](adr/), and keep raw benchmark reports in the sibling
`uptime-bench` repo.

## Start Here

| Document | Use it for |
|---|---|
| [`project.md`](project.md) | Product goals and v2 feature scope. |
| [`architecture.md`](architecture.md) | Runtime shape, package map, and check flow. |
| [`getting-started.md`](getting-started.md) | Local Docker setup and first smoke tests. |
| [`operations-guide.md`](operations-guide.md) | Production config, metrics, dashboards, and debugging. |
| [`v1-to-v2-migration.md`](v1-to-v2-migration.md) | Full production migration and rollback runbook. |

## Rollout And Production

| Document | Use it for |
|---|---|
| [`rollout-quick-reference.md`](rollout-quick-reference.md) | Short operator checklist during rollout. |
| [`jetmon-v2-prelaunch-readiness.md`](jetmon-v2-prelaunch-readiness.md) | Launch gates, owners, and remaining approval evidence. |
| [`production-teamcity-rollout.md`](production-teamcity-rollout.md) | TeamCity/docker-deploy Monitor rollout details. |
| [`production-veriflier-compose.md`](production-veriflier-compose.md) | Veriflier VPS Docker Compose deployment. |
| [`docker-images.md`](docker-images.md) | Docker image build, config rendering, and runtime commands. |
| [`support-guide.md`](support-guide.md) | HE workflows for customer-facing alert explanations. |

## Reference

| Document | Use it for |
|---|---|
| [`internal-api-reference.md`](internal-api-reference.md) | Internal REST API endpoints and payloads. |
| [`api-cli-guide.md`](api-cli-guide.md) | `jetmon2 api` examples for tests and rehearsals. |
| [`data-model.md`](data-model.md) | Legacy and v2 tables, projections, and tenancy. |
| [`events.md`](events.md) | Event lifecycle and transition semantics. |
| [`taxonomy.md`](taxonomy.md) | Severity, state, rollup, and detection taxonomy. |
| [`changelog.md`](changelog.md) | Release notes and implementation history. |

## Labs And Test Plans

| Document | Use it for |
|---|---|
| [`production-rollout-lab.md`](production-rollout-lab.md) | Production-shaped rollout rehearsal. |
| [`rollout-docker-lab.md`](rollout-docker-lab.md) | Local container/API rollout rehearsal. |
| [`rollout-vm-lab.md`](rollout-vm-lab.md) | KVM/libvirt rollout rehearsal with snapshots. |
| [`scale-resilience-lab.md`](scale-resilience-lab.md) | Dynamic ownership, host loss, and DB disruption tests. |
| [`v2-soak-lab.md`](v2-soak-lab.md) | Sustained v2 operation without outbound side effects. |
| [`jetmon-v2-scalability-test-plan.md`](jetmon-v2-scalability-test-plan.md) | Scheduler and check-path scalability validation. |
| [`probe-safety-integration-test-plan.md`](probe-safety-integration-test-plan.md) | DNS rebinding and TLS pathology coverage. |
| [`rollout-canaries.example.json`](rollout-canaries.example.json) | Canary fixture template for rollout gates. |

## Roadmaps And Follow-Ups

These docs are active planning notes, not accepted architecture decisions.

| Document | Use it for |
|---|---|
| [`roadmap.md`](roadmap.md) | Deferred work and long-range planning. |
| [`api-cli-roadmap.md`](api-cli-roadmap.md) | Completed `jetmon2 api` implementation history. |
| [`jetmon-deliverer-rollout.md`](jetmon-deliverer-rollout.md) | Standalone deliverer migration plan. |
| [`outbound-credential-encryption-plan.md`](outbound-credential-encryption-plan.md) | Future secret-at-rest encryption work. |
| [`public-api-gateway-tenant-contract.md`](public-api-gateway-tenant-contract.md) | Public gateway boundary and tenant contract. |
| [`v3-probe-agent-architecture-options.md`](v3-probe-agent-architecture-options.md) | Post-v2 probe-agent architecture options. |

## Architecture Decisions

Accepted decisions live in [`adr/`](adr/). Start with
[`adr/README.md`](adr/README.md) for the ADR index.
