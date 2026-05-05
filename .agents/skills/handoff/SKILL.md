---
name: handoff
description: Create a self-contained Jetmon handoff for another agent.
---

# Jetmon Handoff

Use this when Chris asks for a handoff doc or wants another agent to continue a
Jetmon thread.

## Include

- Repo path, branch, and relevant commit IDs.
- Whether the work affects Jetmon v1, Jetmon v2, Veriflier, bridge, support
  services, or uptime-bench.
- Active test locks and what must not be changed.
- Problem statement, evidence, and current hypothesis.
- Relevant logs, reports, metrics, PRs, and file paths.
- Commands already run and their outcome.
- Next recommended actions and approvals needed.

## Placement

During active tests, prefer `.agents` or global memory for agent-only handoffs.
Ask before editing non-agent project docs.

## Secrets

Do not include tokens, passwords, private keys, or unredacted service configs.
