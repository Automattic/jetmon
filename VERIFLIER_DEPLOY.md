# Veriflier Deployment Script

Quick reference for deploying Veriflier to DigitalOcean VPS hosts using `scripts/deploy-veriflier.sh`.

## Script Location

The script lives in this repo:

```text
scripts/deploy-veriflier.sh
```

Run it from the repo root, or symlink/alias it for convenience:

```bash
# from repo root
./scripts/deploy-veriflier.sh <command> ...

# optional: alias or PATH symlink
ln -s "$(pwd)/scripts/deploy-veriflier.sh" ~/bin/deploy-veriflier
```

### Environment overrides

The script reads these env vars (defaults shown):

- `JETMON_REPO_URL` — `https://github.com/Automattic/jetmon.git`
- `JETMON_BRANCH` — `v2`
- `VERIFLIER_IMAGE_REPO` — `ghcr.io/automattic/veriflier`
- `VERIFLIER_PORT` — `7803`

## Commands

### Bootstrap (First-Time Setup)

Initialize a new VPS with jetmon repo and `.env` configuration:

```bash
./scripts/deploy-veriflier.sh bootstrap <host> \
  -v <VANTAGE_ID> \
  -r <REGION> \
  -t <AUTH_TOKEN> \
  -n <JETMON_HOSTNAME>
```

**Example:**
```bash
./scripts/deploy-veriflier.sh bootstrap <vps-host-or-ip> \
  -v do-<region>-1 \
  -r <region> \
  -t <your-auth-token> \
  -n <region>.jetmon-veriflier
```

**What it does:**
- Clones jetmon repository at `v2` branch (skips if already present)
- Creates `jetmon/docker/.env` with all required Veriflier config
- Validates docker-compose configuration

**Options:**
- `-u USER` — SSH user (default: root)
- `-p PORT` — SSH port (default: 22)
- `-v VANTAGE_ID` — Veriflier vantage identifier (required)
- `-r REGION` — Region name (required)
- `-t AUTH_TOKEN` — Veriflier auth token (required)
- `-n JETMON_HOSTNAME` — Process hostname for metrics (optional)
- `--dry-run` — Show what would be done without executing

### Deploy (Update Image)

Deploy or update a Veriflier image on an existing host:

```bash
./scripts/deploy-veriflier.sh deploy <host> <image-tag>
```

**Example:**
```bash
./scripts/deploy-veriflier.sh deploy <vps-host-or-ip> <YYYY-MM-DD-sha-xxxxxxx>
```

**What it does:**
- Updates `.env` with new image tag
- Pulls latest Docker image
- Brings up containers with `docker compose up -d`
- Health-checks the Veriflier endpoint
- Validates container is running

**Options:**
- `-u USER` — SSH user (default: root)
- `-p PORT` — SSH port (default: 22)
- `--dry-run` — Show commands that would be executed

## Workflow

### Step 1: Bootstrap a new host (one-time)

```bash
./scripts/deploy-veriflier.sh bootstrap <vps-host-or-ip> \
  -v <vantage-id> -r <region> -t <auth-token> -n <hostname>
```

### Step 2: Deploy images (as needed)

```bash
./scripts/deploy-veriflier.sh deploy <vps-host-or-ip> <image-tag>
```

### Step 3: Validate on Monitor

Run from your Monitor environment:

```bash
./jetmon2 validate-config
./jetmon2 verifliers discovery-report --output=text
./jetmon2 telemetry report --since=15m
```

### Rollback

If validation fails, redeploy the previous image tag:

```bash
./scripts/deploy-veriflier.sh deploy <host> <previous-tag>
```

## Dry-Run Mode

Test commands without executing:

```bash
./scripts/deploy-veriflier.sh deploy <host> <image-tag> --dry-run
```

Shows SSH commands and docker-compose operations that would run.

## Features

- **Idempotent**: Safe to run multiple times
- **Retry logic**: Handles transient SSH failures with exponential backoff
- **Validation**: Confirms .env config, docker-compose syntax, and container health
- **Dry-run**: Preview changes before applying
- **Efficient**: Batches SSH commands to minimize connection overhead
- **v2 enforcement**: Always clones/checks out `v2` branch (not v1 `master`)

## Policy Constraints

These are enforced by the script and must remain true:

- **Always use `v2` branch** — v1 (`master`) is incompatible. Script verifies branch and fails if not v2.
- No Monitor/Veriflier co-location
- Monitor routes to direct Veriflier endpoints (no LB)
- Static `VERIFIERS` is the control plane
- Veriflier deploys are manual and operator-driven

## Security Notes

- **Never commit auth tokens, IPs, or vantage IDs** for production hosts to this repository.
- Store host inventory and credentials outside the public repo (e.g., `jetmon-secrets` or a secrets manager).
- Use SSH keys with passphrases and avoid embedding sensitive values in command history.
- The `.env` file written to the VPS contains the auth token — protect file permissions on the host.
