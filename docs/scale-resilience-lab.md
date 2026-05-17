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
  dynamic bucket ownership enabled.
- Uses only local Docker containers for MySQL, Monitors, Verifliers, StatsD,
  Mailpit, and the HTTP fixture.
- Uses a dedicated Docker network with a public-looking fixture address
  (`93.184.217.20` by default). Target safety stays enabled, but fixture
  traffic stays inside the Docker host.
- Does not create webhooks or alert contacts.
- Does not contact WPCOM or change production systems.

## Flow

The lab script:

1. Starts one Monitor and three Verifliers.
2. Seeds fixture-backed sites directly into the isolated Docker database across
   the configured bucket space, avoiding API rate-limit tuning in the product.
3. Waits for healthy dynamic bucket coverage and recent check activity with one
   Monitor.
4. Adds a second Monitor and verifies bucket redistribution.
5. Adds two more Monitors and verifies four-way redistribution.
6. Stops one Monitor and then a second Monitor, verifying the survivors reclaim
   bucket coverage and keep checking every site after each change.
7. Recovers stopped Monitors and verifies four-way coverage again.
8. Stops one Veriflier and then a second Veriflier, verifying fresh agent
   telemetry and fleet dashboard snapshots report the degraded Veriflier state.
9. Recovers Verifliers and captures the final fleet snapshot.

JSON outputs are written under `logs/scale-resilience-lab/`.

## Environment Overrides

| Variable | Default |
| --- | --- |
| `JETMON_SCALE_LAB_PROJECT` | `jetmon-scale-lab` |
| `JETMON_SCALE_LAB_NETWORK` | `jetmon-scale-lab-public` |
| `JETMON_SCALE_LAB_SUBNET` | `93.184.217.0/24` |
| `JETMON_SCALE_LAB_FIXTURE_IP` | `93.184.217.20` |
| `JETMON_SCALE_LAB_SITE_COUNT` | `600` |
| `JETMON_SCALE_LAB_BUCKET_TOTAL` | `12` |
