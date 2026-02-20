---
name: create-issue
description: Create a well-structured GitHub issue for Jetmon work
allowed-tools: Bash(gh issue create:*), Bash(gh issue list:*), Bash(git diff:*), Bash(git log:*), Bash(git status:*), Bash(git branch:*)
---

# Create GitHub Issue

Create a high-quality GitHub issue for the Automattic/jetmon repository.

## Usage

- `/create-issue` - Interactive mode: I'll ask questions to gather information
- `/create-issue [brief description]` - Quick mode: Provide a brief description and I'll create the issue

## Context Collection

Before creating the issue, gather context:

1. **Current branch changes** (if relevant):
   - Run `git diff master...HEAD --stat` to see what files changed
   - Run `git log master...HEAD --oneline` to see commits

2. **Issue details**:
   - What problem does this solve?
   - What component(s) are affected?
   - What are the acceptance criteria?
   - Is this blocking production or a nice-to-have?

## Issue Quality Principles

Every issue should drive action, not create overhead:

- Start with a verb: "Fix memory leak in worker" not "Memory leak exists"
- Provide context: Include logs, metrics, or reproduction steps
- Clarify impact: "Workers crash every 2 hours" vs "Slight memory increase"
- Define success: State what "done" looks like clearly
- Be concise: Maximum clarity, minimum words

## Issue Labels

Apply appropriate labels when creating issues:

| Label | Use When |
|-------|----------|
| `bug` | Something is broken or not working as expected |
| `enhancement` | Improvement to existing functionality |
| `performance` | Related to speed, memory, or resource usage |
| `documentation` | Documentation updates needed |
| `infrastructure` | Docker, deployment, or systems-related |

## Issue Content Structure

### Title

- Be descriptive and specific
- Use action verbs (Fix, Add, Update, Remove, Investigate)
- Include component context when relevant (e.g., "Fix memory leak in worker process")

### Description Template

```markdown
## Problem

Brief description of the issue or need. Include error messages, logs, or metrics if available.

## Affected Component(s)

- [ ] Master Process (`lib/jetmon.js`)
- [ ] Worker Process (`lib/httpcheck.js`)
- [ ] C++ Native Addon (`src/http_checker.cpp`)
- [ ] Veriflier (`veriflier/`)
- [ ] Database (`lib/database.js`)
- [ ] Configuration
- [ ] Docker/Infrastructure
- [ ] WPCOM Integration

## Steps to Reproduce (if applicable)

1. Step one
2. Step two
3. Expected vs actual behavior

## Proposed Solution (if known)

Description of how this might be fixed or implemented.

## Acceptance Criteria

- [ ] Specific, testable requirement
- [ ] Another requirement
- [ ] Tests pass / no regressions

## Additional Context

- Links to related issues or PRs
- Grafana dashboard screenshots
- Relevant metrics or logs
```

## Creating the Issue

Use the GitHub CLI to create the issue:

```bash
gh issue create --title "Issue title" --body "Issue body" --label "bug"
```

For multi-line bodies, use a heredoc:

```bash
gh issue create --title "Fix memory leak in worker process" --label "bug,performance" --body "$(cat <<'EOF'
## Problem

Workers are hitting memory limits more frequently than expected...

## Affected Component(s)

- [x] Worker Process (`lib/httpcheck.js`)

## Acceptance Criteria

- [ ] Workers stay under 53MB memory limit
- [ ] No increase in worker recycling frequency
EOF
)"
```

## Best Practices

1. **Link Related Work**: Reference PRs, other issues, or Grafana dashboards
2. **Include Metrics**: Add specific numbers when available (memory usage, error rates)
3. **Note Dependencies**: Mention if work depends on Systems team or external changes
4. **Testing Strategy**: Describe how the fix will be verified
5. **Production Impact**: Note if this affects live monitoring or requires coordination
