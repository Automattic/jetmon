#!/usr/bin/env bash
cd /jetmon

mkdir -p logs
touch logs/jetmon.log logs/status-change.log

mkdir -p stats
touch stats/sitespersec stats/sitesqueue stats/totals

mkdir -p certs
if [ ! -f certs/jetmon.key ] && [ ! -f certs/jetmon.crt ]; then
	openssl req -newkey rsa:2048 -nodes -keyout certs/jetmon.key -x509 -days 365 -out certs/jetmon.crt -subj "/C=US/ST=California/L=San Francisco/O=Automattic Inc./CN=jetmon"
fi

if [ ! -f config/config.json ]; then
	sed -e "s/<AUTH_TOKEN>/${WPCOM_JETMON_AUTH_TOKEN}/g" -e "s/<VERIFLIER_PORT>/${VERIFLIER_PORT}/g" -e "s/<VERIFLIER_AUTH_TOKEN>/${VERIFLIER_AUTH_TOKEN}/g" config/config-sample.json > config/config.json
fi
if [ ! -f config/db-config.conf ]; then
	sed -e "s/<MYSQLDB_USER>/${MYSQLDB_USER}/g" -e "s/<MYSQLDB_ROOT_PASSWORD>/${MYSQLDB_ROOT_PASSWORD}/g" -e "s/<MYSQLDB_PORT>/${MYSQLDB_DOCKER_PORT}/g" -e "s/<MYSQLDB_DATABASE>/${MYSQLDB_DATABASE}/g" config/db-config-sample.conf > config/db-config.conf
fi

npm install
exec npm run rebuild-run
