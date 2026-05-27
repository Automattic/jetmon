#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"

PROJECT="${JETMON_CADDY_TLS_LAB_PROJECT:-jetmon-caddy-tls-lab}"
WORK_DIR="$REPO_ROOT/logs/caddy-tls-lab"
COMPOSE=(docker compose -p "$PROJECT" -f "$REPO_ROOT/docker/docker-compose.yml" -f "$REPO_ROOT/docker/docker-compose.caddy-lab.yml")

export BIND_ADDR="${BIND_ADDR:-127.0.0.1}"
export API_BIND_ADDR="${API_BIND_ADDR:-127.0.0.1}"
export MYSQL_HOST_PORT="${MYSQL_HOST_PORT:-27307}"
export DASHBOARD_HOST_PORT="${DASHBOARD_HOST_PORT:-27080}"
export API_HOST_PORT="${API_HOST_PORT:-27090}"
export VERIFLIER_HOST_PORT="${VERIFLIER_HOST_PORT:-27803}"
export API_FIXTURE_HTTP_HOST_PORT="${API_FIXTURE_HTTP_HOST_PORT:-27091}"
export API_FIXTURE_HTTPS_HOST_PORT="${API_FIXTURE_HTTPS_HOST_PORT:-27443}"
export MAILPIT_HOST_PORT="${MAILPIT_HOST_PORT:-27025}"
export GRAPHITE_HOST_PORT="${GRAPHITE_HOST_PORT:-27088}"
export STATSD_HOST_PORT="${STATSD_HOST_PORT:-27225}"
export CADDY_HTTP_HOST_PORT="${CADDY_HTTP_HOST_PORT:-27180}"
export CADDY_HTTPS_HOST_PORT="${CADDY_HTTPS_HOST_PORT:-27444}"
export CONFIG_PROFILE=dev
export WPCOM_NOTIFY_ENABLE=false
export JETMON_CONFIG_RENDER_MODE=always
export VERIFLIER_CONFIG_RENDER_MODE=always

usage() {
	cat <<'USAGE'
usage: scripts/caddy-tls-lab.sh <run|cleanup>

Runs a local Docker TLS lab that:
  1. starts Caddy with tls internal for veriflier.local.test and monitor-api.local.test
  2. makes Jetmon trust Caddy's generated local root CA
  3. verifies host-side HTTPS through Caddy without -k
  4. verifies jetmon2 doctor can reach the configured Veriflier over HTTPS
USAGE
}

log() {
	printf 'INFO %s\n' "$*"
}

pass() {
	printf 'PASS %s\n' "$*"
}

fail() {
	printf 'FAIL %s\n' "$*" >&2
	exit 1
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"
}

compose() {
	"${COMPOSE[@]}" "$@"
}

cleanup() {
	cd "$REPO_ROOT"
	compose down -v --remove-orphans >/dev/null 2>&1 || true
	pass "caddy_tls_lab_cleaned project=$PROJECT"
}

wait_for_host_https() {
	local host=$1
	local path=$2
	local label=$3
	local deadline=$((SECONDS + 180))
	until curl -fsS \
		--cacert "$WORK_DIR/caddy-root.crt" \
		--resolve "$host:$CADDY_HTTPS_HOST_PORT:127.0.0.1" \
		"https://$host:$CADDY_HTTPS_HOST_PORT$path" >/dev/null 2>&1; do
		(( SECONDS < deadline )) || fail "$label did not become reachable through Caddy"
		sleep 2
	done
	pass "$label=https://$host:$CADDY_HTTPS_HOST_PORT$path"
}

run() {
	need_cmd docker
	need_cmd curl
	mkdir -p "$WORK_DIR" "$REPO_ROOT/logs" "$REPO_ROOT/stats"
	cd "$REPO_ROOT"

	log "starting Docker Caddy TLS lab project=$PROJECT"
	compose build jetmon veriflier
	compose up -d mysqldb mysql-user mailpit statsd veriflier caddy jetmon

	local deadline=$((SECONDS + 120))
	until compose exec -T caddy test -s /data/caddy/pki/authorities/local/root.crt >/dev/null 2>&1; do
		(( SECONDS < deadline )) || fail "Caddy internal root CA was not generated"
		sleep 2
	done
	compose exec -T caddy cat /data/caddy/pki/authorities/local/root.crt > "$WORK_DIR/caddy-root.crt"
	pass "caddy_internal_ca_exported=$WORK_DIR/caddy-root.crt"

	wait_for_host_https veriflier.local.test /v2/status veriflier_https_status
	wait_for_host_https monitor-api.local.test /api/v1/health monitor_api_https_health

	compose exec -T jetmon sh -ec '
		test -s "$SSL_CERT_FILE"
		./jetmon2 doctor --require-statsd
	' > "$WORK_DIR/jetmon-doctor.txt"
	if ! grep -q '^PASS verifliers ' "$WORK_DIR/jetmon-doctor.txt"; then
		cat "$WORK_DIR/jetmon-doctor.txt" >&2
		fail "jetmon doctor did not pass Veriflier HTTPS readiness"
	fi
	pass "monitor_trusts_caddy_ca_and_veriflier_https"
	pass "caddy_tls_lab_complete project=$PROJECT logs=$WORK_DIR"
}

case "${1:-}" in
	run)
		run
		;;
	cleanup)
		cleanup
		;;
	-h|--help|help)
		usage
		;;
	*)
		usage >&2
		exit 1
		;;
esac
