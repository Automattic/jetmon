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

require_log_indicator() {
  local service="$1"
  local pattern="$2"
  local lines="${3:-300}"
  if ! docker_compose logs --tail "${lines}" "${service}" | grep -E "${pattern}" >/dev/null; then
    echo "Assertion failed: expected log indicator not found for ${service}: ${pattern}" >&2
    docker_compose logs --tail "${lines}" "${service}" >&2
    exit 1
  fi
}

require_service_running() {
  local service="$1"
  if ! docker_compose ps --status running --services | grep -qx "${service}"; then
    echo "Assertion failed: service is not running: ${service}" >&2
    docker_compose ps >&2
    exit 1
  fi
}

require_service_healthy() {
  local service="$1"
  local container_id
  container_id="$(docker_compose ps -q "${service}")"

  if [[ -z "${container_id}" ]]; then
    echo "Assertion failed: no container found for service: ${service}" >&2
    docker_compose ps >&2
    exit 1
  fi

  local health_status
  health_status="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "${container_id}")"

  if [[ "${health_status}" != "healthy" ]]; then
    echo "Assertion failed: service is not healthy: ${service}" >&2
    docker_compose ps "${service}" >&2
    docker inspect --format '{{json .State.Health}}' "${container_id}" >&2 || true
    exit 1
  fi
}

echo "Asserting required services are running"
require_service_running mysqldb
require_service_running jetmon
require_service_running veriflier
require_service_running statsd

echo "Asserting required services are healthy"
require_service_healthy mysqldb
require_service_healthy veriflier

echo "Asserting log indicators are present"
require_log_indicator jetmon "orchestrator: starting|dashboard: listening|migrations applied successfully" 400
require_log_indicator veriflier "veriflier2 .* starting|veriflier: listening" 200

echo "Asserting seeded scenario rows exist"
seed_count="$(docker_compose exec -T mysqldb mysql -N -s -u root "-p${MYSQL_PASSWORD}" "${MYSQL_DATABASE}" -e "SELECT COUNT(*) FROM jetpack_monitor_sites WHERE blog_id IN (910001,910002,910003,910004,910005);")"

if [[ "${seed_count}" != "5" ]]; then
  echo "Assertion failed: expected 5 seeded rows, got ${seed_count}" >&2
  docker_compose exec -T mysqldb mysql -u root "-p${MYSQL_PASSWORD}" "${MYSQL_DATABASE}" -e "SELECT blog_id, monitor_url FROM jetpack_monitor_sites WHERE blog_id BETWEEN 910001 AND 910010 ORDER BY blog_id;" >&2 || true
  exit 1
fi

echo "Asserting expected monitor_url values"
url_mismatch_count="$(docker_compose exec -T mysqldb mysql -N -s -u root "-p${MYSQL_PASSWORD}" "${MYSQL_DATABASE}" -e "SELECT COUNT(*) FROM ( SELECT 910001 AS blog_id, 'https://httpstat.us/200' AS expected_url UNION ALL SELECT 910002, 'https://httpstat.us/500' UNION ALL SELECT 910003, 'https://httpstat.us/200?sleep=15000' UNION ALL SELECT 910004, 'https://httpstat.us/301' UNION ALL SELECT 910005, 'https://httpstat.us/200' ) e LEFT JOIN jetpack_monitor_sites s ON s.blog_id = e.blog_id WHERE s.monitor_url <> e.expected_url OR s.monitor_url IS NULL;")"

if [[ "${url_mismatch_count}" != "0" ]]; then
  echo "Assertion failed: seeded URLs do not match expected scenarios" >&2
  docker_compose exec -T mysqldb mysql -u root "-p${MYSQL_PASSWORD}" "${MYSQL_DATABASE}" -e "SELECT blog_id, monitor_url FROM jetpack_monitor_sites WHERE blog_id IN (910001,910002,910003,910004,910005) ORDER BY blog_id;" >&2
  exit 1
fi

echo "Asserting stats files exist"
for stats_file in sitespersec sitesqueue totals; do
  if [[ ! -f "${ROOT_DIR}/stats/${stats_file}" ]]; then
    echo "Assertion failed: missing stats file ${ROOT_DIR}/stats/${stats_file}" >&2
    exit 1
  fi
done

echo "Asserting seeded scenarios include 910005 keyword-miss URL"
seed_url_910005="$(docker_compose exec -T mysqldb mysql -N -s -u root "-p${MYSQL_PASSWORD}" "${MYSQL_DATABASE}" -e "SELECT monitor_url FROM jetpack_monitor_sites WHERE blog_id = 910005 LIMIT 1;")"
if [[ "${seed_url_910005}" != "https://httpstat.us/200" ]]; then
  echo "Assertion failed: expected blog_id 910005 URL https://httpstat.us/200, got ${seed_url_910005}" >&2
  exit 1
fi

echo "All assertions passed"
