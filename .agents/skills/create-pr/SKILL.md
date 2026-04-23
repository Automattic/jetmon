---
name: create-pr
description: Create a PR for the current branch based on the PR template
allowed-tools: Bash(git diff:*), Bash(git log:*), Bash(git status:*), Bash(git branch:*), Bash(git show:*), Bash(gh pr create:*), Bash(git fetch:*)
---

# Create PR

Create a PR for the current branch, targeting `master`.

## Usage

- `/create-pr` - Analyze the current branch and create a PR

## Process

1. **Gather branch context**:
   - Run `git fetch origin` to ensure we have the latest refs
   - Run `git log origin/master..HEAD --oneline` to see all commits on the branch
   - Run `git diff origin/master...HEAD --stat` to see what files changed
   - Run `git diff origin/master...HEAD` to see the actual changes

2. **Take the entire branch into account**, not just the most recent commit. All commits since branching from `origin/master` should inform the PR description.

3. **Analyze the changes** to understand:
   - Which component(s) are affected (master process, worker, C++ addon, veriflier, config)
   - What type of change this is (bug fix, feature, refactor, performance, config change)
   - Any potential risks or testing considerations

4. **Follow these style guidelines**:
   - Use clear, concise titles that describe what the PR does
   - Start the title with a verb (Add, Fix, Update, Remove, Refactor)
   - For "Changes" section, use bullet points summarizing what changed
   - Include any relevant configuration changes or deployment notes

5. **Identify affected components** based on the changes:

| Component | Key Files |
|-----------|-----------|
| CLI / Entry Point | `cmd/jetmon2/main.go` |
| Orchestrator | `internal/orchestrator/` |
| HTTP Checker | `internal/checker/checker.go` |
| Goroutine Pool | `internal/checker/pool.go` |
| Database | `internal/db/` |
| Config | `internal/config/config.go`, `config/config.readme` |
| gRPC / Veriflier Transport | `internal/grpc/` |
| WPCOM Client | `internal/wpcom/client.go` |
| Audit Log | `internal/audit/audit.go` |
| Metrics | `internal/metrics/metrics.go` |
| Operator Dashboard | `internal/dashboard/dashboard.go` |
| Veriflier Binary | `veriflier2/cmd/main.go` |
| Docker | `docker/docker-compose.yml`, `docker/Dockerfile*` |
| Migrations | `internal/db/migrations.go`, `migrations/001_jetmon2.sql` |

6. **Determine testing requirements**:
   - Config changes: test with `./jetmon2 validate-config`
   - DB/schema changes: test migration with `./jetmon2 migrate`
   - All changes: test with Docker environment (`docker compose up --build`)
   - Run `make test` to verify unit tests pass

7. **Create the PR** using `gh pr create --draft --assignee @me` with this format:

```markdown
## Summary

Brief description of what this PR accomplishes and why.

## Changes

- Bullet points describing specific changes
- Include technical details relevant to reviewers
- Note any configuration changes required

## Affected Components

- List components from the table above that are modified

## Testing

- [ ] Tested locally with Docker environment (`docker compose up --build`)
- [ ] `make test` passes
- [ ] `./jetmon2 validate-config` passes (if config changes)
- [ ] Migration tested with `./jetmon2 migrate` (if schema changes)
- [ ] Tested configuration reload via `./jetmon2 reload` (if config changes)

## Deployment Notes

Any special deployment considerations (e.g., config changes, database migrations, Systems team coordination)
```

## Important Notes

- Jetmon is a private repository, but avoid including sensitive information (auth tokens, internal URLs) in PR descriptions
- Changes require Systems team deployment to production hosts
- Mention if the change affects bucket configuration or horizontal scaling
- Note any metrics changes that would affect Grafana dashboards
