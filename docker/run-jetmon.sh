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

npm run rebuild-run