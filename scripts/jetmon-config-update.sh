#!/usr/bin/env bash
set -euo pipefail

# Host-side production helper for syncing the Jetmon database server map from
# SVN without baking credentials or the generated file into the Docker image.

env_file="${JETMON_CONFIG_SYNC_ENV:-/etc/jetmon/config-sync.env}"

if [[ ! -f "$env_file" ]]; then
	echo "missing config sync env file: $env_file" >&2
	exit 1
fi

# shellcheck source=/dev/null
. "$env_file"

required_vars=(
	SVNUSER
	SVNPASS
	JETMON_CONFIG_SYNC_DEST_DIR
)

for var in "${required_vars[@]}"; do
	if [[ -z "${!var:-}" ]]; then
		echo "missing required config sync setting: $var" >&2
		exit 1
	fi
done

if [[ -z "${SVN_URL:-}" ]]; then
	if [[ -z "${SVNREPO:-}" || -z "${SVNPATH:-}" ]]; then
		echo "set SVN_URL or both SVNREPO and SVNPATH in $env_file" >&2
		exit 1
	fi
	SVN_URL="${SVNREPO%/}/${SVNPATH#/}"
fi

if ! command -v svn >/dev/null 2>&1; then
	echo "svn command not found" >&2
	exit 1
fi

source_file="${JETMON_CONFIG_SYNC_SOURCE_FILE:-db-servers.php}"
dest_file="$(basename "$source_file")"
work_dir="${JETMON_CONFIG_SYNC_WORK_DIR:-/var/lib/jetmon/config-svn}"
dest_dir="$JETMON_CONFIG_SYNC_DEST_DIR"
dest_path="${dest_dir%/}/$dest_file"
if [[ "${JETMON_CONFIG_SYNC_OWNER+x}" == "x" ]]; then
	owner="$JETMON_CONFIG_SYNC_OWNER"
else
	owner="jetmon"
fi
if [[ "${JETMON_CONFIG_SYNC_GROUP+x}" == "x" ]]; then
	group="$JETMON_CONFIG_SYNC_GROUP"
else
	group="jetmon"
fi
mode="${JETMON_CONFIG_SYNC_MODE:-0640}"

mkdir -p "$work_dir" "$dest_dir"

if [[ -d "$work_dir/.svn" ]]; then
	svn update --non-interactive --no-auth-cache \
		--username "$SVNUSER" --password "$SVNPASS" \
		"$work_dir" >/dev/null
else
	svn checkout --non-interactive --no-auth-cache \
		--username "$SVNUSER" --password "$SVNPASS" \
		"$SVN_URL" "$work_dir" >/dev/null
fi

source_path="${work_dir%/}/$source_file"
if [[ ! -f "$source_path" ]]; then
	echo "synced SVN tree does not contain $source_file" >&2
	exit 1
fi

tmp_path="$(mktemp "${dest_dir%/}/.${dest_file}.tmp.XXXXXX")"
cleanup() {
	rm -f "$tmp_path"
}
trap cleanup EXIT

install -m "$mode" "$source_path" "$tmp_path"
if [[ -n "$owner" || -n "$group" ]]; then
	chown "${owner:-}:${group:-}" "$tmp_path"
fi

if [[ -f "$dest_path" ]] && cmp -s "$tmp_path" "$dest_path"; then
	rm -f "$tmp_path"
	trap - EXIT
	echo "unchanged $dest_path"
	exit 0
fi

mv -f "$tmp_path" "$dest_path"
trap - EXIT
echo "updated $dest_path"
