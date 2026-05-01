# Rollout VM Lab

The rollout VM lab is a KVM/libvirt test bed for rehearsing the v1-to-v2 host
rollout on real Linux guests instead of containers. It is meant to catch the
host-level failures that are hard to validate in Docker: systemd unit state,
SSH reachability between fresh-server hosts, service start/stop ordering,
cloud-init provisioning, writable log paths, and snapshot-based rollback.

The lab harness is [`scripts/rollout-vm-lab.sh`](../scripts/rollout-vm-lab.sh).
Run it on the virtualization host itself. For the current in-house lab host:

```bash
ssh jetmon-deploy-test
cd /path/to/jetmon
scripts/rollout-vm-lab.sh doctor
```

## Host Requirements

The lab host needs:

- KVM available through `/dev/kvm`
- `qemu:///system` libvirt access for the operator user
- an active libvirt NAT network, default `default`
- an active libvirt storage pool, default `jetmon-rollout`
- write access to the storage pool path for the operator user
- `qemu-img`, `virt-install`, `cloud-localds`, `ssh`, `scp`, `curl`, `mysql`,
  `sed`, and `awk`
- a dedicated lab SSH key, default
  `~/.ssh/jetmon-rollout-lab_ed25519`

Validate the host:

```bash
scripts/rollout-vm-lab.sh doctor
```

The command is read-only except for checking local files and libvirt state.

## First-Time Setup

Fetch the Ubuntu cloud image once. By default the image is cached in the
libvirt storage pool so QEMU can use it as a backing file:

```bash
scripts/rollout-vm-lab.sh fetch-image
```

Create the baseline topology:

```bash
scripts/rollout-vm-lab.sh create-topology
scripts/rollout-vm-lab.sh start-topology
scripts/rollout-vm-lab.sh wait-ssh db
scripts/rollout-vm-lab.sh wait-ssh v1
scripts/rollout-vm-lab.sh wait-ssh v2
```

This creates:

| VM | Purpose |
| --- | --- |
| `jetmon-rollout-db` | MariaDB host for seeded v1-compatible data and v2 migrations. |
| `jetmon-rollout-v1` | Old monitor host used to model v1 service ownership. |
| `jetmon-rollout-v2` | Fresh v2 runtime host where guided rollout commands run. |

The guests use cloud-init to create a `jetmon` user with passwordless sudo and
the dedicated lab SSH key. The DB guest also installs MariaDB, listens on the
libvirt network, creates `jetmon_db`, and grants `jetmon` / `jetmon`.

## Prepare The Rollout Lab

After the topology is reachable, prepare it for real rollout command testing:

```bash
scripts/rollout-vm-lab.sh prepare-topology
```

This command is intentionally idempotent for the lab data and staged service
files. It:

- seeds the DB VM with a v1-compatible `jetpack_monitor_sites` table and ten
  active sites in buckets `0-99`
- installs and starts `jetmon-v1-sim.service` on the v1 VM
- stages `jetmon2`, `/opt/jetmon2/config/config.json`,
  `/opt/jetmon2/config/jetmon2.env`, `systemd/jetmon2.service`, logrotate, and
  `rollout-buckets.csv` on the v2 VM
- installs the lab SSH key on the v2 VM so fresh-server stop/start commands can
  reach the old v1 VM over SSH
- runs `jetmon2 migrate` from the v2 VM against the DB VM
- runs `rollout host-preflight` and a guided fresh-server dry-run from the v2 VM

From the local workstation, the Makefile wraps artifact sync, v2 VM artifact
staging, VM startup, and remote execution:

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

The harness keeps the v2 `jetmon2` service staged but stopped. That preserves
the production rollout shape: v1 owns the range until the guided flow reaches
the explicit stop-v1/start-v2 transition.

## Snapshots

Create a named snapshot after each known-good checkpoint:

```bash
scripts/rollout-vm-lab.sh snapshot-all base-installed
scripts/rollout-vm-lab.sh snapshot-all db-seeded
scripts/rollout-vm-lab.sh snapshot-all pre-cutover-ready
```

Return every VM to a checkpoint:

```bash
scripts/rollout-vm-lab.sh revert-all pre-cutover-ready
scripts/rollout-vm-lab.sh wait-ssh db
scripts/rollout-vm-lab.sh wait-ssh v1
scripts/rollout-vm-lab.sh wait-ssh v2
```

Snapshots are intentionally offline snapshots. The harness shuts the VM down
before creating or reverting snapshots so disk state is deterministic.

## Useful Commands

List current lab state:

```bash
scripts/rollout-vm-lab.sh list
```

SSH to a guest:

```bash
scripts/rollout-vm-lab.sh ssh v2
scripts/rollout-vm-lab.sh ssh db 'sudo systemctl status mariadb --no-pager'
```

Run only the v2-side rollout smoke checks:

```bash
scripts/rollout-vm-lab.sh start-topology
scripts/rollout-vm-lab.sh smoke-preflight
scripts/rollout-vm-lab.sh smoke-guided-dry-run
```

Run the heavier execute-mode cutover and rollback smoke. This actually stops
the v1 simulator, starts `jetmon2`, verifies the post-start gates, then resumes
guided rollback to stop `jetmon2` and restart the v1 simulator:

```bash
scripts/rollout-vm-lab.sh smoke-guided-execute-rollback
```

Run targeted guided-flow smokes:

```bash
scripts/rollout-vm-lab.sh smoke-interrupted-resume
scripts/rollout-vm-lab.sh smoke-post-start-rollback
scripts/rollout-vm-lab.sh smoke-bad-ssh
scripts/rollout-vm-lab.sh smoke-v2-start-failure
scripts/rollout-vm-lab.sh smoke-runtime-guards
scripts/rollout-vm-lab.sh smoke-real-activity
```

- `smoke-interrupted-resume` stops v1, intentionally leaves the first guided
  run at EOF, resumes from state, completes cutover, then rolls back to v1.
- `smoke-post-start-rollback` starts v2, forces the post-start activity gate to
  fail with a future cutoff, chooses guided rollback, and confirms v1 is active.
- `smoke-bad-ssh` uses an invalid v1 SSH target and confirms the flow fails
  before v1 is stopped or v2 is started.
- `smoke-v2-start-failure` corrupts only the staged v2 systemd start command,
  confirms the guided flow stops after v1 is stopped and v2 fails to start,
  then restores the unit and returns the range to v1.
- `smoke-runtime-guards` confirms guided rollout refuses an unwritable log
  directory before any rollout checks run, and confirms host preflight refuses
  a broken DB connection before service state changes.
- `smoke-real-activity` clears the seeded range's `last_checked_at`, stops the
  v1 simulator, starts real `jetmon2`, and waits for every active seeded site
  to receive a real check write before returning the range to v1.

Run the failure-gate smoke:

```bash
scripts/rollout-vm-lab.sh smoke-failure-gates
```

This injects an overlapping `jetmon_hosts` row and a broken staged systemd unit,
then confirms `rollout host-preflight` refuses both before restoring the DB
state.

Run a flow from a named snapshot, then revert back and normalize the safe lab
service state:

```bash
scripts/rollout-vm-lab.sh snapshot-all pre-guided-flow
scripts/rollout-vm-lab.sh snapshot-run pre-guided-flow execute-rollback
scripts/rollout-vm-lab.sh snapshot-run-all pre-guided-flow
```

Supported snapshot flow names are `execute-rollback`, `interrupted-resume`,
`post-start-rollback`, `bad-ssh`, `v2-start-failure`, `runtime-guards`,
`real-activity`, and `failure-gates`. Snapshot runners are useful when
iterating on guided behavior because each run starts from the same VM, DB,
service, and log state. After each revert, the runner stages the current local
`jetmon2` artifact into the v2 guest so snapshot-backed flows do not silently
test an old binary. The staging step starts shut-off lab VMs before waiting for
SSH, so `make rollout-vm-lab-snapshot-all-smoke` can be run directly after an
offline snapshot. At the end, the runner reverts to the snapshot and enforces
the safe lab state: v1 simulator active, v2 `jetmon2` stopped and disabled.
`snapshot-run-all` replays every named flow from the same snapshot.

Destroy the topology and its lab volumes:

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
| `JETMON_ROLLOUT_JETMON2_LOGROTATE` | `<repo>/systemd/jetmon2-logrotate` |
| `ROLLOUT_VM_LAB_SNAPSHOT` | `pre-guided-flow` for Makefile snapshot smoke |

## Planned Flow Coverage

The VM lab is intended to exercise these rollout scenarios:

- DB seeded with the v1-compatible site table plus v2 additive migrations
- v1 host active for one static bucket range
- fresh v2 host staged with pinned config but stopped
- `rollout guided --dry-run` from the v2 runtime host
- successful fresh-server cutover with `--execute-operator-commands`
- guided rollback after execute-mode cutover
- interrupted guided flow and resume from state
- failed pre-stop gate refusal
- bad staged systemd unit refusal
- failed post-start smoke gate followed by guided rollback
- bad SSH access from the v2 runtime host to the old v1 host
- failed v2 service start after v1 has stopped, preserving a resumable stopped
  state and returning the lab to v1 after the fixture
- unwritable rollout log directory refusal before any rollout checks or service
  commands run
- bad DB connection refusal during host preflight
- real v2 monitor activity that writes seeded sites' `last_checked_at`
- snapshot-backed flow reruns
- bad systemd unit refusal

The current harness provides VM lifecycle, DB seeding, v1/v2 service staging,
preflight/dry-run smoke coverage, and a full execute-mode cutover plus guided
rollback smoke. It also exercises interrupted resume, post-start rollback, bad
SSH, v2 start failure, runtime guard failures, real activity, failure gates,
and named-snapshot reruns. The next layer should add more specialized failure
fixtures as new rollout bugs are discovered.
