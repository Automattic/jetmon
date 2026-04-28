# API CLI Roadmap

Status: started on `feature/api-cli`.

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

- [ ] Add `sites list|get|create|update|delete|pause|resume|trigger-now`.
- [ ] Add `events list|get|transitions|close`.
- [ ] Add `webhooks list|get|create|update|delete|rotate-secret|deliveries|retry`.
- [ ] Add `alert-contacts list|get|create|update|delete|test|deliveries|retry`.
- [ ] Keep typed command payloads close to the OpenAPI component schemas or shared
  request structs so CLI examples do not drift from the server contract.
- [ ] Add `sites bulk-add --count <n>` for creating a bounded batch of real
  monitored URLs for local testing. Support `--source fixture|file|stdin` so the
  default is repeatable but operators can supply their own CSV/JSON/newline
  list without recompiling the CLI.
- [ ] Add a curated local fixture of real public URLs with mixed behavior:
  always-up examples, redirects, slow responses, client/server error responses,
  TLS edge cases, and keyword-check candidates. Keep the fixture small,
  documented, and safe for local-only test data generation.

## P2 - Local Smoke Workflows

- [ ] Add `jetmon2 api smoke` to run a local end-to-end sanity pass against Docker:
  health, auth, create a site, trigger a check, read events, and exercise a
  webhook or alert-contact test path.
- [ ] Add `jetmon2 api sites simulate-failure` to intentionally mutate one or
  more CLI-created test sites into known failure states, trigger checks, and
  show the resulting event IDs and transitions.
- [ ] Support targeted failure modes for simulation: unreachable host, HTTP 500,
  HTTP 403, redirect-policy failure, keyword mismatch, timeout/slow response,
  and TLS/certificate failure.
- [ ] Track CLI-created test-site batches with a stable label or metadata marker
  so smoke tests and failure simulations can operate on `--batch <id>`,
  `--count <n>`, or explicit site IDs without touching unrelated local data.
- [ ] Add cleanup behavior for resources created by smoke runs.
- [ ] Return non-zero exit codes and concise failure summaries suitable for CI.

## Test Site Source Ideas

- **Recommended path:** Start with a curated checked-in fixture plus
  operator-supplied file/stdin imports. Use real public endpoints for network
  realism, add a Docker failure fixture later for deterministic event
  assertions, and cycle through the source list with varied site settings for
  larger `--count` values instead of inventing fake public domains.
- **Curated fixture:** Check in a small `docs/testdata` or `internal/testdata`
  source list with public endpoints selected for deterministic behavior. This
  should be the default because it keeps local test runs repeatable.
- **Operator-supplied file/stdin:** Accept newline, CSV, or JSON site lists so
  developers can test with a private list of real customer-like domains without
  committing those domains to the repo.
- **Docker failure fixture:** Add a local test-site container later for the
  most deterministic failure simulation. Real public sites are useful for
  network realism, but local fixture endpoints are better for asserting exact
  event transitions.
- **Generated variants:** For `--count` larger than the curated fixture, cycle
  through the source list with deterministic suffix metadata and varied
  per-site settings: redirect policy, keyword, timeout, custom headers, and
  check interval. Do not invent nonexistent public domains as "real" sites.
- **External top-site lists:** If broader realism is needed, allow importing an
  operator-downloaded ranked domain list. Keep download/fetch outside the CLI at
  first so local tests remain reproducible and do not depend on third-party
  availability.

## P3 - Output Ergonomics

- [ ] Add stable table output for list commands while keeping JSON as the default
  automation-friendly format.
- [x] Add `--pretty` for formatted JSON and preserve raw JSON for scripts.
- [ ] Add examples to `API.md`, Docker docs, and the v1-to-v2 rollout rehearsal
  docs once the command shape has stabilized.

## Completed

- [x] 2026-04-28: Created the `feature/api-cli` branch and initial roadmap.
- [x] 2026-04-28: Added the `jetmon2 api` command group with `health`, `me`,
  and generic `request` subcommands.
- [x] 2026-04-28: Added local defaults, Bearer-token auth, repeatable custom
  headers, idempotency-key support, request body helpers, JSON pretty printing,
  and verbose request/response header logging.
- [x] 2026-04-28: Added focused tests for URL resolution, auth/idempotency
  headers, verbose output, pretty JSON output, and HTTP error handling.
