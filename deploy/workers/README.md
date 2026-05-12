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
The Container pulls the published image
`ghcr.io/automattic/veriflier:latest`, built by
`.github/workflows/docker-publish.yml` on every push to `v2`. There is no
local `docker build` step in the deploy path — `wrangler deploy` references
the registry image directly. See [../../docs/docker-images.md](../../docs/docker-images.md)
for tag conventions.

Layout
------
- `../../wrangler.toml` — at the repo root. Pins the Container's `image` to
  `ghcr.io/automattic/veriflier:latest`.
- `worker/index.ts` — Worker entry point. Dispatches every incoming request to
  one of the Container instances via a Durable Object binding.
- `worker/package.json`, `worker/tsconfig.json` — TypeScript build for the
  Worker.

Prerequisites
-------------
1. Install Wrangler: `npm install -g wrangler`
2. `wrangler login` against the target Cloudflare account.
3. Cloudflare account with Workers Paid + Containers enabled.
4. The `ghcr.io/automattic/veriflier` package must be **public**, so
   Cloudflare can pull it without GHCR credentials. A maintainer flips this in
   the GHCR package settings; until then, `wrangler deploy` will fail with a
   pull-auth error.

Deploy
------
From the repo root:

    cd deploy/workers/worker
    npm install

    # Set the auth token. Use the same token you put into the matching
    # VERIFIERS[].auth_token entry in jetmon's config/config.json.
    wrangler secret put VERIFLIER_AUTH_TOKEN
    wrangler secret put VERIFLIER_AUTH_TOKEN --env staging

    # Deploy staging first.
    npm run deploy:staging

    # Production once staging looks healthy.
    npm run deploy

`wrangler deploy` resolves `ghcr.io/automattic/veriflier:latest`, copies it
into Cloudflare's container registry, and ships the Worker. To roll a new
build out, re-run `wrangler deploy` after the GHCR workflow has finished on
`v2`; no Dockerfile change or local build is involved.

Local development
-----------------
    cd deploy/workers/worker
    npm install
    wrangler dev

`wrangler dev` pulls `ghcr.io/automattic/veriflier:latest` from GHCR and runs
it under the local container runtime. If you need to iterate on uncommitted
veriflier changes, build locally and tag the image as
`ghcr.io/automattic/veriflier:latest`:

    docker build -f docker/Dockerfile_veriflier -t ghcr.io/automattic/veriflier:latest .

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
