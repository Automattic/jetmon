---
name: safe-background-work
description: Pick useful Jetmon work that cannot affect active uptime-bench or Jetmon tests.
---

# Safe Background Work

Use this when tests are running and Chris asks what can be done without
interrupting them.

## Allowed By Default

- Local code review and static analysis.
- Agent-specific files.
- Branch inspection and commit comparison.
- Handoff writing.
- Local-only planning for changes that will not be deployed.

## Ask First

- Deploying binaries or configs.
- Restarting `jetmon2`, Jetmon v1, bridge, Veriflier, database, StatsD, or
  monitoring services.
- Moving support services between hosts.
- Changing bucket ownership, pinned bucket ranges, or test fleet data.
- Running smoke tests that create, delete, or modify sites/providers.

## Blocker Policy

If a safe task becomes blocked on approval, record the blocker and move to the
next safe task.
