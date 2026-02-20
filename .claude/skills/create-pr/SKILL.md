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
| Master Process | `lib/jetmon.js` |
| Worker Process | `lib/httpcheck.js` |
| C++ Native Addon | `src/http_checker.cpp`, `src/http_checker.h`, `binding.gyp` |
| Veriflier | `veriflier/*.cpp`, `veriflier/*.h` |
| Database | `lib/database.js`, `lib/dbpools.js` |
| Configuration | `config/config.json`, `config/config.readme` |
| Docker | `docker/docker-compose.yml`, `docker/Dockerfile*` |
| StatsD/Metrics | Look for `statsdClient` calls |
| WPCOM Integration | `lib/wpcom.js`, `lib/comms.js` |

6. **Determine testing requirements**:
   - C++ changes require `npm run rebuild-run`
   - Config changes should be tested with Docker environment
   - Worker changes should be tested with memory monitoring
   - Database changes need schema verification

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

- [ ] Tested locally with Docker environment
- [ ] Ran `npm run rebuild-run` (if C++ changes)
- [ ] Verified memory usage is within limits (if worker changes)
- [ ] Tested configuration reload via SIGHUP (if config changes)

## Deployment Notes

Any special deployment considerations (e.g., config changes, database migrations, Systems team coordination)
```

## Important Notes

- Jetmon is a private repository, but avoid including sensitive information (auth tokens, internal URLs) in PR descriptions
- Changes require Systems team deployment to production hosts
- Mention if the change affects bucket configuration or horizontal scaling
- Note any metrics changes that would affect Grafana dashboards
