#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCKER_DIR="${ROOT_DIR}/docker"

docker_compose() {
  (cd "${DOCKER_DIR}" && docker compose "$@")
}

TARGET="${1:-both}"

case "${TARGET}" in
  jetmon)
    SERVICES=(jetmon)
    ;;
  veriflier)
    SERVICES=(veriflier)
    ;;
  both)
    SERVICES=(jetmon veriflier)
    ;;
  *)
    echo "Usage: $0 [jetmon|veriflier|both]" >&2
    exit 1
    ;;
esac

echo "Rebuilding and starting services (${TARGET}) from ${DOCKER_DIR}: ${SERVICES[*]}"
docker_compose up --build -d "${SERVICES[@]}"
docker_compose ps
