# Transparent Anthropic relay

The transparent relay forwards Anthropic API traffic unchanged while
recording who consumed what. It exists for tools that already hold their own
Anthropic credentials, Claude Code first among them: the proxy substitutes
nothing, stores no upstream secrets, and rewrites no bytes. What it adds is
per-user usage accounting on the same tables, dashboards and metrics as the
rest of llmproxy.

```
Claude Code ── x-api-key / OAuth bearer ──▶ llmproxy ──▶ api.anthropic.com
                                              │
                                              └─ usage_event (tokens, cost,
                                                 duration, status; no content)
```

This is distinct from the `/v1` ingress: there the proxy owns the provider
credential and routes by model alias. Here the caller owns the credential and
the proxy is a bystander that meters.

## Relay tokens

Requests go to:

```
/transparent/anthropic/<relay-token>/<anthropic-path>
```

The relay token attributes traffic to a principal. It has the same lifecycle
mechanics as an API key: a random secret with an `lpt_` prefix, generated
server-side, stored only as a keyed hash plus a 4-character display suffix,
shown exactly once at creation, revoked by deletion. But it is deliberately
not an API key. A relay token cannot authenticate against `/v1`, `/my` or
`/admin`, and an API key is not accepted on the relay. Leaking one exposes
usage attribution, not proxy access; the Anthropic credential itself never
touches the proxy's storage at all.

Mint one in the UI (Keys page) or directly:

```bash
curl -s $P/my/relay-tokens -H "authorization: Bearer $KEY" \
  -H 'content-type: application/json' -d '{"label": "claude-code laptop"}'
```

```json
{
  "id": "5e9c1af2d4b04c33a1f09e7d5b246810",
  "token_suffix": "Xk2m",
  "label": "claude-code laptop",
  "created_at": "2026-08-03T10:02:11.482235Z",
  "last_used_at": null,
  "token": "lpt_Vb0mJq..."
}
```

`GET /my/relay-tokens` lists metadata only, `DELETE /my/relay-tokens/{id}`
revokes immediately (the next relayed request gets 404
`unknown_relay_token`). The token appears in the URL path, so the proxy's
access log masks that segment (`/transparent/anthropic/***Xk2m/v1/messages`),
and the prefix is stripped before forwarding, so nothing token-shaped reaches
Anthropic.

## Claude Code setup

Claude Code sends all API traffic to `ANTHROPIC_BASE_URL` and appends the
standard paths (`/v1/messages`, `/v1/messages/count_tokens`, ...). The UI
shows this exact command when a token is created:

```bash
export ANTHROPIC_BASE_URL=https://proxy.example.com/transparent/anthropic/lpt_Vb0mJq...
claude
```

To make it permanent, pick one:

* **Shell profile**: append the `export` line to `~/.zshrc` / `~/.bashrc`.
  Everything on that machine that honours `ANTHROPIC_BASE_URL` goes through
  the relay.
* **Claude Code settings**: scope it to Claude Code only, in
  `~/.claude/settings.json`:

  ```json
  {
    "env": {
      "ANTHROPIC_BASE_URL": "https://proxy.example.com/transparent/anthropic/lpt_Vb0mJq..."
    }
  }
  ```
* **Alias**: proxy Claude Code while other Anthropic tooling stays direct:

  ```bash
  alias claude='ANTHROPIC_BASE_URL=https://proxy.example.com/transparent/anthropic/lpt_Vb0mJq... claude'
  ```

Both authentication styles work, because credentials pass through
byte-for-byte:

* **API key**: `x-api-key` forwarded as received.
* **Claude subscription (OAuth)**: `Authorization: Bearer` plus the
  `anthropic-beta` OAuth header forwarded as received. Token refresh happens
  against claude.ai directly and never crosses the relay.

Any other Anthropic SDK or tool that honours a base URL works the same way.

## What the relay does and does not do

Forwarded verbatim: method, path suffix, query string, request body (streamed
unbuffered, no size cap, no field rewrites), response body (SSE flushed
chunk-by-chunk), and headers in both directions apart from hop-by-hop
headers, `Cookie` (proxy-local browser state) and `Accept-Encoding` (dropped
so the response arrives unencoded and usage stays readable; clients that
asked for gzip still get a correct identity response). Anthropic's
`request-id` and rate-limit headers come back untouched. Client disconnects
cancel the upstream request.

Every method on every sub-path relays. Usage quantities are extracted where a
`usage` object exists (`/v1/messages`, unary and streamed); other endpoints
(`count_tokens`, `models`, future additions) still produce a metadata-only
usage event, so the request log stays complete. The exception is HEAD and
OPTIONS: they relay but are never recorded. They are connectivity probes
(Claude Code sends one per session start, which Anthropic's root answers
with a 404), carry nothing to meter, and would only fill the request log
with phantom failures.

The proxy never buffers a whole streamed body and persists no request or
response content anywhere, same guarantee as the rest of llmproxy.

## Accounting

Each relayed request writes one `usage_event`:

* `provider` is the sentinel `transparent:anthropic`; there is no provider
  row and nothing to configure in the catalog.
* `model` is taken from the response (`message_start` on streams, the
  top-level `model` field on unary bodies), never parsed out of the request.
* `endpoint` is the relayed path suffix (`v1/messages`,
  `v1/messages/count_tokens`, ...).
* `client` is the caller's `User-Agent` header, truncated to 256 characters
  (Claude Code sends `claude-cli/<version> ...`), so different tools behind
  the same token stay distinguishable. Header metadata only, like everything
  else here.
* Outcome, status code, streamed/cancelled flags and duration behave exactly
  as on `/v1`; a mid-stream client disconnect records the partial usage
  reported so far, flagged `cancelled`.

Units map from Anthropic's usage object:

| Anthropic field | llmproxy unit |
|---|---|
| `input_tokens` | `input_tokens` |
| `output_tokens` | `output_tokens` (cumulative across stream deltas) |
| `cache_read_input_tokens` | `cached_input_tokens` (recorded when non-zero) |
| `cache_creation_input_tokens` | `cache_creation_tokens` (recorded when non-zero) |

Stored quantities are raw as Anthropic reported them, so each unit prices at
its own rate. Because Anthropic reports cache reads and writes *outside*
`input_tokens` (the OpenAI shape includes cached tokens in the prompt
count), the usage aggregates fold both cache units into `input_tokens` at
read time; see [keys-and-usage.md](keys-and-usage.md#usage-model).

Pricing uses the normal feed keyed on the model name as Anthropic reports it
(e.g. `claude-opus-5`); see [keys-and-usage.md](keys-and-usage.md#pricing).
Unpriced units are flagged, never counted as free.

Relay traffic appears everywhere ordinary traffic does: `/my/usage`,
`/my/usage/series`, the admin summaries, the request log and `/metrics`
(labelled `provider="transparent:anthropic"`).

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `LLMPROXY_TRANSPARENT_ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | Relay target. The relay only ever connects here, so it is not an open proxy. Empty disables the relay (404 `transparent_relay_disabled`). |

The upstream connection allows up to 10 minutes before response headers
(long non-streaming requests think for a while) and never bounds a streaming
response; the client hanging up is what ends a relay.
