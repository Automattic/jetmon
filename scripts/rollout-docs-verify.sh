#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

jetmon_binary="${ROLLOUT_DOCS_JETMON2:-./bin/jetmon2}"
deliverer_binary="${ROLLOUT_DOCS_DELIVERER:-./bin/jetmon-deliverer}"

step() {
	printf '\n== %s ==\n' "$1"
}

fail() {
	printf 'FAIL %s\n' "$1" >&2
	exit 1
}

run_help_check() {
	local output status
	set +e
	output="$("$@" 2>&1)"
	status=$?
	set -e
	printf '%s\n' "$output"
	if [[ "$status" -ne 0 && "$status" -ne 2 ]]; then
		fail "help command failed with status $status: $*"
	fi
	if ! grep -qi 'usage' <<<"$output"; then
		fail "help output did not contain usage: $*"
	fi
}

step "stale rollout docs scan"
stale_pattern='Status: active on|after the API CLI work|rollout preflight hardening settle|Planned Simulated|simulated site server remains a planned|systemd-analyze verify systemd/|API.md|ROADMAP.md|PROJECT.md|TAXONOMY.md|EVENTS.md'
if rg -n "$stale_pattern" README.md AGENTS.md docs config systemd; then
	fail "stale documentation references found"
fi

step "diff whitespace"
git diff --check

step "rollout command help"
run_help_check "$jetmon_binary" rollout rehearsal-plan --help
run_help_check "$jetmon_binary" rollout static-plan-check --help
run_help_check "$jetmon_binary" rollout pinned-check --help
run_help_check "$jetmon_binary" rollout cutover-check --help
run_help_check "$jetmon_binary" rollout rollback-check --help
run_help_check "$jetmon_binary" rollout dynamic-check --help
run_help_check "$jetmon_binary" rollout activity-check --help
run_help_check "$jetmon_binary" rollout projection-drift --help
run_help_check "$jetmon_binary" rollout state-report --help

step "deliverer command help"
run_help_check "$deliverer_binary" validate-config --help
run_help_check "$deliverer_binary" delivery-check --help

step "rehearsal-plan smoke"
plan_file="${ROLLOUT_DOCS_PLAN_FILE:-/tmp/jetmon-rollout-docs-verify-buckets.csv}"
printf '%s\n' \
	'host,bucket_min,bucket_max' \
	'jetmon-v1-a,0,4' \
	'jetmon-v1-b,5,9' \
	>"$plan_file"
plan_output="$("$jetmon_binary" rollout rehearsal-plan \
	--file "$plan_file" \
	--bucket-total 10 \
	--host jetmon-v1-a \
	--bucket-min 0 \
	--bucket-max 4 \
	--mode same-server)"
printf '%s\n' "$plan_output"
grep -q 'rollout static-plan-check' <<<"$plan_output" || fail "rehearsal plan omitted static-plan-check"
grep -q 'rollout pinned-check' <<<"$plan_output" || fail "rehearsal plan omitted pinned-check"
grep -q 'rollout cutover-check' <<<"$plan_output" || fail "rehearsal plan omitted cutover-check"
grep -q 'rollout rollback-check' <<<"$plan_output" || fail "rehearsal plan omitted rollback-check"
grep -q 'rollout dynamic-check' <<<"$plan_output" || fail "rehearsal plan omitted dynamic-check"

step "rollout json smoke"
json_output="$("$jetmon_binary" rollout static-plan-check \
	--file "$plan_file" \
	--bucket-total 10 \
	--output=json)"
printf '%s\n' "$json_output"
grep -q '"ok": true' <<<"$json_output" || fail "static-plan-check JSON did not report ok=true"
grep -q '"command": "rollout static-plan-check"' <<<"$json_output" || fail "static-plan-check JSON omitted command name"

step "staged systemd verify"
if ! command -v systemd-analyze >/dev/null 2>&1; then
	printf 'WARN systemd-analyze not found; skipping service-unit verification\n'
	exit 0
fi

staged_root="${ROLLOUT_DOCS_SYSTEMD_ROOT:-/tmp/jetmon-rollout-docs-verify-root}"
mkdir -p \
	"$staged_root/opt/jetmon2/bin" \
	"$staged_root/etc/systemd/system" \
	"$staged_root/bin" \
	"$staged_root/usr/lib/systemd/system"
cp "$jetmon_binary" "$staged_root/opt/jetmon2/jetmon2"
cp "$deliverer_binary" "$staged_root/opt/jetmon2/bin/jetmon-deliverer"
cp systemd/jetmon2.service "$staged_root/etc/systemd/system/jetmon2.service"
cp systemd/jetmon-deliverer.service "$staged_root/etc/systemd/system/jetmon-deliverer.service"

if [[ -x /bin/kill ]]; then
	cp /bin/kill "$staged_root/bin/kill"
else
	printf 'WARN /bin/kill not found; skipping service-unit verification\n'
	exit 0
fi

if [[ -f /usr/lib/systemd/system/sysinit.target ]]; then
	cp /usr/lib/systemd/system/sysinit.target "$staged_root/usr/lib/systemd/system/sysinit.target"
elif [[ -f /lib/systemd/system/sysinit.target ]]; then
	cp /lib/systemd/system/sysinit.target "$staged_root/usr/lib/systemd/system/sysinit.target"
else
	printf 'WARN sysinit.target not found; skipping service-unit verification\n'
	exit 0
fi

set +e
systemd_output="$(systemd-analyze --root="$staged_root" verify /etc/systemd/system/jetmon2.service /etc/systemd/system/jetmon-deliverer.service 2>&1)"
systemd_status=$?
set -e
printf '%s\n' "$systemd_output"
if [[ "$systemd_status" -ne 0 ]]; then
	if grep -qiE 'SO_PASSCRED|Operation not permitted' <<<"$systemd_output"; then
		printf 'WARN systemd-analyze was blocked by the local sandbox; rerun on an unrestricted host for service-unit verification\n'
	else
		exit "$systemd_status"
	fi
fi

printf '\nrollout docs verification passed\n'
