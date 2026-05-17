#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

jetmon_binary="${JETMON_MIGRATION_SMOKE_BINARY:-./bin/jetmon2}"
images="${JETMON_MIGRATION_SMOKE_IMAGES:-mariadb:11.4.8 mariadb:11.4.10}"
root_password="${JETMON_MIGRATION_SMOKE_ROOT_PASSWORD:-123456}"
db_name="${JETMON_MIGRATION_SMOKE_DB_NAME:-jetmon_db}"
db_user="${JETMON_MIGRATION_SMOKE_DB_USER:-jetmon}"
db_password="${JETMON_MIGRATION_SMOKE_DB_PASSWORD:-jetmon_dev_password}"
containers=()

step() {
	printf '\n== %s ==\n' "$1"
}

fail() {
	printf 'FAIL %s\n' "$1" >&2
	exit 1
}

cleanup() {
	local container
	for container in "${containers[@]}"; do
		docker rm -f "$container" >/dev/null 2>&1 || true
	done
}
trap cleanup EXIT

if [[ ! -x "$jetmon_binary" ]]; then
	fail "jetmon binary is not executable: $jetmon_binary"
fi
if ! command -v docker >/dev/null 2>&1; then
	fail "docker is required"
fi

mapfile -t migration_ids < <(sed -n 's/^[[:space:]]*{\([0-9][0-9]*\),.*/\1/p' internal/db/migrations.go)
if [[ "${#migration_ids[@]}" -eq 0 ]]; then
	fail "could not find embedded migration ids"
fi
expected_count="${#migration_ids[@]}"
expected_max=0
for id in "${migration_ids[@]}"; do
	if (( id > expected_max )); then
		expected_max="$id"
	fi
done

wait_for_mariadb() {
	local container="$1"
	local attempt
	for attempt in {1..60}; do
		if docker exec "$container" mariadb-admin ping -h 127.0.0.1 -uroot "-p${root_password}" --silent >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	return 1
}

run_smoke() {
	local image="$1"
	local suffix container actual count max_id columns

	suffix="$(printf '%s' "$image" | tr -c '[:alnum:]' '-')"
	container="jetmon-migration-smoke-${suffix}-$$"
	containers+=("$container")

	step "$image"
	docker run --rm --name "$container" \
		-e MARIADB_ROOT_PASSWORD="$root_password" \
		-e MARIADB_DATABASE="$db_name" \
		-e MARIADB_USER="$db_user" \
		-e MARIADB_PASSWORD="$db_password" \
		-d "$image" >/dev/null

	wait_for_mariadb "$container" || fail "$image did not become ready"
	docker cp "$jetmon_binary" "$container:/tmp/jetmon2" >/dev/null

	docker exec \
		-e DB_HOST=127.0.0.1 \
		-e DB_PORT=3306 \
		-e DB_USER="$db_user" \
		-e DB_PASSWORD="$db_password" \
		-e DB_NAME="$db_name" \
		"$container" /tmp/jetmon2 migrate
	docker exec \
		-e DB_HOST=127.0.0.1 \
		-e DB_PORT=3306 \
		-e DB_USER="$db_user" \
		-e DB_PASSWORD="$db_password" \
		-e DB_NAME="$db_name" \
		"$container" /tmp/jetmon2 migrate

	actual="$(docker exec "$container" mariadb \
		-h 127.0.0.1 \
		-u"$db_user" \
		"-p${db_password}" \
		--batch \
		--skip-column-names \
		-e 'SELECT COUNT(*), COALESCE(MAX(id), 0) FROM jetmon_schema_migrations;' \
		"$db_name")"
	read -r count max_id <<<"$actual"
	if [[ "$count" != "$expected_count" || "$max_id" != "$expected_max" ]]; then
		fail "$image applied migrations count=$count max=$max_id, want count=$expected_count max=$expected_max"
	fi

	columns="$(docker exec "$container" mariadb \
		-h 127.0.0.1 \
		-u"$db_user" \
		"-p${db_password}" \
		--batch \
		--skip-column-names \
		-e "SHOW COLUMNS FROM jetmon_process_health WHERE Field IN ('runtime_goroutines','runtime_threads');" \
		"$db_name")"
	grep -q '^runtime_goroutines[[:space:]]' <<<"$columns" || fail "$image missing runtime_goroutines column"
	grep -q '^runtime_threads[[:space:]]' <<<"$columns" || fail "$image missing runtime_threads column"

	printf 'PASS image=%s migrations=%s max_id=%s\n' "$image" "$count" "$max_id"
}

for image in $images; do
	run_smoke "$image"
done

printf '\nMariaDB migration smoke passed for: %s\n' "$images"
