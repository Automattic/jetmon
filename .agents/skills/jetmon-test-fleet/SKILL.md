---
name: jetmon-test-fleet
description: Work safely with Jetmon services used by uptime-bench capacity tests.
---

# Jetmon Test Fleet

Use this when Chris asks about Jetmon v1/v2 test services, Verifliers, support
services, Prometheus capacity data, or whether a Jetmon branch is ready for
uptime-bench tests.

## Safety First

- If tests are running, do not restart services, change config, move support
  services, deploy binaries, mutate databases, or alter target/provider state
  without explicit permission.
- Prefer read-only inspection and report analysis during active tests.
- State which repo is being acted on before making changes.

## Common Context

- Uptime-bench canonical repo:
  `/home/gaarai/code/uptime-bench`.
- Current Prometheus for Jetmon capacity work:
  `http://10.0.0.67:9091`.
- Service hosts:
  `jetmon-service-host-1`/`jetmon-v1`,
  `jetmon-service-host-2`/`jetmon-v2`,
  `jetmon-service-host-3`,
  `jetmon-service-host-4`.
- Support/monitoring hosts:
  `jetmon-vm-host-1`,
  `jetmon-vm-host-2`,
  `jetmon-vm-host-3`.

## Output Expectations

When answering readiness or risk questions, include:

- Branch and commit under discussion.
- What is deployed versus only local.
- Which checks were read-only.
- Whether changes are safe during an active uptime-bench run.
- Recommended next action and any approval needed.
