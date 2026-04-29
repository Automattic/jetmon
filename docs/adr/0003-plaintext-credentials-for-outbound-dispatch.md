# 0003 — Plaintext credential storage for outbound dispatch

**Status:** Accepted (2026-04-25)

## Context

Both `jetmon_webhooks.secret` (HMAC signing key) and
`jetmon_alert_contacts.destination` (transport-specific credential
JSON: PagerDuty integration key, Slack/Teams webhook URL, SMTP
password) need to be available at dispatch time so the worker can
authenticate or sign the outbound request.

`jetmon_api_keys.token_hash` stores SHA-256 hashes — keys are
verified by hashing the inbound bearer token and comparing in
constant time. This pattern works because API keys are validated on
the **inbound** path, where having only the hash is sufficient.

The first draft of the webhook schema (migration 13) mirrored this
pattern with `secret_hash CHAR(64)`. While building the delivery
worker we realized the analogy doesn't transfer: HMAC signing
requires the actual secret material, not its hash. There is no way
to reconstruct the original secret from a SHA-256 hash, so a hashed
secret is functionally useless to the worker.

The same constraint applies to alert-contact credentials. To call
the PagerDuty Events API we need the integration key. To POST to a
Slack incoming-webhook URL we need the URL. To `smtp.SendMail` we
need the password. These are call-time inputs; hashing them at rest
would prevent the call.

## Decision

We will store outbound-dispatch credentials in **plaintext** in the
relevant tables:

- `jetmon_webhooks.secret VARCHAR(80)` — the raw HMAC signing key,
  with the `whsec_` prefix preserved (Stripe-style leak-detection
  hint).
- `jetmon_alert_contacts.destination JSON` — the transport-specific
  credential as supplied by the operator.

Each table also stores a small "preview" column (`secret_preview`
for webhooks, `destination_preview` for alert contacts) holding the
last 4 characters of the credential, so the API can return a
non-sensitive identifier without ever leaking the full value.

The full credential value is never returned through the API after
creation. `secret` is shown ONCE in the create / rotate response.
`destination` is supplied by the caller on create and is never echoed
back; subsequent reads expose only `destination_preview`.

We document the threat model on the migrations and in code comments
so future readers can audit it without rediscovering it.

## Consequences

**Wins:**
- Outbound dispatch works correctly with no special infrastructure
  (no KMS round-trip, no per-secret cache layer).
- Read-only API consumers (read-scope tokens) cannot exfiltrate
  credentials — the SELECT used by handlers does not return the
  credential column. The worker uses a separate `LoadSecret` /
  `LoadDestination` call.
- Rotation is simple: replace the row's secret column, return the
  new value once, the next dispatch picks it up.

**Costs:**
- A read of `jetmon_webhooks` or `jetmon_alert_contacts` at the SQL
  level (DBA query, MySQL replica, backup file) leaks all signing
  keys and destination credentials in plaintext. For an internal
  service behind a gateway with an internal-only set of consumers
  (ADR-0002), this is equivalent to the existing access-to-events
  threat — anyone with that level of DB access already has access to
  the events themselves. The marginal cost is small.
- If Jetmon ever exposes its API directly to customers (i.e.
  ADR-0002 is reversed), this trade-off changes. Customer-managed
  secrets in plaintext under shared infrastructure is a stronger
  threat. The mitigation path is encryption at rest with a master
  key (KMS-style), which is queued in [`../roadmap.md`](../roadmap.md) as a future
  hardening step.

## Alternatives considered

- **Hashed credentials (the API-key pattern).** Rejected because
  HMAC signing and outbound HTTPS auth need the raw key material,
  not its hash. There is no inbound-validation use case for these
  secrets.
- **Encryption at rest with a master key (e.g. KMS).** A real
  improvement on plaintext, but adds an operational dependency
  (KMS access, key rotation procedure) and a runtime cost (decrypt
  on every dispatch or maintain an in-process cache). Deferred —
  the right time to do this is alongside any move toward customer-
  managed secrets, not before.
- **Per-row at-rest encryption with the AUTH_TOKEN as key material.**
  Rejected as security theatre — the key sits next to the data on
  the same host, so an attacker with DB access likely has config
  access too. The complexity buys nothing.

## Related

- ADR-0002 (Internal-only API) — defines the threat model that
  makes plaintext storage acceptable today.
- Migration 13 (`jetmon_webhooks`) — documents the rationale inline.
- Migration 16 (`jetmon_alert_contacts`) — same rationale.
- `internal/webhooks/webhooks.go` — `LoadSecret` is intentionally a
  separate function (not a field on `Webhook`) to prevent leakage
  through serialization.
- `internal/alerting/contacts.go` — `LoadDestination` follows the
  same pattern.
