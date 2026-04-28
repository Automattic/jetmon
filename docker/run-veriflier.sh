#!/usr/bin/env bash
set -euo pipefail

cd /opt/veriflier
export VERIFLIER_PORT="${VERIFLIER_PORT:-${VERIFLIER_GRPC_PORT:-7803}}"

if [ ! -f config/veriflier.json ]; then
	if [ -w config/ ]; then
		sed \
			-e "s/<VERIFLIER_PORT>/${VERIFLIER_PORT}/g" \
			-e "s/<VERIFLIER_AUTH_TOKEN>/${VERIFLIER_AUTH_TOKEN}/g" \
			config/veriflier-sample.json > config/veriflier.json
	else
		export VERIFLIER_CONFIG=/tmp/veriflier.json
		sed \
			-e "s/<VERIFLIER_PORT>/${VERIFLIER_PORT}/g" \
			-e "s/<VERIFLIER_AUTH_TOKEN>/${VERIFLIER_AUTH_TOKEN}/g" \
			config/veriflier-sample.json > "${VERIFLIER_CONFIG}"
	fi
fi

exec ./veriflier2
