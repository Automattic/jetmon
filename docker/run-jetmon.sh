#!/usr/bin/env bash
cd /jetmon
# /jetmon is owned by the jetmon user from the Dockerfile, but the container
# runs as ${UID:-1000}:${GID:-1000} via docker-compose — write to stats/ instead, which
# the Dockerfile chmods 0777 specifically so reload/drain commands work.
export JETMON_PID_FILE=/jetmon/stats/jetmon2.pid
export VERIFLIER_PORT="${VERIFLIER_PORT:-${VERIFLIER_GRPC_PORT:-7803}}"

mkdir -p logs stats
for path in logs/jetmon.log logs/status-change.log stats/sitespersec stats/sitesqueue stats/totals; do
	if ! touch "$path" 2>/dev/null; then
		echo "warning: could not write $path; check docker/.env UID/GID and host directory permissions" >&2
	fi
done

if [ ! -f config/config.json ]; then
	if [ -w config/ ]; then
		sed \
			-e "s/<AUTH_TOKEN>/${WPCOM_AUTH_TOKEN}/g" \
			-e "s/<VERIFLIER_PORT>/${VERIFLIER_PORT}/g" \
			-e "s/<VERIFLIER_AUTH_TOKEN>/${VERIFLIER_AUTH_TOKEN}/g" \
			-e 's/"API_PORT"       : 0/"API_PORT"       : 8090/g' \
			config/config-sample.json > config/config.json
	else
		export JETMON_CONFIG=/tmp/config.json
		sed \
			-e "s/<AUTH_TOKEN>/${WPCOM_AUTH_TOKEN}/g" \
			-e "s/<VERIFLIER_PORT>/${VERIFLIER_PORT}/g" \
			-e "s/<VERIFLIER_AUTH_TOKEN>/${VERIFLIER_AUTH_TOKEN}/g" \
			-e 's/"API_PORT"       : 0/"API_PORT"       : 8090/g' \
			config/config-sample.json > "${JETMON_CONFIG}"
	fi
fi


./jetmon2 migrate

exec ./jetmon2
