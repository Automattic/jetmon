#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DOCKER_DIR="${ROOT_DIR}/docker"
ENV_FILE="${DOCKER_DIR}/.env"
ENV_SAMPLE_FILE="${DOCKER_DIR}/.env-sample"

docker_compose() {
  (cd "${DOCKER_DIR}" && docker compose "$@")
}

echo "Starting local Jetmon stack from ${DOCKER_DIR}"
if [[ ! -f "${ENV_FILE}" ]]; then
  if [[ -f "${ENV_SAMPLE_FILE}" ]]; then
    cp "${ENV_SAMPLE_FILE}" "${ENV_FILE}"
    echo "Created docker/.env from docker/.env-sample"
  else
    echo "Missing ${ENV_FILE} and ${ENV_SAMPLE_FILE}" >&2
    exit 1
  fi
fi

docker_compose up --build -d
docker_compose ps
