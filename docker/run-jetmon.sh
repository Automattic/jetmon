#!/usr/bin/env bash
cd /jetmon
# /jetmon is owned by the jetmon user from the Dockerfile, but the container
# runs as ${JETMON_UID:-1000} via docker-compose — write to stats/ instead, which
# the Dockerfile chmods 0777 specifically so reload/drain commands work.
export JETMON_PID_FILE=/jetmon/stats/jetmon2.pid
export VERIFLIER_PORT="${VERIFLIER_PORT:-${VERIFLIER_GRPC_PORT:-7803}}"

touch logs/jetmon.log logs/status-change.log
touch stats/sitespersec stats/sitesqueue stats/totals

if [ ! -f config/config.json ]; then
	if [ -w config/ ]; then
		sed \
			-e "s/<AUTH_TOKEN>/${WPCOM_JETMON_AUTH_TOKEN}/g" \
			-e "s/<VERIFLIER_PORT>/${VERIFLIER_PORT}/g" \
			-e "s/<VERIFLIER_AUTH_TOKEN>/${VERIFLIER_AUTH_TOKEN}/g" \
			config/config-sample.json > config/config.json
	else
		export JETMON_CONFIG=/tmp/config.json
		sed \
			-e "s/<AUTH_TOKEN>/${WPCOM_JETMON_AUTH_TOKEN}/g" \
			-e "s/<VERIFLIER_PORT>/${VERIFLIER_PORT}/g" \
			-e "s/<VERIFLIER_AUTH_TOKEN>/${VERIFLIER_AUTH_TOKEN}/g" \
			config/config-sample.json > "${JETMON_CONFIG}"
	fi
fi


./jetmon2 migrate

exec ./jetmon2
