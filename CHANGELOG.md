# Changelog

All notable changes to llmproxy are documented in this file. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0] - 2026-08-04

First public release.

### Added

- OpenAI-compatible ingress: `POST /v1/chat/completions` (unary and SSE
  streaming), `POST /v1/completions`, `POST /v1/embeddings`,
  `GET /v1/models`, with byte-for-byte body passthrough. The proxy rewrites
  only the top-level model name and forces `stream_options.include_usage` on
  streams. Client disconnects cancel the upstream request and record partial
  usage.
- Provider and model catalog: register OpenAI-compatible upstreams, discover
  their models, bind them to globally unique public aliases with per-model
  capability sets (`chat`, `chat_stream`, `completions`, `embeddings`,
  `transcription`) and per-unit prices. Per-endpoint URL overrides and
  per-provider TLS options.
- API keys: self-service minting, HMAC-SHA256 keyed hashes with the secret
  kept outside the database, plaintext shown exactly once, deletion as the
  revocation mechanism.
- Usage accounting: per-request (unit, quantity) pairs, per-unit pricing with
  an explicit unpriced state, versioned pricing feeds, usage summaries and
  time series per user and across the team, a request metadata log, and
  aggregate Prometheus metrics at `/metrics`.
- Authentication: OIDC SSO with server-side sessions, group-derived roles,
  session revocation, and a local single-admin mode with a generated password
  when no identity provider is configured.
- Built-in web UI, compiled into the binary: sign-in, key management,
  provider and model administration with upstream discovery, a usage
  dashboard, and the request log.
- Transparent Anthropic relay: forwards requests verbatim to the Anthropic
  API with the caller's own credentials, attributes usage per user via
  dedicated relay tokens, and never rewrites bodies.
- Model-management compatibility endpoints (`POST /model/new`,
  `GET /model/info`, `POST /model/delete`) plus root-level aliases of the
  OpenAI routes, so existing proxy management tooling works unchanged.
- Storage: pure-Go SQLite (WAL) by default, Postgres via
  `LLMPROXY_DATABASE_URL`. No request or response content is ever persisted;
  the schema is content-free and tests enforce it.
- Packaging: single static binary, Dockerfile and docker compose, `just`
  recipes for building, testing, linting and stress runs, Gitea Actions CI
  and release workflows.
