# API CLI Roadmap

Status: active on `feature/api-cli`.

This roadmap tracks a local developer/operator CLI for exercising the internal
Jetmon `/api/v1` surface without remembering endpoint paths, auth headers, and
payload shapes by hand. The CLI should make local Docker testing repeatable,
but it should not become a generic `curl` clone.

## P0 - Request Foundation

- [x] Add `jetmon2 api health` for unauthenticated API/database health checks.
- [x] Add `jetmon2 api me` for validating Bearer-token auth and API-key identity.
- [x] Add `jetmon2 api request <method> <path-or-url>` as the escape hatch for
  newly-added routes before typed commands exist.
- [x] Read `JETMON_API_URL` and `JETMON_API_TOKEN`, with Docker-local defaults.
- [x] Add `-v` / `--verbose` to print full request and response headers for local
  debugging.
- [x] Support request bodies from `--body`, `--body-file`, and stdin, plus
  `--idempotency-key` for POST retry testing.

## P1 - Typed Resource Commands

- [x] Add `sites list|get|create|update|delete|pause|resume|trigger-now`.
- [x] Add `events list|get|transitions|close`.
- [x] Add `webhooks list|get|create|update|delete|rotate-secret|deliveries|retry`.
- [x] Add `alert-contacts list|get|create|update|delete|test|deliveries|retry`.
- [x] Keep typed command payloads close to the OpenAPI component schemas or shared
  request structs so CLI examples do not drift from the server contract.
- [x] Add `sites bulk-add --count <n>` for creating a bounded batch of real
  monitored URLs for local testing. Support `--source fixture|file|stdin` so the
  default is repeatable but operators can supply their own CSV/JSON/newline
  list without recompiling the CLI.
- [x] Add a curated local fixture of real public URLs with mixed behavior:
  always-up examples, redirects, slow responses, client/server error responses,
  TLS edge cases, and keyword-check candidates. Keep the fixture small,
  documented, and safe for local-only test data generation.

## P2 - Local Smoke Workflows

- [x] Add `jetmon2 api smoke` to run a local end-to-end sanity pass against Docker:
  health, auth, create a site, trigger a check, read events, and exercise a
  webhook or alert-contact test path.
- [x] Add `jetmon2 api sites simulate-failure` to intentionally mutate one or
  more CLI-created test sites into known failure states, trigger checks, and
  show the resulting event IDs and transitions.
- [x] Support targeted failure modes for simulation: unreachable host, HTTP 500,
  HTTP 403, redirect-policy failure, keyword mismatch, timeout/slow response,
  and TLS/certificate failure.
- [x] Track CLI-created test-site batches with a stable label or metadata marker
  so smoke tests and failure simulations can operate on `--batch <id>`,
  `--count <n>`, or explicit site IDs without touching unrelated local data.
- [x] Add cleanup behavior for resources created by smoke runs.
- [x] Return non-zero exit codes and concise failure summaries suitable for CI.

## Test Site Source Ideas

- **Implemented path:** Use a curated checked-in fixture plus operator-supplied
  file/stdin imports for repeatable test-site creation. Use real public
  endpoints for network realism, and use the Docker-local `api-fixture` service
  for deterministic event assertions. For larger `--count` values, cycle
  through the source list with varied site settings instead of inventing fake
  public domains.
- **Curated fixture:** Check in a small `docs/testdata` or `internal/testdata`
  source list with public endpoints selected for deterministic behavior. This
  should be the default because it keeps local test runs repeatable.
- **Operator-supplied file/stdin:** Accept newline, CSV, or JSON site lists so
  developers can test with a private list of real customer-like domains without
  committing those domains to the repo.
- **Docker failure fixture:** The local `api-fixture` service provides the most
  deterministic failure simulation. Real public sites remain useful for network
  realism, but local fixture endpoints are better for asserting exact event
  transitions.
- **Generated variants:** For `--count` larger than the curated fixture, cycle
  through the source list with deterministic suffix metadata and varied
  per-site settings: redirect policy, keyword, timeout, custom headers, and
  check interval. Do not invent nonexistent public domains as "real" sites.
- **External top-site lists:** If broader realism is needed, allow importing an
  operator-downloaded ranked domain list. Keep download/fetch outside the CLI at
  first so local tests remain reproducible and do not depend on third-party
  availability.

## P3 - Output Ergonomics

- [x] Add stable table output for list commands while keeping JSON as the default
  automation-friendly format.
- [x] Add `--pretty` for formatted JSON and preserve raw JSON for scripts.
- [x] Add examples to `API.md`, Docker docs, and the v1-to-v2 rollout rehearsal
  docs once the command shape has stabilized.

## P4 - Hardening and Repeatability

- [x] Re-run a fresh `docker compose up --build -d` and API CLI smoke pass after
  Docker bridge networking is healthy, so the branch is verified through the
  normal container entrypoint instead of only the host-run binary.
- [x] Add `jetmon2 api sites cleanup --batch <id>` for removing deterministic
  CLI-created site batches after smoke, bulk-add, and failure simulation runs.
- [x] Add a one-command `make api-cli-smoke` entrypoint for the documented local
  smoke path.
- [x] Add a deterministic Docker-local failure fixture service for response
  codes, redirects, keyword mismatch, slow responses, and TLS edge cases.
- [x] Teach failure simulation to prefer the Docker-local fixture when it is
  available, while retaining public endpoint fallback behavior.
- [x] Add deterministic failure-simulation assertions for exact
  event/transition behavior without depending on public endpoint timing.

## P5 - Feature Documentation

- [x] Add a feature guide with setup instructions and examples for health,
  generic requests, site management, batch test data, events, webhooks, alert
  contacts, smoke runs, failure simulation, cleanup, and automation patterns.

## P6 - Operator Ergonomics and Safety

- [x] Allow API CLI flags before or after positional arguments so examples like
  `sites get 123 --pretty` work the way operators naturally type them.
- [ ] Add batch ownership safety checks for destructive or mutating batch
  workflows. `sites cleanup --batch` and `sites simulate-failure --batch`
  should verify the target still belongs to the requested CLI batch unless the
  operator explicitly opts out.
- [x] Add a reproducible documentation/live validation target for the API CLI
  feature guide and Docker-local smoke path.
- [ ] Improve table output for workflow commands with event IDs, states,
  severities, transition counts, trigger results, and cleanup status.
- [ ] Add shell completion or richer command discovery for the expanded
  `jetmon2 api` command tree.
- [ ] Extend `api-fixture` into a local webhook receiver so smoke tests can
  verify outbound webhook delivery and signing behavior end-to-end.
- [ ] Add a local API-token convenience target or wrapper for creating and
  revoking Docker-local API CLI tokens during rehearsals.

## Completed

- [x] 2026-04-28: Created the `feature/api-cli` branch and initial roadmap.
- [x] 2026-04-28: Added the `jetmon2 api` command group with `health`, `me`,
  and generic `request` subcommands.
- [x] 2026-04-28: Added local defaults, Bearer-token auth, repeatable custom
  headers, idempotency-key support, request body helpers, JSON pretty printing,
  and verbose request/response header logging.
- [x] 2026-04-28: Added focused tests for URL resolution, auth/idempotency
  headers, verbose output, pretty JSON output, and HTTP error handling.
- [x] 2026-04-28: Added typed `jetmon2 api sites`
  `list|get|create|update|delete|pause|resume|trigger-now` commands with
  query flags, typed create/update payload builders, idempotency support for
  POST actions, and focused helper tests.
- [x] 2026-04-28: Added typed `jetmon2 api events`
  `list|get|transitions|close` commands with site-scoped list/transition/close
  paths, direct or site-scoped event lookup, close payload flags, idempotency
  support, and focused path/body tests.
- [x] 2026-04-28: Added typed `jetmon2 api webhooks`
  `list|get|create|update|delete|rotate-secret|deliveries|retry` commands with
  typed event/site/state filter payloads, explicit filter-clearing flags,
  delivery status filters, idempotency support for POST actions, and focused
  path/body tests.
- [x] 2026-04-28: Added typed `jetmon2 api alert-contacts`
  `list|get|create|update|delete|test|deliveries|retry` commands with
  transport-specific destination shortcuts, raw destination JSON support, site
  filter clearing, delivery status filters, idempotency support for POST
  actions, and focused path/body tests.
- [x] 2026-04-28: Kept typed command payloads aligned with the implemented API
  schemas through local request structs, JSON body builders, and focused
  path/body tests for sites, events, webhooks, and alert contacts.
- [x] 2026-04-28: Added `jetmon2 api sites bulk-add --count <n>` with a
  bounded 200-site cap, fixture/file/stdin sources, JSON/CSV/newline parsing,
  dry-run output, per-site idempotency-key prefixes, and deterministic
  sequential blog IDs for local Docker data generation.
- [x] 2026-04-28: Added the embedded `cmd/jetmon2/testdata/api-cli-sites.json`
  fixture with always-up, redirect, slow, HTTP error, TLS error, custom-header,
  and keyword-check examples.
- [x] 2026-04-28: Added `jetmon2 api smoke` for Docker-local end-to-end API
  sanity checks covering health, auth identity, site creation, trigger-now,
  event listing, alert-contact creation, alert-contact send-test, JSON step
  summaries, and best-effort cleanup of created resources.
- [x] 2026-04-28: Added `jetmon2 api sites simulate-failure` with explicit
  `--site-id` targets or deterministic `--batch`/`--count` site IDs,
  `--create-missing`, optional trigger-now, active-event polling, transition
  lookup for returned event IDs, and JSON summaries that include per-site
  errors before exiting non-zero.
- [x] 2026-04-28: Added simulation modes for unreachable hosts, HTTP 500, HTTP
  403, redirect-policy failure, keyword mismatch, timeout/slow responses, and
  TLS certificate failure.
- [x] 2026-04-28: Added CLI batch tracking through deterministic blog ID ranges
  and the `X-Jetmon-CLI-Batch` custom-header marker for smoke-created sites,
  bulk-added sites, and simulated failure targets.
- [x] 2026-04-28: Added `--output table` with stable resource-oriented columns
  for API list responses and CLI workflow summaries while keeping JSON as the
  default script-friendly output.
- [x] 2026-04-28: Documented API CLI setup and examples in `API.md`, the Docker
  development loop, and the pinned v1-to-v2 rollout rehearsal runbook.
- [x] 2026-04-28: Added `jetmon2 api sites cleanup` with deterministic
  batch-derived IDs, explicit site IDs, dry-run output, 404-tolerant cleanup,
  and JSON/table summaries for removing local CLI test data.
- [x] 2026-04-28: Added `make api-cli-smoke` as the repeatable local API CLI
  smoke entrypoint and documented cleanup examples in API, Docker, and rollout
  docs.
- [x] 2026-04-28: Verified a fresh Docker Compose rebuild after Docker bridge
  networking was repaired, ran `make api-cli-smoke` against the rebuilt API on
  alternate host ports, and live-tested `sites cleanup --batch`.
- [x] 2026-04-28: Added the Docker-local `api-fixture` service with stable
  response-code, redirect, keyword, slow-response, and self-signed TLS
  endpoints for deterministic API CLI failure simulation.
- [x] 2026-04-28: Updated `sites simulate-failure` to auto-detect the fixture
  via `http://localhost:18091/health`, use Docker-internal fixture URLs when
  available, and keep public endpoint fallbacks via `--fixture-url=off`.
- [x] 2026-04-28: Added strict failure-simulation assertions for expected event
  state, event severity, transition presence, and transition reason. Assertion
  mode keeps polling until the expectations match or `--wait` expires, then
  returns a non-zero summary with the last observed API state.
- [x] 2026-04-28: Added `docs/api-cli-guide.md` as a feature-oriented API CLI
  usage guide with local setup, command examples, workflow recipes, failure
  simulation assertions, and automation notes.
- [x] 2026-04-28: Allowed API CLI flags before or after positional arguments by
  normalizing recognized flags before parsing while preserving `--` literals;
  added tests for interspersed flags and help output.
- [x] 2026-04-28: Added `make api-cli-validate` and
  `scripts/api-cli-validate.sh` for a live Docker-local validation pass across
  the feature guide's health, identity, generic request, smoke, failure
  simulation, and cleanup paths.
