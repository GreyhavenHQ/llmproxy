# Manage providers

A provider is an upstream HTTP endpoint speaking a wire format. Everything on
this page is also available in the built-in UI under Admin, Providers; the UI
drives exactly the API shown here.

Examples assume:

```bash
P=http://127.0.0.1:4000
ADMIN=lp_...        # an admin key
```

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
  "max_concurrency": null,
  "enabled": true,
  "created_at": "2026-07-29T09:14:02.114523Z"
}
```

A duplicate name gets 409 `provider_exists`.

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | Matches `^[a-z0-9][a-z0-9._-]*$` |
| `wire_format` | no | `openai` (the default and, today, the only value) |
| `base_url` | yes | Full prefix including `/v1`. The proxy appends `/chat/completions`, `/completions`, `/embeddings`, `/audio/transcriptions` or `/models` |
| `api_key` | no | Sent upstream as `Authorization: Bearer`. Omit for unauthenticated upstreams |
| `verify_tls` | no | Default `true` |
| `ca_pem` | no | PEM bundle replacing the system trust pool for this provider |
| `timeout_connect` | no | Seconds, default 10 |
| `timeout_read` | no | Seconds, default 300. Also the deadline for unary requests; streams are unbounded and end when the upstream or the caller does |
| `max_concurrency` | no | Connections per upstream host, unlimited when omitted. Only bounds request concurrency on HTTP/1.1 upstreams, since HTTP/2 multiplexes |
| `endpoints` | no | Per-endpoint URL overrides; see below |

The upstream credential is stored AES-256-GCM encrypted and is never returned
by any endpoint. Responses carry `has_credential` instead.

## Edit a provider

`PATCH /admin/v1/providers/{name}` takes `enabled`, `base_url`, `api_key`,
`remove_credential`, `verify_tls`, `timeout_connect`, `timeout_read` and
`max_concurrency` (zero clears the cap).

```bash
curl -s -X PATCH $P/admin/v1/providers/vllm-1 -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"base_url": "http://10.0.0.6:8000/v1"}'
```

`endpoints` is not editable; recreate the provider to change an override.

## Rotate the upstream credential

```bash
# Rotate
curl -s -X PATCH $P/admin/v1/providers/vllm-1 -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"api_key": "new-upstream-secret"}'

# Remove (the upstream no longer requires auth)
curl -s -X PATCH $P/admin/v1/providers/vllm-1 -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"remove_credential": true}'
```

Sending `"api_key": ""` is the same as `remove_credential: true`. Rotation
takes effect immediately: the catalog cache and the upstream connection pool
are flushed on every catalog mutation.

## Use an internal certificate

For upstreams with a private CA, pass the bundle at registration:

```bash
curl -s $P/admin/v1/providers -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{
    "name": "internal-node",
    "base_url": "https://gpu-1.internal:8443/v1",
    "ca_pem": "-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----\n"
  }'
```

`ca_pem` replaces the system trust pool for this provider only. Responses
report `has_custom_ca`, never the PEM itself.

`"verify_tls": false` disables verification entirely. Prefer `ca_pem`.

## Override individual endpoints

When one logical provider serves different endpoint families from different
hosts, give absolute URLs at registration time:

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

Valid keys are `chat`, `completions`, `embeddings` and `transcription`. The
override is the exact URL called, path included. An unknown key gets 400
`invalid_endpoint`, a non-http(s) value 400 `invalid_override`.

Overrides are returned by `GET /admin/v1/providers/{name}` and cannot be
changed with PATCH.

## Take a provider out of service

Disabling stops every one of its bindings resolving and deletes nothing:

```bash
curl -s -X PATCH $P/admin/v1/providers/vllm-1 -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"enabled": false}'
```

Callers asking for one of its models get 404 `model_not_found`. Re-enable with
`{"enabled": true}`.

## Delete a provider

```bash
curl -s -X DELETE $P/admin/v1/providers/vllm-1 -H "authorization: Bearer $ADMIN"
# {"deleted": "vllm-1"}
```

Deletion cascades in one transaction: every model binding and endpoint
override goes with it. Usage history is untouched, because usage events store
copied identifiers rather than foreign keys into the catalog.

Disable instead of deleting if you might want the provider back.

## Where next

- [models.md](models.md) to bind the provider's models.
- [../reference/api.md](../reference/api.md) for the full endpoint list,
  pagination and the audit trail.
