# Rollout Docker Lab

The Docker rollout lab rehearses the API-controlled v2 rollout flow on a single
Docker host without touching production systems, WPCOM, or real customer sites.
It complements the VM lab: the VM lab validates systemd and SSH host handoff,
while this lab validates the container/API rollout sequence and staged check
policy migration.

Run it from a host that can spend local Docker resources:

```bash
make rollout-docker-lab
```

Clean up the isolated project and volumes:

```bash
make rollout-docker-lab-clean
```

## Safety Model

- Uses Docker project `jetmon-rollout-lab` by default.
- Writes an ignored local `config/config.json` with
  `WPCOM_NOTIFY_ENABLE=false`, `EMAIL_TRANSPORT=stub`,
  `ROLLOUT_MODE=api-controlled`, and `PEER_OFFLINE_LIMIT=1`.
- Uses only local Docker containers for MySQL, Monitor, Veriflier, StatsD,
  Mailpit, and the HTTP fixture.
- Uses a dedicated Docker network with a public-looking fixture address
  (`93.184.216.20` by default). This keeps Jetmon target-safety checks enabled
  while fixture traffic stays inside the Docker host.
- Does not create webhooks or alert contacts.
- Does not force a Prometheus reload or change production state.

## Flow

The lab script:

1. Starts the Docker environment with the rollout-lab override.
2. Creates an admin API key inside the isolated database.
3. Seeds fixture-backed sites in bucket `0` with HEAD/legacy policy.
4. Runs rollout capabilities, preflight, and read-only HEAD/legacy smoke.
5. Plans and executes seed, final reconcile, and bucket activation.
6. Waits for the Monitor to check every seeded site.
7. Verifies bucket coverage, activity, and projection drift gates.
8. Compares HEAD/legacy to GET/simple_http.
9. Stages all sites to GET/simple_http.
10. Compares GET/simple_http to GET/full.
11. Stages all sites to GET/full.
12. Releases the bucket range back to standby.

Outputs are written under `logs/rollout-docker-lab/`. The site fixture JSON is
staged under `stats/rollout-docker-lab/` so the Monitor container can read it
through the existing `/jetmon/stats` mount without adding a runtime log mount.

## Environment Overrides

| Variable | Default |
| --- | --- |
| `JETMON_ROLLOUT_DOCKER_PROJECT` | `jetmon-rollout-lab` |
| `JETMON_ROLLOUT_DOCKER_NETWORK` | `jetmon-rollout-lab-public` |
| `JETMON_ROLLOUT_DOCKER_SUBNET` | `93.184.216.0/24` |
| `JETMON_ROLLOUT_DOCKER_FIXTURE_IP` | `93.184.216.20` |
| `JETMON_ROLLOUT_DOCKER_SITE_COUNT` | `40` |
| `JETMON_ROLLOUT_DOCKER_BUCKET_MIN` | `0` |
| `JETMON_ROLLOUT_DOCKER_BUCKET_MAX` | `0` |
| `JETMON_ROLLOUT_DOCKER_RUN_ID` | timestamped lab run id |
| `JETMON_ROLLOUT_DOCKER_CHANGE_REF` | `overnight-rollout-docker-lab` |
