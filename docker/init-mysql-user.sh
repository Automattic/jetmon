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

MYSQL_PWD="${MYSQL_ROOT_PASSWORD}" mysql \
	--protocol=tcp \
	--host=mysqldb \
	--user=root <<SQL
CREATE DATABASE IF NOT EXISTS ${db_name};
CREATE USER IF NOT EXISTS ${app_user}@'%' IDENTIFIED BY ${app_password};
ALTER USER ${app_user}@'%' IDENTIFIED BY ${app_password};
GRANT ALL PRIVILEGES ON ${db_name}.* TO ${app_user}@'%';
FLUSH PRIVILEGES;
SQL

echo "mysql: ensured ${MYSQL_USER}@% can access ${MYSQL_DATABASE}"
