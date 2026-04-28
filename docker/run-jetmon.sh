#!/usr/bin/env bash
set -euo pipefail

cd /jetmon

sed_escape() {
	printf '%s' "$1" | sed -e 's/[\\&|]/\\&/g'
}

render_config() {
	local target=$1
	sed \
		-e "s|<AUTH_TOKEN>|$(sed_escape "${WPCOM_AUTH_TOKEN:-change_me}")|g" \
		-e "s|<VERIFLIER_PORT>|$(sed_escape "${VERIFLIER_PORT}")|g" \
		-e "s|<VERIFLIER_AUTH_TOKEN>|$(sed_escape "${VERIFLIER_AUTH_TOKEN:-veriflier_1_auth_token}")|g" \
		-e 's|"API_PORT"       : 0|"API_PORT"       : 8090|g' \
		-e "s|\"EMAIL_TRANSPORT\"       : \"stub\"|\"EMAIL_TRANSPORT\"       : \"$(sed_escape "${EMAIL_TRANSPORT:-smtp}")\"|g" \
		-e "s|\"EMAIL_FROM\"            : \"jetmon@noreply.invalid\"|\"EMAIL_FROM\"            : \"$(sed_escape "${EMAIL_FROM:-jetmon@noreply.invalid}")\"|g" \
		-e "s|\"SMTP_HOST\"             : \"\"|\"SMTP_HOST\"             : \"$(sed_escape "${SMTP_HOST:-mailpit}")\"|g" \
		-e "s|\"SMTP_PORT\"             : 0|\"SMTP_PORT\"             : ${SMTP_PORT:-1025}|g" \
		-e "s|\"SMTP_USERNAME\"         : \"\"|\"SMTP_USERNAME\"         : \"$(sed_escape "${SMTP_USERNAME:-}")\"|g" \
		-e "s|\"SMTP_PASSWORD\"         : \"\"|\"SMTP_PASSWORD\"         : \"$(sed_escape "${SMTP_PASSWORD:-}")\"|g" \
		-e "s|\"SMTP_USE_TLS\"          : false|\"SMTP_USE_TLS\"          : ${SMTP_USE_TLS:-false}|g" \
		config/config-sample.json > "${target}"
}

config_target() {
	if [ -w config/ ]; then
		printf '%s\n' "config/config.json"
	else
		export JETMON_CONFIG=/tmp/config.json
		printf '%s\n' "${JETMON_CONFIG}"
	fi
}

# /jetmon is owned by the jetmon user from the Dockerfile, but the container
# runs as ${UID:-1000}:${GID:-1000} via docker-compose — write to stats/ instead, which
# the Dockerfile chmods 0777 specifically so reload/drain commands work.
export JETMON_PID_FILE="${JETMON_PID_FILE:-/jetmon/stats/jetmon2.pid}"
export VERIFLIER_PORT="${VERIFLIER_PORT:-${VERIFLIER_GRPC_PORT:-7803}}"

mkdir -p logs stats
for path in logs/jetmon.log logs/status-change.log stats/sitespersec stats/sitesqueue stats/totals; do
	if ! touch "$path" 2>/dev/null; then
		echo "warning: could not write $path; check docker/.env UID/GID and host directory permissions" >&2
	fi
done

if [ ! -f config/config.json ]; then
	render_config "$(config_target)"
fi

./jetmon2 migrate

exec ./jetmon2
