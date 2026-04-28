# Outbound Credential Encryption Plan

**Status:** Planning note, not an accepted architecture decision.

ADR-0003 accepts plaintext storage for outbound-dispatch credentials under the
current internal-only v2 threat model. This note captures the migration path
for the next hardening step: application-level encryption at rest for webhook
signing secrets and alert-contact destination credentials.

## Current State

Two columns contain raw outbound credentials because dispatch needs the
original value at send time:

- `jetmon_webhooks.secret`: HMAC signing secret used to sign webhook delivery
  bodies.
- `jetmon_alert_contacts.destination`: transport-specific JSON containing an
  email address, PagerDuty integration key, Slack/Teams webhook URL, or SMTP
  password.

Handlers never return these values after creation or rotation. Normal reads
return only `secret_preview` or `destination_preview`; dispatch workers load the
raw value through separate helper functions.

## Goals

- Protect credentials from database-only compromise, read replicas, SQL dumps,
  and backup exposure.
- Keep dispatch fast enough that decrypting credentials does not become the
  bottleneck during event storms.
- Preserve the existing API contract: create/rotate still return a one-time
  secret where applicable, and reads still expose only previews.
- Allow rollback during migration without losing the ability to dispatch
  existing webhooks and alert contacts.

## Non-Goals

- This does not protect against a fully compromised application host. The
  dispatcher must hold decrypt-capable key material in memory to send alerts.
- This does not replace webhook HMAC signing with asymmetric signatures.
- This does not define the public/customer tenant model; that remains a public
  API design item.
- This does not encrypt delivery payload history. Payloads contain event data,
  not destination credentials.

## Target Design

Use envelope-style application encryption with a versioned service data key:

1. A production key manager exposes the active credential-encryption key and
   key id to Jetmon at startup.
2. Jetmon keeps the plaintext data key only in memory.
3. Each credential value is encrypted locally with AES-256-GCM before storage.
4. Each encrypted row stores the ciphertext, nonce, key id, and algorithm.
5. Load helpers decrypt locally using the in-memory key matching the stored key
   id.

This avoids a KMS round trip on every delivery while still protecting database
contents and backups from credential disclosure. If the deployment environment
requires KMS unwrap per key version, do that once at process startup or reload,
not inside the per-delivery hot path.

Recommended config shape:

- `CREDENTIAL_ENCRYPTION_MODE`: `plaintext`, `dual_write`, or
  `encrypted_required`.
- `CREDENTIAL_ENCRYPTION_KEY_ID`: current key version identifier.
- `CREDENTIAL_ENCRYPTION_KEY_SOURCE`: local dev key, environment-provided key,
  or production KMS-backed provider.

## Schema Path

Add encrypted columns alongside the existing plaintext columns:

- `jetmon_webhooks.secret_ciphertext`
- `jetmon_webhooks.secret_nonce`
- `jetmon_webhooks.secret_key_id`
- `jetmon_webhooks.secret_alg`
- `jetmon_alert_contacts.destination_ciphertext`
- `jetmon_alert_contacts.destination_nonce`
- `jetmon_alert_contacts.destination_key_id`
- `jetmon_alert_contacts.destination_alg`

Keep `secret_preview` and `destination_preview` unchanged. Previews are not
credentials and stay useful for operator display.

After backfill and one stable release, make the encrypted columns required for
new rows. Dropping or nulling the plaintext columns should be a separate
deployment step after production has run in `encrypted_required` mode long
enough to prove there is no fallback traffic.

## Migration Phases

1. **Introduce encryption helpers.** Add a small internal package for encrypt
   and decrypt operations, with test vectors and explicit key id handling.
2. **Add nullable encrypted columns.** Existing plaintext rows continue to
   dispatch without behavior change.
3. **Dual-write new credentials.** Create, update, and rotate paths write both
   plaintext and encrypted values. Load helpers prefer encrypted values and
   fall back to plaintext.
4. **Backfill existing rows.** A CLI or migration command encrypts existing
   plaintext values in batches. It should be idempotent and safe to resume.
5. **Require encrypted reads.** Flip production to `encrypted_required` once
   every row has encrypted material. Fallback to plaintext becomes an error and
   a metric.
6. **Remove plaintext storage.** In a later release, null or drop the plaintext
   columns after backup retention and rollback windows make that safe.

## Operational Requirements

- Metrics for encrypt failures, decrypt failures, plaintext fallback count, and
  unknown key id count.
- A startup check that fails fast in `dual_write` or `encrypted_required` when
  the configured key source is unavailable.
- A key rotation runbook: add new key id, dual-write new data with it, rewrap
  old rows, then retire the old key after the rollback window.
- A break-glass procedure for restoring dispatch if the key source is
  unavailable.

## Test Requirements

- Unit tests for encryption round trips, wrong-key failures, nonce uniqueness,
  and malformed ciphertext.
- Repository tests proving create/update/rotate paths write encrypted values in
  `dual_write` and `encrypted_required`.
- Dispatch tests proving load helpers prefer encrypted columns and emit errors
  instead of silently using plaintext when `encrypted_required` is active.
- Migration/backfill tests proving the backfill is resumable and leaves previews
  unchanged.

## Open Questions

- Which production key manager should be the first provider?
- Should local development use a generated throwaway key, a config-provided key,
  or stay in `plaintext` mode by default?
- What is the minimum stable period in `encrypted_required` before plaintext
  columns can be removed?
- Do backups or replica access policies require encrypted columns before public
  API work starts, or only before customer-managed secrets are exposed directly?
