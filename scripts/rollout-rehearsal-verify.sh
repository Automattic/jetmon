#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

jetmon_binary="${ROLLOUT_REHEARSAL_JETMON2:-./bin/jetmon2}"
work_dir="${ROLLOUT_REHEARSAL_WORK_DIR:-/tmp/jetmon-rollout-rehearsal-$$}"
plan_file="${ROLLOUT_REHEARSAL_PLAN_FILE:-$work_dir/ranges.csv}"

step() {
	printf '\n== %s ==\n' "$1"
}

fail() {
	printf 'FAIL %s\n' "$1" >&2
	exit 1
}

require_contains() {
	local haystack="$1"
	local needle="$2"
	local message="$3"
	if ! grep -Fq -- "$needle" <<<"$haystack"; then
		fail "$message"
	fi
}

require_absent() {
	local haystack="$1"
	local needle="$2"
	local message="$3"
	if grep -Fq -- "$needle" <<<"$haystack"; then
		fail "$message"
	fi
}

mkdir -p "$work_dir"
printf '%s\n' \
	'host,bucket_min,bucket_max' \
	'jetmon-v1-a,0,4' \
	'jetmon-v1-b,5,9' \
	>"$plan_file"

step "same-server rehearsal plan"
same_plan="$("$jetmon_binary" rollout rehearsal-plan \
	--file "$plan_file" \
	--bucket-total 10 \
	--host jetmon-v1-a \
	--runtime-host jetmon-v1-a \
	--bucket-min 0 \
	--bucket-max 4 \
	--mode same-server \
	--v1-stop-command 'systemctl stop jetmon' \
	--v1-start-command 'systemctl start jetmon')"
printf '%s\n' "$same_plan"
require_contains "$same_plan" 'INFO mode=same-server' "same-server plan omitted mode"
require_contains "$same_plan" 'INFO plan_host="jetmon-v1-a" runtime_host="jetmon-v1-a" range=0-4' "same-server plan omitted host/range context"
require_contains "$same_plan" 'same DB_* environment used by the jetmon2 service' "same-server plan omitted service environment reminder"
require_contains "$same_plan" './jetmon2 rollout host-preflight' "same-server plan omitted host-preflight"
require_contains "$same_plan" 'systemctl stop jetmon' "same-server plan omitted v1 stop command"
require_contains "$same_plan" 'systemctl start jetmon' "same-server plan omitted v1 start command"
require_contains "$same_plan" './jetmon2 rollout cutover-check --host jetmon-v1-a --bucket-min 0 --bucket-max 4 --since 15m --require-all' "same-server plan omitted full-round cutover gate"
require_contains "$same_plan" './jetmon2 rollout dynamic-check' "same-server plan omitted fleet dynamic gate"
require_absent "$same_plan" 'Fresh-server mode requires' "same-server plan printed fresh-server SSH warning"

step "fresh-server rehearsal plan"
fresh_plan="$("$jetmon_binary" rollout rehearsal-plan \
	--file "$plan_file" \
	--bucket-total 10 \
	--host jetmon-v1-a \
	--runtime-host jetmon-v2-a \
	--bucket-min 0 \
	--bucket-max 4 \
	--mode fresh-server \
	--v1-stop-command 'ssh jetmon-v1-a sudo systemctl stop jetmon' \
	--v1-start-command 'ssh jetmon-v1-a sudo systemctl start jetmon')"
printf '%s\n' "$fresh_plan"
require_contains "$fresh_plan" 'INFO mode=fresh-server' "fresh-server plan omitted mode"
require_contains "$fresh_plan" 'INFO plan_host="jetmon-v1-a" runtime_host="jetmon-v2-a" range=0-4' "fresh-server plan omitted old/new host context"
require_contains "$fresh_plan" 'Fresh-server mode requires jetmon-v2-a to have SSH access to old v1 host jetmon-v1-a' "fresh-server plan omitted SSH access warning"
require_contains "$fresh_plan" 'HOLD: keep v2 stopped on the fresh server until the old v1 monitor process is stopped.' "fresh-server plan omitted v2 hold point"
require_contains "$fresh_plan" 'ssh jetmon-v1-a sudo systemctl stop jetmon' "fresh-server plan omitted remote v1 stop command"
require_contains "$fresh_plan" './jetmon2 rollout cutover-check --host jetmon-v2-a --bucket-min 0 --bucket-max 4 --since 15m --require-all' "fresh-server plan omitted runtime-host cutover gate"
require_contains "$fresh_plan" './jetmon2 rollout rollback-check --host jetmon-v2-a --bucket-min 0 --bucket-max 4' "fresh-server plan omitted runtime-host rollback gate"
require_contains "$fresh_plan" 'ssh jetmon-v1-a sudo systemctl start jetmon' "fresh-server plan omitted remote v1 rollback command"

step "same-server guided dry-run"
same_guided="$("$jetmon_binary" rollout guided \
	--dry-run \
	--file "$plan_file" \
	--bucket-total 10 \
	--host jetmon-v1-a \
	--runtime-host jetmon-v1-a \
	--bucket-min 0 \
	--bucket-max 4 \
	--mode same-server \
	--v1-stop-command 'systemctl stop jetmon' \
	--v1-start-command 'systemctl start jetmon' \
	--log-dir "$work_dir/guided-same")"
printf '%s\n' "$same_guided"
require_contains "$same_guided" 'PASS rollout_log_dir_writable=' "same-server guided dry-run omitted log-dir write check"
require_contains "$same_guided" 'INFO guided_run_origin=runtime_host mode="same-server" v1_host="jetmon-v1-a" runtime_host="jetmon-v1-a"' "same-server guided dry-run omitted run origin"
require_contains "$same_guided" 'INFO remote_v1_access_required=false reason=same_server' "same-server guided dry-run omitted same-server remote access note"
require_contains "$same_guided" 'PLAN path=FORWARD step=stop-v1 typed_confirmation="STOP jetmon-v1-a 0-4"' "same-server guided dry-run omitted v1 stop confirmation"
require_contains "$same_guided" 'PLAN path=FORWARD step=start-v2 typed_confirmation="START V2 jetmon-v1-a 0-4"' "same-server guided dry-run omitted v2 start confirmation"
require_contains "$same_guided" 'PLAN path=ROLLBACK step=rollback-start-v1 typed_confirmation="START V1 jetmon-v1-a 0-4"' "same-server guided dry-run omitted v1 rollback confirmation"

step "fresh-server guided dry-run"
fresh_guided="$("$jetmon_binary" rollout guided \
	--dry-run \
	--file "$plan_file" \
	--bucket-total 10 \
	--host jetmon-v1-a \
	--runtime-host jetmon-v2-a \
	--bucket-min 0 \
	--bucket-max 4 \
	--mode fresh-server \
	--v1-stop-command 'ssh jetmon-v1-a sudo systemctl stop jetmon' \
	--v1-start-command 'ssh jetmon-v1-a sudo systemctl start jetmon' \
	--log-dir "$work_dir/guided-fresh")"
printf '%s\n' "$fresh_guided"
require_contains "$fresh_guided" 'INFO guided_run_origin=runtime_host mode="fresh-server" v1_host="jetmon-v1-a" runtime_host="jetmon-v2-a"' "fresh-server guided dry-run omitted run origin"
require_contains "$fresh_guided" 'WARN remote_v1_access_required=true runtime_host="jetmon-v2-a" v1_host="jetmon-v1-a"' "fresh-server guided dry-run omitted remote access warning"
require_contains "$fresh_guided" 'PLAN path=FORWARD step=stop-v1 command="ssh jetmon-v1-a sudo systemctl stop jetmon"' "fresh-server guided dry-run omitted remote v1 stop command"
require_contains "$fresh_guided" 'PLAN path=FORWARD step=start-v2 typed_confirmation="START V2 jetmon-v2-a 0-4"' "fresh-server guided dry-run omitted runtime-host v2 start confirmation"
require_contains "$fresh_guided" 'PLAN path=ROLLBACK step=rollback-start-v1 command="ssh jetmon-v1-a sudo systemctl start jetmon"' "fresh-server guided dry-run omitted remote v1 rollback command"
require_contains "$fresh_guided" 'PLAN path=ROLLBACK step=rollback-start-v1 typed_confirmation="START V1 jetmon-v1-a 0-4"' "fresh-server guided dry-run omitted old-host v1 restart confirmation"

step "guided rollback dry-run"
rollback_guided="$("$jetmon_binary" rollout guided \
	--dry-run \
	--rollback \
	--file "$plan_file" \
	--bucket-total 10 \
	--host jetmon-v1-a \
	--runtime-host jetmon-v2-a \
	--bucket-min 0 \
	--bucket-max 4 \
	--mode fresh-server \
	--v1-start-command 'ssh jetmon-v1-a sudo systemctl start jetmon' \
	--log-dir "$work_dir/guided-rollback")"
printf '%s\n' "$rollback_guided"
require_contains "$rollback_guided" 'INFO selected_path=rollback' "rollback guided dry-run omitted rollback path marker"
require_contains "$rollback_guided" 'PLAN path=ROLLBACK step=rollback-stop-v2 command="systemctl stop jetmon2 && ! systemctl is-active --quiet jetmon2"' "rollback guided dry-run omitted v2 stop command"
require_contains "$rollback_guided" 'PLAN path=ROLLBACK step=rollback-check title="Run the rollback safety gate"' "rollback guided dry-run omitted rollback safety gate"
require_contains "$rollback_guided" 'PLAN path=ROLLBACK step=rollback-start-v1 command="ssh jetmon-v1-a sudo systemctl start jetmon"' "rollback guided dry-run omitted v1 start command"
require_absent "$rollback_guided" 'PLAN path=FORWARD' "rollback guided dry-run printed forward steps"

printf '\nrollout rehearsal verification passed\n'
