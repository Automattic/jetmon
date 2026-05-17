# Security Lab Client

`security-lab-client` is a lab-only helper used to exercise a temporary
Veriflier against adversarial HTTP responses. It is not a production binary and
is not built by `make all`.

The client posts fixed `/v2/check` scenarios to a Veriflier and fails if any
expected safety behavior regresses. It is intended to run against
`cmd/security-lab-responder` on isolated high ports, usually from one of the
approved Jetmon test hosts.
