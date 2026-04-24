#!/usr/bin/env bash
cd /jetmon
export JETMON_PID_FILE=/jetmon/jetmon2.pid

touch logs/jetmon.log logs/status-change.log
touch stats/sitespersec stats/sitesqueue stats/totals

if [ ! -f config/config.json ]; then
	if [ -w config/ ]; then
		sed \
			-e "s/<AUTH_TOKEN>/${WPCOM_JETMON_AUTH_TOKEN}/g" \
			-e "s/<VERIFLIER_GRPC_PORT>/${VERIFLIER_GRPC_PORT}/g" \
			-e "s/<VERIFLIER_AUTH_TOKEN>/${VERIFLIER_AUTH_TOKEN}/g" \
			config/config-sample.json > config/config.json
	else
		export JETMON_CONFIG=/tmp/config.json
		sed \
			-e "s/<AUTH_TOKEN>/${WPCOM_JETMON_AUTH_TOKEN}/g" \
			-e "s/<VERIFLIER_GRPC_PORT>/${VERIFLIER_GRPC_PORT}/g" \
			-e "s/<VERIFLIER_AUTH_TOKEN>/${VERIFLIER_AUTH_TOKEN}/g" \
			config/config-sample.json > "${JETMON_CONFIG}"
	fi
fi


./jetmon2 migrate

exec ./jetmon2
