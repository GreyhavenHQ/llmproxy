# llmproxy

A self-hosted, OpenAI-compatible LLM proxy: one static Go binary with an embedded web UI. Point any OpenAI SDK at it with one line changed and it routes to your vLLM, SGLang, OpenAI or other OpenAI-compatible upstreams, with per-user API keys, a curated model catalog and usage accounting.

**No prompt, completion or other request/response content is ever persisted.** The database schema has no column that could hold a body, and tests enforce it.

Apache 2.0 licensed.

## Features

- OpenAI-compatible ingress: chat completions (unary and SSE streaming), legacy completions, embeddings and model listing. Bodies pass through byte-for-byte.
- Curated catalog: register providers, discover their models, bind them to stable aliases with capabilities and prices.
- Self-service API keys, stored as keyed hashes, shown once; deleting a key revokes it immediately.
- Usage accounting with pricing, a team-visible dashboard, a request metadata log and Prometheus metrics.
- OIDC SSO with group-derived roles, or a local single-admin mode with a generated password.
- A transparent Anthropic relay that meters Claude Code per user while forwarding its own credentials untouched.
- SQLite (pure Go) by default; Postgres via one environment variable.

## Quickstart

Grab a prebuilt binary from the [latest release](https://github.com/greyhavenhq/llmproxy/releases/latest) (linux/amd64, linux/arm64, darwin/arm64):

```bash
curl -fsSL https://github.com/greyhavenhq/llmproxy/releases/latest/download/llmproxy_Linux_x86_64.tar.gz | tar xz
./llmproxy serve      # http://127.0.0.1:4000
```

Or run the container image:

```bash
docker run -p 127.0.0.1:4000:4000 -v llmproxy-data:/data \
  -e LLMPROXY_ALLOW_NONLOCAL=1 ghcr.io/greyhavenhq/llmproxy:latest
```

Or build from source, which needs a recent Go toolchain and [just](https://just.systems), no C compiler and no node:

```bash
git clone https://github.com/greyhavenhq/llmproxy.git && cd llmproxy
just build            # -> bin/llmproxy (~14 MiB, static)
bin/llmproxy serve    # http://127.0.0.1:4000
```

Open http://127.0.0.1:4000 and sign in with the admin password (generated at first boot into `.llmproxy/admin-password`, or set `LLMPROXY_ADMIN_PASSWORD`). From the UI: register providers, bind models, create API keys, watch usage. The whole flow is walked through in [docs/getting-started.md](docs/getting-started.md).

Then use it from any OpenAI SDK, one line changed:

```python
from openai import OpenAI
client = OpenAI(base_url="http://127.0.0.1:4000/v1", api_key="lp_...")

client.chat.completions.create(model="qwen-72b",
    messages=[{"role": "user", "content": "hello"}])
```

See [docs/docker.md](docs/docker.md) for compose, Postgres and the published image.

## Documentation

Everything lives in [docs/](docs/README.md): a getting-started tutorial, how-to guides (installation, Docker, SSO, providers and models, keys and usage, the Anthropic relay), reference (configuration, the HTTP API, an OpenAPI 3.1 spec, performance) and an architecture explanation.

## Performance

The built-in stress harness sustains around 8,500 req/s at concurrency 200 on SQLite, every request accounted, heap under 50 MiB. Numbers, method and caveats in [docs/performance.md](docs/performance.md); reproduce with `just stress`.

## Development

```bash
just            # list all recipes
just build      # static binary into bin/
just test       # full suite
just race       # same, under the race detector
just lint       # golangci-lint
just stress     # load harness
just ui         # rebuild the embedded web UI (requires node)
```

The web UI lives in `ui/` (React + Vite); its build output is committed in `internal/server/uidist/` and embedded via `go:embed`, so `go build` needs no node toolchain. Run `just ui` after changing anything under `ui/`.

CI runs lint, the race-enabled test suite, the build and a UI drift check on every push; tags matching `v*` publish a release with prebuilt binaries and the container image. Release notes live on the [releases page](https://github.com/greyhavenhq/llmproxy/releases).

[CONTRIBUTING.md](CONTRIBUTING.md) covers the development workflow, the commit-message convention and the release process. Report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).
