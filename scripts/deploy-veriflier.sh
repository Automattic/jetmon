#!/usr/bin/env bash
#
# deploy-veriflier.sh — Operator script for deploying Jetmon Veriflier v2 to
# DigitalOcean VPS hosts. Supports bootstrapping a fresh host and updating an
# existing host with a new image tag.
#
# Policy constraints (non-negotiable):
#   - No Monitor/Veriflier co-location
#   - Monitor routes to direct Veriflier endpoints (no LB)
#   - Static VERIFIERS is the control plane
#   - Veriflier deploys are manual and operator-driven
#   - Always targets the v2 branch (v1/master is incompatible)
#
# See docs/veriflier-deploy.md (or VERIFLIER_DEPLOY.md) for usage.

set -euo pipefail

JETMON_REPO_URL="${JETMON_REPO_URL:-https://github.com/Automattic/jetmon.git}"
JETMON_BRANCH="${JETMON_BRANCH:-v2}"
VERIFLIER_IMAGE_REPO="${VERIFLIER_IMAGE_REPO:-ghcr.io/automattic/veriflier}"
VERIFLIER_PORT="${VERIFLIER_PORT:-7803}"

usage() {
	cat <<EOF
Usage: $(basename "$0") <command> [OPTIONS] [ARGS]

Commands:
  bootstrap <host> [OPTIONS]  Initialize .env and clone repo on VPS
  deploy <host> <image-tag>   Deploy a new image to existing host

Bootstrap options:
  -u USER                SSH user (default: root)
  -p PORT                SSH port (default: 22)
  -v VANTAGE_ID          Veriflier vantage ID (e.g., do-<region>-1)
  -r REGION              Veriflier region (e.g., <region>)
  -t AUTH_TOKEN          Veriflier auth token
  -n JETMON_HOSTNAME     Process identity hostname (e.g., <region>.do-<region>-1)
  --dry-run              Show what would be done without executing

Deploy options:
  -u USER                SSH user (default: root)
  -p PORT                SSH port (default: 22)
  --dry-run              Show what would be done without executing

Environment overrides:
  JETMON_REPO_URL        Override jetmon repo URL (default: public Automattic/jetmon)
  JETMON_BRANCH          Override branch (default: v2)
  VERIFLIER_IMAGE_REPO   Override image repository (default: ghcr.io/automattic/veriflier)
  VERIFLIER_PORT         Override Veriflier port (default: 7803)

Examples:
  $(basename "$0") bootstrap <vps-host-or-ip> \\
    -v do-<region>-1 -r <region> -t <auth-token> -n <region>.do-<region>-1

  $(basename "$0") deploy <vps-host-or-ip> <YYYY-MM-DD-sha-xxxxxxx>

EOF
	exit "${1:-0}"
}

log() {
	echo "[$(date +'%Y-%m-%d %H:%M:%S')] $*" >&2
}

success() {
	echo "✓ $*" >&2
}

error() {
	echo "✗ $*" >&2
	exit 1
}

warn() {
	echo "⚠ $*" >&2
}

ssh_retry() {
	local cmd="$1"
	local max_attempts=3
	local attempt=1
	local wait_time=2

	while [[ $attempt -le $max_attempts ]]; do
		if eval "$cmd" 2>/dev/null; then
			return 0
		fi
		if [[ $attempt -lt $max_attempts ]]; then
			log "SSH attempt $attempt failed, retrying in ${wait_time}s..."
			sleep $wait_time
			wait_time=$((wait_time * 2))
		fi
		attempt=$((attempt + 1))
	done

	return 1
}

bootstrap_veriflier() {
	local HOST="$1"

	if [[ -z "$VANTAGE_ID" || -z "$REGION" || -z "$AUTH_TOKEN" ]]; then
		error "bootstrap requires -v VANTAGE_ID, -r REGION, and -t AUTH_TOKEN"
	fi

	local SSH_CMD="ssh -o ConnectTimeout=10 -o BatchMode=yes -p ${SSH_PORT} ${SSH_USER}@${HOST}"

	log "Testing SSH connectivity to ${HOST}..."
	if ! ssh_retry "$SSH_CMD true"; then
		error "SSH connection failed to ${HOST}:${SSH_PORT} as ${SSH_USER} after retries"
	fi
	success "SSH connection successful"

	log "Setting up jetmon ${JETMON_BRANCH} repository and configuration..."

	# Batch all setup commands into a single SSH session
	local SETUP_SCRIPT="set -euo pipefail

if test -d jetmon/.git; then
	cd jetmon && git fetch origin ${JETMON_BRANCH} && git checkout ${JETMON_BRANCH} && cd ..
else
	git clone --branch ${JETMON_BRANCH} ${JETMON_REPO_URL}
fi

CURRENT_BRANCH=\$(cd jetmon && git rev-parse --abbrev-ref HEAD)
if [ \"\$CURRENT_BRANCH\" != \"${JETMON_BRANCH}\" ]; then
	echo \"ERROR: Not on ${JETMON_BRANCH} branch. Current: \$CURRENT_BRANCH\" >&2
	exit 1
fi

if [ ! -f jetmon/docker/docker-compose.veriflier-prod.yml ]; then
	echo \"ERROR: docker-compose.veriflier-prod.yml not found\" >&2
	exit 1
fi

echo \"branch_ok\"
"

	local SETUP_RESULT
	SETUP_RESULT=$($SSH_CMD "$SETUP_SCRIPT" 2>&1) || error "Failed to set up ${JETMON_BRANCH} repository: $SETUP_RESULT"

	if [[ "$SETUP_RESULT" == *"branch_ok"* ]]; then
		success "Repository set up on ${JETMON_BRANCH} branch"
	else
		error "${JETMON_BRANCH} setup verification failed: $SETUP_RESULT"
	fi

	log "Creating .env file..."
	local ENV_CONTENT="VERIFLIER_IMAGE=${VERIFLIER_IMAGE_REPO}:latest
VERIFLIER_AUTH_TOKEN=${AUTH_TOKEN}
VERIFLIER_VANTAGE_ID=${VANTAGE_ID}
VERIFLIER_REGION=${REGION}
VERIFLIER_PROVIDER=vps
VERIFLIER_BIND_ADDR=0.0.0.0
VERIFLIER_HOST_PORT=${VERIFLIER_PORT}
JETMON_HOSTNAME=${JETMON_HOSTNAME}
STATSD_HOST_PATH=
GRAPHITE_BIND_ADDR=127.0.0.1
GRAPHITE_HOST_PORT=8088
STATSD_GRAPHITE_IMAGE=graphiteapp/graphite-statsd
"

	$SSH_CMD "cat > jetmon/docker/.env" <<< "$ENV_CONTENT" || \
		error "Failed to write .env file"
	success ".env file created"

	log "Verifying docker-compose configuration..."
	$SSH_CMD 'cd jetmon/docker && docker compose -f docker-compose.veriflier-prod.yml config > /dev/null 2>&1' || \
		error "docker-compose configuration is invalid"
	success "Configuration is valid"

	cat >&2 <<EOF

========================================
✓ Bootstrap Complete
========================================

Host:          ${HOST}
Vantage ID:    ${VANTAGE_ID}
Region:        ${REGION}
Hostname:      ${JETMON_HOSTNAME}

Next steps:
1. Deploy initial image:
   $(basename "$0") deploy ${HOST} <image-tag>

2. Verify on Monitor side per deployment checklist

EOF
}

deploy_veriflier_image() {
	local HOST="$1"
	local IMAGE_TAG="$2"

	if [[ -z "$HOST" || -z "$IMAGE_TAG" ]]; then
		usage 1
	fi

	local SSH_CMD="ssh -o ConnectTimeout=10 -o BatchMode=yes -p ${SSH_PORT} ${SSH_USER}@${HOST}"

	log "Testing SSH connectivity to ${HOST}..."
	if ! ssh_retry "$SSH_CMD true"; then
		error "SSH connection failed to ${HOST}:${SSH_PORT} as ${SSH_USER} after retries"
	fi
	success "SSH connection successful"

	log "Checking current .env configuration..."
	local CURRENT_TAG=""
	local SSH_OUTPUT=""
	local SSH_EXIT=0

	local attempt=1
	while [[ $attempt -le 3 ]]; do
		SSH_OUTPUT=$($SSH_CMD 'cd jetmon/docker && grep "^VERIFLIER_IMAGE=" .env | sed "s/.*://"' 2>&1) && SSH_EXIT=0 || SSH_EXIT=$?
		if [[ $SSH_EXIT -eq 0 && -n "$SSH_OUTPUT" ]]; then
			CURRENT_TAG="$SSH_OUTPUT"
			break
		fi
		if [[ $attempt -lt 3 ]]; then
			log "Read attempt $attempt failed (exit=$SSH_EXIT), retrying..."
			sleep 2
		fi
		attempt=$((attempt + 1))
	done

	if [[ -z "$CURRENT_TAG" ]]; then
		error "Could not read VERIFLIER_IMAGE from .env on ${HOST}. SSH output: $SSH_OUTPUT (exit=$SSH_EXIT)"
	fi

	if [[ "$CURRENT_TAG" == "$IMAGE_TAG" ]]; then
		warn "Host is already running image tag: ${IMAGE_TAG}"
	else
		log "Current image tag: ${CURRENT_TAG}"
		log "Target image tag:  ${IMAGE_TAG}"
	fi

	log "Updating .env and deploying containers..."

	if [[ $DRY_RUN -eq 1 ]]; then
		cat >&2 <<DRYRUN
[DRY RUN] Commands that would be executed:
  ssh -p ${SSH_PORT} ${SSH_USER}@${HOST}
  cd jetmon/docker && \\
    sed -i.bak 's|^VERIFLIER_IMAGE=.*|VERIFLIER_IMAGE=${VERIFLIER_IMAGE_REPO}:${IMAGE_TAG}|' .env && \\
    docker compose -f docker-compose.veriflier-prod.yml pull && \\
    docker compose -f docker-compose.veriflier-prod.yml up -d && \\
    docker compose -f docker-compose.veriflier-prod.yml ps
DRYRUN
		success "Dry run completed"
		return
	fi

	$SSH_CMD "cd jetmon/docker && sed -i.bak 's|^VERIFLIER_IMAGE=.*|VERIFLIER_IMAGE=${VERIFLIER_IMAGE_REPO}:${IMAGE_TAG}|' .env && \
		docker compose -f docker-compose.veriflier-prod.yml pull && \
		docker compose -f docker-compose.veriflier-prod.yml up -d && \
		docker compose -f docker-compose.veriflier-prod.yml ps" > /tmp/deploy_$$.log 2>&1 || {
		cat /tmp/deploy_$$.log >&2
		rm -f /tmp/deploy_$$.log
		error "Failed to update .env or deploy containers"
	}

	cat /tmp/deploy_$$.log >&2

	if ! grep -q "veriflier.*Up" /tmp/deploy_$$.log; then
		rm -f /tmp/deploy_$$.log
		error "Veriflier container is not running. Check logs: ssh ${SSH_USER}@${HOST} 'cd jetmon/docker && docker compose logs -f veriflier'"
	fi
	rm -f /tmp/deploy_$$.log
	success "Veriflier container is running"

	log "Waiting for Veriflier health endpoint to respond..."
	local HEALTH_CHECK=""
	attempt=1
	while [[ $attempt -le 6 ]]; do
		HEALTH_CHECK=$($SSH_CMD "curl -fsS http://127.0.0.1:${VERIFLIER_PORT}/v2/status" 2>/dev/null || echo "")
		if [[ -n "$HEALTH_CHECK" ]]; then
			break
		fi
		log "Health check attempt $attempt/6 not ready, waiting 5s..."
		sleep 5
		attempt=$((attempt + 1))
	done

	if [[ -z "$HEALTH_CHECK" ]]; then
		error "Health check failed on ${HOST} after ${attempt} attempts. Check logs: ssh ${SSH_USER}@${HOST} 'cd jetmon/docker && docker compose logs veriflier'"
	fi
	success "Veriflier health check passed"

	# Extract vantage ID from response (scoped to the vantage.id field, not agent.id)
	local VANTAGE_ID
	VANTAGE_ID=$(echo "$HEALTH_CHECK" | sed -n 's/.*"vantage":{[^}]*"id":"\([^"]*\)".*/\1/p')
	[[ -z "$VANTAGE_ID" ]] && VANTAGE_ID="unknown"

	cat >&2 <<EOF

========================================
✓ Deployment Complete
========================================

Host:       ${HOST}
Image Tag:  ${IMAGE_TAG}
Vantage ID: ${VANTAGE_ID}
Status:     ✓ Running and healthy

Next steps:
1. Update Monitor static VERIFIERS config (if needed)
2. Run Monitor validation from your Monitor environment:

   ./jetmon2 validate-config
   ./jetmon2 verifliers discovery-report --output=text
   ./jetmon2 telemetry report --since=15m

3. Confirm:
   - Config validates
   - Discovery shows endpoint: ${HOST}
   - Telemetry is healthy for recent dispatch/probes

Rollback:
  If validation fails, restore the prior image tag and redeploy:
  $(basename "$0") deploy ${HOST} ${CURRENT_TAG}

EOF

	success "Deployment validated on host"
}

# Parse command
if [[ $# -lt 1 ]]; then
	usage 1
fi

COMMAND="$1"
shift

SSH_USER="${SSH_USER:-root}"
SSH_PORT="${SSH_PORT:-22}"
VANTAGE_ID=""
REGION=""
AUTH_TOKEN=""
JETMON_HOSTNAME=""
DRY_RUN=0

case "$COMMAND" in
	bootstrap)
		if [[ $# -lt 1 ]]; then
			error "bootstrap requires <host>"
		fi
		HOST="$1"
		shift

		while [[ $# -gt 0 ]]; do
			case "$1" in
				-u) SSH_USER="$2"; shift 2 ;;
				-p) SSH_PORT="$2"; shift 2 ;;
				-v) VANTAGE_ID="$2"; shift 2 ;;
				-r) REGION="$2"; shift 2 ;;
				-t) AUTH_TOKEN="$2"; shift 2 ;;
				-n) JETMON_HOSTNAME="$2"; shift 2 ;;
				--dry-run) DRY_RUN=1; shift ;;
				-h) usage 0 ;;
				-*) error "Unknown option: $1" ;;
				*) error "Unexpected argument: $1. Did you pass the host twice?" ;;
			esac
		done

		bootstrap_veriflier "$HOST"
		;;
	deploy)
		if [[ $# -lt 2 ]]; then
			error "deploy requires <host> <image-tag>"
		fi
		HOST="$1"
		IMAGE_TAG="$2"
		shift 2

		while [[ $# -gt 0 ]]; do
			case "$1" in
				-u) SSH_USER="$2"; shift 2 ;;
				-p) SSH_PORT="$2"; shift 2 ;;
				--dry-run) DRY_RUN=1; shift ;;
				-h) usage 0 ;;
				-*) error "Unknown option: $1" ;;
				*) error "Unexpected argument: $1" ;;
			esac
		done

		deploy_veriflier_image "$HOST" "$IMAGE_TAG"
		;;
	-h|--help|help)
		usage 0
		;;
	*)
		error "Unknown command: $COMMAND"
		;;
esac
