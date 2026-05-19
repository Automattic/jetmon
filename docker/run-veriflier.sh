#!/usr/bin/env bash
set -euo pipefail

cd /opt/veriflier

sed_escape() {
	printf '%s' "$1" | sed -e 's/[\\&|]/\\&/g'
}

render_config() {
	local target=$1
	sed \
		-e "s|<VERIFLIER_PORT>|$(sed_escape "${VERIFLIER_PORT}")|g" \
		-e "s|<VERIFLIER_AUTH_TOKEN>|$(sed_escape "${VERIFLIER_AUTH_TOKEN:-veriflier_1_auth_token}")|g" \
		-e "s|\"hostname\"   : \"\"|\"hostname\"   : \"$(sed_escape "${VERIFLIER_HOSTNAME:-${JETMON_HOSTNAME:-}}")\"|g" \
		-e "s|<VERIFLIER_VANTAGE_ID>|$(sed_escape "${VERIFLIER_VANTAGE_ID:-local-veriflier}")|g" \
		-e "s|<VERIFLIER_REGION>|$(sed_escape "${VERIFLIER_REGION:-local}")|g" \
		-e "s|<VERIFLIER_PROVIDER>|$(sed_escape "${VERIFLIER_PROVIDER:-docker}")|g" \
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
