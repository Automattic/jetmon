# Jetmon Deliverer Rollout

`jetmon-deliverer` is the standalone process for outbound webhook and
alert-contact delivery. It uses the same delivery code as embedded `jetmon2`
workers, but it does not run monitor checks, bucket ownership, the REST API, the
dashboard, or a Veriflier server.

Delivery rows are claimed with short transactional leases, so multiple active
delivery workers should not send the same row twice. `DELIVERY_OWNER_HOST`
remains the rollout guard when operators want exactly one process class sending
outbound traffic.

## Process Roles

| Process | Owns | Does not own |
|---|---|---|
| `jetmon2` with `API_PORT = 0` | monitor checks, bucket ownership, WPCOM legacy notifications | REST API, webhook delivery, alert-contact delivery |
| `jetmon2` with `API_PORT > 0` | REST API and optionally embedded delivery | standalone delivery isolation |
| `jetmon-deliverer` | webhook delivery, alert-contact delivery | REST API, monitor checks, bucket ownership, dashboard |

The intended split is:

- Monitor hosts run `jetmon2` for checks.
- API hosts run `jetmon2` for `/api/v1` traffic and usually do not deliver.
- Deliverer hosts run `jetmon-deliverer` for outbound dispatch.

## Configuration

`jetmon-deliverer` reads `JETMON_CONFIG` when set, otherwise
`config/config.json`. Use a process-specific config for deliverer hosts when API
hosts need a different `DELIVERY_OWNER_HOST` value.

A deliverer package or container needs:

- `bin/jetmon-deliverer`
- the same JSON config schema used by `jetmon2`
- database config via `DB_SERVER_MAP_*` or explicit `DB_*` JSON keys
- alert transport credentials for the selected `EMAIL_TRANSPORT`
- normal stdout/stderr log collection

For single-owner rollout, set:

```json
{
  "API_PORT": 0,
  "DELIVERY_OWNER_HOST": "deliverer-01",
  "EMAIL_TRANSPORT": "wpcom"
}
```

Use `EMAIL_TRANSPORT=stub` only for dry runs. Production alert-contact email
needs `wpcom` or `smtp` plus the matching credentials.

## Conservative Cutover

This is the preferred path from embedded delivery to standalone delivery.

1. Build and stage `bin/jetmon-deliverer`.
2. Install the deployment unit or Docker Compose service.
3. Pick one owner host and set `DELIVERY_OWNER_HOST` to that host's process
   hostname in the deliverer config.
4. Give API hosts a config where `DELIVERY_OWNER_HOST` does not match their
   process hostnames, so API traffic continues without embedded delivery.
5. Validate the deliverer config from the same environment the service will use:

   ```bash
   JETMON_CONFIG=/opt/jetmon2/config/deliverer.json \
     /opt/jetmon2/bin/jetmon-deliverer validate-config \
       --require-owner-match \
       --require-api-disabled
   ```

   Add `--require-email-delivery` when real email alert contacts must send.
6. Start `jetmon-deliverer` on the owner host.
7. Confirm logs show delivery workers enabled on that host.
8. Confirm API-host logs show embedded delivery skipped or idle.
9. Watch delivery backlog:

   ```bash
   JETMON_CONFIG=/opt/jetmon2/config/deliverer.json \
     /opt/jetmon2/bin/jetmon-deliverer delivery-check --since=15m
   ```

10. Use a strict gate once the queue should be drained:

    ```bash
    JETMON_CONFIG=/opt/jetmon2/config/deliverer.json \
      /opt/jetmon2/bin/jetmon-deliverer delivery-check \
        --since=15m \
        --max-due=0 \
        --max-abandoned=0 \
        --max-failed=0
    ```

Rollback is to stop `jetmon-deliverer`, restore the previous embedded owner
config, start the API host that should resume delivery, and watch
`delivery-check` until pending rows drain normally.

## Active-Active Delivery

Active-active delivery is safe at the row-claim level, but it should be enabled
intentionally:

- If `DELIVERY_OWNER_HOST` is set, only the exact matching hostname runs
  delivery workers.
- If `DELIVERY_OWNER_HOST` is empty, every eligible API process and every
  `jetmon-deliverer` process can run delivery workers.

Do not clear `DELIVERY_OWNER_HOST` in a config shared by API hosts and
deliverer hosts unless that mixed active-active state is the intended rollout.
Prefer process-specific configs:

- API hosts: set `DELIVERY_OWNER_HOST` to a non-matching guard value.
- Deliverer hosts: leave the guard empty only when standalone active-active is
  approved, or use one owner value for conservative single-owner delivery.

## Rollout Checks

Before enabling standalone delivery:

- `bin/jetmon-deliverer version` reports the expected build.
- `validate-config --require-owner-match --require-api-disabled` passes with
  the service's real config and database credentials.
- `--require-email-delivery` is used when email delivery must be live.
- `delivery-check --since=15m --output=json` returns clean JSON for automation.
- The deployment unit or Compose service uses the same config path that passed
  validation.
- MySQL connectivity and schema validation match the active `jetmon2` fleet.
- Owner-host behavior has been verified on each process class before traffic.

During rollout:

- `delivery-check --since=15m` shows no sustained pending growth.
- The strict gate passes once the queue has drained.
- `OLDEST_PENDING_SEC` and `OLDEST_DUE_SEC` stay bounded. A growing oldest age is
  a stronger signal than a one-time backlog spike.
- Logs show only the intended process class running workers.
- Use `--require-recent-delivery` only when the rollout window should include a
  real delivery. Quiet environments can be healthy with no recent sends.
- Use `--require-recent-webhook-delivery` and
  `--require-recent-alert-delivery` when both delivery families must prove a
  successful send independently.

Example output:

```text
INFO deliverer_host="deliverer-01"
INFO delivery_check_since=2026-04-29T18:15:00Z
INFO delivery_owner_host="deliverer-01" matched; delivery workers enabled on this host
KIND     PENDING  DUE_NOW  FUTURE_RETRY  DELIVERED_SINCE  ABANDONED_SINCE  FAILED_SINCE  OLDEST_PENDING_SEC  OLDEST_DUE_SEC
webhook  0        0        0             4                0                0             0                   0
alert    1        0        1             2                0                0             45                  0
total    1        0        1             6                0                0             45                  0
PASS delivery_check=ok
```

After rollout:

- Keep embedded delivery disabled on API hosts unless active-active delivery is
  intentional.
- Revisit `internal/webhooks` and `internal/alerting` duplication only after the
  standalone process has enough production history to show real drift.
- Plan WPCOM legacy notification migration into this process after alert-contact
  parity and recipient inventory are known.

## Failure Modes

| Failure | Expected behavior | Operator action |
|---|---|---|
| Deliverer exits | Leases expire; rows become claimable again | Restart deliverer or roll back to embedded delivery |
| Wrong owner hostname | Deliverer starts but idles | Fix `DELIVERY_OWNER_HOST` or process hostname/config |
| Shared config clears owner guard | API and deliverer hosts may all dispatch | Restore per-process configs; row claims prevent duplicate row sends but load rises |
| Email transport left as `stub` | Email alerts are logged but not sent | Set the real transport and credentials, then restart |
| Third-party outage | Rows retry on the ladder and eventually abandon | Fix destination/provider issue, then use manual retry endpoints |
