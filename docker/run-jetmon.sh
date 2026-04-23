#!/usr/bin/env bash
cd /jetmon

touch logs/jetmon.log logs/status-change.log
touch stats/sitespersec stats/sitesqueue stats/totals

if [ ! -f config/config.json ]; then
	sed \
		-e "s/<AUTH_TOKEN>/${WPCOM_JETMON_AUTH_TOKEN}/g" \
		-e "s/<VERIFLIER_GRPC_PORT>/${VERIFLIER_GRPC_PORT}/g" \
		-e "s/<VERIFLIER_AUTH_TOKEN>/${VERIFLIER_AUTH_TOKEN}/g" \
		config/config-sample.json > config/config.json
fi


./jetmon2 migrate

exec ./jetmon2
