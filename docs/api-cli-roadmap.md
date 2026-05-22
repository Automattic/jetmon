# API CLI Roadmap

Status: complete and merged into `v2`.

This file is retained as implementation history for the local
developer/operator CLI around Jetmon's internal `/api/v1` surface. Current
usage lives in [api-cli-guide.md](api-cli-guide.md). New API CLI follow-up
ideas should be tracked in [roadmap.md](roadmap.md) unless they are small
enough to land with adjacent feature work.

## Delivered Scope

The completed `jetmon2 api` command tree includes:

- request foundation: `health`, `me`, generic `request`, Bearer-token auth,
  custom headers, idempotency keys, request bodies from flags/files/stdin,
  pretty JSON, table output, and verbose request/response debugging
- typed resources: `sites`, `events`, `webhooks`, and `alert-contacts`
- local test data: `sites bulk-add`, deterministic fixture input,
  operator-supplied file/stdin input, batch markers, and cleanup
- local workflows: `api smoke`, `sites simulate-failure`, deterministic
  Docker-local failure fixture, webhook receiver fixture, and signature
  verification
- validation targets: `make api-cli-smoke`, `make api-cli-validate`, and API
  token convenience targets
- operator ergonomics: interspersed flags, command discovery with
  `jetmon2 api commands`, stable workflow tables, non-zero CI-friendly
  summaries, and documented examples

## Important Decisions

The CLI intentionally keeps JSON as the default output because it is the safest
shape for automation. Table output is for operators reading list and workflow
summaries.

Typed command payloads stay close to the API request structs so examples do not
drift from the server contract. Generic `api request` remains the escape hatch
for newly added API routes before typed commands are written.

Local fixture endpoints are preferred for deterministic event assertions.
Public endpoints remain useful for manual network realism, but the repeatable
smoke path should not depend on third-party availability.

CLI-created batches are marked with deterministic blog ID ranges and
`X-Jetmon-CLI-Batch` metadata. Destructive batch workflows verify those markers
unless the operator explicitly opts out with `--allow-unmarked`.

Remote writes are guarded. Non-local API targets refuse mutating requests
without `--allow-remote`; remote smoke, bulk-add, cleanup, and failure
simulation also require an explicit `--batch`.

Verbose output redacts sensitive headers. Automatic auth defaults to
same-origin requests, with `--auth-policy any-origin` available for deliberate
cross-origin API calls.

Webhook smoke stays Docker-local unless an external receiver is explicitly
enabled. Fixture delivery IDs are matched to API delivery rows so signature
verification proves the same delivery path was exercised.

## Completed Milestones

- 2026-04-28: API CLI branch created; request foundation and typed resource
  commands landed.
- 2026-04-28: Bulk test-site creation, fixture inputs, smoke runs, failure
  simulation, cleanup, table output, and Docker-local validation landed.
- 2026-04-28: Remote-write guardrails, same-origin auth defaults, sensitive
  verbose-header redaction, batch ownership checks, and command discovery
  landed.
- 2026-04-29: Docker-local webhook receiver fixture and end-to-end webhook
  smoke validation landed.

## Current References

- Feature guide: [api-cli-guide.md](api-cli-guide.md)
- Internal API reference: [internal-api-reference.md](internal-api-reference.md)
- Docker runtime and local API fixture notes: [docker-images.md](docker-images.md)
- Active roadmap: [roadmap.md](roadmap.md)
