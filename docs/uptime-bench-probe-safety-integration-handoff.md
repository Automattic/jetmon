# Uptime-Bench Probe-Safety Integration Handoff

This handoff defines the remaining v2-only probe-safety coverage that should be
run in uptime-bench or another isolated lab. Do not contact real WPCOM
endpoints, do not send alerts, and do not use customer sites for these tests.

## Goal

Prove that Jetmon v2 blocks unsafe probe targets at runtime without turning
operator-side safety blocks into customer downtime. The important evidence is
not just that the checker returns the right error code; the Monitor and
Veriflier paths must avoid WPCOM notifications, legacy projection changes,
webhook delivery, alert-contact delivery, and false downtime events.

## Required Environment

- Jetmon v2 Monitor with `WPCOM_NOTIFY_ENABLE=false`.
- v2 Verifliers only.
- Internal-only HTTP, HTTPS, and DNS fixtures.
- Target safety enabled unless a scenario explicitly documents why a lab-only
  private fixture mode is needed.
- A clean way to query events, audit rows, `jetpack_monitor_site_safety_flags`,
  check history, and Veriflier responses after each scenario.

## DNS Rebinding Scenarios

Use an authoritative test DNS responder with very short TTLs. Prefer
public-looking lab addresses routed inside the test network so target-safety
guardrails remain enabled.

1. **Mixed answer set:** hostname returns one public-looking address and one
   loopback/private/link-local address in the same response.
   Expected: the check is blocked as probe safety, no downtime event opens, and
   a safety flag or Veriflier unknown outcome is recorded.
2. **Rebind after cache expiry:** first lookup returns a public-looking address,
   later lookup after TTL expiry returns loopback/private/link-local.
   Expected: the later check is blocked before dial; no stale cached public
   result is used past the configured TTL.
3. **Unsafe redirect hostname:** initial public-looking target redirects to a
   hostname whose lookup resolves to loopback/private/link-local.
   Expected: redirect is blocked as probe safety, not generic downtime.
4. **Veriflier parity:** send the same DNS-rebinding cases through the
   Monitor-to-Veriflier v2 JSON path.
   Expected: Veriflier reports `OutcomeUnknown` with checker error code 9
   (`ErrorProbeSafety`) for blocked targets, and those results must not count
   as regional confirmation of customer downtime.

## TLS Pathology Scenarios

Run each TLS case through direct Monitor checks and v2 Veriflier checks where
feasible. Capture outcome, checker error code, detector class, TLS version,
cipher suite, and any event/audit side effects.

1. **TLS 1.0 and TLS 1.1 with otherwise trusted test certificates.**
   Expected in `GET` + `full`: advisory `tls_deprecated` behavior, not legacy
   downtime. In `HEAD` + `legacy` and `GET` + `simple_http`, record the TLS
   telemetry but do not introduce new customer-visible detections.
2. **No common cipher / protocol-version alert.**
   Expected: bounded TLS/connect failure, no hang, no panic, and no sensitive
   details leaked.
3. **Handshake close or alert before certificate exchange.**
   Expected: bounded TLS/connect failure with clear diagnostics.
4. **Expired, self-signed, hostname-mismatch, and incomplete-chain
   certificates.**
   Expected: SSL classification remains stable. If the current Go TLS stack
   reports these as generic certificate verification failures, record that
   fact; do not assume Jetmon can always distinguish expired vs self-signed
   after a failed handshake.
5. **Large certificate chain.**
   Expected: bounded resource usage and stable classification.
6. **Slow TLS handshake.**
   Expected: timeout within the configured check timeout envelope, with no
   goroutine or file-descriptor leak.

## Evidence To Capture

- Scenario name, target URL, DNS answer sequence or TLS fixture config.
- Monitor result: success, HTTP code, error code, detector class, body/TLS
  metadata, and check duration.
- Veriflier result: outcome, success, error code, and duration.
- Event table delta: prove no customer downtime event opens for probe-safety
  blocks.
- Audit delta: prove probe-safety blocks are recorded when Monitor has a
  monitor row.
- `jetpack_monitor_site_safety_flags` delta for Monitor-owned rows.
- WPCOM, webhook, and alert-contact delivery deltas: expected zero.
- Resource deltas for slow/large TLS scenarios if uptime-bench can capture
  them cheaply.

## Stop Conditions

Stop and report immediately if any probe-safety or TLS-pathology fixture:

- sends a WPCOM notification,
- opens or closes a customer downtime event for a Monitor-side safety block,
- updates the legacy projection to down,
- causes a Veriflier probe-safety block to count as downtime confirmation,
- hangs beyond the configured timeout envelope,
- leaks sensitive headers, credentials, certificate material, or full target
  payloads into reports.

## Jetmon-Side Notes

Current v2 already has local unit coverage for direct unsafe target rejection,
unsafe redirect rejection, mixed DNS answers, rebind-after-cache-expiry, and
dial-time safety checks. This handoff is asking for the production-shaped
integration evidence around Monitor, Veriflier, database side effects, and
operator reports.
