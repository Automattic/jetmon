---
name: jetmon-log-triage
description: Triage local Jetmon issues with compose logs and assertions
allowed-tools: Bash(scripts/agent/logs.sh), Bash(scripts/agent/assert.sh), Bash(docker*), Read, Grep
---

# Jetmon log triage

Use this skill when local checks fail or startup behavior is unclear.

## Usage

- `/jetmon-log-triage`
- `/jetmon-log-triage jetmon`
- `/jetmon-log-triage veriflier`

## Fast triage

1. Verify baseline health:

```bash
scripts/agent/assert.sh
```

2. Follow service logs:

```bash
scripts/agent/logs.sh jetmon
scripts/agent/logs.sh veriflier
scripts/agent/logs.sh mysqldb
```

3. If startup is inconsistent, rebuild and re-run assertions:

```bash
scripts/agent/rebuild.sh
scripts/agent/assert.sh
```
