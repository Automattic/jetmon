#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

jetmon_binary="${JETMON_DELIVERY_CLAIM_SMOKE_BINARY:-./bin/jetmon2}"
image="${JETMON_DELIVERY_CLAIM_SMOKE_IMAGE:-mariadb:11.4.10}"
root_password="${JETMON_DELIVERY_CLAIM_SMOKE_ROOT_PASSWORD:-123456}"
db_name="${JETMON_DELIVERY_CLAIM_SMOKE_DB_NAME:-jetmon_delivery_claim_smoke}"
db_user="${JETMON_DELIVERY_CLAIM_SMOKE_DB_USER:-jetmon}"
db_password="${JETMON_DELIVERY_CLAIM_SMOKE_DB_PASSWORD:-jetmon_dev_password}"
work_dir="${JETMON_DELIVERY_CLAIM_SMOKE_WORK_DIR:-}"
cleanup_work_dir=0
container=""

step() {
	printf '\n== %s ==\n' "$1"
}

fail() {
	printf 'FAIL %s\n' "$1" >&2
	exit 1
}

cleanup() {
	if [[ -n "$container" ]]; then
		docker rm -f "$container" >/dev/null 2>&1 || true
	fi
	if [[ "$cleanup_work_dir" -eq 1 && -n "$work_dir" ]]; then
		rm -rf "$work_dir"
	fi
}
trap cleanup EXIT

if [[ ! -x "$jetmon_binary" ]]; then
	fail "jetmon binary is not executable: $jetmon_binary"
fi
if ! command -v docker >/dev/null 2>&1; then
	fail "docker is required"
fi
if [[ -z "$work_dir" ]]; then
	work_dir="$(mktemp -d "${TMPDIR:-/tmp}/jetmon-delivery-claim-smoke.XXXXXXXXXX")"
	cleanup_work_dir=1
fi
mkdir -p "$work_dir"

step "compile claim test binaries"
go test -c -o "$work_dir/webhooks.test" ./internal/webhooks
go test -c -o "$work_dir/alerting.test" ./internal/alerting

suffix="$(printf '%s' "$image" | tr -c '[:alnum:]' '-')"
container="jetmon-delivery-claim-smoke-${suffix}-$$"

step "$image"
docker run --rm --name "$container" \
	-e MARIADB_ROOT_PASSWORD="$root_password" \
	-e MARIADB_DATABASE="$db_name" \
	-e MARIADB_USER="$db_user" \
	-e MARIADB_PASSWORD="$db_password" \
	-d "$image" >/dev/null

for attempt in {1..60}; do
	if docker exec "$container" mariadb-admin ping -h 127.0.0.1 -uroot "-p${root_password}" --silent >/dev/null 2>&1; then
		break
	fi
	if [[ "$attempt" -eq 60 ]]; then
		fail "$image did not become ready"
	fi
	sleep 1
done

step "migrate schema"
docker cp "$jetmon_binary" "$container:/tmp/jetmon2" >/dev/null
docker exec \
	-e DB_HOST=127.0.0.1 \
	-e DB_PORT=3306 \
	-e DB_USER="$db_user" \
	-e DB_PASSWORD="$db_password" \
	-e DB_NAME="$db_name" \
	"$container" /tmp/jetmon2 migrate

step "run claim tests"
docker cp "$work_dir/webhooks.test" "$container:/tmp/webhooks.test" >/dev/null
docker cp "$work_dir/alerting.test" "$container:/tmp/alerting.test" >/dev/null
dsn="${db_user}:${db_password}@tcp(127.0.0.1:3306)/${db_name}?parseTime=true"
docker exec -e JETMON_DELIVERY_CLAIM_TEST_DSN="$dsn" "$container" \
	/tmp/webhooks.test -test.run TestClaimReadySkipsLockedRowsMariaDB -test.v
docker exec -e JETMON_DELIVERY_CLAIM_TEST_DSN="$dsn" "$container" \
	/tmp/alerting.test -test.run TestClaimReadySkipsLockedRowsMariaDB -test.v

printf '\nDelivery claim concurrency smoke passed for %s\n' "$image"
