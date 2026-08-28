# Meter Claude Code

The transparent relay forwards Anthropic API traffic unchanged while recording
who consumed what. It is for tools that already hold their own Anthropic
credentials, Claude Code first among them: the proxy substitutes nothing,
stores no upstream secrets and rewrites no bytes.

```
Claude Code ── x-api-key / OAuth bearer ──▶ llmproxy ──▶ api.anthropic.com
                                              │
                                              └─ usage_event (tokens, cost,
                                                 duration, status; no content)
```

This is not the `/v1` ingress. There the proxy owns the provider credential
and routes by model name. Here the caller owns the credential and the proxy
meters.

Examples assume:

```bash
P=https://proxy.example.com
KEY=lp_...       # any key
ADMIN=lp_...     # an admin key
```

## Set it up

1. Mint a relay token, in the UI under Keys or directly:

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

   Copy `token` now. It is shown once.

2. Point Claude Code at the relay:

   ```bash
   export ANTHROPIC_BASE_URL=https://proxy.example.com/transparent/anthropic/lpt_Vb0mJq...
   claude
   ```

   The UI shows this exact command when the token is created.

3. Run a prompt, then check the Usage tab. The request appears with provider
   `transparent:anthropic` and client `claude-cli/<version>`.

Your Anthropic credential is unchanged by all of this: Claude Code keeps
sending its own, and the proxy forwards it untouched.

## Make it permanent

Pick one:

- **Shell profile.** Append the `export` line to `~/.zshrc` or `~/.bashrc`.
  Everything on that machine honouring `ANTHROPIC_BASE_URL` goes through the
  relay.
- **Claude Code settings**, to scope it to Claude Code only, in
  `~/.claude/settings.json`:

  ```json
  {
    "env": {
      "ANTHROPIC_BASE_URL": "https://proxy.example.com/transparent/anthropic/lpt_Vb0mJq..."
    }
  }
  ```

- **Alias**, to relay Claude Code while other Anthropic tooling stays direct:

  ```bash
  alias claude='ANTHROPIC_BASE_URL=https://proxy.example.com/transparent/anthropic/lpt_Vb0mJq... claude'
  ```

Both authentication styles work, because credentials pass through
byte-for-byte: an `x-api-key` is forwarded as received, and a Claude
subscription's `Authorization: Bearer` plus `anthropic-beta` OAuth header are
too. Token refresh happens against claude.ai directly and never crosses the
relay.

Any other Anthropic SDK or tool honouring a base URL works the same way.

## Relay tokens are not API keys

A relay token has the same lifecycle mechanics as an API key: `lpt_` prefix,
generated server-side, stored as a keyed hash plus a 4-character display
suffix, shown once, revoked by deletion.

It is deliberately a different credential. A relay token cannot authenticate
against `/v1`, `/my` or `/admin`, and an API key is not accepted on the relay.
Leaking one exposes usage attribution, not proxy access.

```bash
curl -s $P/my/relay-tokens -H "authorization: Bearer $KEY"                 # metadata only
curl -s -X DELETE $P/my/relay-tokens/<id> -H "authorization: Bearer $KEY"  # revoke
```

Revocation is immediate: the next relayed request gets 404
`unknown_relay_token`.

The token sits in the URL path, so the access log masks that segment
(`/transparent/anthropic/***Xk2m/v1/messages`), and the prefix is stripped
before forwarding, so nothing token-shaped reaches Anthropic.

## What the relay records

One `usage_event` per relayed request:

| Field | Value |
|---|---|
| `provider` | The sentinel `transparent:anthropic`. There is no provider row and nothing to configure in the catalog |
| `model` | Taken from the response (`message_start` on streams, the top-level `model` on unary bodies), never parsed out of the request |
| `endpoint` | The relayed path suffix: `v1/messages`, `v1/messages/count_tokens`, ... |
| `client` | The caller's `User-Agent`, truncated to 256 characters, so different tools behind one token stay distinguishable |
| `tags` | The caller's `x-llmproxy-tags` header, normalised as on `/v1`. See [usage.md](usage.md#tag-requests-by-application) |

Outcome, status code, streamed and cancelled flags and duration behave exactly
as on `/v1`. A mid-stream disconnect records the partial usage reported so
far, flagged `cancelled`.

Units map from Anthropic's usage object:

| Anthropic field | llmproxy unit |
|---|---|
| `input_tokens` | `input_tokens` |
| `output_tokens` | `output_tokens` (cumulative across stream deltas) |
| `cache_read_input_tokens` | `cached_input_tokens` (recorded when non-zero) |
| `cache_creation_input_tokens` | `cache_creation_tokens` (recorded when non-zero) |

Stored quantities are raw as Anthropic reported them, so each unit prices at
its own rate.

Endpoints without a `usage` object (`count_tokens`, `models`) still produce a
metadata-only event, so the request log stays complete. HEAD and OPTIONS
relay but are never recorded: they are connectivity probes and would only fill
the log with phantom failures.

Relay traffic appears everywhere ordinary traffic does: `/my/usage`, the
series endpoints, the admin summaries, the request log and `/metrics`
(labelled `provider="transparent:anthropic"`).

## Price relayed models

Use the normal pricing feed, keyed on the model name as Anthropic reports it:

```bash
curl -s $P/admin/v1/pricing -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{
    "version": "2026-08",
    "entries": [
      {"model": "claude-opus-5", "unit": "input_tokens", "price_per_million": 5.0},
      {"model": "claude-opus-5", "unit": "output_tokens", "price_per_million": 25.0}
    ]
  }'
```

Unpriced units are flagged, never counted as free. See
[pricing.md](pricing.md).

## What passes through

Forwarded verbatim: method, path suffix, query string, request body (streamed
unbuffered, no size cap, no field rewrites), response body (SSE flushed
chunk by chunk), and headers in both directions. Anthropic's `request-id` and
rate-limit headers come back untouched. Client disconnects cancel the upstream
request.

Four headers are the exception:

| Header | Why |
|---|---|
| Hop-by-hop headers | Not forwardable by definition |
| `Cookie` | Proxy-local browser state |
| `Accept-Encoding` | Dropped so the response arrives unencoded and usage stays readable. Clients that asked for gzip still get a correct identity response |
| `x-llmproxy-tags` | Addressed to the proxy: read for accounting, then stripped |

The proxy never buffers a whole streamed body and persists no request or
response content, same guarantee as the rest of llmproxy.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `LLMPROXY_TRANSPARENT_ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | Relay target. The relay only ever connects here, so it is not an open proxy. Empty disables the relay (404 `transparent_relay_disabled`) |

The upstream connection allows up to 10 minutes before response headers and
never bounds a streaming response; the client hanging up is what ends a relay.

## Where next

- [usage.md](usage.md) to read the resulting accounting.
- [pricing.md](pricing.md) to attach prices.
