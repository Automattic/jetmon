# Documentation Guide

Documentation conventions for Jetmon 2. `AGENTS.md` and the files under
`docs/` are the authoritative project references; this file is only a compact
Claude helper.

## Go Comments

- Package comments should describe the package's role when the package has
  exported APIs or non-obvious responsibilities.
- Exported symbols should have comments when they are part of an internal
  package contract used across packages.
- Inline comments should explain why a choice exists, especially around rollout
  compatibility, transactions, concurrency, and security checks.
- Avoid comments that restate obvious assignments or control flow.

## Configuration Documentation

Use the plain text format in `config/config.readme`:

```text
SETTING_NAME
Description of what the setting does. Include default values and valid ranges.

DANGEROUS_SETTING
WARNING: Do not enable in production.
Explanation of when this should or should not be enabled.
```

Document all new config keys in:

- `config/config.readme`
- relevant rollout or operations docs
- sample Docker/environment files when the key affects local or production
  compose usage

## Operational Docs

Update docs when a change affects:

- production rollout or rollback
- Docker Compose or TeamCity deployment
- database migrations or compatibility
- WPCOM, StatsD, Veriflier, webhook, or alert delivery behavior
- API endpoints or auth scopes
- security posture or safety gates

Keep operational docs secret-free. Use placeholders and describe where secrets
are supplied instead of including real values.

## README Structure

For the main README, keep the current project style and link to detailed docs
instead of duplicating large operational procedures.

For component docs, prefer:

```markdown
component name
==============

Overview
--------
Brief description.

Building
--------
1. Step one
2. Step two
```

## Key Principles

- Prefer one authoritative doc over duplicated copies.
- Update the prelaunch checklist when a change creates a production validation
  gate.
- Keep historical v1 behavior clearly labeled as historical or compatibility
  context.
- Do not document debug-only, local-only, or temporary workarounds as if they
  are production procedure.
