# Providers and models

Provider registration, upstream model discovery, model binding and editing,
pricing and deletion are all available in the built-in UI at `/` for admins
signed in via SSO or the admin password; the UI drives exactly the API below.
This document covers that admin JSON API directly. Examples assume:

```bash
P=http://127.0.0.1:4000
ADMIN=lp_...        # an admin key
```

## The model

A **provider** is an upstream HTTP endpoint speaking a wire format. Only the
`openai` wire format exists today. `base_url` is the full prefix including
`/v1` (for example `http://10.0.0.5:8000/v1`); the proxy appends
`/chat/completions`, `/completions`, `/embeddings`, `/audio/transcriptions` or
`/models` to it. The upstream credential, if any, is stored AES-256-GCM
encrypted and is never returned by any endpoint; responses only carry
`has_credential: true`.

A **model binding** maps a caller-facing **alias** to exactly one
(provider, upstream model name) pair. Aliases are globally unique across all
providers, enforced by a unique constraint, so resolution can never be
ambiguous and there is no fallback to bare upstream names. Each binding
carries:

- a **capability set**, admin-declared, a non-empty subset of `chat`,
  `chat_stream`, `completions`, `embeddings`, `transcription`. Capabilities
  are not probed or inferred; you declare what the deployment actually does.
  A chat request needs `chat` (plus `chat_stream` if `stream: true`); a
  request to an endpoint outside the set fails at the proxy with 400
  `endpoint_not_supported` naming the supported set, instead of a confusing
  upstream 404. `transcription` is reserved: the capability and the
  `audio_seconds` billing unit exist, but `/v1/audio/transcriptions` is not
  served yet.
- an **origin**, `declared` or `discovered`, recording where the binding came
  from. Informational only.

## Register a provider

```bash
curl -s $P/admin/v1/providers -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{
    "name": "vllm-1",
    "wire_format": "openai",
    "base_url": "http://10.0.0.5:8000/v1",
    "api_key": "upstream-secret"
  }'
```

```json
{
  "name": "vllm-1",
  "wire_format": "openai",
  "base_url": "http://10.0.0.5:8000/v1",
  "has_credential": true,
  "verify_tls": true,
  "has_custom_ca": false,
  "timeout_connect": 10,
  "timeout_read": 300,
  "timeout_write": 30,
  "max_concurrency": null,
  "enabled": true,
  "created_at": "2026-07-29T09:14:02.114523Z"
}
```

`name` must match `^[a-z0-9][a-z0-9._-]*$`. `api_key` is optional for
unauthenticated upstreams and is sent upstream as `Authorization: Bearer`.
Timeouts are seconds; `timeout_read` doubles as the deadline for unary
requests (streams are unbounded by design and end when the upstream or caller
does). `max_concurrency` caps connections per upstream host; omitted means
unlimited. A duplicate name gets 409 `provider_exists`.

### TLS options

For upstreams with internal certificates:

```bash
curl -s $P/admin/v1/providers -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{
    "name": "internal-node",
    "base_url": "https://gpu-1.internal:8443/v1",
    "ca_pem": "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n"
  }'
```

`ca_pem` replaces the system trust pool with the given PEM bundle for this
provider. `"verify_tls": false` disables verification entirely; prefer
`ca_pem`. Responses report `has_custom_ca`, never the PEM itself.

### Per-endpoint URL overrides

When one logical provider serves different endpoint families from different
hosts, override individual endpoints with absolute URLs at registration time:

```bash
curl -s $P/admin/v1/providers -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{
    "name": "mixed",
    "base_url": "http://chat-host:8000/v1",
    "endpoints": {
      "embeddings": "http://embed-host:8001/v1/embeddings"
    }
  }'
```

Valid keys are `chat`, `completions`, `embeddings`, `transcription`; an
unknown key gets 400 `invalid_endpoint`, a non-http(s) value 400
`invalid_override`. The override is the exact URL called, path included.
Overrides are set at creation and returned by `GET
/admin/v1/providers/{name}`; they are not editable via PATCH (recreate the
provider to change them).

## Discover upstream models

Discovery asks the provider's `/models` endpoint what it serves and annotates
each name with any existing binding. It is read-only and never exposes or
creates anything.

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

Failures: 502 `provider_unreachable` if the upstream cannot be reached, 502
`discovery_failed` if it answers with an error status or invalid JSON.

## Bind a model

A binding serves callers as soon as it exists; taking it out of service means
deleting it, or disabling its provider.

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

`alias` must match `^[A-Za-z0-9][A-Za-z0-9._:/-]*$` (max 200 chars). Leave it
out and the model serves under `upstream_name`, which is what you want when
you are just exposing a provider's catalogue as-is; give one when callers
should see a name of your choosing. `capabilities` defaults to
`["chat", "chat_stream"]`. Binding an already-used alias gets 409
`alias_exists`. An embeddings model would be `"capabilities": ["embeddings"]`.

`pricing` is optional and holds the price per million units for each unit the
model meters (`input_tokens`, `output_tokens`, `cached_input_tokens`,
`audio_seconds`). A unit with no price records usage as unpriced, never as
free; see [keys-and-usage.md](keys-and-usage.md) for what that means downstream.

### Point a name at another model

A model can also be a name for a model already bound. Give `target` instead of
`provider` and `upstream_name`:

```bash
curl -s $P/admin/v1/models -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' \
  -d '{"alias": "monadical/smart", "target": "z-ai/glm-5.2"}'
```

```json
{
  "alias": "monadical/smart",
  "target": "z-ai/glm-5.2",
  "provider": "tensorx",
  "upstream_name": "z-ai/glm-5.2",
  "capabilities": ["chat", "chat_stream"],
  "pricing": {"input_tokens": 0.6, "output_tokens": 2.2},
  "pricing_inherited": true
}
```

The alias inherits its target's provider, upstream model, capabilities and
prices; `provider`, `upstream_name` and `capabilities` in any read are always
the resolved ones, and `target` says where they came from (`null` for a model
that routes to a provider itself). Repoint or reprice the target and every
name pointing at it follows, which is the point: `monadical/smart` is a
promise to your callers, and what fulfils it is yours to change.

Rules, all enforced at write time:

- **One hop.** A target must route to a provider itself. Pointing at another
  alias is 400 `invalid_target`, and names it so you can point at the right
  one; a model cannot point at itself.
- **What an alias owns is its name and its price.** Sending `capabilities`,
  `upstream_name` or `provider` for one is 400 `invalid_target` rather than a
  silent no-op. Setting `pricing` on it overrides what it would inherit, per
  unit, which is how an internal name charges differently from the model
  behind it.
- **A target cannot be deleted out from under its aliases**: 409
  `model_in_use`, listing them. Deleting the provider takes both with it.

Usage is recorded against the name the caller passed, so two teams pointing at
one model stay distinguishable in the summaries.

### Edit a binding

`PATCH /admin/v1/models/{alias}` takes any of `alias`, `provider`,
`capabilities`, `upstream_name`, `target`, `pricing`. Everything about a
binding is editable in place: nothing here needs deleting and recreating.

```bash
# Retarget the alias and add the completions capability
curl -s -X PATCH $P/admin/v1/models/qwen-72b -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' \
  -d '{"upstream_name": "Qwen/Qwen3-72B", "capabilities": ["chat", "chat_stream", "completions"]}'

# Reprice it
curl -s -X PATCH $P/admin/v1/models/qwen-72b -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' \
  -d '{"pricing": {"input_tokens": 0.5, "output_tokens": 1.5}}'

# Rename it, or move it to another provider
curl -s -X PATCH $P/admin/v1/models/qwen-72b -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' \
  -d '{"alias": "monadical/smart", "provider": "vllm-2"}'
```

`capabilities` and `pricing`, when present, each replace the whole set: a unit
left out of `pricing` has no price afterwards. The response is the updated
binding view.

A rename keeps the row, so the model stays one thing across the change: its
prices follow the new name, names pointing at it keep pointing at it, and the
old name stops resolving at once (callers using it get `model_not_found`).
Renaming onto a name already bound is rejected with 409 `alias_exists`. Usage
already recorded keeps the name it was served under, so history stays truthful
about what callers asked for.

`target` switches a model between the two kinds. Sending an empty `target`
turns an alias back into a binding of its own, which needs `provider` and
`upstream_name` in the same call:

```bash
curl -s -X PATCH $P/admin/v1/models/monadical%2Fsmart -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' \
  -d '{"target": "", "provider": "vllm-1", "upstream_name": "Qwen/Qwen3-72B"}'
```

Similarly for providers, `PATCH /admin/v1/providers/{name}` takes `enabled`,
`base_url`, `api_key` and `remove_credential`:

```bash
# Drain a provider: all its bindings stop resolving, nothing is deleted
curl -s -X PATCH $P/admin/v1/providers/vllm-1 -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"enabled": false}'
```

## Dry-run resolution

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

`endpoint` defaults to `chat`; `stream=true` additionally requires the
`chat_stream` capability. Failure modes are exactly the data-plane ones: 404
`model_not_found` (unknown alias or disabled provider)
and 400 `endpoint_not_supported` (message lists the missing and supported
capabilities). `url` reflects any per-endpoint override.

## Credential rotation and removal

```bash
# Rotate the upstream key
curl -s -X PATCH $P/admin/v1/providers/vllm-1 -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"api_key": "new-upstream-secret"}'

# Remove it (upstream requires no auth anymore)
curl -s -X PATCH $P/admin/v1/providers/vllm-1 -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"remove_credential": true}'
```

Sending `"api_key": ""` is equivalent to `remove_credential: true`. Rotation
takes effect immediately (the catalog cache and the upstream connection pool
are flushed on every catalog mutation).

## Deleting

```bash
curl -s -X DELETE $P/admin/v1/models/qwen-72b -H "authorization: Bearer $ADMIN"
# {"deleted": "qwen-72b"}

curl -s -X DELETE $P/admin/v1/providers/vllm-1 -H "authorization: Bearer $ADMIN"
# {"deleted": "vllm-1"}
```

Deleting a provider cascades in one transaction: all its model bindings and
endpoint overrides go with it. Usage history is untouched (usage events store
copied identifiers, not foreign keys into the catalog). If you want the
option to come back, disable instead of delete.

## Audit trail

Every admin mutation writes a metadata-only event in the same transaction as
the change: `provider.create|update|delete`, `model.create|update|delete`,
`principal.create`, `key.create`, `key.delete`, `pricing.load`.

```bash
curl -s "$P/admin/v1/events?limit=3" -H "authorization: Bearer $ADMIN"
```

```json
{
  "events": [
    {
      "ts": "2026-07-29T09:20:01.334019Z",
      "actor_principal_id": "b1f4c2d8e96a4f0b8d3a5c7e9f012345",
      "action": "model.update",
      "target_kind": "model",
      "target_ref": "qwen-72b"
    },
    {
      "ts": "2026-07-29T09:16:12.008120Z",
      "actor_principal_id": "b1f4c2d8e96a4f0b8d3a5c7e9f012345",
      "action": "model.create",
      "target_kind": "model",
      "target_ref": "qwen-72b"
    },
    {
      "ts": "2026-07-29T09:14:02.114901Z",
      "actor_principal_id": "b1f4c2d8e96a4f0b8d3a5c7e9f012345",
      "action": "provider.create",
      "target_kind": "provider",
      "target_ref": "vllm-1"
    }
  ],
  "limit": 3,
  "offset": 0
}
```

Events are newest first and carry identifiers only, never payloads. Direct
CLI operations (`llmproxy key create` etc.) bypass the HTTP layer and are not
audited.

## Pagination

All admin list endpoints (`providers`, `models`, `keys`, `principals`,
`events`) accept `limit` (1 to 500, default 100) and `offset` (default 0) and
echo both back in the response:

```bash
curl -s "$P/admin/v1/models?provider=vllm-1&limit=50&offset=50" \
  -H "authorization: Bearer $ADMIN"
```

Out-of-range values silently fall back to the defaults. Ordering is by name
or alias (keys by creation time, events newest first), so pagination is
stable between requests.
