# API CLI Feature Guide

`jetmon2 api` is the local operator and developer interface for Jetmon's
internal `/api/v1` API. It wraps the common API paths with typed commands,
repeatable request payloads, Docker-local defaults, and output modes that work
for both humans and scripts.

Use this guide for day-to-day examples. Use [`../API.md`](../API.md) as the
endpoint contract reference when you need exact response shapes, request
schemas, pagination semantics, or design rationale.

## Setup

Build the local binary:

```bash
make build
```

Start Docker and create an API key inside the `jetmon` container:

```bash
cd docker
docker compose up --build -d
docker compose exec jetmon ./jetmon2 keys create --consumer api-cli --scope admin --created-by docker-local
cd ..
```

Point the CLI at the Docker-local API and token:

```bash
export JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090}
export JETMON_API_TOKEN=jm_replace_with_the_printed_token
```

Every command also accepts `--base-url`, `--token`, `--timeout`, `--header`,
`--pretty`, `--output table`, `-v`, and `--verbose`. JSON is the default output
for automation. Use `--pretty` when reading JSON directly and `--output table`
for stable summary tables.

## Health and Identity

Use `health` before authenticating anything. It checks the API and database
health endpoint.

```bash
./bin/jetmon2 api health --pretty
```

Use `me` to confirm the token, consumer name, scope, and rate limit seen by the
API server.

```bash
./bin/jetmon2 api me --pretty
```

Verbose mode prints request and response headers to stderr, which is useful
when debugging auth, rate limiting, or idempotency:

```bash
./bin/jetmon2 api me --verbose --pretty
```

## Request Escape Hatch

Use `api request` when a route exists but a typed CLI wrapper does not yet.

```bash
./bin/jetmon2 api request GET '/api/v1/sites?limit=5' --output table
```

POST and PATCH requests can take literal JSON, a file, or stdin:

```bash
./bin/jetmon2 api request POST /api/v1/sites \
  --idempotency-key local-site-12345-create \
  --body '{"blog_id":12345,"monitor_url":"https://example.com","monitor_active":true}' \
  --pretty
```

```bash
./bin/jetmon2 api request PATCH /api/v1/sites/12345 \
  --body-file site-update.json \
  --pretty
```

## Site Management

Sites are keyed by the existing `blog_id`. The typed site commands cover list,
get, create, update, delete, pause, resume, and trigger-now.

```bash
./bin/jetmon2 api sites list --limit 20 --output table
./bin/jetmon2 api sites list --monitor-active=true --state-in 'Seems Down,Down' --severity-gte 3 --output table
./bin/jetmon2 api sites get 12345 --pretty
```

Create a monitored site with explicit per-site check behavior:

```bash
./bin/jetmon2 api sites create \
  --blog-id 12345 \
  --url https://example.com \
  --monitor-active=true \
  --redirect-policy follow \
  --timeout-seconds 5 \
  --check-interval 1 \
  --idempotency-key site-12345-create \
  --pretty
```

Update a site when testing redirects, keyword checks, custom headers, or
maintenance windows:

```bash
./bin/jetmon2 api sites update 12345 \
  --url https://example.com/health \
  --check-keyword Example \
  --custom-header 'X-Jetmon-Test: api-cli' \
  --maintenance-start 2026-04-28T18:00:00Z \
  --maintenance-end 2026-04-28T19:00:00Z \
  --pretty
```

Pause, resume, and run an immediate check:

```bash
./bin/jetmon2 api sites pause 12345 --idempotency-key site-12345-pause --pretty
./bin/jetmon2 api sites resume 12345 --idempotency-key site-12345-resume --pretty
./bin/jetmon2 api sites trigger-now 12345 --idempotency-key site-12345-trigger --pretty
```

Delete disposable sites:

```bash
./bin/jetmon2 api sites delete 12345
```

## Batch Test Sites

`sites bulk-add` creates bounded, repeatable local test data. The default source
is the checked-in fixture of public URLs with up, redirect, slow, error, TLS,
header, and keyword-check examples.

Preview the payloads:

```bash
./bin/jetmon2 api sites bulk-add --count 5 --batch local-smoke --dry-run --pretty
```

Create the batch:

```bash
./bin/jetmon2 api sites bulk-add \
  --count 5 \
  --batch local-smoke \
  --idempotency-key-prefix local-smoke \
  --pretty
```

The batch label derives deterministic blog IDs and stores an
`X-Jetmon-CLI-Batch` custom header marker so later smoke, simulation, and
cleanup commands can target only CLI-created data.

Use your own source list when needed:

```bash
./bin/jetmon2 api sites bulk-add --source file --file sites.csv --count 10 --batch private-repro --pretty
```

Accepted source formats are newline URLs, CSV with a `url` or `monitor_url`
column, or JSON objects using fields such as `monitor_url`, `check_keyword`,
`redirect_policy`, `timeout_seconds`, `custom_headers`, `alert_cooldown_minutes`,
and `check_interval`.

Clean up a batch after testing:

```bash
./bin/jetmon2 api sites cleanup --batch local-smoke --count 5 --output table
```

## Events and Transitions

Events are the API source of truth for incident state. Use event commands to
list active incidents for a site, inspect an event, fetch transition history,
and manually close false alarms or operator-resolved incidents.

```bash
./bin/jetmon2 api events list 12345 --active=true --output table
./bin/jetmon2 api events list 12345 --state 'Seems Down' --limit 10 --pretty
./bin/jetmon2 api events get --site-id 12345 98765 --pretty
./bin/jetmon2 api events transitions 12345 98765 --output table
```

Close an event with an explicit reason and note:

```bash
./bin/jetmon2 api events close 12345 98765 \
  --reason manual_override \
  --note 'Confirmed maintenance outside scheduled window' \
  --idempotency-key event-98765-close \
  --pretty
```

## Webhooks

Webhooks receive HMAC-signed POSTs for matching event transitions. The CLI can
create, update, rotate secrets, inspect deliveries, and retry failed delivery
rows.

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

```bash
./bin/jetmon2 api webhooks list --output table
./bin/jetmon2 api webhooks get 77 --pretty
./bin/jetmon2 api webhooks deliveries 77 --status failed --output table
./bin/jetmon2 api webhooks retry 77 555 --idempotency-key webhook-77-delivery-555-retry --pretty
./bin/jetmon2 api webhooks rotate-secret 77 --idempotency-key webhook-77-rotate --pretty
```

Update filters without rebuilding the whole object:

```bash
./bin/jetmon2 api webhooks update 77 --clear-sites --state Down --pretty
```

## Alert Contacts

Alert contacts are managed delivery channels backed by the same transition
source as webhooks. Supported transports are `email`, `pagerduty`, `slack`, and
`teams`.

Create an email contact:

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

Create a Slack contact:

```bash
./bin/jetmon2 api alert-contacts create \
  --label 'Local Slack' \
  --transport slack \
  --webhook-url https://hooks.slack.com/services/example \
  --site-id 12345 \
  --min-severity Down \
  --pretty
```

Exercise the send-test path and inspect delivery rows:

```bash
./bin/jetmon2 api alert-contacts test 12 --idempotency-key alert-12-test --pretty
./bin/jetmon2 api alert-contacts deliveries 12 --status failed --output table
./bin/jetmon2 api alert-contacts retry 12 9001 --idempotency-key alert-12-delivery-9001-retry --pretty
```

Use `--destination` for raw transport-specific JSON when a shortcut flag does
not fit a test case:

```bash
./bin/jetmon2 api alert-contacts create \
  --label 'Raw destination example' \
  --transport teams \
  --destination '{"webhook_url":"https://example.test/teams"}' \
  --pretty
```

## Smoke Workflows

`api smoke` runs a compact end-to-end sanity pass against Docker-local API
components: health, auth identity, site creation, trigger-now, event listing,
alert-contact creation, alert-contact send-test, and best-effort cleanup.

```bash
./bin/jetmon2 api smoke --batch local-smoke --pretty
```

The Makefile target builds the binary first and runs the standard smoke path:

```bash
JETMON_API_URL=http://localhost:${API_HOST_PORT:-8090} \
JETMON_API_TOKEN=jm_replace_with_the_printed_token \
make api-cli-smoke
```

## Failure Simulation

`sites simulate-failure` mutates one or more sites into a known failure mode,
optionally creates missing test sites, triggers immediate checks, polls active
events, fetches transitions, and returns non-zero when a site fails the workflow
or an assertion does not match.

Supported modes are `unreachable`, `http-500`, `http-403`, `redirect`,
`keyword`, `timeout`, and `tls`.

```bash
./bin/jetmon2 api sites simulate-failure \
  --batch local-smoke \
  --count 1 \
  --create-missing \
  --mode http-500 \
  --wait 15s \
  --pretty
```

When Docker Compose is running, the command probes
`http://localhost:18091/health` and uses the Docker-internal `api-fixture`
service for deterministic HTTP status, redirect, keyword, timeout, and TLS
cases. Force public endpoint fallbacks with `--fixture-url=off`.

Use assertions when a CI or rehearsal run should fail unless the expected API
state appears before the wait window expires:

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

Target explicit site IDs instead of a batch:

```bash
./bin/jetmon2 api sites simulate-failure \
  --site-id 12345 \
  --site-id 12346 \
  --mode timeout \
  --wait 20s \
  --output table
```

## Automation Notes

- Prefer `--idempotency-key` or `--idempotency-key-prefix` for create, close,
  retry, trigger, and test actions that scripts may repeat.
- Use JSON output for scripts; use table output only for human-readable status.
- Use `--batch` and `sites cleanup` for disposable data so local runs do not
  touch unrelated sites.
- Use `--verbose` when debugging auth, rate limits, idempotency behavior, or
  unexpected server errors.
- Treat tokens as local secrets. Do not commit exported tokens, shell history
  snippets, or generated local config containing credentials.
