# Production Veriflier Compose

Production Verifliers run in a different environment from production Monitors.
Monitor hosts are deployed through TeamCity and already have local StatsD
proxies. Veriflier VPS hosts do not, so the Veriflier Compose stack includes
StatsD and Graphite locally.

Use [../docker/docker-compose.veriflier-prod.yml](../docker/docker-compose.veriflier-prod.yml)
as the deployment template.

## Service Shape

The production Veriflier VPS stack has two containers:

- `veriflier`: Go Veriflier service, reachable by Monitors on port `7803`.
- `statsd`: `graphiteapp/graphite-statsd`, reachable only on the internal
  Docker network for StatsD UDP and exposed through Graphite HTTP for Grafana.

Veriflier sends metrics to `STATSD_ADDR=statsd:8125`. Set `JETMON_HOSTNAME`
when the Graphite path should be stable and different from the container
runtime hostname. Use a stable low-cardinality value such as
`<region>.<vantage>` or another Grafana-approved Veriflier grouping; do not use
container IDs, release SHAs, ports, or random suffixes. The StatsD UDP port is
not published on the host. Graphite HTTP is published on
`GRAPHITE_BIND_ADDR:GRAPHITE_HOST_PORT`; set `GRAPHITE_BIND_ADDR` to a
private/VPN/firewalled address that central Grafana can reach.

The Compose file mounts
[../docker/graphite-storage-schemas.conf](../docker/graphite-storage-schemas.conf)
so Jetmon metrics use the requested retention schedule:
`10s:6h, 1m:7d, 10m:5y`.

## Setup

```bash
cd docker
cp veriflier-prod.env-sample .env
```

Edit `.env` on the VPS:

```text
VERIFLIER_IMAGE=ghcr.io/automattic/veriflier:<tag>
VERIFLIER_AUTH_TOKEN=<secret shared with Monitors>
VERIFLIER_VANTAGE_ID=do-nyc3-1
VERIFLIER_REGION=nyc3
VERIFLIER_PROVIDER=digitalocean
JETMON_HOSTNAME=nyc3.do-nyc3-1
GRAPHITE_BIND_ADDR=<private-or-vpn-address>
GRAPHITE_HOST_PORT=8088
```

Start the stack:

```bash
docker compose -f docker-compose.veriflier-prod.yml up -d
docker compose -f docker-compose.veriflier-prod.yml ps
docker compose -f docker-compose.veriflier-prod.yml logs -f veriflier
```

Health checks:

```bash
curl -fsS http://127.0.0.1:7803/v2/status
curl -fsS http://127.0.0.1:8088/
```

If `GRAPHITE_BIND_ADDR` is not loopback, also confirm the central Grafana host
can reach `http://<GRAPHITE_BIND_ADDR>:<GRAPHITE_HOST_PORT>/`.

StatsD is UDP, so the normal container health checks cannot prove metric
ingestion. Because this Compose stack owns both StatsD and Graphite, run the
optional smoke profile after startup or after metrics config changes:

```bash
docker compose -f docker-compose.veriflier-prod.yml --profile smoke run --rm metrics-smoke
```

That one-shot service sends a single low-cardinality StatsD counter and queries
Graphite for the resulting series. Passing this check validates the local
Veriflier metrics path without exposing the StatsD UDP port on the host.

## Security Notes

- Keep `VERIFLIER_AUTH_TOKEN` only in the VPS-local `.env` file or an
  equivalent secret store. Do not commit it.
- Publish Graphite only on a private/VPN/firewalled interface. Do not expose it
  broadly to the public internet.
- Leave StatsD UDP unexposed; only the Veriflier container needs to send to it.
- Keep `VERIFLIER_ENABLE_LEGACY_HTTP=false` unless an emergency compatibility
  test explicitly requires the legacy endpoints.

## Operations

Use an immutable Veriflier image tag for upgrades:

```bash
sed -i 's#^VERIFLIER_IMAGE=.*#VERIFLIER_IMAGE=ghcr.io/automattic/veriflier:<new-tag>#' .env
docker compose -f docker-compose.veriflier-prod.yml pull
docker compose -f docker-compose.veriflier-prod.yml up -d
```

Graphite data is stored in the `statsd-graphite-storage` Docker volume and
survives container recreates. Remove that volume only when intentionally
discarding historical local metrics for the Veriflier host.
