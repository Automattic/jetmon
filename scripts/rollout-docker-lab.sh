#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"

PROJECT="${JETMON_ROLLOUT_DOCKER_PROJECT:-jetmon-rollout-lab}"
PUBLIC_NETWORK="${JETMON_ROLLOUT_DOCKER_NETWORK:-jetmon-rollout-lab-public}"
PUBLIC_SUBNET="${JETMON_ROLLOUT_DOCKER_SUBNET:-93.184.216.0/24}"
FIXTURE_IP="${JETMON_ROLLOUT_DOCKER_FIXTURE_IP:-93.184.216.20}"
DOCKER_DNS_TEST_IMAGE="${JETMON_ROLLOUT_DOCKER_DNS_TEST_IMAGE:-golang:1.26.3}"
SITE_COUNT="${JETMON_ROLLOUT_DOCKER_SITE_COUNT:-40}"
BUCKET_MIN="${JETMON_ROLLOUT_DOCKER_BUCKET_MIN:-0}"
BUCKET_MAX="${JETMON_ROLLOUT_DOCKER_BUCKET_MAX:-0}"
RUN_ID="${JETMON_ROLLOUT_DOCKER_RUN_ID:-rollout-docker-lab-$(date -u +%Y%m%d%H%M%S)}"
CHANGE_REF="${JETMON_ROLLOUT_DOCKER_CHANGE_REF:-overnight-rollout-docker-lab}"
WORK_DIR="$REPO_ROOT/logs/rollout-docker-lab"
SITE_FIXTURE_FILE="$REPO_ROOT/stats/rollout-docker-lab/sites.json"
SITE_FIXTURE_CONTAINER_FILE="/jetmon/stats/rollout-docker-lab/sites.json"
CANARY_FIXTURE_FILE="$REPO_ROOT/stats/rollout-docker-lab/canaries.json"
CANARY_FIXTURE_CONTAINER_FILE="/jetmon/stats/rollout-docker-lab/canaries.json"
CONFIG_FILE="$REPO_ROOT/config/config.json"
COMPOSE=(docker compose -p "$PROJECT" -f "$REPO_ROOT/docker/docker-compose.yml" -f "$REPO_ROOT/docker/docker-compose.rollout-lab.yml")

export BIND_ADDR="${BIND_ADDR:-127.0.0.1}"
export API_BIND_ADDR="${API_BIND_ADDR:-127.0.0.1}"
export MYSQL_HOST_PORT="${MYSQL_HOST_PORT:-16307}"
export DASHBOARD_HOST_PORT="${DASHBOARD_HOST_PORT:-16080}"
export API_HOST_PORT="${API_HOST_PORT:-16090}"
export VERIFLIER_HOST_PORT="${VERIFLIER_HOST_PORT:-17803}"
export API_FIXTURE_HTTP_HOST_PORT="${API_FIXTURE_HTTP_HOST_PORT:-18091}"
export API_FIXTURE_HTTPS_HOST_PORT="${API_FIXTURE_HTTPS_HOST_PORT:-18443}"
export MAILPIT_HOST_PORT="${MAILPIT_HOST_PORT:-18025}"
export GRAPHITE_HOST_PORT="${GRAPHITE_HOST_PORT:-18088}"
export STATSD_HOST_PORT="${STATSD_HOST_PORT:-18125}"
export EMAIL_TRANSPORT=stub
export JETMON_CONFIG_RENDER_MODE=never
export VERIFLIER_CONFIG_RENDER_MODE=always
export ROLLOUT_LAB_PUBLIC_NETWORK="$PUBLIC_NETWORK"
export ROLLOUT_LAB_FIXTURE_IP="$FIXTURE_IP"

usage() {
	cat <<'USAGE'
usage: scripts/rollout-docker-lab.sh <run|doctor|cleanup>

Runs a local-only Docker rollout rehearsal:
  1. starts Jetmon in ROLLOUT_MODE=api-controlled with WPCOM disabled
  2. seeds public-looking, Docker-internal fixture sites
  3. activates the bucket range
  4. verifies recent Monitor activity
  5. stages HEAD/legacy -> GET/simple_http -> GET/full policy changes
  6. releases the bucket range back to standby

The fixture lives on a Docker network using a public-looking RFC-valid address
so target-safety checks stay enabled while traffic never leaves the Docker host.

doctor checks local prerequisites, including Docker daemon DNS, before a full
Compose build is started.
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

docker_dns_preflight() {
	local output
	if output="$(docker run --rm "$DOCKER_DNS_TEST_IMAGE" sh -ec '
		for host in proxy.golang.org deb.debian.org; do
			getent hosts "$host" >/dev/null
		done
	' 2>&1)"; then
		pass "docker_dns_preflight_ok image=$DOCKER_DNS_TEST_IMAGE hosts=proxy.golang.org,deb.debian.org"
		return
	fi
	printf '%s\n' "$output" >&2
	fail "Docker daemon DNS preflight failed; fix Docker resolver/networking before running rollout-docker-lab"
}

api() {
	compose exec -T \
		-e JETMON_API_URL=http://127.0.0.1:8090 \
		-e JETMON_API_TOKEN="$API_TOKEN" \
		jetmon ./jetmon2 api "$@"
}

sql() {
	compose exec -T mysqldb mariadb \
		-u"${MYSQL_USER:-jetmon}" "-p${MYSQL_PASSWORD:-jetmon_dev_password}" \
		"${MYSQL_DATABASE:-jetmon_db}" "$@"
}

cleanup() {
	cd "$REPO_ROOT"
	compose down -v --remove-orphans >/dev/null 2>&1 || true
	docker network rm "$PUBLIC_NETWORK" >/dev/null 2>&1 || true
	pass "rollout_docker_lab_cleaned project=$PROJECT network=$PUBLIC_NETWORK"
}

prepare_config() {
	mkdir -p "$WORK_DIR" "$REPO_ROOT/logs" "$(dirname "$SITE_FIXTURE_FILE")"
	jq \
		--arg db_user "${MYSQL_USER:-jetmon}" \
		--arg db_password "${MYSQL_PASSWORD:-jetmon_dev_password}" \
		--arg db_name "${MYSQL_DATABASE:-jetmon_db}" \
		--arg auth "rollout-lab-wpcom-disabled" \
		--arg fixture_host "veriflier" \
		--arg verifier_token "${VERIFLIER_AUTH_TOKEN:-veriflier_1_auth_token}" \
		'.AUTH_TOKEN = $auth
		| .DB_HOST = "mysqldb"
		| .DB_PORT = "3306"
		| .DB_USER = $db_user
		| .DB_PASSWORD = $db_password
		| .DB_NAME = $db_name
		| .STATSD_ADDR = "statsd:8125"
		| .WPCOM_NOTIFY_ENABLE = false
		| .EMAIL_TRANSPORT = "stub"
		| .WPCOM_EMAIL_ENDPOINT = ""
		| .WPCOM_EMAIL_AUTH_TOKEN = ""
		| .API_PORT = 8090
		| .DASHBOARD_PORT = 8080
		| .DASHBOARD_BIND_ADDR = "127.0.0.1"
		| .DEBUG_PORT = 0
		| .ROLLOUT_MODE = "api-controlled"
		| .DEFAULT_CHECK_METHOD = "HEAD"
		| .DEFAULT_DETECTION_PROFILE = "legacy"
		| .PEER_OFFLINE_LIMIT = 1
		| .NUM_WORKERS = 12
		| .DATASET_SIZE = 50
		| .NET_COMMS_TIMEOUT = 3
		| .BODY_READ_MAX_MS = 250
		| .BUCKET_TOTAL = 1000
		| .BUCKET_TARGET = 1000
		| .VERIFLIERS = [{"name":"Docker Veriflier","host":$fixture_host,"port":"7803","auth_token":$verifier_token}]' \
		"$REPO_ROOT/config/config-sample.json" >"$CONFIG_FILE"
	pass "safe_config_written=$CONFIG_FILE wpcom_notify=false email_transport=stub rollout_mode=api-controlled"
}

prepare_fixture_sites() {
	cat >"$SITE_FIXTURE_FILE" <<JSON
[
  {
    "monitor_url": "http://$FIXTURE_IP:8091/health",
    "check_keyword": "ok",
    "redirect_policy": "follow",
    "request_method": "HEAD",
    "detection_profile": "legacy",
    "timeout_seconds": 3,
    "check_interval": 5,
    "alert_cooldown_minutes": 0
  },
  {
    "monitor_url": "http://$FIXTURE_IP:8091/ok",
    "check_keyword": "jetmon fixture ok",
    "redirect_policy": "follow",
    "request_method": "HEAD",
    "detection_profile": "legacy",
    "timeout_seconds": 3,
    "check_interval": 5,
    "alert_cooldown_minutes": 0
  },
  {
    "monitor_url": "http://$FIXTURE_IP:8091/keyword",
    "check_keyword": "keyword present",
    "redirect_policy": "follow",
    "request_method": "HEAD",
    "detection_profile": "legacy",
    "timeout_seconds": 3,
    "check_interval": 5,
    "alert_cooldown_minutes": 0
  },
  {
    "monitor_url": "http://$FIXTURE_IP:8091/redirect",
    "redirect_policy": "follow",
    "request_method": "HEAD",
    "detection_profile": "legacy",
    "timeout_seconds": 3,
    "check_interval": 5,
    "alert_cooldown_minutes": 0
  }
]
JSON
	cat >"$CANARY_FIXTURE_FILE" <<JSON
{
  "canaries": [
    {
      "name": "fixture-known-up-head",
      "url": "http://$FIXTURE_IP:8091/health",
      "mode": "head-legacy",
      "expect_success": true,
      "expect_http_code": 200
    },
    {
      "name": "fixture-controlled-down-get",
      "url": "http://$FIXTURE_IP:8091/status/503",
      "method": "GET",
      "profile": "simple_http",
      "expect_success": false,
      "expect_http_code": 503
    },
    {
      "name": "fixture-keyword-full",
      "url": "http://$FIXTURE_IP:8091/keyword",
      "mode": "get-full",
      "keyword": "keyword present",
      "expect_success": true,
      "expect_http_code": 200
    }
  ]
}
JSON
	pass "fixture_sites_written=$SITE_FIXTURE_FILE count=$SITE_COUNT fixture=$FIXTURE_IP"
	pass "fixture_canaries_written=$CANARY_FIXTURE_FILE count=3"
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
	until compose exec -T jetmon curl -fsS http://127.0.0.1:8090/api/v1/health >/dev/null 2>&1; do
		(( SECONDS < deadline )) || fail "API did not become healthy"
		sleep 2
	done
	pass "api_healthy"
}

create_api_token() {
	local out
	out="$(compose exec -T jetmon ./jetmon2 keys create --consumer rollout-docker-lab --scope admin --created-by rollout-docker-lab)"
	API_TOKEN="$(printf '%s\n' "$out" | awk '/^jm_/ {print; exit}')"
	[[ -n "$API_TOKEN" ]] || fail "could not parse API token"
	pass "api_token_created"
}

seed_sites() {
	api sites bulk-add \
		--source file \
		--file "$SITE_FIXTURE_CONTAINER_FILE" \
		--count "$SITE_COUNT" \
		--batch rollout-docker-lab \
		--idempotency-key-prefix rollout-docker-lab-site \
		--output json >/dev/null
	pass "sites_seeded count=$SITE_COUNT bucket=$BUCKET_MIN"
}

json_token() {
	jq -r '.confirmation_token // empty'
}

require_ok_json() {
	local name="$1"
	local body="$2"
	local status
	status="$(printf '%s' "$body" | jq -r '.status // empty')"
	[[ "$status" == "ok" ]] || {
		printf '%s\n' "$body" | jq . >&2
		fail "$name returned status=$status"
	}
}

plan_execute() {
	local name="$1"
	shift
	local plan token result
	plan="$(api "$@" --dry-run --output json)"
	require_ok_json "$name dry-run" "$plan"
	token="$(printf '%s' "$plan" | json_token)"
	[[ -n "$token" ]] || fail "$name dry-run did not return a confirmation token"
	result="$(api "$@" --execute --confirm "$token" --output json)"
	require_ok_json "$name execute" "$result"
	printf '%s\n' "$result" >"$WORK_DIR/$name.json"
	pass "$name executed"
}

wait_for_checked_sites() {
	local deadline=$((SECONDS + 120))
	local checked=0
	while (( SECONDS < deadline )); do
		checked="$(sql --batch --skip-column-names -e "
			SELECT COUNT(*)
			  FROM jetpack_monitor_sites s
			  JOIN jetpack_monitor_site_runtime r ON r.blog_id = s.blog_id
			 WHERE s.monitor_active = 1
			   AND s.bucket_no BETWEEN $BUCKET_MIN AND $BUCKET_MAX
			   AND r.last_checked_at >= UTC_TIMESTAMP() - INTERVAL 2 MINUTE")"
		checked="${checked//[[:space:]]/}"
		if [[ "$checked" == "$SITE_COUNT" ]]; then
			pass "monitor_activity_checked_sites=$checked"
			return
		fi
		sleep 3
	done
	fail "only $checked/$SITE_COUNT sites checked after activation"
}

policy_count() {
	sql --batch --skip-column-names -e "
		SELECT COUNT(*)
		  FROM jetpack_monitor_sites s
		  JOIN jetpack_monitor_site_check_config c ON c.blog_id = s.blog_id
		 WHERE s.monitor_active = 1
		   AND s.bucket_no BETWEEN $BUCKET_MIN AND $BUCKET_MAX
		   AND c.request_method = '$1'
		   AND c.detection_profile = '$2'" | tr -d '[:space:]'
}

run_policy_stage() {
	local method="$1"
	local profile="$2"
	local label="$3"
	plan_execute "$label" rollout stage-policy \
		--bucket-min "$BUCKET_MIN" \
		--bucket-max "$BUCKET_MAX" \
		--run-id "$RUN_ID" \
		--change-ref "$CHANGE_REF" \
		--method "$method" \
		--profile "$profile" \
		--size 100%
	local count
	count="$(policy_count "$method" "$profile")"
	[[ "$count" == "$SITE_COUNT" ]] || fail "$label changed $count/$SITE_COUNT sites"
	pass "$label policy_count=$count"
}

run_lab() {
	need_cmd docker
	need_cmd jq
	cd "$REPO_ROOT"
	cleanup >/dev/null 2>&1 || true
	prepare_config
	prepare_fixture_sites
	ensure_public_network
	docker_dns_preflight

	log "starting compose project=$PROJECT"
	compose up -d --build
	wait_for_api
	create_api_token

	compose exec -T jetmon curl -fsS "http://$FIXTURE_IP:8091/health" >/dev/null
	pass "fixture_reachable_from_monitor=$FIXTURE_IP"

	seed_sites
	api rollout capabilities --output json | tee "$WORK_DIR/capabilities.json" >/dev/null
	api rollout preflight --bucket-min "$BUCKET_MIN" --bucket-max "$BUCKET_MAX" --run-id "$RUN_ID" --change-ref "$CHANGE_REF" --canary-file "$CANARY_FIXTURE_CONTAINER_FILE" --output json | tee "$WORK_DIR/preflight.json" >/dev/null
	api rollout smoke --bucket-min "$BUCKET_MIN" --bucket-max "$BUCKET_MAX" --run-id "$RUN_ID" --mode head-legacy --sample-size "$SITE_COUNT" --read-only --canary-file "$CANARY_FIXTURE_CONTAINER_FILE" --output json | tee "$WORK_DIR/smoke-head-legacy.json" >/dev/null
	plan_execute seed rollout seed --bucket-min "$BUCKET_MIN" --bucket-max "$BUCKET_MAX" --run-id "$RUN_ID" --change-ref "$CHANGE_REF"
	plan_execute final-reconcile rollout final-reconcile --bucket-min "$BUCKET_MIN" --bucket-max "$BUCKET_MAX" --run-id "$RUN_ID" --change-ref "$CHANGE_REF"
	plan_execute activate-buckets rollout activate-buckets --bucket-min "$BUCKET_MIN" --bucket-max "$BUCKET_MAX" --run-id "$RUN_ID" --change-ref "$CHANGE_REF"
	wait_for_checked_sites
	api rollout bucket-coverage --bucket-min "$BUCKET_MIN" --bucket-max "$BUCKET_MAX" --output json | tee "$WORK_DIR/bucket-coverage.json" >/dev/null
	api rollout activity-check --bucket-min "$BUCKET_MIN" --bucket-max "$BUCKET_MAX" --since 2m --output json | tee "$WORK_DIR/activity-check.json" >/dev/null
	api rollout projection-drift --bucket-min "$BUCKET_MIN" --bucket-max "$BUCKET_MAX" --output json | tee "$WORK_DIR/projection-drift.json" >/dev/null
	api rollout compare-methods --bucket-min "$BUCKET_MIN" --bucket-max "$BUCKET_MAX" --run-id "$RUN_ID" --from head-legacy --to get-simple --sample-size "$SITE_COUNT" --output json | tee "$WORK_DIR/compare-head-to-simple.json" >/dev/null
	run_policy_stage GET simple_http stage-get-simple
	api rollout compare-methods --bucket-min "$BUCKET_MIN" --bucket-max "$BUCKET_MAX" --run-id "$RUN_ID" --from get-simple --to get-full --sample-size "$SITE_COUNT" --output json | tee "$WORK_DIR/compare-simple-to-full.json" >/dev/null
	run_policy_stage GET full stage-get-full
	plan_execute release-buckets rollout release-buckets --bucket-min "$BUCKET_MIN" --bucket-max "$BUCKET_MAX" --run-id "$RUN_ID" --change-ref "$CHANGE_REF"
	api rollout status --output json | tee "$WORK_DIR/status-after-release.json" >/dev/null
	pass "rollout_docker_lab_complete project=$PROJECT run_id=$RUN_ID logs=$WORK_DIR"
}

doctor() {
	need_cmd docker
	need_cmd jq
	cd "$REPO_ROOT"
	ensure_public_network
	docker_dns_preflight
	pass "rollout_docker_lab_doctor_ok project=$PROJECT network=$PUBLIC_NETWORK"
}

case "${1:-run}" in
	run)
		run_lab
		;;
	doctor)
		doctor
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
