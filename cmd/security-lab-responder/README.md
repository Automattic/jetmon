# Security Lab Responder

`security-lab-responder` is a lab-only helper that serves adversarial HTTP and
HTTPS responses for Monitor / Veriflier safety validation. It is not a
production binary and is not built by `make all`.

The responder includes endpoints for unsafe redirects, redirect loops,
infinite and slow response bodies, oversized response headers, gzip-expanded
bodies, and self-signed TLS. Run it only on isolated high ports on approved
test hosts.
