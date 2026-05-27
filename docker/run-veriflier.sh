#!/usr/bin/env bash
set -euo pipefail

cd /opt/veriflier

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

render_config() {
	local target=$1
	local legacy_http
	local target_safety
	local statsd_host_path
	local ops_alerts_enabled
	local ops_alerts_service_online
	legacy_http="$(bool_json VERIFLIER_ENABLE_LEGACY_HTTP "${VERIFLIER_ENABLE_LEGACY_HTTP:-false}")"
	target_safety="${VERIFLIER_CHECK_TARGET_SAFETY_MODE:-${CHECK_TARGET_SAFETY_MODE:-public_only}}"
	statsd_host_path="${VERIFLIER_STATSD_HOST_PATH:-${STATSD_HOST_PATH:-}}"
	ops_alerts_enabled="$(bool_json OPS_ALERTS_ENABLED "${OPS_ALERTS_ENABLED:-false}")"
	ops_alerts_service_online="$(bool_json OPS_ALERTS_SERVICE_ONLINE "${OPS_ALERTS_SERVICE_ONLINE:-true}")"
	sed \
		-e "s|<VERIFLIER_PORT>|$(sed_escape "${VERIFLIER_PORT}")|g" \
		-e "s|<VERIFLIER_AUTH_TOKEN>|$(sed_escape "${VERIFLIER_AUTH_TOKEN:?set VERIFLIER_AUTH_TOKEN}")|g" \
		-e "s|\"hostname\"   : \"\"|\"hostname\"   : \"$(sed_escape "${VERIFLIER_HOSTNAME:-${JETMON_HOSTNAME:-}}")\"|g" \
		-e "s|\"statsd_addr\" : \"\"|\"statsd_addr\" : \"$(sed_escape "${STATSD_ADDR:-}")\"|g" \
		-e "s|\"statsd_host_path\" : \"\"|\"statsd_host_path\" : \"$(sed_escape "$statsd_host_path")\"|g" \
		-e "s|<VERIFLIER_VANTAGE_ID>|$(sed_escape "${VERIFLIER_VANTAGE_ID:-local-veriflier}")|g" \
		-e "s|<VERIFLIER_REGION>|$(sed_escape "${VERIFLIER_REGION:-local}")|g" \
		-e "s|<VERIFLIER_PROVIDER>|$(sed_escape "${VERIFLIER_PROVIDER:-docker}")|g" \
		-e "s|\"tls_cert_path\" : \"\"|\"tls_cert_path\" : \"$(sed_escape "${VERIFLIER_TLS_CERT_PATH:-}")\"|g" \
		-e "s|\"tls_key_path\"  : \"\"|\"tls_key_path\"  : \"$(sed_escape "${VERIFLIER_TLS_KEY_PATH:-}")\"|g" \
		-e "s|\"enable_legacy_http\" : false|\"enable_legacy_http\" : ${legacy_http}|g" \
		-e "s|\"check_target_safety_mode\" : \"public_only\"|\"check_target_safety_mode\" : \"$(sed_escape "${target_safety}")\"|g" \
		-e "s|\"ops_alerts_enabled\" : false|\"ops_alerts_enabled\" : ${ops_alerts_enabled}|g" \
		-e "s|\"ops_alerts_slack_webhook_url\" : \"\"|\"ops_alerts_slack_webhook_url\" : \"$(sed_escape "${OPS_ALERTS_SLACK_WEBHOOK_URL:-}")\"|g" \
		-e "s|\"ops_alerts_min_severity\" : \"warning\"|\"ops_alerts_min_severity\" : \"$(sed_escape "${OPS_ALERTS_MIN_SEVERITY:-warning}")\"|g" \
		-e "s|\"ops_alerts_repeat_interval_sec\" : 300|\"ops_alerts_repeat_interval_sec\" : ${OPS_ALERTS_REPEAT_INTERVAL_SEC:-300}|g" \
		-e "s|\"ops_alerts_service_online\" : true|\"ops_alerts_service_online\" : ${ops_alerts_service_online}|g" \
		config/veriflier-sample.json > "${target}"
}

config_render_target() {
	# Default to the in-image (and named-volume backed in prod) location so
	# operators inspecting the container see the same config the binary is
	# using. With render_mode=always (the default), this file is overwritten
	# on every container start from the current environment — no stale data
	# can survive a token rotation, FQDN rename, or vantage_id change.
	# Override via VERIFLIER_CONFIG to point elsewhere (e.g. tmpfs) when
	# write-back to the config dir is undesirable.
	printf '%s\n' "${VERIFLIER_CONFIG:-config/veriflier.json}"
}

render_mode() {
	local mode="${VERIFLIER_CONFIG_RENDER_MODE:-${JETMON_CONFIG_RENDER_MODE:-always}}"
	case "$mode" in
		always|missing|never)
			printf '%s\n' "$mode"
			;;
		*)
			echo "invalid VERIFLIER_CONFIG_RENDER_MODE: ${mode}" >&2
			echo "expected one of: always, missing, never" >&2
			exit 1
			;;
	esac
}

configure_runtime_config() {
	local mode=$1
	local target
	case "$mode" in
		always)
			target="$(config_render_target)"
			mkdir -p "$(dirname "$target")"
			render_config "$target"
			export VERIFLIER_CONFIG="$target"
			echo "config: rendered ${target} from Docker environment (render_mode=always)"
			echo "config: hostname=${VERIFLIER_HOSTNAME:-${JETMON_HOSTNAME:-runtime-hostname}} statsd=${STATSD_ADDR:-disabled} statsd_host_path=${VERIFLIER_STATSD_HOST_PATH:-${STATSD_HOST_PATH:-runtime-hostname}} vantage=${VERIFLIER_VANTAGE_ID:-local-veriflier} legacy_http=${VERIFLIER_ENABLE_LEGACY_HTTP:-false} tls_cert=${VERIFLIER_TLS_CERT_PATH:-disabled} target_safety=${VERIFLIER_CHECK_TARGET_SAFETY_MODE:-${CHECK_TARGET_SAFETY_MODE:-public_only}}"
			;;
		missing)
			target="$(config_render_target)"
			export VERIFLIER_CONFIG="$target"
			if [ ! -f "$target" ]; then
				mkdir -p "$(dirname "$target")"
				render_config "$target"
				echo "config: rendered ${target} from Docker environment (render_mode=missing)"
			else
				echo "config: using existing ${target} (render_mode=missing; environment changes are ignored until the file is removed)"
			fi
			;;
		never)
			if [ -n "${VERIFLIER_CONFIG:-}" ]; then
				echo "config: using ${VERIFLIER_CONFIG} (render_mode=never)"
			else
				echo "config: rendering disabled; veriflier2 will use its default config/veriflier.json path"
			fi
			;;
	esac
}

export VERIFLIER_PORT="${VERIFLIER_PORT:-${VERIFLIER_GRPC_PORT:-7803}}"
export VERIFLIER_VANTAGE_ID="${VERIFLIER_VANTAGE_ID:-local-veriflier}"
export VERIFLIER_REGION="${VERIFLIER_REGION:-local}"
export VERIFLIER_PROVIDER="${VERIFLIER_PROVIDER:-docker}"
export JETMON_REEXEC_PATH="${JETMON_REEXEC_PATH:-/opt/veriflier/entrypoint.sh}"
config_mode="$(render_mode)"

configure_runtime_config "$config_mode"

exec ./veriflier2
