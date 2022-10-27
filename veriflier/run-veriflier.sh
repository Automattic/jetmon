#!/bin/bash

qmake
make

mkdir -p certs
if [ ! -f certs/veriflier.key ] && [ ! -f certs/veriflier.crt ]; then
	openssl req -newkey rsa:2048 -nodes -keyout certs/veriflier.key -x509 -days 365 -out certs/veriflier.crt -subj "/C=US/ST=California/L=San Francisco/O=Jetpack/OU=veriflier/CN=localhost"
fi

./veriflier start
