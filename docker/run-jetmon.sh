#!/usr/bin/env bash
set -euo pipefail

cd /jetmon

sed_escape() {
	printf '%s' "$1" | sed -e 's/[\\&|]/\\&/g'
}

bool_json() {
	local key=$1
	local value=$2
	case "${value,,}" in
		1|t|true|y|yes|on|enabled)
			printf 'true'
			;;
		0|f|false|n|no|off|disabled)
			printf 'false'
			;;
		*)
			echo "invalid ${key} value: ${value}" >&2
			exit 1
			;;
	esac
}

config_profile_render_value() {
	local value="${CONFIG_PROFILE:-}"
	case "$value" in
		dev|production)
			printf '%s\n' "$value"
			;;
		"")
			echo "CONFIG_PROFILE is required for rendered config; set dev or production" >&2
			exit 1
			;;
		*)
			echo "invalid CONFIG_PROFILE: ${value}" >&2
			echo "expected one of: dev, production" >&2
			exit 1
			;;
	esac
}

render_config() {
	local target=$1
	local config_profile
	config_profile="$(config_profile_render_value)"
	local schema_management_mode="${SCHEMA_MANAGEMENT_MODE:-}"
	local statsd_addr="${STATSD_ADDR:-}"
	local check_target_safety_mode="${CHECK_TARGET_SAFETY_MODE:-public_only}"
	local default_check_method="${DEFAULT_CHECK_METHOD:-}"
	local default_detection_profile="${DEFAULT_DETECTION_PROFILE:-}"
	local rollout_mode="${ROLLOUT_MODE:-}"
	local veriflier_discovery_mode="${VERIFLIER_DISCOVERY_MODE:-}"
	local delivery_owner_host="${DELIVERY_OWNER_HOST:-}"
	local debug_port="${DEBUG_PORT:-6060}"
	local wpcom_notify_enable
	local smtp_use_tls
	if [ -z "$schema_management_mode" ]; then
		if [ "$config_profile" = "dev" ]; then
			schema_management_mode="migrate"
		else
			schema_management_mode="validate"
		fi
	fi
	if [ "$config_profile" = "production" ]; then
		statsd_addr="${statsd_addr:-host.docker.internal:8125}"
		default_check_method="${default_check_method:-HEAD}"
		default_detection_profile="${default_detection_profile:-legacy}"
		rollout_mode="${rollout_mode:-api-controlled}"
		veriflier_discovery_mode="${veriflier_discovery_mode:-shadow}"
		debug_port="${DEBUG_PORT:-0}"
	fi
	wpcom_notify_enable="$(bool_json WPCOM_NOTIFY_ENABLE "${WPCOM_NOTIFY_ENABLE:-false}")"
	smtp_use_tls="$(bool_json SMTP_USE_TLS "${SMTP_USE_TLS:-false}")"
	sed \
		-e "s|<AUTH_TOKEN>|$(sed_escape "${WPCOM_AUTH_TOKEN:-change_me}")|g" \
		-e "s|\"CONFIG_PROFILE\"    : \"dev\"|\"CONFIG_PROFILE\"    : \"$(sed_escape "$config_profile")\"|g" \
		-e "s|\"HOSTNAME\"          : \"\"|\"HOSTNAME\"          : \"$(sed_escape "${JETMON_HOSTNAME:-}")\"|g" \
		-e "s|\"STATSD_ADDR\"       : \"\"|\"STATSD_ADDR\"       : \"$(sed_escape "$statsd_addr")\"|g" \
		-e "s|\"STATSD_HOST_PATH\"  : \"\"|\"STATSD_HOST_PATH\"  : \"$(sed_escape "${STATSD_HOST_PATH:-}")\"|g" \
		-e "s|\"DB_HOST\"                  : \"\"|\"DB_HOST\"                  : \"$(sed_escape "${DB_HOST:-}")\"|g" \
		-e "s|\"DB_PORT\"                  : \"\"|\"DB_PORT\"                  : \"$(sed_escape "${DB_PORT:-}")\"|g" \
		-e "s|\"DB_USER\"                  : \"\"|\"DB_USER\"                  : \"$(sed_escape "${DB_USER:-}")\"|g" \
		-e "s|\"DB_PASSWORD\"              : \"\"|\"DB_PASSWORD\"              : \"$(sed_escape "${DB_PASSWORD:-}")\"|g" \
		-e "s|\"DB_NAME\"                  : \"\"|\"DB_NAME\"                  : \"$(sed_escape "${DB_NAME:-}")\"|g" \
		-e "s|\"DB_SERVER_MAP_PATH\"       : \"\"|\"DB_SERVER_MAP_PATH\"       : \"$(sed_escape "${DB_SERVER_MAP_PATH:-}")\"|g" \
		-e "s|\"DB_SERVER_MAP_DATASET\"    : \"\"|\"DB_SERVER_MAP_DATASET\"    : \"$(sed_escape "${DB_SERVER_MAP_DATASET:-}")\"|g" \
		-e "s|\"DB_SERVER_MAP_DATACENTER\" : \"\"|\"DB_SERVER_MAP_DATACENTER\" : \"$(sed_escape "${DB_SERVER_MAP_DATACENTER:-}")\"|g" \
		-e "s|\"DB_SERVER_MAP_ADDRESS\"    : \"\"|\"DB_SERVER_MAP_ADDRESS\"    : \"$(sed_escape "${DB_SERVER_MAP_ADDRESS:-}")\"|g" \
		-e "s|\"SCHEMA_MANAGEMENT_MODE\": \"\"|\"SCHEMA_MANAGEMENT_MODE\": \"$(sed_escape "$schema_management_mode")\"|g" \
		-e "s|\"VERIFLIER_DISCOVERY_MODE\" : \"static\"|\"VERIFLIER_DISCOVERY_MODE\" : \"$(sed_escape "${veriflier_discovery_mode:-static}")\"|g" \
		-e "s|<VERIFLIER_PORT>|$(sed_escape "${VERIFLIER_PORT}")|g" \
		-e "s|<VERIFLIER_AUTH_TOKEN>|$(sed_escape "${VERIFLIER_AUTH_TOKEN:-veriflier_1_auth_token}")|g" \
		-e 's|"API_PORT"       : 0|"API_PORT"       : 8090|g' \
		-e "s|\"WPCOM_NOTIFY_ENABLE\"          : true|\"WPCOM_NOTIFY_ENABLE\"          : ${wpcom_notify_enable}|g" \
		-e "s|\"CHECK_TARGET_SAFETY_MODE\"     : \"public_only\"|\"CHECK_TARGET_SAFETY_MODE\"     : \"$(sed_escape "$check_target_safety_mode")\"|g" \
		-e "s|\"DEFAULT_CHECK_METHOD\"         : \"GET\"|\"DEFAULT_CHECK_METHOD\"         : \"$(sed_escape "${default_check_method:-GET}")\"|g" \
		-e "s|\"DEFAULT_DETECTION_PROFILE\"    : \"full\"|\"DEFAULT_DETECTION_PROFILE\"    : \"$(sed_escape "${default_detection_profile:-full}")\"|g" \
		-e "s|\"ROLLOUT_MODE\"                : \"active\"|\"ROLLOUT_MODE\"                : \"$(sed_escape "${rollout_mode:-active}")\"|g" \
		-e "s|\"DELIVERY_OWNER_HOST\": \"\"|\"DELIVERY_OWNER_HOST\": \"$(sed_escape "$delivery_owner_host")\"|g" \
		-e "s|\"DEBUG_PORT\"     : 6060|\"DEBUG_PORT\"     : ${debug_port}|g" \
		-e "s|\"WPCOM_NOTIFY_MODE\"            : \"legacy\"|\"WPCOM_NOTIFY_MODE\"            : \"$(sed_escape "${WPCOM_NOTIFY_MODE:-legacy}")\"|g" \
		-e "s|\"EMAIL_TRANSPORT\"       : \"stub\"|\"EMAIL_TRANSPORT\"       : \"$(sed_escape "${EMAIL_TRANSPORT:-smtp}")\"|g" \
		-e "s|\"EMAIL_FROM\"            : \"jetmon@noreply.invalid\"|\"EMAIL_FROM\"            : \"$(sed_escape "${EMAIL_FROM:-jetmon@noreply.invalid}")\"|g" \
		-e "s|\"SMTP_HOST\"             : \"\"|\"SMTP_HOST\"             : \"$(sed_escape "${SMTP_HOST:-mailpit}")\"|g" \
		-e "s|\"SMTP_PORT\"             : 0|\"SMTP_PORT\"             : ${SMTP_PORT:-1025}|g" \
		-e "s|\"SMTP_USERNAME\"         : \"\"|\"SMTP_USERNAME\"         : \"$(sed_escape "${SMTP_USERNAME:-}")\"|g" \
		-e "s|\"SMTP_PASSWORD\"         : \"\"|\"SMTP_PASSWORD\"         : \"$(sed_escape "${SMTP_PASSWORD:-}")\"|g" \
		-e "s|\"SMTP_USE_TLS\"          : false|\"SMTP_USE_TLS\"          : ${smtp_use_tls}|g" \
		config/config-sample.json > "${target}"
}

rendered_config_summary() {
	local config_profile="${CONFIG_PROFILE:-unset}"
	local statsd_addr="${STATSD_ADDR:-}"
	local rollout_mode="${ROLLOUT_MODE:-}"
	local veriflier_discovery_mode="${VERIFLIER_DISCOVERY_MODE:-}"
	if [ "$config_profile" = "production" ]; then
		statsd_addr="${statsd_addr:-host.docker.internal:8125}"
		rollout_mode="${rollout_mode:-api-controlled}"
		veriflier_discovery_mode="${veriflier_discovery_mode:-shadow}"
	fi
	printf 'profile=%s hostname=%s statsd=%s rollout=%s veriflier_discovery=%s db=%s' \
		"$config_profile" "${JETMON_HOSTNAME:-runtime-hostname}" \
		"${statsd_addr:-disabled}" "${rollout_mode:-active}" \
		"${veriflier_discovery_mode:-static}" "$(db_source_summary)"
}

config_render_target() {
	printf '%s\n' "${JETMON_CONFIG:-/tmp/jetmon-rendered-config/config.json}"
}

render_mode() {
	case "${JETMON_CONFIG_RENDER_MODE:-always}" in
		always|missing|never)
			printf '%s\n' "${JETMON_CONFIG_RENDER_MODE:-always}"
			;;
		*)
			echo "invalid JETMON_CONFIG_RENDER_MODE: ${JETMON_CONFIG_RENDER_MODE}" >&2
			echo "expected one of: always, missing, never" >&2
			exit 1
			;;
	esac
}

db_source_summary() {
	if [ -n "${DB_SERVER_MAP_PATH:-}" ]; then
		printf 'server-map:%s dataset=%s dc=%s address=%s' \
			"$DB_SERVER_MAP_PATH" "${DB_SERVER_MAP_DATASET:-misc}" \
			"${DB_SERVER_MAP_DATACENTER:-unset}" "${DB_SERVER_MAP_ADDRESS:-internet}"
	elif [ -n "${DB_HOST:-}" ]; then
		printf 'explicit:%s:%s/%s user=%s' \
			"$DB_HOST" "${DB_PORT:-3306}" "${DB_NAME:-jetmon_db}" "${DB_USER:-root}"
	else
		printf 'default:localhost:3306/jetmon_db'
	fi
}

configure_runtime_config() {
	local mode=$1
	local target
	case "$mode" in
		always)
			target="$(config_render_target)"
			mkdir -p "$(dirname "$target")"
			render_config "$target"
			export JETMON_CONFIG="$target"
			echo "config: rendered ${target} from Docker environment (render_mode=always)"
			echo "config: $(rendered_config_summary)"
			;;
		missing)
			target="$(config_render_target)"
			export JETMON_CONFIG="$target"
			if [ ! -f "$target" ]; then
				mkdir -p "$(dirname "$target")"
				render_config "$target"
				echo "config: rendered ${target} from Docker environment (render_mode=missing)"
			else
				echo "config: using existing ${target} (render_mode=missing; environment changes are ignored until the file is removed)"
			fi
			;;
		never)
			if [ -n "${JETMON_CONFIG:-}" ]; then
				echo "config: using ${JETMON_CONFIG} (render_mode=never)"
			else
				echo "config: rendering disabled; jetmon2 will use its default config/config.json path"
			fi
			;;
	esac
}

# /jetmon is owned by the jetmon user from the Dockerfile, but the container
# runs as ${UID:-1000}:${GID:-1000} via docker-compose — write to stats/ instead,
# which the Dockerfile chmods 0777 specifically so reload/drain commands work.
export JETMON_PID_FILE="${JETMON_PID_FILE:-/jetmon/stats/jetmon2.pid}"
export JETMON_REEXEC_PATH="${JETMON_REEXEC_PATH:-/jetmon/entrypoint.sh}"
export VERIFLIER_PORT="${VERIFLIER_PORT:-${VERIFLIER_GRPC_PORT:-7803}}"
config_mode="$(render_mode)"

mkdir -p stats
for path in stats/sitespersec stats/sitesqueue stats/totals; do
	if ! touch "$path" 2>/dev/null; then
		echo "warning: could not write $path; check docker/.env UID/GID and host directory permissions" >&2
	fi
done

configure_runtime_config "$config_mode"

./jetmon2 schema ensure

exec ./jetmon2
