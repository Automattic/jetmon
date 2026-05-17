# Scale Resilience Lab

The scale resilience lab is an internal-only Docker rehearsal for dynamic
Monitor bucket ownership, Monitor failure takeover, Veriflier telemetry, and
fleet dashboard visibility.

Run it from a Docker host that can spend temporary CPU and database resources:

```bash
make scale-resilience-lab
```

Clean up the isolated project and volumes:

```bash
make scale-resilience-lab-clean
```

## Safety Model

- Uses Docker project `jetmon-scale-lab` by default.
- Writes an ignored local `config/config.json` with
  `WPCOM_NOTIFY_ENABLE=false`, `EMAIL_TRANSPORT=stub`, `DEBUG_PORT=0`, and
  dynamic bucket ownership enabled. `DELIVERY_OWNER_HOST` is pinned to the
  first Monitor so the fleet summary does not report multi-owner delivery
  posture while the lab scales Monitor processes.
- Uses only local Docker containers for MySQL, Monitors, Verifliers, StatsD,
  Mailpit, and the HTTP fixture.
- Uses a dedicated Docker network with a public-looking fixture address
  (`93.184.217.20` by default). Target safety stays enabled, but fixture
  traffic stays inside the Docker host.
- Does not create webhooks or alert contacts.
- Does not contact WPCOM or change production systems.

## Flow

The lab script:

1. Builds every lab image, including each named Monitor service image, so
   scale-out services cannot accidentally reuse stale per-service Docker images.
2. Starts one Monitor and three Verifliers.
3. Seeds fixture-backed sites directly into the isolated Docker database across
   the configured bucket space, avoiding API rate-limit tuning in the product.
4. Waits for healthy dynamic bucket coverage and recent check activity with one
   Monitor.
5. Adds a second Monitor and verifies bucket redistribution.
6. Adds two more Monitors and verifies four-way redistribution.
7. Gracefully stops one Monitor and then a second Monitor, verifying the
   survivors reclaim bucket coverage and every site continues being checked
   after each change.
8. Recovers stopped Monitors and verifies four-way coverage again.
9. Hard-kills one Monitor without graceful release, waits for heartbeat/grace
   takeover, verifies full bucket coverage and site activity, then recovers it.
10. Injects database disruption in the isolated lab database: a runtime-table
   lock, temporary read-only mode, a paused database container, and a database
   restart. Each disruption must recover to full Monitor coverage and site
   activity.
11. Stops one Veriflier and then a second Veriflier, verifying fresh agent
   telemetry and fleet dashboard snapshots report the degraded Veriflier state.
12. Recovers Verifliers and captures the final fleet snapshot.

JSON outputs are written under `logs/scale-resilience-lab/`.

Fleet snapshots are asserted against expected states. Stable checkpoints must
report green summary, green bucket coverage, green Verifliers, and three fresh
Veriflier agents. Graceful Monitor stops must keep bucket coverage green and
site checks fresh; the fleet summary may be green or temporarily degraded
depending on process-health cache timing and whether the stopped Monitor row is
still visible. Ungraceful Monitor failures and Veriflier failure checkpoints
must report degraded red/amber summaries while still proving bucket coverage or
Veriflier telemetry is in the expected state.

The lab validates graceful shutdown rebalancing as well as startup claiming.
When a Monitor exits cleanly, it releases its `jetmon_hosts` row and the
remaining active host rows are redistributed in the same database transaction;
hard failures are still recovered by the normal heartbeat/grace-period path.

## Environment Overrides

| Variable | Default |
| --- | --- |
| `JETMON_SCALE_LAB_PROJECT` | `jetmon-scale-lab` |
| `JETMON_SCALE_LAB_NETWORK` | `jetmon-scale-lab-public` |
| `JETMON_SCALE_LAB_SUBNET` | `93.184.217.0/24` |
| `JETMON_SCALE_LAB_FIXTURE_IP` | `93.184.217.20` |
| `JETMON_SCALE_LAB_SITE_COUNT` | `600` |
| `JETMON_SCALE_LAB_BUCKET_TOTAL` | `12` |
| `MAILPIT_HOST_PORT` | `17125` |
