# Set up SSO

Setting `LLMPROXY_OIDC_ISSUER` switches the proxy from local single-admin mode
to SSO mode. It works with any OIDC provider that exposes standard discovery
(`/.well-known/openid-configuration`) and a userinfo endpoint. Authentik is
walked through below.

SSO mode implies a TLS reverse proxy in front, because the redirect URL must
be externally reachable over HTTPS.

## Configuration

| Variable | Required | Meaning |
|---|---|---|
| `LLMPROXY_OIDC_ISSUER` | yes | Issuer URL. Setting it enables SSO mode |
| `LLMPROXY_OIDC_CLIENT_ID` | yes | OAuth client id |
| `LLMPROXY_OIDC_CLIENT_SECRET` | yes | OAuth client secret |
| `LLMPROXY_OIDC_REDIRECT_URL` | yes | Absolute callback URL, `https://your-proxy/auth/callback` |
| `LLMPROXY_OIDC_SCOPES` | no | Default `openid profile email` |
| `LLMPROXY_OIDC_GROUPS_CLAIM` | no | Userinfo claim read as a string array, default `groups` |
| `LLMPROXY_OIDC_ADMIN_GROUP` | no | Members get the `admin` role. Unset means no SSO user is ever auto-promoted |
| `LLMPROXY_OIDC_REQUIRED_GROUP` | no | When set, users outside it are rejected at the callback with 403 `sso_group_required`, and no principal is created |
| `LLMPROXY_SESSION_TTL` | no | Browser session lifetime, default `12h` |

The server refuses to boot if the first four are not all set. It performs OIDC
discovery at boot and fails fast with a clear error if the issuer is wrong or
unreachable.

The loopback guard does not apply in SSO mode, so `LLMPROXY_ALLOW_NONLOCAL` is
unnecessary.

## Authentik walkthrough

Assume the proxy is reachable at `https://llmproxy.example.com` and Authentik
at `https://auth.example.com`.

### 1. Create the provider

In the Authentik admin UI: Applications, Providers, Create, type "OAuth2/OpenID
Provider".

- Client type: **Confidential**.
- Redirect URI: `https://llmproxy.example.com/auth/callback` (exact match).
- Scopes: the defaults (`openid`, `profile`, `email`) are enough. Authentik
  includes the `groups` claim with the `profile` scope, so the proxy's
  defaults work without a custom scope mapping.

Note the generated client ID and client secret.

### 2. Create the application

Applications, Create: name it (for example "LLM Proxy"), set the slug (for
example `llmproxy`), and select the provider from step 1.

The slug determines the issuer URL:
`https://auth.example.com/application/o/llmproxy/`. Confirm it:

```bash
curl -s https://auth.example.com/application/o/llmproxy/.well-known/openid-configuration | head -3
```

### 3. Create the groups

Create `llm-admins` and `llm-users` in Authentik and add users. An admin who
is also a user belongs in both: `llm-admins` grants the role, `llm-users`
passes the gate.

If you use a required group, bind it to the application in Authentik as well,
so users outside it do not see the application in their library. The proxy
enforces the gate either way.

### 4. Configure the proxy

```bash
LLMPROXY_OIDC_ISSUER=https://auth.example.com/application/o/llmproxy/
LLMPROXY_OIDC_CLIENT_ID=<client id from step 1>
LLMPROXY_OIDC_CLIENT_SECRET=<client secret from step 1>
LLMPROXY_OIDC_REDIRECT_URL=https://llmproxy.example.com/auth/callback

# Group mapping (Authentik group names, case-sensitive)
LLMPROXY_OIDC_ADMIN_GROUP=llm-admins        # members become proxy admins
LLMPROXY_OIDC_REQUIRED_GROUP=llm-users      # everyone else is rejected
```

Restart the proxy.

### 5. Check it

Visit `https://llmproxy.example.com/` and click "Sign in with SSO". You should
land back on the built-in UI, signed in.

A member sees their own keys and usage. An `llm-admins` member additionally
sees usage by user, the request log, and the Admin tab: providers, models,
users, services and all keys.

## What users do next

From the UI, a user mints an API key (shown once, `***xxxx` afterwards) and
points any OpenAI SDK at `https://llmproxy.example.com/v1`. A key inherits the
principal's role, so a UI-minted key of an `llm-admins` member is an admin
key.

## Keep the break-glass password

Password login for the local admin stays available in SSO mode unless
`LLMPROXY_ADMIN_PASSWORD_DISABLED=1` is set, so an IdP outage cannot lock you
out of your own proxy. See
[the admin password](../reference/configuration.md#the-admin-password).

## How identity is handled

Four facts that matter operationally:

- **Principals are keyed on the IdP `sub` claim.** On first login a principal
  is created with a name derived from `preferred_username` (falling back to
  `nickname`, `name`, then email), sanitised and suffixed on collision. The
  name, its keys and its usage history stay attached to that `sub`.
- **Identity comes from userinfo**, fetched server-to-server after the code
  exchange. There is no local JWT validation.
- **Roles are reconciled at login only.** A group change in the IdP takes
  effect at the user's next sign-in.
- **Sessions are server-side rows and die on deletion.** The cookie holds an
  opaque token; the server stores only its keyed hash. `GET /auth/logout`
  deletes the caller's row, so even a stolen copy of the cookie stops working.
  `POST /admin/v1/principals/{id}/revoke-sessions` deletes every session of a
  principal and leaves API keys untouched.

Upgrading does not log anyone out: stateless session cookies from releases
before the session table are accepted until they expire.

## Offboard a user

Removing a user at the IdP is not enough on its own. Roles are reconciled at
login, and API keys need no login at all, so a removed admin's keys stay
admin-capable until that user next signs in.

The full sequence, with `P=https://llmproxy.example.com` and `ADMIN=lp_...`:

1. Remove the user at the IdP.
2. Revoke their sessions, which forces a fresh login that re-reconciles group
   membership:

   ```bash
   curl -s -X POST $P/admin/v1/principals/<id>/revoke-sessions \
     -H "authorization: Bearer $ADMIN"
   ```

3. Delete their keys:

   ```bash
   curl -s "$P/admin/v1/keys?principal=alice" -H "authorization: Bearer $ADMIN"
   curl -s -X DELETE $P/admin/v1/keys/<id> -H "authorization: Bearer $ADMIN"
   ```

For a demotion rather than a departure, steps 1 and 2 are enough, as long as
the user's existing keys should keep working as a member.

## Where next

- [api-keys.md](api-keys.md) for the key lifecycle.
- [../reference/configuration.md](../reference/configuration.md) for every
  variable.
