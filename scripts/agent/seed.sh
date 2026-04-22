#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCKER_DIR="${ROOT_DIR}/docker"
ENV_FILE="${DOCKER_DIR}/.env"

if [[ ! -f "${ENV_FILE}" ]]; then
  echo "Missing ${ENV_FILE}. Copy docker/.env-sample to docker/.env first." >&2
  exit 1
fi

MYSQL_PASSWORD="$(awk -F= '/^MYSQLDB_ROOT_PASSWORD=/{print $2}' "${ENV_FILE}" | tail -n 1)"
MYSQL_DATABASE="$(awk -F= '/^MYSQLDB_DATABASE=/{print $2}' "${ENV_FILE}" | tail -n 1)"

if [[ -z "${MYSQL_PASSWORD}" || -z "${MYSQL_DATABASE}" ]]; then
  echo "Missing MYSQLDB_ROOT_PASSWORD or MYSQLDB_DATABASE in ${ENV_FILE}" >&2
  exit 1
fi

docker_compose() {
  (cd "${DOCKER_DIR}" && docker compose "$@")
}

echo "Seeding jetpack_monitor_sites scenarios (idempotent)"

docker_compose exec -T mysqldb mysql -u root "-p${MYSQL_PASSWORD}" "${MYSQL_DATABASE}" <<'SQL'
SET @db := DATABASE();

SELECT COUNT(*) INTO @tbl_exists
FROM information_schema.tables
WHERE table_schema = @db AND table_name = 'jetpack_monitor_sites';

SET @ensure_stmt := IF(
  @tbl_exists = 0,
  'SELECT "missing jetpack_monitor_sites" AS error_message',
  'SELECT "jetpack_monitor_sites present" AS status_message'
);
PREPARE ensure_stmt FROM @ensure_stmt;
EXECUTE ensure_stmt;
DEALLOCATE PREPARE ensure_stmt;

SET @has_blog_id := (
  SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = @db AND table_name = 'jetpack_monitor_sites' AND column_name = 'blog_id'
);
SET @has_bucket_no := (
  SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = @db AND table_name = 'jetpack_monitor_sites' AND column_name = 'bucket_no'
);
SET @has_monitor_url := (
  SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = @db AND table_name = 'jetpack_monitor_sites' AND column_name = 'monitor_url'
);
SET @has_monitor_active := (
  SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = @db AND table_name = 'jetpack_monitor_sites' AND column_name = 'monitor_active'
);
SET @has_site_status := (
  SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = @db AND table_name = 'jetpack_monitor_sites' AND column_name = 'site_status'
);
SET @has_check_interval := (
  SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = @db AND table_name = 'jetpack_monitor_sites' AND column_name = 'check_interval'
);
SET @has_redirect_policy := (
  SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = @db AND table_name = 'jetpack_monitor_sites' AND column_name = 'redirect_policy'
);
SET @has_timeout_seconds := (
  SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = @db AND table_name = 'jetpack_monitor_sites' AND column_name = 'timeout_seconds'
);
SET @has_check_keyword := (
  SELECT COUNT(*) FROM information_schema.columns
  WHERE table_schema = @db AND table_name = 'jetpack_monitor_sites' AND column_name = 'check_keyword'
);

SET @required_ok := (@has_blog_id = 1 AND @has_bucket_no = 1 AND @has_monitor_url = 1);
SET @guard_stmt := IF(
  @tbl_exists = 0,
  'SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''missing required table jetpack_monitor_sites''',
  IF(
    @required_ok = 0,
    'SIGNAL SQLSTATE ''45000'' SET MESSAGE_TEXT = ''missing one or more required columns: blog_id, bucket_no, monitor_url''',
    'SELECT "required table/columns present" AS status_message'
  )
);
PREPARE guard_stmt_exec FROM @guard_stmt;
EXECUTE guard_stmt_exec;
DEALLOCATE PREPARE guard_stmt_exec;


CREATE TEMPORARY TABLE seed_sites (
  blog_id BIGINT UNSIGNED NOT NULL,
  bucket_no SMALLINT UNSIGNED NOT NULL,
  monitor_url VARCHAR(300) NOT NULL,
  monitor_active TINYINT UNSIGNED NOT NULL,
  site_status TINYINT UNSIGNED NOT NULL,
  check_interval TINYINT UNSIGNED NOT NULL,
  redirect_policy VARCHAR(10) NOT NULL,
  timeout_seconds TINYINT UNSIGNED NOT NULL,
  check_keyword VARCHAR(500) NULL
);

INSERT INTO seed_sites (
  blog_id,
  bucket_no,
  monitor_url,
  monitor_active,
  site_status,
  check_interval,
  redirect_policy,
  timeout_seconds,
  check_keyword
)
VALUES
  (910001, 0, 'https://httpstat.us/200', 1, 1, 5, 'follow', 10, NULL),
  (910002, 0, 'https://httpstat.us/500', 1, 1, 5, 'follow', 10, NULL),
  (910003, 0, 'https://httpstat.us/200?sleep=15000', 1, 1, 5, 'follow', 5, NULL),
  (910004, 0, 'https://httpstat.us/301', 1, 1, 5, 'alert', 10, NULL),
  (910005, 0, 'https://httpstat.us/200', 1, 1, 5, 'follow', 10, 'jetmon-keyword');

SET @update_sql := CONCAT(
  'UPDATE jetpack_monitor_sites t JOIN seed_sites s ON t.blog_id = s.blog_id SET ',
  't.bucket_no = s.bucket_no, t.monitor_url = s.monitor_url',
  IF(@has_monitor_active = 1, ', t.monitor_active = s.monitor_active', ''),
  IF(@has_site_status = 1, ', t.site_status = s.site_status', ''),
  IF(@has_check_interval = 1, ', t.check_interval = s.check_interval', ''),
  IF(@has_redirect_policy = 1, ', t.redirect_policy = s.redirect_policy', ''),
  IF(@has_timeout_seconds = 1, ', t.timeout_seconds = s.timeout_seconds', ''),
  IF(@has_check_keyword = 1, ', t.check_keyword = s.check_keyword', '')
);

PREPARE update_stmt FROM @update_sql;
EXECUTE update_stmt;
DEALLOCATE PREPARE update_stmt;

SET @insert_cols := 'blog_id, bucket_no, monitor_url';
SET @insert_vals := 's.blog_id, s.bucket_no, s.monitor_url';

SET @insert_cols := CONCAT(@insert_cols, IF(@has_monitor_active = 1, ', monitor_active', ''));
SET @insert_vals := CONCAT(@insert_vals, IF(@has_monitor_active = 1, ', s.monitor_active', ''));

SET @insert_cols := CONCAT(@insert_cols, IF(@has_site_status = 1, ', site_status', ''));
SET @insert_vals := CONCAT(@insert_vals, IF(@has_site_status = 1, ', s.site_status', ''));

SET @insert_cols := CONCAT(@insert_cols, IF(@has_check_interval = 1, ', check_interval', ''));
SET @insert_vals := CONCAT(@insert_vals, IF(@has_check_interval = 1, ', s.check_interval', ''));

SET @insert_cols := CONCAT(@insert_cols, IF(@has_redirect_policy = 1, ', redirect_policy', ''));
SET @insert_vals := CONCAT(@insert_vals, IF(@has_redirect_policy = 1, ', s.redirect_policy', ''));

SET @insert_cols := CONCAT(@insert_cols, IF(@has_timeout_seconds = 1, ', timeout_seconds', ''));
SET @insert_vals := CONCAT(@insert_vals, IF(@has_timeout_seconds = 1, ', s.timeout_seconds', ''));

SET @insert_cols := CONCAT(@insert_cols, IF(@has_check_keyword = 1, ', check_keyword', ''));
SET @insert_vals := CONCAT(@insert_vals, IF(@has_check_keyword = 1, ', s.check_keyword', ''));

SET @insert_sql := CONCAT(
  'INSERT INTO jetpack_monitor_sites (', @insert_cols, ') ',
  'SELECT ', @insert_vals, ' FROM seed_sites s ',
  'WHERE NOT EXISTS (',
    'SELECT 1 FROM jetpack_monitor_sites t WHERE t.blog_id = s.blog_id',
  ')'
);

PREPARE insert_stmt FROM @insert_sql;
EXECUTE insert_stmt;
DEALLOCATE PREPARE insert_stmt;

SELECT blog_id, monitor_url, bucket_no
FROM jetpack_monitor_sites
WHERE blog_id IN (910001, 910002, 910003, 910004, 910005)
ORDER BY blog_id;

DROP TEMPORARY TABLE seed_sites;
SQL

echo "Seed complete"
