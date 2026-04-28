#!/usr/bin/env bash
set -euo pipefail

: "${MYSQL_ROOT_PASSWORD:?MYSQL_ROOT_PASSWORD is required}"
: "${MYSQL_DATABASE:?MYSQL_DATABASE is required}"
: "${MYSQL_USER:?MYSQL_USER is required}"
: "${MYSQL_PASSWORD:?MYSQL_PASSWORD is required}"

if [ "${MYSQL_USER}" = "root" ]; then
	echo "MYSQL_USER must be a non-root application user" >&2
	exit 1
fi

sql_string() {
	local value=$1
	value=${value//\\/\\\\}
	value=${value//\'/\\\'}
	printf "'%s'" "${value}"
}

sql_identifier() {
	local value=$1
	value=${value//\`/\`\`}
	printf '`%s`' "${value}"
}

db_name=$(sql_identifier "${MYSQL_DATABASE}")
app_user=$(sql_string "${MYSQL_USER}")
app_password=$(sql_string "${MYSQL_PASSWORD}")

mysql_root() {
	MYSQL_PWD="${MYSQL_ROOT_PASSWORD}" mysql \
		--protocol=tcp \
		--host=mysqldb \
		--user=root \
		--connect-timeout=2 \
		"$@"
}

attempt=1
max_attempts=${MYSQL_READY_ATTEMPTS:-60}
while ! mysql_root --execute="SELECT 1" >/dev/null 2>&1; do
	if [ "${attempt}" -ge "${max_attempts}" ]; then
		echo "mysql: could not connect to mysqldb:3306 after ${max_attempts} attempts" >&2
		exit 1
	fi
	echo "mysql: waiting for mysqldb:3306 to accept TCP connections (${attempt}/${max_attempts})" >&2
	attempt=$((attempt + 1))
	sleep 2
done

mysql_root <<SQL
CREATE DATABASE IF NOT EXISTS ${db_name};
CREATE USER IF NOT EXISTS ${app_user}@'%' IDENTIFIED BY ${app_password};
ALTER USER ${app_user}@'%' IDENTIFIED BY ${app_password};
GRANT ALL PRIVILEGES ON ${db_name}.* TO ${app_user}@'%';
FLUSH PRIVILEGES;
SQL

echo "mysql: ensured ${MYSQL_USER}@% can access ${MYSQL_DATABASE}"
