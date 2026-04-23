#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCKER_DIR="${ROOT_DIR}/docker"

docker_compose() {
  (cd "${DOCKER_DIR}" && docker compose "$@")
}

echo "Resetting local Jetmon stack and volumes from ${DOCKER_DIR}"
docker_compose down -v
docker_compose up --build -d
docker_compose ps
