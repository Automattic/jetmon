#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCKER_DIR="${ROOT_DIR}/docker"
TAIL_COUNT="${LOG_TAIL:-120}"

docker_compose() {
  (cd "${DOCKER_DIR}" && docker compose "$@")
}

if [[ "$#" -gt 0 ]]; then
  SERVICES=("$@")
else
  SERVICES=(jetmon veriflier mysqldb)
fi

echo "Following logs (tail=${TAIL_COUNT}) for services: ${SERVICES[*]}"
docker_compose logs -f --tail "${TAIL_COUNT}" "${SERVICES[@]}"
