# Jetmon Deliverer Rollout

**Status:** Operational runbook for the existing v2 implementation.

`jetmon-deliverer` is the first standalone process boundary for outbound
delivery. It runs the webhook and alert-contact workers without starting the
monitor round loop, REST API, dashboard, Veriflier server, or bucket ownership.

The code path is shared with embedded `jetmon2` delivery through
`internal/deliverer`. Delivery rows are claimed with short transactional
`SELECT ... FOR UPDATE` leases, so multiple active delivery workers cannot
claim the same pending delivery row. `DELIVERY_OWNER_HOST` remains useful as a
rollout guard when operators want a deliberately single-owner cutover.

## Process Responsibilities

| Process | Owns | Does not own |
|---|---|---|
| `jetmon2` with `API_PORT = 0` | monitor rounds, bucket ownership, checks, WPCOM legacy notifications | REST API, webhook delivery, alert-contact delivery |
| `jetmon2` with `API_PORT > 0` | REST API and, when allowed by `DELIVERY_OWNER_HOST`, embedded delivery | standalone process isolation for delivery |
| `jetmon-deliverer` | webhook delivery and alert-contact delivery | REST API, monitor rounds, bucket ownership, dashboard |

The production target for the split is:

- monitor hosts run `jetmon2` with monitor responsibilities only;
- API hosts run `jetmon2` for `/api/v1` traffic but do not own delivery;
- deliverer hosts run `jetmon-deliverer` for outbound dispatch.

## Package Contents

A production package for the deliverer should include:

- `bin/jetmon-deliverer`
- `systemd/jetmon-deliverer.service` or the equivalent deployment-system unit
- the same `config/config.json` schema used by `jetmon2`
- database config via the same `DB_*` environment variables used by `jetmon2`
- alert transport credentials required by the selected `EMAIL_TRANSPORT`
- log routing equivalent to the existing `jetmon2` service

The binary uses `JETMON_CONFIG` when set, otherwise it reads
`config/config.json`. Use a separate config file per process class when API
hosts and deliverer hosts need different `DELIVERY_OWNER_HOST` values.

The sample systemd unit expects:

- `ExecStart=/opt/jetmon2/bin/jetmon-deliverer`
- `EnvironmentFile=-/opt/jetmon2/config/jetmon2.env`
- `JETMON_CONFIG=/opt/jetmon2/config/deliverer.json`

Keep `deliverer.json` process-specific. Sharing a config file with API-enabled
`jetmon2` hosts is only safe when `DELIVERY_OWNER_HOST` is intentionally set for
all process classes that read it.

## Single-Owner Cutover

This is the conservative migration path from embedded delivery to standalone
delivery.

1. Build and package `bin/jetmon-deliverer`.
2. Install and enable `systemd/jetmon-deliverer.service` or the equivalent
   deployment-system unit.
3. Pick one deliverer host and set `DELIVERY_OWNER_HOST` to that host's
   hostname in the deliverer config.
4. Keep embedded API hosts from delivering by giving their `jetmon2` process a
   config where `DELIVERY_OWNER_HOST` does not match the API hostnames. The
   most common pattern is a process-specific config file via `JETMON_CONFIG`.
5. Start `jetmon-deliverer` on the owner host.
6. Confirm logs show `delivery_owner_host="<host>" matched; delivery workers
   enabled on this host`.
7. Confirm API-host logs show delivery workers are skipped or idle.
8. Watch `jetmon_webhook_deliveries` and `jetmon_alert_deliveries` for pending
   backlog, abandon rate, and retry volume.
9. Stop embedded delivery after the standalone owner has been stable for at
   least one normal alerting window.

Rollback is simple: stop `jetmon-deliverer` and restore the previous embedded
delivery config so one API-enabled `jetmon2` host matches
`DELIVERY_OWNER_HOST` or uses the legacy empty-owner behavior.

## Active-Active Delivery

Transactional row claims make active-active delivery safe at the delivery-row
level. The remaining rollout question is process selection:

- If `DELIVERY_OWNER_HOST` is set, only the exact matching hostname runs
  delivery workers.
- If `DELIVERY_OWNER_HOST` is empty, every eligible `jetmon2` process with
  `API_PORT > 0` and every `jetmon-deliverer` process runs delivery workers.

Therefore, active-active standalone delivery should use process-specific
configs:

- API hosts: set `DELIVERY_OWNER_HOST` to a non-matching guard value so they
  serve API traffic without dispatching outbound delivery.
- Deliverer hosts: leave `DELIVERY_OWNER_HOST` empty, or run one config per
  deliverer host while keeping the guard disabled only for that process class.

Do not clear `DELIVERY_OWNER_HOST` in a shared config that is also used by
API-enabled `jetmon2` hosts unless the intended state is active-active delivery
from both API hosts and standalone deliverer hosts.

## Rollout Checks

Before enabling standalone delivery:

- `bin/jetmon-deliverer version` reports the expected build.
- `JETMON_CONFIG=/opt/jetmon2/config/deliverer.json bin/jetmon-deliverer
  validate-config` passes for the deliverer-specific config while running with
  the same `DB_*` environment the service will use.
- `systemd-analyze verify systemd/jetmon-deliverer.service` passes, or the
  deployment-system equivalent validates the service definition.
- The process can connect to MySQL using the same schema as `jetmon2`.
- `EMAIL_TRANSPORT` is set to `wpcom` or `smtp` in any environment where real
  alert-contact emails should be delivered; `stub` is safe for dry runs.
- `DELIVERY_OWNER_HOST` behavior is validated with one start on each process
  class before production traffic.

During rollout:

- No sustained growth in `status = 'pending'` rows.
- No unexpected increase in `status = 'abandoned'` rows.
- Logs show only the intended process class running workers.
- Webhook and alert-contact manual retry endpoints still work.

After rollout:

- Keep embedded delivery disabled on API hosts unless intentionally testing
  active-active behavior.
- Revisit `internal/webhooks` and `internal/alerting` duplication only after
  standalone delivery has run long enough to expose real operational drift.
- Plan WPCOM legacy notification migration into this process once alert-contact
  parity and recipient inventory are known.

## Failure Modes

| Failure | Expected behavior | Operator action |
|---|---|---|
| Deliverer process exits | In-flight leases expire after the claim lock duration; rows become claimable again | Restart deliverer or roll back to embedded delivery |
| Wrong owner hostname | Deliverer starts but idles | Fix `DELIVERY_OWNER_HOST` or process hostname/config |
| Shared config accidentally clears owner guard | API hosts and deliverer hosts may all dispatch | Restore per-process configs; row claims prevent duplicate row claims but extra processes add load |
| Email transport left as `stub` | Email alerts are logged but not sent | Set `EMAIL_TRANSPORT` and transport credentials, then restart |
| Third-party outage | Rows retry on the documented ladder and eventually abandon | Fix destination or provider issue, then use manual retry endpoints |
