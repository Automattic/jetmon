Veriflier on Cloudflare Workers (optional deploy target)
========================================================

This directory hosts an **optional, additive** way to deploy the Jetmon veriflier
on Cloudflare Workers + Containers, alongside the canonical Docker / TeamCity
deploy paths. The Go source under `veriflier2/`, `internal/veriflier/`, and
`internal/checker/` is untouched: the existing binary runs unchanged inside a
Cloudflare Container, and a three-line Worker (`worker/index.ts`) acts as
Cloudflare's required HTTPS entry point into the container.

The Worker exposes the **same wire contract** the orchestrator already speaks
(`POST /check`, `GET /status`, `Authorization: Bearer <token>`). To Jetmon, a
Worker-hosted veriflier is just another entry in the `VERIFIERS` array in
`config/config.json`.

Image source
------------
Source of truth for builds is GHCR — `.github/workflows/docker-publish.yml`
publishes `ghcr.io/automattic/veriflier:latest` (and `:<short-sha>` for
labelled PRs) on every push to `v2`. See
[../../docs/docker-images.md](../../docs/docker-images.md) for tag conventions.

Cloudflare Containers does **not** support GHCR as a source registry — its
allowlist for non-Cloudflare registries is DockerHub and AWS ECR only.
Every CF deploy therefore mirrors the GHCR image into Cloudflare's managed
registry (`registry.cloudflare.com/<account-id>/veriflier:<digest>`) and
points `wrangler.toml` at that mirror. CF also rejects `:latest` tags on
container images, so the mirrored tag is the source image's digest prefix.

`deploy/workers/deploy.sh` orchestrates the mirror + deploy in one step; you
should not need to run the underlying commands by hand.

Layout
------
- `../../wrangler.toml` — at the repo root. Pins the Container's `image` to
  the current `registry.cloudflare.com/<account-id>/veriflier:<digest12>` tag.
  `deploy.sh` rewrites this line on each deploy.
- `deploy.sh` — streamlined deploy script (pull → tag → push to CF →
  rewrite wrangler.toml → wrangler deploy). Invoked via
  `make deploy-veriflier-cf` / `make deploy-veriflier-cf-staging`.
- `worker/index.ts` — Worker entry point. Dispatches every incoming request to
  one of the Container instances via a Durable Object binding.
- `worker/package.json`, `worker/tsconfig.json` — TypeScript build for the
  Worker.

Prerequisites
-------------
1. Install Wrangler: `npm install -g wrangler` (or rely on `npx wrangler`).
2. `wrangler login` against the target Cloudflare account.
3. Cloudflare account with Workers Paid + Containers enabled.
4. Docker daemon running locally — the script pulls from GHCR and pushes to
   CF's managed registry via the docker CLI.
5. The `ghcr.io/automattic/veriflier` package must be **public** so the local
   `docker pull` in step 1 of the script succeeds without GHCR credentials.
   A maintainer flips this once in the GHCR package settings; if it is ever
   re-privatised, run `docker login ghcr.io` before `deploy.sh`.

Deploy
------
From the repo root:

    cd deploy/workers/worker && npm install && cd -

    # One-time per environment: set the auth token used by the orchestrator
    # to call this veriflier. Same value as the matching VERIFIERS[].auth_token
    # entry in jetmon's config/config.json.
    npx wrangler secret put VERIFLIER_AUTH_TOKEN
    npx wrangler secret put VERIFLIER_AUTH_TOKEN --env staging

    # Deploy staging first.
    make deploy-veriflier-cf-staging

    # Production once staging looks healthy.
    make deploy-veriflier-cf

Each run pulls `ghcr.io/automattic/veriflier:latest`, mirrors it to
Cloudflare's managed registry under a digest-derived tag, rewrites the
`image = "..."` line in `wrangler.toml`, and runs `wrangler deploy`. The
resulting one-line change to `wrangler.toml` should be committed so the
pinned tag stays in version control.

To deploy a specific source build (e.g. a PR's short-SHA image) instead of
the v2 head:

    SRC_TAG=af64e64 make deploy-veriflier-cf-staging

To rebuild the wrangler.toml entry without actually deploying:

    SKIP_DEPLOY=1 make deploy-veriflier-cf

Local development
-----------------
    cd deploy/workers/worker
    npm install
    npx wrangler dev

`wrangler dev` runs the Worker locally and pulls the container image
referenced in `wrangler.toml` from Cloudflare's managed registry. If you need
to iterate on uncommitted veriflier changes, build locally and tag the image
to match the URI in `wrangler.toml`:

    docker build -f docker/Dockerfile_veriflier \
      -t registry.cloudflare.com/<account-id>/veriflier:<digest12> .

Then, in another shell:

    curl http://localhost:8787/status
    curl -H "Authorization: Bearer $TOKEN" \
         -d '{"sites":[{"BlogID":1,"URL":"https://wordpress.com","TimeoutSeconds":10}]}' \
         http://localhost:8787/check

Compare those responses against the same request hitting the Docker-compose
veriflier (`http://localhost:7803/...`). Fields should match modulo `Host`.

Wiring into Jetmon
------------------
Add an entry to the `VERIFIERS` array in `config/config.json`, alongside any
existing Docker / TeamCity verifliers. Example:

    {
      "name": "veriflier-cf-global",
      "host": "jetmon-veriflier.<your-cf-subdomain>.workers.dev",
      "port": "443",
      "auth_token": "<same token as VERIFLIER_AUTH_TOKEN secret>"
    }

Then `kill -HUP <jetmon-pid>` (or `./jetmon2 reload`) to pick up the config.
The orchestrator's quorum logic in `internal/orchestrator/orchestrator.go`
treats this veriflier identically to any other.

Rollback / coexistence
----------------------
Removing the Worker is a one-line edit: delete the entry from `VERIFIERS` and
SIGHUP jetmon. No Go code, build, or wire protocol changes are needed for
rollback. The existing Docker / TeamCity verifliers continue serving in
parallel and form the safety net.

Open questions before promoting beyond staging
----------------------------------------------
- Vendor approval: confirm with Systems / Barry that Cloudflare Containers is
  on the approved list for production data egress.
- CF account: shared Automattic account, or dedicated `jetmon` account
  (follow the Newspack-AI precedent — ask Miguel Peixe).
- Cost ceiling: model per-second container compute + per-request cost against
  an equivalent VM at expected check volume.
- Geographic distribution: this first deploy is a single global Worker using
  Cloudflare's default placement. For region-specific verifliers, deploy a
  second Worker per region (e.g. `jetmon-veriflier-eu`) with
  `locationHint: "weur"` on the Durable Object and add a second entry to
  `VERIFIERS`.
