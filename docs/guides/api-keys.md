# Manage API keys

Every API key belongs to a principal (a person or a service) and inherits that
principal's role. Keys start with `lp_`, are generated server-side, and are
shown exactly once: the database stores only a keyed hash plus the last 4
characters for display. There is no way to view a key again, only to mint a
new one.

Examples assume:

```bash
P=http://127.0.0.1:4000
KEY=lp_...       # any key
ADMIN=lp_...     # an admin key
```

## Mint a key for yourself

Any key or session holder manages their own keys, in the UI under Keys or
directly:

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

Copy `key` now. Listings show only `***Mq3w`.

```bash
curl -s $P/my/keys -H "authorization: Bearer $KEY"          # metadata only, max 500
```

## Mint a key for someone else

An admin sees and revokes every key in the UI under Admin, All keys, and mints
keys for any principal by name:

```bash
curl -s $P/admin/v1/keys -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"principal": "alice", "label": "onboarding"}'
```

The response is the same shape plus a `principal` field. List across
principals with `GET /admin/v1/keys?principal=alice`.

## Create a key for a service

For workloads, create a `service` principal first. The key then survives staff
turnover and shows up under its own name in usage. In the UI, Admin, Services
does both steps at once. Over the API:

```bash
curl -s $P/admin/v1/principals -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"name": "batch-service", "kind": "service"}'

curl -s $P/admin/v1/keys -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"principal": "batch-service", "label": "prod"}'
```

## Mint the first key

Before any HTTP credential exists, use the CLI. It operates directly on the
database:

```bash
llmproxy key create -label bootstrap
llmproxy key list
llmproxy key delete <id>
llmproxy principal create -name batch-service -kind service
```

Run it with the same working directory or environment as the server, or it
will write to a different database. See
[../install/binary.md](../install/binary.md#first-login).

## Revoke a key

Deletion is the revocation mechanism and takes effect on the next request:

```bash
curl -s -X DELETE $P/my/keys/8c1de2f4a6b84c33a1f09e7d5b246810 \
  -H "authorization: Bearer $KEY"
# {"deleted": "8c1de2f4a6b84c33a1f09e7d5b246810"}
```

Admins delete any key with `DELETE /admin/v1/keys/{id}`.

A deleted key authenticates like any unknown key: 401 `invalid_api_key`. Usage
history survives deletion, because usage events reference the key id, which is
never reused.

`last_used_at` is refreshed at most once per minute per key, so treat it as
coarse.

## Where next

- [usage.md](usage.md) to see what each key consumed.
- [sso.md](sso.md) for group-derived roles and the offboarding sequence.
- [claude-code.md](claude-code.md) for relay tokens, which are not API keys.
