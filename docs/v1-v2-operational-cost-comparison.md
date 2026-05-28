# Jetmon v1/v2 Operational Cost Comparison

This report is organized around the staged rollout path: v1, v2 HEAD/legacy, v2 GET/simple_http, v2 GET/full, and GET/full with alternate check-history retention modes. Percent changes are relative to v1 legacy.

Report root: `/home/gaarai/code/uptime-bench/reports/20260528T125345Z-monitor-systems-rollout-comparison-combined`

Generated: 2026-05-28

Source reports:

- Combined Monitor comparison: `/home/gaarai/code/uptime-bench/reports/20260528T125345Z-monitor-systems-rollout-comparison-combined`
- Base rollout comparison: `/home/gaarai/code/uptime-bench/reports/20260528T065939Z-monitor-systems-rollout-comparison-03359e6`
- Corrected check-history comparison: `/home/gaarai/code/uptime-bench/reports/20260528T105221Z-monitor-history-modes-after-history-writer-fix-03359e6-dirty`
- Veriflier capacity/resource sizing: `/home/gaarai/code/uptime-bench/reports/20260515T222700Z-jetmon-veriflier-resource-sizing-a9bc868`
- Veriflier real-URL bandwidth comparison: `/home/gaarai/code/uptime-bench/reports/20260518T152325Z-jetmon-veriflier-v1-bandwidth-same50k`


## Run Status

| Mode | Run | Health | Target | DB | Network | Disk | Cleanup | Coverage | Requests |
| --- | --- | --- | --- | --- | --- | --- | --- | ---: | ---: |
| v1 legacy | run-v1-legacy-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 60181 |
| v2 HEAD/legacy | run-v2-head-legacy-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 62725 |
| v2 GET/simple_http | run-v2-get-simple-http-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 63744 |
| v2 GET/full status_change | run-v2-get-full-status-change-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 62687 |
| v2 GET/full history disabled | run-v2-get-full-history-disabled-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 62675 |
| v2 GET/full history sample10 | run-v2-get-full-history-sample10-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 62725 |
| v2 GET/full history all | run-v2-get-full-history-all-10000-30m | pass | pass | pass | pass | complete | pass | 100.0% | 62714 |

## Monitor Process Resources

| Mode | CPU avg | CPU p95 | RSS avg | FD avg | Threads avg |
| --- | ---: | ---: | ---: | ---: | ---: |
| v1 legacy | 4.37% core (+0.0%) | 4.89% core (+0.0%) | 4.55 GiB (+0.0%) | 1872 (+0.0%) | 809 (+0.0%) |
| v2 HEAD/legacy | 2.51% core (-42.6%) | 3.12% core (-36.1%) | 115.34 MiB (-97.5%) | 78 (-95.8%) | 20 (-97.5%) |
| v2 GET/simple_http | 3.50% core (-19.8%) | 3.85% core (-21.2%) | 171.24 MiB (-96.3%) | 38 (-98.0%) | 21 (-97.4%) |
| v2 GET/full status_change | 3.02% core (-31.0%) | 3.50% core (-28.3%) | 179.31 MiB (-96.2%) | 84 (-95.5%) | 21 (-97.4%) |
| v2 GET/full history disabled | 3.04% core (-30.5%) | 3.47% core (-29.0%) | 184.81 MiB (-96.0%) | 86 (-95.4%) | 21 (-97.4%) |
| v2 GET/full history sample10 | 3.08% core (-29.5%) | 3.51% core (-28.1%) | 184.37 MiB (-96.0%) | 81 (-95.7%) | 21 (-97.4%) |
| v2 GET/full history all | 3.18% core (-27.2%) | 3.58% core (-26.7%) | 181.94 MiB (-96.1%) | 86 (-95.4%) | 21 (-97.3%) |

## Monitor Database Work

| Mode | Queries | Writes | Rows read | Rows added | Rows updated | DB RX | DB TX |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| v1 legacy | 136 (+0.0%) | 50 (+0.0%) | 79,398,087 (+0.0%) | 0 | 50 (+0.0%) | 17.19 KiB (+0.0%) | 4.36 MiB (+0.0%) |
| v2 HEAD/legacy | 47,521 (+34841.9%) | 1,699 (+3298.0%) | 398,453,027 (+401.8%) | 1,167 | 20,403 (+40706.0%) | 6.39 MiB (+37958.8%) | 30.03 MiB (+588.0%) |
| v2 GET/simple_http | 47,180 (+34591.2%) | 1,590 (+3080.0%) | 402,667,432 (+407.2%) | 1,099 | 16,134 (+32168.0%) | 5.99 MiB (+35552.3%) | 29.53 MiB (+576.6%) |
| v2 GET/full status_change | 47,428 (+34773.5%) | 1,669 (+3238.0%) | 402,678,229 (+407.2%) | 1,160 | 20,399 (+40698.0%) | 6.41 MiB (+38064.8%) | 29.85 MiB (+583.9%) |
| v2 GET/full history disabled | 47,063 (+34505.1%) | 1,550 (+3000.0%) | 398,461,844 (+401.9%) | 1,034 | 20,393 (+40686.0%) | 6.36 MiB (+37789.0%) | 29.80 MiB (+582.7%) |
| v2 GET/full history sample10 | 52,257 (+38324.3%) | 3,280 (+6460.0%) | 402,680,417 (+407.2%) | 7,803 | 20,395 (+40690.0%) | 7.74 MiB (+46013.5%) | 31.83 MiB (+629.3%) |
| v2 GET/full history all | 52,265 (+38330.1%) | 3,284 (+6468.0%) | 402,688,273 (+407.2%) | 63,718 | 20,393 (+40686.0%) | 16.46 MiB (+97906.8%) | 47.67 MiB (+992.2%) |

### Per-table row deltas

| Table | v1 legacy | v2 HEAD/legacy | v2 GET/simple_http | v2 GET/full status_change | v2 GET/full history disabled | v2 GET/full history sample10 | v2 GET/full history all |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| jetpack_monitor_audit_log | 0 | 944 | 880 | 937 | 956 | 944 | 939 |
| jetpack_monitor_check_history | 0 | 145 | 144 | 145 | 0 | 6,781 | 62,701 |
| jetpack_monitor_event_transitions | 0 | 50 | 50 | 50 | 50 | 50 | 50 |
| jetpack_monitor_events | 0 | 25 | 25 | 25 | 25 | 25 | 25 |
| jetpack_monitor_false_positives | 0 | 3 | 0 | 3 | 3 | 3 | 3 |

### Per-table performance_schema I/O deltas

These counters come from MySQL `performance_schema.table_io_waits_summary_by_table` on the DB containers and show read/write operation deltas during each run window.

#### v1 legacy

| Table | Reads | Writes | Fetch | Insert | Update | Delete |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| jetpack_monitor_sites | 82,069,473 | 30,050 | 82,069,473 | 0 | 30,050 | 0 |

#### v2 HEAD/legacy

| Table | Reads | Writes | Fetch | Insert | Update | Delete |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| jetpack_monitor_alert_contacts | 42 | 0 | 42 | 0 | 0 | 0 |
| jetpack_monitor_alert_deliveries | 1,818 | 0 | 1,818 | 0 | 0 | 0 |
| jetpack_monitor_alert_dispatch_progress | 1,860 | 84 | 1,860 | 42 | 42 | 0 |
| jetpack_monitor_audit_log | 372,018 | 945 | 372,018 | 945 | 0 | 0 |
| jetpack_monitor_check_history | 54,792 | 146 | 54,792 | 146 | 0 | 0 |
| jetpack_monitor_check_targets | 10,040 | 20,000 | 10,040 | 10,000 | 0 | 10,000 |
| jetpack_monitor_event_transitions | 37,488 | 50 | 37,488 | 50 | 0 | 0 |
| jetpack_monitor_events | 308,341 | 50 | 308,341 | 25 | 25 | 0 |
| jetpack_monitor_false_positives | 9 | 3 | 9 | 3 | 0 | 0 |
| jetpack_monitor_hosts | 150 | 60 | 150 | 30 | 30 | 0 |
| jetpack_monitor_process_health | 183 | 362 | 183 | 181 | 181 | 0 |
| jetpack_monitor_site_check_config | 90,249 | 20,000 | 90,249 | 10,000 | 0 | 10,000 |
| jetpack_monitor_site_runtime | 124,110 | 60,012 | 124,110 | 30,006 | 20,006 | 10,000 |
| jetpack_monitor_sites | 412,625,946 | 30,050 | 412,625,946 | 0 | 30,050 | 0 |
| jetpack_monitor_veriflier_agents | 936 | 62 | 936 | 31 | 31 | 0 |
| jetpack_monitor_veriflier_vantages | 181 | 0 | 181 | 0 | 0 | 0 |
| jetpack_monitor_webhook_deliveries | 1,818 | 0 | 1,818 | 0 | 0 | 0 |
| jetpack_monitor_webhook_dispatch_progress | 1,860 | 84 | 1,860 | 42 | 42 | 0 |
| jetpack_monitor_webhooks | 42 | 0 | 42 | 0 | 0 | 0 |

#### v2 GET/simple_http

| Table | Reads | Writes | Fetch | Insert | Update | Delete |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| jetpack_monitor_alert_contacts | 29 | 0 | 29 | 0 | 0 | 0 |
| jetpack_monitor_alert_deliveries | 1,819 | 0 | 1,819 | 0 | 0 | 0 |
| jetpack_monitor_alert_dispatch_progress | 1,848 | 58 | 1,848 | 29 | 29 | 0 |
| jetpack_monitor_audit_log | 373,844 | 881 | 373,844 | 881 | 0 | 0 |
| jetpack_monitor_check_history | 55,083 | 145 | 55,083 | 145 | 0 | 0 |
| jetpack_monitor_check_targets | 10,040 | 20,000 | 10,040 | 10,000 | 0 | 10,000 |
| jetpack_monitor_event_transitions | 37,616 | 50 | 37,616 | 50 | 0 | 0 |
| jetpack_monitor_events | 308,516 | 50 | 308,516 | 25 | 25 | 0 |
| jetpack_monitor_false_positives | 12 | 0 | 12 | 0 | 0 | 0 |
| jetpack_monitor_hosts | 150 | 60 | 150 | 30 | 30 | 0 |
| jetpack_monitor_process_health | 183 | 362 | 183 | 181 | 181 | 0 |
| jetpack_monitor_site_check_config | 90,246 | 20,000 | 90,246 | 10,000 | 0 | 10,000 |
| jetpack_monitor_site_runtime | 120,212 | 52,568 | 120,212 | 26,457 | 16,111 | 10,000 |
| jetpack_monitor_sites | 411,618,806 | 30,050 | 411,618,806 | 0 | 30,050 | 0 |
| jetpack_monitor_veriflier_agents | 935 | 60 | 935 | 30 | 30 | 0 |
| jetpack_monitor_veriflier_vantages | 181 | 0 | 181 | 0 | 0 | 0 |
| jetpack_monitor_webhook_deliveries | 1,819 | 0 | 1,819 | 0 | 0 | 0 |
| jetpack_monitor_webhook_dispatch_progress | 1,848 | 58 | 1,848 | 29 | 29 | 0 |
| jetpack_monitor_webhooks | 29 | 0 | 29 | 0 | 0 | 0 |

#### v2 GET/full status_change

| Table | Reads | Writes | Fetch | Insert | Update | Delete |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| jetpack_monitor_alert_contacts | 40 | 0 | 40 | 0 | 0 | 0 |
| jetpack_monitor_alert_deliveries | 1,818 | 0 | 1,818 | 0 | 0 | 0 |
| jetpack_monitor_alert_dispatch_progress | 1,858 | 80 | 1,858 | 40 | 40 | 0 |
| jetpack_monitor_audit_log | 383,217 | 937 | 383,217 | 937 | 0 | 0 |
| jetpack_monitor_check_history | 57,996 | 145 | 57,996 | 145 | 0 | 0 |
| jetpack_monitor_check_targets | 10,040 | 20,000 | 10,040 | 10,000 | 0 | 10,000 |
| jetpack_monitor_event_transitions | 38,092 | 50 | 38,092 | 50 | 0 | 0 |
| jetpack_monitor_events | 309,388 | 50 | 309,388 | 25 | 25 | 0 |
| jetpack_monitor_false_positives | 33 | 3 | 33 | 3 | 0 | 0 |
| jetpack_monitor_hosts | 145 | 58 | 145 | 29 | 29 | 0 |
| jetpack_monitor_process_health | 183 | 362 | 183 | 181 | 181 | 0 |
| jetpack_monitor_site_check_config | 90,249 | 20,000 | 90,249 | 10,000 | 0 | 10,000 |
| jetpack_monitor_site_runtime | 124,110 | 60,012 | 124,110 | 30,006 | 20,006 | 10,000 |
| jetpack_monitor_sites | 408,342,515 | 30,050 | 408,342,515 | 0 | 30,050 | 0 |
| jetpack_monitor_veriflier_agents | 935 | 60 | 935 | 30 | 30 | 0 |
| jetpack_monitor_veriflier_vantages | 181 | 0 | 181 | 0 | 0 | 0 |
| jetpack_monitor_webhook_deliveries | 1,818 | 0 | 1,818 | 0 | 0 | 0 |
| jetpack_monitor_webhook_dispatch_progress | 1,858 | 80 | 1,858 | 40 | 40 | 0 |
| jetpack_monitor_webhooks | 40 | 0 | 40 | 0 | 0 | 0 |

#### v2 GET/full history disabled

| Table | Reads | Writes | Fetch | Insert | Update | Delete |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| jetpack_monitor_alert_contacts | 37 | 0 | 37 | 0 | 0 | 0 |
| jetpack_monitor_alert_deliveries | 1,818 | 0 | 1,818 | 0 | 0 | 0 |
| jetpack_monitor_alert_dispatch_progress | 1,855 | 74 | 1,855 | 37 | 37 | 0 |
| jetpack_monitor_audit_log | 385,110 | 957 | 385,110 | 957 | 0 | 0 |
| jetpack_monitor_check_history | 58,110 | 0 | 58,110 | 0 | 0 | 0 |
| jetpack_monitor_check_targets | 10,040 | 20,000 | 10,040 | 10,000 | 0 | 10,000 |
| jetpack_monitor_event_transitions | 38,198 | 50 | 38,198 | 50 | 0 | 0 |
| jetpack_monitor_events | 309,563 | 50 | 309,563 | 25 | 25 | 0 |
| jetpack_monitor_false_positives | 39 | 3 | 39 | 3 | 0 | 0 |
| jetpack_monitor_hosts | 145 | 58 | 145 | 29 | 29 | 0 |
| jetpack_monitor_process_health | 183 | 362 | 183 | 181 | 181 | 0 |
| jetpack_monitor_site_check_config | 90,249 | 20,000 | 90,249 | 10,000 | 0 | 10,000 |
| jetpack_monitor_site_runtime | 124,110 | 60,012 | 124,110 | 30,006 | 20,006 | 10,000 |
| jetpack_monitor_sites | 408,341,926 | 30,050 | 408,341,926 | 0 | 30,050 | 0 |
| jetpack_monitor_veriflier_agents | 935 | 60 | 935 | 30 | 30 | 0 |
| jetpack_monitor_veriflier_vantages | 181 | 0 | 181 | 0 | 0 | 0 |
| jetpack_monitor_webhook_deliveries | 1,818 | 0 | 1,818 | 0 | 0 | 0 |
| jetpack_monitor_webhook_dispatch_progress | 1,855 | 74 | 1,855 | 37 | 37 | 0 |
| jetpack_monitor_webhooks | 37 | 0 | 37 | 0 | 0 | 0 |

#### v2 GET/full history sample10

| Table | Reads | Writes | Fetch | Insert | Update | Delete |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| jetpack_monitor_alert_contacts | 38 | 0 | 38 | 0 | 0 | 0 |
| jetpack_monitor_alert_deliveries | 1,818 | 0 | 1,818 | 0 | 0 | 0 |
| jetpack_monitor_alert_dispatch_progress | 1,856 | 76 | 1,856 | 38 | 38 | 0 |
| jetpack_monitor_audit_log | 387,014 | 946 | 387,014 | 946 | 0 | 0 |
| jetpack_monitor_check_history | 67,034 | 6,821 | 67,034 | 6,821 | 0 | 0 |
| jetpack_monitor_check_targets | 10,040 | 20,000 | 10,040 | 10,000 | 0 | 10,000 |
| jetpack_monitor_event_transitions | 38,296 | 50 | 38,296 | 50 | 0 | 0 |
| jetpack_monitor_events | 309,738 | 50 | 309,738 | 25 | 25 | 0 |
| jetpack_monitor_false_positives | 45 | 3 | 45 | 3 | 0 | 0 |
| jetpack_monitor_hosts | 145 | 58 | 145 | 29 | 29 | 0 |
| jetpack_monitor_process_health | 183 | 362 | 183 | 181 | 181 | 0 |
| jetpack_monitor_site_check_config | 90,249 | 20,000 | 90,249 | 10,000 | 0 | 10,000 |
| jetpack_monitor_site_runtime | 124,110 | 60,012 | 124,110 | 30,006 | 20,006 | 10,000 |
| jetpack_monitor_sites | 408,375,901 | 30,050 | 408,375,901 | 0 | 30,050 | 0 |
| jetpack_monitor_veriflier_agents | 935 | 60 | 935 | 30 | 30 | 0 |
| jetpack_monitor_veriflier_vantages | 181 | 0 | 181 | 0 | 0 | 0 |
| jetpack_monitor_webhook_deliveries | 1,818 | 0 | 1,818 | 0 | 0 | 0 |
| jetpack_monitor_webhook_dispatch_progress | 1,856 | 76 | 1,856 | 38 | 38 | 0 |
| jetpack_monitor_webhooks | 38 | 0 | 38 | 0 | 0 | 0 |

#### v2 GET/full history all

| Table | Reads | Writes | Fetch | Insert | Update | Delete |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| jetpack_monitor_alert_contacts | 37 | 0 | 37 | 0 | 0 | 0 |
| jetpack_monitor_alert_deliveries | 1,818 | 0 | 1,818 | 0 | 0 | 0 |
| jetpack_monitor_alert_dispatch_progress | 1,855 | 74 | 1,855 | 37 | 37 | 0 |
| jetpack_monitor_audit_log | 388,901 | 941 | 388,901 | 941 | 0 | 0 |
| jetpack_monitor_check_history | 276,935 | 63,060 | 276,935 | 63,060 | 0 | 0 |
| jetpack_monitor_check_targets | 10,040 | 20,000 | 10,040 | 10,000 | 0 | 10,000 |
| jetpack_monitor_event_transitions | 38,398 | 50 | 38,398 | 50 | 0 | 0 |
| jetpack_monitor_events | 309,913 | 50 | 309,913 | 25 | 25 | 0 |
| jetpack_monitor_false_positives | 51 | 3 | 51 | 3 | 0 | 0 |
| jetpack_monitor_hosts | 150 | 60 | 150 | 30 | 30 | 0 |
| jetpack_monitor_process_health | 183 | 362 | 183 | 181 | 181 | 0 |
| jetpack_monitor_site_check_config | 90,249 | 20,000 | 90,249 | 10,000 | 0 | 10,000 |
| jetpack_monitor_site_runtime | 124,110 | 60,012 | 124,110 | 30,006 | 20,006 | 10,000 |
| jetpack_monitor_sites | 409,265,479 | 30,050 | 409,265,479 | 0 | 30,050 | 0 |
| jetpack_monitor_veriflier_agents | 936 | 62 | 936 | 31 | 31 | 0 |
| jetpack_monitor_veriflier_vantages | 181 | 0 | 181 | 0 | 0 | 0 |
| jetpack_monitor_webhook_deliveries | 1,818 | 0 | 1,818 | 0 | 0 | 0 |
| jetpack_monitor_webhook_dispatch_progress | 1,855 | 74 | 1,855 | 37 | 37 | 0 |
| jetpack_monitor_webhooks | 37 | 0 | 37 | 0 | 0 | 0 |

## Network By Traffic Type

| Mode | target http | mysql | statsd | dns | wpcom https | jetmon peer | api | monitoring | ssh | total |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| v1 legacy | 47.01 MiB (+0.0%) | 4.44 MiB (+0.0%) | 37.25 MiB (+0.0%) | 48.04 MiB (+0.0%) | 0 B | 38.92 KiB (+0.0%) | 0 B | 2.41 MiB (+0.0%) | 21.18 MiB (+0.0%) | 203.41 MiB (+0.0%) |
| v2 HEAD/legacy | 24.11 MiB (-48.7%) | 42.14 MiB (+849.6%) | 628.89 KiB (-98.4%) | 3.81 MiB (-92.1%) | 0 B | 0 B (-100.0%) | 0 B | 2.28 MiB (-5.5%) | 2.52 MiB (-88.1%) | 78.51 MiB (-61.4%) |
| v2 GET/simple_http | 91.33 MiB (+94.3%) | 41.27 MiB (+830.2%) | 594.10 KiB (-98.4%) | 3.77 MiB (-92.1%) | 0 B | 0 B (-100.0%) | 0 B | 2.30 MiB (-4.7%) | 2.40 MiB (-88.7%) | 142.86 MiB (-29.8%) |
| v2 GET/full status_change | 67.83 MiB (+44.3%) | 41.96 MiB (+845.6%) | 622.08 KiB (-98.4%) | 3.81 MiB (-92.1%) | 0 B | 0 B (-100.0%) | 0 B | 2.28 MiB (-5.7%) | 2.49 MiB (-88.2%) | 120.18 MiB (-40.9%) |
| v2 GET/full history disabled | 67.84 MiB (+44.3%) | 41.82 MiB (+842.4%) | 635.15 KiB (-98.3%) | 3.80 MiB (-92.1%) | 0 B | 0 B (-100.0%) | 0 B | 2.30 MiB (-4.8%) | 2.50 MiB (-88.2%) | 120.10 MiB (-41.0%) |
| v2 GET/full history sample10 | 67.79 MiB (+44.2%) | 45.86 MiB (+933.4%) | 629.53 KiB (-98.3%) | 3.82 MiB (-92.1%) | 0 B | 0 B (-100.0%) | 0 B | 2.28 MiB (-5.7%) | 2.50 MiB (-88.2%) | 124.08 MiB (-39.0%) |
| v2 GET/full history all | 67.89 MiB (+44.4%) | 70.73 MiB (+1494.0%) | 620.97 KiB (-98.4%) | 3.80 MiB (-92.1%) | 0 B | 0 B (-100.0%) | 0 B | 2.28 MiB (-5.7%) | 2.47 MiB (-88.3%) | 148.99 MiB (-26.8%) |

### Network RX/TX Detail

Each cell is `rx / tx` bytes for the run window.

| Mode | target http | mysql | statsd | dns | wpcom https | jetmon peer | api | monitoring | ssh | total |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| v1 legacy | 23.20 MiB / 23.82 MiB | 4.39 MiB / 46.20 KiB | 0 B / 37.25 MiB | 29.33 MiB / 18.71 MiB | 0 B / 0 B | 15.54 KiB / 23.38 KiB | 0 B / 0 B | 150.08 KiB / 2.27 MiB | 512.83 KiB / 20.68 MiB | 86.73 MiB / 116.68 MiB |
| v2 HEAD/legacy | 10.22 MiB / 13.88 MiB | 32.70 MiB / 9.44 MiB | 0 B / 628.89 KiB | 1.96 MiB / 1.86 MiB | 0 B / 0 B | 0 B / 0 B | 0 B / 0 B | 144.07 KiB / 2.14 MiB | 182.88 KiB / 2.34 MiB | 47.91 MiB / 30.59 MiB |
| v2 GET/simple_http | 62.88 MiB / 28.45 MiB | 32.22 MiB / 9.05 MiB | 0 B / 594.10 KiB | 1.93 MiB / 1.84 MiB | 0 B / 0 B | 0 B / 0 B | 0 B / 0 B | 145.77 KiB / 2.16 MiB | 174.80 KiB / 2.23 MiB | 98.24 MiB / 44.62 MiB |
| v2 GET/full status_change | 52.56 MiB / 15.27 MiB | 32.51 MiB / 9.45 MiB | 0 B / 622.08 KiB | 1.95 MiB / 1.86 MiB | 0 B / 0 B | 0 B / 0 B | 0 B / 0 B | 144.38 KiB / 2.14 MiB | 183.11 KiB / 2.31 MiB | 88.23 MiB / 31.94 MiB |
| v2 GET/full history disabled | 52.56 MiB / 15.28 MiB | 32.43 MiB / 9.39 MiB | 0 B / 635.15 KiB | 1.95 MiB / 1.85 MiB | 0 B / 0 B | 0 B / 0 B | 0 B / 0 B | 145.67 KiB / 2.16 MiB | 175.95 KiB / 2.33 MiB | 88.15 MiB / 31.94 MiB |
| v2 GET/full history sample10 | 52.55 MiB / 15.24 MiB | 34.82 MiB / 11.04 MiB | 0 B / 629.53 KiB | 1.96 MiB / 1.86 MiB | 0 B / 0 B | 0 B / 0 B | 0 B / 0 B | 144.89 KiB / 2.14 MiB | 177.12 KiB / 2.32 MiB | 90.54 MiB / 33.53 MiB |
| v2 GET/full history all | 52.59 MiB / 15.30 MiB | 50.88 MiB / 19.85 MiB | 0 B / 620.97 KiB | 1.95 MiB / 1.85 MiB | 0 B / 0 B | 0 B / 0 B | 0 B / 0 B | 143.97 KiB / 2.14 MiB | 179.71 KiB / 2.30 MiB | 106.63 MiB / 42.35 MiB |

## StatsD And Disk

| Mode | StatsD datagrams | StatsD metric lines | StatsD payload | Host read | Host write | Process read | Process write | Container read | Container write |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| v1 legacy | 1,086 (+0.0%) | 267,893 (+0.0%) | 56.00 MiB (+0.0%) | 79 B/s (+0.0%) | 275.06 KiB/s (+0.0%) | 77 B/s (+0.0%) | 218.69 KiB/s (+0.0%) | 79 B/s (+0.0%) | 198.94 KiB/s (+0.0%) |
| v2 HEAD/legacy | 3,411 (+214.1%) | 18,092 (-93.2%) | 1.43 MiB (-97.4%) | 4.11 KiB/s (+5233.9%) | 520.50 KiB/s (+89.2%) | 4.19 KiB/s (+5460.5%) | 418.99 KiB/s (+91.6%) | 4.11 KiB/s (+5224.1%) | 409.25 KiB/s (+105.7%) |
| v2 GET/simple_http | 3,139 (+189.0%) | 17,409 (-93.5%) | 1.37 MiB (-97.5%) | 692.69 KiB/s (+897919.6%) | 1.09 MiB/s (+307.6%) | 706.87 KiB/s (+937231.5%) | 400.59 KiB/s (+83.2%) | 691.90 KiB/s (+896900.0%) | 1018.03 KiB/s (+411.7%) |
| v2 GET/full status_change | 3,294 (+203.3%) | 17,836 (-93.3%) | 1.41 MiB (-97.5%) | 0 B/s (-100.0%) | 486.06 KiB/s (+76.7%) | 0 B/s (-100.0%) | 402.58 KiB/s (+84.1%) | 0 B/s (-100.0%) | 384.43 KiB/s (+93.2%) |
| v2 GET/full history disabled | 3,373 (+210.6%) | 18,090 (-93.2%) | 1.43 MiB (-97.4%) | 11 B/s (-85.7%) | 492.41 KiB/s (+79.0%) | 11 B/s (-85.3%) | 403.70 KiB/s (+84.6%) | 11 B/s (-85.7%) | 390.69 KiB/s (+96.4%) |
| v2 GET/full history sample10 | 3,403 (+213.4%) | 17,959 (-93.3%) | 1.42 MiB (-97.5%) | 70 B/s (-11.4%) | 488.72 KiB/s (+77.7%) | 70 B/s (-8.8%) | 406.79 KiB/s (+86.0%) | 70 B/s (-11.4%) | 388.15 KiB/s (+95.1%) |
| v2 GET/full history all | 3,298 (+203.7%) | 17,829 (-93.3%) | 1.41 MiB (-97.5%) | 0 B/s (-100.0%) | 488.62 KiB/s (+77.6%) | 0 B/s (-100.0%) | 405.17 KiB/s (+85.3%) | 0 B/s (-100.0%) | 387.62 KiB/s (+94.8%) |

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

Absolute v2 Veriflier resource use is higher at its high-throughput tiers, but
the throughput gain is much larger than the resource increase. Normalized per
1,000 checks/minute, the selected clean v2 tiers used meaningfully less CPU than
the v1 clean ceiling:

| Mode | CPU per 1k checks/min | vs v1 | RSS per 1k checks/min | vs v1 | FD per 1k checks/min | vs v1 |
|---|---:|---:|---:|---:|---:|---:|
| v1 legacy | 0.501% | baseline | 0.433 MiB | baseline | 8.60 | baseline |
| v2 HEAD/legacy | 0.138% | -72.4% | 0.136 MiB | -68.7% | 1.10 | -87.2% |
| v2 GET/simple_http | 0.244% | -51.2% | 0.289 MiB | -33.2% | 1.76 | -79.5% |
| v2 GET/full | 0.224% | -55.3% | 0.291 MiB | -32.7% | 2.17 | -74.8% |
| v2 mixed 50/25/25 | 0.283% | -43.6% | 0.238 MiB | -45.1% | 2.13 | -75.3% |

For rollout sizing, the Veriflier result supports a 4 vCPU floor, at least
1 GiB RAM, and a high file-descriptor limit. RSS stayed below 310 MiB in these
runs, but high-rate modes exceeded 2,200 FDs, so `LimitNOFILE` should leave
substantial headroom.

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

The real-URL data lines up with the expected rollout tradeoff:

- V2 HEAD/legacy is the closest behavioral match to v1. Bandwidth per check is
  in the same class while throughput is higher, RSS is lower, and FD use is much
  lower.
- V2 GET profiles intentionally use more network receive bandwidth because they
  fetch response bodies. That is expected and should be part of rollout capacity
  planning.
- V2 direct Veriflier calls had zero transport errors in this 50k sample; v1 had
  10 transport timeouts.

## Overall Findings

1. V2 Monitor process cost is much lower than v1 across every rollout stage. In the 10k-site, 30-minute cadence run, v2 used about 27-43% less CPU, about 96% less RSS, about 95-98% fewer open file descriptors, and about 97% fewer threads.

2. V2 shifts operational cost toward MySQL. HEAD/legacy, GET/simple_http, and GET/full all passed, but v2 performs many more DB queries and writes than v1 because it records runtime state, event data, audit rows, check-history rows, process health, and fleet dashboard data.

3. HEAD/legacy is the safest first rollout mode. It preserved the v1-style HEAD request shape, used less target HTTP traffic than v1 in this run, and still had materially lower process and StatsD cost.

4. GET/simple_http and GET/full intentionally use more target HTTP bandwidth. That is the price of richer detection, and it should be rolled out in stages after HEAD/legacy is stable.

5. `CHECK_HISTORY_MODE_DEFAULT=status_change` is a practical default. It produced a small check-history footprint while preserving status-change evidence. `disabled` avoids those writes, `sample` adds a controlled sampling cost, and `all` is expensive but now measured accurately.

6. Full check-history retention is the largest optional DB cost in this comparison. `history=all` wrote 62,701 check-history rows in the 30-minute 10k-site run and raised MySQL network traffic from about 42 MiB to about 71 MiB versus the status-change/full baseline.

7. StatsD is no longer a bottleneck in v2. V2 emitted roughly 93% fewer metric lines and roughly 97% less StatsD payload than v1 in these Monitor runs.

8. Existing Veriflier evidence still supports v2 as a substantial capacity upgrade over v1. The detailed Veriflier sections below are retained from the latest dedicated Veriflier comparison campaign; today’s run focused on Monitor cost.

## Caveats

- This is an internal-target cadence test, not a maximum capacity claim.
- WPCOM and live alert delivery stayed disabled by design.
- The v2 Monitor readiness endpoint is red in this test fleet because WPCOM notifications are intentionally disabled under a production profile. API health, checks, DB, StatsD, and Veriflier paths are still measured.
- Global DB counters are exact for the DB instance window; per-table I/O counters depend on MySQL `performance_schema` availability in the lab containers.
- The Veriflier capacity and real-URL bandwidth sections come from dedicated Veriflier comparison runs, not from the Monitor-focused run generated today.
