# Jetmon Adversarial Probe Security Analysis

Date: 2026-05-17
Branch: `security-adversarial-probes`
Scope: Monitor and Veriflier handling of remote, untrusted HTTP(S), DNS, redirect, TLS, headers, and body-response behavior.

## Executive Summary

This pass focused on attacker-controlled site responses that can consume Monitor or Veriflier resources, amplify stored metadata, or make Jetmon probe unintended destinations.

Fixes landed in this branch:

- Enforce `BODY_READ_MAX_MS` and `KEYWORD_READ_MAX_MS` during response-body reads.
- Cap redirect diagnostic metadata before storing it in `Result.RedirectChain` and `Result.FinalURL`.
- Set `MaxResponseHeaderBytes` on checker transports.
- Block cross-host redirects to localhost, private, link-local, multicast, CGNAT, documentation, benchmark, and reserved address ranges.
- Reject same-host redirects that introduce URL userinfo, and reject obfuscated IPv4 host forms such as octal, hex, and single-integer loopback encodings.
- Validate and bound custom headers at API write time, and defensively filter stored custom headers before outbound checks.
- Reject unsafe literal target URLs at the site API and both Veriflier protocols.
- Add last-mile transport safety checks so target-safety-enabled checks validate resolved addresses immediately before dialing, not only before request construction.
- Add per-entry DNS cache expiry jitter so lookups created during one check wave do not all refresh on the same later wave.
- Cap authenticated API JSON request bodies at 1 MiB, including idempotency-key hashing and replay caching.
- Apply public-target validation and guarded HTTP clients to outbound webhook and Slack / Teams alert delivery URLs.
- Add `jetmon2 site-safety unsafe-urls`, a dry-run-first cleanup tool that scans active legacy rows with the same URL guard and can deactivate unsafe rows when run with `--execute`.
- Block already-stored unsafe or malformed direct targets in the checker hot path with a non-downtime `ErrorProbeSafety` result. The orchestrator audits these as `probe_safety_blocked` and does not open or close downtime incidents.
- Classify unsafe redirect targets as `ErrorProbeSafety` rather than generic redirect failures, so redirect SSRF blocks are not counted as customer-site downtime.
- Add regression coverage for gzip expansion, slow/infinite body streaming, oversized response headers, unsafe redirects, unsafe Veriflier URLs, and custom header injection/framing attempts.

The remaining product/design question is whether these probe-safety blocks should also be visible as first-class event/degradation states. This branch records them in the audit log and metrics, but intentionally does not create customer-site downtime events.

## Evidence Reviewed

- Checker transport, redirect, body, DNS, TLS, and metadata paths: `internal/checker/checker.go`
- Checker adversarial and regression tests: `internal/checker/checker_test.go`
- Shared unsafe-target guard: `internal/netguard/`
- Site API URL and custom check config writes: `internal/api/handlers_sites_write.go`
- Veriflier HTTP server request-size, auth, URL validation, and timeout controls: `internal/veriflier/server.go`
- Veriflier client batch/deadline behavior: `internal/veriflier/client.go`
- Monitor escalation request propagation to Verifliers: `internal/orchestrator/orchestrator.go`
- Uptime-bench scenario and target/DNS/TLS responder capabilities, read-only: `../uptime-bench/internal/scenario`, `../uptime-bench/internal/targetserver`, `../uptime-bench/internal/dnsserver`
- Live v1 table backup, read-only and parsed on `jetmon-service-host-5`: `../jetpack_monitor_sites-2026-05-13-225300.sql.gz`

## Backup Data Review

I parsed the May 13, 2026 v1 `jetpack_monitor_sites` backup on `jetmon-service-host-5` to avoid heavier work on the local machine, then restored it into an isolated loopback-only MariaDB container on host 5 to exercise the cleanup command against a realistic copy.

Totals:

- Rows: 3,557,537
- Active rows: 594,377
- Maximum stored `monitor_url` length: 300 bytes
- Schemes: 2,081,334 `http`, 1,475,834 `https`, 366 missing scheme, 3 malformed/bad scheme.

Initial rough categories found:

- 30 active unsafe IP literal URLs.
- 6 active localhost-name URLs.
- 12 active `.local` / local-internal hostname URLs.
- 4 active missing-scheme URLs.
- 1 active URL with userinfo.

The first full cleanup pass surfaced a false-positive risk: ordinary DNS names with numeric labels, such as `02.example.com`, were being treated like octal IPv4 fragments. I narrowed that guard so non-canonical IPv4 detection only applies when the whole hostname is numeric/IPv4-like, then reran the backup exercise. The corrected `site-safety unsafe-urls` pass found 61 unsafe active rows.

Representative active examples:

- `www.nichederich.com`
- `www.umunion.org`
- `naphtaliassociates.org.uk`
- `trulyteachmetarot.com`
- `http://192.168.1.213/dtcpl`
- `https://127.0.0.1:80`
- `http://10.4.5.238:8080`
- `https://10.0.20.176/blog`
- `http://172.31.26.160`
- `https://0.0.0.0`
- `http://localhost/lightupwear.com`
- `http://localhost:8000`
- `https://app.localhost/buddygodutch`
- `https://localhost:3000`
- `https://adonislounge.local/nyc`
- `http://jucosol.local`
- `https://rtcamp.local`
- `http://host.docker.internal:99`
- `https://safemed.siteqa.uthsc.local`
- `http://mjrodero@50.87.195.157`
- `http://102`
- `http://1717657663`
- `http://6`

Cleanup command exercise against the temporary database copy:

```text
dry-run:        scanned_active=594377 unsafe=61 deactivated=0
execute:        scanned_active=594377 unsafe=61 deactivated=61
follow-up dry:  scanned_active=594316 unsafe=0 deactivated=0
```

The command only changed the temporary restored copy. The container was stopped after validation.

## Fixes Added

### Body-Read Time Budgets

`readResponseBody` now starts a post-header read budget when:

- response integrity mode is budgeted, not strict finite, and `BodyReadMaxMS > 0`;
- keyword/body-rule mode is active and `KeywordReadMaxMS > 0`.

Budget exhaustion closes the response body to unblock the reader. Non-keyword budget exhaustion remains `ErrorBodyRead`; keyword budget exhaustion is classified as `ErrorTimeout`.

Tests added:

- `TestCheckBodyReadMaxMSTimesOutBudgetedRead`
- `TestCheckBodyReadMaxMSDoesNotFailStrictFiniteBody`
- `TestCheckKeywordReadMaxMSTimesOutAsTimeout`

### Response Size And Metadata Bounds

Checker transports now set a 64 KiB `MaxResponseHeaderBytes` cap. Redirect chain entries and final URL diagnostics are capped at 2048 bytes plus ellipsis. This prevents hostile targets from producing oversized headers or multi-megabyte event metadata.

Tests added:

- `TestCheckRejectsOversizedResponseHeaders`
- `TestCheckRedirectMetadataBoundsLargeLocation`

### Body Expansion

Gzip responses are bounded after decompression by the existing body-read byte cap. This was added as explicit regression coverage because compressed expansion is a common way to bypass wire-size assumptions.

Test added:

- `TestCheckCompressedLargeBodyIsCappedAfterDecompression`

### Redirect SSRF Guard

Cross-host redirects now run through a target safety check before the checker follows them. The guard rejects localhost, private, link-local, multicast, CGNAT, documentation, benchmark, and reserved address ranges, including hostnames that resolve into those ranges. Unsafe redirect blocks are classified as `ErrorProbeSafety` and are not downtime failures.

Same-host redirects are also rejected if they introduce URL userinfo.

Tests covered/added:

- `TestCheckRejectsCrossHostRedirectToUnsafeAddress`
- `TestCheckRejectsSameHostRedirectWithUserinfo`

### Unsafe Direct Target Rejection

The site API and both Veriflier protocol handlers now reject unsafe direct target URLs before storage or execution. The checker also enforces the same protection for already-stored rows when called by the Monitor, trigger-now, rollout checks, or Veriflier runtime.

Blocked targets include missing schemes, non-HTTP schemes, userinfo, localhost, loopback, RFC1918, `.local` / internal-style names, IPv6 loopback, cloud metadata-style link-local targets, CGNAT, documentation, benchmark, reserved ranges, and non-canonical IPv4 encodings. When the checker blocks a stored direct target, it returns `ErrorProbeSafety`; the orchestrator records an audit row and intentionally skips downtime state transitions.

Target-safety-enabled checks also carry a context flag into the checker transports. The HTTP pooled transport and HTTPS dial path validate every selected address immediately before dialing so a future call path cannot validate one resolution and accidentally dial another unsafe address.

Tests added:

- `TestValidateMonitorURL`
- `TestUnsafeHost`
- `TestParsePublicHTTPURL`
- `TestCheckBlocksUnsafeDirectTargetWhenSafetyEnforced`
- `TestCheckTargetSafetyDNSFailureIsConnectFailure`
- `TestProcessResultsProbeSafetyBlockAuditsWithoutStateChange`
- `TestServerHandleCheckRejectsUnsafeURL`
- `TestServerHandleV2CheckRejectsUnsafeURL`
- `TestCheckerProbeSafetyErrorCodeContract`
- `TestTransportSafetyBlocksUnsafeIPBeforeDial`

### DNS Cache Burst Smoothing

The checker DNS cache is lazy rather than prewarmed. Entries are created when checks first resolve a host. This branch changes cache expiration from a fixed `lookup time + 15m` to a per-entry expiration spread across the last three minutes of the TTL, salted per process. That means a batch of hostnames resolved during one scheduler wave should refresh over a window instead of expiring together.

Tests added:

- `TestCheckDNSCacheExpiryUsesJitter`
- `TestValidateResolvedTargetRejectsMixedPublicPrivateDNSAnswers`
- `TestValidateResolvedTargetRejectsDNSRebindAfterCacheExpiry`

### TLS Pathology Coverage

The checker keeps TLS 1.0/1.1 handshakes enabled so deprecated TLS can be recorded as an advisory rather than an outage. This branch also keeps certificate trust failures classified as SSL failures.

Tests added:

- `TestCheckTLS11IsAdvisoryNotOutage`
- `TestCheckSelfSignedTLSFailsAsSSL`

### API And Delivery URL Hardening

API JSON routes now wrap request bodies with a 1 MiB `MaxBytesReader`, and idempotency-key hashing applies the same cap before `io.ReadAll`. Outbound webhook registrations are validated in the webhooks package as well as the API handler. Slack and Teams alert-contact destinations are validated on create/update, and default alert delivery uses a protected HTTP client that rejects unsafe redirects and non-public resolved dial addresses.

Protected delivery clients intentionally do not use `http.ProxyFromEnvironment`. A proxy would make the Jetmon process dial the proxy rather than the final consumer URL, so this process could no longer prove that the final resolved target is public. If production egress requires proxy support for webhook or alert delivery, add an explicit trusted-proxy mode with documented proxy-side SSRF controls rather than honoring ambient proxy environment variables by default.

Tests added:

- `TestIdempotencyMiddlewareRejectsOversizedJSONBody`
- `TestCreateWebhookRejectsBadURL`
- `TestUpdateWebhookRejectsUnsafeURLBeforeDB`
- `TestValidateDestinationRejectsUnsafeWebhookURLs`
- `TestProtectedDialContextRejectsUnsafeLiteral`
- `TestProtectedHTTPClientRejectsUnsafeRedirectURL`

### Legacy Cleanup Path

`jetmon2 site-safety unsafe-urls` scans active `jetpack_monitor_sites` rows and classifies `monitor_url` with the same admission-time shape/literal guard, `netguard.ParsePublicHTTPURL`. By default it is a dry run and prints counts plus bounded examples. Passing `--execute` deactivates matching active rows by setting `monitor_active = 0`; it does not delete rows. Runtime DNS safety checks still remain necessary for hostnames whose resolution changes later.

Tests added:

- `TestClassifyUnsafeMonitorURL`
- `TestRunSiteSafetyUnsafeURLsDryRun`
- `TestRunSiteSafetyUnsafeURLsExecuteDeactivates`

### Custom Header Safety

API writes now reject custom header maps with too many entries, oversized names/values, invalid token characters, control characters, or hop-by-hop/request-framing headers. The checker also filters stored headers defensively before sending outbound requests.

Tests added:

- `TestEncodeCustomHeadersRejectsUnsafeNamesAndValues`
- `TestEncodeCustomHeadersRejectsLargeInput`
- `TestParseCustomHeaders`

## External Vulnerability Notes Checked

After the initial fixes, I searched public advisory/release notes for uptime-monitor style failures that could suggest new test cases. Relevant patterns:

- Uptime Kuma had a monitor URL handling advisory for local-file access through `file:///`-style targets: <https://github.com/louislam/uptime-kuma/security/advisories/GHSA-2qgm-m29m-cj2h>. Jetmon already required `http`/`https`; this branch adds unsafe literal target rejection at API and Veriflier boundaries.
- Uptime Kuma had an SSRF-style metadata exposure advisory through keyword monitors and cloud metadata endpoints: <https://github.com/louislam/uptime-kuma/security/advisories/GHSA-qjxc-h5jf-c7rj>. This branch blocks metadata link-local IPs, internal metadata hostnames, and custom metadata headers that would require unsafe target URLs to be useful.
- Uptime Kuma had a notification-template SSTI advisory: <https://github.com/louislam/uptime-kuma/security/advisories/GHSA-vffh-c9pq-4crh>. I searched Jetmon for user-provided template execution and found no equivalent template engine path in webhook or alert rendering.
- Uptime Kuma had a notification URL ReDoS advisory: <https://github.com/louislam/uptime-kuma/security/advisories/GHSA-hx7h-9vf7-5xhg>. Jetmon uses Go's RE2 engine for regexes and this branch caps untrusted URL length at admission.
- Uptime Kuma had command injection in a monitor type that shell-executed host input: <https://github.com/louislam/uptime-kuma/security/advisories/GHSA-hfxh-rjv7-2369>. Jetmon's HTTP Monitor / Veriflier paths do not shell out for target checks.
- Uptime Kuma had a missing-authorization monitor-data leak: <https://github.com/louislam/uptime-kuma/security/advisories/GHSA-c7hf-c5p5-5g6h>. Jetmon's internal API routes are scope-gated; the analogous risk remains future public/gateway routes exposing monitor timing data without tenant checks.

## Host Status

- `jetmon-service-host-3`: reachable as `jetmon`; passwordless sudo works; no active `jetmon*` or `uptime-bench*` units observed.
- `jetmon-service-host-4`: reachable as `jetmon`; passwordless sudo works; currently running uptime-bench target/DNS units.
- `jetmon-service-host-5`: reachable as `jetmon`; passwordless sudo works; no active `jetmon*` or `uptime-bench*` units observed. Used for remote checker and Veriflier test-binary execution.
- `jetmon-service-host-6`: reachable as `jetmon`, but `sudo -n` fails with password required. Provisioning this host still needs a bootstrap credential or approved out-of-band admin path.

## End-to-End Lab

On `jetmon-service-host-5`, I ran temporary high-port binaries only:

- `/tmp/jetmon-security-lab-responder`: hostile HTTP/TLS responder on `:18080` and `:18443`.
- `/tmp/jetmon-veriflier2-security`: Veriflier on `:17803`, target safety enabled.
- `/tmp/jetmon-security-lab-client`: scenario runner posting to `/v2/check`.

The responder was accessed through host 5's public IPv6 address, so the Veriflier exercised the real target-safety path instead of bypassing it with loopback. Temporary listeners were stopped after the run.

Passing scenarios:

| Scenario | Result |
|---|---|
| Direct `127.0.0.1` target | Veriflier rejected with HTTP 400 before execution. |
| Public `/ok` target | `outcome=up`, `success=true`, `error_code=0`. |
| Public target redirecting to `127.0.0.1` | `outcome=unknown`, `success=false`, `error_code=9` (`ErrorProbeSafety`). |
| Public target redirecting to `169.254.169.254` | `outcome=unknown`, `success=false`, `error_code=9`. |
| Redirect loop | `outcome=probe_error`, `error_code=4` (`ErrorRedirect`) after redirect cap. |
| Infinite body stream with 1 KiB body cap | Completed immediately as `up`, proving the checker stops reading after the cap. |
| Slow body stream with 50 ms body-read budget | Completed in 51 ms as `ErrorBodyRead`, no hang. |
| 80 KiB response header | Blocked as `probe_error`, `ErrorConnect`, no unbounded header read. |
| Gzip-expanded body with 1 KiB cap | Completed as `up`, proving cap applies after decompression. |
| Self-signed TLS endpoint | `probe_error`, `ErrorSSL`, no crash or hang. |

## Verification Run

Local targeted tests:

```bash
go test -count=1 ./internal/netguard ./internal/checker ./internal/api ./internal/veriflier ./veriflier2/cmd ./internal/orchestrator ./internal/webhooks ./internal/alerting ./internal/deliverer ./cmd/jetmon2 ./cmd/security-lab-client ./cmd/security-lab-responder
git diff --check
```

Remote targeted tests on `jetmon-service-host-5`:

```bash
/tmp/jetmon-checker-security.test -test.run "Test(ParseCustomHeaders|Check(BlocksUnsafeDirectTargetWhenSafetyEnforced|BodyReadMaxMS|KeywordReadMaxMS|RedirectMetadataBoundsLargeLocation|RejectsCrossHostRedirectToUnsafeAddress|RejectsOversizedResponseHeaders|CompressedLargeBody|TruncatedBody|RedirectFail|Timeout))" -test.v
/tmp/jetmon-veriflier-security.test -test.run "Test(ServerHandleCheckRejectsUnsafeURL|ServerHandleV2CheckRejectsUnsafeURL|ServerHandleCheckRequestBodyLimit|ServerRejectsEmptyAuthToken|ServerHandleCheckUnauthorized|ServerHandleV2Check)" -test.v
/tmp/jetmon-api-security.test -test.run "Test(ValidateMonitorURL|TriggerNowUnsafeStoredURLIsProbeSafetyBlock|EncodeCustomHeadersRejects)" -test.v
/tmp/jetmon-orchestrator-security.test -test.run "TestProcessResultsProbeSafetyBlockAuditsWithoutStateChange" -test.v
```

All targeted tests passed.

## Performance Notes

Most checks added here are constant-time string or URL-shape checks. The meaningful added work is target-safety DNS validation for production Monitor / Veriflier checks. That validation runs at check time for already-stored legacy rows, not once at write time, because the v1 table does not have a persisted safety-vetted projection. It uses the existing 15-minute checker DNS cache, so repeated checks of the same hostname on a five-minute cadence should normally reuse cached resolution.

The response-header cap has no steady-state cost. Body-read timing adds a timer only for budgeted response-body reads. Custom-header validation happens at write time, and checker-side header filtering is proportional to the small configured header map size.

Benchmarks were refreshed locally from clean `v2` and this branch. CPU: `13th Gen Intel(R) Core(TM) i5-1340P`. A default-benchtime pass was noisy, so the comparable table below uses `-test.benchtime=3s -test.count=3 -test.cpu=1`; values are medians.

| Benchmark | v2 | this branch | delta |
|---|---:|---:|---:|
| `BenchmarkCheckNoKeywordLargeBody` | 901,947 ns/op, 1,078,171 B/op, 180 allocs/op | 857,896 ns/op, 1,078,201 B/op, 181 allocs/op | -4.9% time, +30 B, +1 alloc |
| `BenchmarkCheckKeywordLargeBody` | 2,339,498 ns/op, 4,372,204 B/op, 225 allocs/op | 2,153,608 ns/op, 4,372,397 B/op, 226 allocs/op | -7.9% time, +193 B, +1 alloc |
| `BenchmarkProbeTargetSafetyCached` | n/a | 341.8 ns/op, 64 B/op, 2 allocs/op | added hot-path target guard |
| `BenchmarkParsePublicHTTPURL` | n/a | 327.6 ns/op, 192 B/op, 2 allocs/op | API / Veriflier admission guard |
| `BenchmarkProbeTargetSafetyBlockedLiteral` | n/a | 578.9 ns/op, 272 B/op, 5 allocs/op | unsafe literal rejection |

The large-body benchmarks are intentionally conservative and noisy: they include `httptest`, large response-body reads, and do not enable the target-safety flag. The direct safety benchmark is the better estimate for the added per-check validation cost after DNS cache warmup: about 0.34 microseconds and 64 bytes per check. A previous draft created a fresh resolver during validation and measured around 23 microseconds / 4.8 KiB per cached check; this branch now reuses the checker resolver and transport cache.

## Probe-Safety Event Options

Current branch behavior is intentionally conservative:

- Unsafe direct targets and unsafe redirects return `ErrorProbeSafety`.
- The checker marks them non-failures for downtime purposes.
- The orchestrator writes `probe_safety_blocked` audit rows and metrics.
- No customer downtime event is opened, promoted, or closed.

Product options:

1. Keep audit + metrics only. This is lowest risk for rollout and avoids confusing unsafe URL cleanup with customer downtime. It is weaker operationally because dashboards and event consumers must know to inspect audit rows.
2. Add a first-class non-downtime event state, for example `Probe Blocked` or `Probe Safety Blocked`, with severity 2 and `check_type=probe_safety`. It should be excluded from SLA downtime and WPCOM down/recovery notifications. This is my recommendation if operators need persistent visibility into unsafe legacy rows without paging customers.
3. Add a separate stored label/deactivation reason for cleanup. The current cleanup command can deactivate unsafe rows but the v1 table has no explicit reason column. An additive v2-side table such as `jetmon_site_safety_flags` could preserve `site_id`, reason, first_seen, last_seen, and remediation status without mutating v1 semantics beyond `monitor_active=0`.
4. Treat public-host hostile responses as degraded states only after repeat evidence. Oversized headers, body-read budget exhaustion, and TLS pathology can be real site problems or adversarial behavior. If represented as events, they should use thresholds and cooldowns to avoid opening transient one-probe noise.

API behavior should remain strict regardless of which product option is chosen: new or updated monitor URLs should be rejected at write time when they are shape-unsafe, and runtime target-safety should remain in place for already-stored rows and DNS changes.

## Probe Error-Code Contract

The Veriflier maps checker probe-safety blocks to `OutcomeUnknown` so unsafe targets are not treated as regional proof of customer-site downtime. Today that mapping uses a private wire-compatible copy of the checker error code and a test asserts it stays equal to `checker.ErrorProbeSafety`. This avoids adding a production dependency from `internal/veriflier` back to `internal/checker` for one constant.

If more checker result fields become shared protocol semantics, move the stable error-code constants into a neutral contract package used by both checker and Veriflier. That change is worth doing when there are multiple duplicated result constants, when a non-Go transport starts consuming the same codes, or when the result schema needs versioning. Until then, the test guard is lower risk than a broader package split.

## Remaining High-Value Scenarios

1. Decide whether to implement `Probe Safety Blocked` as a non-downtime event state before rollout, or keep the current audit/metrics-only behavior for this branch.
2. If cleanup will be run on production data, decide whether deactivation alone is acceptable or whether an additive reason table is needed first.
3. Add a scheduled or operator-invoked cleanup dry-run report so the unsafe legacy row count can be watched before and after API rejection rolls out.
3. Exercise DNS rebinding with an authoritative test DNS responder: public address on first lookup, private address on a redirect hop or later check.
4. Exercise TLS pathology with uptime-bench responders: TLS 1.0/1.1, no common cipher, handshake close/alert, large certificate chains, expired/self-signed/hostname mismatch certificates.
5. Consider streaming keyword matching that can stop as soon as a required-only keyword is found instead of reading until EOF/limit/budget.
