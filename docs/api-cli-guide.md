# API CLI Guide

`jetmon2 api` is the operator and developer CLI for Jetmon's internal
`/api/v1` API. It wraps common API paths with typed commands, repeatable
payloads, safe local defaults, and output modes for humans or scripts.

Use this guide for practical workflows. Use
[internal-api-reference.md](internal-api-reference.md) for exact endpoint
contracts, payload shapes, pagination, and design rationale.

## Setup

Build the binary and start the local Docker stack:

```bash
make build
cd docker
docker compose up --build -d
cd ..
```

Create a local API key:

```bash
make api-cli-token-create
```

Then either export connection details:

```bash
export JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090}
export JETMON_API_TOKEN=jm_replace_with_the_printed_token
```

Or store local defaults in `~/.config/jetmon2.conf`:

```bash
./bin/jetmon2 local-config init \
  --base-url=http://localhost:8090 \
  --token-file=jetmon2-api-token
./bin/jetmon2 local-config show
./bin/jetmon2 local-config keys
```

Example config:

```conf
base_url = http://localhost:8090
token_file = jetmon2-api-token
auth_policy = same-origin
timeout = 10s
output = json
```

Supported local config keys:

- `base_url`
- `token`
- `token_file`
- `auth_policy`
- `allow_remote`
- `timeout`
- `output`
- `pretty`

`token_file` can be absolute or relative to the config file directory. If the
config stores `token` or `token_file`, the config and token file must be mode
`0600`.

Environment variables override the config file. Command flags override both.

Useful token targets:

```bash
make api-cli-token-list
API_CLI_TOKEN_ID=<id> make api-cli-token-revoke
```

Token creation can be varied with `API_CLI_TOKEN_CONSUMER`,
`API_CLI_TOKEN_SCOPE`, `API_CLI_TOKEN_TTL`, and `API_CLI_TOKEN_CREATED_BY`.

## Safety Model

Every command accepts the common API flags:

```text
--base-url
--token
--auth-policy same-origin|any-origin
--allow-remote
--timeout
--header
--pretty
--output json|table
-v / --verbose
```

Important guardrails:

- JSON is the default output. Use `--pretty` for direct reading and
  `--output table` for stable summaries.
- Automatic `Authorization` and `Idempotency-Key` headers are sent only to the
  configured API origin by default, even when `api request` receives an
  absolute URL.
- Use `--auth-policy any-origin` only for a trusted one-off target. Avoid
  exporting `JETMON_API_AUTH_POLICY=any-origin` unless you really want that
  behavior to persist.
- Custom `--header` values are explicit operator input and can bypass
  same-origin automatic auth protections. Use them only with trusted URLs.
- Verbose mode redacts common sensitive headers, but response bodies are
  printed as returned by the server.
- POST, PUT, PATCH, and DELETE refuse non-local API targets unless
  `--allow-remote` is supplied. Local means `localhost`, `*.localhost`, or a
  loopback IP address. Private LAN hosts still count as remote.
- On remote API targets, `smoke`, `sites bulk-add`, `sites cleanup`, and
  `sites simulate-failure` also require `--batch`.
- Remote cleanup and simulation keep the CLI batch marker check mandatory.
  `--allow-unmarked` is local-only.

List the command catalog at any time:

```bash
JETMON_API_CONFIG=/dev/null ./bin/jetmon2 api commands --output table
```

## Guided Rollout

For containerized v1-to-v2 rollout, run the guided API flow from a standalone
operator `jetmon2` binary:

```bash
./bin/jetmon2 api rollout guided \
  --bucket-min=0 \
  --bucket-max=99 \
  --allow-remote
```

The guided flow:

1. Checks API health and identity.
2. Creates or resumes a rollout session.
3. Runs API-controlled preflight.
4. Runs read-only `HEAD`/`legacy` smoke probes.
5. Seeds v2 side state.
6. Pauses for Systems to stop v1.
7. Runs final reconcile.
8. Activates v2 for the bucket range.
9. Runs post-handoff gates.

Mutating steps require typed phrases, dry-run confirmation tokens, and
idempotency keys. Non-dry-run sessions write a local transcript and resume
state under `logs/api-rollout` by default.

Useful flags:

- `--dry-run`: print the plan without contacting the API.
- `--run-id`: resume a known server-side session.
- `--resume`: continue an interrupted local operator session.
- `--change-ref`: record the ticket/change reference.
- `--rollback`: release an activated v2 range before Systems restarts v1.

Primitive rollout commands:

```bash
./bin/jetmon2 api rollout capabilities --allow-remote
./bin/jetmon2 api rollout preflight --bucket-min=0 --bucket-max=99 --mode=api-controlled --allow-remote
./bin/jetmon2 api rollout smoke --bucket-min=0 --bucket-max=99 --mode=head-legacy --sample-size=100 --read-only --allow-remote
./bin/jetmon2 api rollout seed --bucket-min=0 --bucket-max=99 --dry-run --allow-remote
./bin/jetmon2 api rollout final-reconcile --bucket-min=0 --bucket-max=99 --dry-run --allow-remote
./bin/jetmon2 api rollout activate-buckets --bucket-min=0 --bucket-max=99 --dry-run --allow-remote
./bin/jetmon2 api rollout status --allow-remote
./bin/jetmon2 api rollout bucket-coverage --bucket-min=0 --bucket-max=99 --allow-remote
./bin/jetmon2 api rollout activity-check --bucket-min=0 --bucket-max=99 --since=15m --allow-remote
./bin/jetmon2 api rollout projection-drift --bucket-min=0 --bucket-max=99 --allow-remote
./bin/jetmon2 api rollout release-buckets --bucket-min=0 --bucket-max=99 --dry-run --allow-remote
./bin/jetmon2 api rollout compare-methods --bucket-min=0 --bucket-max=99 --from=head-legacy --to=get-simple --sample-size=100 --allow-remote
./bin/jetmon2 api rollout stage-policy --bucket-min=0 --bucket-max=99 --method=GET --profile=simple_http --size=1000 --dry-run --allow-remote
./bin/jetmon2 api rollout jobs get <job-id> --allow-remote
```

## Health, Identity, And Raw Requests

Check unauthenticated health before debugging tokens:

```bash
./bin/jetmon2 api health --pretty
```

Confirm token identity, consumer, scope, and rate limit:

```bash
./bin/jetmon2 api me --pretty
./bin/jetmon2 api me --verbose --pretty
```

Use `api request` when a route exists but a typed CLI wrapper does not:

```bash
./bin/jetmon2 api request --output table GET '/api/v1/sites?limit=5'
```

POST/PATCH bodies can be literal JSON, a file, or stdin:

```bash
./bin/jetmon2 api request \
  --idempotency-key local-site-12345-create \
  --body '{"blog_id":12345,"monitor_url":"https://example.com","monitor_active":true}' \
  --pretty \
  POST /api/v1/sites
```

```bash
./bin/jetmon2 api request \
  --body-file site-update.json \
  --pretty \
  PATCH /api/v1/sites/12345
```

`api request` reads request and response bodies into memory. Avoid very large
files or unbounded streaming endpoints.

## Site Management

Site API paths use the monitor endpoint row id
(`jetpack_monitor_sites.jetpack_monitor_site_id`). The legacy `blog_id` remains
visible in responses and create/update bodies because WPCOM still uses it as
the site identity.

```bash
./bin/jetmon2 api sites list --limit 20 --output table
./bin/jetmon2 api sites list --monitor-active=true --state-in 'Seems Down,Down' --severity-gte 3 --output table
./bin/jetmon2 api sites get --pretty 12345
```

Create or update a monitored site:

```bash
./bin/jetmon2 api sites create \
  --blog-id 12345 \
  --url https://example.com \
  --monitor-active=true \
  --request-method HEAD \
  --detection-profile legacy \
  --idempotency-key site-12345-create \
  --pretty
./bin/jetmon2 api sites update \
  --url https://example.com/health \
  --request-method GET \
  --detection-profile simple_http \
  --check-keyword Example \
  --forbidden-keyword 'database error' \
  --custom-header 'X-Jetmon-Test: api-cli' \
  --pretty \
  12345
```

Common update flags include `--redirect-policy`, `--timeout-seconds`,
`--check-interval`, `--forbidden-keyword-list`, `--maintenance-start`, and
`--maintenance-end`.

Pause, resume, trigger, and delete:

```bash
./bin/jetmon2 api sites pause --idempotency-key site-12345-pause --pretty 12345
./bin/jetmon2 api sites resume --idempotency-key site-12345-resume --pretty 12345
./bin/jetmon2 api sites trigger-now --idempotency-key site-12345-trigger --pretty 12345
./bin/jetmon2 api sites delete 12345
```

## Batch Test Data

`sites bulk-add` creates deterministic test batches. The default fixture
includes public up, redirect, slow, error, TLS, header, and keyword examples.

```bash
./bin/jetmon2 api sites bulk-add --count 5 --batch local-smoke --dry-run --pretty
./bin/jetmon2 api sites bulk-add --count 5 --batch local-smoke --idempotency-key-prefix local-smoke --pretty
```

The batch label derives deterministic blog IDs and stores an
`X-Jetmon-CLI-Batch` marker. Smoke, simulation, and cleanup commands use that
marker to avoid touching unrelated data.

Use your own source list when needed:

```bash
./bin/jetmon2 api sites bulk-add --source file --file sites.csv --count 10 --batch private-repro --pretty
```

Accepted source formats are newline URLs, CSV with `url` or `monitor_url`, or
JSON objects with fields such as `monitor_url`, `request_method`,
`detection_profile`, `timeout_seconds`, `custom_headers`, keywords, cooldown,
and `check_interval`.

Clean up a batch:

```bash
./bin/jetmon2 api sites cleanup --batch local-smoke --count 5 --output table
```

Cleanup verifies each target still exposes the matching `cli_batch` marker
before deleting it.

## Failure Simulation

`sites simulate-failure` mutates test sites into known failure modes, triggers
checks, polls active events, fetches transitions, and returns non-zero when
assertions fail.

Supported modes are `unreachable`, `http-500`, `http-403`, `redirect`,
`keyword`, `timeout`, and `tls`.

Example:

```bash
./bin/jetmon2 api sites simulate-failure \
  --batch local-smoke \
  --count 1 \
  --create-missing \
  --mode http-500 \
  --wait 15s \
  --pretty
```

Assertion example:

```bash
./bin/jetmon2 api sites simulate-failure \
  --batch local-smoke \
  --mode http-500 \
  --wait 30s \
  --expect-event-state 'Seems Down' \
  --expect-event-severity 3 \
  --require-transition \
  --expect-transition-reason opened \
  --pretty
```

When plain Docker Compose is running, the command can use the Docker-internal
`api-fixture` service for deterministic failures. Target-safety-enabled Monitor
checks block that default private Docker hostname. Use
`make api-cli-public-fixture-validate` for the standard isolated fixture
network when target safety should remain enabled.

## Events

Events are the API source of truth for incident state.

```bash
./bin/jetmon2 api events list --active=true --output table 12345
./bin/jetmon2 api events list --state 'Seems Down' --limit 10 --pretty 12345
./bin/jetmon2 api events get --site-id 12345 --pretty 98765
./bin/jetmon2 api events transitions --output table 12345 98765
```

Close an event with an explicit reason and note:

```bash
./bin/jetmon2 api events close \
  --reason manual_override \
  --note 'Confirmed maintenance outside scheduled window' \
  --idempotency-key event-98765-close \
  --pretty \
  12345 98765
```

## Webhooks

Webhooks receive HMAC-signed POSTs for matching event transitions.

The Docker-local fixture receiver is available at
`http://api-fixture:8091/webhook` from inside Compose. From the host, inspect
recorded deliveries at `http://localhost:18091/webhook/requests` or `DELETE`
the same path to clear them. Add `?secret=<webhook-secret>` to the receiver URL
when the fixture should verify `X-Jetmon-Signature`.

Webhook secrets returned by create/rotate responses are shown once; treat the
JSON output like a credential.

```bash
./bin/jetmon2 api webhooks create \
  --url https://receiver.example.test/jetmon \
  --active=true \
  --event event.opened,event.severity_changed,event.closed \
  --site-id 12345 \
  --state 'Down,Seems Down' \
  --idempotency-key webhook-local-create \
  --pretty
```

Useful follow-up commands:

```bash
./bin/jetmon2 api webhooks list --output table
./bin/jetmon2 api webhooks deliveries --status failed --output table 77
./bin/jetmon2 api webhooks retry --idempotency-key webhook-77-delivery-555-retry --pretty 77 555
./bin/jetmon2 api webhooks rotate-secret --idempotency-key webhook-77-rotate --pretty 77
```

## Alert Contacts

Alert contacts are managed delivery channels backed by the same transition
source as webhooks. Supported transports:

- `email`
- `pagerduty`
- `slack`
- `teams`

Create a contact:

```bash
./bin/jetmon2 api alert-contacts create \
  --label 'Local smoke email' \
  --transport email \
  --address alerts@example.test \
  --active=true \
  --min-severity SeemsDown \
  --max-per-hour 10 \
  --idempotency-key alert-email-create \
  --pretty
```

Exercise send-test and inspect delivery rows:

```bash
./bin/jetmon2 api alert-contacts test --idempotency-key alert-12-test --pretty 12
./bin/jetmon2 api alert-contacts deliveries --status failed --output table 12
./bin/jetmon2 api alert-contacts retry --idempotency-key alert-12-delivery-9001-retry --pretty 12 9001
```

PagerDuty integration keys and Slack/Teams webhook URLs are credentials. The
CLI does not print request bodies in verbose mode, but shell history and saved
JSON output can still retain values supplied with flags.

## Smoke Workflows

Run the compact API smoke workflow:

```bash
./bin/jetmon2 api smoke --batch local-smoke --pretty
```

Run the webhook exercise too:

```bash
./bin/jetmon2 api smoke --batch local-webhook --exercise webhook --pretty
```

The webhook exercise is Docker-local. It refuses non-local API targets even
with `--allow-remote`, and the fixture polling URL must resolve to localhost or
a loopback IP because the CLI clears and polls that endpoint directly.

Makefile smoke target:

```bash
JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090} \
JETMON_API_TOKEN=jm_replace_with_the_printed_token \
make api-cli-smoke
```

Fuller local validation:

```bash
JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090} \
JETMON_API_TOKEN=jm_replace_with_the_printed_token \
make api-cli-validate
```

`api-cli-validate` checks health and identity, exercises the generic request
path, dry-runs batch creation, runs smoke, tests webhook delivery/signature,
runs deterministic failure simulation, and cleans up. Use
`API_VALIDATE_BATCH`, `API_VALIDATE_MODE`, `API_VALIDATE_WAIT`,
`API_VALIDATE_WEBHOOK_WAIT`, `API_VALIDATE_COUNT`,
`API_VALIDATE_SKIP_WEBHOOK=1`, and `API_VALIDATE_SKIP_FAILURE=1` to vary it.

For delivery/failure validation with target safety enabled:

```bash
make api-cli-public-fixture-validate
```

That target starts an isolated Docker stack with WPCOM disabled, sends email
only to Mailpit, and puts the deterministic fixture on a public-looking
Docker-internal address.

## Automation Notes

- Use `--idempotency-key` or `--idempotency-key-prefix` for create, close,
  retry, trigger, and test actions that scripts may repeat.
- Use JSON output for scripts. Use table output for human-readable status.
- Use `--batch` and `sites cleanup` for disposable test data.
- Use `--verbose` when debugging auth, rate limits, idempotency, or unexpected
  server errors. Header values are redacted, but response bodies are not.
- Treat tokens as local secrets. Do not commit exported tokens, shell history
  snippets, or generated local config containing credentials.
