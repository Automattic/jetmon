#!/usr/bin/env bash
set -euo pipefail

binary="${API_CLI_BINARY:-./bin/jetmon2}"
batch="${API_VALIDATE_BATCH:-api-cli-validate}"
smoke_batch="${batch}-smoke"
failure_batch="${batch}-failure"
failure_count="${API_VALIDATE_COUNT:-1}"
failure_mode="${API_VALIDATE_MODE:-http-500}"
failure_wait="${API_VALIDATE_WAIT:-30s}"

if [[ -z "${JETMON_API_TOKEN:-}" ]]; then
	echo "JETMON_API_TOKEN is required" >&2
	exit 1
fi

export JETMON_API_URL="${JETMON_API_URL:-http://localhost:${API_HOST_PORT:-8090}}"

cleanup() {
	"$binary" api sites cleanup --batch "$smoke_batch" --count 3 --output table >/dev/null 2>&1 || true
	"$binary" api sites cleanup --batch "$failure_batch" --count "$failure_count" --output table >/dev/null 2>&1 || true
}
trap cleanup EXIT

step() {
	printf '\n== %s ==\n' "$1"
	shift
	"$@"
}

step "health" "$binary" api health --pretty
step "identity" "$binary" api me --pretty
step "request escape hatch" "$binary" api request --output table GET "/api/v1/sites?limit=1"
step "bulk-add dry run" "$binary" api sites bulk-add --count 3 --batch "$smoke_batch" --dry-run --pretty
step "smoke workflow" "$binary" api smoke --batch "$smoke_batch" --pretty

if [[ "${API_VALIDATE_SKIP_FAILURE:-0}" != "1" ]]; then
	step "failure simulation assertions" "$binary" api sites simulate-failure \
		--batch "$failure_batch" \
		--count "$failure_count" \
		--create-missing \
		--mode "$failure_mode" \
		--wait "$failure_wait" \
		--expect-event-state "Seems Down" \
		--expect-event-severity 3 \
		--require-transition \
		--expect-transition-reason opened \
		--pretty
fi
