# Labs And Rehearsals

This file replaces the separate rollout, scale, soak, scalability, and
probe-safety lab pages. Lab scripts remain in `scripts/`; this guide explains
when to use each one and the safety rules that apply.

## Safety Rules

- Do not change deployed services, support hosts, databases, provider state,
  fleet config, or runtime config while uptime-bench or Jetmon capacity tests
  are active unless Chris explicitly approves it.
- Keep lab targets controlled: local fixtures, uptime-bench fixtures, or
  approved canaries only.
- Lab probes must not write customer incident state unless the specific lab is
  intentionally testing event creation in an isolated database.
- Use `--dry-run` first when a script supports it.
- Preserve logs and generated reports when a lab finds a regression.

## Docker Rollout Lab

Use for fast API-guided rollout rehearsal in local containers.

```bash
make rollout-docker-lab-doctor
make rollout-docker-lab
make rollout-docker-lab-clean
```

What it should prove:

- local config renders;
- API health and token creation work;
- rollout preflight/smoke/seed/activate/release commands behave as expected;
- rollback path is available;
- no outbound production effects occur.

Useful overrides are script-specific environment variables; inspect
`scripts/rollout-docker-lab.sh` before running unusual scenarios.

## Rollout VM Lab

Use for production-shaped rehearsal with KVM/libvirt snapshots, systemd units,
and host-level rollout commands.

Common flow:

```bash
make rollout-vm-lab-doctor
make rollout-vm-lab-prepare
make rollout-vm-lab-smoke
```

Artifact sync:

```bash
make rollout-vm-lab-sync
make rollout-vm-lab-sync-artifacts
```

Focused smoke targets:

```bash
make rollout-vm-lab-execute-smoke
make rollout-vm-lab-failure-smoke
make rollout-vm-lab-resume-smoke
make rollout-vm-lab-post-start-rollback-smoke
make rollout-vm-lab-bad-ssh-smoke
make rollout-vm-lab-v2-start-failure-smoke
make rollout-vm-lab-runtime-guard-smoke
make rollout-vm-lab-real-activity-smoke
```

Snapshot runs:

```bash
make rollout-vm-lab-snapshot-execute-smoke
make rollout-vm-lab-snapshot-all-smoke
```

Required environment usually includes:

```bash
export ROLLOUT_VM_LAB_HOST=<host>
export ROLLOUT_VM_LAB_SSH='ssh'
export ROLLOUT_VM_LAB_SNAPSHOT=<snapshot-name>
```

The sync target copies `scripts/rollout-vm-lab.sh` and this consolidated lab
guide to the remote tool directory.

## Scale Resilience Lab

Use for dynamic bucket ownership, host loss, and database disruption behavior.

```bash
make scale-resilience-lab
make scale-resilience-lab-clean
```

Expected evidence:

- hosts claim buckets through DB transactions;
- stale hosts are absorbed after the heartbeat grace;
- no two live hosts claim the same dynamic range;
- process health shows ownership and queue state accurately;
- recovery after DB disruption does not lose coverage silently.

## V2 Soak Lab

Use for sustained v2 operation without outbound side effects.

```bash
make v2-soak-lab
make v2-soak-lab-clean
```

Expected evidence:

- stable queue depth and sites/sec;
- bounded memory and goroutine count;
- check history and runtime writes remain healthy;
- WPCOM/webhook/alert side effects are disabled, stubbed, or isolated;
- dashboards and StatsD expose enough signal to explain behavior.

## Scalability Checks

Use these before making claims about host capacity:

1. Validate DB query plans for target reload, freshness writes, check-history
   inserts, event handling, and process-health reads.
2. Capture scheduler phase timings, queue depth, worker utilization, DB write
   latency, check latency percentiles, and memory/goroutine profiles.
3. Run a capacity ladder instead of jumping straight to the largest target set.
4. Compare one change at a time: worker count, dataset size, check-history mode,
   target reload cadence, and runtime write cadence.
5. Record the limiting resource with evidence.

Do not tune based on average sites/sec alone; stale checks, queue growth, and DB
write pressure matter more.

## Probe-Safety Integration Checks

Use controlled fixtures to validate target-safety behavior:

- DNS rebinding from public to private/link-local addresses;
- private IP literals;
- localhost and link-local names;
- TLS handshake failures;
- expired and soon-to-expire certificates;
- deprecated TLS versions;
- redirect chains into blocked targets.

Expected behavior:

- unsafe targets are blocked before the HTTP client connects;
- audit rows explain the block;
- customer-site downtime is not reported for monitor/probe safety failures;
- rollout smoke and method comparison obey the same guardrails.

## Production-Shaped Rollout Evidence

A complete rehearsal report should include:

- commit SHA and image tags;
- config diff or rendered config fingerprint;
- database/schema version;
- Veriflier quorum report;
- rollout session/job IDs;
- dry-run and execute command transcripts;
- canary file used;
- dashboard screenshots or exported status where appropriate;
- rollback test result;
- known gaps and owner.

Store long raw benchmark reports in the sibling `uptime-bench` repo, not in
Jetmon docs.
