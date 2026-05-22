# Rollout VM Lab

The rollout VM lab rehearses the v1-to-v2 host handoff on real Linux guests.
Use it for failures Docker cannot model well: systemd state, SSH reachability
between old and fresh hosts, cloud-init provisioning, writable runtime paths,
service start/stop order, and snapshot-backed rollback.

Harness: [`scripts/rollout-vm-lab.sh`](../scripts/rollout-vm-lab.sh).

Run the harness on the virtualization host:

```bash
ssh jetmon-vm-host-1
cd /path/to/jetmon
scripts/rollout-vm-lab.sh doctor
```

## Host Requirements

The host needs:

- KVM through `/dev/kvm`
- operator access to `qemu:///system`
- active libvirt NAT network, default `default`
- active libvirt storage pool, default `jetmon-rollout`
- write access to the pool path
- `qemu-img`, `virt-install`, `cloud-localds`, `ssh`, `scp`, `curl`, `mysql`,
  `sed`, and `awk`
- dedicated lab SSH key, default `~/.ssh/jetmon-rollout-lab_ed25519`

`doctor` is read-only except for checking local files and libvirt state.

## Topology

The baseline topology has three guests:

| VM | Purpose |
| --- | --- |
| `jetmon-rollout-db` | MariaDB host with v1-shaped seed data and v2 migrations. |
| `jetmon-rollout-v1` | Old Monitor host modeled by `jetmon-v1-sim.service`. |
| `jetmon-rollout-v2` | Fresh v2 runtime host where guided rollout commands run. |

Guests are created with a `jetmon` user, passwordless sudo, and the lab SSH
key. The DB guest creates `jetmon_db` and grants `jetmon` / `jetmon`.

`start-topology` only starts the expected db/v1/v2 domains derived from
`JETMON_ROLLOUT_PREFIX`. It treats already-running guests as OK and refuses
paused, crashed, suspended, or incomplete topologies so libvirt state can be
inspected before continuing.

## First-Time Setup

```bash
scripts/rollout-vm-lab.sh fetch-image
scripts/rollout-vm-lab.sh create-topology
scripts/rollout-vm-lab.sh start-topology
scripts/rollout-vm-lab.sh wait-ssh db
scripts/rollout-vm-lab.sh wait-ssh v1
scripts/rollout-vm-lab.sh wait-ssh v2
scripts/rollout-vm-lab.sh prepare-topology
```

`prepare-topology` is idempotent for lab data and staged service files. It:

- seeds ten active sites in buckets `0-99`
- installs and starts the v1 simulator
- stages `jetmon2`, config, env, systemd unit, and `rollout-buckets.csv` on v2
- installs the lab SSH key on v2 so fresh-server stop/start commands can reach
  v1
- applies v2 migrations from the v2 VM against the DB VM
- runs host preflight and a guided fresh-server dry-run

The v2 `jetmon2` service remains staged but stopped. V1 owns the range until a
guided flow reaches the explicit stop-v1/start-v2 step.

## Local Make Targets

The Makefile wraps artifact sync, VM startup, v2 staging, and remote execution:

```bash
make rollout-vm-lab-doctor
make rollout-vm-lab-prepare
make rollout-vm-lab-stage-v2
make rollout-vm-lab-smoke
make rollout-vm-lab-execute-smoke
make rollout-vm-lab-failure-smoke
make rollout-vm-lab-resume-smoke
make rollout-vm-lab-post-start-rollback-smoke
make rollout-vm-lab-bad-ssh-smoke
make rollout-vm-lab-v2-start-failure-smoke
make rollout-vm-lab-runtime-guard-smoke
make rollout-vm-lab-real-activity-smoke
make rollout-vm-lab-snapshot-all-smoke
```

Use the Make targets from your workstation when the lab host is reachable over
SSH. Use the script directly when already on the lab host.

## Common Workflows

Inspect the lab:

```bash
scripts/rollout-vm-lab.sh list
scripts/rollout-vm-lab.sh ssh v2
scripts/rollout-vm-lab.sh ssh db 'sudo systemctl status mariadb --no-pager'
```

Run lightweight v2-side gates:

```bash
scripts/rollout-vm-lab.sh start-topology
scripts/rollout-vm-lab.sh smoke-preflight
scripts/rollout-vm-lab.sh smoke-guided-dry-run
```

Run the full execute-mode cutover and rollback smoke:

```bash
scripts/rollout-vm-lab.sh smoke-guided-execute-rollback
```

This stops the v1 simulator, starts real `jetmon2`, verifies post-start gates,
then resumes guided rollback to stop v2 and restart v1.

Run targeted failure and recovery flows:

```bash
scripts/rollout-vm-lab.sh smoke-interrupted-resume
scripts/rollout-vm-lab.sh smoke-post-start-rollback
scripts/rollout-vm-lab.sh smoke-bad-ssh
scripts/rollout-vm-lab.sh smoke-v2-start-failure
scripts/rollout-vm-lab.sh smoke-runtime-guards
scripts/rollout-vm-lab.sh smoke-real-activity
scripts/rollout-vm-lab.sh smoke-failure-gates
```

Coverage:

- interrupted guided flow resumes from state and rolls back cleanly
- failed post-start activity gate offers guided rollback
- bad SSH target fails before v1 is stopped or v2 is started
- v2 service start failure leaves a resumable stopped-v1 state and returns the
  lab to v1 afterward
- unwritable rollout log directory is rejected before checks or service
  commands run
- broken DB config is rejected during host preflight
- real `jetmon2` activity writes
  `jetpack_monitor_site_runtime.last_checked_at` for every seeded site
- overlapping dynamic bucket ownership and broken systemd unit are rejected
  before service state changes

## Snapshots

Snapshots are offline by design. The harness shuts guests down before snapshot
create/revert so disk state is deterministic.

Create checkpoints:

```bash
scripts/rollout-vm-lab.sh snapshot-all base-installed
scripts/rollout-vm-lab.sh snapshot-all db-seeded
scripts/rollout-vm-lab.sh snapshot-all pre-guided-flow
```

Revert:

```bash
scripts/rollout-vm-lab.sh revert-all pre-guided-flow
scripts/rollout-vm-lab.sh wait-ssh db
scripts/rollout-vm-lab.sh wait-ssh v1
scripts/rollout-vm-lab.sh wait-ssh v2
```

Replay one or every snapshot-backed flow:

```bash
scripts/rollout-vm-lab.sh snapshot-run pre-guided-flow execute-rollback
scripts/rollout-vm-lab.sh snapshot-run-all pre-guided-flow
```

Supported flow names:

- `execute-rollback`
- `interrupted-resume`
- `post-start-rollback`
- `bad-ssh`
- `v2-start-failure`
- `runtime-guards`
- `real-activity`
- `failure-gates`

After each revert, the runner stages the current local `jetmon2` artifact into
the v2 guest so snapshot-backed tests do not silently use an old binary. At the
end, it reverts again and enforces the safe state: v1 simulator active and v2
`jetmon2` stopped/disabled.

## Cleanup

Destroy the topology and lab volumes:

```bash
scripts/rollout-vm-lab.sh destroy-topology
```

## Environment Overrides

| Variable | Default |
| --- | --- |
| `JETMON_ROLLOUT_LAB_DIR` | `~/rollout-lab` |
| `JETMON_ROLLOUT_POOL` | `jetmon-rollout` |
| `JETMON_ROLLOUT_NETWORK` | `default` |
| `JETMON_ROLLOUT_PREFIX` | `jetmon-rollout` |
| `JETMON_ROLLOUT_IMAGE_URL` | Ubuntu 24.04 noble amd64 cloud image |
| `JETMON_ROLLOUT_IMAGE_PATH` | `<pool path>/noble-server-cloudimg-amd64.img` |
| `JETMON_ROLLOUT_SSH_KEY` | `~/.ssh/jetmon-rollout-lab_ed25519` |
| `JETMON_ROLLOUT_WAIT_TIMEOUT` | `600` seconds |
| `JETMON_ROLLOUT_MEMORY_MIB` | `2048` |
| `JETMON_ROLLOUT_VCPUS` | `2` |
| `JETMON_ROLLOUT_DISK_GIB` | `20` |
| `JETMON_ROLLOUT_DB_MEMORY_MIB` | `4096` |
| `JETMON_ROLLOUT_DB_DISK_GIB` | `30` |
| `JETMON_ROLLOUT_BUCKET_MIN` | `0` |
| `JETMON_ROLLOUT_BUCKET_MAX` | `99` |
| `JETMON_ROLLOUT_BUCKET_TOTAL` | `1000` |
| `JETMON_ROLLOUT_ACTIVITY_WAIT_TIMEOUT` | `240` seconds |
| `JETMON_ROLLOUT_JETMON2_BINARY` | `<repo>/bin/jetmon2` |
| `JETMON_ROLLOUT_JETMON2_SERVICE` | `<repo>/systemd/jetmon2.service` |
| `ROLLOUT_VM_LAB_SNAPSHOT` | `pre-guided-flow` for Makefile snapshot smoke |
