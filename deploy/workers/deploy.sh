#!/usr/bin/env bash
# Streamlined Cloudflare Workers + Containers deploy for the veriflier.
#
# Steps:
#   1. docker pull ghcr.io/automattic/veriflier:$SRC_TAG  (defaults to :latest, public GHCR)
#   2. Compute a CF-acceptable image tag from the local image digest (CF rejects :latest)
#   3. docker tag + wrangler containers push  →  registry.cloudflare.com/<account>/veriflier:<digest12>
#   4. Rewrite the `image = ...` line in wrangler.toml in-place with the new URI
#   5. wrangler deploy  (top-level env by default; --env staging via DEPLOY_ENV=staging)
#
# Required: docker daemon running, `wrangler login` already done.
# Env overrides:
#   SRC_TAG=<tag>            Source tag on ghcr.io/automattic/veriflier (default: latest)
#   CF_ACCOUNT_ID=<id>       CF account ID (default: parsed from `wrangler whoami`)
#   DEPLOY_ENV=<env>         "staging" or "" (default: "" — top-level / prod)
#   SKIP_DEPLOY=1            Push image and update wrangler.toml but don't deploy

set -euo pipefail

SRC_TAG="${SRC_TAG:-latest}"
DEPLOY_ENV="${DEPLOY_ENV:-}"
SKIP_DEPLOY="${SKIP_DEPLOY:-0}"

SRC_IMAGE="ghcr.io/automattic/veriflier:${SRC_TAG}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WRANGLER_TOML="${REPO_ROOT}/wrangler.toml"

if [[ ! -f "${WRANGLER_TOML}" ]]; then
    echo "error: ${WRANGLER_TOML} not found" >&2
    exit 1
fi

if ! docker info >/dev/null 2>&1; then
    echo "error: docker daemon not reachable. Start Docker Desktop and retry." >&2
    exit 1
fi

if [[ -z "${CF_ACCOUNT_ID:-}" ]]; then
    CF_ACCOUNT_ID=$(npx --no-install wrangler whoami 2>/dev/null \
        | awk -F'│' '/[0-9a-f]{32}/ { gsub(/[[:space:]]/, "", $3); print $3; exit }')
fi

if [[ -z "${CF_ACCOUNT_ID:-}" || ! "${CF_ACCOUNT_ID}" =~ ^[0-9a-f]{32}$ ]]; then
    echo "error: could not determine CF_ACCOUNT_ID. Run 'wrangler whoami' or set CF_ACCOUNT_ID manually." >&2
    exit 1
fi

echo "==> Pulling ${SRC_IMAGE}"
docker pull --platform linux/amd64 "${SRC_IMAGE}" >&2

DIGEST_FULL=$(docker inspect --format '{{index .RepoDigests 0}}' "${SRC_IMAGE}")
DIGEST_SHORT=$(echo "${DIGEST_FULL}" | sed 's/.*@sha256://' | cut -c1-12)

if [[ -z "${DIGEST_SHORT}" ]]; then
    echo "error: could not compute digest for ${SRC_IMAGE}" >&2
    exit 1
fi

CF_IMAGE="registry.cloudflare.com/${CF_ACCOUNT_ID}/veriflier:${DIGEST_SHORT}"

echo "==> Tagging as ${CF_IMAGE}"
docker tag "${SRC_IMAGE}" "${CF_IMAGE}"

echo "==> Pushing to Cloudflare managed registry"
(cd "${REPO_ROOT}" && npx --no-install wrangler containers push "${CF_IMAGE}") >&2

echo "==> Updating wrangler.toml image line"
tmp=$(mktemp)
awk -v new_image="${CF_IMAGE}" '
    /^image = "registry\.cloudflare\.com\// {
        print "image = \"" new_image "\""
        replaced = 1
        next
    }
    /^image = "ghcr\.io\// {
        print "image = \"" new_image "\""
        replaced = 1
        next
    }
    { print }
    END {
        if (!replaced) {
            print "deploy.sh: warning — no image line replaced; check wrangler.toml" > "/dev/stderr"
            exit 2
        }
    }
' "${WRANGLER_TOML}" > "${tmp}"
mv "${tmp}" "${WRANGLER_TOML}"

echo "    image = \"${CF_IMAGE}\""

if [[ "${SKIP_DEPLOY}" == "1" ]]; then
    echo "==> SKIP_DEPLOY=1 — wrangler.toml updated, deploy skipped"
    exit 0
fi

echo "==> wrangler deploy${DEPLOY_ENV:+ --env ${DEPLOY_ENV}}"
deploy_args=()
if [[ -n "${DEPLOY_ENV}" ]]; then
    deploy_args+=(--env "${DEPLOY_ENV}")
else
    deploy_args+=(--env "")
fi

(cd "${REPO_ROOT}" && npx --no-install wrangler deploy "${deploy_args[@]}")
