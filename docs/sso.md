# SSO (OIDC)

Setting `LLMPROXY_OIDC_ISSUER` switches the proxy from local single-admin mode
to SSO mode. `LLMPROXY_OIDC_CLIENT_ID`, `LLMPROXY_OIDC_CLIENT_SECRET` and
`LLMPROXY_OIDC_REDIRECT_URL` are then required; the server refuses to boot
without them. It works with any OIDC provider that exposes standard discovery
(`/.well-known/openid-configuration`) and a userinfo endpoint; Authentik is
walked through below.

## How it works here

The proxy is a minimal OIDC relying party using the authorization code flow
plus a userinfo lookup. `GET /auth/login` sets a short-lived signed state
cookie and redirects to the IdP. The callback (`GET /auth/callback`) verifies
the state, exchanges the code for an access token (confidential client:
`client_id` and `client_secret` in the token request), and then calls the
IdP's userinfo endpoint server-to-server with that token. Identity comes
entirely from userinfo; there is no local JWT validation, the trust chain is
TLS to the IdP plus client authentication.

Principals are keyed on the IdP `sub` claim, which is the only stable join
key. On first login a principal is created with a name derived from
`preferred_username` (falling back to `nickname`, `name`, then email),
sanitized to the local naming rules and suffixed on collision. On every
subsequent login the email and the group-derived role are reconciled, so
group changes in the IdP take effect at the next sign-in; the principal name,
its keys and its usage history stay attached to the same `sub`.

Group handling is configuration, not convention:

- `LLMPROXY_OIDC_GROUPS_CLAIM` (default `groups`) names the userinfo claim
  read as a string array.
- `LLMPROXY_OIDC_ADMIN_GROUP`: members get the `admin` role, everyone else is
  a `member`. Unset means no SSO user is ever auto-promoted.
- `LLMPROXY_OIDC_REQUIRED_GROUP`: when set, users outside this group are
  rejected at the callback with 403 `sso_group_required` and no principal is
  created.

### Sessions

A successful login issues a session cookie (`llmproxy_session`). Sessions are
server-side, the standard model for first-party browser sessions: the cookie
holds an opaque random token, and the server stores only its keyed hash in
the `session` table (like API keys, a stolen database cannot forge one).
Lifetime is `LLMPROXY_SESSION_TTL` (default 12h). The cookie is `HttpOnly`,
marked `Secure` when the redirect URL is `https://` or the request itself
arrived over TLS (directly or via `X-Forwarded-Proto: https` from a
TLS-terminating reverse proxy), and `SameSite=Lax` rather than `Strict`
because it must survive the top-level redirect back from the IdP. To
compensate, every non-GET request authenticated by session is origin-checked:
an `Origin` header matching neither the request `Host` nor the host of
`LLMPROXY_OIDC_REDIRECT_URL` (the browser-facing name, for deployments where
the reverse proxy rewrites `Host`) is rejected with 403
`cross_origin_rejected`.

Two properties worth internalizing:

- Sessions carry the principal's role. An admin's session can call the
  `/admin/v1` API, which is how the built-in UI manages providers and models;
  every session mutation is origin-checked. Automation and scripts use API
  keys, which need no Origin header.
- Invalidation is deletion, immediately effective. `GET /auth/logout` deletes
  the caller's session row, so even a stolen copy of the cookie dies with it.
  `POST /admin/v1/principals/{id}/revoke-sessions` deletes every session of a
  principal, while API keys are untouched.

Signed stateless session cookies from releases before the session table are
still accepted until they expire (at most one `LLMPROXY_SESSION_TTL` after
the upgrade), so deploying the upgrade logs nobody out; revoke-sessions
covers them via a per-principal cutoff timestamp during that window.

## Authentik walkthrough

Assume the proxy is reachable at `https://llmproxy.example.com` (SSO mode
implies a TLS reverse proxy in front) and Authentik at
`https://auth.example.com`.

### 1. Create the provider

In the Authentik admin UI: Applications, Providers, Create, type
"OAuth2/OpenID Provider".

- Client type: **Confidential** (the proxy authenticates with a client
  secret at the token endpoint).
- Redirect URI: `https://llmproxy.example.com/auth/callback` (exact match).
- Scopes: the defaults (`openid`, `profile`, `email`) are enough. Authentik
  includes the `groups` claim with the `profile` scope, so the proxy's default
  `LLMPROXY_OIDC_SCOPES="openid profile email"` and
  `LLMPROXY_OIDC_GROUPS_CLAIM=groups` work without a custom scope mapping.

Note the generated client ID and client secret.

### 2. Create the application

Applications, Create: name it (for example "LLM Proxy"), set the slug (for
example `llmproxy`), and select the provider from step 1. The slug determines
the issuer URL: `https://auth.example.com/application/o/llmproxy/`. You can
confirm it by fetching
`https://auth.example.com/application/o/llmproxy/.well-known/openid-configuration`.

If you plan to use a required group, also bind that group to the application
in Authentik so users outside it do not even see it in their library. The
proxy enforces the gate itself either way.

### 3. Configure the proxy

```bash
LLMPROXY_OIDC_ISSUER=https://auth.example.com/application/o/llmproxy/
LLMPROXY_OIDC_CLIENT_ID=<client id from step 1>
LLMPROXY_OIDC_CLIENT_SECRET=<client secret from step 1>
LLMPROXY_OIDC_REDIRECT_URL=https://llmproxy.example.com/auth/callback

# Group mapping (Authentik group names, case-sensitive)
LLMPROXY_OIDC_ADMIN_GROUP=llm-admins        # members become proxy admins
LLMPROXY_OIDC_REQUIRED_GROUP=llm-users      # everyone else is rejected
```

Restart the proxy. It performs OIDC discovery at boot and fails fast with a
clear error if the issuer is wrong or unreachable. The loopback guard does not
apply in SSO mode, so no `LLMPROXY_ALLOW_NONLOCAL` is needed.

Create the Authentik groups (`llm-admins`, `llm-users`) and add users. An
admin who is also a user belongs in both groups: `llm-admins` grants the role,
`llm-users` passes the gate.

### 4. End-user flow

A user visits `https://llmproxy.example.com/`, clicks "Sign in with SSO", and
lands back on the built-in UI after authenticating with Authentik. There they
mint an API key (shown exactly once, listed as `***xxxx` afterwards), delete
keys, and see their own usage and cost per model and endpoint. The key goes into whatever OpenAI SDK they use, pointed at
`https://llmproxy.example.com/v1`.

An `llm-admins` member additionally sees the admin tabs of the UI: provider
and model management (including upstream model discovery), usage by user, and
the request metadata log. For scripted administration a key inherits the
principal's role, so a UI-minted key of an `llm-admins` member is an admin
key.

### Break-glass admin password

Password login for the local admin principal stays available in SSO mode
unless `LLMPROXY_ADMIN_PASSWORD_DISABLED=1` is set, so an IdP outage cannot
lock you out of the proxy. See
[configuration.md](configuration.md#the-admin-password).

### Role changes and offboarding

Roles live on the principal and are reconciled only at login. Removing a user
from `llm-users` blocks their next login, and removing them from `llm-admins`
demotes them at their next login, but neither touches their live sessions or
existing API keys. To make an IdP change effective now instead of at the next
sign-in, force that sign-in:
`POST /admin/v1/principals/{id}/revoke-sessions` invalidates the user's
browser sessions, and their next UI access goes through a fresh login that
re-reconciles group membership.

API keys keep working without any login, and they carry the principal's role
as last reconciled, so a removed admin's keys stay admin-capable until that
user signs in again. For offboarding, delete the keys explicitly: list them
with `GET /admin/v1/keys?principal=<name>` and delete with
`DELETE /admin/v1/keys/{id}`. The full offboarding sequence is: remove the
user at the IdP, revoke their sessions, delete their keys.
