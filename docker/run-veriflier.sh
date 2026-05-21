#!/usr/bin/env bash
set -euo pipefail

cd /opt/veriflier

sed_escape() {
	printf '%s' "$1" | sed -e 's/[\\&|]/\\&/g'
}

bool_json() {
	case "${1,,}" in
		1|t|true|y|yes|on|enabled)
			printf 'true'
			;;
		0|f|false|n|no|off|disabled)
			printf 'false'
			;;
		*)
			echo "invalid VERIFLIER_ENABLE_LEGACY_HTTP value: $1" >&2
			exit 1
			;;
	esac
}

render_config() {
	local target=$1
	local legacy_http
	legacy_http="$(bool_json "${VERIFLIER_ENABLE_LEGACY_HTTP:-false}")"
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
		config/veriflier-sample.json > "${target}"
}

config_target() {
	if [ -w config/ ]; then
		printf '%s\n' "config/veriflier.json"
	else
		export VERIFLIER_CONFIG=/tmp/veriflier.json
		printf '%s\n' "${VERIFLIER_CONFIG}"
	fi
}

export VERIFLIER_PORT="${VERIFLIER_PORT:-${VERIFLIER_GRPC_PORT:-7803}}"
export VERIFLIER_VANTAGE_ID="${VERIFLIER_VANTAGE_ID:-local-veriflier}"
export VERIFLIER_REGION="${VERIFLIER_REGION:-local}"
export VERIFLIER_PROVIDER="${VERIFLIER_PROVIDER:-docker}"

if [ ! -f config/veriflier.json ]; then
	render_config "$(config_target)"
fi

exec ./veriflier2
