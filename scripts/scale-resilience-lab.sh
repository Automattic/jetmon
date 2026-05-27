#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"

export JETMON_BUILD_VERSION="${JETMON_BUILD_VERSION:-$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || printf dev)}"
export JETMON_BUILD_COMMIT="${JETMON_BUILD_COMMIT:-$(git -C "$REPO_ROOT" rev-parse HEAD 2>/dev/null || printf unknown)}"
export JETMON_BUILD_DATE="${JETMON_BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

PROJECT="${JETMON_SCALE_LAB_PROJECT:-jetmon-scale-lab}"
PUBLIC_NETWORK="${JETMON_SCALE_LAB_NETWORK:-jetmon-scale-lab-public}"
PUBLIC_SUBNET="${JETMON_SCALE_LAB_SUBNET:-93.184.217.0/24}"
FIXTURE_IP="${JETMON_SCALE_LAB_FIXTURE_IP:-93.184.217.20}"
SITE_COUNT="${JETMON_SCALE_LAB_SITE_COUNT:-600}"
BUCKET_TOTAL="${JETMON_SCALE_LAB_BUCKET_TOTAL:-12}"
DB_RUNTIME_LOCK_SEC="${JETMON_SCALE_LAB_DB_RUNTIME_LOCK_SEC:-10}"
DB_READ_ONLY_SEC="${JETMON_SCALE_LAB_DB_READ_ONLY_SEC:-10}"
DB_PAUSE_SEC="${JETMON_SCALE_LAB_DB_PAUSE_SEC:-10}"
WORK_DIR="$REPO_ROOT/logs/scale-resilience-lab"
CONFIG_FILE="$REPO_ROOT/config/config.json"
COMPOSE=(docker compose -p "$PROJECT" -f "$REPO_ROOT/docker/docker-compose.yml" -f "$REPO_ROOT/docker/docker-compose.scale-lab.yml")

export BIND_ADDR="${BIND_ADDR:-127.0.0.1}"
export API_BIND_ADDR="${API_BIND_ADDR:-127.0.0.1}"
export MYSQL_HOST_PORT="${MYSQL_HOST_PORT:-17307}"
export DASHBOARD_HOST_PORT="${DASHBOARD_HOST_PORT:-17080}"
export API_HOST_PORT="${API_HOST_PORT:-17090}"
export VERIFLIER_HOST_PORT="${VERIFLIER_HOST_PORT:-17813}"
export API_FIXTURE_HTTP_HOST_PORT="${API_FIXTURE_HTTP_HOST_PORT:-18191}"
export API_FIXTURE_HTTPS_HOST_PORT="${API_FIXTURE_HTTPS_HOST_PORT:-18543}"
export MAILPIT_HOST_PORT="${MAILPIT_HOST_PORT:-17125}"
export GRAPHITE_HOST_PORT="${GRAPHITE_HOST_PORT:-18188}"
export STATSD_HOST_PORT="${STATSD_HOST_PORT:-18225}"
export EMAIL_TRANSPORT=stub
export WPCOM_NOTIFY_ENABLE=false
export DELIVERY_OWNER_HOST="${DELIVERY_OWNER_HOST:-jetmon-scale-1}"
export JETMON_CONFIG_RENDER_MODE=never
export VERIFLIER_CONFIG_RENDER_MODE=always
export SCALE_LAB_PUBLIC_NETWORK="$PUBLIC_NETWORK"
export SCALE_LAB_FIXTURE_IP="$FIXTURE_IP"

usage() {
	cat <<'USAGE'
usage: scripts/scale-resilience-lab.sh <run|cleanup>

Runs an isolated Docker resilience lab that:
  1. starts one Monitor and three Verifliers
  2. seeds many public-looking Docker-internal fixture sites
  3. validates dynamic bucket coverage and recent site activity
  4. adds Monitors to two and then four active owners
  5. stops and hard-kills Monitors and verifies surviving owners take over
  6. injects isolated database disruption and verifies recovery
  7. stops Verifliers and verifies telemetry/dashboard visibility

No WPCOM calls or alert deliveries are configured.

Useful overrides:
  JETMON_SCALE_LAB_SITE_COUNT=600
  JETMON_SCALE_LAB_DB_RUNTIME_LOCK_SEC=10
  JETMON_SCALE_LAB_DB_READ_ONLY_SEC=10
  JETMON_SCALE_LAB_DB_PAUSE_SEC=10
USAGE
}

log() {
	printf 'INFO %s\n' "$*"
}

pass() {
	printf 'PASS %s\n' "$*"
}

warn() {
	printf 'WARN %s\n' "$*" >&2
}

fail() {
	printf 'FAIL %s\n' "$*" >&2
	exit 1
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"
}

require_non_negative_int() {
	local name="$1"
	local value="$2"
	[[ "$value" =~ ^[0-9]+$ ]] || fail "$name must be a non-negative integer, got $value"
}

compose() {
	"${COMPOSE[@]}" "$@"
}

sql() {
	compose exec -T mysqldb mariadb \
		-u"${MYSQL_USER:-jetmon}" "-p${MYSQL_PASSWORD:-jetmon_dev_password}" \
		"${MYSQL_DATABASE:-jetmon_db}" "$@"
}

root_sql() {
	compose exec -T mysqldb mariadb \
		-u root "-p${MYSQL_ROOT_PASSWORD:-123456}" "$@"
}

api() {
	compose exec -T \
		-e JETMON_API_URL=http://127.0.0.1:8090 \
		-e JETMON_API_TOKEN="$API_TOKEN" \
		jetmon ./jetmon2 api "$@"
}

cleanup() {
	cd "$REPO_ROOT"
	compose down -v --remove-orphans >/dev/null 2>&1 || true
	docker network rm "$PUBLIC_NETWORK" >/dev/null 2>&1 || true
	pass "scale_resilience_lab_cleaned project=$PROJECT network=$PUBLIC_NETWORK"
}

prepare_config() {
	mkdir -p "$WORK_DIR" "$REPO_ROOT/logs" "$REPO_ROOT/stats"
	rm -f "$REPO_ROOT/veriflier2/config/veriflier.json"
	jq \
		--argjson bucket_total "$BUCKET_TOTAL" \
		--arg db_user "${MYSQL_USER:-jetmon}" \
		--arg db_password "${MYSQL_PASSWORD:-jetmon_dev_password}" \
		--arg db_name "${MYSQL_DATABASE:-jetmon_db}" \
		--arg token "${VERIFLIER_AUTH_TOKEN:-veriflier_1_auth_token}" \
		'.AUTH_TOKEN = "scale-lab-wpcom-disabled"
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
		| .DELIVERY_OWNER_HOST = "jetmon-scale-1"
		| .DEBUG_PORT = 0
		| .ROLLOUT_MODE = "active"
		| .DEFAULT_CHECK_METHOD = "GET"
		| .DEFAULT_DETECTION_PROFILE = "simple_http"
		| .PEER_OFFLINE_LIMIT = 1
		| .NUM_WORKERS = 24
		| .DATASET_SIZE = 100
		| .NET_COMMS_TIMEOUT = 3
		| .BODY_READ_MAX_MS = 250
		| .BUCKET_TOTAL = $bucket_total
		| .BUCKET_TARGET = $bucket_total
		| .BUCKET_HEARTBEAT_GRACE_SEC = 75
		| .STATS_UPDATE_INTERVAL_MS = 1000
		| .VERIFLIERS = [
			{"name":"Scale Veriflier 1","host":"veriflier","port":"7803","auth_token":$token},
			{"name":"Scale Veriflier 2","host":"veriflier2","port":"7803","auth_token":$token},
			{"name":"Scale Veriflier 3","host":"veriflier3","port":"7803","auth_token":$token}
		]' \
		"$REPO_ROOT/config/config-sample.json" >"$CONFIG_FILE"
	pass "safe_config_written=$CONFIG_FILE wpcom_notify=false email_transport=stub bucket_total=$BUCKET_TOTAL"
}

prepare_fixture_sites() {
	cat >"$WORK_DIR/sites.json" <<JSON
[
  {
    "monitor_url": "http://$FIXTURE_IP:8091/slow?delay=150ms",
    "redirect_policy": "follow",
    "request_method": "GET",
    "detection_profile": "simple_http",
    "timeout_seconds": 3,
    "check_interval": 1,
    "alert_cooldown_minutes": 0
  },
  {
    "monitor_url": "http://$FIXTURE_IP:8091/ok",
    "redirect_policy": "follow",
    "request_method": "GET",
    "detection_profile": "simple_http",
    "timeout_seconds": 3,
    "check_interval": 1,
    "alert_cooldown_minutes": 0
  },
  {
    "monitor_url": "http://$FIXTURE_IP:8091/keyword",
    "check_keyword": "keyword present",
    "redirect_policy": "follow",
    "request_method": "GET",
    "detection_profile": "full",
    "timeout_seconds": 3,
    "check_interval": 1,
    "alert_cooldown_minutes": 0
  },
  {
    "monitor_url": "http://$FIXTURE_IP:8091/redirect",
    "redirect_policy": "follow",
    "request_method": "GET",
    "detection_profile": "simple_http",
    "timeout_seconds": 3,
    "check_interval": 1,
    "alert_cooldown_minutes": 0
  }
]
JSON
	pass "fixture_sites_written=$WORK_DIR/sites.json count=$SITE_COUNT fixture=$FIXTURE_IP"
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
	out="$(compose exec -T jetmon ./jetmon2 keys create --consumer scale-resilience-lab --scope admin --created-by scale-resilience-lab)"
	API_TOKEN="$(printf '%s\n' "$out" | awk '/^jm_/ {print; exit}')"
	[[ -n "$API_TOKEN" ]] || fail "could not parse API token"
	pass "api_token_created"
}

seed_sites() {
	local created=0
	local sql_file="$WORK_DIR/seed.sql"
	local blog_id
	local bucket
	local url
	local profile
	local keyword

	{
		printf 'START TRANSACTION;\n'
		printf 'DELETE FROM jetpack_monitor_site_runtime WHERE source_site_id IN (SELECT jetpack_monitor_site_id FROM jetpack_monitor_sites WHERE blog_id >= 910000000 AND blog_id < %d);\n' "$(( 910000000 + SITE_COUNT ))"
		printf 'DELETE FROM jetpack_monitor_site_check_config WHERE source_site_id IN (SELECT jetpack_monitor_site_id FROM jetpack_monitor_sites WHERE blog_id >= 910000000 AND blog_id < %d);\n' "$(( 910000000 + SITE_COUNT ))"
		printf 'DELETE FROM jetpack_monitor_sites WHERE blog_id >= 910000000 AND blog_id < %d;\n' "$(( 910000000 + SITE_COUNT ))"
		while (( created < SITE_COUNT )); do
			blog_id=$(( 910000000 + created ))
			bucket=$(( created % BUCKET_TOTAL ))
			case $(( created % 4 )) in
				0)
					url="http://$FIXTURE_IP:8091/slow?delay=150ms"
					profile="simple_http"
					keyword="NULL"
					;;
				1)
					url="http://$FIXTURE_IP:8091/ok"
					profile="simple_http"
					keyword="NULL"
					;;
				2)
					url="http://$FIXTURE_IP:8091/keyword"
					profile="full"
					keyword="'keyword present'"
					;;
				*)
					url="http://$FIXTURE_IP:8091/redirect"
					profile="simple_http"
					keyword="NULL"
					;;
			esac
			printf "INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval) VALUES (%d, %d, '%s', 1, 1, 1);\n" "$blog_id" "$bucket" "$url"
			printf "SET @source_site_id = LAST_INSERT_ID();\n"
			printf "INSERT INTO jetpack_monitor_site_check_config (source_site_id, blog_id, request_method, detection_profile, check_keyword, timeout_seconds, redirect_policy, alert_cooldown_minutes) VALUES (@source_site_id, %d, 'GET', '%s', %s, 3, 'follow', 0);\n" "$blog_id" "$profile" "$keyword"
			created=$(( created + 1 ))
		done
		printf 'COMMIT;\n'
	} >"$sql_file"

	sql <"$sql_file"
	pass "sites_seeded count=$SITE_COUNT bucket_total=$BUCKET_TOTAL"
}

checkpoint_utc() {
	sql --batch --skip-column-names -e "SELECT UTC_TIMESTAMP(6)" | tr -d '\r'
}

scalar_sql() {
	sql --batch --skip-column-names -e "$1" | tr -d '[:space:]'
}

wait_for_sql() {
	local label="$1"
	local deadline=$((SECONDS + 120))
	while (( SECONDS < deadline )); do
		if root_sql --batch --skip-column-names -e "SELECT 1" >/dev/null 2>&1; then
			pass "$label db_available"
			return
		fi
		sleep 2
	done
	fail "$label database did not become available"
}

wait_for_host_count() {
	local expected="$1"
	local label="$2"
	local deadline=$((SECONDS + 120))
	local count=0
	while (( SECONDS < deadline )); do
		count="$(scalar_sql "SELECT COUNT(*) FROM jetpack_monitor_hosts WHERE status = 'active'")"
		if [[ "$count" == "$expected" ]]; then
			pass "$label active_monitor_hosts=$count"
			sql -e "SELECT host_id, bucket_min, bucket_max, status, last_heartbeat FROM jetpack_monitor_hosts ORDER BY bucket_min, host_id"
			return
		fi
		sleep 3
	done
	fail "$label active_monitor_hosts=$count want=$expected"
}

wait_for_dynamic_coverage() {
	local label="$1"
	local deadline=$((SECONDS + 120))
	while (( SECONDS < deadline )); do
		if compose exec -T jetmon ./jetmon2 rollout dynamic-check --output json >"$WORK_DIR/dynamic-$label.json" 2>/dev/null &&
			jq -e '.ok == true' "$WORK_DIR/dynamic-$label.json" >/dev/null; then
			pass "$label dynamic_bucket_coverage"
			return
		fi
		sleep 3
	done
	cat "$WORK_DIR/dynamic-$label.json" >&2 || true
	fail "$label dynamic bucket coverage did not become healthy"
}

run_dynamic_check() {
	local label="$1"
	wait_for_dynamic_coverage "$label"
}

wait_for_checked_since() {
	local label="$1"
	local since="$2"
	local deadline=$((SECONDS + 150))
	local checked=0
	while (( SECONDS < deadline )); do
		checked="$(scalar_sql "
			SELECT COUNT(DISTINCT s.jetpack_monitor_site_id)
			  FROM jetpack_monitor_sites s
			  JOIN jetpack_monitor_site_runtime r ON r.source_site_id = s.jetpack_monitor_site_id
			 WHERE s.monitor_active = 1
			   AND r.last_checked_at >= TIMESTAMP('$since')")"
		if [[ "$checked" == "$SITE_COUNT" ]]; then
			pass "$label checked_sites_since_checkpoint=$checked"
			return
		fi
		sleep 3
	done
	fail "$label only $checked/$SITE_COUNT sites checked since $since"
}

wait_for_worker_scale() {
	local min_workers="$1"
	local deadline=$((SECONDS + 90))
	local workers=0
	while (( SECONDS < deadline )); do
		workers="$(scalar_sql "SELECT COALESCE(MAX(worker_count), 0) FROM jetpack_monitor_process_health WHERE process_type = 'monitor' AND state = 'running'")"
		if (( workers >= min_workers )); then
			pass "vertical_autoscale_observed max_worker_count=$workers"
			return
		fi
		sleep 2
	done
	warn "vertical_autoscale_not_observed max_worker_count=$workers min_expected=$min_workers"
}

fresh_veriflier_agents() {
	scalar_sql "SELECT COUNT(*) FROM jetpack_monitor_veriflier_agents WHERE last_seen >= UTC_TIMESTAMP() - INTERVAL 15 SECOND"
}

wait_for_fresh_veriflier_agents() {
	local expected="$1"
	local label="$2"
	local deadline=$((SECONDS + 90))
	local count=0
	while (( SECONDS < deadline )); do
		count="$(fresh_veriflier_agents)"
		if [[ "$count" == "$expected" ]]; then
			pass "$label fresh_veriflier_agents=$count"
			return
		fi
		sleep 3
	done
	fail "$label fresh_veriflier_agents=$count want=$expected"
}

capture_fleet_snapshot() {
	local label="$1"
	local expected_summary="${2:-any}"
	local expected_bucket="${3:-green}"
	local expected_verifliers="${4:-green}"
	local expected_fresh_agents="${5:-3}"
	local snapshot="$WORK_DIR/fleet-$label.json"
	local summary
	local bucket
	local verifliers
	local fresh_agents
	local monitor_processes

	compose exec -T jetmon curl -fsS http://127.0.0.1:8080/api/fleet >"$snapshot"
	summary="$(jq -r '.summary.status' "$snapshot")"
	bucket="$(jq -r '.bucket_coverage.status' "$snapshot")"
	verifliers="$(jq -r '.verifliers.status' "$snapshot")"
	fresh_agents="$(jq -r '.verifliers.fresh_agents' "$snapshot")"
	monitor_processes="$(jq -r '.summary.monitor_processes' "$snapshot")"

	if [[ "$expected_bucket" != "any" && "$bucket" != "$expected_bucket" ]]; then
		fail "$label bucket_status=$bucket want=$expected_bucket snapshot=$snapshot"
	fi
	if [[ "$expected_verifliers" != "any" && "$verifliers" != "$expected_verifliers" ]]; then
		fail "$label veriflier_status=$verifliers want=$expected_verifliers snapshot=$snapshot"
	fi
	if [[ "$expected_fresh_agents" != "any" && "$fresh_agents" != "$expected_fresh_agents" ]]; then
		fail "$label fresh_veriflier_agents=$fresh_agents want=$expected_fresh_agents snapshot=$snapshot"
	fi
	case "$expected_summary" in
		any)
			;;
		stable)
			if [[ "$summary" != "green" ]]; then
				fail "$label summary=$summary want=green snapshot=$snapshot"
			fi
			;;
		degraded)
			if [[ "$summary" != "red" && "$summary" != "amber" ]]; then
				fail "$label summary=$summary want=red_or_amber snapshot=$snapshot"
			fi
			;;
		stable_or_degraded)
			if [[ "$summary" != "green" && "$summary" != "red" && "$summary" != "amber" ]]; then
				fail "$label summary=$summary want=green_or_red_or_amber snapshot=$snapshot"
			fi
			;;
		green | amber | red)
			if [[ "$summary" != "$expected_summary" ]]; then
				fail "$label summary=$summary want=$expected_summary snapshot=$snapshot"
			fi
			;;
		*)
			fail "$label unknown expected summary class: $expected_summary"
			;;
	esac

	jq -r '"summary=\(.summary.status) monitors=\(.summary.monitor_processes) bucket=\(.bucket_coverage.status) verifliers=\(.verifliers.status) fresh_agents=\(.verifliers.fresh_agents)"' "$snapshot"
	pass "$label fleet_snapshot=$snapshot expected_summary=$expected_summary monitor_processes=$monitor_processes"
}

validate_activity_step() {
	local label="$1"
	local expected_hosts="$2"
	local expected_summary="${3:-stable}"
	local expected_bucket="${4:-green}"
	local expected_verifliers="${5:-green}"
	local expected_fresh_agents="${6:-3}"
	local since
	wait_for_host_count "$expected_hosts" "$label"
	run_dynamic_check "$label"
	since="$(checkpoint_utc)"
	wait_for_checked_since "$label" "$since"
	capture_fleet_snapshot "$label" "$expected_summary" "$expected_bucket" "$expected_verifliers" "$expected_fresh_agents"
}

hard_kill_monitor() {
	local service="$1"
	local cid
	cid="$(compose ps -q "$service")"
	[[ -n "$cid" ]] || fail "could not find container for $service"
	docker update --restart=no "$cid" >/dev/null
	docker kill --signal=SIGKILL "$cid" >/dev/null
	pass "$service hard_killed"
}

recover_monitor() {
	local service="$1"
	compose up -d --build --force-recreate "$service" >/dev/null
	pass "$service recovered"
}

inject_db_runtime_lock() {
	local label="db-runtime-lock"
	log "injecting temporary database table lock duration_sec=$DB_RUNTIME_LOCK_SEC"
	(root_sql -e "LOCK TABLES jetpack_monitor_site_runtime WRITE; DO SLEEP($DB_RUNTIME_LOCK_SEC); UNLOCK TABLES;" "${MYSQL_DATABASE:-jetmon_db}" >/dev/null) &
	local lock_pid=$!
	sleep 2
	capture_fleet_snapshot "$label-during" "any" "any" "any" "any"
	if ! wait "$lock_pid"; then
		fail "$label lock session failed"
	fi
	pass "$label released"
	validate_activity_step "$label-recovery" 4 stable green green 3
}

inject_db_read_only() {
	local label="db-read-only"
	log "enabling temporary database read-only mode duration_sec=$DB_READ_ONLY_SEC"
	root_sql -e "SET GLOBAL read_only = ON"
	sleep "$DB_READ_ONLY_SEC"
	root_sql -e "SET GLOBAL read_only = OFF"
	pass "$label disabled"
	validate_activity_step "$label-recovery" 4 stable green green 3
}

inject_db_pause() {
	local label="db-pause"
	log "pausing database container duration_sec=$DB_PAUSE_SEC"
	compose pause mysqldb >/dev/null
	sleep "$DB_PAUSE_SEC"
	compose unpause mysqldb >/dev/null
	wait_for_sql "$label"
	validate_activity_step "$label-recovery" 4 stable green green 3
}

inject_db_restart() {
	local label="db-restart"
	log "restarting database container"
	compose restart mysqldb >/dev/null
	wait_for_sql "$label"
	validate_activity_step "$label-recovery" 4 stable green green 3
}

run_lab() {
	need_cmd docker
	need_cmd jq
	require_non_negative_int JETMON_SCALE_LAB_DB_RUNTIME_LOCK_SEC "$DB_RUNTIME_LOCK_SEC"
	require_non_negative_int JETMON_SCALE_LAB_DB_READ_ONLY_SEC "$DB_READ_ONLY_SEC"
	require_non_negative_int JETMON_SCALE_LAB_DB_PAUSE_SEC "$DB_PAUSE_SEC"
	cd "$REPO_ROOT"
	cleanup >/dev/null 2>&1 || true
	prepare_config
	prepare_fixture_sites
	ensure_public_network

	log "building lab images"
	compose build api-fixture jetmon jetmon2 jetmon3 jetmon4 veriflier veriflier2 veriflier3

	log "starting base services project=$PROJECT"
	compose up -d mysqldb mysql-user mailpit statsd api-fixture veriflier veriflier2 veriflier3 jetmon
	wait_for_api
	create_api_token
	compose exec -T jetmon curl -fsS "http://$FIXTURE_IP:8091/health" >/dev/null
	pass "fixture_reachable_from_monitor=$FIXTURE_IP"
	seed_sites

	validate_activity_step single-monitor 1 stable green green 3
	wait_for_worker_scale 13
	wait_for_fresh_veriflier_agents 3 verifliers-initial

	log "adding second Monitor"
	compose up -d --build jetmon2
	validate_activity_step two-monitors 2 stable green green 3

	log "adding two more Monitors"
	compose up -d --build jetmon3 jetmon4
	validate_activity_step four-monitors 4 stable green green 3

	log "stopping one Monitor"
	compose stop jetmon2 >/dev/null
	validate_activity_step three-monitors-after-graceful-stop 3 stable_or_degraded green green 3

	log "stopping another Monitor"
	compose stop jetmon3 >/dev/null
	validate_activity_step two-monitors-after-graceful-stop 2 stable_or_degraded green green 3

	log "recovering stopped Monitors"
	compose up -d --build jetmon2 jetmon3
	validate_activity_step four-monitors-recovered 4 stable green green 3

	log "hard-killing one Monitor"
	hard_kill_monitor jetmon2
	validate_activity_step three-monitors-after-hard-kill 3 degraded green green 3

	log "recovering hard-killed Monitor"
	recover_monitor jetmon2
	validate_activity_step four-monitors-after-hard-kill-recovery 4 stable green green 3

	inject_db_runtime_lock
	inject_db_read_only
	inject_db_pause
	inject_db_restart

	log "stopping one Veriflier"
	compose stop veriflier3 >/dev/null
	wait_for_fresh_veriflier_agents 2 veriflier-one-failed
	capture_fleet_snapshot veriflier-one-failed degraded green amber 2

	log "stopping second Veriflier"
	compose stop veriflier2 >/dev/null
	wait_for_fresh_veriflier_agents 1 veriflier-two-failed
	capture_fleet_snapshot veriflier-two-failed degraded green amber 1
	since="$(checkpoint_utc)"
	wait_for_checked_since veriflier-degraded-monitor-activity "$since"

	log "recovering Verifliers"
	compose up -d veriflier2 veriflier3
	wait_for_fresh_veriflier_agents 3 verifliers-recovered
	capture_fleet_snapshot verifliers-recovered stable green green 3

	pass "scale_resilience_lab_complete project=$PROJECT sites=$SITE_COUNT bucket_total=$BUCKET_TOTAL logs=$WORK_DIR"
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
