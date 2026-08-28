# llmproxy documentation

A self-hosted, OpenAI-compatible LLM proxy: one static Go binary, per-user API
keys, a curated model catalog and usage accounting. No request or response
content is ever persisted.

## Start here

| I want to | Go to |
|---|---|
| See it work, end to end | [Getting started](getting-started.md) |
| Deploy it on a server | [Install the binary](install/binary.md) |
| Deploy it with containers | [Install with Docker](install/docker.md) |
| Give my team access | [Set up SSO](guides/sso.md) |
| Meter Claude Code | [Meter Claude Code](guides/claude-code.md) |
| Look up a variable or endpoint | [Configuration](reference/configuration.md), [API](reference/api.md) |
| Understand why it is built this way | [Architecture](concepts/architecture.md) |

## Deploy it

- [install/binary.md](install/binary.md): release install, state layout, first
  login, the loopback guard, a systemd unit.
- [install/docker.md](install/docker.md): the published image, compose,
  Postgres, Coolify.
- [install/upgrades.md](install/upgrades.md): backup, upgrade, what migrations
  do.

## Operate it

- [guides/providers.md](guides/providers.md): register upstreams, rotate
  credentials, TLS, per-endpoint overrides.
- [guides/models.md](guides/models.md): discover, bind, alias, hide, resolve.
- [guides/api-keys.md](guides/api-keys.md): keys for people and services, and
  how to revoke them.
- [guides/pricing.md](guides/pricing.md): per-model prices, the feed format,
  what unpriced means.
- [guides/usage.md](guides/usage.md): what is recorded, application tags,
  the usage and stats endpoints, Prometheus metrics.
- [guides/sso.md](guides/sso.md): OIDC login, group-derived roles, an
  Authentik walkthrough, offboarding.
- [guides/claude-code.md](guides/claude-code.md): meter Anthropic-native tools
  without touching their credentials.

## Look it up

- [reference/configuration.md](reference/configuration.md): every environment
  variable, database URLs, the secret, CLI flags.
- [reference/api.md](reference/api.md): the HTTP surface endpoint by endpoint,
  with error codes and headers.
- [reference/openapi.yaml](reference/openapi.yaml): the same surface as an
  OpenAPI 3.1 spec.
- [reference/performance.md](reference/performance.md): throughput and latency
  measurements, and how to reproduce them.

## Understand it

- [concepts/architecture.md](concepts/architecture.md): scope and non-goals,
  the endpoint families, and every design rule with its rationale, including
  the no-content-persistence guarantee.

## Contribute

- [CONTRIBUTING.md](https://github.com/greyhavenhq/llmproxy/blob/main/CONTRIBUTING.md):
  development setup, the writing rules for these pages, the commit convention,
  how a release is cut.
- [SECURITY.md](https://github.com/greyhavenhq/llmproxy/blob/main/SECURITY.md):
  how to report a vulnerability privately.
