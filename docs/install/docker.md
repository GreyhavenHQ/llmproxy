# Install with Docker

Tagged releases push a multi-arch (amd64 and arm64) image to the GitHub
Container Registry.

## Run the image

```bash
docker run -p 127.0.0.1:4000:4000 -v llmproxy-data:/data \
  -e LLMPROXY_ALLOW_NONLOCAL=1 -e LLMPROXY_ADMIN_PASSWORD=change-me \
  ghcr.io/greyhavenhq/llmproxy:1.0.0
```

```
llmproxy v1.0.0 listening on http://0.0.0.0:4000
```

Open <http://127.0.0.1:4000> and sign in with `change-me`.

Three things this command does deliberately:

- **Mounts `/data`.** All state lives there. Without a volume, the database,
  the key secret and the admin password vanish with the container.
- **Sets `LLMPROXY_ALLOW_NONLOCAL=1`.** The container binds `0.0.0.0`, which
  trips the loopback guard in local (no-SSO) mode; see [the loopback
  guard](binary.md#the-loopback-guard). Inside a container the real exposure
  boundary is the port mapping, so publish the port narrowly, as above. In SSO
  mode the guard does not apply and the variable is unnecessary.
- **Sets `LLMPROXY_ADMIN_PASSWORD`.** Without it a random password is
  generated into `/data/admin-password`, and the log only reports the file
  name. Read it with `docker exec <container> cat /data/admin-password`. See
  [the admin password](../reference/configuration.md#the-admin-password) to
  rotate it, and [the SSO guide](../guides/sso.md) to replace it with your
  identity provider.

Pin the exact version in production. `:latest` and the major tag `:1` move
under you; a prerelease tag moves neither.

## Use docker compose

`docker-compose.yml` in the repository is the local-mode setup: SQLite state
in a named volume, `LLMPROXY_ALLOW_NONLOCAL=1`, the admin password set to
`change-me`, and the port published only on the host's loopback.

1. Start it:

   ```bash
   docker compose up -d
   curl -s http://127.0.0.1:4000/healthz
   ```

   ```json
   {"status":"ok"}
   ```

2. Open <http://127.0.0.1:4000> and sign in with `change-me`. From the UI,
   register providers, bind models and mint keys.

Change `LLMPROXY_ADMIN_PASSWORD` in the compose file before exposing the port
beyond loopback, or remove it to have a random password generated into
`/data/admin-password` (read it with
`docker compose exec llmproxy cat /data/admin-password`).

To bootstrap over the API, mint a key through the container, which shares the
same `/data`:

```bash
docker compose exec llmproxy llmproxy key create -label bootstrap
```

```
key id:    9d0b...
principal: local-admin
api key:   lp_...
Store it now; it will not be shown again.
```

(The first `llmproxy` is the compose service name, the second is the binary.)

Widen the port mapping only after switching to SSO.

## Switch to SSO

Uncomment and fill in the `LLMPROXY_OIDC_*` variables in the compose file
(issuer, client id, client secret, redirect URL, optionally the admin and
required groups), then remove `LLMPROXY_ALLOW_NONLOCAL`. The redirect URL must
be the externally reachable `https://your-proxy/auth/callback`, which implies
a TLS reverse proxy in front. Full walkthrough in
[../guides/sso.md](../guides/sso.md).

## Switch to Postgres

The compose file contains a commented-out `db` service. Uncomment it, the
`llmproxy-pg` volume, and the `LLMPROXY_DATABASE_URL` line:

```yaml
services:
  llmproxy:
    environment:
      LLMPROXY_DATABASE_URL: postgres://llmproxy:change-me@db:5432/llmproxy

  db:
    image: postgres:17-alpine
    environment:
      POSTGRES_USER: llmproxy
      POSTGRES_PASSWORD: change-me
      POSTGRES_DB: llmproxy
    volumes:
      - llmproxy-pg:/var/lib/postgresql/data
    restart: unless-stopped

volumes:
  llmproxy-pg:
```

The schema is created on boot, same as with SQLite.

Two consequences:

- **There is no data migration between backends.** Switching starts from an
  empty database, so switch before you accumulate keys and usage you care
  about.
- **The `/data` volume still matters.** It holds the key secret, and existing
  API keys and encrypted provider credentials are only valid under it. Keep
  the volume, or set `LLMPROXY_KEY_SECRET` explicitly.

## Deploy on Coolify

`docker-compose.coolify.yml` targets Coolify or any compose-based PaaS. It
differs from the development file in two ways: Postgres is the default
backend, as a `db` service with a healthcheck the app waits on, and there is
no `ports` mapping, because the platform's reverse proxy routes to the
container on `LLMPROXY_PORT` (default 4000).

1. Point Coolify at the repository and select
   `docker-compose.coolify.yml`. Every supported `LLMPROXY_*` variable appears
   in the file as `${VAR:-default}`, so Coolify lists each one as an editable
   environment variable.
2. Set `POSTGRES_PASSWORD`. It feeds both the `db` service and the connection
   URL.
3. Deploy, and confirm the deployment answers on `/healthz`.
4. Configure the `LLMPROXY_OIDC_*` variables once the deployment is reachable
   over HTTPS.

Until step 4, the file defaults to local mode with
`LLMPROXY_ALLOW_NONLOCAL=1`, which means anyone who can reach the deployment
can use its keys.

The `/data` volume still holds the generated key secret and admin password
with Postgres. Alternatively pin `LLMPROXY_KEY_SECRET` and
`LLMPROXY_ADMIN_PASSWORD` through the environment.

## What the image does

| | |
|---|---|
| User | `llmproxy`, uid 1000; the process never runs as root |
| State | `/data`, owned by that user |
| Env defaults | `LLMPROXY_DATABASE_URL=/data/llmproxy.db`, `LLMPROXY_SECRET_FILE=/data/secret`, `LLMPROXY_ADMIN_PASSWORD_FILE=/data/admin-password` |
| Bind | `LLMPROXY_HOST=0.0.0.0`, `LLMPROXY_PORT=4000`, port 4000 exposed |
| Entrypoint | `llmproxy`, with `CMD ["serve"]`, so `docker run llmproxy key create ...` runs the CLI against the same `/data` |

Build it yourself with `just docker` (`docker build -t llmproxy .`).

## Where next

- [upgrades.md](upgrades.md) for backup and version pinning.
- [../guides/providers.md](../guides/providers.md) to register your first
  upstream.
- [../reference/configuration.md](../reference/configuration.md) for every
  environment variable.
