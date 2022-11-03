#!/bin/bash

cd /opt/veriflier

qmake
make

mkdir -p certs
if [ ! -f certs/veriflier.key ] && [ ! -f certs/veriflier.crt ]; then
	openssl req -newkey rsa:2048 -nodes -keyout certs/veriflier.key -x509 -days 365 -out certs/veriflier.crt -subj "/C=US/ST=California/L=San Francisco/O=Automattic Inc./CN=jetmon"
fi

sed -e "s/<JETMON_PORT>/${JETMON_PORT}/g" -e "s/<VERIFLIER_PORT>/${VERIFLIER_PORT}/g" -e "s/<VERIFLIER_AUTH_TOKEN>/${VERIFLIER_AUTH_TOKEN}/g" config/veriflier-sample.json > config/veriflier.json

exec ./veriflier start
