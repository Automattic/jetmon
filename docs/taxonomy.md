# Jetmon Detection Taxonomy

This reference covers what Jetmon monitors, how checks are grouped, and how
findings are mapped into site/endpoint state. Detailed database schema lives in
[data-model.md](data-model.md), and detailed event mechanics live in
[events.md](events.md).

**Scope of this document:** what Jetmon tests and how it models the results. Not covered: customer-facing UX, alert notification design, billing, or implementation details beyond architectural decisions. Those belong in separate documents.

---

## Part 1: The Five-Layer Test Taxonomy

The five layers follow the path a request takes from user to server: **Reachability → Transport & Security → Infrastructure & Edge → Application Response → Content Integrity**. A sixth section covers **Reverse Checks** — monitoring where the monitored system reports to us rather than us probing it.

Each test is tagged with an implementation version and can also be tagged by *scope* (single-site, wide outage, architectural) for incident severity and alerting.

### Version labels

- **[v1]** — Table stakes. Low complexity, high value. Competitor free tiers have these.
- **[v2]** — Clear next step. Moderate complexity, noticeably expands coverage.
- **[v3]** — Advanced coverage. Higher complexity or requires dedicated infrastructure (multi-region fleet, headless browsers, baselining).
- **[v4]** — Deferred beyond v3. Often gated on an external dependency: integration, partnership, prerequisite feature, or demand signal. May be split into v5 or later during future roadmap planning if v4 grows too large.
- **[future]** — Genuinely hard, niche, or requires architectural rethinking. Knowable, not schedulable.

### Assumed infrastructure milestones

- **v1 probing:** single-region, HTTP(S), DNS, TCP, basic TLS inspection
- **v2 probing:** multi-region probe fleet, network timing breakdown, expanded protocol support
- **v3 probing:** headless browser fleet, baseline learning, cross-site correlation
- **Jetpack agent:** already installed on target sites; basic reverse-check reporting is v1-achievable

### A note on layer boundaries

Failures often surface at one layer but originate at another. A CDN returning 522 is detected at Layer 3, but the root cause is a Layer 1 or Layer 2 failure between edge and origin. An expired cert at the origin (Layer 2) can manifest as a 502 at the edge (Layer 3). Tag tests by **where the monitor observes the failure**, not where the root cause ultimately lives. Root-cause attribution is tracked separately via causal links between events (see Part 3).

---

## Layer 1: Reachability

Can the monitor reach the site at all? These failures happen before any connection is established.

### Domain and registry
- **[v2]** Domain expired at registrar
- **[v2]** Domain approaching expiration (warning threshold, e.g., <30 days)
- **[v3]** Registrar lock status changed unexpectedly
- **[v3]** WHOIS/RDAP query failures
- **[v3]** Nameserver delegation mismatch (parent zone NS records don't match child zone)
- **[v2]** Domain suspended or in client/server hold status

### DNS resolution
- **[v1]** NXDOMAIN for apex and `www` subdomain
- **[v1]** SERVFAIL from authoritative nameservers
- **[v1]** Timeout contacting authoritative nameservers
- **[v1]** Resolver returns REFUSED
- **[v2]** DNSSEC validation failure (bogus signatures, expired signatures, broken chain of trust)
- **[v2]** CNAME chain exceeds resolver depth limit
- **[v1]** CNAME pointing to NXDOMAIN target

### DNS configuration
- **[v1]** Missing A record
- **[v1]** Missing AAAA record when IPv6 is expected
- **[v2]** A/AAAA records pointing to unreachable or parked IPs
- **[v2]** Round-robin DNS with one or more dead backends
- **[v3]** Geo-DNS returning wrong region's endpoint
- **[v3]** TTL set pathologically low (thrash) or high (stale after cutover)
- **[v4]** Missing or misconfigured MX/TXT records affecting site-adjacent services (SPF, DMARC, domain verification)
- **[future]** Split-horizon DNS mismatch (internal vs. external resolution differs)

### Network-layer connectivity
- **[v1]** IPv4 unreachable from monitor vantage point
- **[v1]** IPv6 unreachable when AAAA is published (common silent failure)
- **[v2]** Asymmetric IPv4/IPv6 behavior (one works, one doesn't)
- **[v2]** ICMP unreachable from upstream router
- **[future]** BGP route withdrawal affecting destination prefix
- **[v3]** MTU/PMTUD blackhole (small packets succeed, large fail)

### Geographic and network-path reachability
- **[v2]** Reachable from one region but not another *(requires multi-region probe fleet)*
- **[v3]** ASN-level block (monitor's ASN blackholed at destination)
- **[v2]** Country-level block or government-level filtering
- **[v3]** Upstream transit provider outage affecting subset of vantage points
- **[v4]** Origin IP listed on major blocklists (Spamhaus, SORBS, etc.)
- **[future]** CDN/origin IP nullrouted by major ISP

---

## Layer 2: Transport & Security

The connection itself — TCP, TLS, and the cryptographic handshake.

### TCP
- **[v1]** Connection refused (port closed)
- **[v1]** Connection reset mid-handshake
- **[v1]** Connection timeout (SYN with no SYN-ACK)
- **[v2]** Half-open connections (handshake completes but no data flows)
- **[v1]** Slow handshake exceeding threshold

### TLS handshake
- **[v1]** TLS handshake failure (generic)
- **[v2]** Unsupported protocol version mismatch
- **[v2]** No common cipher suite
- **[v2]** SNI mismatch (wrong vhost served)
- **[v2]** TLS alert parsing: `handshake_failure`, `protocol_version`, `unrecognized_name`

### Certificate validity
- **[v1]** Expired certificate
- **[v1]** Not-yet-valid certificate (clock skew or premature deployment)
- **[v1]** Certificate hostname mismatch (CN/SAN doesn't cover requested host)
- **[v1]** Self-signed certificate in production
- **[v1]** Certificate signed by untrusted CA
- **[v1]** Missing intermediate certificate(s) — chain incomplete
- **[v2]** Revoked certificate (CRL or OCSP says revoked)
- **[v2]** Weak signature algorithm (SHA-1, MD5)
- **[v2]** Key too short (RSA < 2048)

### Certificate operational issues
- **[v2]** OCSP stapling broken or returning `unknown`/`revoked`
- **[v3]** Certificate Transparency: cert not logged
- **[v1]** Approaching expiration (warning threshold, e.g., <30 days)
- **[v2]** HSTS header missing when expected
- **[v3]** HSTS `max-age` too low or preload list drift

### HTTPS enforcement
- **[v1]** Port 80 not redirecting to 443
- **[v1]** HTTPS not supported at all
- **[v3]** Mixed-content: HTTPS page loads HTTP assets
- **[v2]** HTTP/2 or HTTP/3 negotiation failures when advertised

### Other transport protocols
- **[v3]** WebSocket upgrade failures
- **[future]** gRPC connection or deadline-exceeded failures
- **[v4]** SMTP/IMAP/POP port availability
- **[v4]** Other TCP services (SSH, FTP, database ports)

---

## Layer 3: Infrastructure & Edge

The systems between the internet and the origin server.

### CDN and edge provider
- **[v1]** CDN returning its own error page (Cloudflare 520–526)
- **[v2]** CDN origin-unreachable errors
- **[v4]** Cloudflare/Fastly/Akamai/CloudFront provider-level outage detection
- **[v3]** Cache serving stale error responses
- **[v3]** Cache poisoning (wrong content served from edge)

### Cloud provider
- **[v4]** AWS/GCP/Azure region outage detection
- **[v3]** Managed database failure surfacing as application error
- **[v2]** Object storage outage affecting media

### Load balancer
- **[v1]** Load balancer entirely unreachable
- **[v2]** One or more backends dead but still in rotation
- **[v2]** Stale backend serving old code/content
- **[v3]** Uneven distribution (one backend getting 90% of traffic)
- **[v3]** Session affinity broken
- **[v2]** SSL termination issues at LB (cert mismatch between LB and origin)
- **[future]** LB health checks misconfigured

### WAF, bot protection, and rate limiting
- **[v1]** WAF false-positive blocking monitor (403)
- **[v1]** Bot-protection challenge page served instead of content
- **[v1]** Rate limiting triggered on monitor (429)
- **[v2]** IP reputation block (monitor IP flagged)
- **[v2]** Geoblocking misconfigured

### DDoS and traffic management
- **[v2]** DDoS protection in "under attack" mode serving challenges
- **[v3]** Anycast misrouting (traffic landing in wrong PoP)

---

## Layer 4: Application Response

The server accepts the connection and speaks HTTP — but does it respond correctly and promptly?

### Connection-level HTTP failures
- **[v1]** TCP connection accepted, no HTTP response sent (hang)
- **[v1]** Response timeout (server slow to first byte beyond threshold)
- **[v1]** Connection closed mid-response (truncated body)
- **[v2]** Invalid HTTP framing (bad Content-Length, chunked encoding errors)

### Status code anomalies
- **[v1]** 5xx responses (500, 502, 503, 504)
- **[v2]** Intermittent 5xx at elevated rate (e.g., >1% of requests)
- **[v1]** 4xx on canonical URLs that should succeed (404 on homepage)
- **[v1]** 401/403 on public pages
- **[v1]** Method inconsistency: HEAD returns 200 but GET returns 4xx/5xx
- **[v1]** Method inconsistency: GET succeeds but HEAD returns 405
- **[v2]** OPTIONS preflight failures affecting CORS-dependent pages

### Network timing breakdown
- **[v1]** Total response time exceeds threshold
- **[v1]** Time to First Byte (TTFB) exceeds threshold
- **[v2]** DNS lookup time exceeds threshold
- **[v2]** TCP connect time exceeds threshold
- **[v2]** TLS handshake time exceeds threshold
- **[v2]** Content download time exceeds threshold
- **[v2]** Response size anomalies (much smaller or larger than baseline)
- **[v3]** Slow-loris-style responses (bytes trickle in over long duration)

### Redirect behavior
- **[v1]** Redirect loop (A → B → A)
- **[v1]** Redirect chain too long (>5 hops)
- **[v1]** Redirect to wrong host
- **[v1]** HTTPS → HTTP downgrade in redirect chain
- **[v2]** Redirect strips path or query string when it shouldn't
- **[v3]** 301 when 302 expected, or vice versa

### Header anomalies
- **[v1]** Missing `Content-Type`
- **[v2]** Wrong `Content-Type` (HTML served as `text/plain`)
- **[v2]** Missing security headers when expected
- **[v3]** Malformed `Cache-Control` causing CDN misbehavior
- **[v3]** Excessive cookie size breaking downstream proxies

---

## Layer 5: Content Integrity

The response is valid HTTP — but is the payload actually correct? Layer 5 splits into two classes:

- **Correctness failures** — the payload is wrong regardless of who requested it or when. Detected by inspecting a single response.
- **Consistency failures** — the payload looks fine in isolation but is wrong *for this request* (wrong user's view, wrong region's content, stale cache). Detected by comparing across requests or against expected invariants.

### Correctness: silent application failures
- **[v1]** CMS fatal error rendered with 200 OK (WSOD)
- **[v1]** "Error establishing a database connection" served as HTML with 200
- **[v1]** PHP fatal errors or stack traces in response body
- **[v1]** White-screen-of-death (empty or near-empty body, 200 OK)
- **[v1]** WordPress setup/configuration page served to visitors
- **[v2]** Python/Ruby/Node tracebacks leaked to response body

### Correctness: maintenance and transitional states
- **[v1]** Maintenance mode page served with 200 (should be 503 with Retry-After)
- **[v1]** Holding page from registrar/host
- **[v1]** Default server welcome page (nginx, Apache, IIS default)
- **[v1]** Jetpack probe echo page served as the homepage
- **[v2]** "Coming soon" or placeholder content served unexpectedly

### Correctness: security-relevant content
- **[v2]** Defacement (body diff against baseline exceeds threshold)
- **[v2]** Injected spam links or SEO spam
- **[v3]** Injected cryptominer or malicious JavaScript
- **[v3]** Phishing content replacing legitimate pages
- **[v2]** Admin/debug pages exposed publicly (`/wp-admin` accessible without auth, `.env` served)

### Correctness: content completeness
- **[v1]** Expected string/marker present (canary text)
- **[v2]** Missing critical element (no `<title>`, empty `<body>`)
- **[v2]** Response body significantly smaller than baseline
- **[v3]** Broken HTML structure (unclosed tags affecting render)
- **[v3]** Missing or broken critical assets referenced by page (CSS/JS 404s)

### Correctness: structured data
- **[v2]** JSON API returning HTML error page
- **[v2]** XML/RSS feed malformed
- **[v2]** Sitemap returning 200 but empty or malformed
- **[v2]** `robots.txt` missing or returning HTML

### Consistency: cache and routing
- **[v2]** Wrong vhost served (different site's content on this domain)
- **[v3]** Cache poisoning (one user's content served to another)
- **[future]** A/B test or feature flag stuck in wrong state
- **[v3]** Localized content served to wrong region
- **[v3]** Logged-in view served to anonymous monitor (cache key bug)
- **[v2]** Stale content served long after origin update

### Client-side rendering (rendered-DOM checks)
*All items require headless browser infrastructure.*
- **[v3]** SPA fails to hydrate (initial HTML loads, JS fails)
- **[v3]** Client-side routing broken
- **[v3]** JavaScript errors in console exceeding threshold
- **[v3]** Core Web Vitals regression (LCP, CLS, FID)

### Third-party dependency failures
*All items require rendered-DOM inspection.*
- **[v3]** Critical external JS failing to load
- **[v3]** Payment processor SDK unavailable (Stripe, PayPal)
- **[v3]** Font provider outage affecting rendering
- **[v3]** CDN for assets failing (jsDelivr, unpkg)
- **[v3]** Embedded content broken (YouTube, Vimeo, social embeds)

---

## Reverse Checks: Agent-Reported Monitoring

Probe-based monitoring asks "is the site up from outside?" Reverse checks flip the direction: the monitored system reports *to us*, and silence means failure. This is fundamentally a different detection model.

**Why this matters for Jetmon:** Jetpack's position inside WordPress means it can act as an authenticated agent on the site itself, reporting signals that external probes cannot see. Most of these are v1/v2 precisely *because* Jetpack is already on-site.

### Heartbeat and dead-man's-switch
- **[v1]** Site fails to check in within expected interval
- **[v1]** Grace-period exhaustion (missed enough check-ins to declare down)
- **[v2]** Heartbeat interval drift (checking in late)
- **[v3]** Heartbeat from unexpected location or with unexpected payload

### WordPress cron (wp-cron) and scheduled tasks
- **[v1]** `wp-cron.php` not firing
- **[v2]** Scheduled events backlogging (queue depth growing)
- **[v2]** Individual recurring events failing repeatedly
- **[v3]** Plugin-registered cron jobs silently failing
- **[v2]** Post scheduled for publication but not published

### Background jobs and queues
- **[v2]** Action Scheduler queue depth exceeding threshold
- **[v2]** Failed jobs accumulating
- **[v3]** Job processing time regression
- **[v4]** Specific critical jobs not completing

### Application-internal health signals
- **[v2]** PHP error log rate exceeding threshold
- **[v3]** Database slow-query rate exceeding threshold
- **[v3]** Cache hit rate dropping below expected baseline
- **[v2]** Memory usage approaching PHP limit
- **[v2]** Disk space approaching full on uploads directory
- **[v3]** Database connection pool exhaustion

### Security and integrity signals
- **[v4]** File integrity changes in core or plugin files — *likely overlaps with Jetpack Scan*
- **[v2]** Unexpected admin user creation
- **[v4]** Failed login rate spike — *likely overlaps with Jetpack Protect*
- **[v2]** Plugin/theme update failures
- **[v1]** WordPress core, plugin, or theme out of date beyond threshold

### Deployment and configuration drift
- **[v1]** PHP version approaching EOL
- **[v1]** WordPress version outdated
- **[v2]** Critical plugin disabled unexpectedly
- **[v2]** Site URL or home URL changed
- **[v1]** Debug mode enabled in production

---

## Part 2: Site, Endpoint, And Check Model

Jetmon uses a three-level hierarchy rather than a flat monitor model:

- **Site:** the WordPress site a customer thinks they own.
- **Endpoint:** a specific monitored URL or surface on that site.
- **Check:** one probe or detector running against a site or endpoint.

Some checks are site-level, such as domain expiration, DNS configuration,
shared TLS certificate validity, and reverse checks. Others are endpoint-level,
such as HTTP status, body patterns, TTFB, redirects, and headers. Keeping those
separate prevents confusing states like "homepage down" being treated as the
same fact as "whole site down."

Rollup is explicit: endpoint and check results roll up into site state by
policy, not by hardcoded assumptions. Critical endpoints can affect site state
directly, while non-critical endpoints can remain warnings or degradations.
See [data-model.md](data-model.md) for the implemented schema and current
rollout constraints.

---

## Part 3: State And Event Model

Jetmon uses a multi-state vocabulary:

- **Up:** all relevant checks passing.
- **Warning:** attention needed, but not user-facing downtime.
- **Degraded:** content is served, but some checks fail or exceed thresholds.
- **Seems Down:** first failure detected, awaiting confirmation.
- **Down:** confirmed failure on critical checks.
- **Paused:** monitoring suspended.
- **Maintenance:** maintenance window active.
- **Unknown:** Jetmon cannot determine state without blaming the customer site.

`Unknown` is deliberately separate from `Down`: monitor-side failures, network
loss from a probe region, or agent silence must not be reported as customer-site
downtime.

Events are the source of truth. The current incident row lives in
`jetpack_monitor_events`; every mutation is appended to
`jetpack_monitor_event_transitions` in the same transaction. Severity is numeric
and comparable. State is human-readable lifecycle. A `Seems Down` incident that
is verifier-confirmed updates in place to `Down`, preserving the original
`started_at`.

Full event semantics, schema fields, transition reasons, metadata, and
invariants live in [events.md](events.md). Architectural rationale is captured
in [ADR-0001](adr/0001-event-sourced-state-model.md).

---

## Part 4: The Scope Matrix

Every test can be tagged along two axes: **layer** (what the monitor detected) and **scope** (how broadly the failure affects customers). Scope drives alerting severity and on-call routing.

### Scope definitions

- **Single-site** — affects one customer site only. Typical alert: notify site owner.
- **Wide-outage** — affects many sites simultaneously (provider-level). Typical alert: notify site owners *and* surface on provider status page; suppress duplicate individual alerts to reduce noise.
- **Architectural** — reveals a structural problem in the customer's own setup that will recur without intervention. Typical alert: notify site owner with remediation guidance, not just "down."

### Representative tests mapped by layer × scope

| Layer | Single-site | Wide-outage | Architectural |
|-------|-------------|-------------|---------------|
| L1 Reachability | Expired domain [v2] | DNS provider outage [v4] | Round-robin with dead backend [v2] |
| L2 Transport & Security | Expired certificate [v1] | Root CA distrust event [future] | Persistent missing intermediate cert [v1] |
| L3 Infrastructure & Edge | Origin down [v1] | Cloudflare regional outage [v4] | LB with stale backend in rotation [v2] |
| L4 Application Response | 500 on homepage [v1] | CDN-wide 5xx spike [v4] | HEAD/GET method mismatch on every page [v1] |
| L5 Content Integrity | Defacement [v2], WSOD [v1] | Shared-host theme injection [v3] | Cache key bug serving logged-in views [v3] |
| Reverse | wp-cron stopped on one site [v1] | Update server outage affecting all sites [v2] | wp-cron disabled in favor of unconfigured system cron [v2] |

### Why two axes matter

Slicing by layer answers "do we have coverage gaps?" Slicing by scope answers "how should we alert?" A wide-outage event that generates thousands of single-site alerts is an incident-response failure even if detection worked perfectly.

---

## Part 5: Detection Methodology by Layer

Each layer corresponds to distinct monitoring techniques:

- **Layer 1** → DNS queries, TCP probes, registrar/WHOIS queries from multiple vantage points
- **Layer 2** → TLS inspection, certificate parsing, cipher negotiation, non-HTTP protocol probes
- **Layer 3** → HTTP requests with edge-specific response parsing, status page integration
- **Layer 4** → Full HTTP request/response with network timing breakdown and header analysis
- **Layer 5** → Response body inspection (raw HTML *and* rendered DOM, depending on the class of failure)
- **Reverse** → Inbound API endpoints, heartbeat tracking, agent-reported signal ingestion

A test suite covering all five layers via only raw-HTML inspection still misses SPA failures and third-party dependency breakage. A suite covering all five layers via probes still misses cron death and background job failure. Coverage analysis has to consider technique as well as category.

---

## Part 6: Signal Processing and False-Positive Suppression

Detecting a failure is half the problem; deciding it's real enough to open an event or escalate its severity is the other half.

- **[v1]** Retry-on-failure — confirm with a second check before promoting Seems Down to Down
- **[v1]** Maintenance windows — suppress alerts and event creation during scheduled work
- **[v1]** Basic flap suppression — debounce rapid up/down transitions
- **[v2]** Multi-location confirmation — require failure from N of M vantage points before promoting *(requires multi-region)*
- **[v2]** Alert recurrence rules — how often to re-alert during a sustained incident
- **[v3]** Dependency suppression — if a provider-level outage event is active, suppress individual-site events explained by it (via the causal link field)
- **[v3]** Baseline-aware thresholds — what counts as "slow" depends on the site's historical baseline

These aren't tests themselves, but they determine whether a check's findings become events and whether events become alerts.

---

## Part 7: Version Summary

| Version | Approximate count | Character |
|---------|-------------------|-----------|
| v1 | ~55 items | Ship-worthy baseline monitor |
| v2 | ~55 items | Competitive parity with established solutions |
| v3 | ~40 items | Enterprise differentiation |
| v4 | ~12 items | Deferred beyond v3, often gated on integrations/partnerships/demand |
| future | ~10 items | Known-unscoped |

**v1 as a coherent product:** the v1 set alone gives you DNS/TCP/TLS basics, core HTTP status and timing checks, essential content-integrity patterns (WSOD, DB errors), WordPress-specific reverse checks (wp-cron, core updates, debug mode), and baseline false-positive suppression. That's a credible launch.

**v2 as competitive parity:** adds multi-region probing, network timing breakdown, domain expiration, expanded cert checks, maintenance-page detection, and richer reverse checks. Feature-competitive with Pingdom/UptimeRobot for WordPress sites.

**v3 as differentiation:** headless browser checks, third-party dependency monitoring, cache-consistency detection, baseline learning, and advanced cross-site correlation. Enterprise-tier territory.

---

## Part 8: Out of Scope / Adjacent Concerns

Things a complete monitoring story eventually needs, but which sit outside this taxonomy:

- **[v4]** Transaction / multistep monitoring — scripted user flows (login, checkout, publish a post). Distinct because the failure mode is "step 3 of 5 broke" rather than a single request failing.
- **[future]** Real User Monitoring (RUM) — captures actual user sessions rather than synthetic probes.
- **[future]** Capacity and load testing — "works at 10 rps, collapses at 100 rps" is a real failure class but not an uptime concern.
- **[future]** Application Performance Monitoring (APM) — in-process tracing, slow-query identification, code-level profiling.
- **[future]** Log aggregation and anomaly detection.

The taxonomy above should compose cleanly with any of these rather than trying to absorb them.

---

## Appendix: Decisions To Remember

- Organize checks by where Jetmon observes the failure, not where root cause is
  eventually found.
- Keep layer, scope, and severity separate. They answer different operational
  questions.
- Model sites, endpoints, and checks separately.
- Keep `Unknown` separate from `Down`.
- Treat events as source of truth and keep legacy site status as a projection
  during rollout.
- Keep severity numeric and state human-readable.
- Promote `Seems Down` to `Down` in place after Veriflier confirmation.
- Record resolution reason on close.
- Keep causal links separate from hierarchical rollup.
- Make timeouts and policy decisions per-site where practical.
- Prefer stable enum values for error types over free-text strings.
