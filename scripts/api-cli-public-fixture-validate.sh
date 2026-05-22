#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"

PROJECT="${JETMON_API_VALIDATE_PROJECT:-jetmon-api-validate-public}"
PUBLIC_NETWORK="${JETMON_API_VALIDATE_NETWORK:-jetmon-api-validate-public}"
PUBLIC_SUBNET="${JETMON_API_VALIDATE_SUBNET:-93.184.220.0/24}"
FIXTURE_IP="${JETMON_API_VALIDATE_FIXTURE_IP:-93.184.220.20}"
WORK_DIR="$REPO_ROOT/logs/api-cli-validate"
CONFIG_FILE="$REPO_ROOT/config/config.json"
COMPOSE=(docker compose -p "$PROJECT" -f "$REPO_ROOT/docker/docker-compose.yml" -f "$REPO_ROOT/docker/docker-compose.scale-lab.yml")

export BIND_ADDR="${BIND_ADDR:-127.0.0.1}"
export API_BIND_ADDR="${API_BIND_ADDR:-127.0.0.1}"
export MYSQL_HOST_PORT="${MYSQL_HOST_PORT:-50307}"
export DASHBOARD_HOST_PORT="${DASHBOARD_HOST_PORT:-50080}"
export API_HOST_PORT="${API_HOST_PORT:-50090}"
export VERIFLIER_HOST_PORT="${VERIFLIER_HOST_PORT:-50803}"
export API_FIXTURE_HTTP_HOST_PORT="${API_FIXTURE_HTTP_HOST_PORT:-50091}"
export API_FIXTURE_HTTPS_HOST_PORT="${API_FIXTURE_HTTPS_HOST_PORT:-50443}"
export MAILPIT_HOST_PORT="${MAILPIT_HOST_PORT:-50025}"
export GRAPHITE_HOST_PORT="${GRAPHITE_HOST_PORT:-50088}"
export STATSD_HOST_PORT="${STATSD_HOST_PORT:-50225}"
export EMAIL_TRANSPORT=smtp
export SCALE_LAB_PUBLIC_NETWORK="$PUBLIC_NETWORK"
export SCALE_LAB_FIXTURE_IP="$FIXTURE_IP"

usage() {
	cat <<'USAGE'
usage: scripts/api-cli-public-fixture-validate.sh <run|cleanup>

Runs an isolated Docker API CLI validation stack that:
  1. disables WPCOM notifications and production alert paths
  2. sends email only to Docker-local Mailpit
  3. attaches Monitor, Veriflier, and api-fixture to a public-looking network
  4. runs scripts/api-cli-validate.sh with target safety still enabled

Useful overrides:
  JETMON_API_VALIDATE_PROJECT=jetmon-api-validate-public
  JETMON_API_VALIDATE_NETWORK=jetmon-api-validate-public
  JETMON_API_VALIDATE_SUBNET=93.184.220.0/24
  JETMON_API_VALIDATE_FIXTURE_IP=93.184.220.20
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
	docker network rm "$PUBLIC_NETWORK" >/dev/null 2>&1 || true
	pass "api_cli_public_fixture_validate_cleaned project=$PROJECT network=$PUBLIC_NETWORK"
}

prepare_config() {
	mkdir -p "$WORK_DIR" "$REPO_ROOT/logs" "$REPO_ROOT/stats"
	jq \
		--arg auth "api-cli-validate-wpcom-disabled" \
		--arg verifier_token "${VERIFLIER_AUTH_TOKEN:-veriflier_1_auth_token}" \
		'.AUTH_TOKEN = $auth
		| .WPCOM_NOTIFY_ENABLE = false
		| .EMAIL_TRANSPORT = "smtp"
		| .EMAIL_FROM = "jetmon@noreply.invalid"
		| .SMTP_HOST = "mailpit"
		| .SMTP_PORT = 1025
		| .SMTP_USERNAME = ""
		| .SMTP_PASSWORD = ""
		| .SMTP_USE_TLS = false
		| .WPCOM_EMAIL_ENDPOINT = ""
		| .WPCOM_EMAIL_AUTH_TOKEN = ""
		| .API_PORT = 8090
		| .DASHBOARD_PORT = 8080
		| .DASHBOARD_BIND_ADDR = "127.0.0.1"
		| .DEBUG_PORT = 0
		| .ROLLOUT_MODE = "active"
		| .DELIVERY_OWNER_HOST = "jetmon-scale-1"
		| .PEER_OFFLINE_LIMIT = 1
		| .NUM_WORKERS = 8
		| .DATASET_SIZE = 50
		| .NET_COMMS_TIMEOUT = 3
		| .BODY_READ_MAX_MS = 250
		| .BUCKET_TOTAL = 12
		| .BUCKET_TARGET = 12
		| .VERIFLIERS = [{"name":"Docker Veriflier","host":"veriflier","port":"7803","auth_token":$verifier_token}]' \
		"$REPO_ROOT/config/config-sample.json" >"$CONFIG_FILE"
	pass "safe_config_written=$CONFIG_FILE wpcom_notify=false email_transport=smtp mailpit_only=true"
}

ensure_public_network() {
	if docker network inspect "$PUBLIC_NETWORK" >/dev/null 2>&1; then
		pass "docker_network_exists=$PUBLIC_NETWORK"
		return
	fi
	docker network create --subnet "$PUBLIC_SUBNET" "$PUBLIC_NETWORK" >/dev/null
	pass "docker_network_created=$PUBLIC_NETWORK subnet=$PUBLIC_SUBNET"
}

wait_for_api() {
	local deadline=$((SECONDS + 180))
	until curl -fsS "http://127.0.0.1:$API_HOST_PORT/api/v1/health" >/dev/null 2>&1; do
		(( SECONDS < deadline )) || fail "API did not become healthy"
		sleep 2
	done
	pass "api_healthy"
}

create_api_token() {
	local out
	out="$(compose exec -T jetmon ./jetmon2 keys create --consumer api-cli-validate --scope admin --created-by api-cli-public-fixture-validate)"
	API_TOKEN="$(printf '%s\n' "$out" | awk '/^jm_/ {print; exit}')"
	[[ -n "$API_TOKEN" ]] || fail "could not parse API token"
	pass "api_token_created"
}

run_validation() {
	local binary="${API_CLI_BINARY:-$REPO_ROOT/bin/jetmon2}"
	[[ -x "$binary" ]] || fail "API_CLI_BINARY is not executable: $binary"
	cd "$REPO_ROOT"
	JETMON_API_URL="http://127.0.0.1:$API_HOST_PORT" \
	JETMON_API_TOKEN="$API_TOKEN" \
	JETMON_API_FIXTURE_URL="http://$FIXTURE_IP:8091" \
	JETMON_API_FIXTURE_PROBE_URL="http://127.0.0.1:$API_FIXTURE_HTTP_HOST_PORT/health" \
	JETMON_API_WEBHOOK_FIXTURE_URL="http://api-fixture:8091/webhook" \
	JETMON_API_WEBHOOK_FIXTURE_REQUESTS_URL="http://127.0.0.1:$API_FIXTURE_HTTP_HOST_PORT/webhook/requests" \
	API_CLI_BINARY="$binary" \
	"$REPO_ROOT/scripts/api-cli-validate.sh"
	pass "api_cli_public_fixture_validate_complete project=$PROJECT fixture=$FIXTURE_IP"
}

run_lab() {
	need_cmd curl
	need_cmd docker
	need_cmd jq
	cd "$REPO_ROOT"
	cleanup >/dev/null 2>&1 || true
	prepare_config
	ensure_public_network

	log "starting validation stack project=$PROJECT fixture=$FIXTURE_IP"
	compose up -d --build mysqldb mysql-user mailpit statsd api-fixture veriflier jetmon
	wait_for_api
	create_api_token
	compose exec -T jetmon curl -fsS "http://$FIXTURE_IP:8091/health" >/dev/null
	pass "fixture_reachable_from_monitor=$FIXTURE_IP"
	run_validation
}

case "${1:-run}" in
	run)
		run_lab
		;;
	cleanup)
		cleanup
		;;
	-h | --help | help)
		usage
		;;
	*)
		usage >&2
		exit 2
		;;
esac
