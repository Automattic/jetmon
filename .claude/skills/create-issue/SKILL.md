---
name: create-issue
description: Create a well-structured GitHub issue for Jetmon work
allowed-tools: Bash(gh issue create:*), Bash(gh issue list:*), Bash(git diff:*), Bash(git log:*), Bash(git status:*), Bash(git branch:*)
---

# Create GitHub Issue

Create a high-quality GitHub issue for the Automattic/jetmon repository.

## Usage

- `/create-issue` - Interactive mode
- `/create-issue [brief description]` - Quick mode

## Context Collection

Before creating the issue, gather relevant context:

1. Current branch changes, if related:
   - `git diff origin/v2...HEAD --stat`
   - `git log origin/v2..HEAD --oneline`
2. Problem and impact:
   - What behavior is wrong or missing?
   - Which component is affected?
   - Is this blocking production rollout?
   - What is the acceptance criteria?

## Issue Quality Principles

- Start with a verb.
- Include reproduction steps or evidence when available.
- State production, security, performance, or rollout impact plainly.
- Define what "done" means.
- Keep the issue secret-free.

## Labels

| Label | Use When |
|-------|----------|
| `bug` | Something is broken or not working as expected |
| `enhancement` | Improvement to existing functionality |
| `performance` | Speed, memory, throughput, or resource usage |
| `documentation` | Documentation updates |
| `infrastructure` | Docker, deployment, systems, or lab tooling |
| `security` | Auth, secrets, SSRF, TLS, or attack-surface concerns |

## Issue Template

```markdown
## Problem

Brief description of the issue or need.

## Affected Component(s)

- [ ] CLI / Entry Point (`cmd/jetmon2/`)
- [ ] Orchestrator (`internal/orchestrator/`)
- [ ] Checker (`internal/checker/`)
- [ ] Database / Migrations (`internal/db/`, `migrations/`)
- [ ] Configuration (`internal/config/`)
- [ ] Veriflier Transport (`internal/veriflier/`, `veriflier2/`)
- [ ] WPCOM Client (`internal/wpcom/`)
- [ ] Metrics (`internal/metrics/`)
- [ ] API / Auth (`internal/api/`, `internal/apikeys/`)
- [ ] Dashboard (`internal/dashboard/`)
- [ ] Webhooks / Alerting (`internal/webhooks/`, `internal/alerting/`)
- [ ] Docker / Infrastructure (`docker/`, `systemd/`, `scripts/`)
- [ ] Documentation

## Evidence / Reproduction

1. Step one
2. Step two
3. Expected vs actual behavior

## Proposed Solution

Optional description of a likely fix.

## Acceptance Criteria

- [ ] Specific, testable requirement
- [ ] Tests or validation updated
- [ ] Docs updated if behavior or rollout changes
```

Use `gh issue create` with an appropriate title, body, and label set.
