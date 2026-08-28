# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
just build      # static binary -> bin/llmproxy (CGO_ENABLED=0, no node needed)
just test       # go test ./...
just race       # race detector, -count=1
just lint       # golangci-lint run
just ui         # rebuild the embedded web UI (requires node)
just stress     # load harness (cmd/stress), see docs/reference/performance.md
just docker     # docker build -t llmproxy .
just docs       # preview the documentation site (requires uv)
```

Single test: `go test ./internal/server -run TestName`. Most server tests go through `testenv_test.go`, which boots a real server against a temp SQLite database.

The web UI (`ui/`, React + Vite) builds into `internal/server/uidist/`, which is **committed** and embedded via `go:embed` so plain `go build` (and the Docker image) works without node. After changing anything under `ui/`, run `just ui` and commit the regenerated `uidist/` output. The `ui-drift` CI job rebuilds the UI and fails if the committed `uidist/` is stale, so a plain `go build` from any CI-checked revision embeds the current UI.

## What this is

A self-hosted OpenAI-compatible LLM proxy: one static Go binary, per-user API keys, curated model catalog, usage accounting. Exactly two upstream wire formats (OpenAI-compatible and Anthropic native); the non-goals table in docs/concepts/architecture.md caps it there deliberately. Read docs/concepts/architecture.md before structural changes; it states every design rule with its rationale.

## Hard constraints

- **No request/response content is ever persisted.** No prompt, completion, or embedding input may land in the database or logs. Two tests enforce this: a schema test rejects content-shaped column names in the DDL, and an integration test asserts a marker string sent through a real request never reaches disk. Never add a column, log field, or debug dump that could hold a body.
- **The database is the single source of truth** (SQLite pure-Go by default, Postgres via `LLMPROXY_DATABASE_URL`). Caches are short-TTL hints invalidated on admin writes. Never hold a DB connection across an upstream call; usage is written after the fact, off the request path.
- **Bodies pass through byte-for-byte.** Streaming relays raw bytes with a bounded line scanner that only sniffs usage objects. Proxy metadata goes in `x-llmproxy-*` headers, never into response bodies.
- **Upstream credentials attach at exactly one code site**, shared by unary and streaming paths, with a regression test on both.

## Structure

- `cmd/llmproxy`: CLI entry point; subcommands `serve`, `key`, `relay-token`, `principal`, `version`.
- `cmd/stress`: self-contained load harness.
- `internal/config`: all configuration, environment-driven with `LLMPROXY_` prefix, defaults in `FromEnv()`. Document new variables in docs/reference/configuration.md and expose them in `docker-compose.coolify.yml` as `${VAR:-default}`.
- `internal/server`: HTTP layer. `openai.go` is the OpenAI-compatible ingress, `transparent.go` the Anthropic relay, `admin.go` the admin API, `sso.go`/`password.go`/`session.go` auth, `ui.go` serves the embedded UI, `litellm.go` compatibility surface.
- `internal/store`: all persistence; `schema.go` holds the DDL for both dialects.
- `internal/catalog`: model alias -> provider binding resolution and capability checks (checked in exactly one place; a miss is a typed 400 `endpoint_not_supported`).
- `internal/upstream`: shared HTTP client pool.
- `internal/pricing`, `internal/metrics`, `internal/oidc`, `internal/secrets`, `internal/apierr`: what the names say.

## Behavior rules worth knowing before touching proxy code

- Streamed requests get `stream_options.include_usage` forced on; usage merges max-wins across chunks. Client aborts cancel the upstream but still record partial usage flagged `cancelled`.
- Unpriced usage is recorded as unpriced (`cost: null`), never as zero.
- Model aliases are globally unique; ambiguity is a 409 at write time, one alias hop max.
- Admin mutations write a metadata-only `admin_event` row in the same transaction.
- API keys are stored as HMAC-SHA256 under a secret outside the database (`LLMPROXY_KEY_SECRET` or the generated secret file); plaintext shown once at mint.
- Prometheus labels are aggregate-only (provider, model, endpoint, unit, outcome); never principals or keys.

## Documentation

`docs/` is organised by reader intent: `getting-started.md` (tutorial), `install/` (deploy), `guides/` (operate), `reference/` (look up), `concepts/` (understand). Task pages are imperative and step-numbered with expected output; rationale is one sentence plus a link, and lives in `concepts/architecture.md`. The writing rules are in CONTRIBUTING.md. A new page needs an entry in `mkdocs.yml`'s nav and in the `docs/README.md` router. `mkdocs build --strict` fails on broken internal links and runs in CI on any PR touching `docs/`. The site's look is the Greyhaven design system, vendored into `docs/assets/stylesheets/greyhaven.css` from the design system's tokens; re-vendor rather than hand-tune colors, and keep `mkdocs.yml`'s palette on `custom` or the theme's own colors override the stylesheet.

## Deployment files

`docker-compose.yml` is the development setup (SQLite, loopback-only port). `docker-compose.coolify.yml` is the PaaS deployment (Postgres, no host port mapping, every `LLMPROXY_*` variable exposed as `${VAR:-default}` so Coolify lists them). Keep the two and docs/install/docker.md in sync when configuration changes.
