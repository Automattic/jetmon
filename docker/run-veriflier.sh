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

if [ ! -f config/veriflier.json ]; then
	render_config "$(config_target)"
fi

exec ./veriflier2
