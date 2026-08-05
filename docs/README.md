# llmproxy documentation

The documentation follows the [Diátaxis](https://diataxis.fr/) structure:
a tutorial to learn by doing, how-to guides for specific goals, reference
material for looking things up, and explanation for understanding the design.

## Tutorial

- [getting-started.md](getting-started.md): from `git clone` to a first chat
  completion, including provider registration, model binding and your first
  API key.

## How-to guides

- [installation.md](installation.md): build from source, state layout, first
  key, systemd, upgrades.
- [docker.md](docker.md): the image, docker compose, switching to Postgres, deploying on Coolify.
- [sso.md](sso.md): OIDC login, sessions, group-based roles, an Authentik
  walkthrough.
- [providers-and-models.md](providers-and-models.md): register providers,
  discover and bind models, aliases, capabilities, dry-run resolution.
- [keys-and-usage.md](keys-and-usage.md): key lifecycle, usage accounting,
  pricing feeds, team statistics, metrics.
- [transparent-relay.md](transparent-relay.md): meter Claude Code and other
  Anthropic-native tools without touching their credentials.

## Reference

- [configuration.md](configuration.md): every environment variable, database
  URLs, the secret, the admin password, CLI flags.
- [api.md](api.md): the full HTTP surface, endpoint by endpoint, with error
  codes and headers.
- [openapi.yaml](openapi.yaml): the same surface as an OpenAPI 3.1 spec.
- [performance.md](performance.md): throughput and latency measurements and
  how to reproduce them.

## Explanation

- [architecture.md](architecture.md): scope and non-goals, the three endpoint
  families, and the design rules with their rationale, including the
  no-content-persistence guarantee.
