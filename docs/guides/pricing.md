# Set prices

Prices belong to the model, one price per unit, expressed per million units.
Without a price, usage is still recorded, but as unpriced rather than free.

Examples assume:

```bash
P=http://127.0.0.1:4000
ADMIN=lp_...     # an admin key
```

## Price one model

```bash
curl -s -X PATCH $P/admin/v1/models/qwen-72b -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' \
  -d '{"pricing": {"input_tokens": 0.4, "output_tokens": 1.2}}'
```

`pricing` is the model's complete price set: units left out are unpriced
afterwards, so the same call clears a price by omitting its unit.

Every model read returns the prices in force:

```json
{
  "alias": "qwen-72b",
  "upstream_name": "Qwen/Qwen2.5-72B-Instruct",
  "pricing": {"input_tokens": 0.4, "output_tokens": 1.2},
  "pricing_inherited": false
}
```

## The units

| Unit | Metered from |
|---|---|
| `input_tokens` | Prompt tokens |
| `output_tokens` | Completion tokens |
| `cached_input_tokens` | Cache reads: OpenAI's `prompt_tokens_details.cached_tokens`, Anthropic's `cache_read_input_tokens` |
| `cache_creation_tokens` | Anthropic cache writes, through the [relay](claude-code.md) |
| `audio_seconds` | Reserved for transcription |

Each unit prices at its own rate, which is the point of keeping cache reads
and writes separate from input.

## How a price is found

Prices resolve against the names a model is known by, most specific first:

1. Its own name.
2. The model it points at, if it is [a name for another
   model](models.md#point-a-name-at-another-model).
3. The upstream model name.

So a name inherits a price without repeating it, and overrides it per unit by
setting its own. `pricing_inherited` is true when the price in force is not
keyed on this name.

## Load a whole price list

Prices are a versioned feed keyed on `(model, unit)`, so a full list loads in
one call, or at startup from `LLMPROXY_PRICING_FILE`:

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

The format:

| Field | Rule |
|---|---|
| `version` | Required string |
| `entries[].model` | The public name or the upstream model name; lookup tries the public name first |
| `entries[].unit` | One of the units above |
| `entries[].price_per_million` or `price_per_unit` | Exactly one of the two. Per-million is divided by 1,000,000 on load |

Anything else is rejected with 400 `invalid_pricing_feed`.

Loading a feed replaces the active one atomically and applies to new requests
immediately. Already-recorded events are not repriced. Editing one model's
prices publishes a new version too, carrying every other model's entries over
unchanged.

Read the active feed with `GET /admin/v1/pricing`; it returns the version
(`null` if none), the entry count and the entries as per-million prices.

`LLMPROXY_PRICING_FILE` loads a feed at startup, and stores it only when its
`version` differs from the active one.

## Unpriced is not free

A `(model, unit)` pair with no entry is recorded as unpriced, never as zero:

- The quantity row gets `priced: false` and the event is flagged `unpriced`.
- Summaries show `cost: null` for fully unpriced groups.
- `/metrics` counts the volume under `llmproxy_unpriced_units_total`.

Alert on growth in that metric to catch pricing gaps.

## Where next

- [usage.md](usage.md) to read the resulting costs.
- [models.md](models.md) for the model each price belongs to.
