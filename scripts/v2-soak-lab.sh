#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"

PROJECT="${JETMON_SOAK_LAB_PROJECT:-jetmon-v2-soak-lab}"
PUBLIC_NETWORK="${JETMON_SOAK_LAB_NETWORK:-jetmon-v2-soak-lab-public}"
PUBLIC_SUBNET="${JETMON_SOAK_LAB_SUBNET:-93.184.218.0/24}"
FIXTURE_IP="${JETMON_SOAK_LAB_FIXTURE_IP:-93.184.218.20}"
SITE_COUNT="${JETMON_SOAK_LAB_SITE_COUNT:-360}"
BUCKET_TOTAL="${JETMON_SOAK_LAB_BUCKET_TOTAL:-12}"
DURATION_SEC="${JETMON_SOAK_LAB_DURATION_SEC:-1800}"
WARMUP_SEC="${JETMON_SOAK_LAB_WARMUP_SEC:-120}"
SAMPLE_INTERVAL_SEC="${JETMON_SOAK_LAB_SAMPLE_INTERVAL_SEC:-60}"
MAX_LAST_CHECKED_AGE_SEC="${JETMON_SOAK_LAB_MAX_LAST_CHECKED_AGE_SEC:-960}"
KEEP_RUNNING="${JETMON_SOAK_LAB_KEEP_RUNNING:-0}"
SKIP_BUILD="${JETMON_SOAK_LAB_SKIP_BUILD:-0}"
MODE_COHORTS="${JETMON_SOAK_LAB_MODE_COHORTS:-all}"
WORK_DIR="$REPO_ROOT/logs/v2-soak-lab"
CONFIG_FILE="$REPO_ROOT/config/config.json"
COMPOSE=(docker compose -p "$PROJECT" -f "$REPO_ROOT/docker/docker-compose.yml" -f "$REPO_ROOT/docker/docker-compose.scale-lab.yml")

export BIND_ADDR="${BIND_ADDR:-127.0.0.1}"
export API_BIND_ADDR="${API_BIND_ADDR:-127.0.0.1}"
export MYSQL_HOST_PORT="${MYSQL_HOST_PORT:-27307}"
export DASHBOARD_HOST_PORT="${DASHBOARD_HOST_PORT:-27080}"
export API_HOST_PORT="${API_HOST_PORT:-27090}"
export VERIFLIER_HOST_PORT="${VERIFLIER_HOST_PORT:-27813}"
export API_FIXTURE_HTTP_HOST_PORT="${API_FIXTURE_HTTP_HOST_PORT:-28191}"
export API_FIXTURE_HTTPS_HOST_PORT="${API_FIXTURE_HTTPS_HOST_PORT:-28543}"
export MAILPIT_HOST_PORT="${MAILPIT_HOST_PORT:-28125}"
export GRAPHITE_HOST_PORT="${GRAPHITE_HOST_PORT:-28188}"
export STATSD_HOST_PORT="${STATSD_HOST_PORT:-28225}"
export EMAIL_TRANSPORT=stub
export DELIVERY_OWNER_HOST="${DELIVERY_OWNER_HOST:-jetmon-scale-1}"
export JETMON_CONFIG_RENDER_MODE=never
export VERIFLIER_CONFIG_RENDER_MODE=always
export SCALE_LAB_PUBLIC_NETWORK="$PUBLIC_NETWORK"
export SCALE_LAB_FIXTURE_IP="$FIXTURE_IP"

usage() {
	cat <<'USAGE'
usage: scripts/v2-soak-lab.sh <run|cleanup>

Runs an isolated, internal-only v2 soak with four Monitors, three Verifliers,
and fixture-backed sites. WPCOM notifications are disabled, email uses the stub
transport, no alert contacts or webhooks are created, and target traffic stays
inside the Docker host.

Useful overrides:
  JETMON_SOAK_LAB_DURATION_SEC=1800
  JETMON_SOAK_LAB_SITE_COUNT=360
  JETMON_SOAK_LAB_KEEP_RUNNING=1
  JETMON_SOAK_LAB_SKIP_BUILD=1
  JETMON_SOAK_LAB_MODE_COHORTS=all
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

compose() {
	"${COMPOSE[@]}" "$@"
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
	pass "v2_soak_lab_cleaned project=$PROJECT network=$PUBLIC_NETWORK"
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
		'.AUTH_TOKEN = "v2-soak-lab-wpcom-disabled"
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
		| .NUM_WORKERS = 12
		| .DATASET_SIZE = 75
		| .NET_COMMS_TIMEOUT = 3
		| .BODY_READ_MAX_MS = 250
		| .BUCKET_TOTAL = $bucket_total
		| .BUCKET_TARGET = $bucket_total
		| .BUCKET_HEARTBEAT_GRACE_SEC = 180
		| .ALERT_COOLDOWN_MINUTES = 60
		| .STATS_UPDATE_INTERVAL_MS = 1000
		| .VERIFLIERS = [
			{"name":"Soak Veriflier 1","host":"veriflier","port":"7803","auth_token":$token},
			{"name":"Soak Veriflier 2","host":"veriflier2","port":"7803","auth_token":$token},
			{"name":"Soak Veriflier 3","host":"veriflier3","port":"7803","auth_token":$token}
		]' \
		"$REPO_ROOT/config/config-sample.json" >"$CONFIG_FILE"
	pass "safe_config_written=$CONFIG_FILE wpcom_notify=false email_transport=stub bucket_total=$BUCKET_TOTAL"
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
		((SECONDS < deadline)) || fail "API did not become healthy"
		sleep 2
	done
	pass "api_healthy"
}

seed_sites() {
	local created=0
	local sql_file="$WORK_DIR/seed.sql"
	local blog_id
	local bucket
	local url
	local profile
	local method
	local keyword

	case "$MODE_COHORTS" in
		all|get)
			;;
		*)
			fail "invalid JETMON_SOAK_LAB_MODE_COHORTS=$MODE_COHORTS (want all or get)"
			;;
	esac

	{
		printf 'START TRANSACTION;\n'
		printf 'DELETE FROM jetpack_monitor_site_runtime WHERE source_site_id IN (SELECT jetpack_monitor_site_id FROM jetpack_monitor_sites WHERE blog_id >= 920000000 AND blog_id < %d);\n' "$((920000000 + SITE_COUNT))"
		printf 'DELETE FROM jetpack_monitor_site_check_config WHERE source_site_id IN (SELECT jetpack_monitor_site_id FROM jetpack_monitor_sites WHERE blog_id >= 920000000 AND blog_id < %d);\n' "$((920000000 + SITE_COUNT))"
		printf 'DELETE FROM jetpack_monitor_sites WHERE blog_id >= 920000000 AND blog_id < %d;\n' "$((920000000 + SITE_COUNT))"
		while ((created < SITE_COUNT)); do
			blog_id=$((920000000 + created))
			bucket=$((created % BUCKET_TOTAL))
			case $((created % 4)) in
				0)
					url="http://$FIXTURE_IP:8091/slow?delay=120ms"
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
			method="GET"
			if [[ "$MODE_COHORTS" == "all" ]]; then
				case $((created % 3)) in
					0)
						method="HEAD"
						profile="legacy"
						keyword="NULL"
						;;
					1)
						profile="simple_http"
						;;
					*)
						profile="full"
						;;
				esac
			fi
			printf "INSERT INTO jetpack_monitor_sites (blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval) VALUES (%d, %d, '%s', 1, 1, 1);\n" "$blog_id" "$bucket" "$url"
			printf "SET @source_site_id = LAST_INSERT_ID();\n"
			printf "INSERT INTO jetpack_monitor_site_check_config (source_site_id, blog_id, request_method, detection_profile, check_keyword, timeout_seconds, redirect_policy, alert_cooldown_minutes) VALUES (@source_site_id, %d, '%s', '%s', %s, 3, 'follow', 60);\n" "$blog_id" "$method" "$profile" "$keyword"
			created=$((created + 1))
		done
		printf 'COMMIT;\n'
	} >"$sql_file"

	sql <"$sql_file"
	pass "sites_seeded count=$SITE_COUNT bucket_total=$BUCKET_TOTAL mode_cohorts=$MODE_COHORTS"
}

checkpoint_utc() {
	sql --batch --skip-column-names -e "SELECT UTC_TIMESTAMP(6)" | tr -d '\r'
}

checkpoint_rfc3339() {
	date -u +%Y-%m-%dT%H:%M:%SZ
}

scalar_sql() {
	sql --batch --skip-column-names -e "$1" | tr -d '[:space:]'
}

wait_for_host_count() {
	local expected="$1"
	local label="$2"
	local deadline=$((SECONDS + 180))
	local count=0
	while ((SECONDS < deadline)); do
		count="$(scalar_sql "SELECT COUNT(*) FROM jetpack_monitor_hosts WHERE status = 'active'")"
		if [[ "$count" == "$expected" ]]; then
			pass "$label active_monitor_hosts=$count"
			return
		fi
		sleep 3
	done
	fail "$label active_monitor_hosts=$count want=$expected"
}

wait_for_dynamic_coverage() {
	local label="$1"
	local deadline=$((SECONDS + 180))
	while ((SECONDS < deadline)); do
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

completed_checks_since() {
	local since="$1"
	compose logs --no-color --since "$since" jetmon jetmon2 jetmon3 jetmon4 |
		awk 'match($0, /completed=([0-9]+)/, m) { sum += m[1] } END { print sum + 0 }'
}

wait_for_completed_since() {
	local label="$1"
	local since="$2"
	local deadline=$((SECONDS + 180))
	local checked=0
	while ((SECONDS < deadline)); do
		checked="$(completed_checks_since "$since")"
		if ((checked >= SITE_COUNT)); then
			pass "$label completed_checks_since_checkpoint=$checked"
			return
		fi
		sleep 3
	done
	fail "$label only $checked/$SITE_COUNT completed checks since $since"
}

capture_fleet_snapshot() {
	local label="$1"
	local snapshot="$WORK_DIR/fleet-$label.json"
	local summary
	local bucket
	local verifliers
	local fresh_agents

	compose exec -T jetmon curl -fsS http://127.0.0.1:8080/api/fleet >"$snapshot"
	summary="$(jq -r '.summary.status' "$snapshot")"
	bucket="$(jq -r '.bucket_coverage.status' "$snapshot")"
	verifliers="$(jq -r '.verifliers.status' "$snapshot")"
	fresh_agents="$(jq -r '.verifliers.fresh_agents' "$snapshot")"

	if [[ "$summary" != "green" || "$bucket" != "green" || "$verifliers" != "green" || "$fresh_agents" != "3" ]]; then
		fail "$label fleet_not_green summary=$summary bucket=$bucket verifliers=$verifliers fresh_agents=$fresh_agents snapshot=$snapshot"
	fi
	pass "$label fleet_snapshot=$snapshot summary=$summary bucket=$bucket verifliers=$verifliers fresh_agents=$fresh_agents"
}

mailpit_message_count() {
	curl -fsS "http://127.0.0.1:$MAILPIT_HOST_PORT/api/v1/messages" | jq -r '.total'
}

assert_no_outbound_side_effects() {
	local label="$1"
	local wpcom_audit
	local alert_contacts
	local webhooks
	local mailpit_messages

	wpcom_audit="$(scalar_sql "SELECT COUNT(*) FROM jetpack_monitor_audit_log WHERE event_type IN ('wpcom_sent','wpcom_retry','wpcom_failure')")"
	alert_contacts="$(scalar_sql "SELECT COUNT(*) FROM jetpack_monitor_alert_contacts")"
	webhooks="$(scalar_sql "SELECT COUNT(*) FROM jetpack_monitor_webhooks")"
	mailpit_messages="$(mailpit_message_count)"

	[[ "$wpcom_audit" == "0" ]] || fail "$label wpcom_audit_rows=$wpcom_audit want=0"
	[[ "$alert_contacts" == "0" ]] || fail "$label alert_contacts=$alert_contacts want=0"
	[[ "$webhooks" == "0" ]] || fail "$label webhooks=$webhooks want=0"
	[[ "$mailpit_messages" == "0" ]] || fail "$label mailpit_messages=$mailpit_messages want=0"
	pass "$label no_wpcom_no_alert_side_effects"
}

sample_soak() {
	local sample_no="$1"
	local sql_since="$2"
	local log_since="$3"
	local checked
	local max_age
	local history_rows
	local open_events
	local active_hosts
	local stale_processes

	checked="$(completed_checks_since "$log_since")"
	max_age="$(scalar_sql "
		SELECT COALESCE(MAX(TIMESTAMPDIFF(SECOND, r.last_checked_at, UTC_TIMESTAMP())), 999999)
		  FROM jetpack_monitor_sites s
		  JOIN jetpack_monitor_site_runtime r ON r.source_site_id = s.jetpack_monitor_site_id
		 WHERE s.monitor_active = 1")"
	history_rows="$(scalar_sql "SELECT COUNT(*) FROM jetpack_monitor_check_history WHERE checked_at >= TIMESTAMP('$sql_since')")"
	open_events="$(scalar_sql "SELECT COUNT(*) FROM jetpack_monitor_events WHERE ended_at IS NULL")"
	active_hosts="$(scalar_sql "SELECT COUNT(*) FROM jetpack_monitor_hosts WHERE status = 'active'")"
	stale_processes="$(scalar_sql "
		SELECT COUNT(*)
		  FROM jetpack_monitor_process_health
		 WHERE process_type = 'monitor'
		   AND state = 'running'
		   AND updated_at < UTC_TIMESTAMP() - INTERVAL 20 SECOND")"

	[[ "$active_hosts" == "4" ]] || fail "sample=$sample_no active_monitor_hosts=$active_hosts want=4"
	((checked >= SITE_COUNT)) || fail "sample=$sample_no completed_checks=$checked want_at_least=$SITE_COUNT since=$log_since"
	((max_age <= MAX_LAST_CHECKED_AGE_SEC)) || fail "sample=$sample_no max_legacy_projection_age_sec=$max_age limit=$MAX_LAST_CHECKED_AGE_SEC"
	[[ "$open_events" == "0" ]] || fail "sample=$sample_no open_events=$open_events want=0"
	[[ "$stale_processes" == "0" ]] || fail "sample=$sample_no stale_monitor_processes=$stale_processes want=0"

	capture_fleet_snapshot "sample-$sample_no"
	assert_no_outbound_side_effects "sample-$sample_no"
	pass "sample=$sample_no completed_checks=$checked history_rows=$history_rows max_legacy_projection_age_sec=$max_age"
}

run_lab() {
	need_cmd curl
	need_cmd docker
	need_cmd jq
	cd "$REPO_ROOT"
	cleanup >/dev/null 2>&1 || true
	prepare_config
	ensure_public_network

	if [[ "$SKIP_BUILD" == "1" ]]; then
		log "using prebuilt lab images"
	else
		log "building lab images"
		compose build api-fixture jetmon jetmon2 jetmon3 jetmon4 veriflier veriflier2 veriflier3
	fi

	log "starting soak services project=$PROJECT duration_sec=$DURATION_SEC sites=$SITE_COUNT"
	if [[ "$SKIP_BUILD" == "1" ]]; then
		compose up -d --no-build mysqldb mysql-user mailpit statsd api-fixture veriflier veriflier2 veriflier3 jetmon jetmon2 jetmon3 jetmon4
	else
		compose up -d mysqldb mysql-user mailpit statsd api-fixture veriflier veriflier2 veriflier3 jetmon jetmon2 jetmon3 jetmon4
	fi
	wait_for_api
	compose exec -T jetmon curl -fsS "http://$FIXTURE_IP:8091/health" >/dev/null
	pass "fixture_reachable_from_monitor=$FIXTURE_IP"
	seed_sites

	wait_for_host_count 4 startup
	wait_for_dynamic_coverage startup
	capture_fleet_snapshot startup

	log "warming up duration_sec=$WARMUP_SEC"
	warmup_since="$(checkpoint_rfc3339)"
	sleep "$WARMUP_SEC"
	wait_for_completed_since warmup "$warmup_since"
	assert_no_outbound_side_effects warmup

	local sample_no=0
	local sample_sql_since
	local sample_log_since
	local remaining
	local started_at
	started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	sample_sql_since="$(checkpoint_utc)"
	sample_log_since="$(checkpoint_rfc3339)"
	local deadline=$((SECONDS + DURATION_SEC))
	while ((SECONDS < deadline)); do
		remaining=$((deadline - SECONDS))
		if ((remaining < SAMPLE_INTERVAL_SEC)); then
			log "finishing soak tail duration_sec=$remaining without full-window sample"
			sleep "$remaining"
			break
		fi
		sleep "$SAMPLE_INTERVAL_SEC"
		sample_no=$((sample_no + 1))
		sample_soak "$sample_no" "$sample_sql_since" "$sample_log_since"
		sample_sql_since="$(checkpoint_utc)"
		sample_log_since="$(checkpoint_rfc3339)"
	done

	assert_no_outbound_side_effects final
	pass "v2_soak_lab_complete project=$PROJECT sites=$SITE_COUNT monitors=4 verifliers=3 duration_sec=$DURATION_SEC samples=$sample_no started_at=$started_at logs=$WORK_DIR"

	if [[ "$KEEP_RUNNING" == "1" ]]; then
		warn "soak lab left running because JETMON_SOAK_LAB_KEEP_RUNNING=1"
	else
		cleanup
	fi
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
