# Manage models

Three words used throughout:

- A **provider** is an upstream endpoint. See
  [providers.md](providers.md).
- A **model** binds a caller-facing name to one (provider, upstream model)
  pair, with a capability set and prices.
- A model can instead be a **name for another model**, inheriting everything
  from its target.

Caller-facing names are globally unique across all providers, so resolution is
never ambiguous. Everything here is also in the built-in UI under Admin,
Models.

Examples assume:

```bash
P=http://127.0.0.1:4000
ADMIN=lp_...        # an admin key
```

## Discover what a provider serves

```bash
curl -s $P/admin/v1/providers/vllm-1/discover -H "authorization: Bearer $ADMIN"
```

```json
{
  "provider": "vllm-1",
  "discovered_at": "2026-07-29T09:15:40.582211Z",
  "models": [
    {"upstream_name": "Qwen/Qwen2.5-VL-72B-Instruct", "bound_alias": null},
    {"upstream_name": "intfloat/e5-mistral-7b-instruct", "bound_alias": "e5-embed"}
  ]
}
```

Discovery asks the provider's `/models` endpoint and annotates each name with
any existing binding. It is read-only and creates nothing.

Failures: 502 `provider_unreachable` if the upstream cannot be reached, 502
`discovery_failed` if it answers with an error status or invalid JSON.

## Bind a model

```bash
curl -s $P/admin/v1/models -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{
    "alias": "qwen-72b",
    "provider": "vllm-1",
    "upstream_name": "Qwen/Qwen2.5-VL-72B-Instruct",
    "capabilities": ["chat", "chat_stream"],
    "pricing": {"input_tokens": 0.4, "output_tokens": 1.2}
  }'
```

```json
{
  "alias": "qwen-72b",
  "provider": "vllm-1",
  "upstream_name": "Qwen/Qwen2.5-VL-72B-Instruct",
  "capabilities": ["chat", "chat_stream"],
  "origin": "declared",
  "created_at": "2026-07-29T09:16:12.007843Z",
  "pricing": {"input_tokens": 0.4, "output_tokens": 1.2},
  "pricing_inherited": false
}
```

The binding serves callers as soon as it exists.

| Field | Required | Meaning |
|---|---|---|
| `alias` | no | Matches `^[A-Za-z0-9][A-Za-z0-9._:/-]*$`, max 200 chars. Omit it and the model serves under `upstream_name`, which is what you want when exposing a provider's catalogue as-is |
| `provider` | yes | Registered provider name (unless `target` is given) |
| `upstream_name` | yes | Model name as the upstream knows it (unless `target` is given) |
| `capabilities` | no | Defaults to `["chat", "chat_stream"]` |
| `pricing` | no | Price per million units. See [pricing.md](pricing.md) |
| `target` | no | Another model to point at instead of a provider. See below |

Binding an already-used name gets 409 `alias_exists`.

### Capabilities

Capabilities are declared, never probed: state what the deployment actually
does. A request to an endpoint outside the set fails at the proxy with 400
`endpoint_not_supported` naming the supported set, instead of a confusing
upstream 404.

| Capability | Required for |
|---|---|
| `chat` | `POST /v1/chat/completions` |
| `chat_stream` | The same with `"stream": true` |
| `completions` | `POST /v1/completions` |
| `embeddings` | `POST /v1/embeddings` |
| `transcription` | Reserved. The capability and the `audio_seconds` unit exist; `/v1/audio/transcriptions` is not served yet |
| `vision` | Nothing. Declarative only: it tells callers the model reads image parts |

An embeddings model is `"capabilities": ["embeddings"]`.

## Point a name at another model

Give `target` instead of `provider` and `upstream_name`:

```bash
curl -s $P/admin/v1/models -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' \
  -d '{"alias": "acme/smart", "target": "z-ai/glm-5.2"}'
```

```json
{
  "alias": "acme/smart",
  "target": "z-ai/glm-5.2",
  "provider": "tensorx",
  "upstream_name": "z-ai/glm-5.2",
  "capabilities": ["chat", "chat_stream"],
  "pricing": {"input_tokens": 0.6, "output_tokens": 2.2},
  "pricing_inherited": true
}
```

The name inherits its target's provider, upstream model, capabilities and
prices. Reads always report the resolved values, and `target` says where they
came from (`null` for a model that routes to a provider itself). Repoint or
reprice the target and every name pointing at it follows.

Three rules, enforced at write time:

- **One hop.** A target must route to a provider itself. Pointing at another
  name is 400 `invalid_target`, and the error names the right one to use. A
  model cannot point at itself.
- **A name owns its name and its price, nothing else.** Sending
  `capabilities`, `upstream_name` or `provider` for one is 400
  `invalid_target` rather than a silent no-op. Setting `pricing` overrides
  what it would inherit, per unit.
- **A target cannot be deleted out from under its names:** 409
  `model_in_use`, listing them. Deleting the provider takes both with it.

Usage is recorded against the name the caller passed, so two teams pointing at
one model stay distinguishable in the summaries.

## Edit a model

`PATCH /admin/v1/models/{alias}` takes any of `alias`, `provider`,
`capabilities`, `upstream_name`, `target`, `pricing` and `hidden`. Everything
is editable in place; nothing here needs deleting and recreating.

```bash
# Retarget upstream and add a capability
curl -s -X PATCH $P/admin/v1/models/qwen-72b -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' \
  -d '{"upstream_name": "Qwen/Qwen3-72B", "capabilities": ["chat", "chat_stream", "completions"]}'

# Rename it, or move it to another provider
curl -s -X PATCH $P/admin/v1/models/qwen-72b -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' \
  -d '{"alias": "acme/smart", "provider": "vllm-2"}'
```

`capabilities` and `pricing` each replace the whole set when present: a unit
left out of `pricing` has no price afterwards.

A rename keeps the row, so the model stays one thing across the change. Its
prices follow the new name, names pointing at it keep pointing at it, and the
old name stops resolving at once (callers using it get `model_not_found`).
Renaming onto a name already bound is 409 `alias_exists`. Usage already
recorded keeps the name it was served under.

To turn a name back into a binding of its own, send an empty `target` with a
provider and upstream name in the same call:

```bash
curl -s -X PATCH $P/admin/v1/models/acme%2Fsmart -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' \
  -d '{"target": "", "provider": "vllm-1", "upstream_name": "Qwen/Qwen3-72B"}'
```

## Hide a model

`hidden` takes a model off `GET /v1/models`, and so out of the Playground
picker, without changing anything else:

```bash
curl -s -X PATCH $P/admin/v1/models/qwen-72b -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"hidden": true}'
```

This is decluttering, not access control. A hidden model still serves requests
under its name, and any key or session holder can list hidden models with
`GET /v1/models?include_hidden=1`, which is what the Usage tab's Models subtab
does behind its "Show hidden" toggle.

The flag belongs to the row it is set on, so a name can be hidden while its
target stays listed. To stop a model serving, delete it or disable its
provider.

## Check what a name resolves to

`GET /admin/v1/resolve` answers "what would this request do" without calling
anything, using the same resolution logic and cache as the data plane:

```bash
curl -s "$P/admin/v1/resolve?model=qwen-72b&endpoint=chat&stream=true" \
  -H "authorization: Bearer $ADMIN"
```

```json
{
  "alias": "qwen-72b",
  "provider": "vllm-1",
  "upstream_name": "Qwen/Qwen2.5-VL-72B-Instruct",
  "url": "http://10.0.0.5:8000/v1/chat/completions",
  "capabilities": ["chat", "chat_stream", "completions"]
}
```

`endpoint` defaults to `chat`; `stream=true` additionally requires
`chat_stream`. `url` reflects any per-endpoint override. The failure modes are
the data-plane ones: 404 `model_not_found` (unknown name or disabled provider)
and 400 `endpoint_not_supported`.

## Delete a model

```bash
curl -s -X DELETE $P/admin/v1/models/qwen-72b -H "authorization: Bearer $ADMIN"
# {"deleted": "qwen-72b"}
```

Usage history survives deletion.

## Where next

- [pricing.md](pricing.md) to attach prices.
- [usage.md](usage.md) to see what the models cost.
- [providers.md](providers.md) for the upstream side.
