# API CLI Site Fixture

`api-cli-sites.json` is the embedded default source for:

```bash
jetmon2 api sites bulk-add --count <n>
```

The list is intentionally small and local-test oriented. It mixes always-up
targets, redirects, slow responses, HTTP error responses, TLS/certificate
failures, custom-header checks, and keyword checks so Docker API testing can
exercise more than one site behavior without inventing fake public domains.

Do not use this fixture as a production monitoring seed list.
