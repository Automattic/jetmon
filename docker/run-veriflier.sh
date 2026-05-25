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
	legacy_http="$(bool_json VERIFLIER_ENABLE_LEGACY_HTTP "${VERIFLIER_ENABLE_LEGACY_HTTP:-false}")"
	target_safety="${VERIFLIER_CHECK_TARGET_SAFETY_MODE:-${CHECK_TARGET_SAFETY_MODE:-public_only}}"
	sed \
		-e "s|<VERIFLIER_PORT>|$(sed_escape "${VERIFLIER_PORT}")|g" \
		-e "s|<VERIFLIER_AUTH_TOKEN>|$(sed_escape "${VERIFLIER_AUTH_TOKEN:-veriflier_1_auth_token}")|g" \
		-e "s|\"hostname\"   : \"\"|\"hostname\"   : \"$(sed_escape "${VERIFLIER_HOSTNAME:-${JETMON_HOSTNAME:-}}")\"|g" \
		-e "s|\"statsd_addr\" : \"\"|\"statsd_addr\" : \"$(sed_escape "${STATSD_ADDR:-}")\"|g" \
		-e "s|\"statsd_host_path\" : \"\"|\"statsd_host_path\" : \"$(sed_escape "${STATSD_HOST_PATH:-}")\"|g" \
		-e "s|<VERIFLIER_VANTAGE_ID>|$(sed_escape "${VERIFLIER_VANTAGE_ID:-local-veriflier}")|g" \
		-e "s|<VERIFLIER_REGION>|$(sed_escape "${VERIFLIER_REGION:-local}")|g" \
		-e "s|<VERIFLIER_PROVIDER>|$(sed_escape "${VERIFLIER_PROVIDER:-docker}")|g" \
		-e "s|\"enable_legacy_http\" : false|\"enable_legacy_http\" : ${legacy_http}|g" \
		-e "s|\"check_target_safety_mode\" : \"public_only\"|\"check_target_safety_mode\" : \"$(sed_escape "${target_safety}")\"|g" \
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
			echo "config: hostname=${VERIFLIER_HOSTNAME:-${JETMON_HOSTNAME:-runtime-hostname}} statsd=${STATSD_ADDR:-disabled} vantage=${VERIFLIER_VANTAGE_ID:-local-veriflier} legacy_http=${VERIFLIER_ENABLE_LEGACY_HTTP:-false} target_safety=${VERIFLIER_CHECK_TARGET_SAFETY_MODE:-${CHECK_TARGET_SAFETY_MODE:-public_only}}"
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
config_mode="$(render_mode)"

configure_runtime_config "$config_mode"

exec ./veriflier2
