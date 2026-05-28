# Jetmon v1/v2 Operational Cost Comparison

This report compares the rollout stages Systems will care about first:

- v1 legacy
- v2 HEAD/legacy
- v2 GET/simple_http
- v2 GET/full with `CHECK_HISTORY_MODE_DEFAULT=status_change`
- v2 GET/full with check history `disabled`, `sample10`, and `all`

Percent changes are relative to v1 legacy.

Report root: `/home/gaarai/code/uptime-bench/reports/20260528T125345Z-monitor-systems-rollout-comparison-combined`

Generated: 2026-05-28

Source reports:

- Combined Monitor comparison: `/home/gaarai/code/uptime-bench/reports/20260528T125345Z-monitor-systems-rollout-comparison-combined`
- Base rollout comparison: `/home/gaarai/code/uptime-bench/reports/20260528T065939Z-monitor-systems-rollout-comparison-03359e6`
- Corrected check-history comparison: `/home/gaarai/code/uptime-bench/reports/20260528T105221Z-monitor-history-modes-after-history-writer-fix-03359e6-dirty`
- Veriflier capacity/resource sizing: `/home/gaarai/code/uptime-bench/reports/20260515T222700Z-jetmon-veriflier-resource-sizing-a9bc868`
- Veriflier real-URL bandwidth comparison: `/home/gaarai/code/uptime-bench/reports/20260518T152325Z-jetmon-veriflier-v1-bandwidth-same50k`

## Scope

Each Monitor run used the same test shape:

- 10,000 active internal target sites.
- 5-minute check interval.
- 30-minute run window.
- One simulated 503 event profile affecting 25 deterministic sites for 20 minutes, beginning 3 minutes into the run.
- WPCOM and live alert delivery disabled.
- Internal target hosts only, so target HTTP traffic measures Jetmon behavior without Internet variability.

This is a fixed-size operational-cost comparison, not a maximum-capacity ladder.
Historical Monitor capacity evidence exists, but it predates several later
production-readiness, schema, security, and rollout changes. It should be used
as context only. A fresh current-code sanity ladder would likely take 2-3 hours;
a current ceiling run with retries and resource capture would likely take 4-7+
hours or overnight.

## Run Status

| Mode | Run | Health | Target | DB | Network | Disk | Cleanup | Coverage | Requests |
| --- | --- | --- | --- | --- | --- | --- | --- | ---: | ---: |
| v1 legacy | run-v1-legacy-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 60,181 |
| v2 HEAD/legacy | run-v2-head-legacy-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 62,725 |
| v2 GET/simple_http | run-v2-get-simple-http-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 63,744 |
| v2 GET/full status_change | run-v2-get-full-status-change-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 62,687 |
| v2 GET/full history disabled | run-v2-get-full-history-disabled-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 62,675 |
| v2 GET/full history sample10 | run-v2-get-full-history-sample10-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 62,725 |
| v2 GET/full history all | run-v2-get-full-history-all-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 62,714 |

## Monitor Process Resources

| Mode | CPU avg | CPU p95 | RSS avg | FD avg | Threads avg |
| --- | ---: | ---: | ---: | ---: | ---: |
| v1 legacy | 4.37% core (+0.0%) | 4.89% core (+0.0%) | 4.55 GiB (+0.0%) | 1,872 (+0.0%) | 809 (+0.0%) |
| v2 HEAD/legacy | 2.51% core (-42.6%) | 3.12% core (-36.1%) | 115.34 MiB (-97.5%) | 78 (-95.8%) | 20 (-97.5%) |
| v2 GET/simple_http | 3.50% core (-19.8%) | 3.85% core (-21.2%) | 171.24 MiB (-96.3%) | 38 (-98.0%) | 21 (-97.4%) |
| v2 GET/full status_change | 3.02% core (-31.0%) | 3.50% core (-28.3%) | 179.31 MiB (-96.2%) | 84 (-95.5%) | 21 (-97.4%) |
| v2 GET/full history disabled | 3.04% core (-30.5%) | 3.47% core (-29.0%) | 184.81 MiB (-96.0%) | 86 (-95.4%) | 21 (-97.4%) |
| v2 GET/full history sample10 | 3.08% core (-29.5%) | 3.51% core (-28.1%) | 184.37 MiB (-96.0%) | 81 (-95.7%) | 21 (-97.4%) |
| v2 GET/full history all | 3.18% core (-27.2%) | 3.58% core (-26.7%) | 181.94 MiB (-96.1%) | 86 (-95.4%) | 21 (-97.3%) |

V2 process cost is substantially lower than v1 in every rollout stage. The
biggest operational gains are memory, file descriptors, and thread count.

## Monitor Database Work

The byte columns below use MySQL server perspective:

- `DB client->server` is MySQL `Bytes_received`: data the DB server received from Jetmon.
- `DB server->client` is MySQL `Bytes_sent`: data the DB server sent back to Jetmon.

| Mode | Queries | Writes | Rows read | Rows added | Rows updated | DB client->server | DB server->client |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| v1 legacy | 136 (+0.0%) | 50 (+0.0%) | 79,398,087 (+0.0%) | 0 | 50 (+0.0%) | 17.19 KiB (+0.0%) | 4.36 MiB (+0.0%) |
| v2 HEAD/legacy | 47,521 (+34,841.9%) | 1,699 (+3,298.0%) | 398,453,027 (+401.8%) | 1,167 | 20,403 (+40,706.0%) | 6.39 MiB (+37,958.8%) | 30.03 MiB (+588.0%) |
| v2 GET/simple_http | 47,180 (+34,591.2%) | 1,590 (+3,080.0%) | 402,667,432 (+407.2%) | 1,099 | 16,134 (+32,168.0%) | 5.99 MiB (+35,552.3%) | 29.53 MiB (+576.6%) |
| v2 GET/full status_change | 47,428 (+34,773.5%) | 1,669 (+3,238.0%) | 402,678,229 (+407.2%) | 1,160 | 20,399 (+40,698.0%) | 6.41 MiB (+38,064.8%) | 29.85 MiB (+583.9%) |
| v2 GET/full history disabled | 47,063 (+34,505.1%) | 1,550 (+3,000.0%) | 398,461,844 (+401.9%) | 1,034 | 20,393 (+40,686.0%) | 6.36 MiB (+37,789.0%) | 29.80 MiB (+582.7%) |
| v2 GET/full history sample10 | 52,257 (+38,324.3%) | 3,280 (+6,460.0%) | 402,680,417 (+407.2%) | 7,803 | 20,395 (+40,690.0%) | 7.74 MiB (+46,013.5%) | 31.83 MiB (+629.3%) |
| v2 GET/full history all | 52,265 (+38,330.1%) | 3,284 (+6,468.0%) | 402,688,273 (+407.2%) | 63,718 | 20,393 (+40,686.0%) | 16.46 MiB (+97,906.8%) | 47.67 MiB (+992.2%) |

The HEAD/legacy HTTP behavior is close to v1, but the persistence model is not.
V2 records runtime projections, event state, transitions, audit evidence,
process health, Veriflier health, and delivery progress. That explains why DB
work is much higher even when the network check method is still HEAD.

## Table Row Deltas

These are net row-count changes across the run. They are the cleanest table
growth signal in this dataset.

| Table | v1 legacy | v2 HEAD/legacy | v2 GET/simple_http | v2 GET/full status_change | v2 GET/full history disabled | v2 GET/full history sample10 | v2 GET/full history all |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| jetpack_monitor_audit_log | 0 | 944 | 880 | 937 | 956 | 944 | 939 |
| jetpack_monitor_check_history | 0 | 145 | 144 | 145 | 0 | 6,781 | 62,701 |
| jetpack_monitor_event_transitions | 0 | 50 | 50 | 50 | 50 | 50 | 50 |
| jetpack_monitor_events | 0 | 25 | 25 | 25 | 25 | 25 | 25 |
| jetpack_monitor_false_positives | 0 | 3 | 0 | 3 | 3 | 3 | 3 |

Audit rows are high because v2 records operational evidence, not just customer
incidents. The rows include retry dispatches, Veriflier requests/replies,
suppression decisions, and related control-plane observations. A future
uptime-bench report should break this down by audit event type.

The zero false-positive rows in the GET/simple_http run should not be read as a
semantic difference. The run used the same event shape, but Veriflier outcomes
can vary slightly by timing. Treat this as run variance unless a repeated
comparison shows the same difference.

The raw artifacts also include MySQL `performance_schema` per-table I/O deltas.
Those are not used for conclusions here because the capture window in this run
included activation/deactivation lifecycle SQL for some modes. That is why the
raw per-table I/O data showed 10,000 inserts/deletes on setup tables. A future
report should start table-I/O capture after activation and stop it before
deactivation if we want operational-only per-table I/O.

## Network Traffic

This table separates product traffic from harness/control traffic. Empty
columns from the raw report, such as unused WPCOM/API buckets, are omitted.
`ssh` and monitoring scrape traffic are excluded from the product subtotal and
shown only as harness overhead.

| Mode | target HTTP | MySQL | StatsD | DNS | Jetmon peer | Other | Product subtotal | Harness overhead |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| v1 legacy | 47.01 MiB (+0.0%) | 4.44 MiB (+0.0%) | 37.25 MiB (+0.0%) | 48.04 MiB (+0.0%) | 38.92 KiB (+0.0%) | 43.03 MiB (+0.0%) | 179.81 MiB (+0.0%) | 23.59 MiB (+0.0%) |
| v2 HEAD/legacy | 24.11 MiB (-48.7%) | 42.14 MiB (+849.6%) | 628.89 KiB (-98.4%) | 3.81 MiB (-92.1%) | 0 B (-100.0%) | 3.04 MiB (-92.9%) | 73.71 MiB (-59.0%) | 4.80 MiB (-79.7%) |
| v2 GET/simple_http | 91.33 MiB (+94.3%) | 41.27 MiB (+830.2%) | 594.10 KiB (-98.4%) | 3.77 MiB (-92.1%) | 0 B (-100.0%) | 1.19 MiB (-97.2%) | 138.15 MiB (-23.2%) | 4.70 MiB (-80.1%) |
| v2 GET/full status_change | 67.83 MiB (+44.3%) | 41.96 MiB (+845.6%) | 622.08 KiB (-98.4%) | 3.81 MiB (-92.1%) | 0 B (-100.0%) | 1.20 MiB (-97.2%) | 115.41 MiB (-35.8%) | 4.77 MiB (-79.8%) |
| v2 GET/full history disabled | 67.84 MiB (+44.3%) | 41.82 MiB (+842.4%) | 635.15 KiB (-98.3%) | 3.80 MiB (-92.1%) | 0 B (-100.0%) | 1.22 MiB (-97.2%) | 115.30 MiB (-35.9%) | 4.80 MiB (-79.7%) |
| v2 GET/full history sample10 | 67.79 MiB (+44.2%) | 45.86 MiB (+933.4%) | 629.53 KiB (-98.3%) | 3.82 MiB (-92.1%) | 0 B (-100.0%) | 1.23 MiB (-97.1%) | 119.30 MiB (-33.7%) | 4.77 MiB (-79.8%) |
| v2 GET/full history all | 67.89 MiB (+44.4%) | 70.73 MiB (+1,494.0%) | 620.97 KiB (-98.4%) | 3.80 MiB (-92.1%) | 0 B (-100.0%) | 1.20 MiB (-97.2%) | 144.24 MiB (-19.8%) | 4.75 MiB (-79.9%) |

GET/simple_http showed more target HTTP traffic than GET/full in this run. That
is not expected from the profile names alone. The likely explanation is fixture
body/read behavior or run-to-run target response variance. Treat it as a
measurement observation, not a design rule, until a targeted same-body test
confirms the difference.

The large v1 StatsD and DNS traffic drops are product-relevant. The SSH and
monitoring drops are harness behavior and should not be used as v1/v2 product
claims.

## StatsD

| Mode | StatsD datagrams | StatsD metric lines | StatsD payload |
| --- | ---: | ---: | ---: |
| v1 legacy | 1,086 (+0.0%) | 267,893 (+0.0%) | 56.00 MiB (+0.0%) |
| v2 HEAD/legacy | 3,411 (+214.1%) | 18,092 (-93.2%) | 1.43 MiB (-97.4%) |
| v2 GET/simple_http | 3,139 (+189.0%) | 17,409 (-93.5%) | 1.37 MiB (-97.5%) |
| v2 GET/full status_change | 3,294 (+203.3%) | 17,836 (-93.3%) | 1.41 MiB (-97.5%) |
| v2 GET/full history disabled | 3,373 (+210.6%) | 18,090 (-93.2%) | 1.43 MiB (-97.4%) |
| v2 GET/full history sample10 | 3,403 (+213.4%) | 17,959 (-93.3%) | 1.42 MiB (-97.5%) |
| v2 GET/full history all | 3,298 (+203.7%) | 17,829 (-93.3%) | 1.41 MiB (-97.5%) |

V2 emits more datagrams but far fewer metric lines and much smaller payloads.
That is the useful StatsD capacity signal.

Disk attribution from this run is intentionally omitted from the comparison
tables. The per-host disk samples were noisy and mixed product work with lab
container behavior. Use DB row deltas, DB bytes, and future operational-only
table-I/O capture for storage planning.

## Veriflier Capacity Comparison

Source: controlled internal target on the normal Veriflier port. V1 was tested
with legacy HEAD checks. V2 was tested through `/v2/check` with 50 checks per
RPC. The table below uses the highest clean tier for each mode from the sizing
campaign.

| Mode | Highest clean tier | Throughput vs v1 clean ceiling | CPU avg | RSS | FD avg | p95 latency |
|---|---:|---:|---:|---:|---:|---:|
| v1 legacy | 90k/min | baseline | 45.1% | 39.0 MiB | 774 | 5,101 ms |
| v2 HEAD/legacy | 2.0M/min | 22.2x (+2,122%) | 276.8% (+514%) | 271.1 MiB (+595%) | 2,204 (+185%) | 1,701 ms (-66.7%) |
| v2 GET/simple_http | 1.0M/min | 11.1x (+1,011%) | 244.3% (+442%) | 289.4 MiB (+642%) | 1,763 (+128%) | 2,454 ms (-51.9%) |
| v2 GET/full | 1.0M/min | 11.1x (+1,011%) | 223.8% (+396%) | 291.4 MiB (+647%) | 2,170 (+180%) | 1,490 ms (-70.8%) |
| v2 mixed 50/25/25 | 800k/min | 8.9x (+789%) | 226.1% (+401%) | 190.3 MiB (+388%) | 1,700 (+120%) | 1,076 ms (-78.9%) |

Normalized per 1,000 checks/minute, v2 used less CPU, memory, and file
descriptors than v1 at the selected clean tiers:

| Mode | CPU per 1k checks/min | vs v1 | RSS per 1k checks/min | vs v1 | FD per 1k checks/min | vs v1 |
|---|---:|---:|---:|---:|---:|---:|
| v1 legacy | 0.501% | baseline | 0.433 MiB | baseline | 8.60 | baseline |
| v2 HEAD/legacy | 0.138% | -72.4% | 0.136 MiB | -68.7% | 1.10 | -87.2% |
| v2 GET/simple_http | 0.244% | -51.2% | 0.289 MiB | -33.2% | 1.76 | -79.5% |
| v2 GET/full | 0.224% | -55.3% | 0.291 MiB | -32.7% | 2.17 | -74.8% |
| v2 mixed 50/25/25 | 0.283% | -43.6% | 0.238 MiB | -45.1% | 2.13 | -75.3% |

For rollout sizing, this supports a 4 vCPU floor, at least 1 GiB RAM, and a
high file-descriptor limit for Verifliers.

## Veriflier Real-URL Bandwidth Comparison

Source: same selected 50,000 real URLs from the backup dataset. This is a
direct Veriflier check comparison, not a Monitor cadence run. The v1 run and v2
run used the same deterministic selection checksum.

| Mode | Completed | Transport errors | Checks/sec | RX per check | TX per check | CPU avg | RSS avg | FD avg |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| v1 legacy | 49,990 | 10 | 22.36 | 5,592 B | 2,064 B | 18.0% | 305.1 MiB | 800 |
| v2 HEAD/legacy | 50,000 | 0 | 35.13 (+57%) | 5,735 B (+3%) | 2,288 B (+11%) | 24.5% (+36%) | 240.0 MiB (-21%) | 125 (-84%) |
| v2 GET/simple_http | 50,000 | 0 | 32.28 (+44%) | 27,009 B (+383%) | 2,740 B (+33%) | 22.9% (+27%) | 241.1 MiB (-21%) | 110 (-86%) |
| v2 GET/full | 50,000 | 0 | 32.70 (+46%) | 38,444 B (+588%) | 2,892 B (+40%) | 28.1% (+56%) | 246.8 MiB (-19%) | 119 (-85%) |

The real-URL data lines up with the expected rollout tradeoff. V2 HEAD/legacy
is closest to v1, while GET profiles intentionally use more receive bandwidth
because they fetch response bodies for richer detection.

## Overall Findings

1. V2 Monitor process cost is much lower than v1 across every rollout stage.
   At 10k sites for 30 minutes, v2 used about 27-43% less CPU, about 96% less
   RSS, about 95-98% fewer file descriptors, and about 97% fewer threads.

2. V2 shifts cost toward MySQL. That is the main operational tradeoff for the
   event/runtime model. HEAD/legacy is still much cheaper than v1 at the
   process and StatsD layers, but it is not v1-equivalent at the database layer.

3. HEAD/legacy remains the safest first rollout mode. It preserves v1-style
   HEAD probes, cuts target HTTP traffic in this run, and avoids the body-read
   bandwidth of GET profiles.

4. GET/simple_http and GET/full intentionally use more target HTTP bandwidth.
   Roll them out in stages after HEAD/legacy is stable.

5. `CHECK_HISTORY_MODE_DEFAULT=status_change` is the practical default. It
   preserves status-change evidence with a small check-history footprint.
   `disabled` avoids those writes, `sample` adds controlled sampling cost, and
   `all` is expensive but measured.

6. Full check-history retention is the largest optional DB cost in this
   comparison. `history=all` wrote 62,701 check-history rows and increased
   MySQL network traffic materially versus the status_change/full baseline.

7. Existing Veriflier evidence still supports v2 as a substantial capacity
   upgrade over v1. The detailed Veriflier sections are retained from the
   latest dedicated Veriflier comparison campaign; the Monitor runs here focus
   on steady operational cost.

## Caveats

- This report measures a 10k-site steady workload, not Monitor maximum capacity.
- WPCOM and live alert delivery stayed disabled by design.
- V2 Monitor readiness can be red in this test fleet when WPCOM notifications
  are intentionally disabled under a production profile; API health, checks,
  DB, StatsD, and Veriflier paths were still measured.
- Network `other` is retained because it is product-adjacent traffic that the
  harness did not classify more precisely. It should not be over-interpreted.
- Raw per-table `performance_schema` I/O counters from this run include
  activation/deactivation lifecycle work and are not used for operational
  table-I/O conclusions.
- The Veriflier capacity and real-URL bandwidth sections come from dedicated
  Veriflier comparison runs, not from the Monitor-focused run generated today.
