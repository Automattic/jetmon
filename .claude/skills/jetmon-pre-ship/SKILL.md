---
name: jetmon-pre-ship
description: Run jetmon v2 pre-ship checklist before opening a PR
allowed-tools: Bash(go *) Bash(grep *) Bash(git *)
---

## Changed files
!`git diff main...HEAD --name-only`

## Race detector
!`go test -race ./... 2>&1 | tail -30`

## Known pitfall checks
Retry queue flush (must not happen at round start):
!`grep -rn "RetryQueue\|retryQueue" internal/orchestrator/ | grep -i "flush\|clear\|reset\|= \[\]" || echo "OK"`

Bucket claim outside transaction (must use SELECT FOR UPDATE):
!`grep -rn "UPDATE jetmon_hosts\|INSERT.*jetmon_hosts" internal/ | grep -v "_test.go" || echo "OK"`

Non-context DB calls:
!`grep -rn "\.Query\b\|\.QueryRow\b\|\.Exec\b" internal/ | grep -v "Context\|_test.go" || echo "OK"`

Open maintenance window risk:
!`grep -rn "maintenance_end" internal/ | grep -v "test\|nil\|IsZero" | head -10`

## Review
Work through each result above. Flag any violation. Then confirm the checklist from AGENTS.md is satisfied.
