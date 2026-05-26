# V2 Soak Lab

The v2 soak lab is an internal-only Docker run for checking sustained normal
operation beyond short smoke tests. It runs four Monitors and three Verifliers
against fixture-backed sites inside the Docker host.

Run it from a host that can spend temporary CPU and database resources:

```bash
make v2-soak-lab
```

Clean up the isolated project and volumes:

```bash
make v2-soak-lab-clean
```

## Safety Model

- Uses Docker project `jetmon-v2-soak-lab` by default.
- Writes an ignored local `config/config.json` with
  `WPCOM_NOTIFY_ENABLE=false`, `EMAIL_TRANSPORT=stub`, `DEBUG_PORT=0`, dynamic
  bucket ownership enabled, and `DELIVERY_OWNER_HOST` pinned to the first
  Monitor.
- Uses only local Docker containers for MySQL, Monitors, Verifliers, StatsD,
  Mailpit, and the HTTP fixture.
- Uses a dedicated Docker network with a public-looking fixture address
  (`93.184.218.20` by default). Target safety stays enabled, but fixture
  traffic stays inside the Docker host.
- Does not create webhooks or alert contacts.
- Asserts that WPCOM audit rows, webhooks, alert contacts, and Mailpit messages
  remain at zero during the run.
- Cleans up the isolated Docker project after a successful run unless
  `JETMON_SOAK_LAB_KEEP_RUNNING=1` is set.

## Flow

The lab script:

1. Builds every lab image, including each named Monitor service image.
2. Starts four Monitors and three Verifliers.
3. Seeds fixture-backed sites directly into the isolated Docker database across
   the configured bucket space.
4. Waits for healthy dynamic bucket coverage, green fleet status, and at least
   one completed check per fixture site in the Monitor logs.
5. Runs a timed soak loop and samples once per full interval.

Each sample requires:

- four active Monitor host rows;
- green fleet summary, bucket coverage, and Veriflier status;
- three fresh Veriflier agents;
- at least one completed check per fixture site since the previous sample;
- legacy projection freshness within the configured maximum age;
- no open events;
- no stale Monitor process-health rows;
- no WPCOM audit rows, alert contacts, webhooks, or Mailpit messages.

JSON fleet snapshots and the seed SQL are written under `logs/v2-soak-lab/`.

## Environment Overrides

| Variable | Default |
| --- | --- |
| `JETMON_SOAK_LAB_PROJECT` | `jetmon-v2-soak-lab` |
| `JETMON_SOAK_LAB_NETWORK` | `jetmon-v2-soak-lab-public` |
| `JETMON_SOAK_LAB_SUBNET` | `93.184.218.0/24` |
| `JETMON_SOAK_LAB_FIXTURE_IP` | `93.184.218.20` |
| `JETMON_SOAK_LAB_SITE_COUNT` | `360` |
| `JETMON_SOAK_LAB_BUCKET_TOTAL` | `12` |
| `JETMON_SOAK_LAB_DURATION_SEC` | `1800` |
| `JETMON_SOAK_LAB_WARMUP_SEC` | `120` |
| `JETMON_SOAK_LAB_SAMPLE_INTERVAL_SEC` | `60` |
| `JETMON_SOAK_LAB_MAX_LAST_CHECKED_AGE_SEC` | `960` |
| `JETMON_SOAK_LAB_KEEP_RUNNING` | `0` |
