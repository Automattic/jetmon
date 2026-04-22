#!/usr/bin/env bash
cd /opt/veriflier

if [ ! -f config/veriflier.json ]; then
	sed \
		-e "s/<VERIFLIER_GRPC_PORT>/${VERIFLIER_GRPC_PORT}/g" \
		-e "s/<VERIFLIER_AUTH_TOKEN>/${VERIFLIER_AUTH_TOKEN}/g" \
		config/veriflier-sample.json > config/veriflier.json
fi

exec ./veriflier2
