# Jetmon Docs

The docs are intentionally small. Start with one of these files, then follow
links only when you need deeper operational detail.

| File | Use it for |
| --- | --- |
| [project.md](project.md) | System overview, architecture, data model, event model, and detection vocabulary. |
| [operations-guide.md](operations-guide.md) | Local development, production runtime care, dashboards, metrics, debugging, and support workflows. |
| [v1-to-v2-migration.md](v1-to-v2-migration.md) | Production rollout, rollback, TeamCity/container details, Veriflier deployment, and check-policy migration. |
| [internal-api-reference.md](internal-api-reference.md) | Internal API contract, API CLI usage, endpoint map, webhook signing, and alert contacts. |
| [decisions.md](decisions.md) | Accepted architecture decisions that used to live as separate ADR files. |
| [roadmap.md](roadmap.md) | Deferred work and future product/platform direction. |
| [changelog.md](changelog.md) | Implementation history and release notes. |

`rollout-canaries.example.json` is the fixture template used by rollout smoke
and compare-method commands.

When adding docs, prefer extending one of the canonical files above. Create a
new file only when it has a distinct owner, lifecycle, and reader.
