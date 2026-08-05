# Keys and usage

Examples assume:

```bash
P=http://127.0.0.1:4000
KEY=lp_...       # any key
ADMIN=lp_...     # an admin key
```

## Key lifecycle

Every API key belongs to a principal (a user or a service) and inherits that
principal's role. Keys are random 32-byte values with an `lp_` prefix,
generated server-side; the database stores only an HMAC-SHA256 digest under
the server secret plus the plaintext's last 4 characters for display
(listings show `***xxxx`), so the plaintext exists exactly once, in the
creation response (or CLI output). There is no way to view a key again, only
to mint a new one.

Four ways keys come into existence:

**Self-service.** Any key or session holder manages their own keys, either in
the built-in UI at `/` or directly:

```bash
curl -s $P/my/keys -H "authorization: Bearer $KEY" \
  -H 'content-type: application/json' -d '{"label": "laptop"}'
```

```json
{
  "id": "8c1de2f4a6b84c33a1f09e7d5b246810",
  "key_suffix": "Mq3w",
  "label": "laptop",
  "created_at": "2026-07-29T10:02:11.482235Z",
  "last_used_at": null,
  "key": "lp_Vb0mJq..."
}
```

`GET /my/keys` lists metadata only (no `key` field, up to 500 keys), and
`DELETE /my/keys/{id}` deletes the key outright:

```bash
curl -s -X DELETE $P/my/keys/8c1de2f4a6b84c33a1f09e7d5b246810 \
  -H "authorization: Bearer $KEY"
# {"deleted": "8c1de2f4a6b84c33a1f09e7d5b246810"}
```

**Admin-issued.** An admin sees and revokes every key in the UI (Admin, All
keys) and mints keys for any principal by name over the API:

```bash
curl -s $P/admin/v1/keys -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"principal": "alice", "label": "onboarding"}'
```

The response is the same shape plus a `principal` field. Admins list keys
across principals (`GET /admin/v1/keys?principal=alice`) and delete any key
(`DELETE /admin/v1/keys/{id}`).

**Service principals.** For workloads, create a `service` principal so the
key survives staff turnover and shows up under its own name in usage. In the
UI (Admin, Services) one step creates the service with its key; over the API:

```bash
curl -s $P/admin/v1/principals -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"name": "batch-service", "kind": "service"}'
curl -s $P/admin/v1/keys -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"principal": "batch-service", "label": "prod"}'
```

**CLI.** `llmproxy key create|list|delete` and `llmproxy principal create`
operate directly on the database; this is how the first admin key is minted
before any HTTP credential exists. See [installation.md](installation.md).

Deletion is the revocation mechanism and takes effect immediately (the
database is checked on every request). A deleted key authenticates like any
unknown key: 401 `invalid_api_key`. Usage history survives deletion; usage
events reference the key id, which is never reused. `last_used_at` is
refreshed at most once per minute per key, so treat it as coarse.

## Usage model

Every `/v1` request that reaches resolution produces one `usage_event` row
carrying identifiers, flags and numbers: principal, key, provider, alias,
upstream name, endpoint, client, status code, outcome (`ok`, `upstream_error`,
`unreachable`, `cancelled`), cancelled and streamed flags, duration, cost.
`client` is the caller's `User-Agent` header, truncated to 256 characters and
stored verbatim, so the tool behind a key is visible and can change over time
("claude-cli/2.0.13 (external, cli)", "openai-python/1.51.0", ...). Grouping
by product family happens at read time; the stored value stays exact.
Attached to it are per-unit `usage_quantity` rows: `(unit, quantity,
unit_price, priced)` with units `input_tokens`, `output_tokens`,
`cached_input_tokens` (from `prompt_tokens_details.cached_tokens` when
reported), `cache_creation_tokens` (Anthropic cache writes, via the
[transparent relay](transparent-relay.md)) and `audio_seconds` reserved for
transcription.

Traffic on the [transparent Anthropic relay](transparent-relay.md) lands in
the same tables, attributed via relay token instead of API key, with the
sentinel provider `transparent:anthropic` and the model name as Anthropic
reports it.

Quantities come from the upstream's `usage` object: read from the JSON body
on unary responses, and from SSE chunks on streams (the proxy forces
`stream_options.include_usage` so the final chunk carries usage). Cancelled
and errored requests are recorded too, flagged accordingly, with whatever
partial usage the upstream reported before the end. Accounting runs detached
from the request path, so a client hanging up cannot lose the record.

One asymmetry is normalised at read time. The OpenAI shape reports cached
tokens as a subset of the prompt count (`prompt_tokens_details.cached_tokens
⊆ prompt_tokens`), while Anthropic reports cache reads and writes *outside*
`input_tokens`. Stored quantities stay raw as reported (pricing multiplies
each unit by its own price, so cache reads and writes keep their own rates),
but every aggregate (`/my/usage*`, `/admin/v1/usage*`, `/stats/*`) folds the
relay's `cached_input_tokens` and `cache_creation_tokens` into
`input_tokens`. So `input_tokens` always means "total input processed,
cached included" and is comparable across providers, and
`cached_input_tokens` is always the cached subset. An OpenAI-compatible
upstream that never reports `cached_tokens` (no prefix caching, or caching
not surfaced) simply shows no cached quantity; absence means "not reported",
never "free".

No content is ever persisted. The schema has no column that could hold a
prompt, completion or embedding input, and a test rejects content-shaped
column names in the DDL.

## Pricing

Prices belong to the model. Each binding carries a price per unit, per
million units, set when the model is bound or edited:

```bash
curl -s -X PATCH $P/admin/v1/models/qwen-72b -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' \
  -d '{"pricing": {"input_tokens": 0.4, "output_tokens": 1.2}}'
```

`pricing` is the model's complete price set: units left out are unpriced
afterwards, so the same call clears a price by omitting its unit. Every model
read (`GET /admin/v1/models`) returns the prices that apply to it:

```json
{
  "alias": "qwen-72b",
  "upstream_name": "Qwen/Qwen2.5-72B-Instruct",
  "pricing": {"input_tokens": 0.4, "output_tokens": 1.2},
  "pricing_inherited": false
}
```

Prices resolve against the names a model is known by, most specific first: its
own name, the model it points at (see
[aliases](providers-and-models.md#point-a-name-at-another-model)), then the
upstream model name. So a name inherits a price without repeating it, and can
override it per unit by setting its own. `pricing_inherited` is true when the
price in force is not keyed on this name.

Underneath, prices are a versioned feed keyed on `(model, unit)`, so a whole
price list can also be loaded at once via `POST /admin/v1/pricing` (or
`LLMPROXY_PRICING_FILE` at startup):

```bash
curl -s $P/admin/v1/pricing -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{
    "version": "2026-07",
    "entries": [
      {"model": "qwen-72b", "unit": "input_tokens",  "price_per_million": 0.4},
      {"model": "qwen-72b", "unit": "output_tokens", "price_per_million": 1.2},
      {"model": "e5-embed", "unit": "input_tokens",  "price_per_unit": 0.00000002}
    ]
  }'
# {"version": "2026-07", "count": 3}
```

The feed format: a required string `version`, and `entries` where each entry
has `model`, a `unit` from `input_tokens | output_tokens |
cached_input_tokens | cache_creation_tokens | audio_seconds`, and exactly one
of `price_per_unit` or
`price_per_million` (the latter is divided by 1,000,000 on load). `model` can
be either the public alias or the upstream model name; lookup tries the alias
first. Anything else is rejected with 400 `invalid_pricing_feed`.

Loading a feed replaces the active one atomically and applies to new requests
immediately; already-recorded events are not repriced. `GET /admin/v1/pricing`
returns the active version (`null` if none), the entry count and the entries
themselves (per-million prices). Editing one model's prices publishes a new
version too, carrying every other model's entries over unchanged.

A `(model, unit)` pair with no entry is recorded as **unpriced, never as
zero**: the quantity row gets `priced: false`, the event is flagged
`unpriced`, summaries show `cost: null` for fully unpriced groups, and
`/metrics` counts the volume under `llmproxy_unpriced_units_total`. Silence
never masquerades as "free".

## Reading usage

`GET /my/usage` returns the caller's own usage grouped by (model, endpoint);
`since` and `until` take ISO 8601 dates or datetimes (until is exclusive):

```bash
curl -s "$P/my/usage?since=2026-07-01&until=2026-08-01" -H "authorization: Bearer $KEY"
```

```json
{
  "usage": [
    {
      "model": "qwen-72b",
      "endpoint": "chat",
      "requests": 412,
      "cancelled": 3,
      "cost": 1.20734,
      "units": {"input_tokens": 1882340, "output_tokens": 378512}
    }
  ]
}
```

`GET /admin/v1/usage/summary` is the same aggregation across everyone, each
row carrying a `principal` name, optionally filtered with `?principal=<name>`:

```bash
curl -s "$P/admin/v1/usage/summary?since=2026-07-01" -H "authorization: Bearer $ADMIN"
```

```json
{
  "usage": [
    {
      "principal": "batch-service",
      "model": "e5-embed",
      "endpoint": "embeddings",
      "requests": 9120,
      "cancelled": 0,
      "cost": null,
      "units": {"input_tokens": 44021833}
    },
    {
      "principal": "alice",
      "model": "qwen-72b",
      "endpoint": "chat",
      "requests": 412,
      "cancelled": 3,
      "cost": 1.20734,
      "units": {"input_tokens": 1882340, "output_tokens": 378512}
    }
  ]
}
```

## Usage over time

`GET /my/usage/series` (and `GET /admin/v1/usage/series` across everyone,
optionally filtered with `?principal=<name>`) is the same accounting bucketed
by time. `bucket` is `hour | day | week | month` (default `day`), `since` and
`until` bound the window as above:

```bash
curl -s "$P/my/usage/series?bucket=day&since=2026-07-01" -H "authorization: Bearer $KEY"
```

```json
{
  "bucket": "day",
  "series": [
    {
      "start": "2026-07-01T00:00:00Z",
      "requests": 41,
      "ok": 38,
      "cancelled": 1,
      "failed": 2,
      "unpriced_requests": 3,
      "cost": 0.10734,
      "units": {"input_tokens": 188234, "output_tokens": 37851}
    },
    {
      "start": "2026-07-02T00:00:00Z",
      "requests": 0,
      "ok": 0,
      "cancelled": 0,
      "failed": 0,
      "unpriced_requests": 0,
      "cost": null,
      "units": {}
    }
  ]
}
```

Buckets are UTC (weeks start Monday) and the range is gap-filled: quiet
periods come back as empty buckets rather than as holes, so a series can be
plotted directly. `ok`, `cancelled` and `failed` partition `requests`, where
failed is any non-ok outcome the caller did not cancel (`upstream_error`,
`unreachable`). `cost` is null when nothing in the bucket had a price, and
`unpriced_requests` counts the requests that hit a missing price. Without
`since`, the series starts at the first recorded event; a window needing more
than 1000 buckets is refused with 400 `range_too_large`.

## Team statistics

The `/stats` endpoints serve the same accounting to **every authenticated
user**: the proxy's usage is team-visible by design, and the admin role gates
configuration, not visibility. All three take the same optional filters:
`principal=<name>`, `provider=<name>` (the sentinel `transparent:anthropic`
included; a deleted provider's events resolve to `(deleted)`),
`model=<alias>` and `client=<prefix>` (a prefix match on the stored
User-Agent, so `client=claude-cli` covers every version).

* `GET /stats/series`: the bucketed series above, with the extra filters.
* `GET /stats/summary`: the window aggregated over every recorded dimension:
  one row per `(principal, provider, model, endpoint, client)`, so any
  roll-up (by model, by user, by client) is a client-side sum. **Completed
  requests with a model only**: a failure's model and provider are whatever
  the caller asked for, and model-less events (the relay's `count_tokens`
  and `models` calls report no model and no usage) have no place in a
  by-model breakdown. Both stay visible in the series and in the request
  log.
* `GET /stats/requests`: the newest events with quantities (`?limit=`, max
  500): the request metadata log, failures included.

```bash
curl -s "$P/stats/summary?since=2026-07-01&client=claude-cli" -H "authorization: Bearer $KEY"
```

```json
{
  "usage": [
    {
      "principal": "alice",
      "provider": "transparent:anthropic",
      "model": "claude-opus-5",
      "endpoint": "v1/messages",
      "client": "claude-cli/2.0.13 (external, cli)",
      "requests": 3211,
      "cancelled": 12,
      "cost": 41.20734,
      "units": {"input_tokens": 1882340, "output_tokens": 378512, "cached_input_tokens": 1571200}
    }
  ]
}
```

This is what the UI draws: the Usage tab (tiles, per-period charts, the model
share, the client breakdown and the by-user table, filterable by user,
provider and client) and the Requests tab (the live request log). The older
`/my/usage*` and `/admin/v1/usage*` endpoints stay as documented above.

## Metrics

`GET /metrics` (unauthenticated) exposes Prometheus-text counters with
aggregate labels only; per-principal detail deliberately stays in the
database so metric cardinality stays bounded.

| Metric | Labels | Meaning |
|---|---|---|
| `llmproxy_requests_total` | `endpoint`, `provider`, `model`, `outcome` | Requests by outcome (`ok`, `upstream_error`, `unreachable`, `cancelled`) |
| `llmproxy_request_seconds_sum` | `endpoint`, `provider`, `model` | Total request duration |
| `llmproxy_request_seconds_count` | `endpoint`, `provider`, `model` | Request count (divide sum by count for the mean) |
| `llmproxy_usage_units_total` | `provider`, `model`, `unit` | Consumed units |
| `llmproxy_unpriced_units_total` | `model`, `unit` | Units that had no price entry; alert on growth here to catch pricing gaps |

`model` is always the public alias. Sample output:

```
llmproxy_requests_total{endpoint="chat",model="qwen-72b",outcome="ok",provider="vllm-1"} 412
llmproxy_usage_units_total{model="qwen-72b",provider="vllm-1",unit="input_tokens"} 1.88234e+06
llmproxy_unpriced_units_total{model="e5-embed",unit="input_tokens"} 4.4021833e+07
```
