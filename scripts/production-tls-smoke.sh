#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat <<'USAGE'
usage: scripts/production-tls-smoke.sh <monitor|veriflier> <host>

Checks a production-style Caddy TLS deployment from outside the Docker network:
  monitor   -> https://<host>/api/v1/health and direct :8090 exposure check
  veriflier -> https://<host>/v2/status and direct :7803 exposure check

The script intentionally does not pass -k. Certificate validation must succeed.
USAGE
}

fail() {
	printf 'FAIL %s\n' "$*" >&2
	exit 1
}

pass() {
	printf 'PASS %s\n' "$*"
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"
}

check_https() {
	local url=$1
	local label=$2
	curl -fsS --max-time 10 "$url" >/dev/null
	pass "${label}=${url}"
}

check_http_redirect() {
	local host=$1
	local code
	code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 "http://${host}/" || true)"
	case "$code" in
		301|302|303|307|308)
			pass "http_redirect=${host} code=${code}"
			;;
		*)
			fail "expected HTTP redirect for ${host}, got code=${code:-none}"
			;;
	esac
}

check_port_not_public() {
	local host=$1
	local port=$2
	local path=$3
	local code
	code="$(curl -sS -o /dev/null -w '%{http_code}' --connect-timeout 3 --max-time 5 "http://${host}:${port}${path}" || true)"
	if [ "$code" = "200" ]; then
		fail "plain app port is publicly reachable: http://${host}:${port}${path}"
	fi
	pass "plain_app_port_not_public=${host}:${port}"
}

main() {
	need_cmd curl
	if [ "$#" -ne 2 ]; then
		usage >&2
		exit 1
	fi
	local kind=$1
	local host=$2
	case "$kind" in
		monitor)
			check_https "https://${host}/api/v1/health" "monitor_https_health"
			check_http_redirect "$host"
			check_port_not_public "$host" 8090 /api/v1/health
			;;
		veriflier)
			check_https "https://${host}/v2/status" "veriflier_https_status"
			check_http_redirect "$host"
			check_port_not_public "$host" 7803 /v2/status
			;;
		*)
			usage >&2
			exit 1
			;;
	esac
}

main "$@"
