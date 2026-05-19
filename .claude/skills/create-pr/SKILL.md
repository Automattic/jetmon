---
name: create-pr
description: Create a PR for the current branch based on the PR template
allowed-tools: Bash(git diff:*), Bash(git log:*), Bash(git status:*), Bash(git branch:*), Bash(git show:*), Bash(gh pr create:*), Bash(git fetch:*)
---

# Create PR

Create a PR for the current branch, targeting `v2` unless the user requests a
different base branch.

## Usage

- `/create-pr` - Analyze the current branch and create a PR

## Process

1. Gather branch context:
   - Run `git fetch origin`.
   - Run `git log origin/v2..HEAD --oneline`.
   - Run `git diff origin/v2...HEAD --stat`.
   - Run `git diff origin/v2...HEAD`.
2. Take the whole branch into account, not just the most recent commit.
3. Analyze affected components, rollout impact, config impact, and tests.
4. Avoid secrets, tokens, internal credentials, and customer data in the PR
   title, body, screenshots, and command output.

## Component Map

| Component | Key Files |
|-----------|-----------|
| CLI / Entry Point | `cmd/jetmon2/main.go`, `cmd/jetmon2/*.go` |
| Orchestrator | `internal/orchestrator/` |
| HTTP Checker | `internal/checker/` |
| Database / Migrations | `internal/db/`, `migrations/` |
| Config | `internal/config/`, `config/config.readme`, `config/config-sample.json` |
| Veriflier Transport | `internal/veriflier/`, `veriflier2/` |
| WPCOM Client | `internal/wpcom/` |
| Metrics | `internal/metrics/` |
| API / Auth | `internal/api/`, `internal/apikeys/` |
| Dashboard | `internal/dashboard/` |
| Webhooks / Alerting | `internal/webhooks/`, `internal/alerting/` |
| Docker / Deployment | `docker/`, `systemd/`, `docs/*rollout*`, `docs/operations-guide.md` |
| Rollout Tooling | `scripts/`, `cmd/jetmon2/rollout*.go` |

## PR Body

```markdown
## Summary

Brief description of what this PR accomplishes and why.

## Changes

- Bullet points describing specific changes
- Include relevant config, deployment, or compatibility notes

## Testing

- [ ] `make test`
- [ ] `make rollout-docs-verify` (if rollout docs changed)
- [ ] Docker/local smoke test (if runtime behavior changed)
- [ ] Migration smoke (if schema or DB behavior changed)

## Deployment Notes

Any required rollout steps, config changes, validation gates, or rollback notes.
```

Create as draft unless the user explicitly says it is ready for review:

```bash
gh pr create --draft --base v2 --assignee @me
```
