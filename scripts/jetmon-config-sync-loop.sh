#!/usr/bin/env bash
set -euo pipefail

# Container-side wrapper for periodically refreshing the private Jetmon
# database server map. The one-shot updater owns SVN and file-sync behavior;
# this script only adds retry, cadence, and jitter for sidecar use.

update_cmd="${JETMON_CONFIG_SYNC_UPDATE_CMD:-/usr/local/bin/jetmon-config-update.sh}"
interval="${JETMON_CONFIG_SYNC_INTERVAL_SEC:-600}"
jitter="${JETMON_CONFIG_SYNC_JITTER_SEC:-120}"
initial_jitter="${JETMON_CONFIG_SYNC_INITIAL_JITTER_SEC:-$jitter}"
exit_on_error="${JETMON_CONFIG_SYNC_EXIT_ON_ERROR:-false}"

is_uint() {
	[[ "$1" =~ ^[0-9]+$ ]]
}

for pair in "JETMON_CONFIG_SYNC_INTERVAL_SEC:$interval" \
	"JETMON_CONFIG_SYNC_JITTER_SEC:$jitter" \
	"JETMON_CONFIG_SYNC_INITIAL_JITTER_SEC:$initial_jitter"; do
	name="${pair%%:*}"
	value="${pair#*:}"
	if ! is_uint "$value"; then
		echo "$name must be an integer number of seconds" >&2
		exit 1
	fi
done

sleep_with_jitter() {
	local base="$1"
	local spread="$2"
	local extra=0

	if (( spread > 0 )); then
		extra=$(( RANDOM % (spread + 1) ))
	fi

	sleep "$(( base + extra ))"
}

run_update() {
	local now
	now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
	echo "[$now] running Jetmon config sync"
	"$update_cmd"
}

if (( initial_jitter > 0 )); then
	sleep_with_jitter 0 "$initial_jitter"
fi

while true; do
	if run_update; then
		:
	else
		status=$?
		now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
		echo "[$now] Jetmon config sync failed with status $status" >&2
		if [[ "$exit_on_error" == "true" ]]; then
			exit "$status"
		fi
	fi

	sleep_with_jitter "$interval" "$jitter"
done
