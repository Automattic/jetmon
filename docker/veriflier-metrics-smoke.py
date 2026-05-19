#!/usr/bin/env python3
import json
import os
import socket
import sys
import time
import urllib.parse
import urllib.request


metric = os.getenv("METRICS_SMOKE_METRIC", "com.jetpack.jetmon.compose.metrics_smoke")
udp_host = os.getenv("STATSD_SMOKE_UDP_HOST", "statsd")
udp_port = int(os.getenv("STATSD_SMOKE_UDP_PORT", "8125"))
graphite_url = os.getenv("GRAPHITE_SMOKE_URL", "http://statsd")
attempts = int(os.getenv("METRICS_SMOKE_ATTEMPTS", "9"))
interval = float(os.getenv("METRICS_SMOKE_INTERVAL_SECONDS", "5"))

sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
sock.sendto(f"{metric}:1|c\n".encode("utf-8"), (udp_host, udp_port))
sock.close()

targets = [
    f"stats_counts.{metric}",
    metric,
]

last_error = ""
for attempt in range(1, attempts + 1):
    if attempt > 1:
        time.sleep(interval)
    for target in targets:
        query = urllib.parse.urlencode({
            "target": target,
            "from": "-10min",
            "format": "json",
        })
        url = f"{graphite_url.rstrip('/')}/render?{query}"
        try:
            with urllib.request.urlopen(url, timeout=5) as resp:
                payload = json.loads(resp.read().decode("utf-8"))
        except Exception as exc:
            last_error = f"{target}: {exc}"
            continue
        for series in payload:
            for value, _timestamp in series.get("datapoints", []):
                if value is not None:
                    print(f"PASS graphite_ingested target={target}")
                    sys.exit(0)
        last_error = f"{target}: no non-null datapoints yet"

print(f"FAIL graphite_ingestion metric={metric} last_error={last_error}", file=sys.stderr)
sys.exit(1)
