# Jetmon Test Taxonomy and Architecture Reference (v4)

A comprehensive reference covering what Jetmon monitors, how it organizes those checks, and the underlying state and event model. This consolidates the five-layer test taxonomy, the scope matrix, the site/endpoint hierarchy, the state vocabulary, and the event-sourced architecture into a single reference document.

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
- **[v2]** Python/Ruby/Node tracebacks leaked to response body

### Correctness: maintenance and transitional states
- **[v2]** Maintenance mode page served with 200 (should be 503 with Retry-After)
- **[v2]** "Coming soon" or placeholder content served unexpectedly
- **[v2]** Holding page from registrar/host
- **[v2]** Default server welcome page (nginx, Apache, IIS default)

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

## Part 2: The Data Model — Site, Endpoint, Check

Jetmon uses a three-level hierarchy modeled on Atlassian Statuspage rather than the flat monitor model used by UptimeRobot and Pingdom. This hierarchy is the conceptual frame for everything else in this document.

### Entities

- **Site** — the top-level entity a customer mentally owns ("my WordPress site at example.com"). A site has one canonical domain and an associated Jetpack-connected WordPress installation.
- **Endpoint** — a specific URL or surface being monitored on the site. Typical endpoints include the homepage, login page, REST API root, feed, sitemap, and any customer-specified URLs.
- **Check** — an individual test running against a site or endpoint. Each check is one of the items from the five-layer taxonomy above.

### Site-level vs. endpoint-level checks

Some checks belong to the site as a whole; others belong to specific endpoints. This distinction is structural and affects the data model.

**Site-level checks** apply regardless of which endpoint you probe:
- Domain expiration
- DNS configuration (A/AAAA records, CNAME chain)
- TLS certificate validity (shared across endpoints on the same domain)
- All Reverse Checks (wp-cron, PHP version, disk space, etc.)

**Endpoint-level checks** are specific to a URL:
- HTTP status code
- Response body content patterns
- TTFB and other per-request timing
- Redirect behavior
- Header anomalies

Mixing these creates confusion ("my homepage is down but my site is up?"). The data model reflects this split explicitly: checks have a `target_type` of either `site` or `endpoint`, and site-level events cannot be attributed to a specific endpoint.

### Rollup

Site-level state rolls up from endpoint-level state, which rolls up from individual check results. Rollup rules are **explicit and configurable**, not hardcoded. Ship with sensible defaults (worst-child for critical endpoints, warning-promotion for non-critical ones) but let them be overridden per site. A site owner might reasonably say "the homepage being down means the site is down, but the feed being down is just Degraded."

Specific rollup decisions to expose as config:
- Which endpoints are "critical" (affect site state directly) vs. "non-critical" (promote to warning only)
- Whether site-level check failures (cert, domain, DNS) always set site state or can be overridden per check type
- How Reverse Check events roll up — typically these are their own category that surfaces independently of probe-based state

---

## Part 3: State Model and Event Architecture

### State vocabulary

Jetmon uses a multi-state vocabulary rather than binary up/down:

- **Up** — all checks passing
- **Warning** — something needs attention but isn't user-facing yet (cert expiring in 14 days, WordPress version behind, wp-cron backing up)
- **Degraded** — some checks failing or timing thresholds exceeded, but site is serving content (missing security headers, slow TTFB, one of several endpoints failing)
- **Seems Down** — first failure detected, awaiting verifier confirmation (transient, auto-resolves to Down or Up within minutes)
- **Down** — confirmed failures on critical checks
- **Paused** — monitoring suspended by user
- **Maintenance** — scheduled maintenance window active
- **Unknown** — monitor couldn't determine state (monitor-side failure, agent not reporting, first check pending)

The Warning/Degraded split matters because they route differently in alerting: Degraded might page an on-call engineer; Warning is a daily-digest email. UptimeRobot's API omits this distinction and users frequently ask for it — worth building in from the start.

The Unknown state is critical for honesty: **monitor-side failures should never be reported as customer-site downtime**. If the probe itself crashes, the region loses network, the rate limit hits, or the Jetpack agent stops reporting — these are Unknown, not Down. Conflating these erodes trust quickly.

### Event-sourced model

Jetmon uses an event-sourced architecture where **events are the source of truth** and state is derived.

**Why events over a single state field:**

1. **Multiple concurrent issues.** A site can have an expiring cert (Warning), a failing endpoint (Degraded), and wp-cron stopped (Warning) all at once. A state field collapses this to one value and loses the others. Events keep all three distinct, visible, and separately resolvable.

2. **Incident timeline.** "Was the cert expiration warning active before or after the first 5xx spike?" is a standard postmortem question. Events with start/end timestamps answer it natively.

3. **Root-cause attribution.** Failures at one layer often originate at another. Events can link causally: the Layer 3 "CDN 522" event references the Layer 1 "origin unreachable" event as its likely cause.

### Schema shape

```
events (source of truth):
  id
  site_id
  endpoint_id (nullable — null for site-level events)
  check_type
  severity (numeric, comparable)
  state (human-readable category)
  started_at
  ended_at (nullable — null for active events)
  cause_event_id (nullable — causal link, separate from hierarchical rollup)
  resolution_reason (nullable — why the event closed)
  metadata (JSON — check-specific data)

sites (includes derived state for fast reads):
  id
  ...
  current_state
  current_state_updated_at
  active_event_count
  worst_active_severity
```

**Key design decisions:**

- **Events are the source of truth; derived state is denormalized onto the site row for read performance.** Update both transactionally — the derived state should never write without a corresponding event write, and vice versa.

- **Severity and state are separate fields.** Severity is the numeric, comparable value used for rollup (e.g., 1=Warning, 2=Degraded, 3=Seems Down, 4=Down). State is the human-readable category. Keeping them separate lets you add new states without breaking rollup logic.

- **Seems Down to Down is an immutable close+open transition.** When verifier confirmation arrives, close the open Seems Down event and open a new confirmed Down event in the same transaction. Copy the original `started_at` into the Down event so incident duration still starts at first failure, not at verifier confirmation.

- **Event identity is idempotent.** If the same check fails twice in a row, it's the same event, not two events. Key events by `(site_id, endpoint_id, check_type, [optional discriminator])` so repeated detection of the same failure updates the existing open event rather than creating a new one. Deduplication logic lives in the shared probe runner, not in individual checks.

- **Resolution reason is recorded on close.** When an event closes, record why: the check started passing, the user acknowledged and dismissed it, a maintenance window swallowed it, it was superseded by a broader event. This affects uptime calculations and report accuracy.

- **Causal links are separate from hierarchical rollup.** An endpoint-level event rolls up to site level (hierarchy). A Layer-3 CDN event caused by a Layer-1 DNS event is a different relationship (causation). Keep these as two separate fields. Conflating them creates weird bugs where dismissing a cause accidentally dismisses a rollup, or vice versa.

### The Seems Down flow

The Seems Down state is the key transient between first failure detection and verifier confirmation, and the event model accommodates it cleanly:

1. First failure detected → open event at severity Seems Down, `started_at` = now
2. Verifier runs (retry-on-failure, multi-location confirmation, etc.)
3a. If verifier confirms failure → **close Seems Down and open Down** in one transaction, carrying forward the original `started_at`
3b. If verifier succeeds → **close the event** with `resolution_reason = "false_positive"`, `ended_at` = now

This pattern makes "events that opened at Seems Down and closed without promotion" a direct measure of detection noise, useful for tuning false-positive rates.

### Check-level events vs. site-level state events

Two granularities of events, both stored:

- **Check-level events** answer "what specific thing broke?" — these are the primary events described above, one per failing check.
- **Site-level state events** answer "when did the customer experience degradation?" — these record transitions in the derived site state ("Site was Down from 14:02 to 14:17"). They're derived from check-level events but stored as their own first-class records for historical timeline views, uptime percentage calculations, and SLA reporting.

The rule: never write derived state without writing (or closing) the corresponding events. If the invariant holds, the representations can't drift. If the derived state is ever suspect, it can be recomputed from events and compared.

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

## Appendix: Decisions to Remember

A consolidated list of architectural decisions made across the conversation history of this project, for quick reference:

1. **Five-layer taxonomy (plus Reverse) organized by where the failure originates**, not by severity or scope.
2. **Layer boundaries are fuzzy by design**; events are tagged by where detected, not where originated, with causal links for root-cause attribution.
3. **Site → Endpoint → Check hierarchy** (Statuspage model), not flat monitors (UptimeRobot model).
4. **Site-level checks vs. endpoint-level checks are structurally distinct** in the data model.
5. **Rollup rules are explicit and configurable per site**, not hardcoded.
6. **Multi-state vocabulary:** Up, Warning, Degraded, Seems Down, Down, Paused, Maintenance, Unknown.
7. **Unknown state exists specifically to prevent monitor-side failures from being reported as customer-site downtime.**
8. **Event-sourced architecture** with derived site state denormalized for read performance.
9. **Severity and state are separate fields**; severity is numeric and comparable, state is human-readable.
10. **Seems Down transitions immutably** to Down via close+open on verifier confirmation; `started_at` stays at first-failure time.
11. **Event identity is idempotent** via `(site_id, endpoint_id, check_type, discriminator)`.
12. **Deduplication lives in the shared probe runner**, not in individual checks.
13. **Resolution reason is recorded on event close** for accurate uptime reporting.
14. **Causal links and hierarchical rollup are separate fields**.
15. **Both check-level events and site-level state events are stored**, at different granularities.
16. **Vantage-point ID is in the schema from v1** even though v1 is single-region.
17. **Timeouts are configurable per site**, not global.
18. **Error types have stable enum values**, not just strings.
