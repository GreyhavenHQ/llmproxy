# Configuration

All configuration is environment-driven with an `LLMPROXY_` prefix. There is
no config file. A malformed value (for example a non-numeric port) silently
falls back to the default, so double-check values when something looks like it
was ignored.

## Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `LLMPROXY_DATABASE_URL` | `llmproxy.db` | SQLite file path, or a `postgres://` URL. See below. |
| `LLMPROXY_KEY_SECRET` | empty | Explicit HMAC / encryption secret. When empty, a random secret is generated and stored in the secret file. |
| `LLMPROXY_SECRET_FILE` | `.llmproxy/secret` | Where the generated secret lives (created mode 0600 on first run). Ignored when `LLMPROXY_KEY_SECRET` is set. |
| `LLMPROXY_HOST` | `127.0.0.1` | Bind address. |
| `LLMPROXY_PORT` | `4000` | Bind port. |
| `LLMPROXY_ALLOW_NONLOCAL` | `false` | Allow binding a non-loopback address in local (no-SSO) mode. Accepts Go bool syntax (`1`, `true`, ...). Has no effect in SSO mode. |
| `LLMPROXY_OIDC_ISSUER` | empty | OIDC issuer URL. Unset means local single-admin mode; set means SSO mode. |
| `LLMPROXY_OIDC_CLIENT_ID` | empty | OAuth client id. Required in SSO mode. |
| `LLMPROXY_OIDC_CLIENT_SECRET` | empty | OAuth client secret. Required in SSO mode. |
| `LLMPROXY_OIDC_REDIRECT_URL` | empty | Absolute callback URL, `https://your-proxy/auth/callback`. Required in SSO mode. An `https://` value also marks session cookies `Secure` (as does any request arriving over TLS or with `X-Forwarded-Proto: https`), and its host is accepted by the same-origin check when a reverse proxy rewrites `Host`. |
| `LLMPROXY_OIDC_SCOPES` | `openid profile email` | Space-separated scopes requested from the IdP. |
| `LLMPROXY_OIDC_GROUPS_CLAIM` | `groups` | Userinfo claim (a string array) read for group membership. |
| `LLMPROXY_OIDC_ADMIN_GROUP` | empty | Members of this group get the `admin` role, reconciled on every login. Empty means SSO users are never auto-promoted to admin. |
| `LLMPROXY_OIDC_REQUIRED_GROUP` | empty | When set, users not in this group cannot log in at all (403 `sso_group_required`). |
| `LLMPROXY_SESSION_TTL` | `12h` | Browser session lifetime (Go duration). Sessions are server-side rows; logout and `POST /admin/v1/principals/{id}/revoke-sessions` invalidate them immediately by deletion. |
| `LLMPROXY_LOCAL_ADMIN_NAME` | `local-admin` | Name of the admin principal bootstrapped in local mode. |
| `LLMPROXY_ADMIN_PASSWORD` | empty | Browser password for the local admin principal. When empty, a random one is generated at first boot and written to the password file. See below. |
| `LLMPROXY_ADMIN_PASSWORD_FILE` | `.llmproxy/admin-password` | Where the generated admin password lives (created mode 0600). Ignored when `LLMPROXY_ADMIN_PASSWORD` is set. |
| `LLMPROXY_ADMIN_PASSWORD_DISABLED` | `false` | Disable password login entirely, for SSO-only deployments. |
| `LLMPROXY_CATALOG_TTL` | `5s` | Alias-resolution cache TTL (Go duration). Catalog mutations through the admin API invalidate the cache immediately; this TTL is the staleness bound for changes made directly in the database or from another instance. |
| `LLMPROXY_MAX_BODY_BYTES` | `10485760` (10 MiB) | Request body ceiling on the `/v1` proxy endpoints. Oversize requests get 413 `request_too_large`. |
| `LLMPROXY_MAX_EMBEDDING_BATCH` | `2048` | Maximum items in an embeddings `input` array. `0` or negative disables the check. |
| `LLMPROXY_PRICING_FILE` | empty | Path to a pricing feed JSON loaded at startup. Stored (and activated) only when its `version` differs from the currently active feed. Format in [keys-and-usage.md](keys-and-usage.md#pricing). |
| `LLMPROXY_TRANSPARENT_ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | Target of the [transparent Anthropic relay](transparent-relay.md). Empty disables the relay. |
| `LLMPROXY_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. At `info`, every request logs one line (method, path, status, duration); health checks and static assets only appear at `debug`. Client and server errors log at `warn`/`error`, so they stay visible at higher levels. Logs go to stderr and never contain request or response content. |

Durations use Go syntax: `5s`, `90m`, `12h`, `1h30m`.

## DATABASE_URL forms

Anything starting with `postgres://` or `postgresql://` selects Postgres (via
pgx, max 20 connections):

```
LLMPROXY_DATABASE_URL=postgres://llmproxy:secret@db.internal:5432/llmproxy
```

Everything else is treated as a SQLite file path (an optional `sqlite://`
prefix is stripped). SQLite is opened with WAL journaling, a 30 s busy
timeout, `synchronous=NORMAL` and max 8 connections:

```
LLMPROXY_DATABASE_URL=/var/lib/llmproxy/llmproxy.db
```

The schema is identical on both backends and is created idempotently on boot.

## The secret

One secret underpins all cryptography in the proxy. It is resolved once at
startup: `LLMPROXY_KEY_SECRET` verbatim if set, otherwise the contents of
`LLMPROXY_SECRET_FILE`, generated (32 random bytes) on first run.

What it protects, via derived keys:

- API key verification. Caller keys are stored only as HMAC-SHA256 digests
  under this secret. A stolen database cannot be used to recover or verify
  keys without it.
- Provider credentials. Upstream API keys must be recoverable (they are
  forwarded upstream), so they are encrypted with AES-256-GCM under a key
  derived from the secret.
- Session cookies. The browser session cookie holds an opaque random token
  stored server-side only as a keyed hash, like API keys. (The OAuth state
  cookie and pre-upgrade session cookies are signed tokens under another
  derived key.)

Consequences:

- Losing the secret: every API key stops validating and every stored provider
  credential becomes undecryptable. Recovery means minting new keys (the CLI
  still works, since it writes to the database directly) and re-entering each
  provider's `api_key` via `PATCH /admin/v1/providers/{name}`. The database
  contents (principals, models, usage history) survive.
- Rotating the secret has exactly the same effect as losing it; there is no
  dual-key rotation mechanism. Treat rotation as a planned re-keying event.
- The CLI and the server must see the same secret (same env or same secret
  file), or CLI-minted keys will not validate against the server.

Back the secret up separately from the database. If both leak together, the
attacker can verify keys offline and decrypt provider credentials.

## The admin password

The built-in UI offers a password login for the local admin principal, with or
without SSO configured. In SSO mode it is the break-glass path: an IdP outage
cannot lock you out of your own proxy.

Resolution order: `LLMPROXY_ADMIN_PASSWORD` verbatim if set, otherwise the
contents of `LLMPROXY_ADMIN_PASSWORD_FILE`, generated on first boot. The
password is compared in constant time, never stored in the database, and a
failed attempt is delayed by 400 ms as a blunt brute-force damper (put real
rate limiting in your reverse proxy if the login page is internet-facing).
To rotate a generated password, delete the file and restart. To run SSO-only,
set `LLMPROXY_ADMIN_PASSWORD_DISABLED=1`.

A successful login issues the same server-side session as SSO. Sessions
carry the principal's role: the local admin's session can use the full admin
API (mutations are origin-checked), which is what the built-in UI does.

## CLI flags

`llmproxy serve` accepts three flags that override the corresponding
environment variables:

| Flag | Overrides |
|---|---|
| `-host <addr>` | `LLMPROXY_HOST` |
| `-port <n>` | `LLMPROXY_PORT` |
| `-allow-nonlocal` | `LLMPROXY_ALLOW_NONLOCAL` (sets it true; there is no flag to force it false) |

Everything else is environment-only. The `key` and `principal` subcommands
take no server flags; they read the same environment (`LLMPROXY_DATABASE_URL`,
`LLMPROXY_KEY_SECRET`, `LLMPROXY_SECRET_FILE`, `LLMPROXY_LOCAL_ADMIN_NAME`)
to find the database and secret.
