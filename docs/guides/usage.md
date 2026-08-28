# Read usage and cost

Every `/v1` request that reaches resolution, and every relayed Anthropic
request, records one usage event. The Usage and Requests tabs of the built-in
UI draw the endpoints below; use the API directly for exports and alerting.

Examples assume:

```bash
P=http://127.0.0.1:4000
KEY=lp_...       # any key
ADMIN=lp_...     # an admin key
```

## What gets recorded

Identifiers, flags and numbers. **No prompt, completion or embedding input is
ever persisted**, and there is no flag to turn that on; see
[../concepts/architecture.md](../concepts/architecture.md#the-schema-is-content-free-by-construction).

Per event: principal, key, provider, model, upstream name, endpoint, client,
status code, outcome, error kind, cancelled and streamed flags, duration and
cost. Attached to it, one row per unit: `(unit, quantity, unit_price,
priced)`.

| Field | Values |
|---|---|
| `outcome` | `ok`, `upstream_error`, `unreachable`, `cancelled` |
| `error_kind` | A short token classifying a failure: the transport class on `unreachable` (`timeout`, `connection_error`, `response_too_large`), the upstream's `error.type`/`error.code` on `upstream_error` (`rate_limit_error`, `overloaded_error`, ...). Provider error *messages* are never stored, because they can echo request content |
| `client` | The caller's `User-Agent`, truncated to 256 characters and stored verbatim (`claude-cli/2.0.13 (external, cli)`, `openai-python/1.51.0`). Grouping by product family happens at read time |

Cancelled and errored requests are recorded too, with whatever partial usage
the upstream reported. Accounting runs detached from the request path, so a
client hanging up cannot lose the record.

Relay traffic lands in the same tables, attributed by relay token instead of
API key, under the sentinel provider `transparent:anthropic`.

**One number is normalised at read time.** Stored quantities stay exactly as
the upstream reported them, but every aggregate reports `input_tokens` as
input processed at the full input rate, so the number means the same thing
across OpenAI-shaped and Anthropic-shaped upstreams. `cached_input_tokens` is
always the cheap cache-read count on top of it. The mechanics are in
[../concepts/architecture.md](../concepts/architecture.md#usage-numbers-are-normalised-at-read-time).

## Tag requests by application

A key says who is calling. A tag says which of their applications. Callers
send comma-separated `key:value` pairs:

```
x-llmproxy-tags: app:dataindex,context:search
```

The header works on both ingresses, including the
[relay](claude-code.md), where the proxy consumes and strips it before
forwarding.

Values are normalised at capture so grouping is stable: each pair is trimmed
and lowercased, must match `[a-z0-9][a-z0-9._-]*:[a-z0-9][a-z0-9._-]*`, keys
are unique (first occurrence wins), pairs are sorted by key, and the list is
capped at 8 pairs and 256 bytes. Anything malformed is dropped silently:
telemetry never fails a request.

Events with no `app` tag are grouped as `untagged`, so no traffic drops out of
the breakdowns.

## Read your own usage

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

Grouped by (model, endpoint). `since` and `until` take ISO 8601 dates or
datetimes; `until` is exclusive.

`GET /admin/v1/usage/summary` is the same aggregation across everyone, each
row carrying a `principal`, optionally filtered with `?principal=<name>`.

## Read usage over time

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

`bucket` is `hour | day | week | month`, default `day`. Buckets are UTC, weeks
start Monday, and the range is gap-filled: quiet periods come back as empty
buckets rather than holes, so a series plots directly.

`ok`, `cancelled` and `failed` partition `requests`, where failed is any
non-ok outcome the caller did not cancel. `cost` is null when nothing in the
bucket had a price, and `unpriced_requests` counts requests that hit a missing
price.

Without `since`, the series starts at the first recorded event. A window
needing more than 1000 buckets is refused with 400 `range_too_large`.

`GET /admin/v1/usage/series` is the same across everyone.

## Query team-wide statistics

The `/stats` endpoints serve the same accounting to **every authenticated
user**. Usage is team-visible by design; the admin role gates configuration,
not visibility.

| Endpoint | Returns |
|---|---|
| `GET /stats/series` | The bucketed series above, with the filters below |
| `GET /stats/summary` | The window aggregated over every dimension: one row per (principal, provider, model, endpoint, client, tags), so any roll-up is a client-side sum |
| `GET /stats/requests` | One page of the request metadata log, newest first, failures included. `limit` (max 500) and `offset` page it; the response carries `{requests, limit, offset, total}` |
| `GET /stats/requests/facets` | The distinct principals, keys, providers, models, clients and tags in the window, for filter menus. Each list caps at 500 values |
| `GET /stats/errors` | The errors dashboard in one call: a gap-filled series of counts per outcome, plus a breakdown per (provider, model, endpoint, client, tags, outcome, error_kind, status_code) with request count, average duration, last-seen and time-to-outcome bands |

All of them take the same filters:

| Filter | Matches |
|---|---|
| `principal=<name>` | Exact |
| `key=<api key id>` | Exact. API keys only; relay events never match one, so reach them through `provider=transparent:anthropic` |
| `provider=<name>` | Exact, including the sentinel `transparent:anthropic`. A deleted provider's events resolve to `(deleted)` |
| `model=<name>` | Exact |
| `client=<prefix>` | Prefix on the stored User-Agent, so `client=claude-cli` covers every version |
| `tag=<key>:<value>` | An exact pair, repeatable up to four times, all of which must match |
| `outcome=<value>` | `ok`, `upstream_error`, `unreachable`, `cancelled`, or the meta value `failed` for everything not ok. An unknown value is 400 `invalid_outcome` |
| `since=`, `until=` | The window, as above |

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
      "tags": "app:dataindex,context:search",
      "requests": 3211,
      "cancelled": 12,
      "cost": 41.20734,
      "units": {"input_tokens": 311140, "output_tokens": 378512, "cached_input_tokens": 1571200}
    }
  ]
}
```

Two things to know when reading `/stats/summary`:

- It counts **completed requests with a model only**. A failure's model is
  whatever the caller asked for, and model-less events (the relay's
  `count_tokens` and `models` calls) have no place in a by-model breakdown.
  Both stay visible in the series and the request log.
- `/stats/errors` includes cells with outcome `ok`, so error rates per
  dimension have their denominator.

## Scrape metrics

`GET /metrics` is unauthenticated and exposes Prometheus text with aggregate
labels only. Per-principal detail stays in the database so metric cardinality
stays bounded.

| Metric | Labels | Meaning |
|---|---|---|
| `llmproxy_requests_total` | `endpoint`, `provider`, `model`, `outcome` | Requests by outcome |
| `llmproxy_request_seconds_sum` | `endpoint`, `provider`, `model` | Total request duration |
| `llmproxy_request_seconds_count` | `endpoint`, `provider`, `model` | Request count (divide sum by count for the mean) |
| `llmproxy_usage_units_total` | `provider`, `model`, `unit` | Consumed units |
| `llmproxy_unpriced_units_total` | `model`, `unit` | Units with no price entry; alert on growth to catch pricing gaps |

`model` is always the caller-facing name.

```
llmproxy_requests_total{endpoint="chat",model="qwen-72b",outcome="ok",provider="vllm-1"} 412
llmproxy_usage_units_total{model="qwen-72b",provider="vllm-1",unit="input_tokens"} 1.88234e+06
llmproxy_unpriced_units_total{model="e5-embed",unit="input_tokens"} 4.4021833e+07
```

## Where next

- [pricing.md](pricing.md) if costs come back `null`.
- [claude-code.md](claude-code.md) for relay accounting.
- [../reference/api.md](../reference/api.md) for the full endpoint list.
