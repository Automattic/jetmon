# Development And Rehearsal Guide

Use this for local development, API smoke testing, and non-production labs. Use
[operations-guide.md](operations-guide.md) for production runtime care.

## Local Loop

Docker Compose is the fastest path:

```bash
cd docker
cp .env-sample .env
docker compose up --build -d
```

Build and test from the repository root:

```bash
make all
make test
make test-race
make lint
```

Manual non-Compose run:

```bash
cp config/config-sample.json config/config.json
./bin/jetmon2 schema ensure
./bin/jetmon2 validate-config
./bin/jetmon2
```

`make generate` is intentionally separate. It requires `protoc` and Go protobuf
plugins; production Veriflier traffic uses JSON/HTTP.

## API Smoke

```bash
make build
make api-cli-token-create

export JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090}
export JETMON_API_TOKEN=jm_replace_with_the_printed_token

./bin/jetmon2 api health --pretty
./bin/jetmon2 api me --pretty
./bin/jetmon2 api commands --output table
make api-cli-smoke
```

Use `api request` for routes without typed helpers:

```bash
./bin/jetmon2 api request GET '/api/v1/sites?limit=5'
```

## Lab Safety

Run labs only against local fixtures, uptime-bench fixtures, or approved
canaries. Do not change deployed services, support hosts, databases, provider
state, fleet config, or runtime config while capacity tests are active unless
Chris explicitly approves it.

Long raw benchmark reports belong in the sibling `uptime-bench` repo.

## Lab Targets

| Target | Command | Proves |
| --- | --- | --- |
| Docker rollout | `make rollout-docker-lab` | API-guided rollout and rollback in local containers. |
| VM rollout | `make rollout-vm-lab-smoke` | Production-shaped systemd/KVM rollout rehearsal. |
| Scale resilience | `make scale-resilience-lab` | Dynamic ownership, host loss, DB disruption behavior. |
| Soak | `make v2-soak-lab` | Sustained low-write operation without outbound side effects. |
| API fixture safety | `make api-cli-public-fixture-validate` | Probe-safety behavior against controlled fixtures. |

`make v2-soak-lab` validates Monitor completion summaries plus the coarse
legacy freshness projection. It should not require every healthy probe to write
`jetpack_monitor_site_runtime` or `jetpack_monitor_check_history`.

VM helpers include `rollout-vm-lab-doctor`, `rollout-vm-lab-prepare`,
`rollout-vm-lab-execute-smoke`, failure/resume smoke targets, and snapshot smoke
targets. Set `ROLLOUT_VM_LAB_HOST`, `ROLLOUT_VM_LAB_SSH`, and
`ROLLOUT_VM_LAB_SNAPSHOT` as needed.

## Rehearsal Evidence

Record:

- commit SHA and image tags;
- config fingerprint;
- schema version;
- Veriflier quorum report;
- rollout session/job IDs;
- command transcript;
- canary file;
- rollback result;
- known gaps and owner.
